package cfclient

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/misakacoo/warpgo/internal/netcfg"
)

func pfx(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("解析 %q: %v", s, err)
	}
	return p
}

func TestHookRulesDualStack(t *testing.T) {
	addrs := []netip.Prefix{
		pfx(t, "172.16.0.2/32"),
		pfx(t, "2606:4700:110:8d4e::/128"),
	}
	rules := HookRules(addrs)
	if len(rules) != 4 {
		t.Fatalf("双栈应当产生 4 条规则，实得 %d: %v", len(rules), rules)
	}
	// 顺序：v4 suppress、v4 from、v6 suppress、v6 from。
	if rules[0].Pref != HookPrefSuppressMain || rules[0].Table != netcfg.TableMain || rules[0].SuppressPrefixLen != 0 {
		t.Fatalf("第 1 条应当是 v4 的 suppress 规则，实得 %s", rules[0])
	}
	if rules[1].Pref != HookPrefFromV4 || rules[1].Table != HookTable {
		t.Fatalf("第 2 条应当是 v4 的 from 规则，实得 %s", rules[1])
	}
	if !rules[1].From.Is4() || rules[1].From.String() != "172.16.0.2" {
		t.Fatalf("v4 规则的源地址不对: %s", rules[1])
	}
	if rules[2].Pref != HookPrefSuppressMain || rules[2].Family != netcfg.FamilyV6 {
		t.Fatalf("第 3 条应当是 v6 的 suppress 规则，实得 %s", rules[2])
	}
	if rules[3].Pref != HookPrefFromV6 || !rules[3].From.Is6() {
		t.Fatalf("第 4 条应当是 v6 的 from 规则，实得 %s", rules[3])
	}
}

func TestHookRulesSingleStack(t *testing.T) {
	rules := HookRules([]netip.Prefix{pfx(t, "172.16.0.2/32")})
	if len(rules) != 2 {
		t.Fatalf("单栈应当产生 2 条规则，实得 %d", len(rules))
	}
	for _, r := range rules {
		if r.Family != netcfg.FamilyV4 {
			t.Fatalf("单栈账户不该产生 v6 规则: %s", r)
		}
	}
}

func TestHookRulesDedupesFamilies(t *testing.T) {
	// 同一族给两个地址（例如接口上既有全局地址又有 NAT66 地址）时只出一组规则。
	addrs := []netip.Prefix{
		pfx(t, "172.16.0.2/32"),
		pfx(t, "10.0.0.5/32"),
	}
	if got := HookRules(addrs); len(got) != 2 {
		t.Fatalf("同族多地址应当去重成 2 条，实得 %d: %v", len(got), got)
	}
}

func TestHookRulesEmpty(t *testing.T) {
	if got := HookRules(nil); len(got) != 0 {
		t.Fatalf("没有地址时不该产生规则，实得 %v", got)
	}
}

func TestHookFamiliesOrder(t *testing.T) {
	got := HookFamilies([]netip.Prefix{
		pfx(t, "2606:4700:110::1/128"),
		pfx(t, "172.16.0.2/32"),
	})
	if len(got) != 2 || got[0] != netcfg.FamilyV4 || got[1] != netcfg.FamilyV6 {
		t.Fatalf("地址族应当去重且 v4 在前，实得 %v", got)
	}
	if got := HookFamilies(nil); len(got) != 0 {
		t.Fatalf("空输入应当返回空，实得 %v", got)
	}
}

func TestHookRoutes(t *testing.T) {
	routes := HookRoutes([]int{netcfg.FamilyV4, netcfg.FamilyV6})
	if len(routes) != 2 {
		t.Fatalf("应当有 2 条默认路由，实得 %d", len(routes))
	}
	for _, r := range routes {
		if r.Table != HookTable {
			t.Fatalf("路由应当落在钩子表 %d 里，实得 %d", HookTable, r.Table)
		}
		if r.Dev != ClientIfName {
			t.Fatalf("路由应当指向 %s，实得 %s", ClientIfName, r.Dev)
		}
	}
}

// TestHookPrefsDoNotCollideWithTunnel 是本轮最要紧的一条回归防护：
// 钩子的表号与 pref 必须与 warpgo 自己的隧道**完全错开**。
// 撞号会让两边互相删掉对方的规则，而症状是"有时通有时不通"，极难排查。
func TestHookPrefsDoNotCollideWithTunnel(t *testing.T) {
	if HookTable == netcfg.TableWarp {
		t.Fatalf("钩子表号不能与隧道表号相同（都是 %d）", HookTable)
	}
	tunnelPrefs := map[int]string{
		netcfg.PrefSuppressMain: "隧道 suppress",
		netcfg.PrefFromV4:       "隧道 from-v4",
		netcfg.PrefFromV6:       "隧道 from-v6",
		netcfg.PrefGlobalTunnel: "隧道 global",
	}
	for _, p := range []int{HookPrefSuppressMain, HookPrefFromV4, HookPrefFromV6} {
		if name, bad := tunnelPrefs[p]; bad {
			t.Fatalf("钩子 pref %d 与%s 撞号", p, name)
		}
	}
	if HookPrefSuppressMain == HookPrefFromV4 || HookPrefFromV4 == HookPrefFromV6 {
		t.Fatal("钩子的三个 pref 必须互不相同")
	}
}

func TestRenderDropIn(t *testing.T) {
	got := RenderDropIn("/usr/bin/warpgo")

	for _, want := range []string{
		"[Service]\n",
		"ExecStartPost=/usr/bin/warpgo hook add --wait 30s\n",
		"ExecStop=/usr/bin/warpgo hook del\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("drop-in 里缺少 %q\n---\n%s", want, got)
		}
	}
	// 刻意**不写** ExecStop 之外的停止动作，也不该去改包自带的单元文件；
	// 这里守住"我们只用 drop-in"这条约定。
	if strings.Contains(got, "/lib/systemd/system") || strings.Contains(got, "/usr/lib/systemd/system") {
		t.Fatal("drop-in 不该提到包自带的单元文件路径")
	}
	if !strings.HasPrefix(got, "#") {
		t.Fatal("drop-in 应当以说明性注释开头")
	}
}

func TestDropInPathsAreStable(t *testing.T) {
	// 路径是"我们写进去、再按它删掉"的东西，写错就会在 /etc 里留垃圾。
	if DropInDir != "/etc/systemd/system/warp-svc.service.d" {
		t.Fatalf("drop-in 目录变了: %s", DropInDir)
	}
	if DropInFile != "warpgo-hooks.conf" {
		t.Fatalf("drop-in 文件名变了: %s", DropInFile)
	}
}

func TestHookManagedRulesCoverBothFamilies(t *testing.T) {
	// 清理必须覆盖两族（另一族可能有历史残留），否则"零残留"不成立。
	seen := map[int]bool{}
	for _, h := range hookManagedRules() {
		seen[h.Family] = true
	}
	if !seen[netcfg.FamilyV4] || !seen[netcfg.FamilyV6] {
		t.Fatalf("清理清单必须同时覆盖 v4 与 v6，实得 %v", seen)
	}
	if len(hookManagedRules()) != 4 {
		t.Fatalf("应当有 4 个受管位置（两族各 2 条），实得 %d", len(hookManagedRules()))
	}
}
