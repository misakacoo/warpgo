package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/health"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/proxy"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
)

// proxyTimeout 覆盖"下载 + 校验 + 自检 + 启动"。
//
// 下载那一段是最不可控的（GitHub 在部分线路上很慢），所以给得比别的命令宽。
const proxyTimeout = 6 * time.Minute

// proxyVerifyAttempts / proxyVerifyGap 是"起好之后经它实测"的重试参数。
//
// WireGuard 的首次握手要几百毫秒到几秒，而刚写完配置就立刻探测往往会探到
// 一个还没握上手的状态。重试几次比"一次不通就判定失败"准确得多。
const (
	proxyVerifyAttempts = 4
	proxyVerifyGap      = 2 * time.Second
	proxyProbeTimeout   = 6 * time.Second
)

// cmdProxy 实现菜单 `w`（安装）与 `y`（开关）。
func (a *app) cmdProxy(args []string) int {
	if len(args) == 0 {
		a.log.Errorf("%s", msg.T(msg.KeyProxyUsage))
		return exitError
	}
	switch args[0] {
	case "install":
		return a.cmdProxyInstall(args[1:])
	case "uninstall":
		return a.cmdProxyUninstall(args[1:])
	case "on":
		return a.cmdProxyOn(args[1:])
	case "off":
		return a.cmdProxyOff(args[1:])
	case "status":
		return a.cmdProxyStatus(args[1:])
	default:
		a.log.Errorf("%s", msg.Tf(msg.KeyUnknownCm, "proxy "+args[0]))
		a.log.Errorf("%s", msg.T(msg.KeyProxyUsage))
		return exitError
	}
}

// cmdProxyInstall 装好 WireProxy：取二进制 → 写配置 → 自检 → 建单元 → 启动 → 实测。
func (a *app) cmdProxyInstall(args []string) int {
	var (
		port       int
		mtu        int
		version    string
		dlURL      string
		sha256Want string
		from       string
		noStart    bool
	)
	opts := newOptionSet().
		Int("--port", &port).
		Int("--mtu", &mtu).
		Str("--version", &version).
		Str("--url", &dlURL).
		Str("--sha256", &sha256Want).
		Str("--from", &from).
		Bool("--no-start", &noStart)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	// 装的是"开机自启、并以 root 跑第三方二进制"的东西，非 Debian 直接拒绝。
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyProxyNotDebian, system.PrettyName()))
		return exitNotDebian
	}
	if port == 0 {
		port = proxy.DefaultPort
	}
	if err := proxy.ValidatePort(port); err != nil {
		return a.fail(err)
	}
	if mtu == 0 {
		mtu = netcfg.DefaultMTU
	}

	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}
	mgr := proxy.New()

	// 端口冲突检查。要点是**分清"是不是我们自己"**：
	// 重复 install 是很自然的动作（改参数、修现场），而那时端口必然被我们自己的
	// wireproxy 占着 —— 把它当冲突会让"重装"退化成"必须先手动停服务"的两步操作。
	// 只有"占用者不是我们"才真的该拒绝（socks5 是绑不上就起不来）。
	installed := mgr.Installed()
	active := false
	if installed {
		active, _ = mgr.Systemd().IsActive(context.Background())
	}
	if portConflict(portBusy(port), installed, active, mgr.Port(), port) {
		a.log.Errorf("%s", msg.Tf(msg.KeyProxyPortBusy, port))
		return exitError
	}

	ctx, cancel := context.WithTimeout(context.Background(), proxyTimeout)
	defer cancel()

	// ① 取二进制。校验和的强制策略在 proxy.Installer 里，这里只负责转述。
	if from != "" {
		// 离线安装是一个**不同的事实**（没有联网、也没有发布页清单可核对），
		// 所以单独说一句，而不是混在"来源 local"这种字段里。
		a.log.Infof("%s", msg.Tf(msg.KeyProxyFromLocal, from))
	}
	inst := &proxy.Installer{
		Version: version,
		URL:     dlURL,
		SHA256:  sha256Want,
		From:    from,
		Log:     func(format string, args ...any) { a.log.Debugf(format, args...) },
	}
	info, err := inst.Fetch(ctx, mgr.BinPath)
	if err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyBinary, mgr.BinPath, info.Version, info.Source, shortSHA(info.SHA256)))

	// ② 它真的能跑吗（架构不对只有执行才会暴露）。
	ver, err := proxy.BinaryVersion(ctx, mgr.BinPath)
	if err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyVersion, ver))

	// ③ 写配置。
	cfg := proxy.Config{
		Addrs:         acct.Addresses(),
		MTU:           mtu,
		PrivateKey:    acct.PrivateKey,
		PeerPublicKey: acct.PeerPublicKey,
		Endpoint:      acct.PreferredEndpoint(),
		DNS:           proxyDNS(acct),
		Port:          port,
		Note:          acct.ID,
	}
	if err := mgr.WriteConf(cfg); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyConfWritten, mgr.ConfPath))

	// ④ 用 wireproxy 自己的 -n 自检配置。
	if err := proxy.ConfigTest(ctx, mgr.BinPath, mgr.ConfPath); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.T(msg.KeyProxyConfigOK))

	// ⑤ 单元 + 启动。
	if err := mgr.WriteUnit(); err != nil {
		return a.fail(err)
	}
	mg := mgr.Systemd()
	if err := mg.DaemonReload(ctx); err != nil {
		return a.fail(err)
	}
	if err := mg.Enable(ctx); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyEnabled, proxy.DefaultUnitName))

	st := &proxy.State{
		Port:        port,
		Version:     info.Version,
		SHA256:      info.SHA256,
		Source:      info.URL,
		BinPath:     mgr.BinPath,
		ConfPath:    mgr.ConfPath,
		UnitPath:    mgr.UnitPath,
		InstalledAt: time.Now().UTC(),
	}
	if err := mgr.SaveState(st); err != nil {
		return a.fail(err)
	}

	// WARP 的 reserved 在 WireProxy 模式下**发不出去**。这不是缺陷而是事实，
	// 必须在动手之前就说清楚 —— 需要它的地区会表现为"配置都对、就是不通"。
	if acct.HasReserved() {
		a.log.Warnf("%s", msg.Tf(msg.KeyProxyReserved, acct.Reserved))
	}

	if noStart {
		a.log.Infof("%s", msg.Tf(msg.KeyProxyNoStart, proxy.DefaultUnitName))
		return exitOK
	}
	if err := mg.Restart(ctx); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyStarted, proxy.DefaultUnitName))

	if code := a.verifyProxy(ctx, mgr, acct, port); code != exitOK {
		return code
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyInstallDone, mgr.ProxyURL(port), port))
	return exitOK
}

