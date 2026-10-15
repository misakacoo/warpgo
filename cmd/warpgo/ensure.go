package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/health"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/tunnel"
	"github.com/misakacoo/warpgo/internal/wgconf"
)

const (
	// upTimeout 覆盖"建接口 + 下发配置 + 配路由"，不含后续的联网探测。
	//
	// 150 秒是给"自动切后端"留的余量：内核试一次（含自检 25 秒）、
	// 用户态再试一次，两轮加起来在全局模式下可能接近 90 秒。
	// 超时太紧会让第二次尝试在中途被掐掉，留下一个半成品现场。
	upTimeout = 150 * time.Second

	// defaultKeepalive 是 WireGuard 的持久保活间隔。WARP 的 NAT 映射存活时间很短，
	// 25 秒是上游与整个生态的共识值。
	defaultKeepalive = 25 * time.Second

	// selfCheckBudget 是自检的总预算。
	//
	// 它必须明显小于保险丝的超时（45s），否则会出现"自检还没跑完、保险丝先拉闸"
	// 的竞态 —— 那样的失败看起来像随机断网，非常难排查。
	selfCheckBudget = 25 * time.Second

	// verifyTimeout 是"换个后端再试"之前那次体检的**单族**超时。
	verifyTimeout = 6 * time.Second

	// verifyAttempts / verifyGap 是体检的重试次数与间隔。
	//
	// 为什么必须重试：这是"用探测结果决定要不要换后端"的策略，而一次探测失败
	// 的代价很大 —— 拆掉一条本来好好的隧道、换另一种实现重建。
	// 真机上就看到过这么一次误判（重跑三次都没再复现，属于瞬时抖动）。
	// 多给几次机会，比"宁可错杀"划算得多。
	verifyAttempts = 3
	verifyGap      = 1500 * time.Millisecond

	// verifyBudget 是验证**一个候选后端**的总预算（含重试与间隔）。
	verifyBudget = 40 * time.Second

	// exitProbeTimeout 是探**单一地址族**出口的单次超时。
	//
	// 比 verifyTimeout 略短：出口 IP 只是个展示字段，为它多等一秒都不值。
	exitProbeTimeout = 5 * time.Second

	// exitProbeBudget 是"探一轮公网出口 IP"的总预算。
	//
	// 它是**两族共用**的一份预算（不是各给一份），而且两族是**并行**探的：
	// 串行时"v6 不通"这一常见情况会让每次 up 都白等一个完整超时。
	// 上限比原值（8s）略放宽，因为现在要覆盖两族；正常情况两族都在 1 秒内回来。
	//
	// 它比 verifyBudget 小得多：这只是一次 trace 请求，且**失败无害**
	// （留空即可，菜单会显示"出口 IP 未知"）—— 不该为了一个装饰性字段
	// 把 `up` 拖慢，更不该让它成为 `up` 失败的第二个原因。
	exitProbeBudget = 10 * time.Second
)

// anyPrefix4 / anyPrefix6 是两族的全量 AllowedIPs。
var (
	anyPrefix4 = netip.MustParsePrefix("0.0.0.0/0")
	anyPrefix6 = netip.MustParsePrefix("::/0")
)

// ensureOptions 是一次"让隧道达到期望状态"的全部参数。
//
// `up` 与 `watch` 共用同一份：两条路径必须走**完全一样**的代码 ——
// 否则"手工执行能修好、保活却修不好"这类偏差迟早会出现，而且极难发现。
type ensureOptions struct {
	IfName   string
	Mode     netcfg.Mode
	MTU      int
	Endpoint string

	// Backend 是**用户请求的**后端；空值（BackendAuto）表示由程序决定 ——
	// 见 resolveBackends。实际用上的那个由 ensureTunnel 逐个尝试后确定。
	Backend tunnel.Backend

	// Stack 是隧道携带哪几族地址（上游菜单 `4` / `6` / `d`）。
	//
	// 空值表示"沿用上次的选择"（见 ensureTunnel 开头的继承逻辑），
	// 于是 `watch` / `switch` / `rotate` 不必各自去读运行态。
	Stack netcfg.Stack

	// ProbeAfterEnsure：建好之后要不要立刻验证"经隧道真的能出网"。
	//
	// `up` 打开它（用户要的是"能用"，而且一旦不能用就该去试另一个后端）；
	// `watch` **关闭**它 —— 巡检刚从"不通"里恢复，立刻再探一次既是重复劳动，
	// 也会把"我已经把它修好了"误报成"修不好"（它下一轮本来就会体检）。
	// 全局模式不需要它：那边自带自检。
	ProbeAfterEnsure bool

	// Force 表示先拆再建（无条件重建）。
	Force bool

	// GuardTimeout 是全局模式回滚保险丝的超时。
	GuardTimeout time.Duration

	// NoConfirm 表示全局模式应用后不自动确认（把决定权留给用户）。
	NoConfirm bool

	// NoFuse 关闭全局模式的保险丝。只用于测试；生产路径永远开着。
	NoFuse bool

	// Quiet 把"例行公事"的日志降到 debug —— 保活每 5 分钟跑一次，
	// 每次都刷一遍"已添加规则"会把 journal 淹没，真正的异常反而看不见。
	Quiet bool

	// ResetIfIdle：当接口在、配置也完全一致、但链路依然不通时，
	// 做一次硬复位强制重新握手。
	//
	// 这正是"断链才重建"里"重建"的含义。没有它，保活就只会重建
	// **被删掉的**接口 —— 而现实中最常见的故障是"接口好好的、就是不通"。
	ResetIfIdle bool

	// DNS 非空时把 /etc/resolv.conf 换成这些服务器（会先备份原文）。
	DNS []string
}

