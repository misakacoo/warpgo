package netcfg

import (
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"
)

// managedRule 描述一条"归 warpgo 管"的规则位置。
type managedRule struct {
	Family    int
	Pref      int
	WantTable int
}

// ManagedRules 列出 warpgo 会创建的全部规则位置。
//
// 这个清单是"零残留"的唯一依据：只要这几条都在清理范围内，
// 就不会出现上游那种"规则一直在加、却一条都删不掉"的局面。
func ManagedRules() []managedRule {
	return []managedRule{
		{FamilyV4, PrefSuppressMain, TableMain},
		{FamilyV6, PrefSuppressMain, TableMain},
		{FamilyV4, PrefFromV4, TableWarp},
		{FamilyV6, PrefFromV6, TableWarp},
	}
}

// Result 记录一次应用/清理实际做了什么（供日志与验收使用）。
type Result struct {
	RulesRemoved  []string
	RoutesFlushed int
	RoutesAdded   int
	RulesAdded    int
}

// CleanupRules 删除 warpgo 管理的全部规则。
//
// 这里做了一件上游没做的事：**删之前先核对内容**。pref 号是我们自己选的，
// 但理论上可能与别人的规则撞号；直接按号删会误伤无关配置。所以先读一遍，
// 只有当那条规则的 lookup 表号与我们的预期一致时才删，否则报错停下让人来看。
func CleanupRules() ([]string, error) {
	var removed []string
	for _, m := range ManagedRules() {
		rules, err := ListRules(m.Family)
		if err != nil {
			return removed, err
		}
		r, found := FindRule(rules, m.Family, m.Pref)
		if !found {
			continue
		}
		if r.Table != m.WantTable {
			return removed, fmt.Errorf(
				"pref %d（IPv%d）上有一条不属于 warpgo 的规则（lookup %d，期望 %d），拒绝删除",
				m.Pref, m.Family, r.Table, m.WantTable)
		}
		if err := DelRule(m.Family, m.Pref); err != nil {
			return removed, err
		}
		removed = append(removed, r.String())
	}
	return removed, nil
}

// Apply 把系统调整到 plan 描述的状态。
//
// 顺序是「先清后加」，这是幂等的关键：无论之前跑过多少次、残留了多少条，
// 结果的规则数/路由数都是固定的。上游用 `ip rule add || true` 吞掉 EEXIST，
// 于是每跑一次就多一条，实测累积到过 4681 条。
func Apply(plan *Plan, logf func(string, ...any)) (*Result, error) {
	if plan == nil {
		return nil, errors.New("plan 为空")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	res := &Result{}

	link, err := netlink.LinkByName(plan.IfName)
	if err != nil {
		return res, fmt.Errorf("接口 %s 不存在（应先建接口）: %w", plan.IfName, err)
	}

	// 1. 清掉我们的规则
	removed, err := CleanupRules()
	if err != nil {
		return res, err
	}
	res.RulesRemoved = removed
	for _, r := range removed {
		logf("已清理旧规则: %s", r)
	}

	// 2. 清空我们的路由表
	for _, fam := range plan.Families {
		n, err := FlushTable(fam, plan.Table)
		if err != nil {
			return res, err
		}
		res.RoutesFlushed += n
	}

	// 3. 加路由
	for _, rt := range plan.Routes {
		if err := EnsureDefaultRoute(link, rt.Family, rt.Table); err != nil {
			return res, err
		}
		res.RoutesAdded++
		logf("已添加路由: %s", rt)
	}

	// 4. 加规则
	for _, r := range plan.Rules {
		// ErrRuleExists 说明第 1 步没清干净（理论上不会），当成功继续，
		// 因为我们的目标状态是"这条规则存在"。
		if err := AddRule(r); err != nil && !errors.Is(err, ErrRuleExists) {
			return res, err
		}
		res.RulesAdded++
		logf("已添加规则: %s", r)
	}
	return res, nil
}

// Cleanup 移除 warpgo 的全部规则与路由（拆隧道前调用）。
//
// 刻意**不依赖 Plan**：down 的时候我们可能拿不到完整的 plan（用户可能已经
// 手工改过配置、账户可能已经不在），所以这里按固定的清单清理，保证"不管之前
// 处于什么状态，都能清干净"。
//
// v4 与 v6 都清 —— 即使当前只用了单栈，另一族也可能有历史残留。
func Cleanup(logf func(string, ...any)) (*Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	res := &Result{}

	removed, err := CleanupRules()
	if err != nil {
		return res, err
	}
	res.RulesRemoved = removed
	for _, r := range removed {
		logf("已删除规则: %s", r)
	}

	for _, fam := range []int{FamilyV4, FamilyV6} {
		n, err := FlushTable(fam, TableWarp)
		if err != nil {
			return res, err
		}
		if n > 0 {
			logf("已清空路由表 %d (IPv%d) 的 %d 条路由", TableWarp, fam, n)
		}
		res.RoutesFlushed += n
	}
	return res, nil
}
