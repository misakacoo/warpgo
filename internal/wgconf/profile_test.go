package wgconf

import (
	"strings"
	"testing"

	"github.com/misakacoo/warpgo/internal/cloudflare"
)

// fixedKey 返回一个确定长度的密钥，值可预测 —— 快照测试不能依赖随机数。
func fixedKey(seed byte) Key {
	var k Key
	for i := range k {
		k[i] = seed + byte(i)
	}
	clampScalar(k[:])
	return k
}

func testAccount(v4, v6 string) *cloudflare.Account {
	return &cloudflare.Account{
		ID:            "dev-0001",
		Token:         "tok",
		PrivateKey:    fixedKey(1).String(),
		PublicKey:     fixedKey(101).String(),
		PeerPublicKey: fixedKey(201).String(),
		AddrV4:        v4,
		AddrV6:        v6,
		Endpoint:      "engage.cloudflareclient.com:2408",
		Reserved:      cloudflare.Reserved{203, 199, 161},
	}
}

func TestBuildDualStack(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "2606:4700:110::1/128")

	p, err := Build(acct, Options{Note: "note-line"})
	if err != nil {
		t.Fatal(err)
	}

	if len(p.Skipped) != 0 {
		t.Fatalf("双栈账户不该跳过任何族: %v", p.Skipped)
	}
	if p.MTU != DefaultMTU {
		t.Errorf("MTU 默认应为 %d，实得 %d", DefaultMTU, p.MTU)
	}
	if p.Keepalive != DefaultKeepalive {
		t.Errorf("Keepalive 默认应为 %d，实得 %d", DefaultKeepalive, p.Keepalive)
	}

	out := p.Render()

	// 逐行断言格式。配置文件差一个空格就可能让某个客户端解析失败，
	// 所以这里按行比对，而不是 Contains 一下"大概对"。
	wantLines := []string{
		"# note-line",
		"[Interface]",
		"PrivateKey = " + acct.PrivateKey,
		"Address = 172.16.0.2/32",
		"Address = 2606:4700:110::1/128",
		"DNS = 1.1.1.1, 2606:4700:4700::1111",
		"MTU = 1280",
		"# Reserved = 203, 199, 161",
		"",
		"[Peer]",
		"PublicKey = " + acct.PeerPublicKey,
		"AllowedIPs = 0.0.0.0/0, ::/0",
		"Endpoint = engage.cloudflareclient.com:2408",
		"PersistentKeepalive = 25",
	}
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) != len(wantLines) {
		t.Fatalf("行数不符：期望 %d 行，实得 %d 行\n---\n%s", len(wantLines), len(got), out)
	}
	for i := range wantLines {
		if got[i] != wantLines[i] {
			t.Fatalf("第 %d 行不符\n 期望 %q\n 实得 %q", i+1, wantLines[i], got[i])
		}
	}

	// 文件必须以换行结尾（很多解析器依赖它）。
	if !strings.HasSuffix(out, "\n") {
		t.Error("配置应以换行结尾")
	}
}

// reserved 必须落在 [Interface] 段内 —— 生态里客户端是逐行扫这一段的。
func TestReservedLinePlacement(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "")
	p, err := Build(acct, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := p.Render()
	iInterface := strings.Index(out, "[Interface]")
	iReserved := strings.Index(out, "# Reserved = ")
	iPeer := strings.Index(out, "[Peer]")
	if iInterface < 0 || iReserved < 0 || iPeer < 0 {
		t.Fatalf("缺少必要段落:\n%s", out)
	}
	if !(iInterface < iReserved && iReserved < iPeer) {
		t.Fatalf("`# Reserved` 应在 [Interface] 与 [Peer] 之间:\n%s", out)
	}

	// reserved 为全零时也要输出这一行（生态约定，缺了会让人怀疑没读到）。
	acct.Reserved = cloudflare.Reserved{}
	p2, err := Build(acct, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p2.Render(), "# Reserved = 0, 0, 0") {
		t.Errorf("reserved 为零时也应输出该行:\n%s", p2.Render())
	}
}

// 这条是本轮最容易出人命的一处：账户只有 IPv4 时，AllowedIPs 里绝不能出现 ::/0。
// 否则 v6 流量会被接进隧道、而接口上没有 v6 地址 —— 静默黑洞。
func TestSingleStackDoesNotLeakOtherFamily(t *testing.T) {
	v4Only := testAccount("172.16.0.2/32", "")

	t.Run("dual 请求遇到单栈账户会降级并记录", func(t *testing.T) {
		p, err := Build(v4Only, Options{Family: FamilyDual})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Skipped) != 1 || p.Skipped[0] != FamilyV6 {
			t.Fatalf("应记录跳过 IPv6，实得 %v", p.Skipped)
		}
		out := p.Render()
		if strings.Contains(out, "::/0") {
			t.Fatalf("账户没有 IPv6 地址，AllowedIPs 不该含 ::/0（会造成 v6 黑洞）:\n%s", out)
		}
		if strings.Contains(out, "2606:4700:4700::1111") {
			t.Fatalf("不该给 IPv6 DNS:\n%s", out)
		}
		if !strings.Contains(out, "AllowedIPs = 0.0.0.0/0") {
			t.Fatalf("IPv4 部分应保留:\n%s", out)
		}
	})

	t.Run("显式要 IPv6 而账户没有 → 报错", func(t *testing.T) {
		if _, err := Build(v4Only, Options{Family: FamilyV6}); err == nil {
			t.Fatal("应报错，而不是悄悄给一份没有 v6 的配置")
		}
	})

	t.Run("对称地，v6-only 账户不该出现 0.0.0.0/0", func(t *testing.T) {
		v6Only := testAccount("", "2606:4700:110::1/128")
		p, err := Build(v6Only, Options{Family: FamilyDual})
		if err != nil {
			t.Fatal(err)
		}
		out := p.Render()
		if strings.Contains(out, "0.0.0.0/0") {
			t.Fatalf("账户没有 IPv4 地址，AllowedIPs 不该含 0.0.0.0/0:\n%s", out)
		}
		if len(p.Skipped) != 1 || p.Skipped[0] != FamilyV4 {
			t.Fatalf("应记录跳过 IPv4，实得 %v", p.Skipped)
		}
	})
}

