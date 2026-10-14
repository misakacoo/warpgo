package proxy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misakacoo/warpgo/internal/systemd"
)

// fakeBinary 是一段**能被执行**的载荷。
//
// Fetch 现在会在替换目标文件之前先执行一次（`--version`），所以测试里的"假二进制"
// 不能再是一串随便的文本 —— 那样会走不到 rename 那一步。用一段真能跑的 shell 脚本，
// 既让测试走的是真实路径，也顺带证明了"验证发生在临时文件上"。
const fakeBinary = "#!/bin/sh\necho wireproxy, version v1.1.3\n"

func sampleConfig() Config {
	return Config{
		Addrs:         []string{"172.16.0.2/32", "2606:4700:110:8a1e::2/128"},
		MTU:           1280,
		PrivateKey:    "uCTIK+56CPyCvwJxmU5dBfuyJvPuSXAq1FzHdnIxe1Q=",
		PeerPublicKey: "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
		Endpoint:      "162.159.192.1:2408",
		Port:          DefaultPort,
		Note:          "dev-1",
	}
}

// 逐字节钉住渲染结果：这个文件里有私钥、有对端，用户会直接打开它看，
// 任何"顺手改一下格式"都应当被测试挡下来（而不是等到真机上发现读不懂）。
func TestRenderByteExact(t *testing.T) {
	want := `# 本文件由 ` + "`warpgo proxy install`" + ` 生成 —— 手工改动会在下次 install 时被覆盖。
# **里面有私钥**（0600），请不要提交到任何仓库或贴进聊天窗口。
# 设备: dev-1
# 配置格式见 https://github.com/windtf/wireproxy

[Interface]
Address = 172.16.0.2/32, 2606:4700:110:8a1e::2/128
MTU = 1280
PrivateKey = uCTIK+56CPyCvwJxmU5dBfuyJvPuSXAq1FzHdnIxe1Q=
DNS = 1.1.1.1, 2606:4700:4700::1111

[Peer]
PublicKey = bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=
Endpoint = 162.159.192.1:2408
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25

[Socks5]
BindAddress = 127.0.0.1:40000

[Resolve]
# auto：接口只有 IPv4 地址时等价于 ipv4，否则等价于 ipv6。
ResolveStrategy = auto
`
	if got := Render(sampleConfig()); got != want {
		t.Errorf("渲染结果与预期不一致\n--- 实得 ---\n%s\n--- 期望 ---\n%s", got, want)
	}
}

// socks5 必须只监听回环：绑到 0.0.0.0 等于把一条通往 WARP 的开放代理挂到公网。
func TestRenderSocks5IsLoopbackOnly(t *testing.T) {
	got := Render(sampleConfig())
	if !strings.Contains(got, "BindAddress = 127.0.0.1:40000") {
		t.Errorf("socks5 必须绑 127.0.0.1:\n%s", got)
	}
	if strings.Contains(got, "0.0.0.0:40000") {
		t.Errorf("不该出现 0.0.0.0:40000:\n%s", got)
	}
}

func TestValidate(t *testing.T) {
	base := sampleConfig()

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"没有地址", func(c *Config) { c.Addrs = nil }},
		{"地址不是 CIDR", func(c *Config) { c.Addrs = []string{"172.16.0.2"} }},
		{"没有私钥", func(c *Config) { c.PrivateKey = "" }},
		{"没有对端公钥", func(c *Config) { c.PeerPublicKey = "" }},
		{"没有端点", func(c *Config) { c.Endpoint = "" }},
		{"端点缺端口", func(c *Config) { c.Endpoint = "162.159.192.1" }},
		{"端口太小", func(c *Config) { c.Port = 80 }},
		{"端口太大", func(c *Config) { c.Port = 70000 }},
		{"MTU 为负", func(c *Config) { c.MTU = -1 }},
	}
	for _, tc := range cases {
		c := base
		c.Addrs = append([]string(nil), base.Addrs...)
		tc.mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s：应当报错", tc.name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Errorf("完整配置不该报错: %v", err)
	}

	// IPv6 字面量端点的 [::1]:2408 写法也要认。
	c := base
	c.Endpoint = "[2606:4700:4700::1111]:2408"
	if err := c.Validate(); err != nil {
		t.Errorf("IPv6 字面量端点应被接受: %v", err)
	}
}

