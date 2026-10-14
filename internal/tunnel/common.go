package tunnel

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// 这一层是两种后端**真正共享**的部分。
//
// wgctrl 对内核设备与用户态设备一视同仁：前者走 genetlink，
// 后者走 `/var/run/wireguard/<if>.sock` 上的 UAPI 文本协议。于是
// "下发配置 / 读回状态"只需要一份实现，两个后端的差别收缩到
// 「接口是怎么来的」与「怎么拆」两件事上 —— 这正是设计文档 §5 要的分层。
//
// 这些函数都显式接收 *wgctrl.Client，而不是挂在某个结构体上：
// 后端结构体只该持有自己特有的东西（守护进程句柄之类），
// 把共享逻辑塞进去会让"哪些是共有的"变得看不出来。

// applyAddrsAndUp 给已存在的接口补地址并置为 up。
//
// 返回新加的地址条数与"这次是否真的把接口提起来了"。
func applyAddrsAndUp(link netlink.Link, cfg Config) (added int, up bool, err error) {
	for _, a := range cfg.Addresses {
		ok, aerr := netcfg.EnsureAddr(link, a)
		if aerr != nil {
			return added, false, aerr
		}
		if ok {
			added++
		}
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		if uerr := netcfg.SetLinkUp(link); uerr != nil {
			return added, false, uerr
		}
		up = true
	}
	return added, up, nil
}

// ensureConfig 下发 WireGuard 参数，返回 (配置是否已一致, 本次是否真的下发过)。
//
// 先比对再下发是刻意的：`up` 连跑多次时连一次无谓的重握手都不该发生，
// 否则"出口 IP 会不会变"就又取决于运气了。
func ensureConfig(client *wgctrl.Client, cfg Config) (matched bool, sent bool, err error) {
	need, err := needsConfigure(client, cfg)
	if err != nil {
		return false, false, err
	}
	if !need {
		return true, false, nil
	}
	if err := configure(client, cfg); err != nil {
		return false, false, err
	}
	return false, true, nil
}

// needsConfigure 判断当前设备里的 WireGuard 配置是否已经等于期望值。
func needsConfigure(client *wgctrl.Client, cfg Config) (bool, error) {
	d, err := client.Device(cfg.IfName)
	if err != nil {
		return false, fmt.Errorf("读取 %s 的 WireGuard 配置: %w", cfg.IfName, err)
	}

	if d.PublicKey != wgtypes.Key(cfg.PublicKey) {
		return true, nil
	}
	if cfg.FWMark != 0 && d.FirewallMark != cfg.FWMark {
		return true, nil
	}
	if len(d.Peers) != 1 {
		return true, nil
	}
	p := d.Peers[0]
	if p.PublicKey != wgtypes.Key(cfg.PeerPublicKey) {
		return true, nil
	}
	if cfg.Keepalive > 0 && p.PersistentKeepaliveInterval != cfg.Keepalive {
		return true, nil
	}
	endpoint, err := resolveEndpoint(cfg.Endpoint)
	if err != nil {
		return false, err
	}
	if !sameEndpoint(p.Endpoint, endpoint) {
		return true, nil
	}
	if !sameAllowedIPs(p.AllowedIPs, cfg.AllowedIPs) {
		return true, nil
	}
	return false, nil
}

func configure(client *wgctrl.Client, cfg Config) error {
	priv := wgtypes.Key(cfg.PrivateKey)
	peer := wgtypes.Key(cfg.PeerPublicKey)

	endpoint, err := resolveEndpoint(cfg.Endpoint)
	if err != nil {
		return err
	}

	allowed := make([]net.IPNet, 0, len(cfg.AllowedIPs))
	for _, p := range cfg.AllowedIPs {
		ipnet := prefixToIPNet(p)
		if ipnet != nil {
			allowed = append(allowed, *ipnet)
		}
	}
	if len(allowed) == 0 {
		return fmt.Errorf("没有可用的 AllowedIPs")
	}

	peerCfg := wgtypes.PeerConfig{
		PublicKey:         peer,
		Endpoint:          endpoint,
		ReplaceAllowedIPs: true,
		AllowedIPs:        allowed,
	}
	if cfg.Keepalive > 0 {
		ka := cfg.Keepalive
		peerCfg.PersistentKeepaliveInterval = &ka
	}

	wcfg := wgtypes.Config{
		PrivateKey:   &priv,
		ReplacePeers: true, // 保证不留旧 peer（换账户后不会残留）
		Peers:        []wgtypes.PeerConfig{peerCfg},
	}
	if cfg.FWMark != 0 {
		mark := cfg.FWMark
		wcfg.FirewallMark = &mark
	}

	if err := client.ConfigureDevice(cfg.IfName, wcfg); err != nil {
		return fmt.Errorf("配置 WireGuard 设备 %s: %w", cfg.IfName, err)
	}
	return nil
}

// readStatus 读回接口与 WireGuard 状态。
//
// 接口不存在是**正常状态**（还没 up 或已经 down），返回 Exists=false 而不是错误；
// 但"接口在、却读不出 WireGuard 状态"必须如实报错 —— 那说明后端出了问题，
// 假装"没有设备"会把故障掩盖成"没安装"。
func readStatus(client *wgctrl.Client, ifName string) (Status, error) {
	st := Status{IfName: ifName}
	if !netcfg.LinkExists(ifName) {
		st.Exists = false
		return st, nil
	}
	st.Exists = true

	d, err := client.Device(ifName)
	if err != nil {
		return st, fmt.Errorf("接口 %s 存在但读取 WireGuard 状态失败: %w", ifName, err)
	}
	st.PublicKey = d.PublicKey.String()
	st.ListenPort = d.ListenPort
	st.FWMark = d.FirewallMark
	for _, p := range d.Peers {
		ps := PeerStatus{
			PublicKey:       p.PublicKey.String(),
			Keepalive:       p.PersistentKeepaliveInterval,
			LatestHandshake: p.LastHandshakeTime,
			RxBytes:         p.ReceiveBytes,
			TxBytes:         p.TransmitBytes,
		}
		if p.Endpoint != nil {
			ps.Endpoint = p.Endpoint.String()
		}
		for _, a := range p.AllowedIPs {
			ps.AllowedIPs = append(ps.AllowedIPs, a.String())
		}
		st.Peers = append(st.Peers, ps)
	}
	return st, nil
}

// resolveEndpoint 把 "host:port" 解析成 UDP 地址。
//
// 关键性质：**传字面量时不会查 DNS**（net.ResolveUDPAddr 对 IP 字面量直接短路），
// 所以正常情况下这条路径完全不依赖 DNS。只有用户显式传了域名才会去解析。
func resolveEndpoint(s string) (*net.UDPAddr, error) {
	if s == "" {
		return nil, fmt.Errorf("端点为空")
	}
	addr, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		return nil, fmt.Errorf("解析端点 %q: %w", s, err)
	}
	if addr.IP == nil {
		return nil, fmt.Errorf("端点 %q 没有解析出 IP 地址", s)
	}
	return addr, nil
}

func sameEndpoint(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Port == b.Port && a.IP.Equal(b.IP)
}

func sameAllowedIPs(got []net.IPNet, want []netip.Prefix) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, a := range got {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			seen[p.Masked().String()] = true
		}
	}
	for _, p := range want {
		if !seen[p.Masked().String()] {
			return false
		}
	}
	return true
}

func prefixToIPNet(p netip.Prefix) *net.IPNet {
	if !p.IsValid() {
		return nil
	}
	return &net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}
