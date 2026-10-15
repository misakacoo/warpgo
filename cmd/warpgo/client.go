package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/misakacoo/warpgo/internal/cfclient"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/systemd"
)

// clientTimeout 覆盖"加源 + apt 装包 + 起服务 + 注册 + 连上"。
//
// apt 装包是最不可控的一段（要下几十 MB），所以给得比别的命令宽得多。
const clientTimeout = 12 * time.Minute

// 等待窗口。
const (
	// clientServiceWait 等服务把 AF_UNIX socket 建出来。
	clientServiceWait = 60 * time.Second
	// clientIfaceWait 等 CloudflareWARP 接口出现（warp 模式）。
	clientIfaceWait = 60 * time.Second
	// clientSocksWait 等 socks5 端口开始监听（proxy 模式）。
	clientSocksWait = 30 * time.Second
)

// cmdClient 实现菜单 `c` / `l` / `r`（官方 Linux Client）。
func (a *app) cmdClient(args []string) int {
	if len(args) == 0 {
		a.log.Errorf("%s", msg.T(msg.KeyClientUsage))
		return exitError
	}
	switch args[0] {
	case "install":
		return a.cmdClientInstall(args[1:])
	case "uninstall":
		return a.cmdClientUninstall(args[1:])
	case "mode":
		return a.cmdClientMode(args[1:])
	case "on":
		return a.cmdClientOn(args[1:])
	case "off":
		return a.cmdClientOff(args[1:])
	case "status":
		return a.cmdClientStatus(args[1:])
	default:
		a.log.Errorf("%s", msg.Tf(msg.KeyUnknownCm, "client "+args[0]))
		a.log.Errorf("%s", msg.T(msg.KeyClientUsage))
		return exitError
	}
}

// checkClientPlatform 做两项与平台相关的硬校验。
//
// 它们是"这个功能本身的前置条件"，不是我们能绕过的：
// 官方只发布 amd64 / arm64 的包，也只为 Debian 系提供 apt 源。
func (a *app) checkClientPlatform() int {
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyClientNotDebian, system.PrettyName()))
		return exitNotDebian
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		a.log.Errorf("%s", msg.Tf(msg.KeyClientArch, runtime.GOARCH))
		return exitError
	}
	return exitOK
}

