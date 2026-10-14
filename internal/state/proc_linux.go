//go:build linux

package state

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// osProc 是 Linux 上的生产实现（目标平台就是 Debian）。
type osProc struct{}

// Alive 用 signal 0 探测进程是否存在。
// EPERM 表示进程存在但不属于我们 —— 在需要接管的场景下也算「活着」。
func (osProc) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Verify 判断 /proc/<pid>/cmdline 的 argv[0] 是否指向 warpgo。
//
// 只看 argv[0] 而不是整条命令行：这样 `vim /etc/warpgo.toml` 这类「命令行里恰好
// 带 warpgo 字样」的无辜进程不会被误判，而任何路径下的 warpgo 实例都能认出来
// （包括被重命名或装在别处的构建）。
func (osProc) Verify(pid int) bool {
	if pid <= 0 {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	argv0, _, _ := strings.Cut(string(raw), "\x00")
	if argv0 == "" {
		return false
	}
	base := argv0
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.Contains(base, "warpgo")
}

// Kill 发送 SIGKILL。进程已不存在（ESRCH）视为成功。
func (osProc) Kill(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
