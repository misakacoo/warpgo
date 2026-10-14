package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/health"
	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/tunnel"
	"github.com/misakacoo/warpgo/internal/wgconf"
)

// upTimeout 覆盖"建接口 + 下发配置 + 配路由"，不含后续的联网探测。
const upTimeout = 90 * time.Second

// defaultKeepalive 是 WireGuard 的持久保活间隔。WARP 的 NAT 映射存活时间很短，
// 25 秒是上游与整个生态的共识值。
const defaultKeepalive = 25 * time.Second

// anyPrefix4 / anyPrefix6 是两族的全量 AllowedIPs。
var (
	anyPrefix4 = netip.MustParsePrefix("0.0.0.0/0")
	anyPrefix6 = netip.MustParsePrefix("::/0")
)

type upOptions struct {
	IfName   string
	Mode     netcfg.Mode
	MTU      int
	Endpoint string
	Force    bool
}

// selfCheckBudget 是自检的总预算。
//
// 它必须明显小于保险丝的超时（45s），否则会出现"自检还没跑完、保险丝先拉闸"
// 的竞态 —— 那样的失败看起来像随机断网，非常难排查。
const selfCheckBudget = 25 * time.Second

func (a *app) cmdUp(args []string) int {
	var (
		modeStr         string
		ifName          string
		mtu             int
		endpoint        string
		force           bool
		noConfirm       bool
		guardTimeoutStr string
	)
	opts := newOptionSet().
		Str("--mode", &modeStr).
		Str("--ifname", &ifName).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Bool("--force", &force).
		Bool("--no-confirm", &noConfirm).
		Str("--guard-timeout", &guardTimeoutStr)
	if err := opts.parse(args, a.bundle.Tf); err != nil {
		return a.fail(err)
	}

	mode, err := netcfg.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}

	guardTimeout := rollback.DefaultTimeout
	if guardTimeoutStr != "" {
		d, perr := time.ParseDuration(guardTimeoutStr)
		if perr != nil {
			return a.fail(fmt.Errorf("--guard-timeout 不是合法时长: %w", perr))
		}
		if d <= 0 {
			return a.fail(fmt.Errorf("--guard-timeout 必须为正数（实得 %s）", d))
		}
		guardTimeout = d
	}

	// 建隧道会改系统网络，非 Debian 直接拒绝。
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", a.bundle.Tf(i18n.KeyUpNotDebian, runtime.GOOS))
		return exitNotDebian
	}

	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}

	opt := upOptions{IfName: ifName, Mode: mode, MTU: mtu, Endpoint: endpoint, Force: force}
	cfg, plan, rtState, err := buildTunnel(acct, opt)
	if err != nil {
		return a.fail(err)
	}

	lock, code := a.acquireLock()
	if code != exitOK {
		return code
	}
	defer a.releaseLock(lock)

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	// ★ 全局模式的前置：**先上保险，再动网络**。
	//
	// 顺序不能反。保险丝的整个意义就是"当变更把网络弄断时，还有人在数秒后拉闸"；
	// 如果先改网络再派生保险丝，那么断网那一刻保险丝还没出生，等于没有保险。
	var snap *rollback.Snapshot
	if mode == netcfg.ModeGlobal {
		if snap, err = newSnapshot(cfg.IfName, plan); err != nil {
			return a.fail(err)
		}
		if err := rollback.ClearConfirm(); err != nil {
			return a.fail(err)
		}
		if err := snap.Save(rollback.SnapshotPath()); err != nil {
			return a.fail(err)
		}
		if err := a.spawnGuard(guardTimeout); err != nil {
			return a.fail(err)
		}
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyUpGuardSpawned, guardTimeout))
		a.log.Warnf("%s", a.bundle.T(i18n.KeyUpGlobalStart))
	}

	if force {
		a.log.Infof("%s", a.bundle.T(i18n.KeyUpForce))
		if err := a.tearDown(ctx, cfg.IfName); err != nil {
			a.log.Warnf("%v", err)
		}
	}

	a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpStart, mode, cfg.IfName))

	tun, err := tunnel.NewKernel(cfg.IfName)
	if err != nil {
		return a.fail(err)
	}
	defer tun.Close()

	ch, err := tun.Ensure(ctx, cfg)
	if err != nil {
		return a.fail(err)
	}
	a.reportChange(ch, cfg)

	res, err := netcfg.Apply(plan, a.netlog)
	if err != nil {
		return a.fail(err)
	}
	if len(res.RulesRemoved) > 0 {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpCleaned, len(res.RulesRemoved)))
	}
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpNetcfg, res.RoutesAdded, res.RulesAdded))
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpAddrs, strings.Join(plan.AddrStrings(), ", ")))

	if err := rtState.Save(state.RuntimePath()); err != nil {
		return a.fail(err)
	}

	if mode == netcfg.ModeGlobal {
		return a.finishGlobal(ctx, snap, plan, guardTimeout, noConfirm)
	}

	a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpDone, mode))
	return exitOK
}

