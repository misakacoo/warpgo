package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misakacoo/warpgo/internal/logx"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/state"
)

// newTestApp 造一个把输出丢到 /dev/null 的 app。
//
// 注意 app 的 stdout/stderr 是 *os.File（不是 io.Writer），所以用 os.DevNull
// 而不是 io.Discard —— 菜单状态那一行只看返回值，但万一它以后开始打日志，
// 也不该污染测试输出。
func newTestApp(t *testing.T) *app {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	return &app{
		log:    logx.New(devnull, logx.LevelError),
		stdout: devnull,
		stderr: devnull,
	}
}

func writeStateFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state.Dir(), name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMenuStatusLinesOnCleanMachine 覆盖菜单最常见的起点：什么都没建。
//
// 概况行必须能打出来 —— 恰恰是"隧道没起来、账户也没注册"的时候，
// 用户最需要看到菜单（而不是被一个报错挡住）。而且这一行里**必须有
// "优先哪一族"**：它与运行态无关，是独立的系统设置。
func TestMenuStatusLinesOnCleanMachine(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())

	lines := newTestApp(t).menuStatusLines()
	if len(lines) != 1 {
		t.Fatalf("隧道没起来时不该有出口行（无从谈起），实得 %d 行: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "隧道未建立") {
		t.Fatalf("没有运行态时应显示「隧道未建立」，实得 %q", lines[0])
	}
	if !strings.Contains(lines[0], "未注册") {
		t.Fatalf("没有账户时应显示「未注册」，实得 %q", lines[0])
	}
	// 优先族与隧道在不在无关：它读的是 /etc/gai.conf。
	if got := preferText(); got == "" || !strings.Contains(lines[0], got) {
		t.Fatalf("概况行里应有「优先哪一族」，实得 %q", lines[0])
	}
}

// TestMenuStatusLinesWithRuntimeAndAccount 是本条最要紧的正面用例：
// 状态区必须**概况一行 + 每个地址族各一行**，而且出口那几行显示的是
// **公网出口 IP**，不是隧道接口地址。
//
// 用户连着提了两条："IP 放第二行，信息都放一行会很长"、"只显示了 IPv4
// 没显示 IPv6"。这条测试把两件事都钉住。
func TestMenuStatusLinesWithRuntimeAndAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)

	checkedAt := time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)
	writeStateFile(t, state.RuntimeFile, `{
		"if_name": "warp",
		"mode": "non-global",
		"backend": "kernel",
		"mtu": 1280,
		"table": 51820,
		"families": [4, 6],
		"addrs": ["172.16.0.2/32", "2606:4700:110::1/128"],
		"endpoint": "engage.cloudflareclient.com:2408",
		"exit_v4": {"ip": "104.28.211.105", "location": "JP", "at": "`+checkedAt+`"},
		"exit_v6": {"ip": "2606:4700:110:834c:eb9e:c595:a449:70e4", "location": "JP", "at": "`+checkedAt+`"},
		"up_since": "2026-10-15T00:00:00Z"
	}`)
	writeStateFile(t, state.AccountFile, `{
		"id": "abc", "account_type": "free",
		"private_key": "x", "peer_public_key": "y",
		"endpoint": "engage.cloudflareclient.com:2408"
	}`)

	lines := newTestApp(t).menuStatusLines()
	if len(lines) != 3 {
		t.Fatalf("双栈应有三行（概况 + 每族一行），实得 %d 行: %q", len(lines), lines)
	}
	for _, want := range []string{"隧道 warp", "模式 非全局", "后端 内核直通", "账户 free"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("概况行里应包含 %q，实得 %q", want, lines[0])
		}
	}
	for _, want := range []string{"出口 IPv4 104.28.211.105", "JP", "3 分钟前实测"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("IPv4 那一行里应包含 %q，实得 %q", want, lines[1])
		}
	}
	for _, want := range []string{"出口 IPv6", "2606:4700:110:834c:eb9e:c595:a449:70e4"} {
		if !strings.Contains(lines[2], want) {
			t.Fatalf("IPv6 那一行里应包含 %q，实得 %q", want, lines[2])
		}
	}
	// 隧道内部地址（172.16.0.2 / 2606:4700:110::1）**不该**出现在菜单里：
	// 它不是"我的 IP"，拿它去判断"IP 变没变"只会得出错误结论。
	for _, line := range lines {
		if strings.Contains(line, "172.16.0.2") || strings.Contains(line, "2606:4700:110::1") {
			t.Fatalf("菜单状态行里不该出现隧道内部地址: %q", line)
		}
	}
	// 运行态里的原始值不该原样露出来（用户看不懂 kernel / non-global）。
	if strings.Contains(lines[0], "kernel") || strings.Contains(lines[0], "non-global") {
		t.Fatalf("概况行应显示人话而不是运行态原值: %q", lines[0])
	}
}

