package gai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Debian 12 上 /etc/gai.conf 的真实形状：一整份被注释掉的 RFC 6724 表。
const stockConf = `# Configuration for getaddrinfo(3).
#
# So add 'label' definition for each of your addresses.  If you do not have
# any, you probably do not need this file.
#
#precedence  ::1/128       50
#precedence  ::/0          40
#precedence  2002::/16     30
#precedence ::ffff:0:0/96  10
#
# For sites which prefer IPv4 connections change the last line to
#
#precedence ::ffff:0:0/96  100
`

// countManaged 数出内容里**生效的**受管行有几条（注释掉的不算）。
func countManaged(content []byte, mode Mode) int {
	n := 0
	for _, line := range strings.Split(string(content), "\n") {
		if managedKind(line) == mode {
			n++
		}
	}
	return n
}

// 原厂文件里那行被注释掉的 v4 写法**不算受管行**：
// 它是 glibc 自带的文档，删掉它才是不该发生的事。
func TestCommentedLineIsNotManaged(t *testing.T) {
	if got := DetectContent([]byte(stockConf)); got != ModeDefault {
		t.Errorf("全是注释的文件应判定为默认，实得 %q", got)
	}
	if n := countManaged([]byte(stockConf), ModeV4); n != 0 {
		t.Errorf("注释行不该被当成受管行，实得 %d 条", n)
	}
	// 设置 v4 之后，注释那行仍在、生效的那行多一条。
	next := Rewrite([]byte(stockConf), ModeV4)
	if n := countManaged(next, ModeV4); n != 1 {
		t.Errorf("应当正好一条生效的 v4 行，实得 %d", n)
	}
	if !strings.Contains(string(next), "#precedence ::ffff:0:0/96  100") {
		t.Error("注释行被误删了")
	}
}

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"4":       ModeV4,
		"6":       ModeV6,
		"d":       ModeDefault,
		"IPv4":    ModeV4,
		" ipv6 ":  ModeV6,
		"default": ModeDefault,
	}
	for in, want := range cases {
		got, err := ParseMode(in)
		if err != nil {
			t.Errorf("ParseMode(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %q，期望 %q", in, got, want)
		}
	}

	// 空串必须报错：命令行漏参数与"明确要求默认"是两件事。
	for _, bad := range []string{"", "  ", "5", "v5", "yes"} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("ParseMode(%q) 应该报错", bad)
		}
	}
}

func TestDetectContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Mode
	}{
		{"原厂文件（全是注释）→ 默认", stockConf, ModeDefault},
		{"只有 v4 行", stockConf + LineV4 + "\n", ModeV4},
		{"只有 v6 行", stockConf + LineV6 + "\n", ModeV6},
		{"非注释行里提到 v4 但那不是指令", "#" + LineV4 + "\n", ModeDefault},
		{"两行都在 → 报默认（让 Set 去收敛）", stockConf + LineV4 + "\n" + LineV6 + "\n", ModeDefault},
		{"多余空格也能认出来", "precedence     ::ffff:0:0/96      100\n", ModeV4},
		{"行尾注释也能认出来", "label 2002::/16 2   # 上游的写法\n", ModeV6},
		{"空文件", "", ModeDefault},
	}
	for _, tc := range cases {
		if got := DetectContent([]byte(tc.content)); got != tc.want {
			t.Errorf("%s: DetectContent = %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// 只动自己那两行：别的字节一个都不许变。
//
// 这是整个包最重要的一条不变量 —— 用户（或别的工具）写在 gai.conf 里的东西
// 一旦被我们顺手重排，就再也说不清是谁改的了。
func TestRewriteKeepsEverythingElse(t *testing.T) {
	cur := stockConf + "\n# 我自己的备注\nprecedence ::1/128 50\n"
	next := string(Rewrite([]byte(cur), ModeV4))

	if !strings.HasSuffix(next, LineV4+"\n") {
		t.Fatalf("末尾应追加受管行，实得:\n%s", next)
	}
	body := strings.TrimSuffix(next, LineV4+"\n")
	if body != cur {
		t.Errorf("除追加那一行外内容必须逐字节一致\n原文:\n%q\n实得:\n%q", cur, body)
	}
}

// 原文件不以换行结尾时，追加前必须补一个换行 —— 否则两行会粘成一行，
// 结果是"既没生效、又在文件里留下一条坏行"。
func TestRewriteHandlesMissingTrailingNewline(t *testing.T) {
	next := string(Rewrite([]byte("# 没有结尾换行"), ModeV4))
	want := "# 没有结尾换行\n" + LineV4 + "\n"
	if next != want {
		t.Errorf("实得 %q，期望 %q", next, want)
	}
}

// 切回默认 = 删掉受管行，于是"还原"是天然幂等的（不需要备份文件）。
//
// 唯一允许的偏差：原文件没有结尾换行时，往返之后会补上一个 ——
// 因为我们追加那一行时必须补换行，而删掉它时无从知道当初那个换行是不是我们补的。
// 这正是"没有备份文件"换来的代价，值得说清楚而不是假装不存在。
func TestRewriteRoundTrip(t *testing.T) {
	for _, content := range []string{stockConf, "# 短文件\n", "# 无尾换行"} {
		for _, mode := range []Mode{ModeV4, ModeV6, ModeDefault} {
			want := content
			// 只有"真的追加过一行"的那两种模式才会补上缺失的结尾换行；
			// 切默认时不追加任何东西，因此这种情况是严格逐字节还原。
			if mode != ModeDefault && !strings.HasSuffix(want, "\n") {
				want += "\n"
			}
			got := string(Rewrite(Rewrite([]byte(content), mode), ModeDefault))
			if got != want {
				t.Errorf("原始 %q 经 %q 再回默认后 = %q，期望 %q", content, mode, got, want)
			}
		}
	}
}

func TestRewriteEmptyContent(t *testing.T) {
	// 空内容 + 默认 → 不做任何事（nil 会让 Set 判定为"无需改动"）。
	if got := Rewrite(nil, ModeDefault); got != nil {
		t.Errorf("空文件切默认应返回 nil，实得 %q", got)
	}
	if got := string(Rewrite(nil, ModeV4)); got != LineV4+"\n" {
		t.Errorf("空文件切 v4 应只写一行，实得 %q", got)
	}
	// 只剩空行时切默认：内容变了（空行被保留），但受管行确实没了。
	got := string(Rewrite([]byte("\n\n"), ModeDefault))
	if strings.Contains(got, "precedence") {
		t.Errorf("不该有受管行: %q", got)
	}
}

func TestSetIdempotentAndRestores(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gai.conf")
	if err := os.WriteFile(path, []byte(stockConf), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(path)

	changed, err := m.Set(ModeV4)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("第一次设置应当报告改动")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if countManaged(got, ModeV4) != 1 {
		t.Errorf("文件里应有且只有一条生效的 v4 行:\n%s", got)
	}

	// 第二次必须是"零改动"，且内容一字不变。
	changed, err = m.Set(ModeV4)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("重复设置同一模式不该报告改动")
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(got) {
		t.Error("重复设置不应改变内容")
	}

	// 切到 6：v4 行必须消失（那才是真正让顺序回到默认的动作）。
	if _, err := m.Set(ModeV6); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 这里必须按"受管行"来判断，不能用 strings.Contains：
	// Debian 的原厂文件里本来就有一行**被注释掉的** v4 写法
	// （`#precedence ::ffff:0:0/96  100`），子串匹配会把它当成生效的配置。
	if countManaged(got, ModeV4) != 0 {
		t.Errorf("切到 6 之后不该再有生效的 v4 行:\n%s", got)
	}
	if countManaged(got, ModeV6) != 1 {
		t.Errorf("应当正好有一行 v6 行:\n%s", got)
	}
	if !strings.Contains(string(got), "#precedence ::ffff:0:0/96  100") {
		t.Errorf("被注释掉的原厂行不该被动过:\n%s", got)
	}

	// 回到默认：与最初逐字节相同。
	if _, err := m.Set(ModeDefault); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != stockConf {
		t.Errorf("回到默认后应与原始文件逐字节一致\n原始:\n%q\n实得:\n%q", stockConf, got)
	}

	mode, found, err := m.Detect()
	if err != nil || !found {
		t.Fatalf("Detect: mode=%q found=%v err=%v", mode, found, err)
	}
	if mode != ModeDefault {
		t.Errorf("Detect = %q，期望默认", mode)
	}
}

// 文件不存在时：先创建再写（Debian 上它总在，但别的地方不一定）。
func TestSetCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "gai.conf")
	m := New(path)
	if _, err := m.Set(ModeV6); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != LineV6+"\n" {
		t.Errorf("实得 %q", b)
	}
}

// 权限要沿用原文件的，不要顺手改成别的。
func TestSetPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gai.conf")
	if err := os.WriteFile(path, []byte("# x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path).Set(ModeV4); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("权限被改成了 %v", fi.Mode().Perm())
	}
}

func TestModeLineAndValid(t *testing.T) {
	if Mode("x").Valid() {
		t.Error("未知取值不该有效")
	}
	if ModeDefault.Line() != "" {
		t.Errorf("默认模式不该有受管行，实得 %q", ModeDefault.Line())
	}
	if ModeV4.Line() != LineV4 || ModeV6.Line() != LineV6 {
		t.Error("受管行映射不对")
	}
}
