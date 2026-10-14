// Package state 负责 warpgo 的持久状态与进程间互斥。
//
// 本文件是**单实例锁**。它看起来只是个小文件，但它承载了三条真机踩出来的教训
// （源自 fscarmen/warp shell 版 v3.2.14 的排障记录），三条都必须成立：
//
//  1. **拿不到锁时不能静默成功。** 上游的 net() 发现锁被别人拿着时直接 `return 0`，
//     调用方以为「已经完成」，实际什么都没做。实测因此得出过两次错误结论
//     （把「被锁挡住」误读成「体检通过所以没重建」）。这里用 Status 明确告诉调用方。
//
//  2. **持锁者必须能被判定失控。** 锁文件里除了 PID 还记一个心跳时间戳，持锁者每轮
//     重试都刷新它。只有心跳停跳超过 StaleAfter 才视为失控 —— 比纯墙钟超时可靠：
//     一次正常的重建循环可以持续十几分钟，但它一直在刷心跳。
//
//  3. **失控的锁必须能被接管。** 否则一个卡住的实例会让保活永远修不回来，
//     直接违反「断联必须能自愈」。接管时会尽量终止旧持锁者，但**必须先确认它是
//     warpgo 进程**（看 /proc/<pid>/cmdline），防止 PID 复用误杀无辜进程；
//     确认不了就只接管锁文件、不杀进程。
//
// 另外兼容旧格式锁（只有 PID、没有心跳）：一律按失效处理，否则升级后会被老锁卡死。
package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 默认值。运行时由调用方传入，便于测试与自定义。
const (
	DefaultLockPath   = "/run/warpgo/net.lock"
	DefaultStaleAfter = 180 * time.Second
)

// Status 描述一次 Acquire 的结果。
type Status int

const (
	// StatusAcquired 拿到了锁（可能是新建，也可能是接管了失控的锁）。
	StatusAcquired Status = iota
	// StatusBusy 别人正在正常干活，本次应当直接跳过（**不是**成功）。
	StatusBusy
)

func (s Status) String() string {
	switch s {
	case StatusAcquired:
		return "acquired"
	case StatusBusy:
		return "busy"
	default:
		return "unknown"
	}
}

// Lock 一个已获取的锁。
type Lock struct {
	path           string
	staleAfter     time.Duration
	pid            int
	ops            procOps
	tookOverFrom   int           // 被接管的旧持锁者 PID（它还活着但心跳停跳）
	tookOverAge    time.Duration // 接管时旧心跳已经停了多久（用于日志，避免打出误导性数字）
	leftoverPID    int           // 文件里的旧 PID 但进程已死（崩溃残留）
	tookOverLegacy bool          // 被接管的锁是旧格式（无心跳字段）
	unverified     bool          // 旧持锁者活着但无法确认身份，因此没有杀它
	killedOK       bool          // 确认身份后确实终止成功
	killErr        error
	released       bool
}

// Acquire 尝试获取 path 处的锁。
//
// 返回 StatusAcquired 时 l 非 nil，调用方**必须**在结束时调用 l.Release()；
// 返回 StatusBusy 时 l 为 nil，调用方应当把它当成「本次未执行」而不是「成功」。
func Acquire(path string, staleAfter time.Duration) (*Lock, Status, error) {
	return acquireWith(path, staleAfter, osProc{}, os.Getpid())
}

func acquireWith(path string, staleAfter time.Duration, ops procOps, self int) (*Lock, Status, error) {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}

	raw, err := os.ReadFile(path)
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, StatusBusy, fmt.Errorf("state: read lock %s: %w", path, err)
	case err == nil:
		pid, ts, legacy := parseLock(raw)
		// 自己持有（重入调用）或文件里没有有效 PID：直接覆盖写。
		if pid > 0 && pid != self {
			if ops.Alive(pid) {
				// 心跳新鲜 → 别人正在正常干活。返回 Busy 而不是「成功」，
				// 否则调用方会把「什么都没做」当成「已完成」（上游踩过的坑）。
				if !legacy && ts > 0 && time.Since(time.Unix(ts, 0)) < staleAfter {
					return nil, StatusBusy, nil
				}
				l := &Lock{
					path:           path,
					staleAfter:     staleAfter,
					pid:            self,
					ops:            ops,
					tookOverFrom:   pid,
					tookOverLegacy: legacy,
				}
				if ts > 0 {
					l.tookOverAge = time.Since(time.Unix(ts, 0))
				}
				if ops.Verify(pid) {
					l.killErr = ops.Kill(pid)
					l.killedOK = l.killErr == nil
				} else {
					// 无法确认身份就不要误杀（防 PID 复用），只接管锁文件
					l.unverified = true
				}
				if werr := l.write(); werr != nil {
					return nil, StatusBusy, werr
				}
				return l, StatusAcquired, nil
			}
			// 持锁者已经死了：锁文件是崩溃残留，直接接管并记录，便于排查
			l := &Lock{path: path, staleAfter: staleAfter, pid: self, ops: ops, leftoverPID: pid}
			if werr := l.write(); werr != nil {
				return nil, StatusBusy, werr
			}
			return l, StatusAcquired, nil
		}
	}

	l := &Lock{path: path, staleAfter: staleAfter, pid: self, ops: ops}
	if err := l.write(); err != nil {
		return nil, StatusBusy, err
	}
	return l, StatusAcquired, nil
}

