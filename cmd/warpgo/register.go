package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/wgconf"
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
	if err := opts.parse(args, a.bundle.Tf); err != nil {
		return a.fail(err)
	}

	acctPath := state.AccountPath()
	if _, err := cloudflare.LoadAccount(acctPath); err == nil && !force {
		// 已有账户时默认拒绝重新注册：新账户会分配到不同的 WARP IP，
		// 用户所有对外访问的出口地址都会变。这必须是显式决定，
		// 不能是"顺手又跑了一次 register"的副作用。
		a.log.Errorf("%s", a.bundle.Tf(i18n.KeyRegExists, acctPath))
		return exitError
	}

	// 先确保状态目录可写。放在注册之前是刻意的：否则会先占掉一个服务端设备、
	// 再发现存不下来（既浪费一个设备名额，也让用户以为注册失败了）。
	if err := state.EnsureDir(state.Dir()); err != nil {
		a.log.Errorf("%s: %v", a.bundle.Tf(i18n.KeyNeedRoot, state.Dir()), err)
		return exitError
	}

	a.log.Infof("%s", a.bundle.T(i18n.KeyRegStart))

	// 私钥在本地生成，只把公钥发出去 —— 服务端从头到尾见不到私钥。
	kp, err := wgconf.GenerateKeyPair()
	if err != nil {
		return a.fail(err)
	}

	client := cloudflare.New(cloudflare.Options{Locale: locale})
	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()

	dev, err := client.Register(ctx, kp.Public.String())
	if err != nil {
		return a.fail(err)
	}

	// 注册出来的设备默认 warp_enabled=false，必须 PATCH 一次才真正可用。
	//
	// 失败不致命：账户本身已经有效，稍后可以用 `warpgo account --enable` 补。
	// 所以这里记下错误、继续保存账户，最后再以非零码退出（让脚本知道没完工）。
	var enableErr error
	if d2, eerr := client.EnableWarp(ctx, dev.ID, dev.Token); eerr != nil {
		enableErr = eerr
	} else if d2 != nil {
		// PATCH 的响应只用来补账户信息：**config 一律以注册响应为准**。
		// 服务端在不同情况下返回的 PATCH 响应完整度并不一致，而注册响应是
		// 唯一保证带 config 的那一份；混用两者会引入"有时有地址、有时没有"的
		// 间歇性故障，排查成本极高。
		if d2.Account.AccountType != "" || d2.Account.ID != "" {
			dev.Account = d2.Account
		}
		if d2.Token != "" {
			dev.Token = d2.Token
		}
	}

	acct, err := cloudflare.FromDevice(dev, kp.Private.String(), kp.Public.String())
	if err != nil {
		return a.fail(err)
	}
	if err := acct.Save(acctPath); err != nil {
		return a.fail(err)
	}

	a.log.Infof("%s", a.bundle.Tf(i18n.KeyRegDone, acct.ID, joinOrDash(acct.AccountType)))
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyRegSaved, acctPath))
	if acct.HasReserved() {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyRegReservedOK, acct.Reserved, acct.ClientID))
	} else {
		a.log.Warnf("%s", a.bundle.T(i18n.KeyRegReservedNone))
	}
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyRegAddr, strings.Join(acct.Addresses(), ", ")))
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyRegEndpoint, acct.Endpoint))

	if enableErr != nil {
		a.log.Warnf("%s", a.bundle.Tf(i18n.KeyRegEnableFailed, enableErr))
		return exitError
	}
	return exitOK
}

// loadAccount 读取本机账户，并把"还没注册"翻译成一句可操作的提示。
func (a *app) loadAccount() (*cloudflare.Account, int) {
	acct, err := cloudflare.LoadAccount(state.AccountPath())
	if err != nil {
		if errors.Is(err, cloudflare.ErrNoAccount) {
			a.log.Errorf("%s", a.bundle.T(i18n.KeyAcctNone))
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
