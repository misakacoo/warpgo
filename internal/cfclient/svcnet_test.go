package cfclient

import (
	"net/netip"
	"testing"

	"github.com/misakacoo/warpgo/internal/netcfg"
)

// svcLeftover 是"要不要删"的唯一判据，所以它的每条否决分支都要单独钉住：
// 多删一条不认识的规则是不可逆的。

func TestSvcLeftoverMatchesWarpSvcRule(t *testing.T) {
	r := netcfg.Rule{
		Family: netcfg.FamilyV4,
		Pref:   SvcPref,
		Table:  SvcTable,
		FWMark: SvcFWMark,
		Invert: true,
	}
	if !svcLeftover(r) {
		t.Fatalf("这条正是 warp-svc 的规则，应当判为遗留: %s", r)
	}
}

func TestSvcLeftoverRejectsNearMisses(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*netcfg.Rule)
	}{
		{"pref 不同", func(r *netcfg.Rule) { r.Pref = SvcPref + 1 }},
		{"没有取反", func(r *netcfg.Rule) { r.Invert = false }},
		{"fwmark 不同", func(r *netcfg.Rule) { r.FWMark = 0xca6c }},
		{"表号不同", func(r *netcfg.Rule) { r.Table = netcfg.TableWarp }},
		{"带了源地址", func(r *netcfg.Rule) { r.From = netip.MustParseAddr("172.16.0.2") }},
	}
	for _, c := range cases {
		r := netcfg.Rule{
			Family: netcfg.FamilyV4,
			Pref:   SvcPref,
			Table:  SvcTable,
			FWMark: SvcFWMark,
			Invert: true,
		}
		c.mut(&r)
		if svcLeftover(r) {
			t.Fatalf("%s：不该判为遗留，会误删别人的规则 —— %s", c.name, r)
		}
	}
}

// 我们自己的全局模式规则（not fwmark 0xca6c lookup 51820）绝不能被这套逻辑碰到。
func TestSvcLeftoverNeverMatchesOwnGlobalRule(t *testing.T) {
	own := netcfg.Rule{
		Family: netcfg.FamilyV4,
		Pref:   netcfg.PrefSuppressMain,
		Table:  netcfg.TableWarp,
		FWMark: netcfg.FWMarkWarp,
		Invert: true,
	}
	if svcLeftover(own) {
		t.Fatalf("把 warpgo 自己的全局模式规则当成别人的了: %s", own)
	}
}