// hookBinPath 返回要写进 drop-in 的可执行文件路径。
//
// 优先用**安装后的**路径（`/usr/bin/warpgo`）：drop-in 会被 systemd 在
// 开机时执行，那时 `dist/warpgo` 之类的开发路径多半已经不在了。
// 没装成服务时才退回当前可执行文件，并让调用方把它打出来 —— 用户需要
// 知道钩子指向的是哪一份。
func hookBinPath() string {
	if _, err := os.Stat(systemd.DefaultBinPath); err == nil {
		return systemd.DefaultBinPath
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return systemd.DefaultBinPath
}

// cmdClientInstall 装好官方客户端并按模式配置。
func (a *app) cmdClientInstall(args []string) int {
	var (
		modeStr string
		port    int
		noStart bool
		noHooks bool
	)
	opts := newOptionSet().
		Str("--mode", &modeStr).
		Int("--port", &port).
		Bool("--no-start", &noStart).
		Bool("--no-hooks", &noHooks)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}
	if code := a.checkClientPlatform(); code != exitOK {
		return code
	}

	mode := cfclient.ModeProxy
	if modeStr != "" {
		m, err := cfclient.ParseModeName(modeStr)
		if err != nil {
			return a.fail(err)
		}
		mode = m
	}
	if mode == cfclient.ModeProxy && port == 0 {
		port = cfclient.DefaultSocksPort
	}
	if mode == cfclient.ModeProxy {
		if err := cfclient.ValidatePort(port); err != nil {
			return a.fail(err)
		}
	} else {
		// warp 模式会接管默认路由 —— 这句话必须在动手**之前**说。
		a.log.Warnf("%s", msg.T(msg.KeyClientWarpRisk))
	}

	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()

	mgr := cfclient.New()
	if mgr.Installed() {
		// 重复安装不是错误 —— 它是最自然的"改参数 / 修现场"动作。
		// 这里只提示一句，然后继续（装包那步 apt 会自己判断"已是最新"）。
		a.log.Infof("%s", msg.T(msg.KeyClientAlreadyInstalled))
	}

	// ① apt 源。
	codename, err := a.clientCodename()
	if err != nil {
		return a.fail(err)
	}
	created, err := mgr.EnsureRepo(ctx, codename)
	if err != nil {
		return a.fail(err)
	}
	if len(created) > 0 {
		a.log.Infof("%s", msg.Tf(msg.KeyClientRepoCreated, len(created), codename))
		for _, p := range created {
			a.log.Debugf("  新建 %s", p)
		}
	} else {
		a.log.Infof("%s", msg.Tf(msg.KeyClientRepoReady, codename))
	}

	// ② 装包。
	if !mgr.CLIAvailable() {
		a.log.Infof("%s", msg.T(msg.KeyClientInstalling))
		if err := mgr.InstallPackage(ctx); err != nil {
			return a.fail(err)
		}
	}
	if !mgr.CLIAvailable() {
		return a.fail(errors.New(msg.T(msg.KeyClientCLIMissing)))
	}
	a.log.Infof("%s", msg.Tf(msg.KeyClientInstalled, "cloudflare-warp"))

	// ③ 起服务并等 socket。
	svc := mgr.Service()
	active, _ := svc.IsActive(ctx)
	if !active {
		if err := svc.Start(ctx); err != nil {
			return a.fail(err)
		}
	}
	a.log.Infof("%s", msg.T(msg.KeyClientServiceWaiting))
	if err := mgr.WaitForService(ctx, clientServiceWait); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.T(msg.KeyClientServiceStarted))

	// ④ 注册（只在没有注册时做）。
	st, err := mgr.Query(ctx)
	if err != nil {
		return a.fail(err)
	}
	if st.Conn == cfclient.ConnNoRegistration || st.Account == "" {
		a.log.Infof("%s", msg.T(msg.KeyClientRegistering))
		if err := mgr.NewRegistration(ctx); err != nil {
			return a.fail(err)
		}
		a.log.Infof("%s", msg.T(msg.KeyClientRegistered))
	}
	// qlog 是客户端默认开启的调试日志，服务器上没人看它，关掉即可。
	if err := mgr.DisableQLog(ctx); err != nil {
		a.log.Debugf("%v", err)
	}

	// ⑤ 模式 + 连接。
	if err := mgr.ApplyMode(ctx, mode, port); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyClientModeSet, mode))

	cst := &cfclient.State{
		Mode:        mode,
		Port:        port,
		Keyring:     containsPath(created, mgr.KeyringPath),
		Source:      containsPath(created, mgr.SourceListPath),
		InstalledAt: time.Now().UTC(),
	}

	if !noStart {
		if code := a.clientConnectAndWait(ctx, mgr, mode, port); code != exitOK {
			return code
		}
	} else {
		a.log.Infof("%s", msg.T(msg.KeyClientNoStart))
	}

	// ⑥ warp 模式下的策略规则钩子。
	//
	// 放在"连上并等到接口"之后：钩子里的规则要以接口地址为源，
	// 接口没出现时加了也没有意义。
	if mode == cfclient.ModeWarp && !noHooks && !noStart {
		if code := a.installClientHooks(ctx, mgr, cst); code != exitOK {
			return code
		}
	}

	if err := mgr.SaveState(cst); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyClientInstallDone, string(mode)))
	return exitOK
}

// clientConnectAndWait 连上客户端并等到"真的可用"。
func (a *app) clientConnectAndWait(ctx context.Context, mgr *cfclient.Manager, mode cfclient.Mode, port int) int {
	if err := mgr.Connect(ctx); err != nil {
		return a.fail(err)
	}
	switch mode {
	case cfclient.ModeWarp:
		a.log.Infof("%s", msg.T(msg.KeyClientIfaceWaiting))
		if err := mgr.WaitForInterface(ctx, clientIfaceWait); err != nil {
			a.log.Errorf("%s", msg.Tf(msg.KeyClientIfaceMissing, err))
			return exitUnhealthy
		}
		a.log.Infof("%s", msg.Tf(msg.KeyClientIfaceUp, cfclient.ClientIfName))
	case cfclient.ModeProxy:
		a.log.Infof("%s", msg.T(msg.KeyClientSocksWaiting))
		if err := mgr.WaitForSocks5(ctx, port, clientSocksWait); err != nil {
			a.log.Errorf("%s", msg.Tf(msg.KeyClientSocksMissing, err))
			return exitUnhealthy
		}
		a.log.Infof("%s", msg.Tf(msg.KeyClientSocksOK, port))
	}
	return exitOK
}

