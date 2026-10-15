package cfclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/netcfg"
)

// 钩子（菜单 `z` / `x`）用的表号与规则优先级。
//
// 刻意与 warpgo 自己那套隧道（表 51820、pref 9000/9010/9011）**分开**：
//   - 两者的规则结构一样，但匹配的是不同的源地址（一个是 warpgo 账户的地址，
//     一个是官方客户端接口的地址），因此可以共存；
//   - 分开之后，`hook del` 不会碰到隧道规则，`warpgo down` 也不会碰到钩子规则。
//     上游两套共用表 51820，清理时是互相踩的。
const (
	// HookTable 是官方客户端钩子用的路由表。
	HookTable = 51821

	// HookPrefSuppressMain 让 main 表里的具体路由（内网、直连）优先。
	// 与隧道侧同一条规则、只是号不同 —— 语义完全一样，见 netcfg 里的说明。
	HookPrefSuppressMain = 9100

	// HookPrefFromV4 / HookPrefFromV6 把"以客户端接口地址为源"的流量交给钩子表。
	HookPrefFromV4 = 9110
	HookPrefFromV6 = 9111
)

// drop-in 的落点。
//
// 用 drop-in 而不是直接改 `/lib/systemd/system/warp-svc.service`：
// 后者是 **dpkg 管的文件**，改它会在下次 `apt upgrade` 时被静默覆盖，
// 而症状是"重启之后规则又没了"——极难往"包升级"上想。
// drop-in 不受包管理影响，也随时可以整份删掉。
const (
	DropInDir  = "/etc/systemd/system/warp-svc.service.d"
	DropInFile = "warpgo-hooks.conf"
)

// ErrIfaceAbsent 表示等不到客户端的接口（多半是客户端不在 warp 模式下）。
var ErrIfaceAbsent = errors.New("官方客户端的接口没有出现")

