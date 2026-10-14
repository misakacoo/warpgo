// Package keepalive 是"巡检"这件事的策略与循环。
//
// 语义只有一句话：**健康就不动，断了才重建**。
//
// 这句话看着平淡，却是上游最贵的一课。上游的保活每 5 分钟无条件把接口拆掉重建，
// 于是出口 IP 一直在变 —— 用户访问的网站、账号系统看到的都是一个不断跳动的身份。
// 更糟的是重建窗口里接口短暂消失，"拿不到 IP" 被误判成"没安装"，又触发更多重建。
//
// 所以本包把顺序钉死：
//
//	先只读体检 → 健康就到此为止（一个字节都不改）→ 不健康才重建
//
// 本包刻意**不碰任何系统调用**：体检、重建、加锁、休眠全部由调用方注入。
// 这样四条分支（健康 / 断链 / 被锁挡住 / 重建失败）都能在不碰真实网络的前提下单测，
// 而"不碰真实网络"正是本项目唯一安全的测试方式。
//
// 库层只**发事件**、不拼文案：文案要跟随用户语言，属于 CLI 层的职责
// （与 internal/rollback 同一约定）。
package keepalive

import (
	"context"
	"errors"
	"time"
)

// DefaultInterval 是默认巡检间隔。
//
// 5 分钟是上游与整个生态的共识值：WARP 的 NAT 映射不会活得比这更久，
// 而更频繁的巡检除了增加噪声没有任何好处。
const DefaultInterval = 5 * time.Minute

// MinInterval 是允许的最小间隔。
//
// 下限的意义不是"省点 CPU"，而是防止误配置把保活变成一个压测器：
// 每轮都要做网络探测，间隔过小会让本机变成持续扫描 Cloudflare 的机器。
const MinInterval = 10 * time.Second

// Outcome 是一轮巡检的结论。
type Outcome int

const (
	// OutcomeSkipped：本轮什么都没做（单实例锁被别人占着）。
	OutcomeSkipped Outcome = iota
	// OutcomeHealthy：体检通过，刻意一个字节都没改。
	OutcomeHealthy
	// OutcomeRebuilt：体检不通过，已重建。
	OutcomeRebuilt
	// OutcomeRebuildFailed：体检不通过，重建也失败了（会继续等下一轮）。
	OutcomeRebuildFailed
)

// Healthy 报告这个结论是否意味着"链路可用"。
func (o Outcome) Healthy() bool { return o == OutcomeHealthy || o == OutcomeRebuilt }

// Event 是巡检过程中的一个可报告事件。
type Event int

const (
	// EvRoundStart 一轮开始（参数：轮次）。
	EvRoundStart Event = iota
	// EvHealthy 体检通过，未做任何改动（参数：体检摘要）。
	EvHealthy
	// EvUnhealthy 体检不通过（参数：失败原因）。
	EvUnhealthy
	// EvRebuilding 正在重建。
	EvRebuilding
	// EvRebuilt 重建完成（参数：重建摘要）。
	EvRebuilt
	// EvRebuildFailed 重建失败（参数：错误）。**不是致命错误** ——
	// 下一轮还会再试，这样 systemd 不会陷入"退出→重启"的循环。
	EvRebuildFailed
	// EvLocked 单实例锁被占，本轮跳过（参数：轮次）。
	EvLocked
	// EvStopped 巡检结束（参数：汇总）。
	EvStopped
)

// Logger 接收巡检事件。
type Logger func(Event, ...any)

func nopLogger(Event, ...any) {}

// Deps 是巡检的全部外部依赖。
//
// 每一项都可注入，是本包能在单测里覆盖全部分支的原因。
type Deps struct {
	// Check 做只读体检：返回 nil 表示链路健康，返回错误时错误本身就是"为什么不健康"。
	//
	// 它必须**只读** —— 体检一旦会动系统，"探测"就变成了"故障放大器"。
	Check func(ctx context.Context) (detail string, err error)

	// Rebuild 重建隧道。只在 Check 判定不健康时才会被调用。
	Rebuild func(ctx context.Context) (detail string, err error)

	// Lock 尝试获取单实例锁。ok 为 false 表示锁被别人占着 ——
	// 这是**正常情形**（比如用户正好在手工执行 up），本轮跳过即可，不算错误。
	// release 可能为 nil。
	Lock func() (release func(), ok bool)

	// Sleep 等待一个周期，返回 false 表示被 ctx 取消。默认用可中断的定时器。
	Sleep func(ctx context.Context, d time.Duration) bool

	// Log 接收事件。
	Log Logger
}

