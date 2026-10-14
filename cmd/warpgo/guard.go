package main

import (
	"fmt"
	"time"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/rollback"
)

// cmdRollbackGuard 是那个"保险丝"进程。
//
// 它由 up 用 setsid 派生，**生命周期与调用方无关** —— 这一点是它的全部价值所在：
// 当全局模式把网络弄断时，发起变更的那个 SSH 会话多半已经收不到任何东西了，
// 命令的输出没人看得到、甚至整个会话都没了。而它作为独立进程仍然在数秒后把网络恢复。
//
// 它是内部命令，不进 help 列表。
func (a *app) cmdRollbackGuard(args []string) int {
	var (
		timeoutStr string
		snapshot   string
	)
	opts := newOptionSet().Str("--timeout", &timeoutStr).Str("--snapshot", &snapshot)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	timeout := rollback.DefaultTimeout
	if timeoutStr != "" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return a.fail(fmt.Errorf("--timeout 不是合法时长: %w", err))
		}
		timeout = d
	}
	if snapshot == "" {
		snapshot = rollback.SnapshotPath()
	}

	if _, err := rollback.Guard(timeout, snapshot, a.guardLog); err != nil {
		a.log.Errorf("%v", err)
		return exitError
	}
	return exitOK
}

// guardLog 把回滚事件翻译成当前语言的文案。
//
// 库层只发事件、不拼文案（见 rollback.Event 的说明），翻译集中在这里，
// 于是回滚逻辑本身与文案无关，单独测它也不依赖 msg。
func (a *app) guardLog(ev rollback.Event, args ...any) {
	switch ev {
	case rollback.EvArmed:
		a.log.Infof("%s", msg.Tf(msg.KeyGuardArmed, args...))
	case rollback.EvConfirmed:
		a.log.Infof("%s", msg.T(msg.KeyGuardConfirmed))
	case rollback.EvTimeout:
		a.log.Warnf("%s", msg.Tf(msg.KeyGuardTimeout, args...))
	case rollback.EvRollbackRules:
		a.log.Warnf("%s", msg.T(msg.KeyGuardRBRules))
	case rollback.EvRollbackTable:
		a.log.Warnf("%s", msg.Tf(msg.KeyGuardRBTable, args...))
	case rollback.EvRollbackLink:
		a.log.Warnf("%s", msg.Tf(msg.KeyGuardRBLink, args...))
	case rollback.EvRollbackDone:
		a.log.Warnf("%s", msg.T(msg.KeyGuardRBDone))
	case rollback.EvNote:
		a.log.Debugf("%s", msg.Tf(msg.KeyGuardNote, args...))
	default:
		a.log.Debugf("rollback event %d: %v", int(ev), args)
	}
}

// cmdConfirm 告诉保险丝"新配置可用，别回滚"。
//
// 正常路径下 up 会自己确认（自检通过后），这个命令是留给两种情形的：
//   - 用户用了 --no-confirm，想自己判断后手动确认；
//   - 用户想给自己留一个"跑完再看一眼"的窗口。
func (a *app) cmdConfirm(args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(a.stderr, "%s\n", msg.Tf(msg.KeyBadOpt, args[0]))
		return exitError
	}

	// 没有待确认的变更时说清楚，免得用户以为"确认生效了"。
	if _, err := rollback.Load(rollback.SnapshotPath()); err != nil {
		a.log.Warnf("%s", msg.T(msg.KeyConfirmNone))
		return exitOK
	}

	if err := rollback.Confirm(); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.T(msg.KeyConfirmOK))
	return exitOK
}