// installClientHooks 写 drop-in 并立即加一次规则。
//
// 两件事都要做：drop-in 保证"重启之后规则还在"，而立即加一次保证
// "现在这条链路就已经通了" —— 只写 drop-in 的话，用户得重启服务才生效。
func (a *app) installClientHooks(ctx context.Context, mgr *cfclient.Manager, st *cfclient.State) int {
	bin := hookBinPath()
	if err := mgr.InstallDropIn(ctx, bin); err != nil {
		a.log.Warnf("%v", err)
	} else {
		st.DropIn = mgr.DropInPath
		a.log.Infof("%s", msg.Tf(msg.KeyClientHooksInstalled, mgr.DropInPath, bin))
	}
	res, err := mgr.HookAdd(ctx, 0, func(f string, args ...any) { a.log.Debugf(f, args...) })
	if err != nil {
		// 规则没加上不该让整次安装失败：客户端本身已经装好并连上了，
		// 缺的只是"让流量按源地址走 WARP"这一步。
		a.log.Warnf("%s", msg.Tf(msg.KeyClientHooksFailed, err))
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyClientHooksAdded, res.RulesAdded, res.RoutesAdded))
	return exitOK
}

// cmdClientMode 只切模式（不重新装包）。
func (a *app) cmdClientMode(args []string) int {
	if len(args) == 0 {
		a.log.Errorf("%s", msg.T(msg.KeyClientUsage))
		return exitError
	}
	mode, err := cfclient.ParseModeName(args[0])
	if err != nil {
		return a.fail(err)
	}
	var port int
	if err := newOptionSet().Int("--port", &port).parse(args[1:]); err != nil {
		return a.fail(err)
	}
	if mode == cfclient.ModeProxy && port == 0 {
		port = cfclient.DefaultSocksPort
	}
	if mode == cfclient.ModeWarp {
		a.log.Warnf("%s", msg.T(msg.KeyClientWarpRisk))
	}

	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()
	mgr := cfclient.New()
	if !mgr.Installed() {
		a.log.Errorf("%s", msg.T(msg.KeyClientNotInstalled))
		return exitError
	}
	if err := mgr.ApplyMode(ctx, mode, port); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyClientModeSet, mode))
	if code := a.clientConnectAndWait(ctx, mgr, mode, port); code != exitOK {
		return code
	}
	if st, found, _ := mgr.Load(); found {
		st.Mode = mode
		if mode == cfclient.ModeProxy {
			st.Port = port
		}
		_ = mgr.SaveState(st)
	}
	return exitOK
}

// cmdClientOn 打开（菜单 `r`）。
func (a *app) cmdClientOn(args []string) int {
	if len(args) > 0 {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, args[0])))
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()
	mgr := cfclient.New()
	if !mgr.Installed() {
		a.log.Errorf("%s", msg.T(msg.KeyClientNotInstalled))
		return exitError
	}

	st, err := mgr.Query(ctx)
	if err != nil {
		return a.fail(err)
	}
	if st.Connected() {
		a.log.Infof("%s", msg.T(msg.KeyClientAlreadyOn))
		return exitOK
	}
	mode := st.Mode
	if mode == "" {
		mode = cfclient.ModeProxy
	}
	port := st.SocksPort
	if port == 0 {
		if saved, found, _ := mgr.Load(); found && saved.Port > 0 {
			port = saved.Port
		} else {
			port = cfclient.DefaultSocksPort
		}
	}
	if code := a.clientConnectAndWait(ctx, mgr, mode, port); code != exitOK {
		return code
	}
	if mode == cfclient.ModeWarp {
		if _, err := mgr.HookAdd(ctx, clientIfaceWait, func(f string, args ...any) { a.log.Debugf(f, args...) }); err != nil {
			a.log.Warnf("%s", msg.Tf(msg.KeyClientHooksFailed, err))
		}
	}
	a.log.Infof("%s", msg.T(msg.KeyClientOnOK))
	return exitOK
}

// cmdClientOff 关闭（菜单 `r`）。
func (a *app) cmdClientOff(args []string) int {
	if len(args) > 0 {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, args[0])))
	}
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	mgr := cfclient.New()

	st, err := mgr.Query(ctx)
	if err != nil {
		return a.fail(err)
	}
	if !st.CLIPresent {
		a.log.Errorf("%s", msg.T(msg.KeyClientNotInstalled))
		return exitError
	}
	// 先撤规则再断开：反过来的话，接口已经没了而规则还指着它 ——
	// 现场看起来像"WARP 拆了、流量却还在往那张表里走"。
	if st.Mode == cfclient.ModeWarp {
		if _, err := mgr.HookDel(func(f string, args ...any) { a.log.Debugf(f, args...) }); err != nil {
			a.log.Warnf("%v", err)
		}
	}
	if !st.Connected() {
		a.log.Infof("%s", msg.T(msg.KeyClientAlreadyOff))
		return exitOK
	}
	if err := mgr.Disconnect(ctx); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.T(msg.KeyClientOffOK))
	return exitOK
}

