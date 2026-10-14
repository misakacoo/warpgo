package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeOps 让锁的接管逻辑（会 kill 进程，属安全敏感代码）能被密集覆盖。
type fakeOps struct {
	alive   map[int]bool
	verify  map[int]bool
	killed  []int
	killErr error
}

func newFakeOps() *fakeOps {
	return &fakeOps{alive: map[int]bool{}, verify: map[int]bool{}}
}

func (f *fakeOps) Alive(pid int) bool  { return f.alive[pid] }
func (f *fakeOps) Verify(pid int) bool { return f.verify[pid] }
func (f *fakeOps) Kill(pid int) error {
	f.killed = append(f.killed, pid)
	return f.killErr
}

func tmpLock(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "net.lock")
}

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写锁文件失败: %v", err)
	}
}

// backdate 把锁文件里的心跳时间戳改成 age 之前（模拟时间流逝）。
func backdate(t *testing.T, path string, pid int, age time.Duration) {
	t.Helper()
	ts := time.Now().Add(-age).Unix()
	writeRaw(t, path, strconv.Itoa(pid)+" "+strconv.FormatInt(ts, 10)+"\n")
}

func mustAcquire(t *testing.T, path string, stale time.Duration, ops procOps, self int) *Lock {
	t.Helper()
	l, st, err := acquireWith(path, stale, ops, self)
	if err != nil {
		t.Fatalf("acquireWith 报错: %v", err)
	}
	if st != StatusAcquired {
		t.Fatalf("期望拿到锁，实际 status=%v", st)
	}
	return l
}

// 1. 空路径：直接建锁，并写入 "pid ts"（带心跳）
func TestAcquireOnFreePath(t *testing.T) {
	p := tmpLock(t)
	l := mustAcquire(t, p, time.Minute, newFakeOps(), 1000)

	if l.TookOverFrom() != 0 || l.LeftoverPID() != 0 {
		t.Fatalf("新建锁不应报告接管/残留：tookOver=%d leftover=%d", l.TookOverFrom(), l.LeftoverPID())
	}
	pid, ts, legacy := parseLock(mustRead(t, p))
	if pid != 1000 || ts == 0 || legacy {
		t.Fatalf("锁内容不对: pid=%d ts=%d legacy=%v", pid, ts, legacy)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("释放后锁文件应被删除，实际 err=%v", err)
	}
}

