// Package cfclient 编排 Cloudflare **官方**的 WARP Linux 客户端（`cloudflare-warp` 包）。
//
// 这一层刻意"只做编排"：装包、切模式、开关、加减策略规则、卸载。
// 我们不重写 `warp-svc`，也不碰它的实现 —— 它由 Cloudflare 维护，
// 我们能做的只是把它放到正确的位置、再用正确的方式驱动它。
//
// 与 internal/proxy（WireProxy）的分工很值得对照：
//   - proxy 引入的是一个**第三方二进制**，配置由我们生成、由我们校验；
//   - cfclient 装的是**官方 .deb**，配置由 `warp-cli` 自己管理。
//     两者都是"把 WARP 变成另一种出口形态"，区别只在控制权在谁手里。
//
// 本包的边界（与其它模块一致）：所有外部命令都经 `systemd.Runner` 注入，
// 所有"读输出再判断"的逻辑都抽成纯函数 —— 于是解析与决策能脱离 root
// 与真实客户端做单测，而真正碰系统的只剩薄薄一层。
package cfclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/systemd"
)

// 默认位置与名字。
const (
	// UnitName 是官方客户端的守护进程单元。
	//
	// 它是**包管理器**装的单元（`/lib/systemd/system/warp-svc.service`），
	// 不是我们渲染的 —— 我们只调用 systemctl，绝不去改那个文件
	// （改它会在下次 apt 升级时被覆盖，而且那属于"动别人的包"）。
	UnitName = "warp-svc.service"

	// SvcSocket 是 warp-cli 与 warp-svc 之间的 AF_UNIX socket。
	//
	// warp-cli 只会连这个路径，所以"服务起没起"最可靠的判据是它存不存在，
	// 而不是 systemctl is-active（进程活着但 socket 还没建出来时，
	// 任何 warp-cli 调用都会失败）。
	SvcSocket = "/run/cloudflare-warp/warp_service"

	// ClientIfName 是客户端在 warp 模式下创建的接口名。
	ClientIfName = "CloudflareWARP"

	// APTListPath / KeyringPath 是 apt 源的落点。
	//
	// 记下来是为了卸载时**只删我们自己建的那一份**：如果这台机器上
	// 原本就有官方源（用户手工配过），删掉它会把用户的配置一起带走。
	APTListPath = "/etc/apt/sources.list.d/cloudflare-client.list"
	KeyringPath = "/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg"

	// PubkeyURL / RepoURL 是官方发布的密钥与仓库地址。
	PubkeyURL = "https://pkg.cloudflareclient.com/pubkey.gpg"
	RepoURL   = "https://pkg.cloudflareclient.com/"

	// DefaultSocksPort 与上游一致：官方客户端的默认 socks5 端口。
	DefaultSocksPort = 40000

	// MinSocksPort / MaxSocksPort 是允许的端口范围。
	//
	// 下限取 1024（避开特权端口）：客户端以 root 跑，绑 80 之类的端口
	// 会让"代理端口"与"系统服务端口"混在一起，出问题时很难分清是谁的。
	MinSocksPort = 1024
	MaxSocksPort = 65535

	// StateFileName 记录"这台机器上我们做过什么"。
	//
	// 它不记录客户端的状态（那是 warp-cli 的职责），只记录**我们的动作**：
	// 源文件与密钥环是不是我们建的、drop-in 是不是我们写的。
	// 卸载时据此决定删什么 —— 与 `uninstall --purge` 的"只删自己认识的文件"
	// 是同一条原则。
	StateFileName = "client.json"
)

// 环境变量：覆盖路径（单测与验收脚本用）。
const (
	EnvSourceList = "WARPGO_CLIENT_APT_LIST"
	EnvKeyring    = "WARPGO_CLIENT_KEYRING"
	EnvDropIn     = "WARPGO_CLIENT_DROPIN"
	EnvStatePath  = "WARPGO_CLIENT_STATE"
	EnvCLI        = "WARPGO_CLIENT_CLI"
)

// Runner 执行一条外部命令，签名与 systemd.Runner 一致。
//
// 直接复用 systemd 的那个类型，而不是再定义一个：两者语义完全相同
// （退出码非零不算错误），而"注入点"越少，测试装配就越简单。
type Runner = systemd.Runner