// statusViewClient 是 `client status --json` 的输出形状。
type statusViewClient struct {
	Installed      bool   `json:"installed"`
	ServiceActive  bool   `json:"service_active"`
	ServiceEnabled bool   `json:"service_enabled"`
	Connection     string `json:"connection"`
	Mode           string `json:"mode,omitempty"`
	Account        string `json:"account,omitempty"`
	AccountLabel   string `json:"account_label,omitempty"`
	SocksPort      int    `json:"socks_port,omitempty"`
	SocksURL       string `json:"socks_url,omitempty"`
	SocksListening bool   `json:"socks_listening"`
	InterfaceUp    bool   `json:"interface_up"`
	HooksInstalled bool   `json:"hooks_installed"`
	DropInPath     string `json:"drop_in_path,omitempty"`
	Keyring        string `json:"keyring"`
	SourceList     string `json:"apt_source"`
}

// cmdClientStatus 报告客户端状态。
func (a *app) cmdClientStatus(args []string) int {
	var asJSON bool
	if err := newOptionSet().Bool("--json", &asJSON).parse(args); err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	mgr := cfclient.New()
	st, err := mgr.Query(ctx)
	if err != nil {
		return a.fail(err)
	}
	v := statusViewClient{
		Installed:      st.CLIPresent,
		ServiceActive:  st.ServiceActive,
		ServiceEnabled: st.ServiceEnabled,
		Connection:     string(st.Conn),
		Mode:           string(st.Mode),
		Account:        st.Account,
		AccountLabel:   cfclient.AccountLabel(st.Account),
		SocksPort:      st.SocksPort,
		SocksListening: st.SocksListening,
		InterfaceUp:    st.InterfaceUp,
		HooksInstalled: mgr.DropInExists(),
		DropInPath:     mgr.DropInPath,
		Keyring:        mgr.KeyringPath,
		SourceList:     mgr.SourceListPath,
	}
	if st.Mode == cfclient.ModeProxy && st.SocksPort > 0 {
		v.SocksURL = fmt.Sprintf("socks5h://127.0.0.1:%d", st.SocksPort)
	}

	if asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return a.fail(err)
		}
	} else {
		a.printClientStatus(v)
	}

	if !v.Installed {
		a.log.Errorf("%s", msg.T(msg.KeyClientNotInstalled))
		return exitUnhealthy
	}
	if !v.ServiceActive {
		a.log.Errorf("%s", msg.Tf(msg.KeyClientServiceDown, cfclient.UnitName))
		return exitUnhealthy
	}
	return exitOK
}

func (a *app) printClientStatus(v statusViewClient) {
	fmt.Fprintln(a.stdout, msg.T(msg.KeyClientStatusTitle))
	fields := [][2]string{
		{"installed", yesNo(v.Installed)},
		{"service", fmt.Sprintf("%s（运行: %s，自启: %s）", cfclient.UnitName, yesNo(v.ServiceActive), yesNo(v.ServiceEnabled))},
		{"connection", v.Connection},
	}
	if v.Mode != "" {
		fields = append(fields, [2]string{"mode", string(v.Mode)})
	}
	if v.Account != "" {
		fields = append(fields, [2]string{"account", v.AccountLabel})
	}
	if v.Mode == string(cfclient.ModeProxy) {
		fields = append(fields, [2]string{"socks5", fmt.Sprintf("%s（监听: %s）", v.SocksURL, yesNo(v.SocksListening))})
	}
	if v.Mode == string(cfclient.ModeWarp) {
		fields = append(fields, [2]string{"interface", fmt.Sprintf("%s（存在: %s）", cfclient.ClientIfName, yesNo(v.InterfaceUp))})
		fields = append(fields, [2]string{"hooks", fmt.Sprintf("已装: %s", yesNo(v.HooksInstalled))})
	}
	fields = append(fields,
		[2]string{"apt source", v.SourceList},
		[2]string{"keyring", v.Keyring},
	)
	for _, f := range fields {
		fmt.Fprintf(a.stdout, "  %-12s %s\n", f[0], f[1])
	}
}

// cmdClientUninstall 把官方客户端从这台机器上撤掉。
func (a *app) cmdClientUninstall(args []string) int {
	var keepRepo bool
	if err := newOptionSet().Bool("--keep-repo", &keepRepo).parse(args); err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientTimeout)
	defer cancel()
	// requireState=false：这是用户**显式**要求卸载客户端，不必先证明是我们装的。
	if _, code := a.clientRemove(ctx, keepRepo, false); code != exitOK {
		return code
	}
	return exitOK
}

