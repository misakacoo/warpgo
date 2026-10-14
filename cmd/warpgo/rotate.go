package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/health"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/tunnel"
	"github.com/misakacoo/warpgo/internal/wgconf"
)

// defaultRotateAttempts 是换 IP 的默认尝试次数。
//
// 3 次足够：每次注册都会分配一个新设备地址，连续三次都撞上同一个地址
// 在现实中基本不会发生；而次数再多只会拉长限流窗口（CF 对 /reg 有 1015 限流）。
const defaultRotateAttempts = 3

// maxRotateAttempts 是 --attempts 的上限。
//
// 它存在的意义是别把限流撞死：注册接口对同一来源有速率限制，
// 一次跑十几次注册会让随后的正常操作（比如 register）也一起被拒。
const maxRotateAttempts = 10

// cmdRotate 换一个新的 WARP 出口地址 —— 对应菜单里的 `i`（换 IP）。
//
// 与上游 change_ip() 的关系：上游是"反复重注册 + 用 Netflix 页面判断地区"，
// 刷到能解锁流媒体的那个 IP 为止。这里只做**换 IP**本身，不做流媒体探测 ——
// 探测要依赖第三方页面的 HTML 结构、还要引 UA 与地区映射表，
// 与"拿到一个不同的出口地址"是两件事（而且前者随时会因对方改页面而失效）。
//
// 安全性设计：**任何失败都回退到原账户并把隧道重建起来**。
// 用户机器上的隧道可能是他唯一的对外通道，换 IP 失败绝不能把通道弄丢。
func (a *app) cmdRotate(args []string) int {
	var (
		attempts   int
		ifName     string
		modeStr    string
		backendStr string
		mtu        int
		endpoint   string
		dnsStr     string
	)
	opts := newOptionSet().
		Int("--attempts", &attempts).
		Str("--ifname", &ifName).
		Str("--mode", &modeStr).
		Str("--backend", &backendStr).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Str("--dns", &dnsStr)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	// 模式 / 后端 / 接口名 / MTU 的默认值全部取自运行态：换 IP 之后
	// 隧道必须还是原来那个样子。如果这里退回"非全局"的默认值，
	// 用户的全局隧道会在一次 rotate 之后无声无息地降级。
	modeStr, backendStr, ifName, mtu = applyRuntimeDefaults(modeStr, backendStr, ifName, mtu)
	if ifName == "" {
		ifName = netcfg.DefaultIfName
	}
	mode, err := netcfg.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}
	backend, err := tunnel.ParseBackend(backendStr)
	if err != nil {
		return a.fail(err)
	}
	if attempts <= 0 {
		attempts = defaultRotateAttempts
	}
	if attempts > maxRotateAttempts {
		return a.fail(fmt.Errorf("--attempts 最大 %d（再多只会撞上注册接口的限流）", maxRotateAttempts))
	}

	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyUpNotDebian, system.PrettyName()))
		return exitNotDebian
	}

	old, code := a.loadAccount()
	if code != exitOK {
		return code
	}

	lock, code := a.acquireLock()
	if code != exitOK {
		return code
	}
	defer a.releaseLock(lock)

	a.log.Infof("%s", msg.Tf(msg.KeyRotateStart, attempts))
	a.log.Infof("%s", msg.Tf(msg.KeyRotateOldAddr, joinOrDash(strings.Join(old.Addresses(), ", "))))

	// 换之前先记下"公网看到的出口 IP"。
	//
	// 账户与隧道地址**一定**会换（那是重新注册的直接结果），但出口 IP 由
	// Cloudflare 的 PoP 决定：同一 PoP 上的免费账户共享出口，所以它可能不变。
	// 用户真正在意的恰恰是后者，所以必须如实测一次并如实汇报 ——
	// 只说"地址换了"会让人以为访问网站时看到的 IP 也换了。
	beforeIP := a.exitIP(ifName)
	if beforeIP != "" {
		a.log.Infof("%s", msg.Tf(msg.KeyRotateOldExit, beforeIP))
	}

	// ① 注册到"地址确实不同"为止。
	//
	// 判据是**地址集合不完全相同**，而不是"每一族都变了"：单栈账户只有一族，
	// 要求全变会让它永远认为没换成，然后白白把尝试次数耗光。
	regCtx, regCancel := context.WithTimeout(context.Background(), registerTimeout)
	defer regCancel()

	picked, lastErr := a.pickRotatedAccount(regCtx, old, attempts)
	if picked == nil {
		if lastErr != nil {
			a.log.Errorf("%s", msg.Tf(msg.KeyRotateFailed, lastErr))
			return exitError
		}
		// 注册全部成功、但每次拿到的地址都和现在一样：这是一次没有意义的
		// 重建（会换接口、换握手，却换不来新地址）。**不动现有隧道**。
		a.log.Errorf("%s", msg.Tf(msg.KeyRotateFailed, errors.New("多次注册拿到的地址都与当前相同")))
		return exitError
	}
	a.log.Infof("%s", msg.Tf(msg.KeyRotatePicked, strings.Join(picked.Addresses(), ", ")))

	// ② 用新账户把隧道重建起来。
	//
	// Force 是必须的：密钥、接口地址、对端都可能变，在旧接口上"补地址"
	// 只会留下双份地址，而旧客户的私钥与新的对不上。重建才是唯一正确的做法。
	opt := ensureOptions{
		IfName:       ifName,
		Mode:         mode,
		Backend:      backend,
		MTU:          mtu,
		Endpoint:     endpoint,
		DNS:          splitList(dnsStr),
		Force:        true,
		GuardTimeout: rollback.DefaultTimeout,
	}
	upCtx, upCancel := context.WithTimeout(context.Background(), upTimeout)
	defer upCancel()

	a.log.Infof("%s", msg.T(msg.KeyRotateRebuild))
	res, code := a.ensureTunnel(upCtx, picked, opt)
	var verifyDetail string
	if code == exitOK {
		// **必须自己验一次**，不能只看 ensureTunnel 的退出码：
		// 非全局模式的 up 本来就不探连通性（那是 M2 的既定语义），
		// 而且用户可能把后端钉死了（那时不会触发自动切换里的那次验证）。
		// 而 rotate 的全部安全承诺都建立在"新账户确实能用"之上 ——
		// 验不过就退回原账户，绝不把一份没验证过的凭据写进磁盘。
		detail, verr := a.verifyViaTunnel(upCtx, picked, res)
		if verr != nil {
			a.log.Errorf("%s", msg.Tf(msg.KeyUpCheckFail, verr))
			code = exitUnhealthy
		} else {
			verifyDetail = detail
		}
	}
	if code == exitOK {
		if verifyDetail != "" {
			a.log.Infof("%s", msg.Tf(msg.KeyUpCheckOK, verifyDetail))
		}
		// ③ 只有隧道真的通了才把磁盘上的账户换成新的。
		//
		// 顺序不能反：先落盘再验证的话，一次换 IP 失败会留下"磁盘上是新账户、
		// 隧道是旧账户"的分裂现场，而保活下一轮读到的就是那个新账户 ——
		// 它会试图用一份从没成功过的配置去修隧道。
		if err := picked.Save(state.AccountPath()); err != nil {
			return a.fail(fmt.Errorf(
				"新隧道已通，但保存新账户失败: %w（磁盘上仍是旧账户，执行 `warpgo up` 可让两者一致）", err))
		}
		a.log.Infof("%s", msg.Tf(msg.KeyRotateNewAddr, strings.Join(picked.Addresses(), ", ")))
		a.log.Infof("%s", res.Detail)

		// 如实汇报"公网出口到底换没换"。
		afterIP := a.exitIP(ifName)
		switch {
		case beforeIP == "" || afterIP == "":
			a.log.Infof("%s", msg.T(msg.KeyRotateExitUnknown))
		case beforeIP == afterIP:
			a.log.Warnf("%s", msg.Tf(msg.KeyRotateExitSame, afterIP))
		default:
			a.log.Infof("%s", msg.Tf(msg.KeyRotateExitChanged, beforeIP, afterIP))
		}

		// ④ 最后才注销旧设备：此时新链路已经验证过，退回旧账户这条路
		//    已经不需要了。反过来先注销的话，一旦新链路随后出问题，
		//    我们就既没有新链路、也没有旧账户。
		a.bestEffortDeleteAccount(old)
		return exitOK
	}

	// 换 IP 没成功 —— 把原账户与隧道恢复回来。
	//
	// 这一步比"换 IP"重要得多：用户的隧道可能就是他唯一的对外通道。
	a.log.Warnf("%s", msg.T(msg.KeyRotateRestoring))
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), upTimeout)
	defer restoreCancel()
	if _, rcode := a.ensureTunnel(restoreCtx, old, opt); rcode != exitOK {
		return a.fail(fmt.Errorf(
			"换 IP 失败（退出码 %d），且用原账户重建也没成功（退出码 %d）—— 请手工执行 `warpgo up -v` 查看现场",
			code, rcode))
	}
	a.log.Warnf("%s", msg.Tf(msg.KeyRotateRestored, strings.Join(old.Addresses(), ", ")))
	return code
}

