package main

import (
	"context"
	"fmt"
	"time"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// cmdUp 是"把隧道拉起来"的手工入口。
//
// 真正的活都在 ensureTunnel 里 —— 它与保活走的是**同一份代码**。
// 这里只负责解析参数、做前置校验、持锁，然后交给它。
func (a *app) cmdUp(args []string) int {
	var (
		modeStr         string
		backendStr      string
		stackStr        string
		ifName          string
		mtu             int
		endpoint        string
		dnsStr          string
		force           bool
		noConfirm       bool
		guardTimeoutStr string
	)
	opts := newOptionSet().
		Str("--mode", &modeStr).
		Str("--backend", &backendStr).
		Str("--stack", &stackStr).
		Str("--ifname", &ifName).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Str("--dns", &dnsStr).
		Bool("--force", &force).
		Bool("--no-confirm", &noConfirm).
		Str("--guard-timeout", &guardTimeoutStr)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	mode, err := netcfg.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}
	backend, err := tunnel.ParseBackend(backendStr)
	if err != nil {
		return a.fail(err)
	}
	// 只有显式给了 --stack 才覆盖 —— 空值要留给 ensureTunnel 去**沿用
	// 运行态里的选择**（否则一次 `up` 就会把用户选的"只用 IPv4"改回双栈）。
	var stack netcfg.Stack
	if stackStr != "" {
		s, perr := netcfg.ParseStack(stackStr)
		if perr != nil {
			return a.fail(perr)
		}
		stack = s
	}

	// 换了栈就必须重建：接口上原来那一族的地址**不会自己消失**，
	// 而 netcfg 的"先清后加"只清规则与它自己那张路由表，不碰接口地址。
	if stackStr != "" && !force {
		if rt, rerr := state.LoadRuntime(state.RuntimePath()); rerr == nil {
			before := netcfg.Stack(rt.Stack).Normalize()
			if after := stack.Normalize(); before != after {
				force = true
				a.log.Infof("%s", msg.Tf(msg.KeyUpStackRebuild, before, after))
			}
		}
	}

	guardTimeout := rollback.DefaultTimeout
	if guardTimeoutStr != "" {
		d, perr := time.ParseDuration(guardTimeoutStr)
		if perr != nil {
			return a.fail(fmt.Errorf("--guard-timeout 不是合法时长: %w", perr))
		}
		if d <= 0 {
			return a.fail(fmt.Errorf("--guard-timeout 必须为正数（实得 %s）", d))
		}
		guardTimeout = d
	}

	// 建隧道会改系统网络，非 Debian 直接拒绝。
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyUpNotDebian, system.PrettyName()))
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

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	opt := ensureOptions{
		IfName:       ifName,
		Mode:         mode,
		Backend:      backend,
		Stack:        stack,
		MTU:          mtu,
		Endpoint:     endpoint,
		DNS:          splitList(dnsStr),
		Force:        force,
		GuardTimeout: guardTimeout,
		NoConfirm:    noConfirm,
		// 用户要的是"能用"：建好之后立刻验一次，不通就去试另一个后端。
		ProbeAfterEnsure: true,
		// 手工 up 不做"配置一致也硬复位"——那是保活的判断（见 ResetIfIdle）。
		// 用户要强制重建有 --force，语义更明确。
		ResetIfIdle: false,
	}

	res, code := a.ensureTunnel(ctx, acct, opt)
	if code != exitOK {
		return code
	}
	if mode == netcfg.ModeGlobal {
		// 全局模式的收尾文案由 finishGlobal 给出（它还要区分是否确认）。
		return exitOK
	}
	a.log.Infof("%s", res.Detail)
	return exitOK
}
