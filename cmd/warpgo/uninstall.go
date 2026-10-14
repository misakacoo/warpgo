package main

import (
	"context"
	"errors"
	"os"

	"github.com/misakacoo/warpgo/internal/dns"
	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/rollback"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/systemd"
)

// knownStateFiles 是 warpgo 自己写进状态目录的文件。
//
// `--purge` 只删这些，出现别的一律拒绝 —— 见 purgeStateDir 的说明。
var knownStateFiles = map[string]bool{
	state.AccountFile:  true,
	state.RuntimeFile:  true,
	dns.BackupFileName: true,
	dns.MetaFileName:   true,
	// 预留：将来加用户可编辑的默认值文件时不用再改这里。
	"warpgo.toml": true,
}

// cmdUninstall 把 warpgo 从这台机器上撤干净。
//
// 顺序是有讲究的，尤其是 DNS 还原必须排在删除状态目录**之前** ——
// 原始内容就保存在那里，先删掉它就再也还原不回去了。
// 旧版正是丢在这个顺序上：卸载后 /etc/resolv.conf 还是 `nameserver 1.1.1.1`，
// 而备份已被删除，用户连"原来是什么"都无从得知。
func (a *app) cmdUninstall(args []string) int {
	var (
		ifName     string
		purge      bool
		keepBinary bool
	)
	opts := newOptionSet().
		Str("--ifname", &ifName).
		Bool("--purge", &purge).
		Bool("--keep-binary", &keepBinary)
	if err := opts.parse(args, a.bundle.Tf); err != nil {
		return a.fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	mgr := systemd.New()
	unitExisted := mgr.UnitExists()

	// ① 先停服务。必须在拆隧道之前 —— 否则保活会在下一轮把隧道又建起来，
	//    用户看到的是"卸了又自己回来"。
	if unitExisted {
		if active, err := mgr.IsActive(ctx); err == nil && active {
			if err := mgr.Stop(ctx); err != nil {
				a.log.Warnf("%v", err)
			} else {
				a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallStopped, systemd.UnitName))
			}
		}
		if err := mgr.Disable(ctx); err != nil {
			a.log.Warnf("%v", err)
		} else {
			a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallDisabled, systemd.UnitName))
		}
		if err := mgr.RemoveUnit(); err != nil {
			a.log.Warnf("%v", err)
		} else {
			a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallUnitRemoved, mgr.UnitPath()))
		}
		if err := mgr.DaemonReload(ctx); err != nil {
			a.log.Warnf("%v", err)
		}
	} else {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallNoUnit, mgr.UnitPath()))
	}
	// reset-failed 无条件执行：即使单元文件已经删了，systemd 里可能还留着
	// failed 记录（那会让后续同名的单元启动行为变得难以推理）。
	if err := mgr.ResetFailed(ctx); err != nil {
		a.log.Debugf("%v", err)
	}

	// ② 拆隧道。锁在这里才拿：先停服务再拿锁，才能保证拿到的是干净的。
	lock, code := a.acquireLock()
	if code != exitOK {
		a.log.Warnf("%s", a.bundle.T(i18n.KeyUninstallLockBusy))
	} else {
		defer a.releaseLock(lock)
	}

	name := ifName
	if name == "" {
		if rt, err := state.LoadRuntime(state.RuntimePath()); err == nil && rt.IfName != "" {
			name = rt.IfName
		} else {
			name = netcfg.DefaultIfName
		}
	}
	if err := a.tearDown(ctx, name); err != nil {
		a.log.Warnf("%v", err)
	}
	if err := state.RemoveRuntime(state.RuntimePath()); err != nil {
		a.log.Warnf("%v", err)
	}
	// 保险丝与其快照都清掉：卸载之后不该再有任何东西会去动网络。
	if err := rollback.Disarm(); err != nil {
		a.log.Warnf("%v", err)
	}
	if err := rollback.ClearSnapshot(); err != nil {
		a.log.Warnf("%v", err)
	}
	a.log.Infof("%s", a.bundle.T(i18n.KeyUninstallTornDown))

	// ③ 还原 DNS。必须在删状态目录之前（备份就在那里）。
	restored, err := a.restoreDNS()
	switch {
	case err != nil:
		a.log.Warnf("%v", err)
	case restored:
		// restoreDNS 自己已经打印了
	default:
		a.log.Debugf("%s", a.bundle.T(i18n.KeyUninstallNoDNS))
	}

	// ④ 删二进制。
	if keepBinary {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallKeptBinary, mgr.BinPath()))
	} else if err := os.Remove(mgr.BinPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallNoBinary, mgr.BinPath()))
		} else {
			a.log.Warnf("%v", err)
		}
	} else {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallBinaryRemoved, mgr.BinPath()))
	}

	// ⑤ 运行时目录。
	if err := os.RemoveAll(rollback.RunDir()); err != nil {
		a.log.Warnf("%v", err)
	}

	// ⑥ 状态目录。默认保留账户 —— 它是一个身份，删掉就意味着下次注册会拿到
	//    一个新的 WARP 出口地址。这种事必须由用户明确要求。
	if purge {
		removed, err := a.purgeStateDir(state.Dir())
		if err != nil {
			a.log.Errorf("%v", err)
			a.log.Warnf("%s", a.bundle.Tf(i18n.KeyUninstallStateKept, state.Dir()))
			return exitError
		}
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallStatePurged, state.Dir(), len(removed)))
	} else {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyUninstallStateKept, state.Dir()))
	}

	a.log.Infof("%s", a.bundle.T(i18n.KeyUninstallDone))
	return exitOK
}