// pickRotatedAccount 反复注册，直到拿到一个地址与旧账户不同的新账户。
//
// 返回 (新账户, 最后一次错误)。新账户为 nil 时表示"试完了都没换成"：
//   - lastErr 非空 → 中间有注册失败（网络 / 限流 / 接口变更）
//   - lastErr 为空 → 每次都注册成功，但地址与旧的一模一样
func (a *app) pickRotatedAccount(ctx context.Context, old *cloudflare.Account, attempts int) (*cloudflare.Account, error) {
	var lastErr error
	for i := 1; i <= attempts; i++ {
		a.log.Infof("%s", msg.Tf(msg.KeyRotateAttempt, i))
		cand, enableErr, err := a.registerDevice(ctx, "")
		if err != nil {
			lastErr = err
			a.log.Warnf("%v", err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		if enableErr != nil {
			// 设备注册出来了但没能启用 WARP，它建不起隧道 —— 对 rotate 来说
			// 就是一次失败。顺手注销掉，免得留在账户里。
			lastErr = enableErr
			a.log.Warnf("%v", enableErr)
			a.bestEffortDeleteAccount(cand)
			continue
		}
		if addressesDiffer(old, cand) {
			return cand, nil
		}
		a.log.Warnf("%s", msg.Tf(msg.KeyRotateSameAddr, strings.Join(cand.Addresses(), ", ")))
		// 这次注册出来的设备我们不会用了 —— 立刻注销掉，
		// 否则每换一次 IP 就在账户里多留一个孤儿设备。
		a.bestEffortDeleteAccount(cand)
	}
	return nil, lastErr
}

// addressesDiffer 报告两个账户的出口地址是否不同。
func addressesDiffer(old, cand *cloudflare.Account) bool {
	if old == nil || cand == nil {
		return true
	}
	os, cs := old.Addresses(), cand.Addresses()
	if len(os) != len(cs) {
		return true
	}
	for i := range os {
		if os[i] != cs[i] {
			return true
		}
	}
	return false
}

// exitIP 探一次"公网看到的出口 IP"。
//
// 只读；探不到就返回空串（绝不猜）。探不到的正常原因有两种，都不该当成错误：
//   - 隧道当前没在跑（比如先 down 再 rotate）
//   - 探测本身失败（网络抖动）
//
// 必须确认 warp=on 才采信这个 IP：否则拿到的可能是宿主自己的出口地址，
// 用它去比较会得出"IP 一点没变"这种完全错误的结论。
func (a *app) exitIP(ifName string) string {
	if !netcfg.LinkExists(ifName) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*verifyTimeout)
	defer cancel()
	res, err := health.ProbeAny(ctx, []int{health.FamilyV4, health.FamilyV6}, health.Options{
		Interface: ifName,
		Timeout:   verifyTimeout,
	})
	if err != nil || !res.WarpOn() {
		return ""
	}
	return res.IP
}

// bestEffortDeleteAccount 尽力把一台设备从服务端注销掉，失败只记一条警告。
//
// 为什么不把失败当错误：注销是"打扫战场"，它失败不影响当前链路可用，
// 而把一次成功的换 IP 变成"退出码非零"会让脚本误判成失败。
func (a *app) bestEffortDeleteAccount(acct *cloudflare.Account) {
	if acct == nil || strings.TrimSpace(acct.ID) == "" || strings.TrimSpace(acct.Token) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	if err := cloudflare.New(cloudflare.Options{}).DeleteDevice(ctx, acct.ID, acct.Token); err != nil {
		a.log.Warnf("%s", msg.Tf(msg.KeyRotateOldDevFail, acct.ID, err))
		return
	}
	a.log.Infof("%s", msg.Tf(msg.KeyRotateOldDevice, acct.ID))
}

// applyRuntimeDefaults 用运行态里的值补齐没在命令行指定的选项。
//
// 每个会**重建隧道**的子命令都要这么做。理由不是"方便"，而是正确性：
// `--mode global` 起过隧道之后，如果 rotate / switch 退回 non-global 的默认值，
// 用户会在毫不知情的情况下从全局掉回非全局 —— 表面上只是"换了个 IP"。
func applyRuntimeDefaults(modeStr, backendStr, ifName string, mtu int) (string, string, string, int) {
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil {
		return modeStr, backendStr, ifName, mtu
	}
	if modeStr == "" && rt.Mode != "" {
		modeStr = rt.Mode
	}
	if backendStr == "" {
		// 取 BackendRequested 而不是 Backend：前者是用户的原始意图
		// （可能是 auto），后者只是上次实际落到了哪一个。
		backendStr = rt.BackendRequested
	}
	if ifName == "" {
		ifName = rt.IfName
	}
	if mtu == 0 {
		mtu = rt.MTU
	}
	return modeStr, backendStr, ifName, mtu
}

// registerDevice 注册一台新设备并启用 WARP。
//
// cmdRegister 与 rotate 共用它 —— 两条路径各写一遍注册流程的话，
// 迟早会出现"手工 register 能成、rotate 却总失败"这类只在一条路径上的偏差。
//
// 返回值刻意把 enableErr 单独拿出来：注册本身成功时账户就已经有效，
// 而 PATCH 失败是可以稍后补的（cmdRegister 就按"记下来但继续"处理），
// 而 rotate 需要的是"这台设备现在就能用"，所以它把 enableErr 当失败。
func (a *app) registerDevice(ctx context.Context, locale string) (*cloudflare.Account, error, error) {
	kp, err := wgconf.GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	client := cloudflare.New(cloudflare.Options{Locale: locale})

	dev, err := client.Register(ctx, kp.Public.String())
	if err != nil {
		return nil, nil, err
	}

	// 注册出来的设备默认 warp_enabled=false，必须 PATCH 一次才真正可用。
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
		return nil, enableErr, err
	}
	return acct, enableErr, nil
}