// clientRemove 是撤销官方客户端的**唯一实现**，供 `client uninstall` 与
// `warpgo uninstall` 共用 —— 两条路径必须走完全一样的代码，否则
// "手工撤得干净、总卸载却留了尾巴"这类偏差迟早出现。
//
// `requireState` 为真时（总卸载走这条路），只有"我们装过"才动手：
// 机器上若本来就装着官方客户端（用户自己装的），我们无权把它删掉。
//
// 顺序（每一步都有理由）：
//  1. 撤规则 —— 否则接口没了规则还在，流量会往一张指向空接口的表里走；
//  2. 断开 + 注销注册 —— 在停服务之前做，因为这两个动作都要经 warp-svc；
//  3. 停并禁用服务、卸包；
//  4. 删 drop-in（我们写的）；
//  5. **只删我们建的** apt 源与密钥环。
//
// 返回 (是否真的动过手, 退出码)。
func (a *app) clientRemove(ctx context.Context, keepRepo, requireState bool) (bool, int) {
	mgr := cfclient.New()
	st, found, _ := mgr.Load()
	installed := mgr.Installed()

	if requireState && !found {
		a.log.Infof("%s", msg.T(msg.KeyClientNotOurs))
		return false, exitOK
	}
	if !installed && !found && !mgr.DropInExists() && !mgr.RepoAny() {
		a.log.Infof("%s", msg.T(msg.KeyClientNotInstalled))
		return false, exitOK
	}
	a.log.Infof("%s", msg.T(msg.KeyClientUninstallStart))

	// ① 规则。
	if _, err := mgr.HookDel(func(f string, args ...any) { a.log.Debugf(f, args...) }); err != nil {
		a.log.Warnf("%v", err)
	}

	// ② 断开 + 注销（要经 warp-svc，所以必须排在停服务之前）。
	if installed {
		if active, _ := mgr.Service().IsActive(ctx); active {
			if err := mgr.Disconnect(ctx); err != nil {
				a.log.Debugf("%v", err)
			}
			if err := mgr.DeleteRegistration(ctx); err != nil {
				a.log.Debugf("%v", err)
			}
		}
	}

	// ③ 服务 + 包。
	svc := mgr.Service()
	_ = svc.Disable(ctx)
	_ = svc.Stop(ctx)
	_ = svc.ResetFailed(ctx)
	if installed {
		if err := mgr.RemovePackage(ctx); err != nil {
			// 包可能压根没装上（只配了源），那不算失败。
			a.log.Warnf("%v", err)
		}
	}

	// ④ drop-in。
	if removed, err := mgr.RemoveDropIn(ctx); err != nil {
		a.log.Warnf("%v", err)
	} else if removed {
		a.log.Infof("%s", msg.Tf(msg.KeyClientDropInRemoved, mgr.DropInPath))
	}

	// ⑤ apt 源与密钥环 —— 只删我们建的。
	switch {
	case keepRepo:
		a.log.Infof("%s", msg.T(msg.KeyClientRepoKept))
	case found:
		var created []string
		if st.Keyring {
			created = append(created, mgr.KeyringPath)
		}
		if st.Source {
			created = append(created, mgr.SourceListPath)
		}
		removed, warn := mgr.RemoveRepo(created)
		if warn != nil {
			a.log.Warnf("%v", warn)
		}
		if len(removed) > 0 {
			a.log.Infof("%s", msg.Tf(msg.KeyClientRepoRemoved, len(removed)))
		} else {
			a.log.Infof("%s", msg.T(msg.KeyClientRepoKept))
		}
	default:
		// 没有状态文件（例如状态目录被清过）：不敢删任何东西 ——
		// 无从判断哪一份是我们建的。
		a.log.Infof("%s", msg.T(msg.KeyClientRepoKept))
	}

	if err := mgr.RemoveState(); err != nil {
		a.log.Warnf("%v", err)
	}
	a.log.Infof("%s", msg.T(msg.KeyClientUninstallDone))
	return true, exitOK
}

// clientCodename 读出本机发行版代号（含"官方源还没有目录"的回退）。
func (a *app) clientCodename() (string, error) {
	raw, err := os.ReadFile(system.OSReleaseFile)
	if err != nil {
		return "", fmt.Errorf("读取 %s: %w", system.OSReleaseFile, err)
	}
	return cfclient.CodenameFrom(string(raw))
}

// containsPath 报告路径是否在列表里。
func containsPath(list []string, p string) bool {
	for _, v := range list {
		if v == p {
			return true
		}
	}
	return false
}