func TestParsePort(t *testing.T) {
	conf := Render(sampleConfig())
	port, err := ParsePort(conf)
	if err != nil {
		t.Fatal(err)
	}
	if port != DefaultPort {
		t.Errorf("实得 %d，期望 %d", port, DefaultPort)
	}

	// 端口改了也要读得出来。
	c := sampleConfig()
	c.Port = 25344
	if port, _ := ParsePort(Render(c)); port != 25344 {
		t.Errorf("实得 %d，期望 25344", port)
	}

	// 没有 [Socks5] 段时要报错，而不是返回 0 让调用方以为"端口是 0"。
	if _, err := ParsePort("[Interface]\nPrivateKey = x\n"); err == nil {
		t.Error("缺少 Socks5 段应当报错")
	}
	// BindAddress 的端口不合法也要报错。
	if _, err := ParsePort("[Socks5]\nBindAddress = 127.0.0.1:abc\n"); err == nil {
		t.Error("端口不合法应当报错")
	}
}

func TestAssetName(t *testing.T) {
	cases := map[string]string{
		"linux/amd64":   "wireproxy_linux_amd64.tar.gz",
		"linux/arm64":   "wireproxy_linux_arm64.tar.gz",
		"linux/riscv64": "wireproxy_linux_riscv64.tar.gz",
	}
	for in, want := range cases {
		parts := strings.SplitN(in, "/", 2)
		if got := assetName(parts[0], parts[1]); got != want {
			t.Errorf("%s: 实得 %q，期望 %q", in, got, want)
		}
	}
	// 没有发布产物的平台必须返回空串（调用方据此给出明确报错，而不是去下 404）。
	for _, in := range [][2]string{{"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "mips64"}, {"linux", "loong64"}} {
		if got := assetName(in[0], in[1]); got != "" {
			t.Errorf("%s/%s 不该有资产名，实得 %q", in[0], in[1], got)
		}
	}
}

func TestLookupSumAndNormalize(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	content := fmt.Sprintf("%s  wireproxy_linux_386.tar.gz\n%s *wireproxy_linux_amd64.tar.gz\n", sum, sum)
	if got := lookupSum(content, "wireproxy_linux_amd64.tar.gz"); got != sum {
		t.Errorf("实得 %q，期望 %q（含 * 前缀的写法要认）", got, sum)
	}
	if got := lookupSum(content, "wireproxy_linux_arm64.tar.gz"); got != "" {
		t.Errorf("清单里没有的资产应返回空串，实得 %q", got)
	}
	// 带目录前缀的名字也要能对上。
	if got := lookupSum(sum+"  releases/wireproxy_linux_amd64.tar.gz", "wireproxy_linux_amd64.tar.gz"); got != sum {
		t.Errorf("带目录的名字应能对上，实得 %q", got)
	}

	if normalizeSHA(strings.ToUpper(sum)) != sum {
		t.Error("大写应被收敛成小写")
	}
	if normalizeSHA("sha256:"+sum) != sum {
		t.Error("sha256: 前缀应被剥掉")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		if normalizeSHA(bad) != "" {
			t.Errorf("%q 不该被当成合法校验和", bad)
		}
	}
}

// 造一个 tar.gz（可指定条目名）。
func tarGz(t *testing.T, files map[string]string, order []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := order
	if len(names) == 0 {
		for name := range files {
			names = append(names, name)
		}
	}
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallFromTarGz(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar.gz")
	dst := filepath.Join(dir, "bin", "wireproxy")
	if err := os.WriteFile(src, tarGz(t, map[string]string{"wireproxy": "#!/bin/sh\necho hi\n"}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installFrom(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "#!/bin/sh\necho hi\n" {
		t.Errorf("内容不对: %q", b)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("权限应为 0755，实得 %v", fi.Mode().Perm())
	}
}

// 归档里唯一的常规文件即使改了名字也要装得上（发布物改名的容错路径）。
func TestInstallFromTarGzSingleOtherFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar.gz")
	dst := filepath.Join(dir, "wireproxy")
	if err := os.WriteFile(src, tarGz(t, map[string]string{"wireproxy_linux_amd64": "X"}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installFrom(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "X" {
		t.Errorf("实得 %q", b)
	}
}

// 多个文件、且没有一个叫 wireproxy → 必须拒绝。
//
// 宁可失败也不装一个来路不明的文件到 /usr/local/bin。
func TestInstallFromTarGzRejectsAmbiguous(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar.gz")
	dst := filepath.Join(dir, "wireproxy")
	body := tarGz(t, map[string]string{"LICENSE.md": "l", "README.md": "r"}, []string{"LICENSE.md", "README.md"})
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installFrom(src, dst); err == nil {
		t.Fatal("应当拒绝：归档里没有 wireproxy")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("失败时不该留下目标文件")
	}
}

// 归档里的条目名带 `../` 只能落到我们指定的目标路径上（不能顺着名字写出去）。
func TestInstallFromTarGzIgnoresEntryPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar.gz")
	dst := filepath.Join(dir, "wireproxy")
	if err := os.WriteFile(src, tarGz(t, map[string]string{"../../evil": "payload"}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installFrom(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "evil")); err == nil {
		t.Error("不该按条目名往外写")
	}
	if b, _ := os.ReadFile(dst); string(b) != "payload" {
		t.Errorf("内容应落到目标路径，实得 %q", b)
	}
}

func TestInstallFromRawBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wireproxy.raw")
	dst := filepath.Join(dir, "out", "wireproxy")
	if err := os.WriteFile(src, []byte("ELF-not-really"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installFrom(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "ELF-not-really" {
		t.Errorf("实得 %q", b)
	}
}

// 一条完整的"下载 → 校验 → 解包 → 安装"路径，用本地假服务器跑。
func TestFetchVerifiesChecksum(t *testing.T) {
	archive := tarGz(t, map[string]string{"wireproxy": fakeBinary}, nil)
	sum := sha256.Sum256(archive)
	sumHex := hex.EncodeToString(sum[:])

	serve := func(sumBody string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, ChecksumFile):
				fmt.Fprint(w, sumBody)
			case strings.HasSuffix(r.URL.Path, ".tar.gz"):
				w.Write(archive)
			default:
				http.NotFound(w, r)
			}
		}))
	}

	t.Run("校验和正确则安装成功", func(t *testing.T) {
		srv := serve(sumHex + "  " + assetName("linux", "amd64") + "\n")
		defer srv.Close()

		dst := filepath.Join(t.TempDir(), "wireproxy")
		inst := &Installer{Base: srv.URL, GOOS: "linux", GOARCH: "amd64", Version: "v1.1.3"}
		info, err := inst.Fetch(context.Background(), dst)
		if err != nil {
			t.Fatal(err)
		}
		if info.SHA256 != sumHex || info.Source != "download" {
			t.Errorf("Info 不对: %+v", info)
		}
		if b, _ := os.ReadFile(dst); string(b) != fakeBinary {
			t.Errorf("装出来的内容不对: %q", b)
		}
	})

	t.Run("校验和不符必须拒绝", func(t *testing.T) {
		srv := serve(strings.Repeat("00", 32) + "  " + assetName("linux", "amd64") + "\n")
		defer srv.Close()

		dst := filepath.Join(t.TempDir(), "wireproxy")
		inst := &Installer{Base: srv.URL, GOOS: "linux", GOARCH: "amd64", Version: "v1.1.3"}
		_, err := inst.Fetch(context.Background(), dst)
		if err == nil {
			t.Fatal("校验和不符时应当报错")
		}
		if !strings.Contains(err.Error(), "校验和不符") {
			t.Errorf("错误信息应说清楚原因: %v", err)
		}
		if _, serr := os.Stat(dst); serr == nil {
			t.Error("校验失败时不该留下二进制")
		}
	})

	t.Run("取不到清单时报错并给出两条出路", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		dst := filepath.Join(t.TempDir(), "wireproxy")
		inst := &Installer{Base: srv.URL, GOOS: "linux", GOARCH: "amd64", Version: "v1.1.3"}
		_, err := inst.Fetch(context.Background(), dst)
		if err == nil {
			t.Fatal("拿不到清单时应当报错（不能静默降级成不校验）")
		}
		for _, want := range []string{"--sha256", "--from"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("错误信息里应提示 %s: %v", want, err)
			}
		}
	})

	t.Run("--url 必须配 --sha256", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "wireproxy")
		inst := &Installer{URL: "https://example.invalid/x.tar.gz"}
		if _, err := inst.Fetch(context.Background(), dst); err == nil ||
			!strings.Contains(err.Error(), "--sha256") {
			t.Errorf("应当要求 --sha256，实得 %v", err)
		}
	})

	t.Run("没有发布产物的平台直接报错", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "wireproxy")
		inst := &Installer{GOOS: "windows", GOARCH: "amd64"}
		if _, err := inst.Fetch(context.Background(), dst); err == nil {
			t.Error("应当报错")
		}
	})
}

