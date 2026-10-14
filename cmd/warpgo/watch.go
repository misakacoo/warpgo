package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/health"
	"github.com/misakacoo/warpgo/internal/keepalive"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// probeTimeout 是单轮体检里每一族的超时。
//
// 8 秒这个值来自真机实测：接口刚被硬复位时首次握手偏慢，
// 更短的超时会把"正在恢复"误判成"还没恢复"，于是白白多重建一次。
const probeTimeout = 8 * time.Second

// cmdWatch 是保活巡检。
//
// 它的全部行为可以压缩成一句话：**健康就不动，断了才重建**。
//
// 上游的保活每 5 分钟无条件拆建一次接口，用户的出口 IP 一直在变；
// 而且重建窗口里"拿不到 IP"会被误判成"没安装"，进而触发更多重建。
// 这里把顺序钉死：先只读体检 → 健康就到此为止（一个字节都不改）。
func (a *app) cmdWatch(args []string) int {
	var (
		intervalStr     string
		modeStr         string
		backendStr      string
		ifName          string
		mtu             int
		endpoint        string
		dnsStr          string
		guardTimeoutStr string
		once            bool
		maxRounds       int
	)
	opts := newOptionSet().
		Str("--interval", &intervalStr).
		Str("--mode", &modeStr).
		Str("--backend", &backendStr).
		Str("--ifname", &ifName).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Str("--dns", &dnsStr).
		Str("--guard-timeout", &guardTimeoutStr).
		Bool("--once", &once).
		Int("--max-rounds", &maxRounds)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	interval := keepalive.DefaultInterval
	if intervalStr != "" {
		d, err := time.ParseDuration(intervalStr)
		if err != nil {
			return a.fail(fmt.Errorf("--interval 不是合法时长: %w", err))
		}
		if d < keepalive.MinInterval {
			return a.fail(fmt.Errorf("--interval 太小（%s），最小 %s —— 巡检要做网络探测，过密等于把本机变成扫描器",
				d, keepalive.MinInterval))
		}
		interval = d
	}

	// 模式默认取运行态里记的那个 —— 这样"用 --mode global 起过隧道"的机器
	// 装上保活之后，保活重建出来的仍是全局模式，不会悄悄降级成非全局。
	mode := netcfg.ModeNonGlobal
	families := []int{health.FamilyV4, health.FamilyV6}
	// 后端的默认值取运行态里记的"用户当初要求的是什么"（auto / kernel / userspace）。
	//
	// 注意是**要求**而不是**实际用上的**：如果拿实际值当默认，一次自动回落
	// 就会把 auto 永久钉死成 userspace —— 之后即使内核本来能用（比如换了地区、
	// 换了端点），也再不会回到内核。
	backend := tunnel.BackendAuto
	if rt, err := state.LoadRuntime(state.RuntimePath()); err == nil {
		if rt.IfName != "" && ifName == "" {
			ifName = rt.IfName
		}
		if m, merr := netcfg.ParseMode(rt.Mode); merr == nil && modeStr == "" {
			mode = m
		}
		if b, berr := tunnel.ParseBackend(rt.BackendRequested); berr == nil && backendStr == "" {
			backend = b
		}
		if len(rt.Families) > 0 {
			families = rt.Families
		}
	}
	if modeStr != "" {
		m, err := netcfg.ParseMode(modeStr)
		if err != nil {
			return a.fail(err)
		}
		mode = m
	}
	if backendStr != "" {
		b, err := tunnel.ParseBackend(backendStr)
		if err != nil {
			return a.fail(err)
		}
		backend = b
	}
	if ifName == "" {
		ifName = netcfg.DefaultIfName
	}
	// 运行态不在时（比如刚装上、还没起过隧道）由账户推断地址族。
	if acct, err := cloudflare.LoadAccount(state.AccountPath()); err == nil {
		families = accountFamilies(acct)
	}

	// 巡检会建隧道，也就是会改系统网络 —— 非 Debian 直接拒绝。
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyWatchNotDebian, system.PrettyName()))
		return exitNotDebian
	}

	guardTimeout := rollback.DefaultTimeout
	if guardTimeoutStr != "" {
		d, err := time.ParseDuration(guardTimeoutStr)
		if err != nil {
			return a.fail(fmt.Errorf("--guard-timeout 不是合法时长: %w", err))
		}
		if d <= 0 {
			return a.fail(fmt.Errorf("--guard-timeout 必须为正数（实得 %s）", d))
		}
		guardTimeout = d
	}

	buildOpt := ensureOptions{
		IfName:       ifName,
		Mode:         mode,
		Backend:      backend,
		MTU:          mtu,
		Endpoint:     endpoint,
		DNS:          splitList(dnsStr),
		GuardTimeout: guardTimeout,
		Quiet:        true,
		// 断链而配置未变时做硬复位 —— 见 ensureOptions.ResetIfIdle 的说明。
		ResetIfIdle: true,
	}

	// Ctrl-C 与 systemd stop 都走这条路：ctx 一取消，循环立刻收尾并安静退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a.log.Infof("%s", msg.Tf(msg.KeyWatchStart, interval, ifName, mode, backendLabel(backend)))

	deps := keepalive.Deps{
		Lock:  a.tryLock,
		Check: func(ctx context.Context) (string, error) { return a.watchCheck(ctx, ifName, families) },
		Rebuild: func(ctx context.Context) (string, error) {
			return a.watchRebuild(ctx, buildOpt)
		},
		Log: a.watchLog,
	}
	cfg := keepalive.Config{Interval: interval, Once: once, MaxRounds: maxRounds}

	sum, err := keepalive.Run(ctx, cfg, deps)
	if err != nil {
		return a.fail(err)
	}
	if once && !sum.IsHealthy() {
		return exitUnhealthy
	}
	return exitOK
}

