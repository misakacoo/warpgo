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

	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/logx"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/version"
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

// continueRun 由 parseGlobal 返回，表示「选项解析完毕，继续执行子命令」。
const continueRun = -1

type app struct {
	bundle *i18n.Bundle
	log    *logx.Logger
	stdout *os.File
	stderr *os.File
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	a := &app{
		bundle: i18n.New(i18n.Detect()),
		log:    logx.Default(),
		stdout: os.Stdout,
		stderr: os.Stderr,
	}

	rest, code := a.parseGlobal(args)
	if code != continueRun {
		return code
	}

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
	case "down":
		return a.cmdDown(cmdArgs)
	case "status":
		return a.cmdStatus(cmdArgs)
	case "confirm":
		return a.cmdConfirm(cmdArgs)
	case "rollback-guard":
		// 内部命令：由 up 派生，不进 help 列表。
		return a.cmdRollbackGuard(cmdArgs)
	case "dev":
		return a.cmdDev(cmdArgs)
	default:
		fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyUnknownCm, cmd))
		a.printUsage()
		return exitError
	}
}

// parseGlobal 处理全局选项；被它吃掉的选项不会出现在子命令参数里。
func (a *app) parseGlobal(args []string) ([]string, int) {
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--lang" || arg == "-L":
			if i+1 >= len(args) {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyNeedArg, arg))
				return nil, exitError
			}
			i++
			a.setLang(args[i])
			if a.bundle == nil {
				return nil, exitError
			}
		case strings.HasPrefix(arg, "--lang="):
			a.setLang(strings.TrimPrefix(arg, "--lang="))
			if a.bundle == nil {
				return nil, exitError
			}
		case arg == "--verbose" || arg == "-v":
			a.log.SetLevel(logx.LevelDebug)
		case arg == "--quiet" || arg == "-q":
			a.log.SetLevel(logx.LevelWarn)
		default:
			rest = append(rest, arg)
		}
	}
	return rest, continueRun
}

// setLang 解析并应用语言；失败时打印错误并把 bundle 置 nil 作为信号。
func (a *app) setLang(v string) {
	l, ok := i18n.Parse(v)
	if !ok {
		fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadLang, v))
		a.bundle = nil
		return
	}
	a.bundle = i18n.New(l)
}

func (a *app) printUsage() {
	fmt.Fprintln(a.stdout, a.bundle.T(i18n.KeyUsage))
}

// cmdVersion 打印版本信息。
//
// 注意：`version` 在非 Debian 系统上也返回 0 —— 它是只读的查看命令，
// 在开发机/CI 上应该能用；真正会改系统的命令才做 Debian 强校验。
func (a *app) cmdVersion(args []string) int {
	check := false
	for _, arg := range args {
		switch arg {
		case "--check":
			check = true
		default:
			fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, arg))
			return exitError
		}
	}

	fmt.Fprintf(a.stdout, "%s %s\n", a.bundle.T(i18n.KeyVersion), version.Info())

	if v, ok := system.DebianVersion(); ok {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyPlatformDebian, v))
	} else {
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyPlatformNotDebia, runtime.GOOS))
	}

	if check {
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyNotImplemented, "M9"))
		return exitOK
	}
	return exitOK
}

func (a *app) cmdDev(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyNeedArg, "dev"))
		return exitError
	}
	switch args[0] {
	case "lock":
		return a.cmdDevLock(args[1:])
	case "platform":
		return a.cmdDevPlatform(args[1:])
	default:
		fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyUnknownCm, "dev "+args[0]))
		return exitError
	}
}

func (a *app) cmdDevPlatform(args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, args[0]))
		return exitError
	}
	if v, ok := system.DebianVersion(); ok {
		fmt.Fprintf(a.stdout, "%s\n", a.bundle.Tf(i18n.KeyPlatformDebian, v))
		return exitOK
	}
	fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyPlatformNotDebia, runtime.GOOS))
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
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyNeedArg, arg))
				return exitError
			}
			path = v
		case strings.HasPrefix(arg, "--path="):
			path = strings.TrimPrefix(arg, "--path=")
		case arg == "--stale":
			v, ok := sc.next()
			if !ok {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyNeedArg, arg))
				return exitError
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, arg+" "+v))
				return exitError
			}
			stale = d
		case strings.HasPrefix(arg, "--stale="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--stale="))
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, arg))
				return exitError
			}
			stale = d
		case arg == "--hold":
			v, ok := sc.next()
			if !ok {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyNeedArg, arg))
				return exitError
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, arg+" "+v))
				return exitError
			}
			hold = d
		case strings.HasPrefix(arg, "--hold="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--hold="))
			if err != nil {
				fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, arg))
				return exitError
			}
			hold = d
		default:
			fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyBadOpt, arg))
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
		fmt.Fprintf(a.stderr, "%s\n", a.bundle.Tf(i18n.KeyLockBusy, holderPID(path)))
		return exitLockBusy
	}

	a.log.Infof("%s", a.bundle.Tf(i18n.KeyLockAcquired, path))
	a.reportTakeover(l)

	defer func() {
		if rerr := l.Release(); rerr != nil {
			a.log.Warnf("release: %v", rerr)
			return
		}
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyLockReleased, path))
	}()

	if hold > 0 {
		deadline := time.Now().Add(hold)
		for time.Now().Before(deadline) {
			time.Sleep(time.Second)
			if terr := l.Touch(); terr != nil {
				a.log.Errorf("touch: %v", terr)
				return exitError
			}
			a.log.Debugf("%s", a.bundle.Tf(i18n.KeyLockTouch, os.Getpid()))
		}
	}
	return exitOK
}

func (a *app) reportTakeover(l *state.Lock) {
	switch {
	case l.TookOverFrom() > 0 && l.TookOverLegacy():
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyLockTakeoverLegacy, l.TookOverFrom()))
	case l.TookOverFrom() > 0:
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyLockTakeoverStale,
			l.TookOverFrom(), state.FormatAge(l.TookOverAge())))
	}
	if l.Unverified() {
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyLockTakeoverUnverif, l.TookOverFrom()))
	}
	switch {
	case l.KillErr() != nil:
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyLockKillFailed, l.TookOverFrom(), l.KillErr()))
	case l.KilledStaleHolder():
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyLockKilled, l.TookOverFrom()))
	}
	if l.LeftoverPID() > 0 {
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyLockLeftover, l.LeftoverPID()))
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
