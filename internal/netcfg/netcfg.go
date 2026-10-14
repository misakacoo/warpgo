// Package netcfg 负责所有"与传输无关"的网络配置：接口、地址、MTU、路由、策略路由。
//
// 它不知道隧道是内核 WireGuard 还是用户态实现建的 —— 那是 internal/tunnel 的事。
//
// # 本包的两条硬性约束（都是在现有 shell 项目里踩出来的，直接在实现层消除）
//
//  1. **所有规则/路由操作"先清后加"**。上游 `ip rule add` 不幂等（用 `|| true` 吞掉
//     EEXIST），残留累积到过 4681 条。本包每次 Apply 都先按 pref 清掉自己的规则、
//     清空自己的路由表，再重新添加 —— 于是无论跑多少次，结果都是"正好一组"。
//  2. **删除一律按 pref / 句柄，不解析文本**。上游按 `ip rule show` 的输出做字符串匹配，
//     而接口被删除后规则会显示成 `from all oif warp [detached] lookup 51820`，
//     原样 grep 永远匹配不上，一份都清不掉（实测残留 3 份）。
//     这里从头到尾只用数字标识符，不碰任何显示文本。
package netcfg

import (
	"fmt"
	"net/netip"
	"strconv"
)

// warpgo 使用的路由表号与规则优先级。
const (
	// TableWarp 是 WARP 专用路由表。51820 是 WireGuard/wg-quick 的生态约定值，
	// 选它可以让排查时一眼认出"这是 WARP 那条"。
	TableWarp = 51820

	// TableMain 是内核主表（RT_TABLE_MAIN）。
	TableMain = 254

	// PrefSuppressMain 的语义：查 main 表时忽略"前缀长度 ≤ 0"的路由（也就是默认路由）。
	//
	// 它解决的是一个很具体的问题：如果只写「源地址是 WARP 地址 → 查 51820 表」，
	// 那么以 WARP 地址为源的流量去访问**内网**时，51820 表里只有一条默认路由，
	// 包会被塞进 WARP 隧道 —— 内网直连就断了。
	// 有了这条优先规则，流量先查 main 表里那些**具体**路由（内网、直连网段），
	// 只有 main 表给不出结果时才轮到 WARP 的默认路由。
	PrefSuppressMain = 9000

	// PrefGlobalTunnel 是全局模式下把「其余流量」交给 WARP 的规则。
	//
	// 必须排在 PrefSuppressMain 之后：先让 main 表里的具体路由（内网、直连）
	// 有机会命中，命不中的才落到 WARP。
	PrefGlobalTunnel = 9020

	// FWMarkWarp 是内核给「WireGuard 自己发出的封装包」打的标记。
	//
	// 它是全局模式的**防环路关键**：如果隧道自己的封装包也走「默认 → WARP」，
	// 就会自己套自己、永远出不去（表现为握手永远不成功、整机断网）。
	// 规则写成 `not fwmark 0xca6c lookup 51820`，把封装包排除在外，
	// 它就会回落到系统默认规则、走原生路径出去。
	//
	// 取值 0xca6c = 51820（我们的表号），沿用的是 wg-quick 的约定，
	// 好处是排查时两个数字能对上。
	FWMarkWarp = 0xca6c

	// PrefFromV4 / PrefFromV6 把「以 WARP 接口地址为源」的流量交给 51820 表。
	//
	// 用源地址匹配而不是 `oif`：语义更明确（"以 WARP 身份发出的流量才走 WARP"），
	// 而且 v4 / v6 完全对称、行为可预测。
	PrefFromV4 = 9010
	PrefFromV6 = 9011
)

// Family 是地址族（4 或 6）。用 int 而不是 netlink 常量，避免调用方依赖具体库。
const (
	FamilyV4 = 4
	FamilyV6 = 6
)

const (
	// DefaultIfName 是默认接口名。沿用上游的名字，方便对照与排障。
	DefaultIfName = "warp"
	// DefaultMTU 是接口 MTU。WARP 出口对分片敏感，取保守值（与 wgconf 的默认一致，
	// 但两者服务的目标不同：这里管接口，那里管生成的配置文件，所以各自定义）。
	DefaultMTU = 1280
)

// Rule 是一条策略路由规则（纯数据，便于单测与日志）。
//
// 刻意不叫 netlink.Rule：它是本包的领域模型，字段只有我们真正用到的那些，
// 与任何具体库解耦。
type Rule struct {
	Family int
	Pref   int
	Table  int
	// From 为零值时表示 "from all"。
	From netip.Addr
	// SuppressPrefixLen：-1 表示不设置该属性；0 表示"忽略默认路由"。
	SuppressPrefixLen int

	// FWMark 非零时按防火墙标记匹配；Invert 为真表示 `not fwmark`
	// （全局模式靠它把隧道自己的封装包排除出去，见 FWMarkWarp）。
	FWMark int
	Invert bool
}

// IsZeroFrom 报告这条规则是否匹配所有源地址。
func (r Rule) IsZeroFrom() bool { return !r.From.IsValid() }

// String 输出成与 `ip rule` 相近的可读形式（只用于日志）。
func (r Rule) String() string {
	s := "pref " + strconv.Itoa(r.Pref) + ": "
	switch {
	case r.FWMark != 0:
		if r.Invert {
			s += "not "
		}
		s += fmt.Sprintf("fwmark 0x%x ", r.FWMark)
	case r.From.IsValid():
		s += "from " + r.From.String() + " "
	default:
		s += "from all "
	}
	if r.Table > 0 {
		s += "lookup " + strconv.Itoa(r.Table)
	}
	if r.SuppressPrefixLen >= 0 {
		s += " suppress_prefixlength " + strconv.Itoa(r.SuppressPrefixLen)
	}
	return s
}

// String 输出成与 `ip route` 相近的可读形式（只用于日志）。
func (r Route) String() string {
	dst := "default"
	if r.Dst.IsValid() {
		dst = r.Dst.Masked().String()
	}
	return "table " + strconv.Itoa(r.Table) + ": " + dst + " dev " + r.Dev
}

// Equal 比较两条规则的语义是否相同。
func (r Rule) Equal(o Rule) bool {
	return r.Family == o.Family &&
		r.Pref == o.Pref &&
		r.Table == o.Table &&
		r.From == o.From &&
		r.SuppressPrefixLen == o.SuppressPrefixLen &&
		r.FWMark == o.FWMark &&
		r.Invert == o.Invert
}

// Route 是一条路由表项（纯数据）。
type Route struct {
	Family int
	Table  int
	// Dst 为零值时表示默认路由。
	Dst netip.Prefix
	Dev string
}

// DefaultPrefix 返回某族的默认路由前缀。
func DefaultPrefix(family int) netip.Prefix {
	if family == FamilyV6 {
		return netip.PrefixFrom(netip.IPv6Unspecified(), 0)
	}
	return netip.PrefixFrom(netip.IPv4Unspecified(), 0)
}
