package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/netcfg"
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

func (a *app) cmdUp(args []string) int {
	var (
		modeStr  string
		ifName   string
		mtu      int
		endpoint string
		force    bool
	)
	opts := newOptionSet().
		Str("--mode", &modeStr).
		Str("--ifname", &ifName).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Bool("--force", &force)
	if err := opts.parse(args, a.bundle.Tf); err != nil {
		return a.fail(err)
	}

	mode, err := netcfg.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}
	if mode != netcfg.ModeNonGlobal {
		// 全局模式要 fwmark + watchdog 回滚（M3）。现在直接拒绝，
		// 而不是给一份"看起来能跑、实际会切断 SSH"的配置。
		return a.fail(fmt.Errorf("模式 %s 尚未实现（计划在 M3）", mode))
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
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyUpDone, mode))
	return exitOK
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

	rtState := &state.Runtime{
		IfName:   ifName,
		Mode:     string(opt.Mode),
		Backend:  string(tunnel.BackendKernel),
		MTU:      mtu,
		Table:    netcfg.TableWarp,
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