// ensureResult 记录这次做到了什么。
type ensureResult struct {
	Plan   *netcfg.Plan
	Change tunnel.Change
	Net    *netcfg.Result
	RT     *state.Runtime
	// Backend 是这次实际使用的后端。
	Backend tunnel.Backend
	// HardReset 报告是否做过硬复位。
	HardReset bool
	// Detail 是一行摘要（给日志与保活用）。
	Detail string
}

// resolveBackends 决定这次按什么顺序尝试隧道后端。
//
// 四条规则，对应四种情形：
//
//  1. 用户显式指定了后端 → 只有一个候选，**不做任何回落**。回落会让
//     `--backend kernel` 变成一个"建议"，用户看到的状态就与命令行说的不一样了。
//  2. 没指定、且账户没有 reserved → 内核。行为与 M2/M4 逐字节一致。
//  3. 没指定、且账户带 reserved → 内核优先，用户态作为备选。
//  4. 没指定、带 reserved、且**现在跑着的就是用户态** → 用户态优先。
//
// 第 4 条是第 3 条的补充，也是真机验收逼出来的：`up` 与 `watch` 每次都会
// 重新决策一遍，如果顺序永远固定成"内核优先"，那么一台**只能用用户态**的机器
// 每修一次链路就要先把可用的用户态隧道拆掉、去试一个已知不通的内核后端、
// 验证失败后再拆掉重建回用户态 —— 多花一倍时间，还平白重建一次接口。
//
// 于是 auto 的语义精确表述为"**在两种后端之间自适应**"，而不是"永远先试内核"。
// 想换回内核随时可以 `up --backend kernel` 或 `switch --backend kernel`。
func resolveBackends(requested tunnel.Backend, r cloudflare.Reserved, current tunnel.Backend) []tunnel.Backend {
	if requested.Valid() {
		return []tunnel.Backend{requested}
	}
	if r.IsZero() {
		return []tunnel.Backend{tunnel.BackendKernel}
	}
	if current == tunnel.BackendUserspace {
		return []tunnel.Backend{tunnel.BackendUserspace, tunnel.BackendKernel}
	}
	return []tunnel.Backend{tunnel.BackendKernel, tunnel.BackendUserspace}
}

// backendLabel 把后端名翻成给人看的短标签。
//
// 映射放在 CLI 层而不是 tunnel 包里：库层只该产出可机读的名字
// （runtime.json 里存的就是 kernel/userspace），文案归文案。
func backendLabel(be tunnel.Backend) string {
	switch be {
	case tunnel.BackendUserspace:
		return msg.T(msg.KeyBackendUserspace)
	case tunnel.BackendKernel:
		return msg.T(msg.KeyBackendKernel)
	default:
		return msg.T(msg.KeyBackendAuto)
	}
}