// TestMenuStatusLinesExitUnknown：隧道在线但从没体检过 —— 出口行要说清
// "还不知道"，而不是留空（留空看着像这一行坏了，也像"没有出口 IP"）。
func TestMenuStatusLinesExitUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp", "mode": "non-global", "backend": "userspace"}`)

	lines := newTestApp(t).menuStatusLines()
	if len(lines) != 3 {
		t.Fatalf("运行态里没有 families 时按双栈显示，实得 %d 行: %q", len(lines), lines)
	}
	for _, i := range []int{1, 2} {
		if !strings.Contains(lines[i], "还没体检过") {
			t.Fatalf("第 %d 行应说明出口 IP 还不知道，实得 %q", i+1, lines[i])
		}
	}
	if !strings.Contains(lines[0], "后端 用户态兼容") {
		t.Fatalf("后端应翻成人话，实得 %q", lines[0])
	}
}

// TestMenuExitLinesPerFamily 钉住"每族一行、时间跟着族走"。
//
// 两族的探测是各自独立的（`up` 并行探、保活一轮只探一族），所以
// "IPv4 刚刚测过、IPv6 是一小时前测的"是**正常现场**，
// 而共用一个时间戳恰好会把这件事说反 —— 那正是这条测试要挡的。
func TestMenuExitLinesPerFamily(t *testing.T) {
	now := time.Now()
	rt := &state.Runtime{
		Families: []int{state.FamilyV4, state.FamilyV6},
		ExitV4:   &state.Exit{IP: "104.28.211.105", Location: "JP", At: now},
		ExitV6:   &state.Exit{IP: "2606:4700:110:834c:eb9e:c595:a449:70e4", Location: "HK", At: now.Add(-time.Hour)},
	}
	lines := menuExitLines(rt)
	if len(lines) != 2 {
		t.Fatalf("两族都该各占一行，实得 %d 行: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "IPv4") || !strings.Contains(lines[0], "104.28.211.105") {
		t.Fatalf("第一行应是 IPv4 出口: %q", lines[0])
	}
	if !strings.Contains(lines[1], "IPv6") || !strings.Contains(lines[1], "2606:4700:110:834c") {
		t.Fatalf("第二行应是 IPv6 出口: %q", lines[1])
	}
	if strings.Contains(lines[1], "刚刚实测") {
		t.Fatalf("IPv6 那条是一小时前测的，不该跟着 IPv4 说「刚刚」: %q", lines[1])
	}
	if !strings.Contains(lines[1], "HK") {
		t.Fatalf("两族的地区可能不同，各自都要显示: %q", lines[1])
	}
}

// TestMenuExitLinesSingleStack 覆盖单栈隧道：不带的那一族**不该出现**。
//
// 凭空列一个永远为空的"出口 IPv6"等于制造一个假问题 ——
// 真相是这条隧道本来就不带 v6。
func TestMenuExitLinesSingleStack(t *testing.T) {
	rt := &state.Runtime{Families: []int{state.FamilyV4}}

	lines := menuExitLines(rt)
	if len(lines) != 1 || !strings.Contains(lines[0], "IPv4") {
		t.Fatalf("单栈 v4 只该列一行 IPv4，实得 %q", lines)
	}
	if !strings.Contains(lines[0], "还没体检过") {
		t.Fatalf("没有值时该明说，实得 %q", lines[0])
	}
}

// TestMenuStatusLinesIgnoresBrokenRuntime 是本条最要紧的性质：
// 运行态文件坏了（半截写入、手工改坏）不该让菜单打不开 ——
// 那正是用户需要菜单来"重来一次"的时候。
func TestMenuStatusLinesIgnoresBrokenRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp"`) // 截断的 JSON

	lines := newTestApp(t).menuStatusLines()
	if !strings.Contains(lines[0], "隧道未建立") {
		t.Fatalf("运行态读不出来时应退化成「隧道未建立」，实得 %q", lines[0])
	}
}

