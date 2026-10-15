package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- 纯函数 -------------------------------------------------------------------

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"0.1.0", "0.2.0", -1, true},
		{"0.2.0", "0.1.0", 1, true},
		{"v0.1.0", "0.1.0", 0, true},
		{"v1.2.3", "v1.2.3", 0, true},
		// 这两条是不能按字符串比较的理由：字符串比较下 "1.10.0" < "1.9.0"。
		{"1.10.0", "1.9.0", 1, true},
		{"1.9.0", "1.10.0", -1, true},
		// 预发布排在正式版之前。
		{"1.0.0-rc1", "1.0.0", -1, true},
		{"dev", "0.1.0", 0, false},
		{"", "0.1.0", 0, false},
		{"abc", "0.1.0", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CompareVersions(%q, %q) = (%d, %v)，期望 (%d, %v)", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		tag, goos, goarch, want string
	}{
		{"v0.2.0", "linux", "amd64", "warpgo_v0.2.0_linux_amd64.tar.gz"},
		{"v0.2.0", "linux", "arm64", "warpgo_v0.2.0_linux_arm64.tar.gz"},
		// tag 原样带上 `v`：资产名要由 build.sh 生成、再被这里反推回来，
		// 中间任何一次"美化"都是一个可能对不上的地方。
		{"0.2.0", "linux", "amd64", "warpgo_0.2.0_linux_amd64.tar.gz"},
		{"v0.2.0", "darwin", "amd64", ""},
		{"v0.2.0", "linux", "386", ""},
		{"v0.2.0", "linux", "riscv64", ""},
	}
	for _, c := range cases {
		if got := AssetName(c.tag, c.goos, c.goarch); got != c.want {
			t.Errorf("AssetName(%q, %q, %q) = %q，期望 %q", c.tag, c.goos, c.goarch, got, c.want)
		}
	}
}

// ---- 假发布页 -----------------------------------------------------------------

// fakeServer 起一个假 GitHub：同一个 server 既当 API 也当下载站
// （路径前缀不同：`/repos/...` 与 `/<repo>/releases/download/...`）。
type fakeServer struct {
	*httptest.Server
	repo string
	// assets 是"资产名 → 内容"。
	assets map[string][]byte
	// sums 是 checksums.txt 的内容；为空时按 assets 自动生成。
	sums string
	// checksumStatus 非 0 时，清单请求返回该状态码（用于测"清单取不到"）。
	checksumStatus int
}