func TestBuildSingleFamilySelection(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "2606:4700:110::1/128")

	p4, err := Build(acct, Options{Family: FamilyV4})
	if err != nil {
		t.Fatal(err)
	}
	out4 := p4.Render()
	if strings.Contains(out4, "::/0") || strings.Contains(out4, "Address = 2606") {
		t.Fatalf("选了 IPv4 就不该出现 IPv6 内容:\n%s", out4)
	}
	if !strings.Contains(out4, "DNS = 1.1.1.1\n") {
		t.Fatalf("IPv4 的 DNS 不对:\n%s", out4)
	}

	p6, err := Build(acct, Options{Family: FamilyV6})
	if err != nil {
		t.Fatal(err)
	}
	out6 := p6.Render()
	if strings.Contains(out6, "0.0.0.0/0") || strings.Contains(out6, "Address = 172.16") {
		t.Fatalf("选了 IPv6 就不该出现 IPv4 内容:\n%s", out6)
	}
	if !strings.Contains(out6, "DNS = 2606:4700:4700::1111\n") {
		t.Fatalf("IPv6 的 DNS 不对:\n%s", out6)
	}
}

func TestBuildOverrides(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "")

	p, err := Build(acct, Options{
		MTU:       1380,
		DNS:       []string{"9.9.9.9", "149.112.112.112"},
		Keepalive: 15,
		Endpoint:  "custom.example.com:1234",
		TableOff:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := p.Render()
	for _, want := range []string{
		"MTU = 1380",
		"DNS = 9.9.9.9, 149.112.112.112",
		"PersistentKeepalive = 15",
		"Endpoint = custom.example.com:1234",
		"Table = off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("缺少 %q:\n%s", want, out)
		}
	}
}

func TestBuildKeepaliveDisabled(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "")
	p, err := Build(acct, Options{Keepalive: -1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Render(), "PersistentKeepalive") {
		t.Fatalf("负数应表示关闭保活:\n%s", p.Render())
	}
}

func TestBuildMTUBounds(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "")
	for _, mtu := range []int{100, 100000} {
		if _, err := Build(acct, Options{MTU: mtu}); err == nil {
			t.Errorf("MTU %d 超出范围应报错", mtu)
		}
	}
	for _, mtu := range []int{576, 1280, 1500} {
		if _, err := Build(acct, Options{MTU: mtu}); err != nil {
			t.Errorf("MTU %d 应被接受: %v", mtu, err)
		}
	}
}

func TestBuildRejectsBadAccount(t *testing.T) {
	if _, err := Build(nil, Options{}); err == nil {
		t.Error("nil 账户应报错")
	}

	bad := testAccount("172.16.0.2/32", "")
	bad.PrivateKey = "not-base64!!!"
	if _, err := Build(bad, Options{}); err == nil {
		t.Error("坏私钥应报错")
	}

	bad2 := testAccount("", "")
	if _, err := Build(bad2, Options{}); err == nil {
		t.Error("没有任何地址的账户应报错")
	}

	bad3 := testAccount("172.16.0.2/32", "")
	bad3.Endpoint = ""
	if _, err := Build(bad3, Options{}); err == nil {
		t.Error("没有端点的账户应报错")
	}
}

func TestParseFamily(t *testing.T) {
	cases := map[string]Family{
		"":      FamilyDual,
		"4":     FamilyV4,
		"6":     FamilyV6,
		"d":     FamilyDual,
		"v4":    FamilyV4,
		"IPv6":  FamilyV6,
		"dual":  FamilyDual,
		"  v6 ": FamilyV6,
	}
	for in, want := range cases {
		got, err := ParseFamily(in)
		if err != nil {
			t.Errorf("ParseFamily(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseFamily(%q) = %q，期望 %q", in, got, want)
		}
	}
	if _, err := ParseFamily("5"); err == nil {
		t.Error("不认识的地址族应报错")
	}
}

// Render 必须是确定性的：同样的输入必得同样的字节。
func TestRenderIsDeterministic(t *testing.T) {
	acct := testAccount("172.16.0.2/32", "2606:4700:110::1/128")
	a, err := Build(acct, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(acct, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Render() != b.Render() {
		t.Fatal("两次构建的渲染结果不一致")
	}
	if a.String() != a.Render() {
		t.Fatal("String() 应与 Render() 相同")
	}
}
