package netcfg

import (
	"net/netip"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("解析 %q: %v", s, err)
	}
	return a
}

func TestParseStack(t *testing.T) {
	cases := []struct {
		in   string
		want Stack
		ok   bool
	}{
		// 空值 = 双栈：`up` 不带 --stack 时走的就是这条路。
		{"", StackBoth, true},
		{"d", StackBoth, true},
		{"both", StackBoth, true},
		{"DUAL", StackBoth, true},
		{"4", StackV4, true},
		{"v4", StackV4, true},
		{"IPv4", StackV4, true},
		{"6", StackV6, true},
		{"v6", StackV6, true},
		{"ipv6", StackV6, true},
		{"  4  ", StackV4, true},
		{"5", "", false},
		{"v5", "", false},
		{"yes", "", false},
	}
	for _, c := range cases {
		got, err := ParseStack(c.in)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("ParseStack(%q) = (%q, %v)，期望 (%q, ok=%v)", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestStackFamiliesAndAllows(t *testing.T) {
	// 双栈的 Families() 是 nil —— 表示"不限制"，而不是"空集"。
	// 这个区别要紧：调用方按 nil 走"原样返回"的快路径。
	if got := StackBoth.Families(); got != nil {
		t.Errorf("双栈应当返回 nil（不限制），实得 %v", got)
	}
	if got := StackV4.Families(); len(got) != 1 || got[0] != FamilyV4 {
		t.Errorf("StackV4.Families() = %v", got)
	}
	if got := StackV6.Families(); len(got) != 1 || got[0] != FamilyV6 {
		t.Errorf("StackV6.Families() = %v", got)
	}

	checks := []struct {
		stack Stack
		fam   int
		want  bool
	}{
		{StackBoth, FamilyV4, true},
		{StackBoth, FamilyV6, true},
		{StackV4, FamilyV4, true},
		{StackV4, FamilyV6, false},
		{StackV6, FamilyV4, false},
		{StackV6, FamilyV6, true},
		// 空值按双栈算。
		{"", FamilyV4, true},
		{"", FamilyV6, true},
	}
	for _, c := range checks {
		if got := c.stack.Allows(c.fam); got != c.want {
			t.Errorf("Stack(%q).Allows(%d) = %v，期望 %v", c.stack, c.fam, got, c.want)
		}
	}
}

func TestStackNormalizeAndString(t *testing.T) {
	if got := Stack("").Normalize(); got != StackBoth {
		t.Errorf("空值应当归一成双栈，实得 %q", got)
	}
	if got := StackV6.Normalize(); got != StackV6 {
		t.Errorf("非空值不该被改：%q", got)
	}
	// 界面上永远显示一个确定的取值，不会出现空字符串。
	if got := Stack("").String(); got != "both" {
		t.Errorf("空值的 String() = %q，期望 both", got)
	}
	if got := StackV4.String(); got != "4" {
		t.Errorf("StackV4.String() = %q", got)
	}
}

func TestFamilyOfAddr(t *testing.T) {
	cases := map[string]int{
		"172.16.0.2":        FamilyV4,
		"10.0.0.1":          FamilyV4,
		"2606:4700:110::1":  FamilyV6,
		"::1":               FamilyV6,
		"::ffff:172.16.0.2": FamilyV6, // v4-mapped 在 netip 里算 v6，除非 Unmap
	}
	for in, want := range cases {
		a := mustAddr(t, in)
		if got := FamilyOfAddr(a); got != want {
			t.Errorf("FamilyOfAddr(%s) = %d，期望 %d", in, got, want)
		}
	}
}
