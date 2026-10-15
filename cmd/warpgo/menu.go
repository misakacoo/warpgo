package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/cfclient"
	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/gai"
	"github.com/misakacoo/warpgo/internal/health"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/netcfg"
	"github.com/misakacoo/warpgo/internal/proxy"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// cmdMenu 是交互菜单。**裸敲 `warpgo`（且开着终端）也会走到这里**，
// 见 main.go 的 runDefault —— 上游 `warp` 就是这个手感，菜单的意义就是不用记子命令。
//
// 它是**薄封装**：每个选项最终都翻译成一条已经存在的子命令，再交给 run() 执行。
// 这样做而不是"在菜单里直接调内部函数"，有三个实际好处：
//   - 菜单与命令行走的是**同一条代码路径**，不会出现"命令行能修好、菜单修不好"；
//   - 每一项都能被脚本复现（把菜单里的 argv 抄下来就是那条命令）；
//   - 新增功能时不必改菜单框架，加一行映射即可。
func (a *app) cmdMenu(args []string) int {
	if len(args) > 0 {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, args[0])))
	}
	in := bufio.NewReader(os.Stdin)

	for {
		a.printMenu()
		choice, err := a.ask(in, msg.T(msg.KeyMenuPrompt))
		if err != nil {
			// stdin 关闭（管道里跑）不算错误，安静退出即可。
			fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuBye))
			return exitOK
		}
		choice = strings.ToLower(strings.TrimSpace(choice))
		switch choice {
		case "":
			continue
		case "0", "q", "quit", "exit":
			fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuBye))
			return exitOK
		}

		e, found := findMenuEntry(choice)
		if !found {
			a.log.Warnf("%s", msg.Tf(msg.KeyMenuBadChoice, choice))
			continue
		}
		argv, ok := a.argvFor(e, in)
		if !ok {
			continue
		}
		if argv == nil {
			continue // 用户在追问里取消
		}
		// 会动机器状态的选项先问一次（默认不执行）。
		//
		// 这一关存在的理由就是"按错一位"：选项号是单字符的，9 与 0、1 与 2
		// 在键盘上挨着，而按错的代价可能是"换掉隧道方式"甚至"拆掉隧道"。
		// 追问过具体值的项（选项 9 / 10 / 11）也一样要问 —— 那几步恰恰
		// 是"改设置"，最需要最后一次拦。
		if e.mutates && !a.confirm(e, in) {
			a.log.Infof("%s", msg.T(msg.KeyMenuCanceled))
			continue
		}
		fmt.Fprintln(a.stdout)
		code := run(argv)
		if code != exitOK {
			a.log.Warnf("%s", msg.Tf(msg.KeyMenuSubFailed, strings.Join(argv, " "), code))
		}
		// 卸载会把**这个二进制自己**删掉。进程还活在内存里，菜单当然能接着转，
		// 但"已经卸载了还在菜单里点下一项"是个说不通的现场 —— 直接收摊。
		if argv[0] == "uninstall" && code == exitOK {
			fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuBye))
			return exitOK
		}
		fmt.Fprintln(a.stdout)
	}
}

// menuEntry 是一个菜单项。
//
// 字段顺序：分组 / 选项号 / 文案 key / 会不会动机器 / 静态命令 / 动态判定。
//
// 两种项：
//   - **静态项**：`argv` 就是那条命令（如 `status` → `warpgo status`）。
//   - **动态项**：`decide` 按现场状态算出该跑哪条命令（如"开关"要看当前是开还是关）。
//
// 用 `decide` 而不是"在 menuArgv 里 switch 选项号"，是为了让"哪些项是动态的"
// 只写在**这一张表**上：早先的写法要把每个动态项的数字在表里和 switch 里各写一遍，
// 加一项就得同时改两处、还容易对不上号。
//
// `mutates` 决定这一项要不要**先问一次**（见 cmdMenu 里的 confirm）。
// 判据是"这条命令会不会改机器的状态"，与危险不危险无关 ——
// 只读项（查看状态 / 查看账户 / 查新版本）不问，其余全问。
// 这样"什么时候会被问"是一条能一眼看懂的规则，而不是逐个选项的取舍。
type menuEntry struct {
	section string                                     // 分组标题（文案 key）；与上一条不同时才打印
	choice  string                                     // 用户敲的键
	label   string                                     // 选项文案 key
	mutates bool                                       // 会改机器状态 → 执行前必须确认
	argv    []string                                   // 静态项的命令
	decide  func(*app, *bufio.Reader) ([]string, bool) // 动态项：返回 argv（nil = 用户取消）与"这个键存在吗"
}

