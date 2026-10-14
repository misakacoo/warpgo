package rollback

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 保险丝"该不该拉闸"的判据。
//
// 这是 M5 的真机验收逼出来的一条：自动换后端之后，一次 up 会先后生出**两个**
// 保险丝（内核一次、用户态一次）。而"解除标记"是一个被消费的文件（谁先看到谁删），
// 两个保险丝共用一个标记时必然有一个会漏看 —— 漏看的那个会在超时后把
// **后来建立的隧道**拆掉，表现成"随机掉线"。
//
// 所以判据不能只靠标记，还要看"我守护的那次变更还在不在"。
func TestStillPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollback.json")

	born := &Snapshot{CreatedAt: time.Now().UTC(), IfName: "warp", Table: 51820}
	if err := born.Save(path); err != nil {
		t.Fatal(err)
	}
	if !stillPending(path, born) {
		t.Error("快照还在、内容一致 → 应当仍然待确认")
	}

	// JSON 往返：guard 是从磁盘把自己那份读出来的，判据不能因为序列化精度而失效。
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !stillPending(path, reloaded) {
		t.Error("经过 JSON 往返之后仍应判为同一次变更")
	}

	// 被新的变更换掉 → 不该回滚（这正是"两个保险丝"的场景）。
	newer := &Snapshot{CreatedAt: time.Now().Add(time.Second).UTC(), IfName: "warp", Table: 51820}
	if err := newer.Save(path); err != nil {
		t.Fatal(err)
	}
	if stillPending(path, born) {
		t.Error("快照已被新的变更换掉 → 不该再回滚")
	}

	// 被 ClearSnapshot 清掉 → 不该回滚（正常确认成功、主动回滚完、down 都属于这种）。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if stillPending(path, born) {
		t.Error("快照已被清除 → 不该再回滚")
	}

	// 拿不到自己的出生信息 → 一律不动手（误拉闸的代价是不对称的）。
	if stillPending(path, nil) {
		t.Error("born 为 nil 时应当判定为「不动手」")
	}
}

// 确认标记必须**带身份**：它只该让"写下它的那一次变更"的保险丝退出。
//
// 这是 M5 真机验收逼出来的第二处，也是第一版的真正病根：
// 曾经是一块不带内容的标记文件，谁先看到谁删。而"开始新的变更前先把它删掉"
// 这一步，会在旧保险丝还没轮到下一次轮询时就把标记抹掉 ——
// 于是旧保险丝永远等不到解除，只能守满自己的超时。
// 一次 up 只会开一个保险丝时，这个窗口小到看不见；
// M5 让一次 up 可能先后开两个（内核一次、用户态一次），它立刻稳定复现。
func TestConfirmFlagIsIdentityBound(t *testing.T) {
	t.Setenv(EnvRunDir, t.TempDir())

	// 没有快照时 Disarm 是空操作（也就没有保险丝需要解除）。
	if err := Disarm(); err != nil {
		t.Fatalf("没有快照时 Disarm 不该报错: %v", err)
	}
	if ConfirmedFor(time.Now().UTC()) {
		t.Error("还没有任何标记时不该报告已确认")
	}

	// 空时间戳 = "说不清是哪一次变更"，必须拒绝：
	// 否则它会变成一块"人人认领"的标记，等于把保险全关了。
	if err := ConfirmFor(time.Time{}); err == nil {
		t.Error("空时间戳应当被拒绝")
	}

	a := time.Now().UTC()
	b := a.Add(time.Second)
	if err := ConfirmFor(a); err != nil {
		t.Fatal(err)
	}
	if !ConfirmedFor(a) {
		t.Error("写下标记的那一次应当认得出自己")
	}
	if ConfirmedFor(b) {
		t.Error("别人的标记绝不能被认领（那会让新保险丝一开始就不设防）")
	}
	if ConfirmedFor(time.Time{}) {
		t.Error("空时间戳不该匹配任何标记")
	}

	// Disarm 写的是"当前快照"的身份 —— 这样上一次变更的保险丝能立刻看到并退出。
	snap := &Snapshot{CreatedAt: b, IfName: "warp", Table: 51820}
	if err := snap.Save(SnapshotPath()); err != nil {
		t.Fatal(err)
	}
	if err := Disarm(); err != nil {
		t.Fatal(err)
	}
	if !ConfirmedFor(b) {
		t.Error("Disarm 应当写下当前快照的 CreatedAt")
	}
	if ConfirmedFor(a) {
		t.Error("旧身份的标记应当已被覆盖")
	}
}

// 快照的 CreatedAt 必须能被逐位还原 —— 它是上面那条判据的唯一依据。
func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollback.json")

	in := &Snapshot{
		CreatedAt:    time.Now().UTC(),
		IfName:       "warp",
		Table:        51820,
		Mode:         "global",
		ManagedPrefs: []int{9000, 9020},
		ResolvSHA256: "deadbeef",
		PID:          1234,
		Reason:       "up --mode global",
	}
	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("CreatedAt 往返后不相等: %v vs %v", out.CreatedAt, in.CreatedAt)
	}
	if out.IfName != in.IfName || out.Table != in.Table || out.Mode != in.Mode || out.PID != in.PID {
		t.Errorf("字段往返不一致: %+v", out)
	}
	if len(out.ManagedPrefs) != 2 || out.ManagedPrefs[0] != 9000 {
		t.Errorf("ManagedPrefs 往返不一致: %v", out.ManagedPrefs)
	}

	// 缺失的快照要能明确区分出来（Guard 靠它判断"没有待确认的变更"）。
	if _, err := Load(filepath.Join(dir, "nope.json")); err == nil {
		t.Error("快照不存在时应当返回错误，而不是空快照")
	}
}