// ensureTunnel 把系统带到"隧道可用"的状态。
//
// 它**不取单实例锁** —— 锁由调用方（up / watch）持有，因为持锁的粒度
// 是"一次完整的动作"，而不是"其中一段"。调用方拿不到锁时不应该进到这里。
//
// 后端选择在这一层：先按 resolveBackends 定候选顺序，然后逐个尝试。
// 只有"候选不止一个"时才会在成功之后多做一次体检 —— 单候选的机器
// （也就是所有不带 reserved 的机器）行为与 M2/M4 完全一致，不多花一毫秒。
func (a *app) ensureTunnel(ctx context.Context, acct *cloudflare.Account, opt ensureOptions) (*ensureResult, int) {
	// 没显式指定栈就**沿用上次的选择**。
	//
	// 集中在这一处而不是让每个调用方自己填：`watch` 重建、`switch` 互换、
	// `rotate` 换 IP 都会走到这里，任何一处漏了都会把用户选的"只用 IPv4"
	// 悄悄改回双栈 —— 而那是很难发现的一类回归（隧道照样通，只是多了一族）。
	if opt.Stack == "" {
		if rt, err := state.LoadRuntime(state.RuntimePath()); err == nil {
			opt.Stack = netcfg.Stack(rt.Stack).Normalize()
		} else {
			opt.Stack = netcfg.StackBoth
		}
	}

	// auto 会沿用"现在跑着的那个后端"（见 resolveBackends 的第 4 条）。
	// 读不到运行态（第一次运行、刚 down 过）就当没有偏好。
	current := tunnel.BackendAuto
	if rt, err := state.LoadRuntime(state.RuntimePath()); err == nil {
		current = tunnel.Backend(rt.Backend)
	}
	cands := resolveBackends(opt.Backend, acct.Reserved, current)
	// 只有"要在多种后端之间做选择"时才在成功之后多探一次；
	// 单候选（含用户钉死后端）与全局模式的机器完全不多花这一毫秒。
	probe := opt.ProbeAfterEnsure && len(cands) > 1 && opt.Mode != netcfg.ModeGlobal

	for i, be := range cands {
		cur := opt
		if i > 0 {
			// 换后端必须**先拆干净再重来**：内核的 wireguard 接口与用户态的
			// TUN 是不同的设备类型，前一次留下的接口会让后一次直接失败。
			cur.Force = true
		}

		res, code := a.ensureTunnelOnce(ctx, acct, cur, be)
		if code != exitOK {
			if i == len(cands)-1 {
				// 单候选、且账户带 reserved、且用户钉死了内核 ——
				// 这正是设计文档里退出码 3 的含义："需要 reserved 但后端不支持"。
				if len(cands) == 1 && be == tunnel.BackendKernel && !acct.Reserved.IsZero() {
					a.log.Errorf("%s", msg.Tf(msg.KeyUpNeedReserved, acct.Reserved))
					return nil, exitNeedReserved
				}
				return nil, code
			}
			a.log.Warnf("%s", msg.Tf(msg.KeyUpBackendFallback, backendLabel(be), exitCodeReason(code)))
			continue
		}

		if !probe {
			return res, exitOK
		}

		detail, err := a.verifyViaTunnel(ctx, acct, res)
		if err == nil {
			if i > 0 {
				a.log.Infof("%s", msg.Tf(msg.KeyUpBackendSwitched, backendLabel(be), detail))
			} else {
				a.log.Debugf("%s", msg.Tf(msg.KeyUpBackendOK, backendLabel(be), detail))
			}
			return res, exitOK
		}

		if i == len(cands)-1 {
			// 最后一个候选也验不过 —— 如实说不通，而不是"命令成功但网不通"。
			a.log.Errorf("%s", msg.Tf(msg.KeyUpBackendUnfitAll, err))
			return nil, exitUnhealthy
		}
		a.log.Warnf("%s", msg.Tf(msg.KeyUpBackendUnfit, backendLabel(be), err))
	}
	// 候选列表为空只可能来自编程错误，但宁可给一个明确的错误码。
	return nil, exitError
}

// exitCodeReason 把退出码翻译成一句能给日志用的话。
func exitCodeReason(code int) string {
	switch code {
	case exitUnhealthy:
		return msg.T(msg.KeyUpReasonUnhealthy)
	case exitNeedReserved:
		return msg.T(msg.KeyUpReasonNeedReserved)
	case exitLockBusy:
		return msg.T(msg.KeyUpReasonLockBusy)
	case exitNotDebian:
		return msg.T(msg.KeyUpReasonNotDebian)
	default:
		return msg.T(msg.KeyUpReasonError)
	}
}