// TestMenuNamesAreHuman 钉住"菜单里不出现行话"这件事。
//
// 用户实测的原话是"内核 用户态，谁知道是什么" —— 这一条就是把那句话
// 变成可执行的断言：认得出的值必须是中文人话，认不出的值原样返回
// （宁可显示一个没翻译的词，也不显示空串）。
func TestMenuNamesAreHuman(t *testing.T) {
	if got := backendName("kernel"); got != "内核直通" {
		t.Fatalf("backendName(kernel) = %q", got)
	}
	if got := backendName("userspace"); got != "用户态兼容" {
		t.Fatalf("backendName(userspace) = %q", got)
	}
	if got := backendName("whatever"); got != "whatever" {
		t.Fatalf("认不出的后端应原样返回，实得 %q", got)
	}
	if got := modeName("global"); got != "全局接管" {
		t.Fatalf("modeName(global) = %q", got)
	}
	if got := modeName("non-global"); got != "非全局" {
		t.Fatalf("modeName(non-global) = %q", got)
	}
	if got := modeName(""); got != "" {
		t.Fatalf("modeName(\"\") = %q", got)
	}
	// 选项 8 / 9 的取舍说明也得能查到（查不到会返回 key 本身）。
	for _, key := range []string{
		msg.KeyMenuBackendHintKernel, msg.KeyMenuBackendHintUserspace,
		msg.KeyMenuModeHintGlobal, msg.KeyMenuModeHintNonGlobal,
	} {
		if got := msg.T(key); got == key {
			t.Fatalf("说明文案 %q 查不到", key)
		}
	}
}

// TestExitAgoJustNow：刚体检完不该显示"0 秒前实测"。
//
// 这不是吹毛求疵：`up` 一结束就打开菜单看状态是最常见的用法，
// 而那条路径恰恰总会命中"几秒钟前"。
func TestExitAgoJustNow(t *testing.T) {
	rt := &state.Runtime{ExitV4: &state.Exit{IP: "104.28.211.105", Location: "JP", At: time.Now()}}
	if got := exitAgo(rt.ExitV4); !strings.Contains(got, "刚刚实测") {
		t.Fatalf("刚体检完应显示「刚刚实测」，实得 %q", got)
	}
	// 没有时间戳（老运行态文件里没有 at）时也要能显示 IP，不能整句消失。
	if got := exitAgo(&state.Exit{IP: "1.2.3.4"}); got != "1.2.3.4" {
		t.Fatalf("没有时间戳时应只给 IP，实得 %q", got)
	}
	// 没有地区时不该留一个空括号（"1.2.3.4（）"看着像坏了）。
	if got := exitAgo(&state.Exit{IP: "1.2.3.4", At: time.Now()}); got != "1.2.3.4，刚刚实测" {
		t.Fatalf("没有地区时不该出现空括号，实得 %q", got)
	}
}

