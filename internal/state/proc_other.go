//go:build !linux && !windows

package state

import "errors"

// 其他平台（macOS 等）：warpgo 不支持，这里只保证能编译。
// 策略与 Windows 相同 —— 只靠心跳判定，不杀进程。
type osProc struct{}

// Alive 保守地认为 PID 有效即存在。
func (osProc) Alive(pid int) bool { return pid > 0 }

// Verify 无法确认身份。
func (osProc) Verify(int) bool { return false }

// Kill 不支持。
func (osProc) Kill(int) error {
	return errors.New("state: killing a stale lock holder is not supported on this platform")
}