// finishGlobal 是全局模式的后置：自检通过才确认，否则立刻回滚。
//
// 为什么"自检失败要立刻回滚"而不是等保险丝超时：早点回滚能把断网窗口从
// 45 秒缩到几秒。保险丝是给"连这段代码都没跑完"的极端情况兜底的。
func (a *app) finishGlobal(ctx context.Context, snap *rollback.Snapshot, plan *netcfg.Plan, timeout time.Duration, noConfirm bool) int {
	if noConfirm {
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyUpNoConfirm, timeout))
		return exitOK
	}

	a.log.Infof("%s", a.bundle.T(i18n.KeyUpChecking))
	// 自检有独立的预算，且必须明显小于保险丝超时（见 selfCheckBudget 的说明）。
	checkCtx, cancel := context.WithTimeout(ctx, selfCheckBudget)
	defer cancel()
	detail, err := a.selfCheck(checkCtx, plan)
	if err != nil {
		a.log.Errorf("%s", a.bundle.Tf(i18n.KeyUpCheckFail, err))
		a.log.Warnf("%s", a.bundle.T(i18n.KeyUpRollingBack))
		if rerr := rollback.Rollback(snap, a.guardLog); rerr != nil {
			return a.fail(rerr)
		}
		_ = rollback.ClearSnapshot()
		return exitUnhealthy
	}
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpCheckOK, detail))
	if err := rollback.Confirm(); err != nil {
		return a.fail(err)
	}
	_ = rollback.ClearSnapshot()
	a.log.Infof("%s", a.bundle.T(i18n.KeyUpGlobalDone))
	return exitOK
}

