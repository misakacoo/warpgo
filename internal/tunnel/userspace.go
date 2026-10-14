package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
)

// 运行期位置与可覆盖项。
const (
	// DefaultRunDir 是运行期目录（tmpfs）。与锁、保险丝日志同一个目录。
	DefaultRunDir = "/run/warpgo"
	// EnvRunDir 可覆盖它为别的目录（测试用）。
	EnvRunDir = "WARPGO_RUN_DIR"

	// UAPIDir 是 wireguard-go 约定的 UAPI socket 目录。
	//
	// 它**不可配置**：wgctrl 找用户态设备时就是按这个固定路径去 glob 的，
	// 换个目录会让"配置得进去、但上层读不到"变成常态。
	// （wireguard-go 自己的 ipc 包也把它写成变量、只留给 Android 用 linker flag 改。）
	UAPIDir = "/var/run/wireguard"

	// EnvDaemonBin 覆盖守护进程用哪个可执行文件（测试与打包用）。
	EnvDaemonBin = "WARPGO_DAEMON_BIN"

	// daemonStartTimeout 是"拉起守护进程后等它就绪"的上限。
	//
	// 15 秒给得很宽：这一步只在真正需要新建守护进程时发生，
	// 而排队等待的常见原因是磁盘慢或进程被调度推迟 —— 太短的超时会把
	// "慢"误判成"起不来"，然后就是一次无谓的重建（会改变接口创建时间）。
	daemonStartTimeout = 15 * time.Second

	// daemonStopTimeout 是"等守护进程退出"的上限。
	daemonStopTimeout = 5 * time.Second
)

// RunDir 返回运行期目录。
func RunDir() string {
	if v := strings.TrimSpace(os.Getenv(EnvRunDir)); v != "" {
		return v
	}
	return DefaultRunDir
}

// UAPISocketPath 返回某个接口的 UAPI socket 路径。
func UAPISocketPath(ifName string) string {
	return filepath.Join(UAPIDir, ifName+".sock")
}

// DaemonStatePath 返回守护进程状态文件的位置。
func DaemonStatePath(ifName string) string {
	return filepath.Join(RunDir(), "userspace-"+ifName+".json")
}

// DaemonLogPath 返回守护进程日志的位置。
//
// 放在 tmpfs 里而不是 /var/log：它是排障用的短命日志，不值得往磁盘上写，
// 重启后也确实没有保留价值（那时设备已经不存在了）。
func DaemonLogPath(ifName string) string {
	return filepath.Join(RunDir(), "userspace-"+ifName+".log")
}

// DaemonState 是"谁在管这个接口"的权威记录，由拉起守护进程的一方写下。
//
// 为什么需要它：父进程必须能回答"现在跑着的那个守护进程，参数和我要的一致吗"。
// 进程活着但 reserved 已经变了（比如刚 rotate 过）时，只有重启它才生效；
// 而这个判断只能来自一份明确记录 —— 从外部是猜不出来的。
type DaemonState struct {
	PID      int                 `json:"pid"`
	IfName   string              `json:"ifname"`
	MTU      int                 `json:"mtu"`
	Reserved cloudflare.Reserved `json:"reserved"`
	Started  time.Time           `json:"started_at"`
}

// ReadDaemonState 读取状态文件；不存在或读不出内容时返回 found=false。
func ReadDaemonState(ifName string) (DaemonState, bool) {
	var st DaemonState
	raw, found, err := state.ReadFile(DaemonStatePath(ifName))
	if err != nil || !found {
		return st, false
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, false
	}
	return st, true
}

func writeDaemonState(st DaemonState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("编码用户态守护进程状态: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(RunDir(), 0o700); err != nil {
		return fmt.Errorf("创建 %s: %w", RunDir(), err)
	}
	return state.WriteFileAtomic(DaemonStatePath(st.IfName), data, state.FilePerm)
}

