package main

import (
	"errors"
	"fmt"

	"github.com/misakacoo/warpgo/internal/dns"
	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/state"
)

// dnsManager 返回本机的 resolv.conf 管理器。
//
// 备份放在 warpgod 的状态目录里（/etc/warpgo），与账户、运行态同处一地 ——
// 这样"还原"这件事永远找得到它的依据，不会出现旧版那种
// "备份已被删、原值无从得知"的局面。
func (a *app) dnsManager() *dns.Manager {
	return dns.New(state.Dir())
}

// applyDNS 把 nameserver 换成 servers（首次调用会备份原文）。
func (a *app) applyDNS(servers []string) error {
	m := a.dnsManager()
	created, err := m.Backup()
	if err != nil {
		return a.dnsErr(err, m.ResolvPath)
	}
	if created {
		a.log.Infof("%s", a.bundle.Tf(i18n.KeyDNSBackup, m.BackupPath))
	}
	if err := m.Set(servers); err != nil {
		return a.dnsErr(err, m.ResolvPath)
	}
	return nil
}

// restoreDNS 用备份还原 resolv.conf。
//
// restored 报告是否真的做了还原（没有备份时返回 false 而不是错误 ——
// 大部分机器我们压根没碰过 DNS，那不是异常）。
func (a *app) restoreDNS() (restored bool, err error) {
	m := a.dnsManager()
	if !m.HasBackup() {
		return false, nil
	}
	if err := m.Restore(); err != nil {
		return false, a.dnsErr(err, m.ResolvPath)
	}
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyDNSRestored, m.ResolvPath))
	return true, nil
}

// dnsErr 把 dns 包的哨兵错误翻成能指导行动的话。
func (a *app) dnsErr(err error, path string) error {
	if errors.Is(err, dns.ErrSymlink) {
		return fmt.Errorf("%s", a.bundle.Tf(i18n.KeyDNSSymlink, path))
	}
	return err
}
