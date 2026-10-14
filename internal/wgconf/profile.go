package wgconf

import (
	"errors"
	"fmt"
	"strings"

	"github.com/misakacoo/warpgo/internal/cloudflare"
)

// 生成 wg-quick 配置时的默认值。
const (
	// DefaultMTU：WARP 的 WireGuard 通道在 IPv4/6 上的常用值。
	// 之所以不用 1420（普通 wg-quick 默认）：WARP 出口对分片比较敏感，
	// 上游也用 1280，先取保守值；M2 会加 best_mtu 探测再上调。
	DefaultMTU = 1280
	// DefaultKeepalive：WARP 的 NAT 映射存活时间很短，25 秒是上游与生态的共识值。
	DefaultKeepalive = 25

	// mtuMin / mtuMax 是允许的 MTU 范围。超出范围直接拒绝，
	// 而不是生成一个一看就不对、却要等隧道起不来才发现的配置。
	mtuMin = 576
	mtuMax = 9000
)

// 按地址族区分的默认 DNS。
var (
	// DefaultDNS4 是 IPv4 默认 DNS。
	DefaultDNS4 = []string{"1.1.1.1"}
	// DefaultDNS6 是 IPv6 默认 DNS。
	DefaultDNS6 = []string{"2606:4700:4700::1111"}
)

// Family 是要写进配置的地址族。
type Family string

// 地址族取值。
const (
	FamilyV4   Family = "4"
	FamilyV6   Family = "6"
	FamilyDual Family = "d"
)

// ParseFamily 解析地址族参数，接受 4/6/d 与 v4/v6/dual 等写法。
func ParseFamily(s string) (Family, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "4", "v4", "ipv4":
		return FamilyV4, nil
	case "6", "v6", "ipv6":
		return FamilyV6, nil
	case "", "d", "dual", "both", "all":
		return FamilyDual, nil
	default:
		return "", fmt.Errorf("不认识的地址族 %q（可用 4 / 6 / d）", s)
	}
}

// Label 返回给人看的名字。
func (f Family) Label() string {
	switch f {
	case FamilyV4:
		return "IPv4"
	case FamilyV6:
		return "IPv6"
	default:
		return "IPv4+IPv6"
	}
}

// Profile 是一份 wg-quick 配置的全部字段。
type Profile struct {
	// Note 是写在文件顶部的注释（由上层用 i18n 填；wgconf 不做翻译）。
	Note string

	PrivateKey Key
	Addresses  []string
	DNS        []string
	MTU        int

	// TableOff 输出 `Table = off`，让 wg-quick 只建接口、不碰路由表。
	// 这是给「自己管策略路由」的场景用的（例如 M2 的非全局模式），
	// 默认 false —— 也就是标准的 wg-quick 行为：接管默认路由。
	TableOff bool

	// Reserved 是 WARP 的私有扩展。它只对**用户态**实现有效（内核 WireGuard
	// 设不了握手报文里那 3 个字节），所以这里以注释形式输出 —— 既符合生态约定
	// （客户端读 `# Reserved = a, b, c`），又不会让 wg-quick 解析失败。
	Reserved cloudflare.Reserved

	PeerPublicKey Key
	AllowedIPs    []string
	Endpoint      string
	Keepalive     int

	// Skipped 记录「请求了但账户没有」而被动跳过的地址族。
	// 上层据此提示用户，避免出现"以为配了双栈、实际只有单栈"的静默偏差。
	Skipped []Family
}

// Options 是 Build 的可调项；零值全部走默认。
type Options struct {
	Family Family
	MTU    int
	DNS    []string
	// Keepalive：0 → 用 DefaultKeepalive；负数 → 不写这一行（关闭保活）。
	Keepalive int
	Endpoint  string
	Note      string
	// TableOff 见 Profile.TableOff。
	TableOff bool
	// NoDNS 表示不写 DNS 行。
	//
	// 存在的理由很实际：wg-quick 见到 DNS 行就会去改写 /etc/resolv.conf，
	// 而在远程服务器上那是一次"有可能把自己弄断网"的操作。真机验收脚本用
	// 它拿到一份只建接口、不碰系统配置的配置。
	NoDNS bool
}

