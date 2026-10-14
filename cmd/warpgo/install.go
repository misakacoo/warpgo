package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/keepalive"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/system"
	"github.com/misakacoo/warpgo/internal/systemd"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// watchUnitParams 是一次"装 / 更新保活单元"的全部参数。
type watchUnitParams struct {
	IfName   string
	Mode     netcfg.Mode
	Backend  tunnel.Backend
	Interval time.Duration
	MTU      int
	Endpoint string
	DNS      []string
}

// renderWatchUnit 生成保活单元的内容。
//
// install 与 switch 共用它。这一点很要紧：换后端 / 换模式之后如果不同步改写
// 单元，保活下一轮就会把你刚切掉的东西重新建回来 —— 用户看到的是"切了没用"，
// 而且会怀疑 switch 根本没生效。让两条路径生成同一份文本，这类偏差就没有了。
//
// backend 为空（auto）时**不写** --backend：让保活在需要时仍能自动换后端。
func renderWatchUnit(mgr *systemd.Manager, p watchUnitParams) string {
	args := []string{
		"watch",
		"--interval", p.Interval.String(),
		"--ifname", p.IfName,
		"--mode", string(p.Mode),
	}
	if p.Backend.Valid() {
		args = append(args, "--backend", string(p.Backend))
	}
	if p.MTU > 0 {
		args = append(args, "--mtu", strconv.Itoa(p.MTU))
	}
	if p.Endpoint != "" {
		args = append(args, "--endpoint", p.Endpoint)
	}
	if len(p.DNS) > 0 {
		args = append(args, "--dns", strings.Join(p.DNS, ","))
	}
	return systemd.Render(systemd.UnitParams{
		BinPath:   mgr.BinPath(),
		ExecStart: args,
	})
}

// cmdInstall 把 warpgo 装成一个开机自启的保活服务。
//
// 它只做四件事：把二进制放到 /usr/bin、写单元、daemon-reload、enable。
// 隧道本身**不在这里建** —— 那是 watch 第一轮巡检的事。这样"装"和
// "跑"是两条独立的路：装错了不会影响正在运行的隧道，卸载也一样。
func (a *app) cmdInstall(args []string) int {
	var (
		ifName      string
		modeStr     string
		backendStr  string
		intervalStr string
		mtu         int
		endpoint    string
		dnsStr      string
		noStart     bool
	)
	opts := newOptionSet().
		Str("--ifname", &ifName).
		Str("--mode", &modeStr).
		Str("--backend", &backendStr).
		Str("--interval", &intervalStr).
		Int("--mtu", &mtu).
		Str("--endpoint", &endpoint).
		Str("--dns", &dnsStr).
		Bool("--no-start", &noStart)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	// 装的是"以后会自动改系统网络"的东西，非 Debian 直接拒绝。
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyInstallNotDebian, system.PrettyName()))
		return exitNotDebian
	}

	interval := keepalive.DefaultInterval
	if intervalStr != "" {
		d, err := time.ParseDuration(intervalStr)
		if err != nil {
			return a.fail(fmt.Errorf("--interval 不是合法时长: %w", err))
		}
		if d < keepalive.MinInterval {
			return a.fail(fmt.Errorf("--interval 太小（%s），最小 %s", d, keepalive.MinInterval))
		}
		interval = d
	}

	mode := netcfg.ModeNonGlobal
	if modeStr != "" {
		m, err := netcfg.ParseMode(modeStr)
		if err != nil {
			return a.fail(err)
		}
		mode = m
	}
	backend, err := tunnel.ParseBackend(backendStr)
	if err != nil {
		return a.fail(err)
	}
	if ifName == "" {
		ifName = netcfg.DefaultIfName
	}

	mgr := systemd.New()

	// 1. 二进制
	exe, err := os.Executable()
	if err != nil {
		return a.fail(fmt.Errorf("定位自身可执行文件: %w", err))
	}
	installed, err := installBinary(exe, mgr.BinPath())
	if err != nil {
		return a.fail(err)
	}
	if installed {
		a.log.Infof("%s", msg.Tf(msg.KeyInstallBinary, exe, mgr.BinPath()))
	} else {
		a.log.Infof("%s", msg.Tf(msg.KeyInstallBinarySame, mgr.BinPath()))
	}

	// 2. 单元
	content := renderWatchUnit(mgr, watchUnitParams{
		IfName:   ifName,
		Mode:     mode,
		Backend:  backend,
		Interval: interval,
		MTU:      mtu,
		Endpoint: endpoint,
		DNS:      splitList(dnsStr),
	})
	if err := mgr.WriteUnit(content); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyInstallUnit, mgr.UnitPath()))

	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()

	if err := mgr.DaemonReload(ctx); err != nil {
		return a.fail(err)
	}
	if err := mgr.Enable(ctx); err != nil {
		return a.fail(err)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyInstallEnabled, systemd.UnitName))

	if noStart {
		a.log.Infof("%s", msg.Tf(msg.KeyInstallNoStart, systemd.UnitName))
	} else {
		// 用 restart 而不是 start：重装时参数可能变了，restart 才能让新参数生效。
		if err := mgr.Restart(ctx); err != nil {
			return a.fail(err)
		}
		a.log.Infof("%s", msg.Tf(msg.KeyInstallStarted, systemd.UnitName))
	}

	a.log.Infof("%s", msg.Tf(msg.KeyInstallDone, systemd.UnitName, interval))
	return exitOK
}

// installBinary 把当前可执行文件复制到 dst。
//
// installed 为 false 表示"源与目标本来就是同一个文件"（重复 install 的常见情形），
// 这时什么都不做 —— 直接覆盖一个正在运行的自身会让人怀疑是不是把自己写坏了。
func installBinary(src, dst string) (installed bool, err error) {
	if si, serr := os.Stat(src); serr == nil {
		if di, derr := os.Stat(dst); derr == nil && os.SameFile(si, di) {
			return false, nil
		}
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("创建 %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".warpgo.tmp*")
	if err != nil {
		return false, fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		return false, fmt.Errorf("打开 %s: %w", src, err)
	}
	defer in.Close()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return false, fmt.Errorf("复制到 %s: %w", tmpName, err)
	}
	// 先落到临时文件、设好权限、刷盘，最后 rename —— 这样即使中途被打断，
	// /usr/bin/warpgo 要么是旧的完整文件、要么是新的完整文件，不会是半截。
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return false, fmt.Errorf("设置执行权限: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("刷盘: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return false, fmt.Errorf("安装到 %s: %w", dst, err)
	}
	return true, nil
}

// purgeStateDir 删除状态目录里的内容。
//
// **只删我们认识的文件**：目录里一旦出现别的东西就整个拒绝，并把清单打出来。
// 理由是 /etc/warpgo 的位置可以被环境变量指向任何地方（测试就是这么做的），
// 无脑 RemoveAll 有可能把用户的目录清空 —— 那是不可逆的。
func (a *app) purgeStateDir(dir string) (removed []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", dir, err)
	}

	var unknown []string
	for _, e := range entries {
		if !knownStateFiles[e.Name()] {
			unknown = append(unknown, e.Name())
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%s",
			msg.Tf(msg.KeyPurgeRefused, dir, strings.Join(unknown, ", ")))
	}

	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if err := os.Remove(full); err != nil {
			return removed, fmt.Errorf("删除 %s: %w", full, err)
		}
		removed = append(removed, e.Name())
	}
	// 目录本身可能不为空（上面拒绝过），这里失败也不该算错。
	_ = os.Remove(dir)
	return removed, nil
}