func removeDaemonState(ifName string) error {
	err := os.Remove(DaemonStatePath(ifName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 %s: %w", DaemonStatePath(ifName), err)
	}
	return nil
}

// processAlive 报告进程是否还活着。
//
// 僵尸算"已经死了"：父进程还没 wait 掉它时它仍然占着 PID，但已经不再干活，
// 这时候还认为它活着，会让"参数没变就不用重启"的判断永远成立 ——
// 于是隧道再也不会被修好。这类坑本项目在单实例锁上已经踩过一次。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// stat 的格式是 `pid (comm) state ...`，comm 里可能带空格与括号，
	// 所以状态位要从**最后一个** ')' 之后再数。
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z' && s[i+2] != 'X'
}

// isOurDaemon 核对 PID 上的进程确实是"管某个接口的 warpgo 用户态守护进程"。
//
// 状态文件里的 PID 可能早已被别的进程复用（机器重启、PID 回绕），
// 无脑 kill 会误伤无关进程 —— 所以杀之前必须核对身份，核对不了就不杀。
func isOurDaemon(pid int, ifName string) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	seenCmd, seenIf := false, false
	for _, a := range args {
		switch {
		case a == "userspace-daemon":
			seenCmd = true
		case a == ifName:
			seenIf = true
		}
	}
	return seenCmd && seenIf
}

// StopUserspaceDaemon 停掉某个接口的用户态守护进程，并清掉它的运行期痕迹。
//
// 幂等：没有守护进程时什么都不做（`down` / `uninstall` 会无条件调用它）。
func StopUserspaceDaemon(ifName string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if st, ok := ReadDaemonState(ifName); ok && processAlive(st.PID) {
		switch {
		case !isOurDaemon(st.PID, ifName):
			// 身份对不上就不杀 —— 宁可留下一个多余的进程，也不能误杀别人的。
			logf("PID %d 上的进程不是 warpgo 的用户态守护进程，不停止它（仅清理状态文件）", st.PID)
		default:
			if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("停止用户态守护进程 %d: %w", st.PID, err)
			} else {
				logf("已停止用户态守护进程 %d（接口 %s）", st.PID, ifName)
				waitGone(st.PID, daemonStopTimeout)
			}
		}
	}
	if err := removeDaemonState(ifName); err != nil {
		return err
	}
	// socket 通常由守护进程自己摘掉；被 SIGKILL 之类强杀时可能留下来，
	// 而残留的 socket 会让"下一次拉起"直接失败（unix socket in use）。
	if err := os.Remove(UAPISocketPath(ifName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 %s: %w", UAPISocketPath(ifName), err)
	}
	return nil
}

func waitGone(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// userspaceTunnel 是用户态后端（父进程侧）。
//
// 它自己**不持有** WireGuard 设备 —— 设备在守护进程里。父进程通过
// wgctrl（它按 UAPI socket 找到用户态设备）配置与读取它，于是
// "加地址 / 下发配置 / 读状态"三段与内核后端共用同一份代码。
type userspaceTunnel struct {
	ifName string
	client *wgctrl.Client
}

// NewUserspace 创建一个用户态后端。reserved 由 Ensure 的 Config 提供。
func NewUserspace(ifName string) (*userspaceTunnel, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("初始化 WireGuard 客户端: %w", err)
	}
	return &userspaceTunnel{ifName: ifName, client: c}, nil
}

func (u *userspaceTunnel) Backend() Backend { return BackendUserspace }

func (u *userspaceTunnel) Close() error { return u.client.Close() }