// --from：用本地文件安装（离线路径）。
func TestFetchFromLocalFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wireproxy.tar.gz")
	if err := os.WriteFile(src, tarGz(t, map[string]string{"wireproxy": fakeBinary}, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "bin", "wireproxy")
	info, err := (&Installer{From: src}).Fetch(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != "local" || info.SHA256 == "" {
		t.Errorf("Info 不对: %+v", info)
	}
	if b, _ := os.ReadFile(dst); string(b) != fakeBinary {
		t.Errorf("实得 %q", b)
	}
}

// 环境变量覆盖安装位置（测试与打包用）。
func TestNewHonoursEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WARPGO_ETC", dir)
	t.Setenv(EnvBinPath, filepath.Join(dir, "bin", "wireproxy"))
	t.Setenv(EnvUnitPath, filepath.Join(dir, "units", "x.service"))

	m := New()
	if m.ConfPath != filepath.Join(dir, ConfFileName) {
		t.Errorf("配置路径实得 %q", m.ConfPath)
	}
	if m.StatePath != filepath.Join(dir, StateFileName) {
		t.Errorf("状态路径实得 %q", m.StatePath)
	}
	if !strings.HasSuffix(m.BinPath, filepath.Join("bin", "wireproxy")) {
		t.Errorf("二进制路径实得 %q", m.BinPath)
	}
	if !strings.HasSuffix(m.UnitPath, "x.service") {
		t.Errorf("单元路径实得 %q", m.UnitPath)
	}
}

