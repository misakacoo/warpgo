package main

import (
	"context"

	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

func (a *app) cmdDown(args []string) int {
	var ifName string
	opts := newOptionSet().Str("--ifname", &ifName)
	if err := opts.parse(args, a.bundle.Tf); err != nil {
		return a.fail(err)
	}

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

	lock, code := a.acquireLock()
	if code != exitOK {
		return code
	}
	defer a.releaseLock(lock)

	a.log.Infof("%s", a.bundle.T(i18n.KeyDownStart))

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	// 顺序很重要：**先清规则与路由，再删接口**。
	//
	// 反过来做的话，规则会短暂处于"引用了已不存在的接口"的状态；
	// 上游正是在这里翻过车 —— 接口删掉后 `ip rule show` 把它渲染成
	// `oif warp [detached]`，按字符串匹配就再也删不掉了。
	res, err := netcfg.Cleanup(a.netlog)
	if err != nil {
		return a.fail(err)
	}

	tun, err := tunnel.NewKernel(name)
	if err != nil {
		return a.fail(err)
	}
	defer tun.Close()

	deleted, err := tun.TearDown(ctx)
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

	if len(res.RulesRemoved) == 0 && res.RoutesFlushed == 0 && !deleted {
		a.log.Infof("%s", a.bundle.T(i18n.KeyDownNothing))
		return exitOK
	}
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyDownSummary, len(res.RulesRemoved), res.RoutesFlushed))
	if deleted {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyNetDeletedLink, name))
	}
	a.log.Infof("%s", a.bundle.T(i18n.KeyDownDone))
	return exitOK
}