// Build 由账户生成一份 wg-quick 配置。
func Build(acct *cloudflare.Account, opt Options) (*Profile, error) {
	if acct == nil {
		return nil, errors.New("账户为空")
	}

	priv, err := ParseKey(acct.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("账户里的私钥不可用: %w", err)
	}
	peer, err := ParseKey(acct.PeerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("账户里的对端公钥不可用: %w", err)
	}

	fam := opt.Family
	if fam == "" {
		fam = FamilyDual
	}

	p := &Profile{
		Note:          opt.Note,
		PrivateKey:    priv,
		Reserved:      acct.Reserved,
		PeerPublicKey: peer,
		MTU:           opt.MTU,
		Keepalive:     opt.Keepalive,
		TableOff:      opt.TableOff,
	}
	if p.MTU == 0 {
		p.MTU = DefaultMTU
	}
	if p.MTU < mtuMin || p.MTU > mtuMax {
		return nil, fmt.Errorf("MTU %d 超出允许范围 %d-%d", p.MTU, mtuMin, mtuMax)
	}
	if p.Keepalive == 0 {
		p.Keepalive = DefaultKeepalive
	}

	// 接口地址与 AllowedIPs 必须**同时**按「请求的族 ∩ 账户实际有的族」取。
	//
	// 这是一个容易出错的地方：如果账户只有 IPv4，却在 AllowedIPs 里写了 ::/0，
	// 隧道会接住所有 IPv6 流量、但接口上没有 IPv6 地址 —— 结果是 v6 静默黑洞，
	// 表现成"能 ping 通 v4、v6 全超时"，排查起来极费时间。宁可少配，不能配错。
	var requested []Family
	switch fam {
	case FamilyV4:
		requested = []Family{FamilyV4}
	case FamilyV6:
		requested = []Family{FamilyV6}
	case FamilyDual:
		requested = []Family{FamilyV4, FamilyV6}
	default:
		return nil, fmt.Errorf("不认识的地址族 %q", fam)
	}

	for _, f := range requested {
		switch f {
		case FamilyV4:
			if !acct.HasV4() {
				p.Skipped = append(p.Skipped, FamilyV4)
				continue
			}
			p.Addresses = append(p.Addresses, acct.AddrV4)
			p.AllowedIPs = append(p.AllowedIPs, "0.0.0.0/0")
			p.DNS = append(p.DNS, DefaultDNS4...)
		case FamilyV6:
			if !acct.HasV6() {
				p.Skipped = append(p.Skipped, FamilyV6)
				continue
			}
			p.Addresses = append(p.Addresses, acct.AddrV6)
			p.AllowedIPs = append(p.AllowedIPs, "::/0")
			p.DNS = append(p.DNS, DefaultDNS6...)
		}
	}
	if len(p.Addresses) == 0 {
		return nil, fmt.Errorf("账户里没有任何接口地址，无法生成 %s 配置", fam.Label())
	}
	// 用户显式指定单栈、而账户恰好没有这一族时，必须报错而不是悄悄给一份空的配置。
	if len(p.Addresses) < len(requested) && fam != FamilyDual {
		return nil, fmt.Errorf("账户没有 %s 地址，无法生成 %s 配置", fam.Label(), fam.Label())
	}

	// 显式给的 DNS 覆盖默认值（保留用户意图，连"不写 DNS"都能表达）。
	switch {
	case opt.NoDNS:
		p.DNS = nil
	case len(opt.DNS) > 0:
		p.DNS = append([]string(nil), opt.DNS...)
	}

	p.Endpoint = strings.TrimSpace(opt.Endpoint)
	if p.Endpoint == "" {
		p.Endpoint = acct.Endpoint
	}
	if p.Endpoint == "" {
		return nil, errors.New("账户里没有对端端点")
	}

	return p, nil
}

// Render 输出 wg-quick 配置文本。
//
// 输出是**确定性**的（同样的 Profile 必得同样的字节），这样才能用快照测试
// 卡住格式回归 —— 配置文件差一个空格就可能让某个客户端解析失败。
func (p *Profile) Render() string {
	var b strings.Builder

	if p.Note != "" {
		for _, line := range strings.Split(strings.TrimRight(p.Note, "\n"), "\n") {
			fmt.Fprintf(&b, "# %s\n", line)
		}
	}

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", p.PrivateKey)
	for _, a := range p.Addresses {
		fmt.Fprintf(&b, "Address = %s\n", a)
	}
	if len(p.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(p.DNS, ", "))
	}
	fmt.Fprintf(&b, "MTU = %d\n", p.MTU)
	if p.TableOff {
		b.WriteString("Table = off\n")
	}
	// reserved 始终输出（哪怕是 0,0,0）：生态里客户端就是靠这一行传递它的，
	// 缺了这行反而会让人怀疑"是不是没读到"。
	fmt.Fprintf(&b, "# Reserved = %s\n", p.Reserved)

	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", p.PeerPublicKey)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(p.AllowedIPs, ", "))
	fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
	if p.Keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
	}

	return b.String()
}

// String 实现 fmt.Stringer，等于 Render。
func (p *Profile) String() string { return p.Render() }