// 单元里必须是绝对路径 + 指向我们的配置文件。
func TestRenderUnit(t *testing.T) {
	m := &Manager{BinPath: "/usr/local/bin/wireproxy", ConfPath: "/etc/warpgo/proxy.conf"}
	unit := m.RenderUnit()
	for _, want := range []string{
		"ExecStart=/usr/local/bin/wireproxy -c /etc/warpgo/proxy.conf",
		"Type=simple",
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("单元里应有 %q:\n%s", want, unit)
		}
	}
	// 与保活单元同样的取舍：不写 ExecStop。
	if strings.Contains(unit, "ExecStop") {
		t.Errorf("不该写 ExecStop:\n%s", unit)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		BinPath:   filepath.Join(dir, "bin", "wireproxy"),
		UnitPath:  filepath.Join(dir, "units", "warpgo-proxy.service"),
		ConfPath:  filepath.Join(dir, "etc", "proxy.conf"),
		StatePath: filepath.Join(dir, "etc", "proxy.json"),
	}
	m.Runner = func(ctx context.Context, name string, args ...string) (string, int, error) {
		return "", 0, nil
	}

	if err := os.MkdirAll(filepath.Dir(m.BinPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.BinPath, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteConf(sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteUnit(); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveState(&State{Port: 40000, BinPath: m.BinPath, ConfPath: m.ConfPath, UnitPath: m.UnitPath}); err != nil {
		t.Fatal(err)
	}
	if !m.Installed() {
		t.Fatal("准备好之后 Installed() 应为 true")
	}

	removed, warn := m.Remove(context.Background(), false)
	if warn != nil {
		t.Errorf("不该有警告: %v", warn)
	}
	if len(removed) != 4 {
		t.Errorf("应报告删除 4 项，实得 %d: %v", len(removed), removed)
	}
	for _, p := range []string{m.BinPath, m.UnitPath, m.ConfPath, m.StatePath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 应当已被删除", p)
		}
	}
	if m.Installed() {
		t.Error("撤掉之后 Installed() 应为 false")
	}
}

// --keep-binary：只留二进制，其余都删。
func TestRemoveKeepsBinary(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		BinPath:   filepath.Join(dir, "wireproxy"),
		UnitPath:  filepath.Join(dir, "x.service"),
		ConfPath:  filepath.Join(dir, "proxy.conf"),
		StatePath: filepath.Join(dir, "proxy.json"),
	}
	m.Runner = func(ctx context.Context, name string, args ...string) (string, int, error) { return "", 0, nil }
	if err := os.WriteFile(m.BinPath, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.BinPath); err != nil {
		t.Errorf("--keep-binary 时二进制应当留着: %v", err)
	}
}