// menuEntries 是菜单的全部选项。
//
// 顺序即显示顺序，分组按"从最常做的到最危险的"排：
// 账户 → 隧道 → 官方客户端 → WireProxy → 保活与维护。
// `uninstall` 放最后一项是刻意的 —— 它是唯一会把这个二进制本身删掉的操作。
func menuEntries() []menuEntry {
	return []menuEntry{
		{msg.KeyMenuSecAccount, "1", msg.KeyMenuRegister, true, []string{"register"}, nil},
		{msg.KeyMenuSecAccount, "2", msg.KeyMenuAccount, false, []string{"account"}, nil},

		{msg.KeyMenuSecTunnel, "3", msg.KeyMenuUpNonGlobal, true, []string{"up"}, nil},
		{msg.KeyMenuSecTunnel, "4", msg.KeyMenuUpGlobal, true, []string{"up", "--mode", "global"}, nil},
		{msg.KeyMenuSecTunnel, "5", msg.KeyMenuOff, true, []string{"off"}, nil},
		{msg.KeyMenuSecTunnel, "6", msg.KeyMenuOn, true, []string{"on"}, nil},
		{msg.KeyMenuSecTunnel, "7", msg.KeyMenuStatus, false, []string{"status"}, nil},
		{msg.KeyMenuSecTunnel, "8", msg.KeyMenuRotate, true, []string{"rotate"}, nil},
		{msg.KeyMenuSecTunnel, "9", msg.KeyMenuSwitchBackend, true, nil, (*app).menuSwitchBackend},
		{msg.KeyMenuSecTunnel, "10", msg.KeyMenuSwitchMode, true, nil, (*app).menuSwitchMode},
		{msg.KeyMenuSecTunnel, "11", msg.KeyMenuPriority, true, nil, (*app).menuPriority},
		{msg.KeyMenuSecTunnel, "12", msg.KeyMenuDown, true, []string{"down"}, nil},

		{msg.KeyMenuSecClient, "13", msg.KeyMenuClientProxy, true, []string{"client", "install", "--mode", "proxy"}, nil},
		{msg.KeyMenuSecClient, "14", msg.KeyMenuClientWarp, true, []string{"client", "install", "--mode", "warp"}, nil},
		{msg.KeyMenuSecClient, "15", msg.KeyMenuClientToggle, true, nil, (*app).menuClientToggle},
		{msg.KeyMenuSecClient, "16", msg.KeyMenuClientStatus, false, []string{"client", "status"}, nil},

		{msg.KeyMenuSecProxy, "17", msg.KeyMenuProxyInstall, true, []string{"proxy", "install"}, nil},
		{msg.KeyMenuSecProxy, "18", msg.KeyMenuProxyToggle, true, nil, (*app).menuProxyToggle},
		{msg.KeyMenuSecProxy, "19", msg.KeyMenuProxyStatus, false, []string{"proxy", "status"}, nil},

		{msg.KeyMenuSecMaint, "20", msg.KeyMenuInstallWatch, true, []string{"install"}, nil},
		{msg.KeyMenuSecMaint, "21", msg.KeyMenuCheckUpdate, false, []string{"version", "--check"}, nil},
		{msg.KeyMenuSecMaint, "22", msg.KeyMenuUninstall, true, []string{"uninstall"}, nil},
	}
}

// printMenu 打印菜单。
//
// 顶部先给一行系统状态 —— 上游的菜单也是这样：菜单的用途就是"看一眼现在是什么状态、
// 再决定按哪个键"，所以状态不该要人再敲一次 `status` 才看得到。
//
// 这一行**只读本机的运行态与账户文件**：不联网、不做探测、不改任何东西。
// 打开菜单是个纯观察动作，不该有副作用（因此在断网时也照样能看）。
func (a *app) printMenu() {
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuTitle))
	for _, line := range a.menuStatusLines() {
		fmt.Fprintf(a.stdout, "  %s\n", line)
	}

	section := ""
	for _, e := range menuEntries() {
		if e.section != section {
			section = e.section
			fmt.Fprintf(a.stdout, "\n%s\n", msg.T(section))
		}
		fmt.Fprintf(a.stdout, "  %2s) %s\n", e.choice, msg.T(e.label))
	}

	fmt.Fprintf(a.stdout, "\n  %2s) %s\n", "0", msg.T(msg.KeyMenuQuit))
	fmt.Fprintln(a.stdout)
}