// ensureTunnelOnce 用**一个**确定的后端把系统带到"隧道可用"的状态。
//
// be 是这次实际使用的后端；opt.Backend 里的原值（可能是 auto）会被记进运行态。
func (a *app) ensureTunnelOnce(ctx context.Context, acct *cloudflare.Account, opt ensureOptions, be tunnel.Backend) (*ensureResult, int) {
	if opt.IfName == "" {
		opt.IfName = netcfg.DefaultIfName
	}
	if opt.MTU == 0 {
		opt.MTU = netcfg.DefaultMTU
	}
	if !be.Valid() {
		be = tunnel.BackendKernel
	}

	cfg, plan, rtState, err := buildTunnel(acct, opt, be)
	if err != nil {
		return nil, a.fail(err)
	}

	// 内核后端带不了 reserved —— 这是结构性的，不是缺陷。当账户确实带它
	// 而我们又在用内核时，把这件事说清楚：用户需要知道"报文里发的是 0,0,0"，
	// 否则一旦连不上就完全没有线索。
	if be == tunnel.BackendKernel && !acct.Reserved.IsZero() {
		a.log.Warnf("%s", msg.Tf(msg.KeyUpPinnedKernel, acct.Reserved))
	}
	if be == tunnel.BackendUserspace {
		a.say(opt.Quiet, msg.KeyUpUserspaceReserved, acct.Reserved)
	}

	// 上一次变更可能留下了还在等待的保险丝（比如上次自检失败但进程还没退出）：
	// 先解除它。否则它会在超时后把**这次**刚建立的东西拆掉，
	// 表现成"莫名其妙掉线"，而且极难排查。
	if derr := rollback.Disarm(); derr != nil {
		a.log.Warnf("%v", derr)
	}

	guardTimeout := opt.GuardTimeout
	if guardTimeout <= 0 {
		guardTimeout = rollback.DefaultTimeout
	}
	useFuse := opt.Mode == netcfg.ModeGlobal && !opt.NoFuse

	// ★ 全局模式的前置：**先上保险，再动网络**。
	//
	// 顺序不能反。保险丝的整个意义就是"当变更把网络弄断时，还有人在数秒后拉闸"；
	// 如果先改网络再派生保险丝，那么断网那一刻保险丝还没出生，等于没有保险。
	var snap *rollback.Snapshot
	if useFuse {
		if snap, err = newSnapshot(cfg.IfName, plan); err != nil {
			return nil, a.fail(err)
		}
		// 注意这里**不再**"先把确认标记删掉"。
		//
		// 标记现在自带身份（那一次变更的 CreatedAt），遗留一份不会影响谁；
		// 而在这里删掉它反而会把上面那次 Disarm 刚写下的标记吃掉 ——
		// 上一次变更的保险丝就再也等不到解除，只能守到自己的超时。
		if err := snap.Save(rollback.SnapshotPath()); err != nil {
			return nil, a.fail(err)
		}
		if err := a.spawnGuard(guardTimeout); err != nil {
			return nil, a.fail(err)
		}
		a.log.Warnf("%s", msg.Tf(msg.KeyUpGuardSpawned, guardTimeout))
		a.log.Warnf("%s", msg.T(msg.KeyUpGlobalStart))
	}

	if opt.Force {
		a.say(opt.Quiet, msg.KeyUpForce)
		if _, _, err := a.tearDown(cfg.IfName); err != nil {
			a.log.Warnf("%v", err)
		}
	}

	a.say(opt.Quiet, msg.KeyUpStart, opt.Mode, backendLabel(be), cfg.IfName)

	tun, err := tunnel.New(be, cfg.IfName)
	if err != nil {
		return nil, a.fail(err)
	}
	defer tun.Close()

	ch, err := tun.Ensure(ctx, cfg)
	if err != nil {
		return nil, a.fail(err)
	}
	a.reportChange(opt.Quiet, ch, cfg)

	// 接口在、配置也对，但链路就是不通 —— 这是最常见的故障形态
	// （WARP 把当前握手黑洞掉了）。这时唯一的解法是硬复位一次，
	// 逼它重新握手。注意：出口 IP 不会因此改变 —— 它是账户里分配的固定地址，
	// 变的只是这一次握手。
	res := &ensureResult{Plan: plan, Change: ch, Backend: be}
	if opt.ResetIfIdle && !ch.LinkCreated && ch.ConfigMatched {
		a.say(opt.Quiet, msg.KeyUpHardReset)
		if _, _, err := a.tearDown(cfg.IfName); err != nil {
			return nil, a.fail(err)
		}
		if ch, err = tun.Ensure(ctx, cfg); err != nil {
			return nil, a.fail(err)
		}
		a.reportChange(opt.Quiet, ch, cfg)
		res.HardReset = true
		res.Change = ch
	}

	nres, err := netcfg.Apply(plan, a.netlog)
	if err != nil {
		return nil, a.fail(err)
	}
	res.Net = nres
	if len(nres.RulesRemoved) > 0 {
		a.say(opt.Quiet, msg.KeyUpCleaned, len(nres.RulesRemoved))
	}
	a.say(opt.Quiet, msg.KeyUpNetcfg, nres.RoutesAdded, nres.RulesAdded)
	a.say(opt.Quiet, msg.KeyUpAddrs, strings.Join(plan.AddrStrings(), ", "))

	if len(opt.DNS) > 0 {
		if err := a.applyDNS(opt.DNS); err != nil {
			return nil, a.fail(err)
		}
		a.log.Infof("%s", msg.Tf(msg.KeyDNSSet, strings.Join(opt.DNS, ", ")))
	}

	if err := rtState.Save(state.RuntimePath()); err != nil {
		return nil, a.fail(err)
	}
	res.RT = rtState

	if useFuse {
		if code := a.finishGlobal(ctx, snap, plan, guardTimeout, opt.NoConfirm); code != exitOK {
			return nil, code
		}
	}

	// 隧道确实建成之后，顺手探一次"公网看到的出口"并记进运行态。
	//
	// 放在最后、且在**非致命**路径上：这一步只为了让菜单能回答
	// "我现在从哪个 IP 出去"，探不到就留空 —— 绝不能因为它让一次成功的 up 变成失败。
	a.recordExitIP(ctx, rtState, opt.Quiet)

	res.Detail = msg.Tf(msg.KeyUpDone, opt.Mode)
	return res, exitOK
}

