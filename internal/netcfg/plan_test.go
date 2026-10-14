package netcfg

import (
	"net/netip"
	"testing"
)

func pfxs(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestBuildNonGlobalDualStack(t *testing.T) {
	plan, err := Build(pfxs("172.16.0.2/32", "2606:4700:110::2/128"), Options{})
	if err != nil {
		t.Fatal(err)
	}

	if plan.IfName != DefaultIfName {
		t.Errorf("默认接口名应为 %s，实得 %s", DefaultIfName, plan.IfName)
	}
	if plan.Table != TableWarp {
		t.Errorf("默认表号应为 %d，实得 %d", TableWarp, plan.Table)
	}
	if plan.MTU != DefaultMTU {
		t.Errorf("默认 MTU 应为 %d，实得 %d", DefaultMTU, plan.MTU)
	}
	if plan.Mode != ModeNonGlobal {
		t.Errorf("默认模式应为 non-global，实得 %s", plan.Mode)
	}
	if len(plan.Families) != 2 {
		t.Fatalf("应有两族，实得 %v", plan.Families)
	}

	// 路由：每族一条默认路由指向表 51820。
	if len(plan.Routes) != 2 {
		t.Fatalf("应有 2 条路由，实得 %d: %v", len(plan.Routes), plan.RouteStrings())
	}
	for i, fam := range []int{FamilyV4, FamilyV6} {
		r := plan.Routes[i]
		if r.Family != fam || r.Table != TableWarp || r.Dev != DefaultIfName {
			t.Errorf("路由 %d 不对: %+v", i, r)
		}
		if r.Dst != DefaultPrefix(fam) {
			t.Errorf("路由 %d 应是默认路由，实得 %v", i, r.Dst)
		}
	}

	// 规则：每族两条 —— 先是「main 表的具体路由优先」，再是「from WARP 地址」。
	want := []Rule{
		{Family: FamilyV4, Pref: PrefSuppressMain, Table: TableMain, SuppressPrefixLen: 0},
		{Family: FamilyV4, Pref: PrefFromV4, Table: TableWarp,
			From: netip.MustParseAddr("172.16.0.2"), SuppressPrefixLen: -1},
		{Family: FamilyV6, Pref: PrefSuppressMain, Table: TableMain, SuppressPrefixLen: 0},
		{Family: FamilyV6, Pref: PrefFromV6, Table: TableWarp,
			From: netip.MustParseAddr("2606:4700:110::2"), SuppressPrefixLen: -1},
	}
	if len(plan.Rules) != len(want) {
		t.Fatalf("应有 %d 条规则，实得 %d: %v", len(want), len(plan.Rules), plan.RuleStrings())
	}
	for i := range want {
		if !plan.Rules[i].Equal(want[i]) {
			t.Errorf("规则 %d 不对\n 期望 %s\n 实得 %s", i, want[i], plan.Rules[i])
		}
	}
}

// 单栈账户只能生成单栈规则：多出一条引用不存在地址的规则会让流量进黑洞。
func TestBuildSingleStack(t *testing.T) {
	plan, err := Build(pfxs("172.16.0.2/32"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Families) != 1 || plan.Families[0] != FamilyV4 {
		t.Fatalf("应只有 IPv4，实得 %v", plan.Families)
	}
	if len(plan.Routes) != 1 || len(plan.Rules) != 2 {
		t.Fatalf("单栈应得 1 路由 2 规则，实得 %d/%d", len(plan.Routes), len(plan.Rules))
	}
	for _, r := range plan.Rules {
		if r.Family != FamilyV4 {
			t.Errorf("不该出现 IPv6 规则: %s", r)
		}
	}

	// 对称地检查 v6 单栈。
	plan6, err := Build(pfxs("2606:4700:110::2/128"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range plan6.Rules {
		if r.Family != FamilyV6 {
			t.Errorf("不该出现 IPv4 规则: %s", r)
		}
	}
}

// 同一族给多个地址时，规则只需要一条 —— 否则规则数会随地址数增长。
func TestBuildDeduplicatesFamilies(t *testing.T) {
	plan, err := Build(pfxs("172.16.0.2/32", "172.16.0.3/32", "2606:4700:110::2/128"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Families) != 2 {
		t.Fatalf("两族应各算一次，实得 %v", plan.Families)
	}
	if len(plan.Rules) != 4 {
		t.Fatalf("应得 4 条规则，实得 %d: %v", len(plan.Rules), plan.RuleStrings())
	}
}

func TestBuildRejectsEmptyAndBadInput(t *testing.T) {
	if _, err := Build(nil, Options{}); err == nil {
		t.Error("没有任何地址应报错")
	}
	if _, err := Build(pfxs(), Options{}); err == nil {
		t.Error("空列表应报错")
	}
	// 无效前缀（零值）应被跳过，最终因为"没有地址"报错。
	if _, err := Build([]netip.Prefix{{}}, Options{}); err == nil {
		t.Error("无效前缀应被忽略并报错")
	}
}

func TestBuildRejectsUnknownMode(t *testing.T) {
	if _, err := Build(pfxs("172.16.0.2/32"), Options{Mode: Mode("half")}); err == nil {
		t.Fatal("不认识的模式应报错，而不是悄悄按某个默认值处理")
	}
	// 两种受支持的模式都必须被接受（M3 起全局模式不再是"未实现"）。
	for _, m := range []Mode{ModeNonGlobal, ModeGlobal} {
		if _, err := Build(pfxs("172.16.0.2/32"), Options{Mode: m}); err != nil {
			t.Fatalf("模式 %s 应被接受: %v", m, err)
		}
	}
}

func TestBuildHonoursOverrides(t *testing.T) {
	plan, err := Build(pfxs("172.16.0.2/32"), Options{IfName: "wg9", MTU: 1380, Table: 12345})
	if err != nil {
		t.Fatal(err)
	}
	if plan.IfName != "wg9" || plan.MTU != 1380 || plan.Table != 12345 {
		t.Fatalf("覆盖项没生效: %+v", plan)
	}
	if plan.Routes[0].Dev != "wg9" || plan.Routes[0].Table != 12345 {
		t.Errorf("路由没跟上覆盖项: %+v", plan.Routes[0])
	}
	if plan.Rules[1].Table != 12345 {
		t.Errorf("规则没跟上覆盖项: %+v", plan.Rules[1])
	}
}

func TestParseMode(t *testing.T) {
	ok := map[string]Mode{
		"":            ModeNonGlobal,
		"non-global":  ModeNonGlobal,
		"nonglobal":   ModeNonGlobal,
		"non_global":  ModeNonGlobal,
		"  GLOBAL  ":  ModeGlobal,
		"global":      ModeGlobal,
	}
	for in, want := range ok {
		got, err := ParseMode(in)
		if err != nil {
			t.Errorf("ParseMode(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %q，期望 %q", in, got, want)
		}
	}
	if _, err := ParseMode("half"); err == nil {
		t.Error("不认识的模式应报错")
	}
}

// ManagedRules 是"零残留"的依据：它必须覆盖**两种模式**会产出的所有规则位置。
//
// 不能反过来要求"每个受管 pref 都必须出现在同一次 Build 里" —— down 的时候
// 我们并不知道之前是哪种模式（甚至可能刚从全局切回非全局），清理清单必须更宽。
func TestManagedRulesCoversBuiltRules(t *testing.T) {
	managed := map[[2]int]bool{}
	for _, m := range ManagedRules() {
		managed[[2]int{m.Family, m.Pref}] = true
	}
	for _, mode := range []Mode{ModeNonGlobal, ModeGlobal} {
		plan, err := Build(pfxs("172.16.0.2/32", "2606:4700:110::2/128"), Options{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range plan.Rules {
			if !managed[[2]int{r.Family, r.Pref}] {
				t.Errorf("[%s] 规则 %s 不在 ManagedRules 里 —— down 时清不掉它", mode, r)
			}
		}
	}
}

// 全局模式：其余流量走 WARP，但必须把隧道自己的封装包排除掉（not fwmark）。
//
// 少了这个 exclude，WireGuard 的封装包会被自己的规则再次送进隧道 ——
// 表现为握手永远不成功、整机断网。这是全局模式唯一"写错就完蛋"的地方。
func TestBuildGlobal(t *testing.T) {
	plan, err := Build(pfxs("172.16.0.2/32", "2606:4700:110::2/128"), Options{Mode: ModeGlobal})
	if err != nil {
		t.Fatal(err)
	}
	if plan.FWMark != FWMarkWarp {
		t.Errorf("全局模式必须要求隧道打 fwmark（实得 %d）", plan.FWMark)
	}

	want := []Rule{
		{Family: FamilyV4, Pref: PrefSuppressMain, Table: TableMain, SuppressPrefixLen: 0},
		{Family: FamilyV4, Pref: PrefGlobalTunnel, Table: TableWarp,
			FWMark: FWMarkWarp, Invert: true, SuppressPrefixLen: -1},
		{Family: FamilyV6, Pref: PrefSuppressMain, Table: TableMain, SuppressPrefixLen: 0},
		{Family: FamilyV6, Pref: PrefGlobalTunnel, Table: TableWarp,
			FWMark: FWMarkWarp, Invert: true, SuppressPrefixLen: -1},
	}
	if len(plan.Rules) != len(want) {
		t.Fatalf("应有 %d 条规则，实得 %d: %v", len(want), len(plan.Rules), plan.RuleStrings())
	}
	for i := range want {
		if !plan.Rules[i].Equal(want[i]) {
			t.Errorf("规则 %d 不对\n 期望 %s\n 实得 %s", i, want[i], plan.Rules[i])
		}
	}

	// 全局模式靠策略规则接管流量，**不该**出现按源地址匹配的规则。
	for _, r := range plan.Rules {
		if r.From.IsValid() {
			t.Errorf("全局模式不该出现 from 规则: %s", r)
		}
		if r.FWMark != 0 && !r.Invert {
			t.Errorf("交付给 WARP 的那条必须是 `not fwmark`，实得 %s", r)
		}
	}

	// 接口地址与表里的默认路由与非全局完全一致 —— 换模式不需要重建接口。
	if len(plan.Addrs) != 2 || len(plan.Routes) != 2 {
		t.Errorf("地址/路由应与非全局一致，实得 %d 地址 %d 路由", len(plan.Addrs), len(plan.Routes))
	}
}

// 反向：非全局模式不该出现 fwmark 规则（那是全局的特征）。
func TestBuildNonGlobalHasNoFwMark(t *testing.T) {
	plan, err := Build(pfxs("172.16.0.2/32"), Options{Mode: ModeNonGlobal})
	if err != nil {
		t.Fatal(err)
	}
	if plan.FWMark != 0 {
		t.Errorf("非全局模式不该要求 fwmark，实得 %d", plan.FWMark)
	}
	for _, r := range plan.Rules {
		if r.FWMark != 0 {
			t.Errorf("非全局模式不该出现 fwmark 规则: %s", r)
		}
	}
}

// pref 号不能撞在一起 —— 撞了就会互相覆盖。
func TestPrefsAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for _, p := range []struct {
		name string
		val  int
	}{
		{"PrefSuppressMain", PrefSuppressMain},
		{"PrefFromV4", PrefFromV4},
		{"PrefFromV6", PrefFromV6},
	} {
		if prev, dup := seen[p.val]; dup {
			t.Fatalf("%s 与 %s 的 pref 都是 %d", p.name, prev, p.val)
		}
		seen[p.val] = p.name
		if p.val <= 0 {
			t.Errorf("%s 的 pref 必须是正数（实得 %d）", p.name, p.val)
		}
	}
}

func TestRuleString(t *testing.T) {
	cases := []struct {
		r    Rule
		want string
	}{
		{Rule{Family: FamilyV4, Pref: 9000, Table: TableMain, SuppressPrefixLen: 0},
			"pref 9000: from all lookup 254 suppress_prefixlength 0"},
		{Rule{Family: FamilyV4, Pref: 9010, Table: TableWarp, From: netip.MustParseAddr("172.16.0.2"), SuppressPrefixLen: -1},
			"pref 9010: from 172.16.0.2 lookup 51820"},
		{Rule{Family: FamilyV6, Pref: 9011, Table: TableWarp, From: netip.MustParseAddr("2606:4700:110::2"), SuppressPrefixLen: -1},
			"pref 9011: from 2606:4700:110::2 lookup 51820"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("实得 %q，期望 %q", got, c.want)
		}
	}
}