// menuStatusLines 拼出菜单顶部的状态区，第一行是概况、后面每族出口一行：
//
//	隧道 warp · 模式 非全局 · 后端 用户态兼容 · 账户 free · 优先走 IPv4
//	出口 IPv4 104.28.211.105（JP）· 3 分钟前实测
//	出口 IPv6 2606:4700:110:834c:…:70e4（JP）· 3 分钟前实测
//
// 为什么必须分这么多行（用户实测反馈"信息都放一行会很长"，随后又指出
// "只显示 IPv4 没显示 IPv6"、"也没显示优先哪一族"）：光一个 IPv6 地址就是
// 39 个字符，三项挤进一行能到 120+ 字符 —— 在 80 列的终端上折得不成样子。
//
// 每一项都独立容错：没有运行态就是"隧道没起来"（菜单最常见的起点，不是错误），
// 没有账户就是"未注册"。两者都不该让菜单打不开 —— 恰恰相反，
// 这两种情况正是用户最需要看到菜单的时候。优先级那一项与运行态无关，永远显示。
func (a *app) menuStatusLines() []string {
	var head []string
	var exit []string

	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil || rt.IfName == "" {
		head = append(head, msg.T(msg.KeyMenuStatusDown))
	} else {
		head = append(head, msg.Tf(msg.KeyMenuStatusUp, rt.IfName))
		if rt.Mode != "" {
			head = append(head, msg.Tf(msg.KeyMenuStatusMode, modeName(rt.Mode)))
		}
		if rt.Backend != "" {
			head = append(head, msg.Tf(msg.KeyMenuStatusBackend, backendName(rt.Backend)))
		}
		exit = menuExitLines(rt)
	}

	acct, err := cloudflare.LoadAccount(state.AccountPath())
	if err != nil {
		head = append(head, msg.T(msg.KeyMenuStatusNoAccount))
	} else {
		head = append(head, msg.Tf(msg.KeyMenuStatusAccount, joinOrDash(acct.AccountType)))
	}

	// 优先级排在概况行最后：它描述的是系统怎么选地址族，与隧道在不在无关，
	// 所以隧道没起来时它照样在（"我该怎么建"本来就该参考它）。
	head = append(head, preferText())

	lines := []string{strings.Join(head, " · ")}
	return append(lines, exit...)
}

// menuExitLines 组织"公网出口 IP"那几行 —— **每个地址族一行**。
//
// 为什么必须分行：IPv6 地址本身就有 39 个字符，把两族挤进一行是 80+ 字符，
// 在 80 列的终端上必折行（用户先前反馈"信息都放一行会很长"）。
// 而"只显示 IPv4"则来自另一处：探测用的 `ProbeAny` 遇到第一个通的族就返回，
// 于是双栈机器上 v6 那条从来没有被记下来过（用户的第二条反馈）。
//
// 打印的是**记下来的值**，不是现探的：菜单是纯观察动作，不许联网
// （断网、隧道刚断的时候它照样要能打开）。代价是这个值可能已经过时，
// 所以每行都带上"多久之前实测" —— 一个不带时间的 IP 会被当成此刻的事实。
// 时间跟着族走：两族的探测各自独立，v6 完全可能是上一轮保活测的。
//
// 只列**隧道实际携带的族**（`rt.Families`）：单栈隧道下再列一个永远为空的
// "出口 IPv6"，等于凭空制造一个假问题。
//
// 显示的是**公网出口**（访问网站时对方看到的地址），不是隧道接口地址
// （172.16.0.2 那种）。用户实测反馈过这一点：后者看着像个 IP，
// 但拿它去对"我的 IP 变了吗"只会得出错误结论。
func menuExitLines(rt *state.Runtime) []string {
	families := rt.Families
	if len(families) == 0 {
		// 老版本写的运行态文件里没有 families；按双栈显示，
		// 认不出的一族自然会落在"未知"上，而不是凭空少一行。
		families = []int{state.FamilyV4, state.FamilyV6}
	}
	lines := make([]string, 0, len(families))
	for _, fam := range families {
		detail := msg.T(msg.KeyExitUnknown)
		if e := rt.ExitFor(fam); e != nil && e.IP != "" {
			detail = exitAgo(e)
		}
		lines = append(lines, msg.Tf(msg.KeyMenuStatusExitIP, health.FamilyName(fam), detail))
	}
	return lines
}