// TestHumanAge 覆盖"多久之前"的四档。不用 state.FormatAge 是因为它给的是
// Go 时长串（2m5s），塞进中文句子会变成"2m5s前实测"。
func TestHumanAge(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "0 秒"},
		{20 * time.Second, "20 秒"},
		{3 * time.Minute, "3 分钟"},
		{5 * time.Hour, "5 小时"},
		{50 * time.Hour, "2 天"},
	}
	for _, c := range cases {
		if got := humanAge(c.in); got != c.want {
			t.Fatalf("humanAge(%s) = %q，应为 %q", c.in, got, c.want)
		}
	}
}

// withStdinPipe 在测试期间把 os.Stdin 换成一个管道（模拟"没有终端"）。
//
// 这是本文件里唯一需要动 os.Stdin 的地方：判据本身就看 stdin 与 /dev/tty，
// 而 go test 的 stdin 现场（可能是 /dev/null、也可能接着真终端）不可控，
// 只能显式摆出"管道"这个现场。
func withStdinPipe(t *testing.T) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
		_ = w.Close()
	})
}

// TestStdinIsTerminalFalseForPipe：管道不是终端。
//
// 这一条挡的是"菜单把脚本挂住"：如果判据错了、管道也进菜单，
// 菜单会读 stdin 直到 EOF 才退出；而 `curl | bash` 那种场合 stdin 是脚本本身，
// 会被菜单当成一连串选择吃掉。
func TestStdinIsTerminalFalseForPipe(t *testing.T) {
	withStdinPipe(t)
	if stdinIsTerminal() {
		t.Fatal("stdin 是管道时不该判定为终端")
	}
}

// TestRunDefaultWithoutTTYPrintsUsage：没有终端时裸敲 `warpgo` 印用法（且不阻塞）。
//
// 用户看到的现场就是这一条的反面：在终端里敲 `warpgo` 结果弹出一屏用法。
// 现在两种现场分开 —— 有终端进菜单（见 TestMenuEntriesWellFormed 之外的真机验证），
// 没终端才是用法。
func TestRunDefaultWithoutTTYPrintsUsage(t *testing.T) {
	withStdinPipe(t)

	out, err := os.CreateTemp(t.TempDir(), "usage")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	a := newTestApp(t)
	a.stdout = out

	if code := a.runDefault(); code != exitOK {
		t.Fatalf("裸敲且无终端应返回 0，实得 %d", code)
	}
	body, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "用法:") {
		t.Fatalf("应打印用法，实得:\n%s", body)
	}
}

// TestMenuEntriesWellFormed 把菜单表的几条不变量钉住。
//
// 这些是"加一项时最容易踩"的地方：选项号重号 / 漏号、静态项与动态项都没写
// （点下去什么都不做）、分组标题断成两截（同一组被别的组插开，菜单看着像坏了一半）。
//
// 另外还钉住"只读项不用确认"这条：确认问得太滥会让人闭着眼敲 y，
// 那等于把这道拦又拆掉了。
func TestMenuEntriesWellFormed(t *testing.T) {
	entries := menuEntries()
	if len(entries) == 0 {
		t.Fatal("菜单是空的")
	}

	// 只读项：执行它们不改机器状态，所以不确认。这张白名单是**穷举**的 ——
	// 新加一项忘了想"要不要确认"，会在这里红掉，而不是悄悄放行。
	readOnly := map[string]bool{"2": true, "7": true, "16": true, "19": true, "21": true}

	seen := map[string]bool{}
	section := ""
	closed := map[string]bool{}
	for i, e := range entries {
		want := strconv.Itoa(i + 1)
		if e.choice != want {
			t.Fatalf("第 %d 项的选项号应是 %q，实得 %q（选项号必须连续、从 1 起）", i+1, want, e.choice)
		}
		if seen[e.choice] {
			t.Fatalf("选项号 %q 重复", e.choice)
		}
		seen[e.choice] = true

		if e.section == "" {
			t.Fatalf("第 %d 项没写分组", i+1)
		}
		if e.section != section {
			if closed[e.section] {
				t.Fatalf("分组 %q 被拆成了两截", e.section)
			}
			closed[section] = true
			section = e.section
		}

		// 静态项要有 argv，动态项要有 decide —— 两者都空的话这个选项点了没反应。
		if len(e.argv) == 0 && e.decide == nil {
			t.Fatalf("选项 %q 既没有 argv 也没有 decide", e.choice)
		}
		if len(e.argv) > 0 && e.decide != nil {
			t.Fatalf("选项 %q 同时写了 argv 和 decide，不知道听谁的", e.choice)
		}

		if e.mutates == readOnly[e.choice] {
			if e.mutates {
				t.Fatalf("选项 %q 是只读的，不该要求确认（mutates 应为 false）", e.choice)
			}
			t.Fatalf("选项 %q 会改机器状态，必须要求确认（mutates 应为 true）", e.choice)
		}

		// 文案必须是"查得到"的（不是 key 本身）—— 菜单上不许出现 menu.xxx。
		for _, key := range []string{e.section, e.label} {
			if got := msg.T(key); got == key || got == "" {
				t.Fatalf("选项 %q 的文案 key %q 查不到", e.choice, key)
			}
		}
	}
}

