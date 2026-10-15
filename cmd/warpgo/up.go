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
		withKeepalive   bool
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
		Bool("--keepalive", &withKeepalive).
		Str("--guard-timeout", &guardTimeoutStr)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	mode, err := netcfg.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}
	// --backend 没给时**沿用运行态里记的"用户当初要求的那个"**。
	//
	// 这一条 `on` / `watch` / `rotate` 都有，`up` 原先漏了 —— 于是
	// "用菜单换成用户态兼容、再点一次建立隧道"会悄悄换回内核直通。
	// 道理与 rotate 里那句注释同源：**用户的选择不该被一次常规操作抹掉**。
	//
	// 沿用的是**要求**（auto / kernel / userspace）而不是实际用上的那个：
	// 拿实际值当默认，会把一次自动回落永久钉死成 userspace（见 state.Runtime）。
	//
	// **`--mode` 刻意不跟着沿用**：`up` 的默认必须永远是"非全局"。
	// 模式决定要不要接管默认路由，一个没带参数的 `up` 不该有可能切断正在用的 SSH ——
	// 想恢复上次的全局模式用 `warpgo on`（它才是"按上次那份配置原样恢复"）。
	if backendStr == "" {
		if rt, rerr := state.LoadRuntime(state.RuntimePath()); rerr == nil && rt.BackendRequested != "" {
			backendStr = rt.BackendRequested
		}
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
	// released 让"提前交锁"成为可能（见下面 --keepalive 那段）。
	// 不能靠 `defer a.releaseLock(lock)` + 之后把 lock 置空 —— defer 的实参
	// 在**注册那一刻**就求值了，置空改的是局部变量，延迟调用拿到的还是那个指针，
	// 结果是释放两次。
	released := false
	defer func() {
		if !released {
			a.releaseLock(lock)
		}
	}()

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
	// 全局模式的收尾文案由 finishGlobal 给出（它还要区分是否确认），这里不重复。
	if mode != netcfg.ModeGlobal {
		a.log.Infof("%s", res.Detail)
	}

	// 建好之后顺手把保活装上（`--keepalive`，菜单里的"建立隧道"默认带它）。
	//
	// 装之前**先把锁交出去**：`install` 会 restart 保活服务，而服务起来的第一个
	// 动作就是抢同一把锁 —— 握着锁去装它，等于让它开头那几轮必然空转，
	// journal 里还会凭空多出一行"锁被占"（那时我们其实只是在装它）。
	//
	// 注意这一步在**两种模式下都做**：全局模式尤其需要它 —— 它自带的保险丝只管
	// 建起来那一次，管不了之后隧道断掉。
	if withKeepalive {
		released = true
		a.releaseLock(lock)
		a.installKeepalive()
	}
	return exitOK
}
