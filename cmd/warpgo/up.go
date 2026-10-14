package main

import (
	"context"
	"fmt"
	"time"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
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