// exitAgo 把一条出口记录拼成"IP（地区）+ 多久前实测"。
//
// **菜单与 `status` 共用**它，而不是各拼一遍：这是同一个值的两种呈现，
// 分开写必然漂移（第一版就漂了 —— 一边写"出口 IP 未知"，一边写"还没体检过"）。
func exitAgo(e *state.Exit) string {
	detail := exitDetail(e.IP, e.Location)
	if e.At.IsZero() {
		return detail
	}
	age := time.Since(e.At)
	if age < time.Minute {
		// "0 秒前实测"读着别扭，而 `up` 一结束就打开菜单恰恰是最常见的现场。
		return msg.Tf(msg.KeyExitJustNow, detail)
	}
	return msg.Tf(msg.KeyExitLine, detail, humanAge(age))
}

// exitDetail 拼出"IP（地区）"；没有地区时只给 IP，不留一个空括号。
func exitDetail(ip, loc string) string {
	if loc == "" {
		return ip
	}
	return fmt.Sprintf("%s（%s）", ip, loc)
}

// preferText 报告本机当前**优先用哪一族地址**（读 /etc/gai.conf，纯本地文件）。
//
// 这是状态区里唯一一项与运行态无关的信息 —— 它描述的是系统的解析偏好，
// 隧道建没建、账户有没有都不影响它，所以**永远都显示**：
// 菜单上少一行，用户会以为这项不存在，而它恰恰是"我该建哪种隧道"的输入之一。
//
// 读不出文件内容时（权限、怪格式）直说读不到，不留空 —— 与文案表"查不到 key
// 就返回 key 本身"是同一条原则：宁可显示一个明确的"不知道"，也不要显示空白。
func preferText() string {
	mode, _, err := gai.New(gai.DefaultPath).Detect()
	if err != nil {
		return msg.T(msg.KeyMenuPreferUnknown)
	}
	switch mode {
	case gai.ModeV4:
		return msg.T(msg.KeyMenuPreferV4)
	case gai.ModeV6:
		return msg.T(msg.KeyMenuPreferV6)
	default:
		return msg.T(msg.KeyMenuPreferDefault)
	}
}

// backendName / modeName 把运行态里的原始值翻成人话。
//
// 运行态文件、`status`、DESIGN 里必须留 `kernel` / `userspace` / `non-global`
// 这些原值（它们要与 CLI 参数、与磁盘上的键对得上），但菜单是给
// "只想知道现在是什么状态"的人看的 —— 那里写 `userspace`、或者写
// "内核 / 用户态"这种行话，等于没说（用户的原话：谁知道是什么）。
//
// 认不出的值原样返回：宁可显示一个没翻译的词，也不要显示空串（§8.15 的同一条原则）。
func backendName(v string) string {
	switch v {
	case string(tunnel.BackendKernel):
		return msg.T(msg.KeyMenuBackendKernel)
	case string(tunnel.BackendUserspace):
		return msg.T(msg.KeyMenuBackendUserspace)
	}
	return v
}

func modeName(v string) string {
	switch v {
	case string(netcfg.ModeGlobal):
		return msg.T(msg.KeyMenuModeGlobal)
	case string(netcfg.ModeNonGlobal):
		return msg.T(msg.KeyMenuModeNonGlobal)
	}
	return v
}

// humanAge 把"多久之前"说成人话。
//
// 不用 state.FormatAge：那个返回 Go 的时长串（`2m5s`），塞进中文句子
// 会变成"2m5s前实测"，读起来比不写还费劲。
func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}

// findMenuEntry 按选项号取菜单项。
func findMenuEntry(choice string) (menuEntry, bool) {
	for _, e := range menuEntries() {
		if e.choice == choice {
			return e, true
		}
	}
	return menuEntry{}, false
}

// argvFor 把选项翻译成一条子命令。返回 (argv, ok)；
// argv 为 nil 表示"用户取消了"（不是错误）。
func (a *app) argvFor(e menuEntry, in *bufio.Reader) ([]string, bool) {
	if e.decide != nil {
		return e.decide(a, in)
	}
	return e.argv, true
}