// ClientIfaceAddrs 读出 `CloudflareWARP` 接口上的地址（每族取第一个）。
//
// 过滤掉链路本地与回环：它们是自动生成的，拿它们做策略规则没有意义，
// 而且 v6 链路本地地址带 `%zone`，塞进 netip 会直接解析失败。
func ClientIfaceAddrs() ([]netip.Prefix, error) {
	iface, err := net.InterfaceByName(ClientIfName)
	if err != nil {
		return nil, err
	}
	raw, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, a := range raw {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// HookRules 由接口地址生成期望的规则。
//
// 纯函数（只依赖入参），于是"给定地址该产生哪几条规则"可以单测 ——
// 而它正是钩子里最容易写错的部分。
func HookRules(addrs []netip.Prefix) []netcfg.Rule {
	var rules []netcfg.Rule
	seen := map[int]bool{}
	for _, a := range addrs {
		if !a.IsValid() {
			continue
		}
		addr := a.Addr()
		fam, pref := netcfg.FamilyV4, HookPrefFromV4
		if addr.Is6() {
			fam, pref = netcfg.FamilyV6, HookPrefFromV6
		}
		if seen[fam] {
			continue
		}
		seen[fam] = true

		// 先让 main 表里的具体路由优先（否则以接口地址为源的流量去访问内网
		// 会被一律塞进 WARP —— 与隧道侧同一条理由）。
		rules = append(rules, netcfg.Rule{
			Family: fam, Pref: HookPrefSuppressMain, Table: netcfg.TableMain, SuppressPrefixLen: 0,
		})
		rules = append(rules, netcfg.Rule{
			Family: fam, Pref: pref, Table: HookTable, From: addr, SuppressPrefixLen: -1,
		})
	}
	return rules
}

// HookFamilies 返回地址里出现过的地址族（去重、v4 在前）。
func HookFamilies(addrs []netip.Prefix) []int {
	var out []int
	seen := map[int]bool{}
	for _, fam := range []int{netcfg.FamilyV4, netcfg.FamilyV6} {
		for _, a := range addrs {
			if !a.IsValid() {
				continue
			}
			got := netcfg.FamilyV4
			if a.Addr().Is6() {
				got = netcfg.FamilyV6
			}
			if got == fam && !seen[fam] {
				seen[fam] = true
				out = append(out, fam)
			}
		}
	}
	return out
}

// HookRoutes 生成钩子表里的默认路由。
func HookRoutes(families []int) []netcfg.Route {
	out := make([]netcfg.Route, 0, len(families))
	for _, fam := range families {
		out = append(out, netcfg.Route{
			Family: fam, Table: HookTable, Dst: netcfg.DefaultPrefix(fam), Dev: ClientIfName,
		})
	}
	return out
}

// hookRule 描述一条"归钩子管"的规则位置。
type hookRule struct {
	Family    int
	Pref      int
	WantTable int
}

// hookManagedRules 列出钩子会创建的全部规则位置。
//
// 两族都列上（即使当前只用了单栈）：另一族可能有历史残留，
// 而"不管之前处于什么状态都能清干净"是本项目的一贯要求。
func hookManagedRules() []hookRule {
	return []hookRule{
		{netcfg.FamilyV4, HookPrefSuppressMain, netcfg.TableMain},
		{netcfg.FamilyV6, HookPrefSuppressMain, netcfg.TableMain},
		{netcfg.FamilyV4, HookPrefFromV4, HookTable},
		{netcfg.FamilyV6, HookPrefFromV6, HookTable},
	}
}

// HookCleanupRules 删除钩子管理的全部规则。
//
// 与 netcfg.CleanupRules 同一条原则：**删之前先核对内容**。
// pref 是我们选的，但理论上可能撞号，直接按号删会误伤无关配置。
func HookCleanupRules() ([]string, error) {
	var removed []string
	for _, h := range hookManagedRules() {
		rules, err := netcfg.ListRules(h.Family)
		if err != nil {
			return removed, err
		}
		r, found := netcfg.FindRule(rules, h.Family, h.Pref)
		if !found {
			continue
		}
		if r.Table != h.WantTable {
			return removed, fmt.Errorf(
				"pref %d（IPv%d）上有一条不属于官方客户端钩子的规则（lookup %d，期望 %d），拒绝删除",
				h.Pref, h.Family, r.Table, h.WantTable)
		}
		if err := netcfg.DelRule(h.Family, h.Pref); err != nil {
			return removed, err
		}
		removed = append(removed, r.String())
	}
	return removed, nil
}

// HookDel 撤掉钩子加的全部规则与路由。
//
// 它**不依赖接口是否存在**：删除一律按 pref（不解析显示文本），
// 接口已经没了也照样删得掉 —— 上游按字符串匹配，接口一删就一条都清不掉。
func (m *Manager) HookDel(logf func(string, ...any)) (*netcfg.Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	res := &netcfg.Result{}

	removed, err := HookCleanupRules()
	if err != nil {
		return res, err
	}
	res.RulesRemoved = removed
	for _, r := range removed {
		logf("已删除钩子规则: %s", r)
	}

	for _, fam := range []int{netcfg.FamilyV4, netcfg.FamilyV6} {
		n, err := netcfg.FlushTable(fam, HookTable)
		if err != nil {
			return res, err
		}
		if n > 0 {
			logf("已清空钩子表 %d (IPv%d) 的 %d 条路由", HookTable, fam, n)
		}
		res.RoutesFlushed += n
	}
	return res, nil
}

// HookAdd 加钩子规则与路由：等接口 → 读地址 → 先清后加。
//
// `wait` 是等接口出现的上限。**必须给它一个等待窗口**：这个命令最常被
// systemd 的 `ExecStartPost` 调用，而那一刻 warp-svc 的进程刚起来、
// 接口还没建出来（接口要等客户端连上才出现）。等不到就返回 ErrIfaceAbsent，
// 由调用方决定怎么处理 —— 但**绝不能因此让 warp-svc 启动失败**。
func (m *Manager) HookAdd(ctx context.Context, wait time.Duration, logf func(string, ...any)) (*netcfg.Result, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	res := &netcfg.Result{}

	if err := waitUntil(ctx, wait, 500*time.Millisecond, func() bool {
		return interfaceExists(ClientIfName)
	}, fmt.Sprintf("等待接口 %s 出现", ClientIfName)); err != nil {
		return res, fmt.Errorf("%w（%v）", ErrIfaceAbsent, err)
	}

	addrs, err := ClientIfaceAddrs()
	if err != nil {
		return res, fmt.Errorf("读取 %s 的地址: %w", ClientIfName, err)
	}
	if len(addrs) == 0 {
		return res, fmt.Errorf("%w：接口存在但没有任何可用地址", ErrIfaceAbsent)
	}
	families := HookFamilies(addrs)
	rules := HookRules(addrs)
	logf("接口 %s 的地址: %v", ClientIfName, addrs)

	// ① 先清掉自己的旧规则（先清后加 ⇒ 幂等）。
	removed, err := HookCleanupRules()
	if err != nil {
		return res, err
	}
	res.RulesRemoved = removed
	for _, r := range removed {
		logf("已清理旧钩子规则: %s", r)
	}

	// ② 清空自己的表。
	for _, fam := range families {
		n, err := netcfg.FlushTable(fam, HookTable)
		if err != nil {
			return res, err
		}
		res.RoutesFlushed += n
	}

	// ③ 加路由（接口已确认存在）。
	for _, rt := range HookRoutes(families) {
		if err := netcfg.EnsureDefaultRouteDev(rt.Dev, rt.Family, rt.Table); err != nil {
			return res, err
		}
		res.RoutesAdded++
		logf("已添加钩子路由: %s", rt)
	}

	// ④ 加规则。
	for _, r := range rules {
		if err := netcfg.AddRule(r); err != nil && !errors.Is(err, netcfg.ErrRuleExists) {
			return res, err
		}
		res.RulesAdded++
		logf("已添加钩子规则: %s", r)
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// systemd drop-in
// ---------------------------------------------------------------------------

// RenderDropIn 生成 drop-in 内容（纯函数，可逐字节单测）。
//
// 两个钩子各自的作用：
//   - `ExecStartPost` 在服务起来后补上策略规则。开机重启时是它保证
//     "重启之后规则还在"；
//   - `ExecStop` 在服务停下时撤掉规则。少了它，服务停了规则还指着
//     一个不存在的接口，现场看起来像"WARP 拆了但流量还在往表里走"。
//
// **`hook del` 必须永远以 0 退出**：ExecStop 非零退出会让 systemd 把整个
// 单元标成 failed，之后 restart 的语义会变得难以推理 —— 这个坑在 M4 记过。
func RenderDropIn(binPath string) string {
	var b strings.Builder
	b.WriteString("# 本文件由 `warpgo client install` 生成 —— 手工改动会在下次安装时被覆盖。\n")
	b.WriteString("#\n")
	b.WriteString("# 它给官方客户端的 warp-svc 挂上两个钩子：服务起来后补策略规则、\n")
	b.WriteString("# 服务停下时撤掉它们。用 drop-in 而不是改包自带的单元文件，\n")
	b.WriteString("# 是因为后者由 dpkg 管，会在下次升级时被静默覆盖。\n\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "ExecStartPost=%s hook add --wait 30s\n", binPath)
	fmt.Fprintf(&b, "ExecStop=%s hook del\n", binPath)
	return b.String()
}

// DropInExists 报告 drop-in 是否在位。
func (m *Manager) DropInExists() bool { return fileExists(m.DropInPath) }

// InstallDropIn 写入 drop-in 并 daemon-reload。
func (m *Manager) InstallDropIn(ctx context.Context, binPath string) error {
	if err := os.MkdirAll(filepath.Dir(m.DropInPath), 0o755); err != nil {
		return fmt.Errorf("创建 %s: %w", filepath.Dir(m.DropInPath), err)
	}
	if err := os.WriteFile(m.DropInPath, []byte(RenderDropIn(binPath)), 0o644); err != nil {
		return fmt.Errorf("写入 %s: %w", m.DropInPath, err)
	}
	// 写完必须 reload，否则 systemd 看到的还是旧配置 ——
	// 表现是"明明装了钩子却没生效"，而重试一次还是同样结果。
	return m.Service().DaemonReload(ctx)
}

// RemoveDropIn 删掉 drop-in 并 daemon-reload；返回是否真的删了。
func (m *Manager) RemoveDropIn(ctx context.Context) (bool, error) {
	if !m.DropInExists() {
		return false, nil
	}
	if err := os.Remove(m.DropInPath); err != nil {
		return false, fmt.Errorf("删除 %s: %w", m.DropInPath, err)
	}
	// 目录空了就顺手删掉，不给 /etc 留一个空目录。
	if entries, err := os.ReadDir(filepath.Dir(m.DropInPath)); err == nil && len(entries) == 0 {
		_ = os.Remove(filepath.Dir(m.DropInPath))
	}
	if err := m.Service().DaemonReload(ctx); err != nil {
		// 文件已经删了，reload 失败只是让运行中的 systemd 还记着旧的 drop-in；
		// 重启后现场自然一致，所以这里只返回警告级的错误。
		return true, err
	}
	return true, nil
}
