package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/systemd"
)

// cmdOn 恢复隧道（上游菜单 `o` 的"开"）。
//
// 它**按上次的运行态恢复** —— 模式、后端、MTU、端点、接口名、DNS 都取自
// `runtime.json`，所以"临时关掉再打开"回到的是原来那个配置，
// 而不是账户的默认配置。显式给的选项仍然优先（它们接在默认值后面）。
//
// 实现上是把运行态翻译成一组 `up` 的参数再交给 cmdUp —— 于是"恢复"与"建立"
// 走的是**同一条代码路径**，不会出现"up 能修好、on 修不好"这种分叉。
func (a *app) cmdOn(args []string) int {
	argv := append(a.onDefaults(), args...)
	if code := a.cmdUp(argv); code != exitOK {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	a.startWatchServiceIfEnabled(ctx)
	return exitOK
}

// onDefaults 把上次的运行态翻译成一组 `up` 的参数。
//
// 没有运行态时返回 nil —— 那说明上次是被 `down` 清掉的、或者从来没起来过，
// 那就按账户默认建立，与 `up` 完全一样。
func (a *app) onDefaults() []string {
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil || rt.IfName == "" {
		return nil
	}
	var argv []string
	add := func(flag, val string) {
		if val != "" {
			argv = append(argv, flag, val)
		}
	}
	add("--ifname", rt.IfName)
	add("--mode", rt.Mode)
	// 栈也要恢复：不传的话 ensureTunnel 虽然会沿用运行态，但显式传更清楚 ——
	// `on` 的语义就是"按上次那份配置原样恢复"。
	add("--stack", netcfg.Stack(rt.Stack).Normalize().String())
	// 用**用户当初要求**的后端，而不是这次实际用上的那个：
	// 后者可能是 auto 自动降级的结果，照搬会把它钉死成一个永久选择。
	if rt.BackendRequested != "" {
		add("--backend", rt.BackendRequested)
	} else {
		add("--backend", rt.Backend)
	}
	if rt.MTU > 0 {
		add("--mtu", strconv.Itoa(rt.MTU))
	}
	add("--endpoint", rt.Endpoint)
	if len(rt.DNS) > 0 {
		add("--dns", strings.Join(rt.DNS, ","))
	}
	return argv
}

// startWatchServiceIfEnabled 把保活拉回来（只在它本来就被设为开机自启时）。
//
// 判据用 `is-enabled` 而不是"我们记得它之前是开着的"：这是**无状态**的 ——
// 重启过机器、换过 shell、甚至换过一次 warpgo 版本，判断都一样。
// 记一个 `was_watching` 标志反而会多出一份可能与现实不符的状态。
func (a *app) startWatchServiceIfEnabled(ctx context.Context) {
	mgr := systemd.New()
	if !mgr.UnitExists() {
		return
	}
	enabled, err := mgr.IsEnabled(ctx)
	if err != nil || !enabled {
		return
	}
	if active, err := mgr.IsActive(ctx); err == nil && active {
		return
	}
	if err := mgr.Start(ctx); err != nil {
		a.log.Warnf("%v", err)
		return
	}
	a.log.Infof("%s", msg.Tf(msg.KeyOnWatchStarted, systemd.UnitName))
}