// probeExit 探一次"经隧道出去时公网看到的出口"，返回**第一个通的族**。
//
// 只读。**必须确认 warp=on 才采信**：否则拿到的可能是宿主自己的出口地址，
// 把它当成"WARP 出口"汇报会得出完全错误的结论（rotate 那边就是靠这条
// 才敢说"IP 一点没变"）。
//
// 只要一个值（"出口 IP 是多少、变了没有"）时用它，比如 `rotate`；
// 要把两族**各记一份**时用 probeExitFamily —— `ProbeAny` 遇到第一个通的族
// 就返回，双栈机器上永远只拿得到 v4 那条，菜单里正是因此只显示了 IPv4。
func (a *app) probeExit(ctx context.Context, ifName string, families []int) (health.Result, bool) {
	if !netcfg.LinkExists(ifName) {
		return health.Result{}, false
	}
	res, err := health.ProbeAny(ctx, families, health.Options{
		Interface: ifName,
		Timeout:   verifyTimeout,
	})
	if err != nil || !res.WarpOn() {
		return health.Result{}, false
	}
	return res, true
}

// probeExitFamily 探**单一地址族**的出口。
func (a *app) probeExitFamily(ctx context.Context, ifName string, family int) (health.Result, bool) {
	if !netcfg.LinkExists(ifName) {
		return health.Result{}, false
	}
	res, err := health.Probe(ctx, family, health.Options{
		Interface: ifName,
		Timeout:   exitProbeTimeout,
	})
	if err != nil || !res.WarpOn() {
		return health.Result{}, false
	}
	return res, true
}

// recordExitIP 把这次体检看到的出口记进运行态，**每一族各一条**。
//
// 为什么值得花两次探测：菜单顶部要显示"你的出口 IP"，而菜单本身不许联网
// （见 state.Runtime 的 ExitV4 注释），所以真实值必须由会联网的那一方写下来。
// 双栈机器上 v4 与 v6 是两个不同的答案（实测可以落在两个不同的 PoP），
// 只记一个等于答了一半 —— 用户看到的就是"IPv6 那一行是空的"。
func (a *app) recordExitIP(ctx context.Context, rt *state.Runtime, quiet bool) {
	if rt == nil {
		return
	}
	families := rt.Families
	if len(families) == 0 {
		families = []int{state.FamilyV4, state.FamilyV6}
	}
	probeCtx, cancel := context.WithTimeout(ctx, exitProbeBudget)
	defer cancel()

	// 两族并行：它们互不依赖，而串行时"v6 不通"（常见）会让每次 up 都白等
	// 一个完整超时。用 buffer = len(families) 的 channel，即便某个 goroutine
	// 超时后仍在跑，也不会漏掉接收、更不会因无人接收而永久阻塞。
	type outcome struct {
		res health.Result
		ok  bool
	}
	ch := make(chan outcome, len(families))
	for _, fam := range families {
		go func(fam int) {
			res, ok := a.probeExitFamily(probeCtx, rt.IfName, fam)
			ch <- outcome{res: res, ok: ok}
		}(fam)
	}
	got := map[int]health.Result{}
	for range families {
		o := <-ch
		if o.ok {
			got[o.res.Family] = o.res
		}
	}

	// 落盘顺序按 families 来，不按 goroutine 的完成顺序 —— 否则日志里
	// "先 IPv6 后 IPv4"会随调度漂移，看着像出了岔子。
	now := time.Now().UTC()
	var parts []string
	for _, fam := range families {
		res, ok := got[fam]
		if !ok {
			continue
		}
		rt.SetExit(fam, res.IP, res.Location, now)
		parts = append(parts, fmt.Sprintf("%s %s", health.FamilyName(fam), exitDetail(res.IP, res.Location)))
	}
	if len(parts) == 0 {
		a.log.Debugf("%s", msg.T(msg.KeyUpExitUnknown))
		return
	}
	if err := rt.Save(state.RuntimePath()); err != nil {
		a.log.Debugf("记录出口 IP 失败: %v", err)
		return
	}
	a.say(quiet, msg.KeyUpExitIP, strings.Join(parts, " · "))
}

