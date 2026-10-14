package netcfg

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Mode 是出口接管方式。
type Mode string

const (
	// ModeNonGlobal 只让「以 WARP 接口地址为源」的流量走 WARP。
	// 它是默认值，因为天然不会切断 SSH —— 服务器上这是最要紧的性质。
	ModeNonGlobal Mode = "non-global"
	// ModeGlobal 让所有流量走 WARP（M3 实现，需要 fwmark 防环路 + watchdog 回滚）。
	ModeGlobal Mode = "global"
)

// ParseMode 解析模式名。
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "non-global", "nonglobal", "non_global", "ng", "local":
		return ModeNonGlobal, nil
	case "global", "all":
		return ModeGlobal, nil
	default:
		return "", fmt.Errorf("不认识的模式 %q（可用 non-global / global）", s)
	}
}

// Plan 是一次 up 之后系统**应该**处于的状态。
//
// 之所以把它做成纯数据：网络配置真正难的不是"发命令"，而是"这次该发哪些命令、
// 与上次是否一致、下次怎么清理干净"。把这部分抽出来就能脱离 root、脱离真实内核
// 做单测 —— 而真正碰内核的那几行反而简单到不需要测。
type Plan struct {
	IfName   string
	Table    int
	MTU      int
	Mode     Mode
	Addrs    []netip.Prefix
	Families []int
	Routes   []Route
	Rules    []Rule
	// FWMark 非零时要求隧道自己打上该标记（全局模式的防环路依赖它）。
	FWMark int
}

// Options 是 Build 的可调项；零值走默认。
type Options struct {
	IfName string
	MTU    int
	Table  int
	Mode   Mode
}

// Build 由接口地址生成期望状态。
//
// 两种模式的差别**全在规则上**：接口配置、地址、表里的默认路由都一样，
// 变的是"谁有资格用这张表"。这也是分层带来的好处 —— 换模式不用重建接口
// （重建会换出口 IP）。
func Build(addrs []netip.Prefix, opt Options) (*Plan, error) {
	if opt.IfName == "" {
		opt.IfName = DefaultIfName
	}
	if opt.Table == 0 {
		opt.Table = TableWarp
	}
	if opt.MTU == 0 {
		opt.MTU = DefaultMTU
	}
	mode := opt.Mode
	if mode == "" {
		mode = ModeNonGlobal
	}
	if mode != ModeNonGlobal && mode != ModeGlobal {
		return nil, fmt.Errorf("不认识的模式 %q", mode)
	}

	p := &Plan{IfName: opt.IfName, Table: opt.Table, MTU: opt.MTU, Mode: mode}
	if mode == ModeGlobal {
		// 全局模式必须让隧道自己打 fwmark，否则封装包会被自己的规则再次送进隧道。
		p.FWMark = FWMarkWarp
	}

	// 每个地址族只处理一次：同一族给了多个地址时，规则只需要一条
	// （规则匹配的是"源地址等于某个 WARP 地址"，而接口通常每族只有一个地址）。
	seen := map[int]bool{}
	for _, a := range addrs {
		if !a.IsValid() {
			continue
		}
		addr := a.Addr()
		fam := familyOfAddr(addr)
		if seen[fam] {
			continue
		}
		seen[fam] = true

		p.Addrs = append(p.Addrs, a)
		p.Families = append(p.Families, fam)
		p.Routes = append(p.Routes, Route{
			Family: fam, Table: opt.Table, Dst: DefaultPrefix(fam), Dev: opt.IfName,
		})

		// 先让 main 表里的**具体**路由（内网、直连网段）优先于 WARP 的默认路由，
		// 否则以 WARP 地址为源的流量会被一律塞进隧道，内网就断了。
		p.Rules = append(p.Rules, Rule{
			Family: fam, Pref: PrefSuppressMain, Table: TableMain, SuppressPrefixLen: 0,
		})

		if mode == ModeGlobal {
			// 其余流量全部交给 WARP —— 但把隧道自己的封装包排除掉（not fwmark）。
			// 少了这个 exclude，WireGuard 的封装包会被这条规则再次送进隧道，
			// 结果是握手永远不成功、整机断网。
			p.Rules = append(p.Rules, Rule{
				Family: fam, Pref: PrefGlobalTunnel, Table: opt.Table,
				FWMark: FWMarkWarp, Invert: true, SuppressPrefixLen: -1,
			})
			continue
		}
		pref := PrefFromV4
		if fam == FamilyV6 {
			pref = PrefFromV6
		}
		p.Rules = append(p.Rules, Rule{
			Family: fam, Pref: pref, Table: opt.Table, From: addr, SuppressPrefixLen: -1,
		})
	}

	if len(p.Addrs) == 0 {
		return nil, errors.New("账户里没有任何接口地址，无法生成网络配置")
	}
	return p, nil
}

// AddrStrings 返回接口地址的可读列表（日志用）。
func (p *Plan) AddrStrings() []string {
	out := make([]string, 0, len(p.Addrs))
	for _, a := range p.Addrs {
		out = append(out, a.String())
	}
	return out
}

// RuleStrings / RouteStrings 返回可读列表（日志用）。
func (p *Plan) RuleStrings() []string {
	out := make([]string, 0, len(p.Rules))
	for _, r := range p.Rules {
		out = append(out, r.String())
	}
	return out
}

// RouteStrings 见 RuleStrings。
func (p *Plan) RouteStrings() []string {
	out := make([]string, 0, len(p.Routes))
	for _, r := range p.Routes {
		out = append(out, r.String())
	}
	return out
}