func newFakeServer(t *testing.T, tag string, assets map[string][]byte) *fakeServer {
	t.Helper()
	fs := &fakeServer{repo: "test/warpgo", assets: assets}
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") && !strings.Contains(r.URL.Path, "/releases/tags/") {
			http.NotFound(w, r)
			return
		}
		names := make([]string, 0, len(fs.assets))
		for n := range fs.assets {
			names = append(names, n)
		}
		payload := map[string]any{
			"tag_name": tag,
			"html_url": "https://example.invalid/releases/" + tag,
		}
		assetsJSON := []map[string]string{}
		for _, n := range names {
			assetsJSON = append(assetsJSON, map[string]string{
				"name":                 n,
				"browser_download_url": fs.URL + "/" + fs.repo + "/releases/download/" + tag + "/" + n,
			})
		}
		payload["assets"] = assetsJSON
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})

	mux.HandleFunc("/"+fs.repo+"/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if name == ChecksumFile {
			if fs.checksumStatus != 0 {
				w.WriteHeader(fs.checksumStatus)
				return
			}
			_, _ = w.Write([]byte(fs.checksumText()))
			return
		}
		body, ok := fs.assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})

	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fakeServer) checksumText() string {
	if fs.sums != "" {
		return fs.sums
	}
	var b strings.Builder
	for name, body := range fs.assets {
		h := sha256.Sum256(body)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	return b.String()
}

func (fs *fakeServer) client() *Client {
	return &Client{
		Repo:    fs.repo,
		APIBase: fs.URL,
		Base:    fs.URL,
		GOOS:    "linux",
		GOARCH:  "amd64",
		HTTP:    fs.Client(),
	}
}

// tarGz 打一个只含一个可执行文件的归档。
func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 一个"能跑起来"的假 warpgo：`version` 会打印一行东西。
const goodScript = "#!/bin/sh\necho \"warpgo 9.9.9 (test)\"\n"

func assetFor(tag string) string { return AssetName(tag, "linux", "amd64") }

// ---- Check --------------------------------------------------------------------

func TestCheckFindsNewer(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	res, err := fs.client().Check(context.Background(), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Comparable || !res.Newer {
		t.Fatalf("应当判定「有新版」：%+v", res)
	}
	if res.Latest != "0.2.0" {
		t.Errorf("最新版本应为 0.2.0，实得 %q", res.Latest)
	}
	if res.Asset != assetFor(tag) {
		t.Errorf("资产名应为 %s，实得 %q", assetFor(tag), res.Asset)
	}
}

func TestCheckUpToDate(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	res, err := fs.client().Check(context.Background(), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Comparable || res.Newer {
		t.Fatalf("同版本不该判成有新版：%+v", res)
	}
}

func TestCheckDevIsNotComparable(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	res, err := fs.client().Check(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	// 关键：`dev` 必须被明确标成"不可比"，而不是静默地 Newer=false
	// —— 后者会被读成"已经是最新"。
	if res.Comparable {
		t.Fatal("dev 构建不该被判成「可比」")
	}
	if res.Newer {
		t.Fatal("不可比时 Newer 必须为 false")
	}
}

// ---- Update -------------------------------------------------------------------

func TestUpdateSkippedWhenCurrentIsLatest(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	dst := filepath.Join(t.TempDir(), "warpgo")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := fs.client().Update(context.Background(), dst, UpdateOptions{Current: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatal("已经是最新时不该动手")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "旧内容" {
		t.Fatalf("不该改动文件，实得 %q", b)
	}
}

func TestUpdateRejectsMissingAsset(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{"warpgo_v0.2.0_linux_riscv64.tar.gz": []byte("x")})
	_, err := fs.client().Update(context.Background(), filepath.Join(t.TempDir(), "warpgo"),
		UpdateOptions{Current: "0.1.0"})
	if err == nil {
		t.Fatal("没有对应资产时应当报错")
	}
	// 报错里要列出实际有哪些资产 —— 否则"对不上"这件事没法自查。
	if !strings.Contains(err.Error(), "riscv64") {
		t.Fatalf("报错里应列出实际资产名，实得: %v", err)
	}
}

func TestUpdateRejectsChecksumMismatch(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	fs.sums = strings.Repeat("0", 64) + "  " + assetFor(tag) + "\n"

	dst := filepath.Join(t.TempDir(), "warpgo")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := fs.client().Update(context.Background(), dst, UpdateOptions{Current: "0.1.0"})
	if err == nil {
		t.Fatal("校验和不符时应当报错")
	}
	if !strings.Contains(err.Error(), "校验和不符") {
		t.Fatalf("报错应说明是校验和不符，实得: %v", err)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "旧内容" {
		t.Fatal("校验失败时旧文件必须一个字节都没动")
	}
}

func TestUpdateFailsWhenChecksumListUnavailable(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	fs.checksumStatus = http.StatusNotFound

	dst := filepath.Join(t.TempDir(), "warpgo")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := fs.client().Update(context.Background(), dst, UpdateOptions{Current: "0.1.0"})
	if err == nil {
		t.Fatal("拿不到清单时应当报错，而不是静默地不校验")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "旧内容" {
		t.Fatal("旧文件必须没被动过")
	}
}

func TestUpdateSuccess(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	dst := filepath.Join(t.TempDir(), "bin", "warpgo")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := fs.client().Update(context.Background(), dst, UpdateOptions{Current: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.To != "0.2.0" || res.From != "0.1.0" {
		t.Fatalf("结果不对: %+v", res)
	}
	if res.Asset != assetFor(tag) {
		t.Errorf("资产名不对: %s", res.Asset)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goodScript {
		t.Fatalf("文件内容没被换掉: %q", b)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("权限应为 0755，实得 %v", fi.Mode().Perm())
	}
}

// TestUpdateDoesNotClobberOnBadArtifact 是本包最要紧的一条：
// 下载到的东西跑不起来时，**磁盘上那个能用的 warpgo 必须一个字节都没动**。
func TestUpdateDoesNotClobberOnBadArtifact(t *testing.T) {
	tag := "v0.2.0"
	// 归档里的"可执行文件"其实是一段普通文本 —— 跑不起来。
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", "这不是一个程序\n")})
	dst := filepath.Join(t.TempDir(), "warpgo")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := fs.client().Update(context.Background(), dst, UpdateOptions{Current: "0.1.0"})
	if err == nil {
		t.Fatal("产物跑不起来时应当报错")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "旧内容" {
		t.Fatal("坏产物绝不能覆盖掉能用的那个")
	}
	// 也不该在目标目录里留下临时文件。
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".warpgo-install.tmp") {
			t.Fatalf("留下了临时文件 %s", e.Name())
		}
	}
}

func TestUpdateForceReinstallsSameVersion(t *testing.T) {
	tag := "v0.2.0"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	dst := filepath.Join(t.TempDir(), "warpgo")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := fs.client().Update(context.Background(), dst,
		UpdateOptions{Current: "0.2.0", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("--force 时同版本也该重装")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != goodScript {
		t.Fatalf("内容没被换成新的: %q", b)
	}
}

func TestUpdateSpecificTag(t *testing.T) {
	tag := "v0.1.5"
	fs := newFakeServer(t, tag, map[string][]byte{assetFor(tag): tarGz(t, "warpgo", goodScript)})
	dst := filepath.Join(t.TempDir(), "warpgo")
	if err := os.WriteFile(dst, []byte("旧内容"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 指定 tag 时**不**做"是不是更新"的拦截（降级/重装都是显式选择）。
	res, err := fs.client().Update(context.Background(), dst,
		UpdateOptions{Current: "0.2.0", Tag: tag})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.To != "0.1.5" {
		t.Fatalf("指定版本应当照装: %+v", res)
	}
}