// selfCheck 是放行全局模式前的最后一道检查，三条缺一不可。
//
// 全部只读：即使判定"不行"，我们也只是把自己加的东西撤掉，
// 不会让"正在恢复"的状态变得更糟。
func (a *app) selfCheck(ctx context.Context, plan *netcfg.Plan) (string, error) {
	// ① 经隧道至少一族能拿到 warp=on —— 隧道真的通了。
	viaTunnel, err := health.ProbeAny(ctx, plan.Families, health.Options{
		Interface: plan.IfName,
		Timeout:   8 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("经隧道没有拿到 warp=on: %w", err)
	}

	// ② 系统默认路径仍能出网。全局模式下这条本身也走 WARP，
	//    但它走的是"策略规则"而不是"绑定接口"，正好验证规则配对没配错。
	viaDefault, err := health.ProbeAny(ctx, plan.Families, health.Options{
		Timeout: 6 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("系统默认路径已不可用（很可能整机断网）: %w", err)
	}

	// ③ 默认路由表项还在，且没有指向我们 —— 全局模式靠策略规则，
	//    绝不该去改默认路由。这一条是在验证我们没做出格的事。
	dev, err := netcfg.DefaultRouteDev(netcfg.FamilyV4)
	if err != nil {
		return "", fmt.Errorf("读取默认路由: %w", err)
	}
	if dev == "" {
		return "", errors.New("找不到系统默认路由")
	}
	if dev == plan.IfName {
		return "", fmt.Errorf("默认路由被指向了 %s（全局模式不该这么做）", plan.IfName)
	}

	return fmt.Sprintf("经隧道 %s；默认路径 %s；默认路由 dev=%s", viaTunnel, viaDefault, dev), nil
}

// newSnapshot 记录这次变更动了什么，供回滚使用。
func newSnapshot(ifName string, plan *netcfg.Plan) (*rollback.Snapshot, error) {
	if plan == nil {
		return nil, errors.New("plan 为空，无法生成回滚快照")
	}
	seen := map[int]bool{}
	var prefs []int
	for _, r := range plan.Rules {
		if !seen[r.Pref] {
			seen[r.Pref] = true
			prefs = append(prefs, r.Pref)
		}
	}
	return &rollback.Snapshot{
		CreatedAt:    time.Now().UTC(),
		IfName:       ifName,
		Table:        plan.Table,
		Mode:         string(plan.Mode),
		ManagedPrefs: prefs,
		ResolvSHA256: rollback.ResolvFingerprint(),
		PID:          os.Getpid(),
		Reason:       "up --mode " + string(plan.Mode),
	}, nil
}

// spawnGuard 派生保险丝进程。
//
// 三个要点：
//   - setsid：让它脱离当前会话。发起变更的 SSH 会话一旦消失，
//     同会话的进程会被一并带走，那样保险丝就形同虚设。
//   - 不 Wait：它必须活得比我们久。
//   - 日志进文件：那一刻已经没有任何终端可以给它了。
func (a *app) spawnGuard(timeout time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件: %w", err)
	}
	if err := os.MkdirAll(rollback.RunDir(), 0o700); err != nil {
		return fmt.Errorf("创建 %s: %w", rollback.RunDir(), err)
	}
	logFile, err := os.OpenFile(rollback.GuardLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开保险丝日志: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe,
		"--lang", string(a.bundle.Lang()),
		"rollback-guard",
		"--timeout", timeout.String(),
		"--snapshot", rollback.SnapshotPath())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("派生保险丝进程: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// buildTunnel 把账户翻译成「隧道参数 + 期望网络状态 + 运行态」。
//
// 三样东西一起产出，是因为它们必须来自同一次解析 —— 分开算容易出现
// "隧道用一个端点、运行态里记另一个"这种对不上的情况。
func buildTunnel(acct *cloudflare.Account, opt upOptions) (tunnel.Config, *netcfg.Plan, *state.Runtime, error) {
	var zeroCfg tunnel.Config

	ifName := opt.IfName
	if ifName == "" {
		ifName = netcfg.DefaultIfName
	}
	mtu := opt.MTU
	if mtu == 0 {
		mtu = netcfg.DefaultMTU
	}

	priv, err := wgconf.ParseKey(acct.PrivateKey)
	if err != nil {
		return zeroCfg, nil, nil, fmt.Errorf("账户里的私钥不可用: %w", err)
	}
	pub, err := wgconf.ParseKey(acct.PublicKey)
	if err != nil {
		return zeroCfg, nil, nil, fmt.Errorf("账户里的公钥不可用: %w", err)
	}
	peer, err := wgconf.ParseKey(acct.PeerPublicKey)
	if err != nil {
		return zeroCfg, nil, nil, fmt.Errorf("账户里的对端公钥不可用: %w", err)
	}

	addrs, err := accountPrefixes(acct)
	if err != nil {
		return zeroCfg, nil, nil, err
	}

	endpoint := strings.TrimSpace(opt.Endpoint)
	if endpoint == "" {
		endpoint = acct.PreferredEndpoint()
	}
	if endpoint == "" {
		return zeroCfg, nil, nil, errors.New("账户里没有可用的对端端点")
	}

	cfg := tunnel.Config{
		IfName:        ifName,
		PrivateKey:    priv,
		PublicKey:     pub,
		PeerPublicKey: peer,
		Endpoint:      endpoint,
		AllowedIPs:    defaultAllowedIPs(addrs),
		Keepalive:     defaultKeepalive,
		MTU:           mtu,
		Addresses:     addrs,
	}

	plan, err := netcfg.Build(addrs, netcfg.Options{IfName: ifName, MTU: mtu, Mode: opt.Mode})
	if err != nil {
		return zeroCfg, nil, nil, err
	}
	// 全局模式要求隧道自己打 fwmark，否则封装包会被自己的规则送进隧道（死循环）。
	cfg.FWMark = plan.FWMark

	rtState := &state.Runtime{
		IfName:   ifName,
		Mode:     string(opt.Mode),
		Backend:  string(tunnel.BackendKernel),
		MTU:      mtu,
		Table:    netcfg.TableWarp,
		FWMark:   plan.FWMark,
		Families: plan.Families,
		Addrs:    plan.AddrStrings(),
		Endpoint: endpoint,
		UpSince:  time.Now().UTC(),
	}
	return cfg, plan, rtState, nil
}

// accountPrefixes 取出账户里的接口地址。
func accountPrefixes(acct *cloudflare.Account) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range []string{acct.AddrV4, acct.AddrV6} {
		v := strings.TrimSpace(s)
		if v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return nil, fmt.Errorf("账户里的地址 %q 无法解析: %w", v, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("账户里没有任何接口地址")
	}
	return out, nil
}

// defaultAllowedIPs 让隧道接住所有目的地 —— 谁能用由策略路由决定，不是由这里决定。
//
// 只覆盖账户实际拥有的地址族：账户只有 IPv4 时若写上 ::/0，
// 隧道会接住 v6 流量而接口上没有 v6 地址，结果是静默黑洞。
func defaultAllowedIPs(addrs []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	seenV4, seenV6 := false, false
	for _, a := range addrs {
		if a.Addr().Is4() {
			if !seenV4 {
				seenV4 = true
				out = append(out, anyPrefix4)
			}
			continue
		}
		if !seenV6 {
			seenV6 = true
			out = append(out, anyPrefix6)
		}
	}
	return out
}

// reportChange 说明这次 Ensure 到底动了什么 —— 幂等性因此变得可观测。
func (a *app) reportChange(ch tunnel.Change, cfg tunnel.Config) {
	if ch.LinkCreated {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpLinkCreated, cfg.IfName, cfg.MTU))
	} else {
		// 复用时明确说出来：用户最关心的恰恰是"这次有没有换 IP"。
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpLinkReused, cfg.IfName))
	}
	switch {
	case ch.Configured:
		a.log.Infof("%s", a.bundle.T(i18n.KeyUpConfigured))
	case ch.ConfigMatched:
		a.log.Infof("%s", a.bundle.T(i18n.KeyUpCfgMatched))
	}
}