// cmdProxyOn 启动 WireProxy（菜单 `y`）。
func (a *app) cmdProxyOn(args []string) int {
	if len(args) > 0 {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, args[0])))
	}
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyProxyNotDebian, system.PrettyName()))
		return exitNotDebian
	}
	mgr := proxy.New()
	if !mgr.Installed() {
		a.log.Errorf("%s", msg.T(msg.KeyProxyNotInstalled))
		return exitError
	}

	ctx, cancel := context.WithTimeout(context.Background(), proxyTimeout)
	defer cancel()

	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}
	mg := mgr.Systemd()
	if err := mg.Start(ctx); err != nil {
		return a.fail(err)
	}
	port := mgr.Port()
	a.log.Infof("%s", msg.Tf(msg.KeyProxyStarted, proxy.DefaultUnitName))

	if acct.HasReserved() {
		a.log.Warnf("%s", msg.Tf(msg.KeyProxyReserved, acct.Reserved))
	}
	return a.verifyProxy(ctx, mgr, acct, port)
}

// cmdProxyOff 停止 WireProxy。
func (a *app) cmdProxyOff(args []string) int {
	if len(args) > 0 {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, args[0])))
	}
	mgr := proxy.New()
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	mg := mgr.Systemd()
	active, err := mg.IsActive(ctx)
	if err != nil {
		return a.fail(err)
	}
	if !active {
		a.log.Infof("%s", msg.T(msg.KeyProxyAlreadyOff))
		return exitOK
	}
	if err := mg.Stop(ctx); err != nil {
		return a.fail(err)
	}
	// reset-failed 顺手做掉：单元一旦被标成 failed，下次 restart 的语义会变得难以推理。
	if err := mg.ResetFailed(ctx); err != nil {
		a.log.Debugf("%v", err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyOffOK, mgr.Port()))
	return exitOK
}

// cmdProxyUninstall 撤掉 WireProxy（停服务、删单元、删二进制、删配置）。
func (a *app) cmdProxyUninstall(args []string) int {
	var keepBinary bool
	opts := newOptionSet().Bool("--keep-binary", &keepBinary)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	mgr := proxy.New()
	ctx, cancel := context.WithTimeout(context.Background(), proxyTimeout)
	defer cancel()

	removed, warn := mgr.Remove(ctx, keepBinary)
	if warn != nil {
		a.log.Warnf("%v", warn)
	}
	if keepBinary {
		a.log.Infof("%s", msg.Tf(msg.KeyProxyKeptBinary, mgr.BinPath))
	}
	a.log.Infof("%s", msg.Tf(msg.KeyProxyUninstalled, len(removed)))
	for _, path := range removed {
		a.log.Debugf("  已删除 %s", path)
	}
	return exitOK
}

