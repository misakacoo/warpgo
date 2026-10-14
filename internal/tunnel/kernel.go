package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/vishvananda/netlink"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// kernelTunnel 是内核 WireGuard 后端。
//
// 接口的创建/地址/MTU 走 netlink（vishvananda），WireGuard 参数走 wgctrl。
// 为什么配置不用 UAPI 文本协议：**内核模块根本不提供那个 socket** ——
// `/var/run/wireguard/<if>.sock` 是 wireguard-go（用户态实现）才有的东西。
// 内核设备只能通过 genetlink 配置，wgctrl 正是为此而生（它由 WireGuard 作者维护，
// 内部会自动判断设备属于内核还是用户态）。
type kernelTunnel struct {
	ifName string
	client *wgctrl.Client
}

// NewKernel 创建一个内核后端。
func NewKernel(ifName string) (*kernelTunnel, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("初始化 WireGuard 客户端: %w", err)
	}
	return &kernelTunnel{ifName: ifName, client: c}, nil
}

func (k *kernelTunnel) Backend() Backend { return BackendKernel }

func (k *kernelTunnel) Close() error { return k.client.Close() }

// Ensure 让隧道达到期望状态，且尽力少动东西。
//
// 顺序：建接口 → 加地址 → 起接口 → 下发 WireGuard 配置。
// 每一步都先判断"是否已经是要的样子"，只在需要时才动手。
func (k *kernelTunnel) Ensure(ctx context.Context, cfg Config) (Change, error) {
	var ch Change
	name := cfg.IfName
	if name == "" {
		name = k.ifName
	}

	link, created, err := netcfg.EnsureWireGuardLink(name, cfg.MTU)
	if err != nil {
		return ch, err
	}
	ch.LinkCreated = created

	for _, a := range cfg.Addresses {
		added, err := netcfg.EnsureAddr(link, a)
		if err != nil {
			return ch, err
		}
		if added {
			ch.AddrsAdded++
		}
	}

	if link.Attrs().Flags&net.FlagUp == 0 {
		if err := netcfg.SetLinkUp(link); err != nil {
			return ch, err
		}
		ch.LinkUp = true
	}

	need, err := k.needsConfigure(cfg)
	if err != nil {
		return ch, err
	}
	ch.ConfigMatched = !need
	if need {
		if err := k.configure(cfg); err != nil {
			return ch, err
		}
		ch.Configured = true
	}
	return ch, nil
}

// needsConfigure 判断当前内核里的 WireGuard 配置是否已经等于期望值。
//
// 有了这一步，`up` 连跑多次也只会"看一眼、什么都不做" —— 既不会重建接口，
// 也不会重下发参数触发无谓的重握手。这正是上游最缺的性质。
func (k *kernelTunnel) needsConfigure(cfg Config) (bool, error) {
	d, err := k.client.Device(cfg.IfName)
	if err != nil {
		return false, fmt.Errorf("读取 %s 的 WireGuard 配置: %w", cfg.IfName, err)
	}

	wantPub := wgtypes.Key(cfg.PublicKey)
	if d.PublicKey != wantPub {
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

func (k *kernelTunnel) configure(cfg Config) error {
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

	if err := k.client.ConfigureDevice(cfg.IfName, wcfg); err != nil {
		return fmt.Errorf("配置 WireGuard 设备 %s: %w", cfg.IfName, err)
	}
	return nil
}

func (k *kernelTunnel) TearDown(ctx context.Context) (bool, error) {
	return netcfg.DeleteLink(k.ifName)
}

// SetReserved 在内核后端必然失败 —— 见 ErrReservedUnsupported 的说明。
// reserved 为全零时视为"不需要"，成功返回（这是绝大多数地区的正常情况）。
func (k *kernelTunnel) SetReserved(ctx context.Context, r cloudflare.Reserved) error {
	if r.IsZero() {
		return nil
	}
	return ErrReservedUnsupported
}

func (k *kernelTunnel) Status(ctx context.Context) (Status, error) {
	st := Status{IfName: k.ifName}
	link, err := netlink.LinkByName(k.ifName)
	if err != nil {
		if netcfg.IsNotExist(err) {
			st.Exists = false
			return st, nil
		}
		return st, fmt.Errorf("查找接口 %s: %w", k.ifName, err)
	}
	st.Exists = true

	d, err := k.client.Device(k.ifName)
	if err != nil {
		// 接口在、但读不出 WireGuard 状态：如实报错，不假装"没有"。
		return st, fmt.Errorf("接口 %s 存在但读取 WireGuard 状态失败: %w", k.ifName, err)
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
	_ = link
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
