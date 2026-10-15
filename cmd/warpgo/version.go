package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/systemd"
	"github.com/misakacoo/warpgo/internal/update"
	"github.com/misakacoo/warpgo/internal/version"
)

// updateTimeout 覆盖"查发布 + 下载 + 校验 + 替换"。
//
// 下载是其中最不可控的一段（GitHub 在部分线路上很慢），所以给得比别的命令宽。
const updateTimeout = 5 * time.Minute

// cmdVersion 打印版本信息，并可选地查新版本（`--check`）或自我更新（`--self-update`）。
//
// 注意：这一族命令**不做 Debian 强校验**。`--check` 是只读的，在开发机与 CI 上
// 都应该能用；`--self-update` 只替换 warpgo 自己那一个文件，不碰任何系统配置
// —— 真正会改系统状态的命令（`up` / `install` / `client` …）才需要那道校验。
func (a *app) cmdVersion(args []string) int {
	var (
		check      bool
		selfUpdate bool
		force      bool
		noRestart  bool
		tag        string
		repo       string
		apiBase    string
		base       string
	)
	opts := newOptionSet().
		Bool("--check", &check).
		Bool("--self-update", &selfUpdate).
		Bool("--force", &force).
		Bool("--no-restart", &noRestart).
		Str("--version", &tag).
		Str("--repo", &repo).
		// --api-base / --base 覆盖 GitHub API 与下载前缀：给镜像与自建发布页用，
		// 也是真机验收能构造"坏产物 / 校验和不符"这些情形的唯一办法
		// （真 GitHub 上没法往发布里塞一个坏产物）。
		Str("--api-base", &apiBase).
		Str("--base", &base)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}
	// `--version v0.2.0` 的意思就是"装这个版本"，不必再写一遍 --self-update。
	if tag != "" {
		selfUpdate = true
	}

	fmt.Fprintf(a.stdout, "%s %s\n", msg.T(msg.KeyVersion), version.Info())
	if v, ok := system.DebianVersion(); ok {
		a.log.Infof("%s", msg.Tf(msg.KeyPlatformDebian, v))
	} else {
		a.log.Warnf("%s", msg.Tf(msg.KeyPlatformNotDebia, runtime.GOOS))
	}

	if !check && !selfUpdate {
		return exitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	cl := &update.Client{
		Repo:    repo,
		APIBase: apiBase,
		Base:    base,
		Log:     func(format string, args ...any) { a.log.Debugf(format, args...) },
	}

	if !selfUpdate {
		return a.versionCheck(ctx, cl)
	}
	return a.versionSelfUpdate(ctx, cl, tag, force, noRestart)
}

// versionCheck 查一次有没有新版本。
//
// 退出码始终是 0（查询成功）/ 1（查询失败）—— "落后了"不算错误，
// 它写在输出里。这样脚本里可以放心地把它当成一次只读探测。
func (a *app) versionCheck(ctx context.Context, cl *update.Client) int {
	res, err := cl.Check(ctx, version.Version)
	if err != nil {
		a.log.Errorf("%s", msg.Tf(msg.KeyVerCheckFailed, err))
		return exitError
	}
	if !res.Comparable {
		// 当前是 `dev`（构建时没注入版本号）—— 两边没法比。
		// 这件事必须说清楚，否则会被人读成"已经是最新"。
		a.log.Warnf("%s", msg.Tf(msg.KeyVerNoCompare, res.Current, res.Latest))
	} else if res.Newer {
		a.log.Warnf("%s", msg.Tf(msg.KeyVerNewer, res.Latest, res.Current))
		a.log.Infof("%s", msg.T(msg.KeyVerUpdateHint))
	} else {
		a.log.Infof("%s", msg.Tf(msg.KeyVerUpToDate, res.Current, res.Latest))
	}
	if res.PageURL != "" {
		a.log.Infof("%s", msg.Tf(msg.KeyVerReleasePage, res.PageURL))
	}
	return exitOK
}

// versionSelfUpdate 把自己换成新版本。
func (a *app) versionSelfUpdate(ctx context.Context, cl *update.Client, tag string, force, noRestart bool) int {
	dst, err := os.Executable()
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", msg.T(msg.KeyVerNoSelfPath), err))
	}

	a.log.Infof("%s", msg.Tf(msg.KeyVerUpdateStart, version.Version))
	res, err := cl.Update(ctx, dst, update.UpdateOptions{
		Tag:     tag,
		Force:   force,
		Current: version.Version,
	})
	if err != nil {
		a.log.Errorf("%s", msg.Tf(msg.KeyVerUpdateFailed, err))
		return exitError
	}
	if !res.Changed {
		a.log.Infof("%s", msg.Tf(msg.KeyVerNoChange, res.From, res.To))
		a.log.Infof("%s", msg.T(msg.KeyVerForceHint))
		return exitOK
	}

	a.log.Infof("%s", msg.Tf(msg.KeyVerUpdateDone, res.To, res.Path, shortSHA(res.SHA256)))

	// 保活单元跑的是**旧的那份可执行文件**（进程起来时就已经映射好了），
	// 不重启它就等于"更新了但没生效"。重启是安全的：`watch` 幂等，
	// 健康时什么都不做。
	if noRestart {
		a.log.Infof("%s", msg.T(msg.KeyVerNoRestart))
		return exitOK
	}
	mgr := systemd.New()
	active, aerr := mgr.IsActive(ctx)
	if aerr != nil || !active {
		return exitOK
	}
	if err := mgr.Restart(ctx); err != nil {
		a.log.Warnf("%s", msg.Tf(msg.KeyVerRestartFailed, err))
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyVerRestarted, systemd.UnitName))
	return exitOK
}
