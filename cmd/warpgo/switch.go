package main

import (
	"context"
	"fmt"

	"github.com/misakacoo/warpgo/internal/keepalive"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/systemd"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// cmdSwitch 在线互换隧道后端（菜单 `k`）与工作模式（菜单 `g`）。
//
// 上游这两个菜单项是两条各写一遍的代码：`k` 靠替换 /usr/bin/wg-quick 的符号链接
// （指向原始版或打过补丁的 wireguard-go），`g` 靠 sed 改配置文件里的注释。
// 这里它们变成同一个动作 —— "改掉一个设置，然后重建隧道"：
//
//   - 两种后端在 warpgo 里只是 tunnel 包的两个实现，互换 = 用另一个实现重建；
//   - 模式只影响策略路由，规则是"先清后加"的，重建时自然收敛。
//
// 出口地址**不会**因为互换而改变 —— 它是账户里分配的固定地址。
func (a *app) cmdSwitch(args []string) int {
	var (
		backendStr string
		modeStr    string
		ifName     string
		mtu        int
		endpoint   string
		dnsStr     string
	)
	opts := newOptionSet().
		Str("--backend", &backendStr).
		Str("--mode", &modeStr).
		Str("--ifname", &ifName).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Str("--dns", &dnsStr)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}
	if backendStr == "" && modeStr == "" {
		return a.fail(fmt.Errorf("至少要指定 --backend 或 --mode 之一（例如 --backend userspace / --mode global）"))
	}

	// 没指定的那项沿用运行态里的值：`switch --backend userspace` 不该
	// 顺手把全局模式改成非全局。
	curModeStr, curBackendStr, ifName, mtu := applyRuntimeDefaults("", "", ifName, mtu)
	if modeStr == "" {
		modeStr = curModeStr
	}
	if backendStr == "" {
		backendStr = curBackendStr
	}
	mode, err := netcfg.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}
	fromBackend, err := tunnel.ParseBackend(curBackendStr)
	if err != nil {
		return a.fail(err)
	}
	toBackend, err := tunnel.ParseBackend(backendStr)
	if err != nil {
		return a.fail(err)
	}
	fromMode, err := netcfg.ParseMode(curModeStr)
	if err != nil {
		return a.fail(err)
	}

	if fromBackend == toBackend && fromMode == mode {
		a.log.Infof("%s", msg.Tf(msg.KeySwitchNoChange, backendLabel(toBackend), mode))
		return exitOK
	}

	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeySwitchNotDebian, system.PrettyName()))
		return exitNotDebian
	}

	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}

	lock, code := a.acquireLock()
	if code != exitOK {
		return code
	}
	defer a.releaseLock(lock)

	a.log.Infof("%s", msg.T(msg.KeySwitchStart))
	if fromBackend != toBackend {
		a.log.Infof("%s", msg.Tf(msg.KeySwitchBackend, backendLabel(fromBackend), backendLabel(toBackend)))
	}
	if fromMode != mode {
		a.log.Infof("%s", msg.Tf(msg.KeySwitchMode, fromMode, mode))
	}

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	// Force 是必须的：内核的 wireguard 接口与用户态的 TUN 是两种设备类型，
	// 在同一名字上不能共存，必须先把旧的拆掉。
	//
	// 这里传的是**用户要求的后端**（toBackend，可能是 auto），而不是
	// "当前实际用的那个" —— 否则 `switch --backend userspace` 之后想切回
	// 自动，就没有任何命令能做到。
	opt := ensureOptions{
		IfName:       ifName,
		Mode:         mode,
		Backend:      toBackend,
		MTU:          mtu,
		Endpoint:     endpoint,
		DNS:          splitList(dnsStr),
		Force:        true,
		GuardTimeout: rollback.DefaultTimeout,
	}
	if _, code := a.ensureTunnel(ctx, acct, opt); code != exitOK {
		return code
	}

	// 保活单元里钉着 --backend / --mode。不同步改写的话，它下一轮就会
	// 按旧参数把隧道重建回去 —— 用户看到的是"切了没用"。
	a.syncWatchUnit(ctx, watchUnitParams{
		IfName:   ifName,
		Mode:     mode,
		Backend:  toBackend,
		Interval: 0, // 0 表示"沿用默认间隔"
		MTU:      mtu,
		Endpoint: endpoint,
		DNS:      splitList(dnsStr),
	})

	a.log.Infof("%s", msg.Tf(msg.KeySwitchDone, backendLabel(toBackend), mode))
	return exitOK
}

// syncWatchUnit 在保活单元已经装好的前提下，把它的启动参数改写成新设置并重启。
//
// 没装单元时什么都不做 —— `switch` 不该顺手把保活装上（那是 `install` 的职责，
// 用户可能故意只用手工 up/down）。
func (a *app) syncWatchUnit(ctx context.Context, p watchUnitParams) {
	mgr := systemd.New()
	if !mgr.UnitExists() {
		return
	}
	if p.Interval <= 0 {
		p.Interval = keepalive.DefaultInterval
	}
	if p.IfName == "" {
		p.IfName = netcfg.DefaultIfName
	}
	if err := mgr.WriteUnit(renderWatchUnit(mgr, p)); err != nil {
		a.log.Warnf("%v", err)
		return
	}
	if err := mgr.DaemonReload(ctx); err != nil {
		a.log.Warnf("%v", err)
		return
	}
	if err := mgr.Restart(ctx); err != nil {
		a.log.Warnf("%v", err)
		return
	}
	a.log.Infof("%s", msg.T(msg.KeySwitchUnit))
}