// saveExit 把**一族**的探测结果写进运行态文件（保活巡检用）。
//
// 写失败只记 debug：它只是个展示字段，不该影响调用方的主流程。
func (a *app) saveExit(rt *state.Runtime, family int, res health.Result) bool {
	rt.SetExit(family, res.IP, res.Location, time.Now().UTC())
	if err := rt.Save(state.RuntimePath()); err != nil {
		a.log.Debugf("记录出口 IP 失败: %v", err)
		return false
	}
	return true
}

// recordExitFrom 把保活**已经拿到**的探测结果记进运行态。
//
// 保活每轮本来就要探一次连通性，顺手更新这一项 —— 否则菜单里的"出口 IP"
// 会一直停在最后一次 up 的时刻，看久了就像个错的值。每轮多一次几百字节的
// 原子写，换来的是这一行始终与最近一次体检一致，值这个价。
//
// 只更新**探到的那一族**：保活一轮只探一族（`ProbeAny`），把另一族一起清掉
// 会让菜单里的 IPv6 那一行随机消失 —— 而保活的本意是"巡检连通性"，
// 不是"顺手把别人记的数据抹了"。
func (a *app) recordExitFrom(res health.Result) {
	if res.IP == "" {
		return
	}
	if res.Family != state.FamilyV4 && res.Family != state.FamilyV6 {
		return
	}
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil {
		return
	}
	_ = a.saveExit(rt, res.Family, res)
}

// verifyViaTunnel 在隧道刚建好之后做一次只读体检：至少一族能经接口拿到 warp=on。
//
// 它只在"候选不止一个后端"且调用方要求体检时被调用 —— 也就是只有带 reserved 的
// 账户在 `up` / `switch` 时会多花这几秒钟。理由很直接：那种账户才有
// "这个后端到底行不行"的问题要回答。
//
// 会重试（见 verifyAttempts）：这里的结论会直接决定"要不要换一个后端重建"，
// 而误判的代价是拆掉一条本来可用的隧道。
func (a *app) verifyViaTunnel(ctx context.Context, acct *cloudflare.Account, res *ensureResult) (string, error) {
	if res == nil || res.Plan == nil {
		return "", errors.New("缺少网络计划，无法体检")
	}
	families := res.Plan.Families
	if len(families) == 0 {
		families = accountFamilies(acct)
	}
	ctx, cancel := context.WithTimeout(ctx, verifyBudget)
	defer cancel()

	var last error
	for i := 0; i < verifyAttempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return "", last
			case <-time.After(verifyGap):
			}
		}
		r, err := health.ProbeAny(ctx, families, health.Options{
			Interface: res.Plan.IfName,
			Timeout:   verifyTimeout,
		})
		if err == nil {
			if i > 0 {
				a.log.Debugf("第 %d 次体检才通过（前几次是瞬时抖动）", i+1)
			}
			return r.String(), nil
		}
		last = err
	}
	return "", last
}

// say 按 Quiet 决定是 info 还是 debug。
//
// 保活每 5 分钟跑一轮，例行成功的输出如果都用 info，journal 会被
// "已添加规则…" 淹没；而真正要看"它到底做了什么"的时候，`-v` 就能看到全部。
func (a *app) say(quiet bool, key string, args ...any) {
	if quiet {
		a.log.Debugf("%s", msg.Tf(key, args...))
		return
	}
	a.log.Infof("%s", msg.Tf(key, args...))
}