// TestMenuBuildTunnelEnablesKeepalive 钉住"建隧道 = 连保活一起装"这条约定。
//
// 理由（用户实测提的）：隧道起来之后没有任何东西看着它 —— 断了不会自愈，
// 机器重启也不会自己回来。菜单是绝大多数人的入口，所以"默认开启保活"这件事
// 必须落在这一张表上；哪天有人把它删了，这条测试会红，而不是等到线上隧道断了
// 才发现没人管。
//
// 同时要求**选项文案里提到保活**：那一行就是确认提问的题干，
// 用户按 y 之前得看得到自己要装的是什么（否则就是替人做了个没说的事）。
func TestMenuBuildTunnelEnablesKeepalive(t *testing.T) {
	for _, choice := range []string{"3", "4"} {
		e, ok := findMenuEntry(choice)
		if !ok {
			t.Fatalf("菜单里找不到选项 %q", choice)
		}
		if !hasArg(e.argv, "--keepalive") {
			t.Fatalf("选项 %q（%s）应带 --keepalive，实得 %q", choice, msg.T(e.label), e.argv)
		}
		if !strings.Contains(msg.T(e.label), "保活") {
			t.Fatalf("选项 %q 的文案该说清会装保活，实得 %q", choice, msg.T(e.label))
		}
	}

	// 反向：其余项不该偷偷带上它（"恢复了隧道顺手装个服务"是最不该有的意外）。
	for _, e := range menuEntries() {
		if e.choice == "3" || e.choice == "4" {
			continue
		}
		if hasArg(e.argv, "--keepalive") {
			t.Fatalf("选项 %q 不该带 --keepalive：%q", e.choice, e.argv)
		}
	}
}

func hasArg(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// TestConfirmDefaultsToNo 是"按错保护"本身的性质：
// 只有明确的 y / yes 才放行；回车、别的字母、读不到输入，一律当取消。
func TestConfirmDefaultsToNo(t *testing.T) {
	e := menuEntry{choice: "12", label: msg.KeyMenuDown}
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{" yes \n", true},
		{"y", true}, // 最后一行没有换行符也要收下（管道输入常见）
		{"\n", false},
		{"n\n", false},
		{"no\n", false},
		{"1\n", false},
		{"", false}, // stdin 直接关闭 = 用户已经走了：不执行、也不报错
	}
	for _, c := range cases {
		a := newTestApp(t)
		if got := a.confirm(e, bufio.NewReader(strings.NewReader(c.in))); got != c.want {
			t.Fatalf("输入 %q 时应为 %v，实得 %v", c.in, c.want, got)
		}
	}
}
