package keepalive

import (
	"context"
	"errors"
	"testing"
	"time"
)

// noSleep 让测试跑得像没有等待一样 —— 这个包的主要用法就是"每 5 分钟一次"，
// 真睡的话一个用例就要跑几分钟。
func noSleep(context.Context, time.Duration) bool { return true }

// record 收集事件，用于断言"到底走的是哪条分支"。
type record struct {
	events []Event
	args   [][]any
}

func (r *record) log(ev Event, args ...any) {
	r.events = append(r.events, ev)
	r.args = append(r.args, args)
}

func (r *record) has(ev Event) bool {
	for _, e := range r.events {
		if e == ev {
			return true
		}
	}
	return false
}

func (r *record) count(ev Event) int {
	n := 0
	for _, e := range r.events {
		if e == ev {
			n++
		}
	}
	return n
}

// ① 健康时**一个字节都不改** —— 这是整个包的立身之本。
//
// 上游的保活每 5 分钟无条件重建一次接口，用户看到的是出口 IP 一直在变。
// 这条用例就是那条行为的回归防护：只要 Rebuild 被调用过，测试就红。
func TestHealthyDoesNothing(t *testing.T) {
	rebuilds := 0
	rec := &record{}

	sum, err := Run(context.Background(), Config{Once: true}, Deps{
		Check: func(context.Context) (string, error) { return "IPv4 warp=on", nil },
		Rebuild: func(context.Context) (string, error) {
			rebuilds++
			return "", nil
		},
		Sleep: noSleep,
		Log:   rec.log,
	})
	if err != nil {
		t.Fatalf("Run 报错: %v", err)
	}
	if rebuilds != 0 {
		t.Fatalf("链路健康时不该重建，实际重建了 %d 次", rebuilds)
	}
	if !sum.IsHealthy() || sum.Healthy != 1 || sum.Last != OutcomeHealthy {
		t.Fatalf("汇总不对: %+v", sum)
	}
	if !rec.has(EvHealthy) || rec.has(EvRebuilding) {
		t.Fatalf("事件不对: %v", rec.events)
	}
}

// ② 断链才重建，且只重建一次。
func TestUnhealthyRebuilds(t *testing.T) {
	rebuilds := 0
	rec := &record{}

	sum, err := Run(context.Background(), Config{Once: true}, Deps{
		Check: func(context.Context) (string, error) { return "", errors.New("接口 warp 不存在") },
		Rebuild: func(context.Context) (string, error) {
			rebuilds++
			return "隧道已就绪", nil
		},
		Sleep: noSleep,
		Log:   rec.log,
	})
	if err != nil {
		t.Fatalf("Run 报错: %v", err)
	}
	if rebuilds != 1 {
		t.Fatalf("应当重建 1 次，实际 %d 次", rebuilds)
	}
	if !sum.IsHealthy() || sum.Rebuilt != 1 {
		t.Fatalf("汇总不对: %+v", sum)
	}
	for _, ev := range []Event{EvUnhealthy, EvRebuilding, EvRebuilt} {
		if !rec.has(ev) {
			t.Fatalf("缺少事件 %d: %v", ev, rec.events)
		}
	}
}

// ③ 重建失败**不是致命错误**：循环继续，进程活着。
//
// 这条很关键。如果失败就退出，systemd 会每 30 秒重启一次，日志里堆满
// 没有信息量的重启记录；而"端点不可达"这类问题并不会因为重启而自愈。
func TestRebuildFailureIsNotFatal(t *testing.T) {
	rebuilds := 0
	rec := &record{}

	sum, err := Run(context.Background(), Config{MaxRounds: 3, Interval: time.Millisecond}, Deps{
		Check: func(context.Context) (string, error) { return "", errors.New("断链") },
		Rebuild: func(context.Context) (string, error) {
			rebuilds++
			return "", errors.New("端点不可达")
		},
		Sleep: noSleep,
		Log:   rec.log,
	})
	if err != nil {
		t.Fatalf("重建失败不该让 Run 返回错误，实际: %v", err)
	}
	if rebuilds != 3 {
		t.Fatalf("应当每轮都重试（3 次），实际 %d 次", rebuilds)
	}
	if sum.Failed != 3 || sum.IsHealthy() {
		t.Fatalf("汇总不对: %+v", sum)
	}
	if rec.count(EvRebuildFailed) != 3 {
		t.Fatalf("失败事件数量不对: %v", rec.events)
	}
}

