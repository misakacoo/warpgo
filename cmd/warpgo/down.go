package main

import (
	"context"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/systemd"
)

func (a *app) cmdDown(args []string) int {
	var (
		ifName    string
		keepWatch bool
	)
	opts := newOptionSet().Str("--ifname", &ifName).Bool("--keep-watch", &keepWatch)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	// ① 先停保活巡检。
	//
	// 必须排在拆隧道**之前**：保活每 5 分钟一轮，只要它还在跑，我们刚拆掉的
	// 东西会在下一轮被它重新建起来 —— 用户看到的是"拆了又自己回来"，
	// 而且会怀疑 down 根本没生效。
	if !keepWatch {
		a.stopWatchService(ctx)
	}

	// ② 拿单实例锁。放在停服务之后，才能保证拿到的是干净的。
	lock, code := a.acquireLock()
	if code != exitOK {
		return code
	}
	defer a.releaseLock(lock)

	// 接口名优先取命令行，其次取运行态，最后退回默认值 —— 三者都不依赖
	// 「上一状态是否完好」，所以即使运行态文件已损坏也能把现场清干净。
	name := ifName
	if name == "" {
		if rt, err := state.LoadRuntime(state.RuntimePath()); err == nil && rt.IfName != "" {
			name = rt.IfName
		} else {
			name = netcfg.DefaultIfName
		}
	}

	a.log.Infof("%s", msg.T(msg.KeyDownStart))

	// tearDown 的顺序很重要：**先清规则与路由，再删接口**。
	//
	// 反过来做的话，规则会短暂处于"引用了已不存在的接口"的状态；
	// 上游正是在这里翻过车 —— 接口删掉后 `ip rule show` 把它渲染成
	// `oif warp [detached]`，按字符串匹配就再也删不掉了。
	//
	// 它对两种后端都适用：删接口是同一个动作，而"停用户态守护进程"
	// 在没有守护进程时是空操作。所以 down 不必先判断当前用的是哪种后端。
	res, deleted, err := a.tearDown(name)
	if err != nil {
		return a.fail(err)
	}

	if err := state.RemoveRuntime(state.RuntimePath()); err != nil {
		return a.fail(err)
	}
	// 拆掉之后就没有"待回滚的变更"了：
	//   · 解除可能还在等待的保险丝（否则它超时后会去拆未来建立的东西）；
	//   · 清掉快照，让 confirm 能如实回答"当前没有待确认的改动"。
	if err := rollback.Disarm(); err != nil {
		a.log.Warnf("%v", err)
	}
	if err := rollback.ClearSnapshot(); err != nil {
		a.log.Warnf("%v", err)
	}

	// ③ 还原 DNS。只在"我们确实改过它"的时候才会真的动（有备份才还原）。
	if restored, derr := a.restoreDNS(); derr != nil {
		a.log.Warnf("%v", derr)
	} else if !restored {
		a.log.Debugf("%s", msg.T(msg.KeyUninstallNoDNS))
	}

	if len(res.RulesRemoved) == 0 && res.RoutesFlushed == 0 && !deleted {
		a.log.Infof("%s", msg.T(msg.KeyDownNothing))
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyDownSummary, len(res.RulesRemoved), res.RoutesFlushed))
	if deleted {
		a.log.Infof("%s", msg.Tf(msg.KeyNetDeletedLink, name))
	}
	a.log.Infof("%s", msg.T(msg.KeyDownDone))
	return exitOK
}

// stopWatchService 停掉保活单元（没装就什么都不做）。
//
// 停完还要 reset-failed：单元一旦被标成 failed，systemd 的 restart 语义
// 会退化成"只重启不重载"，后续行为难以推理 —— 上游在这一处吃过亏。
func (a *app) stopWatchService(ctx context.Context) {
	mgr := systemd.New()
	if !mgr.UnitExists() {
		return
	}
	active, err := mgr.IsActive(ctx)
	if err != nil {
		a.log.Debugf("%v", err)
		return
	}
	if active {
		if err := mgr.Stop(ctx); err != nil {
			a.log.Warnf("%v", err)
		} else {
			a.log.Infof("%s", msg.Tf(msg.KeyDownWatchStopped, systemd.UnitName))
		}
	}
	if err := mgr.ResetFailed(ctx); err != nil {
		a.log.Debugf("%v", err)
	}
}
