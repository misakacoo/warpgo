// Command warpgo 是 Debian 上的 Cloudflare WARP 管理工具。
//
// 这是 fscarmen/warp 的 Go 重写版，刻意只支持 Debian、不含 Docker、不适配其他平台。
// 设计文档见 docs/DESIGN.md。
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/logx"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
)

// 退出码，与设计文档 §9 一致。
const (
	exitOK           = 0
	exitError        = 1
	exitUnhealthy    = 2
	exitNeedReserved = 3
	exitLockBusy     = 4
	exitNotDebian    = 5
)

type app struct {
	log    *logx.Logger
	stdout *os.File
	stderr *os.File
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	a := &app{
		log:    logx.Default(),
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	rest := a.parseGlobal(args)

	if len(rest) == 0 {
		a.printUsage()
		return exitOK
	}

	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "help", "-h", "--help":
		a.printUsage()
		return exitOK
	case "version", "-V", "--version":
		return a.cmdVersion(cmdArgs)
	case "register":
		return a.cmdRegister(cmdArgs)
	case "account":
		return a.cmdAccount(cmdArgs)
	case "profile":
		return a.cmdProfile(cmdArgs)
	case "up":
		return a.cmdUp(cmdArgs)
	case "on":
		// 菜单 `o` 的"开"：按上次的运行态恢复（与 up 的差别只在默认值来源）。
		return a.cmdOn(cmdArgs)
	case "off":
		// 菜单 `o` 的"关"：临时关闭，保留运行态与保活单元，随时可 on 回来。
		return a.cmdOff(cmdArgs)
	case "down":
		return a.cmdDown(cmdArgs)
	case "status":
		return a.cmdStatus(cmdArgs)
	case "rotate":
		return a.cmdRotate(cmdArgs)
	case "switch":
		return a.cmdSwitch(cmdArgs)
	case "priority":
		return a.cmdPriority(cmdArgs)
	case "proxy":
		return a.cmdProxy(cmdArgs)
	case "client":
		return a.cmdClient(cmdArgs)
	case "menu":
		return a.cmdMenu(cmdArgs)
	case "watch":
		return a.cmdWatch(cmdArgs)
	case "install":
		return a.cmdInstall(cmdArgs)
	case "uninstall":
		return a.cmdUninstall(cmdArgs)
	case "confirm":
		return a.cmdConfirm(cmdArgs)
	case "rollback-guard":
		// 内部命令：由 up 派生，不进 help 列表。
		return a.cmdRollbackGuard(cmdArgs)
	case "userspace-daemon":
		// 内部命令：由 up / watch 派生，不进 help 列表。见 cmdUserspaceDaemon。
		return a.cmdUserspaceDaemon(cmdArgs)
	case "hook":
		// 隐藏子命令：由 warp-svc 的 ExecStartPost / ExecStop 调用（菜单 z / x），
		// 不进 help 列表 —— 它不该由用户日常手动执行。
		return a.cmdHook(cmdArgs)
	case "dev":
		return a.cmdDev(cmdArgs)
	default:
		// 以 `-` 开头的是选项而不是子命令。分开报错是有实际用处的：
		// 用户（或上一版 systemd 单元）传 `--lang en` 时，
		// 说"未知选项"比说"未知子命令"准确得多。
		if strings.HasPrefix(cmd, "-") {
			fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, cmd))
		} else {
			fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyUnknownCm, cmd))
		}
		a.printUsage()
		return exitError
	}
}

// parseGlobal 处理全局选项；被它吃掉的选项不会出现在子命令参数里。
//
// 这里曾经还要解析 `--lang` / `-L` 并调用 setLang。语言机制已移除
// （只保留简体中文），于是只剩两个日志级别开关 —— 相应地，函数也不再
// 需要返回值里的"错误码"，因为不存在会失败的选项了。
func (a *app) parseGlobal(args []string) []string {
	var rest []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "--verbose", "-v":
			a.log.SetLevel(logx.LevelDebug)
		case "--quiet", "-q":
			a.log.SetLevel(logx.LevelWarn)
		default:
			rest = append(rest, arg)
		}
	}
	return rest
}

func (a *app) printUsage() {
	fmt.Fprintln(a.stdout, msg.T(msg.KeyUsage))
}

// cmdVersion 已移到 version.go —— 它现在不只是一行信息，还要能查新版本
// （`--check`）与自我更新（`--self-update`），单独一个文件更好读。

func (a *app) cmdDev(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyNeedArg, "dev"))
		return exitError
	}
	switch args[0] {
	case "lock":
		return a.cmdDevLock(args[1:])
	case "platform":
		return a.cmdDevPlatform(args[1:])
	default:
		fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyUnknownCm, "dev "+args[0]))
		return exitError
	}
}