// statusViewProxy 是 `proxy status --json` 的输出形状。
type statusViewProxy struct {
	Installed bool   `json:"installed"`
	Port      int    `json:"port"`
	Proxy     string `json:"proxy_url,omitempty"`
	BinPath   string `json:"bin_path"`
	ConfPath  string `json:"conf_path"`
	UnitPath  string `json:"unit_path"`
	BinExists bool   `json:"binary_exists"`
	ConfExist bool   `json:"conf_exists"`
	UnitExist bool   `json:"unit_exists"`
	Active    bool   `json:"active"`
	Enabled   bool   `json:"enabled"`
	Listening bool   `json:"listening"`
	Version   string `json:"version,omitempty"`
	Warp      string `json:"warp,omitempty"`
	ExitIP    string `json:"exit_ip,omitempty"`
	Location  string `json:"location,omitempty"`
	ProbeErr  string `json:"probe_error,omitempty"`
}

// cmdProxyStatus 报告 WireProxy 当前的状态。
func (a *app) cmdProxyStatus(args []string) int {
	var asJSON bool
	opts := newOptionSet().Bool("--json", &asJSON)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	mgr := proxy.New()
	port := mgr.Port()
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	mg := mgr.Systemd()
	active, _ := mg.IsActive(ctx)
	enabled, _ := mg.IsEnabled(ctx)
	listening := portListening(port)

	v := statusViewProxy{
		Installed: mgr.Installed(),
		Port:      port,
		Proxy:     mgr.ProxyURL(port),
		BinPath:   mgr.BinPath,
		ConfPath:  mgr.ConfPath,
		UnitPath:  mgr.UnitPath,
		BinExists: mgr.BinExists(),
		ConfExist: mgr.ConfExists(),
		UnitExist: mgr.UnitExists(),
		Active:    active,
		Enabled:   enabled,
		Listening: listening,
	}
	if st, found, err := mgr.Load(); err == nil && found {
		v.Version = st.Version
	}

	// 只在"进程在跑"时才去探测：没跑的时候探测结果必然是超时，
	// 那既浪费时间，也会把"服务没起"这个更重要的信息淹掉。
	if active && listening {
		// 账户读不出来不算错：status 的核心是"这个代理现在什么样"，
		// 读不到就按双栈各探一次。
		families := []int{health.FamilyV4, health.FamilyV6}
		if acct, err := cloudflare.LoadAccount(state.AccountPath()); err == nil {
			families = accountFamilies(acct)
		}
		if r, err := probeViaProxy(ctx, mgr, port, families); err != nil {
			v.ProbeErr = err.Error()
		} else {
			v.Warp, v.ExitIP, v.Location = r.Warp, r.IP, r.Location
		}
	}

	if asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return a.fail(err)
		}
	} else {
		a.printProxyStatus(v)
	}

	if !v.Installed {
		a.log.Errorf("%s", msg.T(msg.KeyProxyNotInstalled))
		return exitUnhealthy
	}
	if !v.Active {
		a.log.Errorf("%s", msg.Tf(msg.KeyProxyNotRunning, proxy.DefaultUnitName))
		return exitUnhealthy
	}
	if !v.WarpOn() {
		a.log.Errorf("%s", msg.Tf(msg.KeyProxyUnhealthy, v.ProbeErr))
		return exitUnhealthy
	}
	return exitOK
}

// WarpOn 报告经代理出去的流量是不是 WARP。
func (v statusViewProxy) WarpOn() bool { return v.Warp == "on" || v.Warp == "plus" }

func (a *app) printProxyStatus(v statusViewProxy) {
	fmt.Fprintln(a.stdout, msg.T(msg.KeyProxyStatusTitle))
	fields := [][2]string{
		{"installed", yesNo(v.Installed)},
		{"binary", fmt.Sprintf("%s（存在: %s）", v.BinPath, yesNo(v.BinExists))},
		{"config", fmt.Sprintf("%s（存在: %s）", v.ConfPath, yesNo(v.ConfExist))},
		{"unit", fmt.Sprintf("%s（存在: %s）", v.UnitPath, yesNo(v.UnitExist))},
		{"active", yesNo(v.Active)},
		{"enabled", yesNo(v.Enabled)},
		{"socks5", fmt.Sprintf("%s（监听: %s）", v.Proxy, yesNo(v.Listening))},
	}
	if v.Version != "" {
		fields = append(fields, [2]string{"version", v.Version})
	}
	if v.Warp != "" {
		fields = append(fields, [2]string{"warp", fmt.Sprintf("%s（出口 %s %s）", v.Warp, v.ExitIP, v.Location)})
	}
	if v.ProbeErr != "" {
		fields = append(fields, [2]string{"probe", v.ProbeErr})
	}
	for _, f := range fields {
		fmt.Fprintf(a.stdout, "  %-18s %s\n", f[0], f[1])
	}
}

