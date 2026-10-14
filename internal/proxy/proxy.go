package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/systemd"
)

// 默认位置。
const (
	// DefaultBinPath 是 wireproxy 二进制的安装位置。
	//
	// 放 /usr/local/bin 而不是 /usr/bin：后者是包管理器（dpkg）的地盘，
	// 我们放进去的东西既不受 dpkg 管理、又会和同名包冲突。
	DefaultBinPath = "/usr/local/bin/wireproxy"

	// DefaultUnitName 与上游的 `wireproxy.service` 刻意不同名。
	//
	// 同名会让"这台机器以前装过上游脚本"的情形变得难以诊断：
	// 两个来源都觉得自己在管这个单元，而 systemctl 只会认最后写入的那份。
	DefaultUnitName = "warpgo-proxy.service"

	// DefaultUnitDir 是 systemd 的单元目录。
	DefaultUnitDir = "/etc/systemd/system"

	// ConfFileName / StateFileName 落在 warpgo 的状态目录里
	// （`uninstall --purge` 要按名字认领它们，见 cmd/warpgo/uninstall.go）。
	ConfFileName  = "proxy.conf"
	StateFileName = "proxy.json"
)

// 环境变量：覆盖安装位置。
//
// 与 systemd.EnvUnitPath / EnvBinPath 同样的用途 —— 让单测与冒烟测试
// 能在临时目录里把"装 / 卸"跑一遍，而不必真的往 /usr/local/bin 里写东西。
const (
	EnvBinPath  = "WARPGO_PROXY_BIN"
	EnvUnitPath = "WARPGO_PROXY_UNIT"
)