// 没装过的时候卸载也必须成功（幂等），并且不报任何错。
func TestRemoveOnCleanMachine(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		BinPath:   filepath.Join(dir, "wireproxy"),
		UnitPath:  filepath.Join(dir, "x.service"),
		ConfPath:  filepath.Join(dir, "proxy.conf"),
		StatePath: filepath.Join(dir, "proxy.json"),
	}
	m.Runner = func(ctx context.Context, name string, args ...string) (string, int, error) {
		// 未安装 / 未运行时 systemctl 会返回非 0 —— 卸载仍应成功。
		return "not found", 5, nil
	}
	removed, warn := m.Remove(context.Background(), false)
	if warn != nil || len(removed) != 0 {
		t.Errorf("干净机器上卸载应当什么都不做：removed=%v warn=%v", removed, warn)
	}
}

// 单元名的定制要真的传到底下的 systemctl（M7 复用 systemd.Manager 的接缝）。
func TestSystemdUsesCustomUnitName(t *testing.T) {
	var seen []string
	m := &Manager{BinPath: "/b", UnitPath: "/u/warpgo-proxy.service"}
	m.Runner = func(ctx context.Context, name string, args ...string) (string, int, error) {
		seen = append(seen, strings.Join(append([]string{name}, args...), " "))
		return "active", 0, nil
	}
	m.Systemd().Runner = m.Runner

	if _, err := m.Systemd().IsActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "systemctl is-active "+DefaultUnitName {
		t.Errorf("实得 %v", seen)
	}
	if DefaultUnitName == systemd.UnitName {
		t.Error("代理单元与保活单元不能同名")
	}
}

func TestPortFallbackOrder(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		BinPath:   filepath.Join(dir, "wireproxy"),
		UnitPath:  filepath.Join(dir, "x.service"),
		ConfPath:  filepath.Join(dir, "proxy.conf"),
		StatePath: filepath.Join(dir, "proxy.json"),
	}
	// 什么都没有 → 默认端口。
	if got := m.Port(); got != DefaultPort {
		t.Errorf("实得 %d，期望默认值", got)
	}
	// 只有状态文件 → 用状态文件里的。
	if err := m.SaveState(&State{Port: 12345}); err != nil {
		t.Fatal(err)
	}
	if got := m.Port(); got != 12345 {
		t.Errorf("实得 %d，期望 12345", got)
	}
	// 配置存在时以配置为准（它才是服务真正读的东西）。
	c := sampleConfig()
	c.Port = 23456
	if err := m.WriteConf(c); err != nil {
		t.Fatal(err)
	}
	if got := m.Port(); got != 23456 {
		t.Errorf("实得 %d，期望 23456（配置文件优先）", got)
	}
	if got := m.ProxyURL(0); got != "socks5h://127.0.0.1:23456" {
		t.Errorf("代理地址实得 %q", got)
	}
}

// 一个跑不起来的产物**不能覆盖**现在能用的那个二进制。
//
// 这是"先执行、再替换"这条规则的直接理由：dst 上通常已经躺着一个能用的 wireproxy，
// 而 `--from` 给错文件（或下载到一张 HTML 错误页）时，如果先 rename 再验证，
// 用户就从"能代理"变成"什么都没有"，而报错只会说"刚装的那个跑不起来"。
func TestFetchRejectsNonExecutableWithoutClobbering(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "wireproxy")
	if err := os.WriteFile(dst, []byte("OLD-GOOD"), 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "bad.tar.gz")
	// 一段普通文本：解出来、有执行位，但内核执行不了它（exec format error）。
	if err := os.WriteFile(src, tarGz(t, map[string]string{"wireproxy": "这不是一个可执行文件\n"}, nil), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := (&Installer{From: src}).Fetch(context.Background(), dst); err == nil {
		t.Fatal("跑不起来的产物应当被拒绝")
	}
	if b, _ := os.ReadFile(dst); string(b) != "OLD-GOOD" {
		t.Errorf("旧文件被覆盖了: %q", b)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wireproxy.tmp") {
			t.Errorf("失败路径上留下了临时文件 %s", e.Name())
		}
	}
}