// Ensure 让用户态隧道达到期望状态。
//
// 与内核后端的分工差异只有一处：**接口不由我们创建**，而是守护进程建 TUN 时
// 产生的。所以这里先保证守护进程在跑（该拉就拉、参数变了就换），
// 之后的路（地址 / up / 下发配置）与内核后端完全一样。
func (u *userspaceTunnel) Ensure(ctx context.Context, cfg Config) (Change, error) {
	var ch Change
	name := cfg.IfName
	if name == "" {
		name = u.ifName
	}
	if name == "" {
		return ch, errors.New("用户态后端需要接口名")
	}

	started, err := u.ensureDaemon(ctx, name, cfg)
	if err != nil {
		return ch, err
	}
	ch.DaemonStarted = started
	// 守护进程新建 = 接口新建。用同一个字段表达，上层的"这次有没有换 IP"
	// 判断因此对两种后端都成立。
	ch.LinkCreated = started

	link, err := netlink.LinkByName(name)
	if err != nil {
		return ch, fmt.Errorf("用户态设备 %s 应当已经存在，但找不到它: %w", name, err)
	}

	added, up, err := applyAddrsAndUp(link, cfg)
	ch.AddrsAdded, ch.LinkUp = added, up
	if err != nil {
		return ch, err
	}

	matched, sent, err := ensureConfig(u.client, cfg)
	if err != nil {
		return ch, err
	}
	ch.ConfigMatched, ch.Configured = matched, sent
	return ch, nil
}

// ensureDaemon 保证有一个参数正确的守护进程在跑；返回这次是否新建了它。
func (u *userspaceTunnel) ensureDaemon(ctx context.Context, name string, cfg Config) (bool, error) {
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = netcfg.DefaultMTU
	}
	want := DaemonState{IfName: name, MTU: mtu, Reserved: cfg.Reserved}

	if cur, ok := ReadDaemonState(name); ok && processAlive(cur.PID) &&
		cur.MTU == want.MTU && cur.Reserved == want.Reserved {
		// 参数对上了，还要确认它**真的在服务**：只看进程在不在是不够的，
		// 进程可能正在收尾（socket 已经摘掉），这时往前走会得到一个
		// "接口在、却配不进去"的假象。
		if _, err := u.client.Device(name); err == nil {
			return false, nil
		}
	}
	// 走到这里说明：没有记录、进程已死、参数不一致、或者它在应答上已经不可靠。
	// 四种情况都按"先停干净再拉起"处理 —— 停是幂等的。
	if _, err := os.Stat(DaemonStatePath(name)); err == nil {
		if err := StopUserspaceDaemon(name, nil); err != nil {
			return true, err
		}
	} else if _, err := u.client.Device(name); err == nil {
		// 没有状态文件却有一个能应答的设备：可能是被手工清理过现场。
		// 我们无法证明它带着正确的 reserved，所以也重来一次 ——
		// "悄悄用着错的 reserved"比"多重建一次"危险得多。
		if err := StopUserspaceDaemon(name, nil); err != nil {
			return true, err
		}
	}

	if err := spawnDaemon(name, mtu, cfg.Reserved); err != nil {
		return true, err
	}
	if err := u.waitReady(ctx, name); err != nil {
		return true, err
	}
	return true, nil
}