// verifyProxy 经 socks5 实测一次，确认"从它出去的流量真的是 WARP"。
//
// 端口在听只说明进程活着，配置对只说明文件没错 —— 只有真正发一次请求，
// 才能回答"这条路通不通"。这与隧道模式的自检是同一条原则。
func (a *app) verifyProxy(ctx context.Context, mgr *proxy.Manager, acct *cloudflare.Account, port int) int {
	families := accountFamilies(acct)
	var lastErr error
	for i := 1; i <= proxyVerifyAttempts; i++ {
		r, err := probeViaProxy(ctx, mgr, port, families)
		if err == nil {
			a.log.Infof("%s", msg.Tf(msg.KeyProxyOnOK, mgr.ProxyURL(port), r.String()))
			return exitOK
		}
		lastErr = err
		if i < proxyVerifyAttempts {
			a.log.Debugf("%s", msg.Tf(msg.KeyProxyVerifyRetry, i, proxyVerifyAttempts, err))
			select {
			case <-ctx.Done():
				return a.fail(ctx.Err())
			case <-time.After(proxyVerifyGap):
			}
		}
	}
	// 刻意**不停掉服务**：WARP 的对端偶尔会短暂不可用，而 wireproxy 会自己重试。
	// 停掉它反而让"过一会儿就好了"变成"需要人再来一次"。
	a.log.Errorf("%s", msg.Tf(msg.KeyProxyUnhealthy, lastErr))
	return exitUnhealthy
}

// probeViaProxy 经 socks5 探一次。
func probeViaProxy(ctx context.Context, mgr *proxy.Manager, port int, families []int) (health.Result, error) {
	probeCtx, cancel := context.WithTimeout(ctx, proxyProbeTimeout*time.Duration(len(families)+1))
	defer cancel()
	return health.ProbeAny(probeCtx, families, health.Options{
		Proxy:   mgr.ProxyURL(port),
		Timeout: proxyProbeTimeout,
	})
}

// proxyDNS 按账户具备的地址族给出隧道内 DNS。
//
// 只给该族有的：给一个"隧道里根本没有对应地址"的解析器（比如 v4-only 账户
// 配一个 v6 DNS），结果是解析偶尔卡住再回退，表现成"时快时慢"。
func proxyDNS(acct *cloudflare.Account) []string {
	var out []string
	if acct.HasV4() {
		out = append(out, "1.1.1.1")
	}
	if acct.HasV6() {
		out = append(out, "2606:4700:4700::1111")
	}
	if len(out) == 0 {
		out = proxy.DefaultDNS
	}
	return out
}

// portConflict 判断"这个端口是否构成冲突、该不该拒绝本次安装"。
//
// 纯函数（把"端口在不在听"作为入参传进来），于是这条策略可以单测 ——
// 它是唯一一处会让"重装一个已装好的 WireProxy"失败的地方，值得钉住。
//
// 判定：端口有人听 **且** 那个听着的人不是我们（不是"已安装 + 正在运行 + 用的就是
// 这个端口"）。我们自己的进程马上会被 restart 顶掉，不算冲突。
func portConflict(busy, installed, active bool, currentPort, wantPort int) bool {
	if !busy {
		return false
	}
	if installed && active && currentPort == wantPort {
		return false
	}
	return true
}

// portBusy 报告端口是否已被占用。
//
// "谁占着"这个信息刻意不查：要查就得解析 /proc 或调 ss，而它能提供的唯一价值
// 是让人看到进程名 —— 对"能不能 bind"这个问题来说，答案是有东西在听就够了，
// 而用户接下来要做的事（换个端口，或停掉那个进程）也不需要它。
// 唯一的例外是"占用者就是我们自己"，那一条由 portConflict 用系统状态判断，
// 不必去猜进程名。
func portBusy(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// portListening 报告本机是否有进程在监听该端口。
func portListening(port int) bool { return portBusy(port) }

// shortSHA 只显示校验和的前 12 位（完整值记在状态文件里）。
func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	if s == "" {
		return "-"
	}
	return s
}

// accountFamilies 已由 ensure.go 提供（隧道自检与这里要的是同一件事：
// "账户具备哪几族地址"），此处刻意不再重复定义。