// ConnState 是客户端上报的连接状态。
type ConnState string

const (
	ConnConnected    ConnState = "connected"
	ConnDisconnected ConnState = "disconnected"
	ConnConnecting   ConnState = "connecting"
	// ConnNoRegistration 表示客户端装好了但还没注册（`Registration missing`）。
	ConnNoRegistration ConnState = "no_registration"
	ConnUnknown        ConnState = "unknown"
)

// Mode 是客户端的工作模式。
type Mode string

const (
	// ModeWarp 让客户端创建 `CloudflareWARP` 接口接管流量。
	ModeWarp Mode = "warp"
	// ModeProxy 只在本机开一个 socks5 入口，不动系统路由。
	ModeProxy Mode = "proxy"
)

// ParseModeName 解析命令行给的模式名。
func ParseModeName(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warp", "interface", "iface", "l":
		return ModeWarp, nil
	case "proxy", "socks5", "socks", "c":
		return ModeProxy, nil
	default:
		return "", fmt.Errorf("不认识的模式 %q（可用 warp / proxy）", s)
	}
}

// 解析用的正则。
//
// **首选是 `-j` 的 JSON**（见 Query 与 Parse*JSON）：结构化输出不会被排版变化打垮。
// 这些正则只服务"没有 -j 的老版本"，因此写得尽量宽容 —— 真实形态见过两种：
//
//	(user set)	Mode: WarpProxy on port 40000     ← 2026.8.2110.0
//	Mode: Warp                                        ← 更早的版本
//
// 前者前面多了一列"取值来源"，模式名后面还跟着端口，所以不能假设
// "冒号后面只有一个词"、也不能假设行首就是 `Mode`。
var (
	reModeValue = regexp.MustCompile(`(?i)\bmode:\s*(.+?)\s*$`)
	rePortWord  = regexp.MustCompile(`(?i)\bport\s+(\d+)`)
	rePortValue = regexp.MustCompile(`(?i)^\s*proxy port:\s*(\d+)\s*$`)
	reTypeValue = regexp.MustCompile(`(?i)^\s*(?:account\s+)?type:\s*(\S+)\s*$`)
)

// connStateFromWord 把客户端给出的状态词归一成 ConnState。
//
// 判定顺序是有讲究的：`Disconnected` 里也含 `connected`，
// 所以必须先匹配更具体的词。`Registration missing` 优先于一切 ——
// 那是"还没注册"，与"注册了但没连"是两件不同的事，后续动作也不同。
func connStateFromWord(s string) ConnState {
	low := strings.ToLower(strings.TrimSpace(s))
	switch {
	// 两种拼写都要认：文本输出是 `Registration missing`，
	// 而 `-j` 给的是驼峰 `RegistrationMissing`（中间没有空格）。
	case strings.Contains(low, "registration missing"), strings.Contains(low, "registrationmissing"):
		return ConnNoRegistration
	case strings.Contains(low, "connecting"):
		return ConnConnecting
	case strings.Contains(low, "disconnected"):
		return ConnDisconnected
	case strings.Contains(low, "connected"):
		return ConnConnected
	default:
		return ConnUnknown
	}
}

// modeFromWord 把客户端给出的模式词归一成 Mode。
//
// `proxy` 必须先判：`WarpProxy` 里含 `Warp`。
// `tunnel_only` / `warp+doh` / `warp+dot` 都算 warp（它们都建隧道），
// 而 `doh` / `dot` 不建隧道 —— 那两种对我们来说就是"不认识"。
func modeFromWord(s string) (Mode, bool) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(v, "proxy"):
		return ModeProxy, true
	case strings.Contains(v, "warp"), strings.Contains(v, "tunnel"):
		return ModeWarp, true
	default:
		return "", false
	}
}

// ParseConnState 从 `warp-cli status` 的**文本**输出里读出连接状态。
func ParseConnState(out string) ConnState { return connStateFromWord(out) }

