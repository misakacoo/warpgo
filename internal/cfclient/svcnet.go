package cfclient

import (
	"fmt"

	"github.com/misakacoo/warpgo/internal/netcfg"
)

// warp-svc 自己写的策略规则（不是我们加的，但卸载后得有人收尾）。
//
// 官方客户端做整机接管（warp 模式）时会往系统里插一条高优先级规则：
//
//	not from all fwmark 0x100cf lookup 65743    （IPv4 与 IPv6 各一条）
//
// 语义是"不带 0x100cf 标记的流量先查 65743 表"—— 隧道自己的封装包打上
// 0x100cf，从这条规则旁边绕过去。正常注销（disconnect / unregister）时
// warp-svc 会自己把它撤掉；但**卸载路径是先停服务再卸包**，服务被硬杀，
// 这两条规则就留在系统里了。表 65743 随包删除而不存在，lookup 落空、
// 流量继续走 main 表 —— 无害，但违反"卸载零残留"的承诺。
//
// 只认内容、不认来源：warp-svc 不给我们任何句柄，删除前按 pref + 取反 +
// fwmark + 表号逐项核对，撞上同号的无关配置时拒绝动手（与钩子清理、
// netcfg.CleanupRules 同一条原则）。
const (
	// SvcPref 是 warp-svc 规则的优先级（比我们自己的 9000 靠前）。
	SvcPref = 8999
	// SvcTable 是 warp-svc 的路由表号。
	SvcTable = 65743
	// SvcFWMark 是 warp-svc 给自己封装包打的防火墙标记。
	SvcFWMark = 0x100cf
)

// svcLeftover 报告一条规则是否就是 warp-svc 留下的那一条。
//
// 四个字段全部一致才敢删：pref 相同可能是巧合（别的软件挑了同样的号），
// 但 pref + `not fwmark 0x100cf` + `lookup 65743` 的组合同时撞车，
// 概率上可以忽略 —— 而错删一条不认识的规则是不可逆的。
func svcLeftover(r netcfg.Rule) bool {
	return r.Pref == SvcPref &&
		r.Invert &&
		r.FWMark == SvcFWMark &&
		r.Table == SvcTable &&
		r.IsZeroFrom()
}

// SvcCleanupRules 删除 warp-svc 遗留的策略规则（IPv4 / IPv6 各查一遍）。
//
// 返回被删规则的描述。找不到就返回空切片 —— 这不是错误：正常注销过的
// 客户端不会留下任何东西，此时本函数只是空跑一遍。
func SvcCleanupRules() ([]string, error) {
	var removed []string
	for _, fam := range []int{netcfg.FamilyV4, netcfg.FamilyV6} {
		rules, err := netcfg.ListRules(fam)
		if err != nil {
			return removed, fmt.Errorf("列出 IPv%d 规则: %w", fam, err)
		}
		r, found := netcfg.FindRule(rules, fam, SvcPref)
		if !found || !svcLeftover(r) {
			continue
		}
		if err := netcfg.DelRule(fam, SvcPref); err != nil {
			return removed, fmt.Errorf("删除 IPv%d pref %d: %w", fam, SvcPref, err)
		}
		removed = append(removed, r.String())
	}
	return removed, nil
}