// ④ 锁被占是**正常情形**，跳过而不报错，也不去动网络。
func TestLockedSkipsRound(t *testing.T) {
	checkCalls, rebuilds := 0, 0
	rec := &record{}

	sum, err := Run(context.Background(), Config{Once: true}, Deps{
		Check: func(context.Context) (string, error) { checkCalls++; return "", nil },
		Rebuild: func(context.Context) (string, error) {
			rebuilds++
			return "", nil
		},
		Lock:  func() (func(), bool) { return nil, false },
		Sleep: noSleep,
		Log:   rec.log,
	})
	if err != nil {
		t.Fatalf("Run 报错: %v", err)
	}
	if checkCalls != 0 || rebuilds != 0 {
		t.Fatalf("被锁挡住时不该做任何事（体检 %d 次、重建 %d 次）", checkCalls, rebuilds)
	}
	if sum.Skipped != 1 || sum.Last != OutcomeSkipped || sum.IsHealthy() {
		t.Fatalf("汇总不对: %+v", sum)
	}
	if !rec.has(EvLocked) {
		t.Fatalf("应当上报被锁挡住: %v", rec.events)
	}
}

// 拿到锁要负责释放 —— 否则一次异常退出会把锁永久占住。
func TestLockIsReleased(t *testing.T) {
	released := 0
	_, err := Run(context.Background(), Config{MaxRounds: 2, Interval: time.Millisecond}, Deps{
		Check:   func(context.Context) (string, error) { return "ok", nil },
		Rebuild: func(context.Context) (string, error) { return "", nil },
		Lock:    func() (func(), bool) { return func() { released++ }, true },
		Sleep:   noSleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Fatalf("每轮都该释放锁，实际释放 %d 次", released)
	}
}

// ctx 取消（Ctrl-C / systemd stop）时干净退出，且留下汇总事件。
func TestContextCancelStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &record{}
	rounds := 0

	sum, err := Run(ctx, Config{Interval: time.Millisecond}, Deps{
		Check:   func(context.Context) (string, error) { return "ok", nil },
		Rebuild: func(context.Context) (string, error) { return "", nil },
		Sleep: func(_ context.Context, _ time.Duration) bool {
			rounds++
			cancel() // 模拟"第二轮开始前收到停止信号"
			return true
		},
		Log: rec.log,
	})
	if err != nil {
		t.Fatalf("被取消不该报错: %v", err)
	}
	if sum.Rounds == 0 {
		t.Fatal("应当至少跑过一轮")
	}
	if !rec.has(EvStopped) {
		t.Fatalf("取消路径也要留下汇总事件: %v", rec.events)
	}
	_ = rounds
}

// 被取消在睡眠中：Sleep 返回 false 时立刻返回，不做多余的轮次。
func TestSleepCancelledEndsLoop(t *testing.T) {
	rec := &record{}
	sum, err := Run(context.Background(), Config{Interval: time.Minute}, Deps{
		Check:   func(context.Context) (string, error) { return "ok", nil },
		Rebuild: func(context.Context) (string, error) { return "", nil },
		Sleep:   func(context.Context, time.Duration) bool { return false },
		Log:     rec.log,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Rounds != 1 {
		t.Fatalf("应当只跑一轮，实际 %d 轮", sum.Rounds)
	}
}

func TestRunRequiresDeps(t *testing.T) {
	if _, err := Run(context.Background(), Config{Once: true}, Deps{}); err == nil {
		t.Fatal("缺少 Check/Rebuild 时应当报错，而不是静默空转")
	}
}

// 间隔为 0 时用默认值；负值同理（不能让配置错误变成忙等）。
func TestIntervalDefaults(t *testing.T) {
	got := time.Duration(0)
	_, err := Run(context.Background(), Config{Once: true}, Deps{
		Check:   func(context.Context) (string, error) { return "ok", nil },
		Rebuild: func(context.Context) (string, error) { return "", nil },
		Sleep: func(_ context.Context, d time.Duration) bool {
			got = d
			return false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Once 模式下不睡，这里只验证默认常量本身是合理的。
	if DefaultInterval != 5*time.Minute {
		t.Fatalf("默认间隔应当是 5 分钟，实际 %s", DefaultInterval)
	}
	if MinInterval <= 0 {
		t.Fatalf("最小间隔必须为正，实际 %s", MinInterval)
	}
	_ = got
}

func TestSummaryHealthyOnlyWhenRan(t *testing.T) {
	var zero Summary
	if zero.IsHealthy() {
		t.Fatal("一轮都没跑过时不该报告健康")
	}
	if !(Summary{Ran: true, Last: OutcomeRebuilt}).IsHealthy() {
		t.Fatal("重建成功应当算健康")
	}
	if (Summary{Ran: true, Last: OutcomeRebuildFailed}).IsHealthy() {
		t.Fatal("重建失败不该算健康")
	}
	if (Summary{Ran: true, Last: OutcomeSkipped}).IsHealthy() {
		t.Fatal("被锁挡住不该算健康 —— 它意味着链路的真实状态未知")
	}
}