// buildTunnel 把账户翻译成「隧道参数 + 期望网络状态 + 运行态」。
//
// 三样东西一起产出，是因为它们必须来自同一次解析 —— 分开算容易出现
// "隧道用一个端点、运行态里记另一个"这种对不上的情况。
func buildTunnel(acct *cloudflare.Account, opt ensureOptions, be tunnel.Backend) (tunnel.Config, *netcfg.Plan, *state.Runtime, error) {
	var zeroCfg tunnel.Config

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
	addrs, err = applyStack(addrs, opt.Stack)
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
		IfName:        opt.IfName,
		PrivateKey:    priv,
		PublicKey:     pub,
		PeerPublicKey: peer,
		Endpoint:      endpoint,
		AllowedIPs:    defaultAllowedIPs(addrs),
		Keepalive:     defaultKeepalive,
		MTU:           opt.MTU,
		Addresses:     addrs,
		Reserved:      acct.Reserved,
	}

	plan, err := netcfg.Build(addrs, netcfg.Options{IfName: opt.IfName, MTU: opt.MTU, Mode: opt.Mode})
	if err != nil {
		return zeroCfg, nil, nil, err
	}
	// 全局模式要求隧道自己打 fwmark，否则封装包会被自己的规则送进隧道（死循环）。
	cfg.FWMark = plan.FWMark

	rtState := &state.Runtime{
		IfName:           opt.IfName,
		Mode:             string(opt.Mode),
		Backend:          string(be),
		BackendRequested: string(opt.Backend),
		MTU:              opt.MTU,
		Table:            netcfg.TableWarp,
		FWMark:           plan.FWMark,
		Families:         plan.Families,
		Addrs:            plan.AddrStrings(),
		Endpoint:         endpoint,
		// 栈要记下来：`watch` 重建、`switch` 互换、`rotate` 换 IP 都靠它
		// 保持"用户选的只用 IPv4"不被悄悄改回双栈。
		Stack: string(opt.Stack.Normalize()),
		// 记下这次改过 DNS 没有 —— `off` / `on` 要靠它把解析器也一起还原。
		DNS:     opt.DNS,
		UpSince: time.Now().UTC(),
	}
	return cfg, plan, rtState, nil
}

// applyStack 按栈过滤账户地址（上游菜单 `4` / `6` / `d`）。
//
// 双栈时**原样返回同一个切片**：那是最常见的路径，不该被过滤逻辑碰到。
func applyStack(addrs []netip.Prefix, stack netcfg.Stack) ([]netip.Prefix, error) {
	stack = stack.Normalize()
	if stack.Families() == nil {
		return addrs, nil
	}
	out := make([]netip.Prefix, 0, len(addrs))
	for _, a := range addrs {
		if a.IsValid() && stack.Allows(netcfg.FamilyOfAddr(a.Addr())) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		// 说清"是账户里没有这一族"，而不是让 netcfg.Build 报一句
		// "账户里没有任何接口地址" —— 后者会让人去查账户，而账户其实是好的。
		return nil, fmt.Errorf("账户里没有 %s 的地址，装不了 --stack %s", stackFamilyName(stack), stack)
	}
	return out, nil
}

