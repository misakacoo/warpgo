package main

import (
	"context"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/systemd"
)

// downOptions 描述"这次要拆到哪一步"。
//
// `down`（清空全部残留）与 `off`（临时关闭、保留配置）共用同一份拆除实现，
// 差别全部落在这几个开关上 —— 两处各写一遍的话，"拆干净"这件事迟早会分叉。
type downOptions struct {
	// IfName 为空时按运行态、再按默认值推断。
	IfName string

	// KeepWatch 为真时不碰保活服务。
	KeepWatch bool

	// KeepRuntime 为真时**保留运行态文件**。
	//
	// `off` 要的就是它：下次 `on` 才能按上次的模式 / 后端 / 端点 / DNS
	// 原样恢复，而不是退回账户的默认配置。
	// `down` 反过来必须清掉 —— 它的语义是"清空全部残留"。
	KeepRuntime bool
}

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
	return a.downTunnel(ctx, downOptions{IfName: ifName, KeepWatch: keepWatch})
}

// cmdOff 临时关闭隧道（上游菜单 `o` 的"关"）。
//
// 与 `down` 的唯一差别是**保留运行态**，但那一点很要紧：`on` 因此能把隧道
// 按原来的模式 / 后端 / 端点 / DNS 原样恢复，而不是退回账户的默认配置。
// 这正是上游 `o` 的语义（`wg-quick down warp` 保留 `warp.conf`，
// 再按一次 `o` 就用 `net` 拉起来）。
//
// 保活单元只是被 stop，**不动 enabled** —— 所以"临时"是名副其实的：
// 重启之后它自己会把隧道拉回来。
func (a *app) cmdOff(args []string) int {
	if err := newOptionSet().parse(args); err != nil {
		return a.fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	return a.downTunnel(ctx, downOptions{KeepRuntime: true})
}

// downTunnel 是拆除的**唯一实现**，`down` 与 `off` 都走它。
func (a *app) downTunnel(ctx context.Context, opt downOptions) int {
	// ① 先停保活巡检。
	//
	// 必须排在拆隧道**之前**：保活每 5 分钟一轮，只要它还在跑，我们刚拆掉的
	// 东西会在下一轮被它重新建起来 —— 用户看到的是"拆了又自己回来"，
	// 而且会怀疑 down 根本没生效。
	if !opt.KeepWatch {
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
	name := opt.IfName
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

	// ③ 运行态：`down` 清掉（"零残留"），`off` 留着（下次 `on` 按它恢复）。
	if opt.KeepRuntime {
		if _, err := state.LoadRuntime(state.RuntimePath()); err == nil {
			a.log.Infof("%s", msg.T(msg.KeyOffKeptRuntime))
		}
	} else if err := state.RemoveRuntime(state.RuntimePath()); err != nil {
		return a.fail(err)
	}

	// ④ 拆掉之后就没有"待回滚的变更"了：
	//   · 解除可能还在等待的保险丝（否则它超时后会去拆未来建立的东西）；
	//   · 清掉快照，让 confirm 能如实回答"当前没有待确认的改动"。
	if err := rollback.Disarm(); err != nil {
		a.log.Warnf("%v", err)
	}
	if err := rollback.ClearSnapshot(); err != nil {
		a.log.Warnf("%v", err)
	}

	// ⑤ 还原 DNS。只在"我们确实改过它"的时候才会真的动（有备份才还原）。
	//
	// `off` 也必须做这一步：隧道一拆，WARP 的解析器就不可达了，
	// 不还原的话机器会变成"网还在、域名一个都解析不了" —— 那比隧道关掉难受得多。
	// 当初改成了什么记在运行态里（`Runtime.DNS`），`on` 会照着改回去。
	if restored, derr := a.restoreDNS(); derr != nil {
		a.log.Warnf("%v", derr)
	} else if !restored {
		a.log.Debugf("%s", msg.T(msg.KeyUninstallNoDNS))
	}

	if len(res.RulesRemoved) == 0 && res.RoutesFlushed == 0 && !deleted {
		a.log.Infof("%s", msg.T(msg.KeyDownNothing))
		if opt.KeepRuntime {
			a.log.Infof("%s", msg.T(msg.KeyOffDone))
		}
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyDownSummary, len(res.RulesRemoved), res.RoutesFlushed))
	if deleted {
		a.log.Infof("%s", msg.Tf(msg.KeyNetDeletedLink, name))
	}
	if opt.KeepRuntime {
		a.log.Infof("%s", msg.T(msg.KeyOffDone))
	} else {
		a.log.Infof("%s", msg.T(msg.KeyDownDone))
	}
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
