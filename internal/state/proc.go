package state

import "time"

// procOps 抽象「查看 / 确认 / 终止进程」三个动作。
//
// 之所以抽成接口：锁的接管逻辑是**安全敏感**代码（会 kill 进程），必须能用假实现
// 把各种分支密集覆盖，而不是靠真机上碰运气。生产实现见 proc_linux.go / proc_windows.go。
type procOps interface {
	// Alive 报告进程是否存活。
	Alive(pid int) bool
	// Verify 报告能否确认该进程就是 warpgo 的实例。接管时用它防止 PID 复用误杀。
	Verify(pid int) bool
	// Kill 终止进程（已确认身份后才会被调用）。
	Kill(pid int) error
}

// FormatAge 把时长整理成适合放进日志的样子（负数按 0 处理）。
func FormatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}