// stackFamilyName 把栈名说成人话（错误信息里用）。
func stackFamilyName(stack netcfg.Stack) string {
	switch stack.Normalize() {
	case netcfg.StackV4:
		return "IPv4"
	case netcfg.StackV6:
		return "IPv6"
	default:
		return "任何地址族"
	}
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

// accountFamilies 返回账户里实际存在的地址族（用于体检时决定探哪几族）。
func accountFamilies(acct *cloudflare.Account) []int {
	var out []int
	addrs, err := accountPrefixes(acct)
	if err != nil {
		return []int{health.FamilyV4, health.FamilyV6}
	}
	seen4, seen6 := false, false
	for _, a := range addrs {
		if a.Addr().Is4() {
			if !seen4 {
				seen4 = true
				out = append(out, health.FamilyV4)
			}
			continue
		}
		if !seen6 {
			seen6 = true
			out = append(out, health.FamilyV6)
		}
	}
	if len(out) == 0 {
		return []int{health.FamilyV4, health.FamilyV6}
	}
	return out
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
func (a *app) reportChange(quiet bool, ch tunnel.Change, cfg tunnel.Config) {
	switch {
	case ch.DaemonStarted:
		a.say(quiet, msg.KeyUpDaemonStarted, cfg.IfName, cfg.MTU)
	case ch.LinkCreated:
		a.say(quiet, msg.KeyUpLinkCreated, cfg.IfName, cfg.MTU)
	default:
		// 复用时明确说出来：用户最关心的恰恰是"这次有没有换 IP"。
		a.say(quiet, msg.KeyUpLinkReused, cfg.IfName)
	}
	switch {
	case ch.Configured:
		a.say(quiet, msg.KeyUpConfigured)
	case ch.ConfigMatched:
		a.say(quiet, msg.KeyUpCfgMatched)
	}
}

// netlog 把 netcfg 的明细日志降到 debug 级别：默认输出只留汇总，
// 需要排障时 `-v` 就能看到每一条规则/路由。
func (a *app) netlog(format string, args ...any) {
	a.log.Debugf(format, args...)
}

// finishGlobal 是全局模式的后置：自检通过才确认，否则立刻回滚。
//
// 为什么"自检失败要立刻回滚"而不是等保险丝超时：早点回滚能把断网窗口从
// 45 秒缩到几秒。保险丝是给"连这段代码都没跑完"的极端情况兜底的。
func (a *app) finishGlobal(ctx context.Context, snap *rollback.Snapshot, plan *netcfg.Plan, timeout time.Duration, noConfirm bool) int {
	if noConfirm {
		a.log.Warnf("%s", msg.Tf(msg.KeyUpNoConfirm, timeout))
		return exitOK
	}

	a.log.Infof("%s", msg.T(msg.KeyUpChecking))
	// 自检有独立的预算，且必须明显小于保险丝超时（见 selfCheckBudget 的说明）。
	checkCtx, cancel := context.WithTimeout(ctx, selfCheckBudget)
	defer cancel()
	detail, err := a.selfCheck(checkCtx, plan)
	if err != nil {
		a.log.Errorf("%s", msg.Tf(msg.KeyUpCheckFail, err))
		a.log.Warnf("%s", msg.T(msg.KeyUpRollingBack))
		if rerr := rollback.Rollback(snap, a.guardLog); rerr != nil {
			return a.fail(rerr)
		}
		// 回滚完了就要解除保险丝 —— 否则它超时后会再回滚一次，
		// 把用户随后建立的隧道拆掉（见 rollback.Disarm 的说明）。
		if derr := rollback.Disarm(); derr != nil {
			a.log.Warnf("%v", derr)
		}
		_ = rollback.ClearSnapshot()
		return exitUnhealthy
	}
	a.log.Infof("%s", msg.Tf(msg.KeyUpCheckOK, detail))
	if err := rollback.Confirm(); err != nil {
		return a.fail(err)
	}
	_ = rollback.ClearSnapshot()
	a.log.Infof("%s", msg.T(msg.KeyUpGlobalDone))
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
		a.log.Errorf("%s", msg.Tf(msg.KeyLockBusy, holderPID(state.DefaultLockPath)))
		return nil, exitLockBusy
	}
	a.reportTakeover(lock)
	return lock, exitOK
}

// tryLock 尝试取锁；被占时返回 ok=false（不报错）。
//
// 巡检专用的语义：锁被别人占着是**正常情形**（用户可能正在手工 up），
// 本轮跳过就好，不该让保活在 journal 里刷错误。
func (a *app) tryLock() (release func(), ok bool) {
	if err := os.MkdirAll(filepath.Dir(state.DefaultLockPath), 0o700); err != nil {
		a.log.Warnf("准备锁目录: %v", err)
		return nil, false
	}
	lock, st, err := state.Acquire(state.DefaultLockPath, state.DefaultStaleAfter)
	if err != nil {
		a.log.Warnf("获取单实例锁: %v", err)
		return nil, false
	}
	if st == state.StatusBusy {
		a.log.Infof("%s", msg.Tf(msg.KeyLockBusy, holderPID(state.DefaultLockPath)))
		return nil, false
	}
	a.reportTakeover(lock)
	return func() { a.releaseLock(lock) }, true
}

func (a *app) releaseLock(lock *state.Lock) {
	if lock == nil {
		return
	}
	if err := lock.Release(); err != nil {
		a.log.Warnf("release lock: %v", err)
	}
}

// tearDown 清规则/路由 + 删接口 + 停用户态守护进程。
//
// 返回 (清理统计, 是否真的删掉了接口, error)。
//
// 这里刻意**不依赖"当前是哪种后端"**：删接口对两种后端都是同一个动作
// （用户态那条 TUN 也是一个普通接口），而停守护进程在没有守护进程时是空操作。
// 于是 down / uninstall 不必先去猜现场是什么状态 —— 猜错的代价是清不干净。
func (a *app) tearDown(ifName string) (*netcfg.Result, bool, error) {
	res, err := netcfg.Cleanup(a.netlog)
	if err != nil {
		return res, false, err
	}
	if ifName == "" {
		ifName = netcfg.DefaultIfName
	}
	// ① 删接口。删掉 TUN 会让 wireguard-go 的读循环报错、进而自己退出。
	deleted, err := netcfg.DeleteLink(ifName)
	if err != nil {
		return res, deleted, err
	}
	// ② 收尾：停掉万一还活着的守护进程，清掉状态文件与残留 socket。
	if serr := tunnel.StopUserspaceDaemon(ifName, a.netlog); serr != nil {
		return res, deleted, serr
	}
	return res, deleted, nil
}
