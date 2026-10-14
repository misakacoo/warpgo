package cloudflare

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustDevice(t *testing.T, raw string) *Device {
	t.Helper()
	var d Device
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestFromDeviceHappyPath(t *testing.T) {
	dev := mustDevice(t, sampleRegisterResponse)
	acct, err := FromDevice(dev, "PRIVATEKEY", "PUBKEY")
	if err != nil {
		t.Fatal(err)
	}

	if acct.ID != dev.ID {
		t.Errorf("ID = %q", acct.ID)
	}
	if acct.Token != "tok-abcdef" {
		t.Errorf("Token = %q", acct.Token)
	}
	if acct.PrivateKey != "PRIVATEKEY" || acct.PublicKey != "PUBKEY" {
		t.Error("本地密钥没有被带进账户")
	}
	if acct.AccountID != "acc-0001" || acct.AccountType != "free" {
		t.Errorf("账户信息不对: %+v", acct)
	}

	// 地址要补上掩码 —— wg-quick 需要 CIDR。
	if acct.AddrV4 != "172.16.0.2/32" {
		t.Errorf("AddrV4 = %q", acct.AddrV4)
	}
	if acct.AddrV6 != "2606:4700:110:8a1b:c1e2:9f3d:1a2b:3c4d/128" {
		t.Errorf("AddrV6 = %q", acct.AddrV6)
	}

	// 端点必须取 host（v4/v6 的端口是 0，用不得）。
	if acct.Endpoint != "engage.cloudflareclient.com:2408" {
		t.Errorf("Endpoint = %q", acct.Endpoint)
	}

	// reserved 来自 client_id。
	if acct.Reserved != (Reserved{203, 199, 161}) || !acct.HasReserved() {
		t.Errorf("Reserved = %v", acct.Reserved)
	}

	if got := acct.Addresses(); len(got) != 2 || got[0] != acct.AddrV4 || got[1] != acct.AddrV6 {
		t.Errorf("Addresses() 顺序应为 v4 → v6，实得 %v", got)
	}
	if !acct.HasV4() || !acct.HasV6() {
		t.Error("HasV4/HasV6 判错")
	}
}

func TestFromDeviceRejectsIncompleteResponses(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"缺 id", `{"token":"t","config":{"peers":[{"public_key":"p","endpoint":{"host":"h:1"}}]}}`},
		{"缺 token", `{"id":"x","config":{"peers":[{"public_key":"p","endpoint":{"host":"h:1"}}]}}`},
		{"缺 peers", `{"id":"x","token":"t","config":{"peers":[]}}`},
		{"peer 缺公钥", `{"id":"x","token":"t","config":{"peers":[{"endpoint":{"host":"h:1"}}]}}`},
		{"peer 缺端点", `{"id":"x","token":"t","config":{"peers":[{"public_key":"p","endpoint":{}}]}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dev := mustDevice(t, c.raw)
			if _, err := FromDevice(dev, "priv", "pub"); err == nil {
				t.Fatal("应当报错 —— 宁可注册明确失败，也不要存下一份用不起来的账户")
			}
		})
	}

	if _, err := FromDevice(nil, "priv", "pub"); err == nil {
		t.Fatal("nil 设备应报错")
	}
}

// client_id 缺失不该让整次注册失败 —— 有些环境就是不返回它，
// 只是 reserved 会退化成全零（在那个地区可能连不上，但要给用户一个可诊断的状态）。
func TestFromDeviceToleratesMissingClientID(t *testing.T) {
	dev := mustDevice(t, `{
	  "id":"x","token":"t",
	  "config":{"peers":[{"public_key":"p","endpoint":{"host":"h:2408"}}]}
	}`)
	acct, err := FromDevice(dev, "priv", "pub")
	if err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if acct.HasReserved() {
		t.Errorf("没有 client_id 时 reserved 应为全零，实得 %v", acct.Reserved)
	}
}

func TestAccountSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")

	dev := mustDevice(t, sampleRegisterResponse)
	orig, err := FromDevice(dev, "PRIVATEKEY", "PUBKEY")
	if err != nil {
		t.Fatal(err)
	}
	if err := orig.Save(path); err != nil {
		t.Fatal(err)
	}

	// 私钥落盘必须 0600。
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("账户文件权限应为 0600，实得 %o", perm)
	}

	back, err := LoadAccount(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != orig.ID || back.Token != orig.Token || back.PrivateKey != orig.PrivateKey {
		t.Errorf("往返后关键字段不一致")
	}
	if back.Reserved != orig.Reserved {
		t.Errorf("reserved 往返后不一致: %v vs %v", back.Reserved, orig.Reserved)
	}
}

func TestLoadAccountMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	_, err := LoadAccount(path)
	// "还没注册" 必须与 "读坏了" 可区分：前者 ErrNoAccount，后者是别的错误。
	if !errors.Is(err, ErrNoAccount) {
		t.Fatalf("应返回 ErrNoAccount，实得 %v", err)
	}
}

func TestLoadAccountCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadAccount(path)
	if err == nil {
		t.Fatal("坏文件应报错")
	}
	if errors.Is(err, ErrNoAccount) {
		t.Fatal("坏文件不该被当成「还没注册」")
	}
}

// 脱敏视图是"默认安全的打印"的实现方式，必须真的不含机密。
func TestRedactedHidesSecrets(t *testing.T) {
	dev := mustDevice(t, sampleRegisterResponse)
	acct, err := FromDevice(dev, "SUPER-SECRET-PRIVATE-KEY", "PUBKEY")
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(acct.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	view := string(b)
	for _, secret := range []string{"SUPER-SECRET-PRIVATE-KEY", acct.Token, acct.FCMToken} {
		if secret != "" && strings.Contains(view, secret) {
			t.Fatalf("脱敏视图里出现了机密 %q: %s", secret, view)
		}
	}
	if !strings.Contains(view, `"has_private_key":true`) {
		t.Error("脱敏视图应报告私钥是否存在（让人知道账户是完整的）")
	}
}
