package cfclient

import (
	"strings"
	"testing"
)

func TestParseConnState(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want ConnState
	}{
		{"上游格式", "Status update: Connected\n", ConnConnected},
		{"上游格式-断开", "Status update: Disconnected\n", ConnDisconnected},
		{"连接中", "Status update: Connecting\n", ConnConnecting},
		{"未注册", "Status update: Registration missing\n", ConnNoRegistration},
		// Disconnected 里含 connected —— 判定顺序错了就会把它读成"已连接"，
		// 这条用例就是钉住那个顺序的。
		{"断开不能读成已连接", "Disconnected", ConnDisconnected},
		{"未注册优先于断开", "Registration missing (Disconnected)", ConnNoRegistration},
		{"认不出来", "something else entirely", ConnUnknown},
		{"空输出", "", ConnUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseConnState(c.out); got != c.want {
				t.Fatalf("ParseConnState(%q) = %q, 期望 %q", c.out, got, c.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want Mode
		ok   bool
	}{
		{"warp 模式", "Mode: Warp\n", ModeWarp, true},
		{"proxy 模式", "Mode: WarpProxy\n", ModeProxy, true},
		{"小写", "mode: warp\n", ModeWarp, true},
		{"缩进", "  Mode: WarpProxy  \n", ModeProxy, true},
		{"带其他行", "Some setting: 1\nMode: Warp\nProxy port: 40000\n", ModeWarp, true},
		// 真实形态（warp-cli 2026.8.2110.0）：前面有一列"取值来源"，
		// 模式名后面还跟着端口。
		{"真实格式-proxy", "(user set)\tMode: WarpProxy on port 40000\n", ModeProxy, true},
		{"真实格式-warp", "(user set)\tMode: Warp on port 0\n", ModeWarp, true},
		{"真实格式-完整输出", "Merged configuration:\n(not set)\tCompliance Environment: Normal\n(user set)\tMode: WarpProxy on port 25344\n(default)\tPacket Tunnel: false\n", ModeProxy, true},
		{"tunnel_only 也算建隧道", "Mode: tunnel_only\n", ModeWarp, true},
		{"doh 不建隧道", "Mode: doh\n", "", false},
		{"没有模式行", "Proxy port: 40000\n", "", false},
		{"空输出", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseMode(c.out)
			if ok != c.ok || got != c.want {
				t.Fatalf("ParseMode(%q) = (%q, %v), 期望 (%q, %v)", c.out, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestParseModeOrdering(t *testing.T) {
	// WarpProxy 里含 Warp：先匹配 proxy 才不会读错。
	if got, _ := ParseMode("Mode: WarpProxy"); got != ModeProxy {
		t.Fatalf("WarpProxy 应读成 proxy，实得 %q", got)
	}
}

func TestParseProxyPort(t *testing.T) {
	cases := []struct {
		out  string
		want int
		ok   bool
	}{
		{"Mode: WarpProxy\nProxy port: 40000\n", 40000, true},
		{"  Proxy port: 25344  \n", 25344, true},
		// 真实形态：端口跟在模式名后面。
		{"(user set)\tMode: WarpProxy on port 25344\n", 25344, true},
		{"Proxy port: notanumber\n", 0, false},
		{"Mode: Warp\n", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseProxyPort(c.out)
		if ok != c.ok || got != c.want {
			t.Fatalf("ParseProxyPort(%q) = (%d, %v), 期望 (%d, %v)", c.out, got, ok, c.want, c.ok)
		}
	}
}

func TestParseAccountType(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		// 真实形态（warp-cli 2026.8.2110.0）：`Account type: Free`。
		{"Account type: Free\nID: abc\nDevice ID: abc\n", "Free"},
		{"type: Limited\n", "Limited"},
		{"  Account Type: Teams  \n", "Teams"},
		// 同一段输出里的 `Account ID:` 不能被误当成类型。
		{"Account ID: 8193e4a1\nAccount type: Free\n", "Free"},
		{"什么都没有", ""},
	}
	for _, c := range cases {
		if got := ParseAccountType(c.out); got != c.want {
			t.Fatalf("ParseAccountType(%q) = %q, 期望 %q", c.out, got, c.want)
		}
	}
}

func TestAccountLabel(t *testing.T) {
	cases := map[string]string{
		"Limited": "WARP+",
		"limited": "WARP+",
		"Free":    "Free",
		// `-j` 给的是小写。
		"free":  "Free",
		"Teams": "Teams",
		"team":  "Teams",
		"":      "",
	}
	for in, want := range cases {
		if got := AccountLabel(in); got != want {
			t.Fatalf("AccountLabel(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 下面三条钉住 `-j` 的结构化解析 —— 它们是首选路径，
// 真实取值来自 warp-cli 2026.8.2110.0。

func TestParseStatusJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ConnState
		ok   bool
	}{
		{"已连接", `{"status":"Connected","reason":"NetworkHealthy"}`, ConnConnected, true},
		{"已断开", `{"status":"Disconnected","reason":"Manual"}`, ConnDisconnected, true},
		{"连接中", `{"status":"Connecting","reason":""}`, ConnConnecting, true},
		{"未注册", `{"status":"RegistrationMissing"}`, ConnNoRegistration, true},
		{"不是 JSON", `Status update: Connected`, ConnUnknown, false},
		{"空对象", `{}`, ConnUnknown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseStatusJSON([]byte(c.body))
			if ok != c.ok || got != c.want {
				t.Fatalf("ParseStatusJSON(%q) = (%q, %v), 期望 (%q, %v)", c.body, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestParseSettingsJSON(t *testing.T) {
	body := `{"settings":{"always_on":true,"operation_mode":"proxy","proxy_port":40000,"split_tunnel_mode":"exclude"}}`
	mode, port, ok := ParseSettingsJSON([]byte(body))
	if !ok || mode != ModeProxy || port != 40000 {
		t.Fatalf("ParseSettingsJSON = (%q, %d, %v)，期望 (proxy, 40000, true)", mode, port, ok)
	}

	body = `{"settings":{"operation_mode":"warp","proxy_port":0}}`
	if mode, _, ok := ParseSettingsJSON([]byte(body)); !ok || mode != ModeWarp {
		t.Fatalf("warp 模式解析失败: (%q, %v)", mode, ok)
	}

	for _, bad := range []string{`{}`, `not json`, `{"settings":{"operation_mode":"doh"}}`} {
		if _, _, ok := ParseSettingsJSON([]byte(bad)); ok {
			t.Fatalf("%q 不该被解析成功", bad)
		}
	}
}

func TestParseRegistrationJSON(t *testing.T) {
	body := `{"id":"abc","device_id":"abc","managed":false,"account":{"type":"free","id":"x","license":"y"}}`
	got, ok := ParseRegistrationJSON([]byte(body))
	if !ok || got != "free" {
		t.Fatalf("ParseRegistrationJSON = (%q, %v)，期望 (free, true)", got, ok)
	}
	if _, ok := ParseRegistrationJSON([]byte(`{"account":{}}`)); ok {
		t.Fatal("没有 type 字段时不该算解析成功")
	}
	if _, ok := ParseRegistrationJSON([]byte(`Account type: Free`)); ok {
		t.Fatal("文本输出不该被 JSON 解析器接受")
	}
}

func TestParseModeName(t *testing.T) {
	good := map[string]Mode{
		"warp": ModeWarp, "interface": ModeWarp, "l": ModeWarp,
		"proxy": ModeProxy, "socks5": ModeProxy, "socks": ModeProxy, "c": ModeProxy,
		"  WARP  ": ModeWarp,
	}
	for in, want := range good {
		got, err := ParseModeName(in)
		if err != nil || got != want {
			t.Fatalf("ParseModeName(%q) = (%q, %v), 期望 (%q, nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "masque", "kernel"} {
		if _, err := ParseModeName(bad); err == nil {
			t.Fatalf("ParseModeName(%q) 应当报错", bad)
		}
	}
}

func TestValidatePort(t *testing.T) {
	for _, ok := range []int{1024, 40000, 65535} {
		if err := ValidatePort(ok); err != nil {
			t.Fatalf("端口 %d 应当通过，实得 %v", ok, err)
		}
	}
	for _, bad := range []int{0, 80, 1023, 65536, -1} {
		if err := ValidatePort(bad); err == nil {
			t.Fatalf("端口 %d 应当被拒绝", bad)
		}
	}
}

func TestCodenameFrom(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"标准 Debian",
			"PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_CODENAME=bookworm\n",
			"bookworm",
		},
		{
			"带引号",
			"VERSION_CODENAME=\"trixie\"\n",
			"trixie",
		},
		{
			"新发行版回退",
			"VERSION_CODENAME=resolute\n",
			"noble",
		},
		{
			"只有 UBUNTU_CODENAME",
			"UBUNTU_CODENAME=noble\n",
			"noble",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CodenameFrom(c.body)
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if got != c.want {
				t.Fatalf("CodenameFrom() = %q, 期望 %q", got, c.want)
			}
		})
	}

	if _, err := CodenameFrom("PRETTY_NAME=\"x\"\n"); err == nil {
		t.Fatal("读不到 codename 时应当报错，而不是猜一个")
	}
}

func TestMapCodename(t *testing.T) {
	if got := MapCodename("RESOLUTE"); got != "noble" {
		t.Fatalf("大小写不敏感的回退失败: %q", got)
	}
	if got := MapCodename("bookworm"); got != "bookworm" {
		t.Fatalf("不该改动的代号被改了: %q", got)
	}
}

func TestSourceListContent(t *testing.T) {
	got := SourceListContent("/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg", "bookworm")
	want := "deb [signed-by=/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg] https://pkg.cloudflareclient.com/ bookworm main\n"
	if got != want {
		t.Fatalf("SourceListContent() =\n%q\n期望\n%q", got, want)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("apt 源文件必须以换行结尾，否则 apt 会给出难懂的告警")
	}
}