// confirm 是执行前的最后一道拦：问一句"真要执行吗"，默认**不执行**。
//
// 默认给 N（回车 = 取消）是刻意的：菜单每一步只要一个数字，而按错的代价
// 可能是拆掉隧道、换掉后端、甚至卸载自己。安全侧默认值让"手滑回车"
// 变成"什么都没发生"，而不是"发生了不该发生的事"。
func (a *app) confirm(e menuEntry, in *bufio.Reader) bool {
	ans, err := a.ask(in, msg.Tf(msg.KeyMenuConfirm, msg.T(e.label)))
	if err != nil {
		// 读不到输入（stdin 关了）＝用户已经走了：不执行、也不报错。
		return false
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// menuSwitchBackend 读运行态，给出"换到另一个后端"的命令。
//
// 先把两种方式的**取舍**讲清楚再动手：只说"内核 ↔ 用户态"没人知道该选哪个
// （用户实测反馈的原话）。这段提示是纯文字，不产生任何动作 ——
// 用户看到之后仍然可以按 Ctrl-C 或者选 0 退出，不会因为"点了 8"就被迫换掉。
func (a *app) menuSwitchBackend(in *bufio.Reader) ([]string, bool) {
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil || rt.Backend == "" {
		a.log.Warnf("%s", msg.T(msg.KeyMenuNoRuntime))
		return nil, true
	}
	target := string(tunnel.BackendUserspace)
	if rt.Backend == target {
		target = string(tunnel.BackendKernel)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyMenuSwitchTo, backendName(rt.Backend), backendName(target)))
	a.log.Infof("%s", msg.T(backendHint(rt.Backend)))
	a.log.Infof("%s", msg.T(backendHint(target)))
	return []string{"switch", "--backend", target}, true
}

// backendHint 取某种隧道方式的一句话说明。
func backendHint(v string) string {
	if v == string(tunnel.BackendKernel) {
		return msg.T(msg.KeyMenuBackendHintKernel)
	}
	return msg.T(msg.KeyMenuBackendHintUserspace)
}

// menuSwitchMode 读运行态，给出"换到另一个工作模式"的命令。
func (a *app) menuSwitchMode(in *bufio.Reader) ([]string, bool) {
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil || rt.Mode == "" {
		a.log.Warnf("%s", msg.T(msg.KeyMenuNoRuntime))
		return nil, true
	}
	target := string(netcfg.ModeGlobal)
	if rt.Mode == target {
		target = string(netcfg.ModeNonGlobal)
	}
	a.log.Infof("%s", msg.Tf(msg.KeyMenuSwitchTo, modeName(rt.Mode), modeName(target)))
	a.log.Infof("%s", msg.T(modeHint(rt.Mode)))
	a.log.Infof("%s", msg.T(modeHint(target)))
	return []string{"switch", "--mode", target}, true
}

// modeHint 取某种接管方式的一句话说明。
func modeHint(v string) string {
	if v == string(netcfg.ModeGlobal) {
		return msg.T(msg.KeyMenuModeHintGlobal)
	}
	return msg.T(msg.KeyMenuModeHintNonGlobal)
}

// menuPriority 追问要先走哪一族。
//
// 提示语刻意不用"地址族"这个词：那是网络工程的行话，而这里要问的其实就是
// "你希望先走 IPv4 还是 IPv6"（用户实测的原话：「地址族？你不如叫
// IPV4 还是 IPV6 不是更好嘛」）。选项号仍与上游菜单一致（4 / 6 / d），
// 因为 `warpgo priority 4` 这条命令得对得上。
func (a *app) menuPriority(in *bufio.Reader) ([]string, bool) {
	v, err := a.ask(in, msg.T(msg.KeyMenuAskPriority))
	if err != nil {
		return nil, true
	}
	v = strings.ToLower(strings.TrimSpace(v))
	chosen := map[string]string{
		"4": msg.KeyMenuPriorityV4,
		"6": msg.KeyMenuPriorityV6,
		"d": msg.KeyMenuPriorityDef,
	}
	key, ok := chosen[v]
	if !ok {
		a.log.Warnf("%s", msg.Tf(msg.KeyMenuBadChoice, v))
		return nil, true
	}
	// 把选中的那条再说一遍，紧接着的确认提问才有上下文。
	a.log.Infof("%s", msg.T(key))
	return []string{"priority", v}, true
}

// menuClientToggle 按当前状态决定是开还是关。
//
// 这里直接查状态，而不是跑一次 `client status` 再拿它的退出码当判据 ——
// 后者会把一大段 JSON 打到屏幕上，而用户只是选了个菜单项。
func (a *app) menuClientToggle(in *bufio.Reader) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	if st, err := cfclient.New().Query(ctx); err == nil && st.Connected() {
		return []string{"client", "off"}, true
	}
	return []string{"client", "on"}, true
}

// menuProxyToggle 同上，针对 WireProxy。
func (a *app) menuProxyToggle(in *bufio.Reader) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	if active, err := proxy.New().Systemd().IsActive(ctx); err == nil && active {
		return []string{"proxy", "off"}, true
	}
	return []string{"proxy", "on"}, true
}

// ask 打印提示并读一行。
func (a *app) ask(in *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(a.stdout, prompt)
	line, err := in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
			// 最后一行没有换行符也要收下（管道输入常见）。
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}
