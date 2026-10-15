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
// 这两行必须都能打出来 —— 恰恰是"隧道没起来、账户也没注册"的时候，
// 用户最需要看到菜单（而不是被一个报错挡住）。
func TestMenuStatusLinesOnCleanMachine(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())

	lines := newTestApp(t).menuStatusLines()
	if len(lines) != 1 {
		t.Fatalf("隧道没起来时不该有第二行（出口 IP 无从谈起），实得 %d 行: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "隧道未建立") {
		t.Fatalf("没有运行态时应显示「隧道未建立」，实得 %q", lines[0])
	}
	if !strings.Contains(lines[0], "未注册") {
		t.Fatalf("没有账户时应显示「未注册」，实得 %q", lines[0])
	}
}

// TestMenuStatusLinesWithRuntimeAndAccount 是本条最要紧的正面用例：
// 状态行必须**分两行**，且第二行是**公网出口 IP**，不是隧道接口地址。
func TestMenuStatusLinesWithRuntimeAndAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)

	writeStateFile(t, state.RuntimeFile, `{
		"if_name": "warp",
		"mode": "non-global",
		"backend": "kernel",
		"mtu": 1280,
		"table": 51820,
		"families": [4, 6],
		"addrs": ["172.16.0.2/32", "2606:4700:110::1/128"],
		"endpoint": "engage.cloudflareclient.com:2408",
		"exit_ip": "104.28.211.105",
		"exit_loc": "JP",
		"exit_checked_at": "`+time.Now().Add(-3*time.Minute).UTC().Format(time.RFC3339)+`",
		"up_since": "2026-10-15T00:00:00Z"
	}`)
	writeStateFile(t, state.AccountFile, `{
		"id": "abc", "account_type": "free",
		"private_key": "x", "peer_public_key": "y",
		"endpoint": "engage.cloudflareclient.com:2408"
	}`)

	lines := newTestApp(t).menuStatusLines()
	if len(lines) != 2 {
		t.Fatalf("隧道在线时应有 2 行（第二行是出口 IP），实得 %d 行: %q", len(lines), lines)
	}
	for _, want := range []string{"隧道 warp", "模式 非全局", "后端 内核直通", "账户 free"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("第一行里应包含 %q，实得 %q", want, lines[0])
		}
	}
	// 第二行：公网出口 IP + 地区 + "多久前实测"。
	for _, want := range []string{"出口 IP 104.28.211.105", "JP", "3 分钟前实测"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("第二行里应包含 %q，实得 %q", want, lines[1])
		}
	}
	// 隧道内部地址（172.16.0.2 / 2606:4700:110::）**不该**出现在菜单里：
	// 它不是"我的 IP"，拿它去判断"IP 变没变"只会得出错误结论。
	for _, line := range lines {
		if strings.Contains(line, "172.16.0.2") || strings.Contains(line, "2606:4700:110::1") {
			t.Fatalf("菜单状态行里不该出现隧道内部地址: %q", line)
		}
	}
	// 运行态里的原始值不该原样露出来（用户看不懂 kernel / non-global）。
	if strings.Contains(lines[0], "kernel") || strings.Contains(lines[0], "non-global") {
		t.Fatalf("第一行应显示人话而不是运行态原值: %q", lines[0])
	}
}

// TestMenuStatusLinesExitUnknown：隧道在线但从没体检过 —— 第二行要说清
// "还不知道"，而不是留空（留空看着像这一行坏了，也像"没有出口 IP"）。
func TestMenuStatusLinesExitUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp", "mode": "non-global", "backend": "userspace"}`)

	lines := newTestApp(t).menuStatusLines()
	if len(lines) != 2 {
		t.Fatalf("实得 %d 行: %q", len(lines), lines)
	}
	if !strings.Contains(lines[1], "还没体检过") {
		t.Fatalf("第二行应说明出口 IP 还不知道，实得 %q", lines[1])
	}
	if !strings.Contains(lines[0], "后端 用户态兼容") {
		t.Fatalf("后端应翻成人话，实得 %q", lines[0])
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

// TestExitSummaryJustNow：刚体检完不该显示"0 秒前实测"。
//
// 这不是吹毛求疵：`up` 一结束就打开菜单看状态是最常见的用法，
// 而那条路径恰恰总会命中"几秒钟前"。
func TestExitSummaryJustNow(t *testing.T) {
	rt := &state.Runtime{ExitIP: "104.28.211.105", ExitLoc: "JP", ExitAt: time.Now()}
	if got := exitSummary(rt); !strings.Contains(got, "刚刚实测") {
		t.Fatalf("刚体检完应显示「刚刚实测」，实得 %q", got)
	}
	if got := exitSummary(&state.Runtime{}); got != "" {
		t.Fatalf("没有值时应返回空串（由调用方表达「没有」），实得 %q", got)
	}
	// 没有时间戳（老运行态文件）时也要能显示 IP，不能整句消失。
	rt2 := &state.Runtime{ExitIP: "1.2.3.4"}
	if got := exitSummary(rt2); got != "1.2.3.4" {
		t.Fatalf("没有时间戳时应只给 IP，实得 %q", got)
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