// 2. 心跳新鲜：必须返回 Busy，且**不能杀任何进程**
func TestBusyWhenHeartbeatFresh(t *testing.T) {
	p := tmpLock(t)
	ops := newFakeOps()
	ops.alive[1000] = true

	l1 := mustAcquire(t, p, time.Minute, ops, 1000)

	l2, st, err := acquireWith(p, time.Minute, ops, 2000)
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if st != StatusBusy || l2 != nil {
		t.Fatalf("期望 Busy 且不返回锁，实际 st=%v l=%v", st, l2)
	}
	if len(ops.killed) != 0 {
		t.Fatalf("心跳新鲜时不该杀进程，实际杀了 %v", ops.killed)
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
}

// 3. 心跳停跳 + 身份确认：接管并终止旧持锁者
func TestTakeoverStaleKillsVerifiedHolder(t *testing.T) {
	p := tmpLock(t)
	backdate(t, p, 4242, 10*time.Minute)
	ops := newFakeOps()
	ops.alive[4242] = true
	ops.verify[4242] = true

	l := mustAcquire(t, p, time.Minute, ops, 1000)

	if l.TookOverFrom() != 4242 {
		t.Fatalf("应报告接管自 4242，实际 %d", l.TookOverFrom())
	}
	if l.TookOverLegacy() || l.Unverified() {
		t.Fatalf("不该标记 legacy/unverified")
	}
	if !l.KilledStaleHolder() || l.KillErr() != nil {
		t.Fatalf("应成功终止旧持锁者，实际 killedOK=%v err=%v", l.KilledStaleHolder(), l.KillErr())
	}
	if len(ops.killed) != 1 || ops.killed[0] != 4242 {
		t.Fatalf("期望恰好杀掉 4242，实际 %v", ops.killed)
	}
	if pid, _, _ := parseLock(mustRead(t, p)); pid != 1000 {
		t.Fatalf("接管后锁文件应是新持有者，实际 pid=%d", pid)
	}
}

// 4. 旧格式锁（只有 PID、没有心跳）必须按失效处理，否则升级后会被老锁卡死
func TestTakeoverLegacyLock(t *testing.T) {
	p := tmpLock(t)
	writeRaw(t, p, "4242\n")
	ops := newFakeOps()
	ops.alive[4242] = true
	ops.verify[4242] = true

	l := mustAcquire(t, p, time.Hour, ops, 1000) // stale 给很大也不该当新鲜

	if !l.TookOverLegacy() {
		t.Fatalf("应报告旧格式锁被接管")
	}
	if l.TookOverFrom() != 4242 || !l.KilledStaleHolder() {
		t.Fatalf("应接管并终止 4242：tookOver=%d killed=%v", l.TookOverFrom(), l.KilledStaleHolder())
	}
}

// 5. 无法确认身份时：接管锁但**不杀进程**（防 PID 复用误杀无辜）
func TestTakeoverWithoutKillingUnverified(t *testing.T) {
	p := tmpLock(t)
	backdate(t, p, 4242, 10*time.Minute)
	ops := newFakeOps()
	ops.alive[4242] = true
	ops.verify[4242] = false

	l := mustAcquire(t, p, time.Minute, ops, 1000)

	if !l.Unverified() {
		t.Fatalf("应标记为未验证")
	}
	if l.KilledStaleHolder() || len(ops.killed) != 0 {
		t.Fatalf("未验证身份时绝不能杀进程，实际 killed=%v", ops.killed)
	}
	if l.TookOverFrom() != 4242 {
		t.Fatalf("仍应记录接管自 4242")
	}
}

// 6. 持锁者已死（崩溃残留）：接管、不杀、但要记下残留 PID 便于排查
func TestTakeoverWhenHolderDead(t *testing.T) {
	p := tmpLock(t)
	backdate(t, p, 4242, 10*time.Minute)
	ops := newFakeOps() // alive 里没有 4242 → 已死

	l := mustAcquire(t, p, time.Minute, ops, 1000)

	if l.TookOverFrom() != 0 {
		t.Fatalf("持锁者已死不算「接管」，tookOver 应为 0，实际 %d", l.TookOverFrom())
	}
	if l.LeftoverPID() != 4242 {
		t.Fatalf("应记录崩溃残留 PID 4242，实际 %d", l.LeftoverPID())
	}
	if len(ops.killed) != 0 {
		t.Fatalf("不该杀进程，实际 %v", ops.killed)
	}
}

// 7. 心跳边界 + Touch 刷新
func TestHeartbeatBoundaryAndTouch(t *testing.T) {
	p := tmpLock(t)
	ops := newFakeOps()
	ops.alive[1000] = true

	l := mustAcquire(t, p, 30*time.Second, ops, 1000)

	// 25 秒前的心跳：还没到 30 秒阈值 → 仍然 Busy
	backdate(t, p, 1000, 25*time.Second)
	if _, st, _ := acquireWith(p, 30*time.Second, ops, 2000); st != StatusBusy {
		t.Fatalf("25s < 30s 阈值，应 Busy，实际 %v", st)
	}

	// Touch 之后应重新变新鲜
	if err := l.Touch(); err != nil {
		t.Fatalf("Touch 失败: %v", err)
	}
	if _, st, _ := acquireWith(p, 30*time.Second, ops, 2000); st != StatusBusy {
		t.Fatalf("Touch 后应仍 Busy，实际 %v", st)
	}

	// 超过阈值 → 应被接管
	backdate(t, p, 1000, 40*time.Second)
	if _, st, _ := acquireWith(p, 30*time.Second, ops, 2000); st != StatusAcquired {
		t.Fatalf("40s > 30s 阈值，应接管，实际 %v", st)
	}
}

// 8. 释放后再 Touch 应报错，而不是静默写回一个假锁
func TestTouchAfterReleaseFails(t *testing.T) {
	p := tmpLock(t)
	l := mustAcquire(t, p, time.Minute, newFakeOps(), 1000)
	if err := l.Release(); err != nil {
		t.Fatalf("释放失败: %v", err)
	}
	if err := l.Touch(); err == nil {
		t.Fatalf("释放后 Touch 应报错")
	}
}

// 9. 锁已被别人接管时，自己 Release **不能**删掉后来者的锁
func TestReleaseDoesNotRemoveOthersLock(t *testing.T) {
	p := tmpLock(t)
	l := mustAcquire(t, p, time.Minute, newFakeOps(), 1000)

	// 模拟被别人接管
	writeRaw(t, p, "2000 "+strconv.FormatInt(time.Now().Unix(), 10)+"\n")

	if err := l.Release(); err != nil {
		t.Fatalf("Release 报错: %v", err)
	}
	pid, _, _ := parseLock(mustRead(t, p))
	if pid != 2000 {
		t.Fatalf("别人的锁被误删/误改：pid=%d", pid)
	}
}

// 10. Release 幂等
func TestReleaseIdempotent(t *testing.T) {
	p := tmpLock(t)
	l := mustAcquire(t, p, time.Minute, newFakeOps(), 1000)
	if err := l.Release(); err != nil {
		t.Fatalf("第一次释放失败: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("第二次释放应无错: %v", err)
	}
}

// 11. parseLock 各种输入
func TestParseLock(t *testing.T) {
	cases := []struct {
		in     string
		pid    int
		ts     int64
		legacy bool
	}{
		{"123 456\n", 123, 456, false},
		{"123", 123, 0, true},
		{"123\n", 123, 0, true},
		{"123 abc", 123, 0, true},
		{"", 0, 0, true},
		{"\n", 0, 0, true},
		{"abc 456", 0, 456, false},
	}
	for _, c := range cases {
		pid, ts, legacy := parseLock([]byte(c.in))
		if pid != c.pid || ts != c.ts || legacy != c.legacy {
			t.Errorf("parseLock(%q) = (%d,%d,%v)，期望 (%d,%d,%v)",
				c.in, pid, ts, legacy, c.pid, c.ts, c.legacy)
		}
	}
}

// 12. Inspect 只读观测
func TestInspect(t *testing.T) {
	p := tmpLock(t)

	if _, found, err := Inspect(p); err != nil || found {
		t.Fatalf("无锁时应 found=false err=nil，实际 found=%v err=%v", found, err)
	}

	l := mustAcquire(t, p, time.Minute, newFakeOps(), os.Getpid())
	info, found, err := Inspect(p)
	if err != nil || !found {
		t.Fatalf("应有锁：found=%v err=%v", found, err)
	}
	if info.PID != os.Getpid() {
		t.Fatalf("PID 应为 %d，实际 %d", os.Getpid(), info.PID)
	}
	if info.Legacy {
		t.Fatalf("不该是旧格式")
	}
	if info.Age < 0 || info.Age > time.Minute {
		t.Fatalf("Age 不合理: %v", info.Age)
	}
	_ = l.Release()

	// 旧格式：Age 为 0、Legacy 为 true（Alive 依平台而定，不断言）
	writeRaw(t, p, "4242\n")
	info2, found2, _ := Inspect(p)
	if !found2 || !info2.Legacy || info2.PID != 4242 {
		t.Fatalf("旧格式识别失败: %+v found=%v", info2, found2)
	}
}

// 13. Kill 失败时要如实记录下来，不能假装接管成功
func TestKillFailureIsRecorded(t *testing.T) {
	p := tmpLock(t)
	backdate(t, p, 4242, 10*time.Minute)
	ops := newFakeOps()
	ops.alive[4242] = true
	ops.verify[4242] = true
	ops.killErr = errors.New("权限不足")

	l := mustAcquire(t, p, time.Minute, ops, 1000)
	if l.KillErr() == nil {
		t.Fatalf("应记录 kill 失败")
	}
	if l.KilledStaleHolder() {
		t.Fatalf("kill 失败时不该标记成功")
	}
	// 但仍然要拿到锁（自愈不能被「杀不掉旧进程」挡住）
	if pid, _, _ := parseLock(mustRead(t, p)); pid != 1000 {
		t.Fatalf("仍应持有锁，实际 pid=%d", pid)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读锁文件失败: %v", err)
	}
	return b
}