func (a *app) cmdDevPlatform(args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, args[0]))
		return exitError
	}
	if v, ok := system.DebianVersion(); ok {
		fmt.Fprintf(a.stdout, "%s\n", msg.Tf(msg.KeyPlatformDebian, v))
		return exitOK
	}
	fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyPlatformNotDebia, runtime.GOOS))
	return exitNotDebian
}

// cmdDevLock 演练单实例锁，让「占用 / 心跳 / 接管」三种行为可以在真机上一键验证。
func (a *app) cmdDevLock(args []string) int {
	path := state.DefaultLockPath
	stale := state.DefaultStaleAfter
	var hold time.Duration

	sc := &argScanner{args: args}
	for !sc.done() {
		arg := sc.cur()
		switch {
		case arg == "--path":
			v, ok := sc.next()
			if !ok {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyNeedArg, arg))
				return exitError
			}
			path = v
		case strings.HasPrefix(arg, "--path="):
			path = strings.TrimPrefix(arg, "--path=")
		case arg == "--stale":
			v, ok := sc.next()
			if !ok {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyNeedArg, arg))
				return exitError
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, arg+" "+v))
				return exitError
			}
			stale = d
		case strings.HasPrefix(arg, "--stale="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--stale="))
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, arg))
				return exitError
			}
			stale = d
		case arg == "--hold":
			v, ok := sc.next()
			if !ok {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyNeedArg, arg))
				return exitError
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, arg+" "+v))
				return exitError
			}
			hold = d
		case strings.HasPrefix(arg, "--hold="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--hold="))
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, arg))
				return exitError
			}
			hold = d
		default:
			fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, arg))
			return exitError
		}
		sc.i++
	}

	l, st, err := state.Acquire(path, stale)
	if err != nil {
		a.log.Errorf("%v", err)
		return exitError
	}
	if st == state.StatusBusy {
		// 关键：Busy **不是**成功。必须明确告诉调用方本次什么都没做，
		// 否则会重演上游「静默 return 0 让人误读成已完成」的坑。
		fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyLockBusy, holderPID(path)))
		return exitLockBusy
	}

	a.log.Infof("%s", msg.Tf(msg.KeyLockAcquired, path))
	a.reportTakeover(l)

	defer func() {
		if rerr := l.Release(); rerr != nil {
			a.log.Warnf("release: %v", rerr)
			return
		}
		a.log.Infof("%s", msg.Tf(msg.KeyLockReleased, path))
	}()

	if hold > 0 {
		deadline := time.Now().Add(hold)
		for time.Now().Before(deadline) {
			time.Sleep(time.Second)
			if terr := l.Touch(); terr != nil {
				a.log.Errorf("touch: %v", terr)
				return exitError
			}
			a.log.Debugf("%s", msg.Tf(msg.KeyLockTouch, os.Getpid()))
		}
	}
	return exitOK
}

func (a *app) reportTakeover(l *state.Lock) {
	switch {
	case l.TookOverFrom() > 0 && l.TookOverLegacy():
		a.log.Warnf("%s", msg.Tf(msg.KeyLockTakeoverLegacy, l.TookOverFrom()))
	case l.TookOverFrom() > 0:
		a.log.Warnf("%s", msg.Tf(msg.KeyLockTakeoverStale,
			l.TookOverFrom(), state.FormatAge(l.TookOverAge())))
	}
	if l.Unverified() {
		a.log.Warnf("%s", msg.Tf(msg.KeyLockTakeoverUnverif, l.TookOverFrom()))
	}
	switch {
	case l.KillErr() != nil:
		a.log.Warnf("%s", msg.Tf(msg.KeyLockKillFailed, l.TookOverFrom(), l.KillErr()))
	case l.KilledStaleHolder():
		a.log.Infof("%s", msg.Tf(msg.KeyLockKilled, l.TookOverFrom()))
	}
	if l.LeftoverPID() > 0 {
		a.log.Warnf("%s", msg.Tf(msg.KeyLockLeftover, l.LeftoverPID()))
	}
}

// holderPID 只读地取出当前锁的持有者 PID（仅用于提示文案）。
func holderPID(path string) int {
	info, found, err := state.Inspect(path)
	if err != nil || !found {
		return 0
	}
	return info.PID
}

// argScanner 是极简的按位取参工具，避免为每个子命令手写下标运算。
type argScanner struct {
	args []string
	i    int
}

func (s *argScanner) done() bool  { return s.i >= len(s.args) }
func (s *argScanner) cur() string { return s.args[s.i] }

func (s *argScanner) next() (string, bool) {
	if s.i+1 >= len(s.args) {
		return "", false
	}
	s.i++
	return s.args[s.i], true
}