// Touch 刷新心跳。长时间的重建循环里必须每轮调用一次，否则会被判成失控。
func (l *Lock) Touch() error {
	if l.released {
		return errors.New("state: lock already released")
	}
	return l.write()
}

// Release 释放锁。只有当文件里记的仍是我们自己时才删除 ——
// 若已被别人接管，绝不能删掉后来者的锁。
func (l *Lock) Release() error {
	if l.released {
		return nil
	}
	l.released = true

	raw, err := os.ReadFile(l.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if pid, _, _ := parseLock(raw); pid != l.pid {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// TookOverFrom 返回被接管的旧持锁者 PID；0 表示本次既没接管也没覆盖残留。
func (l *Lock) TookOverFrom() int { return l.tookOverFrom }

// TookOverAge 返回接管时旧心跳已经停了多久（旧格式或无心跳时为 0）。
func (l *Lock) TookOverAge() time.Duration { return l.tookOverAge }

// LeftoverPID 返回锁文件里那个已经死掉的旧 PID（崩溃残留）；0 表示没有。
func (l *Lock) LeftoverPID() int { return l.leftoverPID }

// TookOverLegacy 报告被接管的锁是否为旧格式（无心跳字段）。
func (l *Lock) TookOverLegacy() bool { return l.tookOverLegacy }

// Unverified 报告接管时是否无法确认旧持锁者身份（因此没有杀它）。
func (l *Lock) Unverified() bool { return l.unverified }

// KilledStaleHolder 报告是否确实终止了旧持锁者。
func (l *Lock) KilledStaleHolder() bool { return l.killedOK }

// KillErr 返回终止旧持锁者时的错误（nil 表示成功或未尝试）。
func (l *Lock) KillErr() error { return l.killErr }

// Path 返回锁文件路径。
func (l *Lock) Path() string { return l.path }

// PID 返回本次持锁的进程号。
func (l *Lock) PID() int { return l.pid }

func (l *Lock) write() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("state: create lock dir: %w", err)
	}
	content := fmt.Sprintf("%d %d\n", l.pid, time.Now().Unix())
	if err := os.WriteFile(l.path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("state: write lock %s: %w", l.path, err)
	}
	return nil
}

// parseLock 解析锁内容："<pid> <unix-ts>"；只有 PID 时 legacy=true。
func parseLock(b []byte) (pid int, ts int64, legacy bool) {
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, 0, true
	}
	pid, _ = strconv.Atoi(fields[0])
	if len(fields) < 2 {
		return pid, 0, true
	}
	ts, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return pid, 0, true
	}
	return pid, ts, false
}

// LockInfo 是对锁文件的一次只读观测（status 子命令用）。
type LockInfo struct {
	Path   string
	PID    int
	Since  time.Time
	Legacy bool
	Alive  bool
	Age    time.Duration
}

// Inspect 只读地查看锁状态。found=false 表示当前没有锁。
func Inspect(path string) (LockInfo, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LockInfo{Path: path}, false, nil
		}
		return LockInfo{Path: path}, false, err
	}
	pid, ts, legacy := parseLock(raw)
	info := LockInfo{Path: path, PID: pid, Legacy: legacy}
	if ts > 0 {
		info.Since = time.Unix(ts, 0)
		info.Age = time.Since(info.Since)
	}
	if pid > 0 {
		info.Alive = osProc{}.Alive(pid)
	}
	return info, true, nil
}
