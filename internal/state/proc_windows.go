//go:build windows

package state

import "errors"

// Windows 只是开发机，warpgo 的目标平台是 Debian。
// 这里刻意采取**最保守**的策略：只靠心跳停跳判定接管，绝不杀进程。
//
// 注意 Alive 返回 true 会带来什么：锁文件中记录了 PID 且 PID 号有效时，
// 「心跳新鲜」→ Busy（正确）；「心跳停跳」→ 接管但不杀（安全）。
// 唯一代价是持锁者已死但心跳还没过期（< StaleAfter）时会多等一会儿 —— 可接受。
type osProc struct{}

// Alive 在 Windows 上无法可靠判断，保守地认为存在。
func (osProc) Alive(pid int) bool { return pid > 0 }

// Verify 在 Windows 上一律无法确认身份，因此永远不会触发 Kill。
func (osProc) Verify(int) bool { return false }

// Kill 在 Windows 上不支持。
func (osProc) Kill(int) error {
	return errors.New("state: killing a stale lock holder is not supported on windows")
}