// Config 是巡检参数。
type Config struct {
	// Interval 是两轮之间的间隔。
	Interval time.Duration

	// Once 表示只跑一轮就返回 —— "立刻检查并修好"的语义，
	// 也是真机验收能精确断言"一轮内自愈"的前提。
	Once bool

	// MaxRounds 限制总轮数（0 = 不限）。仅测试用。
	MaxRounds int
}

// Summary 是巡检的汇总。
type Summary struct {
	Rounds  int
	Healthy int
	Rebuilt int
	Failed  int
	Skipped int
	// Last 是最后一轮的结论；没跑过任何一轮时为零值（OutcomeSkipped）。
	Last Outcome
	// Ran 报告是否至少跑过一轮 —— 用来区分"被取消"与"跑完了一轮"。
	Ran bool
}

// IsHealthy 报告巡检结束时链路是否可用。
//
// 刻意不叫 Healthy()：Summary 里已经有一个同名的计数字段，
// 同名会让人分不清"有多少轮健康"与"最后是否健康"。
func (s Summary) IsHealthy() bool { return s.Ran && s.Last.Healthy() }

// Run 执行巡检循环。
//
// 只有**配置错误**才会返回 error。链路问题一律走事件上报，因为
// 巡检进程必须活下去：它一旦退出，systemd 就会重启它，而重启并不能让
// "端点不可达"这类问题自愈 —— 只会制造一串没有信息量的重启日志。
func Run(ctx context.Context, cfg Config, deps Deps) (Summary, error) {
	if deps.Check == nil || deps.Rebuild == nil {
		return Summary{}, errors.New("keepalive: Check 与 Rebuild 必须提供")
	}
	logf := deps.Log
	if logf == nil {
		logf = nopLogger
	}
	sleep := deps.Sleep
	if sleep == nil {
		sleep = SleepContext
	}
	lock := deps.Lock
	if lock == nil {
		lock = func() (func(), bool) { return nil, true }
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	var sum Summary
	// 收尾事件用 defer 发，这样"跑到上限"和"被信号取消"两条路径都会留下汇总，
	// journal 里永远能看到这一次巡检到底做了多少事。
	defer func() { logf(EvStopped, sum) }()

	for round := 1; ; round++ {
		if ctx.Err() != nil {
			return sum, nil
		}
		sum.Rounds++
		logf(EvRoundStart, round)

		release, ok := lock()
		if !ok {
			sum.Skipped++
			sum.Last = OutcomeSkipped
			sum.Ran = true
			logf(EvLocked, round)
		} else {
			out := runRound(ctx, deps, logf)
			sum.Last = out
			sum.Ran = true
			switch out {
			case OutcomeHealthy:
				sum.Healthy++
			case OutcomeRebuilt:
				sum.Rebuilt++
			case OutcomeRebuildFailed:
				sum.Failed++
			}
			if release != nil {
				release()
			}
		}

		if cfg.Once || (cfg.MaxRounds > 0 && round >= cfg.MaxRounds) {
			break
		}
		if !sleep(ctx, interval) {
			return sum, nil
		}
	}
	// 走到这里说明循环是被 Once / MaxRounds 结束的（见上面的 break）。
	return sum, nil
}

// runRound 跑一轮：体检 → 必要时重建。返回本轮结论。
func runRound(ctx context.Context, deps Deps, logf Logger) Outcome {
	detail, err := deps.Check(ctx)
	if err == nil {
		// ★ 这里**什么都不做**就是全部意义所在。
		logf(EvHealthy, detail)
		return OutcomeHealthy
	}
	logf(EvUnhealthy, err)

	logf(EvRebuilding)
	rdetail, rerr := deps.Rebuild(ctx)
	if rerr != nil {
		logf(EvRebuildFailed, rerr)
		return OutcomeRebuildFailed
	}
	logf(EvRebuilt, rdetail)
	return OutcomeRebuilt
}

// SleepContext 等待 d，或在 ctx 结束时提前返回（返回 false）。
func SleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
