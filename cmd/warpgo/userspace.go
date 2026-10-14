package main

import (
	"fmt"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// cmdUserspaceDaemon 是**内部命令**：用户态后端的守护进程本体。
//
// 它由 up / watch 派生（见 tunnel.spawnDaemon），有三个刻意的性质：
//
//   - **不取单实例锁**。锁的语义是"同一时间只有一次变更在改系统"，
//     而守护进程是长期存在的东西 —— 它拿了锁就等于把锁永久占住了，
//     之后用户连 `warpgo down` 都跑不动。
//   - **不写 usage / 不进 help**。它需要 root、需要一个 TUN 与一个 socket 目录，
//     手工执行没有任何意义；真要排障看的是它写的日志
//     （/run/warpgo/userspace-<接口>.log）。
//   - **退出码非零就是故障**。它是被 setsid 派生的，父进程早就不在了，
//     所以唯一能表达失败的地方就是退出码与那份日志。
func (a *app) cmdUserspaceDaemon(args []string) int {
	var (
		ifName   string
		mtu      int
		reserved string
		verbose  bool
	)
	opts := newOptionSet().
		Str("--ifname", &ifName).
		Int("--mtu", &mtu).
		Str("--reserved", &reserved).
		Bool("--verbose", &verbose)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}
	if ifName == "" {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyNeedArg, "--ifname")))
	}
	r, err := tunnel.ParseReservedArg(reserved)
	if err != nil {
		return a.fail(err)
	}

	if err := tunnel.RunDaemon(tunnel.DaemonOptions{
		IfName:   ifName,
		MTU:      mtu,
		Reserved: r,
		Verbose:  verbose,
	}); err != nil {
		return a.fail(err)
	}
	return exitOK
}