// watchCheck 是只读体检：接口在 + 至少一族能经接口拿到 warp=on。
//
// 刻意**不要求所有族都好**：WARP 的 v6 通路本身抖动更大，
// 要求全好会让一台 v6 偶发抖动的机器每 5 分钟被重建一次 ——
// 那正是我们想消除的行为。
func (a *app) watchCheck(ctx context.Context, ifName string, families []int) (string, error) {
	if !netcfg.LinkExists(ifName) {
		return "", fmt.Errorf("接口 %s 不存在", ifName)
	}
	res, err := health.ProbeAny(ctx, families, health.Options{
		Interface: ifName,
		Timeout:   probeTimeout,
	})
	if err != nil {
		return "", err
	}
	return res.String(), nil
}

// watchRebuild 把隧道重新拉起来。走的是与 `up` 完全相同的代码路径。
func (a *app) watchRebuild(ctx context.Context, opt ensureOptions) (string, error) {
	acct, err := cloudflare.LoadAccount(state.AccountPath())
	if err != nil {
		return "", err
	}
	// ensureTunnel 内部会把具体错误打到 error 级别，这里只回一个短摘要，
	// 避免同一件事在 journal 里出现两遍长文本。
	res, code := a.ensureTunnel(ctx, acct, opt)
	if code != exitOK {
		return "", fmt.Errorf("重建未成功（退出码 %d）", code)
	}
	if res.HardReset {
		return res.Detail + "（已硬复位）", nil
	}
	return res.Detail, nil
}

// watchLog 把巡检事件翻译成当前语言的文案。
func (a *app) watchLog(ev keepalive.Event, args ...any) {
	switch ev {
	case keepalive.EvRoundStart:
		a.log.Debugf("%s", msg.Tf(msg.KeyWatchRound, args...))
	case keepalive.EvHealthy:
		a.log.Infof("%s", msg.Tf(msg.KeyWatchHealthy, args...))
	case keepalive.EvUnhealthy:
		a.log.Warnf("%s", msg.Tf(msg.KeyWatchUnhealthy, args...))
	case keepalive.EvRebuilding:
		a.log.Warnf("%s", msg.T(msg.KeyWatchRebuilding))
	case keepalive.EvRebuilt:
		a.log.Infof("%s", msg.Tf(msg.KeyWatchRebuilt, args...))
	case keepalive.EvRebuildFailed:
		a.log.Errorf("%s", msg.Tf(msg.KeyWatchFailed, args...))
	case keepalive.EvLocked:
		// 锁被占的提示已由 tryLock 给出，这里只留一条时间线。
		a.log.Debugf("%s", msg.Tf(msg.KeyWatchLocked, args...))
	case keepalive.EvStopped:
		sum, _ := args[0].(keepalive.Summary)
		a.log.Infof("%s", msg.Tf(msg.KeyWatchStopped,
			sum.Rounds, sum.Healthy, sum.Rebuilt, sum.Failed, sum.Skipped))
	}
}
