package main

import (
	"fmt"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/wgconf"
)

// cmdProfile 输出一份可直接交给 wg-quick 的配置。
//
// 它是本项目的**逃生口**：只要账户还在本机，即使将来 CF 改了 API、warpgo 一时
// 跟不上，用户依然能用这份配置把隧道立起来（`wg-quick up ./warpgo.conf`）。
func (a *app) cmdProfile(args []string) int {
	var (
		familyStr string
		mtu       int
		dnsStr    string
		keepalive int
		endpoint  string
		out       string
		tableOff  bool
		noDNS     bool
	)
	opts := newOptionSet().
		Str("--family", &familyStr).
		Int("--mtu", &mtu).
		Str("--dns", &dnsStr).
		Int("--keepalive", &keepalive).
		Str("--endpoint", &endpoint).
		Str("--out", &out).
		Bool("--table-off", &tableOff).
		Bool("--no-dns", &noDNS)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}

	family, err := wgconf.ParseFamily(familyStr)
	if err != nil {
		return a.fail(err)
	}

	p, err := wgconf.Build(acct, wgconf.Options{
		Family:    family,
		MTU:       mtu,
		DNS:       splitList(dnsStr),
		Keepalive: keepalive,
		Endpoint:  endpoint,
		TableOff:  tableOff,
		NoDNS:     noDNS,
		Note:      msg.Tf(msg.KeyProfNote, acct.ID),
	})
	if err != nil {
		return a.fail(err)
	}

	text := p.Render()
	if out != "" {
		// 配置里有私钥，落到文件一律 0600。
		if err := state.WriteFileAtomic(out, []byte(text), state.FilePerm); err != nil {
			return a.fail(err)
		}
		a.log.Infof("%s", msg.Tf(msg.KeyProfSaved, out, "0600"))
	} else {
		fmt.Fprint(a.stdout, text)
	}

	// 提示走 stderr（经 log），所以 stdout 可以安全重定向进文件。
	if tableOff {
		a.log.Warnf("%s", msg.T(msg.KeyProfTableOffHint))
	}
	for _, f := range p.Skipped {
		a.log.Warnf("%s", msg.Tf(msg.KeyProfSkipped, f.Label()))
	}
	if acct.HasReserved() {
		a.log.Warnf("%s", msg.Tf(msg.KeyProfKernelWarn, acct.Reserved))
	}
	return exitOK
}