// netlog 把 netcfg 的明细日志降到 debug 级别：默认输出只留汇总，
// 需要排障时 `-v` 就能看到每一条规则/路由。
func (a *app) netlog(format string, args ...any) {
	a.log.Debugf(format, args...)
}

// acquireLock 拿单实例锁。拿不到时明确报错（退出码 4），绝不静默继续。
func (a *app) acquireLock() (*state.Lock, int) {
	// 锁文件在 /run/warpgo/ 下，父目录可能还不存在。
	if err := os.MkdirAll(filepath.Dir(state.DefaultLockPath), 0o700); err != nil {
		a.log.Errorf("%v", err)
		return nil, exitError
	}
	lock, st, err := state.Acquire(state.DefaultLockPath, state.DefaultStaleAfter)
	if err != nil {
		a.log.Errorf("%v", err)
		return nil, exitError
	}
	if st == state.StatusBusy {
		a.log.Errorf("%s", a.bundle.Tf(i18n.KeyLockBusy, holderPID(state.DefaultLockPath)))
		return nil, exitLockBusy
	}
	a.reportTakeover(lock)
	return lock, exitOK
}

func (a *app) releaseLock(lock *state.Lock) {
	if lock == nil {
		return
	}
	if err := lock.Release(); err != nil {
		a.log.Warnf("release lock: %v", err)
	}
}

// tearDown 清规则/路由 + 删接口，返回是否真的删掉了接口。
func (a *app) tearDown(ctx context.Context, ifName string) error {
	if _, err := netcfg.Cleanup(a.netlog); err != nil {
		return err
	}
	if ifName == "" {
		ifName = netcfg.DefaultIfName
	}
	tun, err := tunnel.NewKernel(ifName)
	if err != nil {
		return err
	}
	defer tun.Close()
	_, err = tun.TearDown(ctx)
	return err
}
