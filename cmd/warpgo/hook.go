package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/misakacoo/warpgo/internal/cfclient"
	"github.com/misakacoo/warpgo/internal/msg"
)

// hookWaitDefault 是 `hook add` 等接口出现的默认上限。
//
// 这个命令最常被 warp-svc 的 `ExecStartPost` 调用，而那一刻接口通常还没建出来
// （它要等客户端连上才出现）—— 所以"先等一会儿"是**正常路径**，不是异常处理。
// 30 秒与上游 wait_for 的上限一致。
const hookWaitDefault = 30 * time.Second

// cmdHook 实现菜单 `z` / `x`：给官方客户端的 warp-svc 加减策略规则。
//
// 它们是**隐藏子命令**（不进 help 列表、不由用户日常调用），由
// `warpgo client install` 写进 warp-svc 的 drop-in 里：
//
//	ExecStartPost=warpgo hook add --wait 30s
//	ExecStop=warpgo hook del
//
// 两个都必须**永远以 0 退出**：ExecStartPost / ExecStop 非零退出会让 systemd
// 把整个单元标成 failed，之后 restart 的语义会变得难以推理 —— 这个坑在 M4 记过。
// 所以这里的错误一律降级成警告。
func (a *app) cmdHook(args []string) int {
	if len(args) == 0 {
		a.log.Errorf("%s", msg.T(msg.KeyHookUsage))
		return exitError
	}
	switch args[0] {
	case "add":
		return a.cmdHookAdd(args[1:])
	case "del":
		return a.cmdHookDel(args[1:])
	default:
		a.log.Errorf("%s", msg.Tf(msg.KeyUnknownCm, "hook "+args[0]))
		a.log.Errorf("%s", msg.T(msg.KeyHookUsage))
		return exitError
	}
}

// cmdHookAdd 加上钩子规则。
func (a *app) cmdHookAdd(args []string) int {
	waitStr := ""
	opts := newOptionSet().Str("--wait", &waitStr)
	if err := opts.parse(args); err != nil {
		// 参数错是"调用方写错了"，但作为钩子仍不能返回非 0 ——
		// 记一条错误、返回 0，让 warp-svc 照常起来。
		a.log.Errorf("%v", err)
		return exitOK
	}
	wait := hookWaitDefault
	if waitStr != "" {
		d, err := time.ParseDuration(waitStr)
		if err != nil {
			a.log.Errorf("%v", err)
			return exitOK
		}
		wait = d
	}

	if !isRoot() {
		a.log.Errorf("%s", msg.Tf(msg.KeyHookNeedRoot, "hook add"))
		return exitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait+30*time.Second)
	defer cancel()

	mgr := cfclient.New()
	res, err := mgr.HookAdd(ctx, wait, func(f string, args ...any) { a.log.Debugf(f, args...) })
	if err != nil {
		if errors.Is(err, cfclient.ErrIfaceAbsent) {
			// 最常见的正常情形：客户端不在 warp 模式下，或者还没连上。
			// 这不是"出错"，只是"没什么可做的"。
			a.log.Warnf("%s", msg.Tf(msg.KeyHookIfaceAbsent, cfclient.ClientIfName))
			return exitOK
		}
		a.log.Warnf("%v", err)
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyHookAdded, res.RulesAdded, res.RoutesAdded))
	return exitOK
}

// cmdHookDel 撤掉钩子规则。
func (a *app) cmdHookDel(args []string) int {
	if err := newOptionSet().parse(args); err != nil {
		a.log.Errorf("%v", err)
		return exitOK
	}
	if !isRoot() {
		a.log.Errorf("%s", msg.Tf(msg.KeyHookNeedRoot, "hook del"))
		return exitOK
	}

	// HookDel 只做 netlink 的按 pref 删除，是毫秒级操作，不需要超时控制。
	res, err := cfclient.New().HookDel(func(f string, args ...any) { a.log.Debugf(f, args...) })
	if err != nil {
		// 注意：这里**不能**因为"规则不在"就报错 —— 钩子被调用时现场
		// 本来就可能是干净的（比如服务从没起来过）。
		a.log.Warnf("%v", err)
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyHookRemoved, len(res.RulesRemoved), res.RoutesFlushed))
	return exitOK
}

// isRoot 报告当前进程是不是以 root 跑。
func isRoot() bool { return os.Geteuid() == 0 }