// ParseMode 从 `warp-cli settings` 的**文本**输出里读出工作模式。
func ParseMode(out string) (Mode, bool) {
	for _, line := range strings.Split(out, "\n") {
		m := reModeValue.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		return modeFromWord(m[1])
	}
	return "", false
}

// ParseProxyPort 从 `warp-cli settings` 的**文本**输出里读出 socks5 端口。
//
// 两种形态都要认：新版的端口跟在模式名后面（`Mode: WarpProxy on port 40000`），
// 老版单独一行（`Proxy port: 40000`）。
func ParseProxyPort(out string) (int, bool) {
	for _, line := range strings.Split(out, "\n") {
		if m := reModeValue.FindStringSubmatch(line); m != nil {
			if p := rePortWord.FindStringSubmatch(m[1]); p != nil {
				if n, err := strconv.Atoi(p[1]); err == nil {
					return n, true
				}
			}
		}
		if m := rePortValue.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// ParseAccountType 从 `warp-cli registration show` 的**文本**输出里读出账户类型。
//
// 取值形如 `Free` / `Limited`（= WARP+）/ `Teams`。读不出来就返回空串 ——
// 它只用于显示，不该因为一个措辞变化就让整条命令失败。
func ParseAccountType(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if m := reTypeValue.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// `-j` 的结构化输出（首选）
// ---------------------------------------------------------------------------

// ParseStatusJSON 解析 `warp-cli -j status`：`{"status":"Connected","reason":…}`。
func ParseStatusJSON(b []byte) (ConnState, bool) {
	var v struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(b, &v); err != nil || strings.TrimSpace(v.Status) == "" {
		return ConnUnknown, false
	}
	return connStateFromWord(v.Status), true
}

// ParseSettingsJSON 解析 `warp-cli -j settings`：
// `{"settings":{"operation_mode":"proxy","proxy_port":40000,…}}`。
func ParseSettingsJSON(b []byte) (Mode, int, bool) {
	var v struct {
		Settings struct {
			OperationMode string `json:"operation_mode"`
			ProxyPort     int    `json:"proxy_port"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", 0, false
	}
	m, ok := modeFromWord(v.Settings.OperationMode)
	if !ok {
		return "", 0, false
	}
	return m, v.Settings.ProxyPort, true
}

// ParseRegistrationJSON 解析 `warp-cli -j registration show`：
// `{"account":{"type":"free",…},…}`。
func ParseRegistrationJSON(b []byte) (string, bool) {
	var v struct {
		Account struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Account.Type == "" {
		return "", false
	}
	return v.Account.Type, true
}

// AccountLabel 把账户类型变成界面上的写法（Limited = WARP+，与上游一致）。
//
// 大小写不敏感：`-j` 给的是小写 `free`，文本模式给的是 `Free`。
func AccountLabel(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "":
		return ""
	case "limited":
		return "WARP+"
	case "free":
		return "Free"
	case "team", "teams":
		return "Teams"
	default:
		return t
	}
}

// Status 是一次聚合查询的结果。
type Status struct {
	CLIPresent     bool      `json:"cli_present"`
	ServiceActive  bool      `json:"service_active"`
	ServiceEnabled bool      `json:"service_enabled"`
	Conn           ConnState `json:"connection"`
	Mode           Mode      `json:"mode,omitempty"`
	Account        string    `json:"account,omitempty"`
	SocksPort      int       `json:"socks_port,omitempty"`
	InterfaceUp    bool      `json:"interface_up"`
	SocksListening bool      `json:"socks_listening"`
}

// Connected 报告客户端是否已经连上。
func (s Status) Connected() bool { return s.Conn == ConnConnected }

// Manager 管理这套安装。
type Manager struct {
	// Runner 可注入（测试用）；所有 warp-cli / apt / systemctl 调用都走它。
	Runner Runner

	// 路径（生产走默认值，测试与验收脚本可用环境变量覆盖）。
	SourceListPath string
	KeyringPath    string
	DropInPath     string
	StatePath      string

	// CLI 是 warp-cli 的命令名（测试可换成假实现）。
	CLI string

	// Fetcher 下载一个 URL 的内容（测试可注入假实现）。
	// 留空则用内置的 HTTP 下载器 —— 刻意不依赖 curl/wget：
	// 精简安装的 Debian 上它们不一定在，而"为了装客户端先装 curl"很别扭。
	Fetcher Fetcher
}

// New 创建使用真实路径的 Manager。
func New() *Manager {
	m := &Manager{
		SourceListPath: APTListPath,
		KeyringPath:    KeyringPath,
		DropInPath:     DropInDir + "/" + DropInFile,
		StatePath:      filepath.Join(state.Dir(), StateFileName),
		CLI:            "warp-cli",
	}
	if v := os.Getenv(EnvSourceList); v != "" {
		m.SourceListPath = v
	}
	if v := os.Getenv(EnvKeyring); v != "" {
		m.KeyringPath = v
	}
	if v := os.Getenv(EnvDropIn); v != "" {
		m.DropInPath = v
	}
	if v := os.Getenv(EnvStatePath); v != "" {
		m.StatePath = v
	}
	if v := os.Getenv(EnvCLI); v != "" {
		m.CLI = v
	}
	return m
}

// Service 返回管着 warp-svc 的 systemd Manager。
//
// 复用 systemd.Manager（它的 systemctl 动作与单元名无关）——
// 一套代码管所有单元，出错的地方就少一处。
func (m *Manager) Service() *systemd.Manager {
	return &systemd.Manager{Name: UnitName, Runner: m.Runner}
}

func (m *Manager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return systemd.ExecRunner
}

// run 执行一条命令并返回 (输出, 退出码)。
func (m *Manager) run(ctx context.Context, name string, args ...string) (string, int, error) {
	return m.runner()(ctx, name, args...)
}

// cli 调用 warp-cli。所有调用都带 `--accept-tos`：没有它，
// 新装的客户端会停在"请接受服务条款"上，所有子命令都以非 0 退出，
// 而报错信息并不直白（看起来像"命令不存在"）。
func (m *Manager) cli(ctx context.Context, args ...string) (string, int, error) {
	full := append([]string{"--accept-tos"}, args...)
	return m.run(ctx, m.CLI, full...)
}

// CLIAvailable 报告 warp-cli 是否可执行。
//
// 用 `command -v` 的等价物（在 PATH 里找）而不是直接跑 `warp-cli --version`：
// 后者在客户端装好但服务没起时也可能失败，那会把"装了"误判成"没装"。
func (m *Manager) CLIAvailable() bool {
	if strings.ContainsAny(m.CLI, `/\`) {
		fi, err := os.Stat(m.CLI)
		return err == nil && !fi.IsDir()
	}
	_, err := exec.LookPath(m.CLI)
	return err == nil
}

// Installed 报告客户端是否已安装（以 warp-cli 存在为准）。
func (m *Manager) Installed() bool { return m.CLIAvailable() }

// Query 聚合查询一次客户端状态。
//
// 每一项都独立容错：服务没起时 warp-cli 全都会失败，而那时我们仍然希望
// 报告出"CLI 在、服务没起"这个更有用的结论，而不是整条命令报错。
func (m *Manager) Query(ctx context.Context) (*Status, error) {
	st := &Status{CLIPresent: m.CLIAvailable()}

	svc := m.Service()
	if v, err := svc.IsActive(ctx); err == nil {
		st.ServiceActive = v
	}
	if v, err := svc.IsEnabled(ctx); err == nil {
		st.ServiceEnabled = v
	}
	st.InterfaceUp = interfaceExists(ClientIfName)

	if !st.CLIPresent {
		return st, nil
	}

	// 服务没起时不必再问 —— warp-cli 只会超时或报连不上，
	// 那既浪费时间，也会把"服务没起"这个根因淹在噪音里。
	if !st.ServiceActive {
		return st, nil
	}

	// 三项查询都**先试 `-j`（结构化）再退回文本**。
	//
	// 为什么以 JSON 为主：真实形态里 `settings` 的输出是
	// `(user set)\tMode: WarpProxy on port 40000` —— 带一列"取值来源"、
	// 值后面还跟着端口，靠正则刮文本必然被下一个版本打垮。
	// 而 `-j` 给的是 `{"settings":{"operation_mode":"proxy","proxy_port":40000}}`，
	// 字段名是契约的一部分，比排版稳得多。
	// 保留文本兜底只是为了"老版本没有 -j"这一种情况。
	if out, _, err := m.cli(ctx, "-j", "status"); err == nil {
		if c, ok := ParseStatusJSON([]byte(out)); ok {
			st.Conn = c
		}
	}
	if st.Conn == ConnUnknown {
		if out, _, err := m.cli(ctx, "status"); err == nil {
			st.Conn = ParseConnState(out)
		}
	}

	if out, _, err := m.cli(ctx, "-j", "settings"); err == nil {
		if mode, port, ok := ParseSettingsJSON([]byte(out)); ok {
			st.Mode, st.SocksPort = mode, port
		}
	}
	if st.Mode == "" {
		if out, _, err := m.cli(ctx, "settings"); err == nil {
			if mode, ok := ParseMode(out); ok {
				st.Mode = mode
			}
			if p, ok := ParseProxyPort(out); ok {
				st.SocksPort = p
			}
		}
	}

	if out, _, err := m.cli(ctx, "-j", "registration", "show"); err == nil {
		if t, ok := ParseRegistrationJSON([]byte(out)); ok {
			st.Account = t
		}
	}
	if st.Account == "" {
		if out, _, err := m.cli(ctx, "registration", "show"); err == nil {
			st.Account = ParseAccountType(out)
		}
	}

	// 端口监听只看"模式是不是 proxy"，不看"端口号读没读出来"：
	// 读不出端口时也该用默认值去探一次 —— 否则一台正在跑的代理
	// 会因为一个解析失败而报告成"没在监听"。
	if st.Mode == ModeProxy {
		port := st.SocksPort
		if port == 0 {
			port = DefaultSocksPort
		}
		st.SocksListening = portListening(port)
	}
	return st, nil
}

// 常用动作。

// SetMode 切换工作模式。
func (m *Manager) SetMode(ctx context.Context, mode Mode) error {
	out, code, err := m.cli(ctx, "mode", string(mode))
	if err != nil {
		return fmt.Errorf("执行 warp-cli mode %s: %w", mode, err)
	}
	if code != 0 {
		return fmt.Errorf("warp-cli mode %s 退出码 %d: %s", mode, code, strings.TrimSpace(out))
	}
	return nil
}

// SetProxyPort 设置 socks5 端口。
func (m *Manager) SetProxyPort(ctx context.Context, port int) error {
	out, code, err := m.cli(ctx, "proxy", "port", strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("执行 warp-cli proxy port: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("warp-cli proxy port %d 退出码 %d: %s", port, code, strings.TrimSpace(out))
	}
	return nil
}

// Connect 连上 WARP。
func (m *Manager) Connect(ctx context.Context) error {
	out, code, err := m.cli(ctx, "connect")
	if err != nil {
		return fmt.Errorf("执行 warp-cli connect: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("warp-cli connect 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// Disconnect 断开。
func (m *Manager) Disconnect(ctx context.Context) error {
	out, code, err := m.cli(ctx, "disconnect")
	if err != nil {
		return fmt.Errorf("执行 warp-cli disconnect: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("warp-cli disconnect 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// NewRegistration 注册一台新设备。
func (m *Manager) NewRegistration(ctx context.Context) error {
	out, code, err := m.cli(ctx, "registration", "new")
	if err != nil {
		return fmt.Errorf("执行 warp-cli registration new: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("warp-cli registration new 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// DeleteRegistration 注销本机注册。
func (m *Manager) DeleteRegistration(ctx context.Context) error {
	out, code, err := m.cli(ctx, "registration", "delete")
	if err != nil {
		return fmt.Errorf("执行 warp-cli registration delete: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("warp-cli registration delete 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// DisableQLog 关掉隧道 qlog 日志。
//
// 它是官方客户端默认开启的调试日志，会在 /var/lib/cloudflare-warp 下持续写盘。
// 上游也关它 —— 服务器上没人看那些文件，只是白占空间。
// 失败不算错（老版本没有这个子命令），调用方只记一条 debug。
func (m *Manager) DisableQLog(ctx context.Context) error {
	_, code, err := m.cli(ctx, "debug", "qlog", "disable")
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("warp-cli debug qlog disable 退出码 %d", code)
	}
	return nil
}

// 等待类。

// WaitForService 等服务把 AF_UNIX socket 建出来。
//
// 判据是 socket 文件存在，不是 systemctl is-active：进程活着但 socket
// 还没建好时，任何 warp-cli 调用都会失败，而那时 is-active 已经是 active。
func (m *Manager) WaitForService(ctx context.Context, timeout time.Duration) error {
	return waitUntil(ctx, timeout, 500*time.Millisecond, func() bool {
		_, err := os.Stat(SvcSocket)
		return err == nil
	}, fmt.Sprintf("等待 %s 就绪", SvcSocket))
}

// WaitForInterface 等 `CloudflareWARP` 接口出现（warp 模式）。
func (m *Manager) WaitForInterface(ctx context.Context, timeout time.Duration) error {
	return waitUntil(ctx, timeout, 500*time.Millisecond, func() bool {
		return interfaceExists(ClientIfName)
	}, fmt.Sprintf("等待接口 %s 出现", ClientIfName))
}

// WaitForSocks5 等 socks5 端口开始监听（proxy 模式）。
func (m *Manager) WaitForSocks5(ctx context.Context, port int, timeout time.Duration) error {
	return waitUntil(ctx, timeout, 500*time.Millisecond, func() bool {
		return portListening(port)
	}, fmt.Sprintf("等待 socks5 端口 %d 开始监听", port))
}

// waitUntil 轮询 cond 直到为真或超时。
//
// 每一轮都检查 ctx：调用方给的是带超时的 ctx，于是"用户 Ctrl-C"与
// "等超时"走同一条路径，不会出现"取消了还在傻等"。
func waitUntil(ctx context.Context, timeout, interval time.Duration, cond func() bool, what string) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 超时（%s）", what, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// 状态文件。

// State 记录"我们在这台机器上做过什么"。
type State struct {
	Mode    Mode   `json:"mode,omitempty"`
	Port    int    `json:"port,omitempty"`
	DropIn  string `json:"drop_in,omitempty"`
	Keyring bool   `json:"keyring_created,omitempty"`
	Source  bool   `json:"source_created,omitempty"`

	InstalledAt time.Time `json:"installed_at"`
}

// Load 读取状态；不存在时返回 (nil, false, nil)。
func (m *Manager) Load() (*State, bool, error) {
	raw, found, err := state.ReadFile(m.StatePath)
	if err != nil || !found {
		return nil, false, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, false, fmt.Errorf("解析 %s: %w", m.StatePath, err)
	}
	return &st, true, nil
}

// SaveState 写入状态。
func (m *Manager) SaveState(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("编码状态: %w", err)
	}
	data = append(data, '\n')
	if err := state.WriteFileAtomic(m.StatePath, data, state.FilePerm); err != nil {
		return fmt.Errorf("保存 %s: %w", m.StatePath, err)
	}
	return nil
}

// RemoveState 删除状态文件（不存在不算错）。
func (m *Manager) RemoveState() error {
	if err := os.Remove(m.StatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 %s: %w", m.StatePath, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 主机侧的小工具（刻意用标准库，不引 netlink）
// ---------------------------------------------------------------------------

// ValidatePort 检查 socks5 端口是否可用。
func ValidatePort(port int) error {
	if port < MinSocksPort || port > MaxSocksPort {
		return fmt.Errorf("socks5 端口 %d 不在可用范围 %d-%d 内", port, MinSocksPort, MaxSocksPort)
	}
	return nil
}

// interfaceExists 报告某个网络接口是否已经存在。
//
// 用 net.InterfaceByName 而不是 netlink：这里只需要"在不在"，
// 而标准库版本在任何平台都能编译（netlink 只在 Linux 上有意义）。
func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// portListening 报告本机是否有进程在监听该 TCP 端口。
//
// 判据是"能不能连上"而不是解析 `ss` 的输出：后者要解析文本格式，
// 而不同版本的列宽不同（这正是 M7 验收里踩过的坑）。
func portListening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
