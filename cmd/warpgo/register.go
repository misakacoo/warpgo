package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/state"
)

// registerTimeout 覆盖「注册 + 启用」两步，含限流重试的退避时间。
//
// 上限按最坏情况估：3 次重试的退避是 8+16+24 = 48 秒，再加上请求本身的往返。
// 给到 3 分钟是为了让"被限流但最终成功"这条路径能走完，而不是在中途被超时掐掉
// 留下一个半成品设备。
const registerTimeout = 3 * time.Minute

// apiTimeout 是只读/轻量写操作的超时（查账户、启用、绑 license）。
const apiTimeout = 90 * time.Second

func (a *app) cmdRegister(args []string) int {
	var (
		force  bool
		locale string
	)
	opts := newOptionSet().Bool("--force", &force).Str("--locale", &locale)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	acctPath := state.AccountPath()
	if _, err := cloudflare.LoadAccount(acctPath); err == nil && !force {
		// 已有账户时默认拒绝重新注册：新账户会分配到不同的 WARP IP，
		// 用户所有对外访问的出口地址都会变。这必须是显式决定，
		// 不能是"顺手又跑了一次 register"的副作用。
		a.log.Errorf("%s", msg.Tf(msg.KeyRegExists, acctPath))
		return exitError
	}

	// 先确保状态目录可写。放在注册之前是刻意的：否则会先占掉一个服务端设备、
	// 再发现存不下来（既浪费一个设备名额，也让用户以为注册失败了）。
	if err := state.EnsureDir(state.Dir()); err != nil {
		a.log.Errorf("%s: %v", msg.Tf(msg.KeyNeedRoot, state.Dir()), err)
		return exitError
	}

	a.log.Infof("%s", msg.T(msg.KeyRegStart))

	// 私钥在本地生成，只把公钥发出去 —— 服务端从头到尾见不到私钥。
	// （注册 + 启用这一段与 rotate 共用，见 registerDevice。）
	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()

	acct, enableErr, err := a.registerDevice(ctx, locale)
	if err != nil {
		return a.fail(err)
	}
	if err := acct.Save(acctPath); err != nil {
		return a.fail(err)
	}

	a.log.Infof("%s", msg.Tf(msg.KeyRegDone, acct.ID, joinOrDash(acct.AccountType)))
	a.log.Infof("%s", msg.Tf(msg.KeyRegSaved, acctPath))
	if acct.HasReserved() {
		a.log.Infof("%s", msg.Tf(msg.KeyRegReservedOK, acct.Reserved, acct.ClientID))
	} else {
		a.log.Warnf("%s", msg.T(msg.KeyRegReservedNone))
	}
	a.log.Infof("%s", msg.Tf(msg.KeyRegAddr, strings.Join(acct.Addresses(), ", ")))
	a.log.Infof("%s", msg.Tf(msg.KeyRegEndpoint, acct.Endpoint))

	if enableErr != nil {
		// 账户本身已经有效，只是 PATCH 没成功；记下来但不当致命错误。
		a.log.Warnf("%s", msg.Tf(msg.KeyRegEnableFailed, enableErr))
		return exitError
	}
	return exitOK
}

// loadAccount 读取本机账户，并把"还没注册"翻译成一句可操作的提示。
func (a *app) loadAccount() (*cloudflare.Account, int) {
	acct, err := cloudflare.LoadAccount(state.AccountPath())
	if err != nil {
		if errors.Is(err, cloudflare.ErrNoAccount) {
			a.log.Errorf("%s", msg.T(msg.KeyAcctNone))
			return nil, exitError
		}
		a.log.Errorf("%v", err)
		return nil, exitError
	}
	return acct, exitOK
}

// fail 打印错误并返回通用错误码。
func (a *app) fail(err error) int {
	a.log.Errorf("%v", err)
	return exitError
}