// waitReady 等守护进程把 UAPI socket 挂起来并且真的能应答。
func (u *userspaceTunnel) waitReady(ctx context.Context, name string) error {
	sock := UAPISocketPath(name)
	deadline := time.Now().Add(daemonStartTimeout)
	var last error
	for {
		if _, err := os.Stat(sock); err == nil {
			if _, derr := u.client.Device(name); derr == nil {
				return nil
			} else {
				last = derr
			}
		} else {
			last = err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"用户态守护进程在 %s 内没有就绪（socket %s: %v）%s",
				daemonStartTimeout, sock, last, daemonLogTail(name))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// daemonLogTail 取守护进程日志的末尾几行，附在启动失败的原因后面。
//
// 没有它的话，用户看到的只是"15 秒内没有就绪" —— 而真正的原因（没有 /dev/net/tun、
// 没权限、被别的进程占了 socket……）全在守护进程的日志里，还得自己再找一遍。
func daemonLogTail(name string) string {
	raw, err := os.ReadFile(DaemonLogPath(name))
	if err != nil || len(raw) == 0 {
		return fmt.Sprintf("；日志 %s 为空或读不到", DaemonLogPath(name))
	}
	const keep = 600
	if len(raw) > keep {
		raw = raw[len(raw)-keep:]
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	for i := range lines {
		lines[i] = "\n    " + lines[i]
	}
	return "；守护进程日志（" + DaemonLogPath(name) + "）末尾：" + strings.Join(lines, "")
}

func (u *userspaceTunnel) TearDown(ctx context.Context) (bool, error) {
	// ① 删接口。删掉 TUN 会让 wireguard-go 的读循环拿到错误、进而自己关闭设备
	//    并退出进程 —— 这一条在当前内核上可靠，但不能指望它一定来得及。
	deleted, err := netcfg.DeleteLink(u.ifName)
	if err != nil {
		return deleted, err
	}
	// ② 收尾：停掉（万一还活着的）守护进程，清掉状态文件与残留 socket。
	if serr := StopUserspaceDaemon(u.ifName, nil); serr != nil {
		return deleted, serr
	}
	return deleted, nil
}

func (u *userspaceTunnel) Status(ctx context.Context) (Status, error) {
	return readStatus(u.client, u.ifName)
}

// DaemonRunning 报告某个接口的用户态守护进程当前是否在跑。
//
// 做成包级函数而不是方法：`tunnel.New` 返回的是 Tunnel 接口，
// 通过它拿不到具体后端的方法 —— 而 `status` 命令正是在那种场景下需要这个答案。
func DaemonRunning(ifName string) bool {
	st, ok := ReadDaemonState(ifName)
	return ok && processAlive(st.PID)
}

// spawnDaemon 以独立会话拉起用户态守护进程。
//
// 三个要点与保险丝进程完全一致：setsid（脱离发起变更的那个会话，否则 SSH 一断
// 隧道就跟着没了）、不 Wait（它必须比我们活得久）、日志进文件（那一刻已经
// 没有任何终端可以给它了）。
func spawnDaemon(ifName string, mtu int, r cloudflare.Reserved) error {
	exe := strings.TrimSpace(os.Getenv(EnvDaemonBin))
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("定位自身可执行文件: %w", err)
		}
		exe = self
	}
	if err := os.MkdirAll(RunDir(), 0o700); err != nil {
		return fmt.Errorf("创建 %s: %w", RunDir(), err)
	}
	logFile, err := os.OpenFile(DaemonLogPath(ifName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开用户态守护进程日志: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, DaemonArgs(ifName, mtu, r)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("拉起用户态守护进程: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	return writeDaemonState(DaemonState{
		PID:      pid,
		IfName:   ifName,
		MTU:      mtu,
		Reserved: r,
		Started:  time.Now().UTC(),
	})
}

// DaemonArgs 返回拉起用户态守护进程所需的完整参数（"userspace-daemon …"）。
//
// 单独导出是为了让命令行与这里共用同一份拼装逻辑 ——
// 两处各写一遍，迟早会出现"手工跑能起来、程序拉起来的却少了某个参数"。
func DaemonArgs(ifName string, mtu int, r cloudflare.Reserved) []string {
	return []string{
		"userspace-daemon",
		"--ifname", ifName,
		"--mtu", strconv.Itoa(mtu),
		"--reserved", ReservedArg(r),
	}
}

// ReservedArg 把 reserved 编码成命令行参数（"36,212,144"）。
//
// 用十进制三元组而不是 base64：`ps` 里一眼能看出它是哪三个字节，
// 排障时不必再手工解一次码。
func ReservedArg(r cloudflare.Reserved) string {
	return fmt.Sprintf("%d,%d,%d", r[0], r[1], r[2])
}

// ParseReservedArg 解析 ReservedArg 写出的形式。
func ParseReservedArg(s string) (cloudflare.Reserved, error) {
	var r cloudflare.Reserved
	s = strings.TrimSpace(s)
	if s == "" {
		return r, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return r, fmt.Errorf("reserved 应为 3 个十进制数，实得 %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return r, fmt.Errorf("reserved 第 %d 项 %q 不是整数: %w", i+1, p, err)
		}
		if n < 0 || n > 255 {
			return r, fmt.Errorf("reserved 第 %d 项 %d 超出 0-255", i+1, n)
		}
		r[i] = uint8(n)
	}
	return r, nil
}