// State 是"这个方案装成什么样"的记录。
//
// 它存在的意义是让 status / off / uninstall 不必去猜：端口、来源、版本
// 都是装的时候确定的，重新推导（比如从配置里猜端口）在某些场景下会猜错。
type State struct {
	Port     int    `json:"port"`
	Version  string `json:"version,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Source   string `json:"source,omitempty"`
	BinPath  string `json:"bin_path"`
	ConfPath string `json:"conf_path"`
	UnitPath string `json:"unit_path"`

	InstalledAt time.Time `json:"installed_at"`
}

// Manager 管理这套本地安装。
type Manager struct {
	BinPath   string
	UnitPath  string
	ConfPath  string
	StatePath string
	// Runner 可注入（测试用）；systemctl 与系统检查都走它。
	Runner systemd.Runner
}

// New 创建使用真实路径的 Manager。
func New() *Manager {
	dir := state.Dir()
	m := &Manager{
		BinPath:   DefaultBinPath,
		UnitPath:  filepath.Join(DefaultUnitDir, DefaultUnitName),
		ConfPath:  filepath.Join(dir, ConfFileName),
		StatePath: filepath.Join(dir, StateFileName),
	}
	if v := os.Getenv(EnvBinPath); v != "" {
		m.BinPath = v
	}
	if v := os.Getenv(EnvUnitPath); v != "" {
		m.UnitPath = v
	}
	return m
}

// Systemd 返回管着这个单元的 systemd Manager。
func (m *Manager) Systemd() *systemd.Manager {
	return &systemd.Manager{
		Name:   DefaultUnitName,
		Paths:  systemd.Paths{UnitPath: m.UnitPath, BinPath: m.BinPath},
		Runner: m.Runner,
	}
}

// 文件存在性。

// BinExists 报告二进制在不在。
func (m *Manager) BinExists() bool { _, err := os.Stat(m.BinPath); return err == nil }

// ConfExists 报告配置在不在。
func (m *Manager) ConfExists() bool { _, err := os.Stat(m.ConfPath); return err == nil }

// UnitExists 报告单元文件在不在。
func (m *Manager) UnitExists() bool { return m.Systemd().UnitExists() }

// Installed 报告"二进制 + 配置 + 单元"三者齐全。
//
// 三者缺一都不算装好：只有二进制是没配置，只有配置是没程序，
// 只有单元文件则 systemd 一启动就会失败。
func (m *Manager) Installed() bool {
	return m.BinExists() && m.ConfExists() && m.UnitExists()
}

// WriteConf 写入配置文件（0600：里面有私钥）。
func (m *Manager) WriteConf(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	text := Render(c)
	if err := state.WriteFileAtomic(m.ConfPath, []byte(text), state.FilePerm); err != nil {
		return fmt.Errorf("写入 %s: %w", m.ConfPath, err)
	}
	return nil
}

// ReadConfPort 从配置文件里读回端口。
func (m *Manager) ReadConfPort() (int, error) {
	b, err := os.ReadFile(m.ConfPath)
	if err != nil {
		return 0, fmt.Errorf("读取 %s: %w", m.ConfPath, err)
	}
	return ParsePort(string(b))
}

// RenderUnit 生成单元文件内容。
//
// 与保活单元的两处刻意差别：
//   - `Restart=always` + 较短的 RestartSec：wireproxy 是个前台进程，
//     它挂了就是"代理没了"，尽快拉起来比等一会儿更重要（它不会动系统网络，
//     所以重启的代价仅仅是几秒不可用）；
//   - 仍然**不写 ExecStop**：理由与保活单元一致，见 internal/systemd 的说明。
//
// wireproxy 用的是 systemd 自带的日志（stdout/stderr），所以这里不需要
// 重定向到文件，`journalctl -u warpgo-proxy` 就能看到它的话。
func (m *Manager) RenderUnit() string {
	var b strings.Builder
	b.WriteString("# 本文件由 `warpgo proxy install` 生成 —— 手工改动会在下次 install 时被覆盖。\n")
	b.WriteString("# 需要改端口就重跑 install（例如：warpgo proxy install --port 25344）。\n\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=warpgo WireProxy（WARP 的 socks5 出口）\n")
	b.WriteString("Documentation=https://github.com/misakacoo/warpgo\n")
	b.WriteString("Documentation=https://github.com/windtf/wireproxy\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s -c %s\n", m.BinPath, m.ConfPath)
	b.WriteString("# 它是纯用户态的代理进程，不碰接口与路由，所以重启是廉价的。\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=10s\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// WriteUnit 写入单元文件。
func (m *Manager) WriteUnit() error { return m.Systemd().WriteUnit(m.RenderUnit()) }

// 状态文件。

// Load 读取状态；文件不存在时返回 (nil, false, nil)。
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

// Port 返回当前应该用的端口：优先状态文件，其次配置文件，最后默认值。
//
// 三级回退的顺序是有理由的：状态文件是"上次装的时候用了什么"，
// 配置文件是"现在文件里写的是什么"，两者不一致时以配置文件为准更安全
// —— 因为它是服务实际读的东西。所以这里先读配置，失败再落到状态。
func (m *Manager) Port() int {
	if p, err := m.ReadConfPort(); err == nil {
		return p
	}
	if st, found, err := m.Load(); err == nil && found && st.Port > 0 {
		return st.Port
	}
	return DefaultPort
}

// ProxyURL 返回给探测用的 socks5 地址。
func (m *Manager) ProxyURL(port int) string {
	if port <= 0 {
		port = m.Port()
	}
	return fmt.Sprintf("socks5h://127.0.0.1:%d", port)
}

// Remove 把这套安装撤掉：停服务 → 删单元 → 删二进制（可选）→ 删配置与状态。
//
// 顺序是刻意的：**先停服务再删文件**。反过来做的话，正在运行的 wireproxy 会继续
// 占着端口，而 systemd 已经找不到单元文件了 —— 现场变成"端口被占、却没人能停它"。
//
// keepBinary 为真时保留二进制：它是几 MB 的下载产物，重装时能省一次网络往返。
//
// 三个 systemctl 动作的错误**刻意忽略**：未安装 / 未运行时它们会以非 0 退出，
// 而"卸载一个没装的东西"应当成功。真正的失败会由后面的文件检查暴露出来。
func (m *Manager) Remove(ctx context.Context, keepBinary bool) (removed []string, warn error) {
	mg := m.Systemd()

	// ① 停服务。单元文件可能已经不在、但进程还在跑，所以无条件停一次。
	_ = mg.Stop(ctx)
	_ = mg.Disable(ctx)
	_ = mg.ResetFailed(ctx)

	// ② 删单元文件 + daemon-reload。
	if m.UnitExists() {
		if err := mg.RemoveUnit(); err != nil {
			return removed, err
		}
		removed = append(removed, m.UnitPath)
		// reload 失败只记一条警告：单元文件已经删了，重启后现场自然一致。
		if err := mg.DaemonReload(ctx); err != nil {
			warn = err
		}
	}

	// ③ 删二进制。
	if !keepBinary && m.BinExists() {
		if err := os.Remove(m.BinPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("删除 %s: %w", m.BinPath, err)
		}
		removed = append(removed, m.BinPath)
	}

	// ④ 删配置与状态（配置里有私钥，留着没有意义）。
	for _, path := range []string{m.ConfPath, m.StatePath} {
		switch err := os.Remove(path); {
		case err == nil:
			removed = append(removed, path)
		case errors.Is(err, os.ErrNotExist):
		default:
			return removed, fmt.Errorf("删除 %s: %w", path, err)
		}
	}
	return removed, warn
}
