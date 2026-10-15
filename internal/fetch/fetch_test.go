package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 说明：校验和清单解析与归档解包那几条路径（`LookupSum` / `NormalizeSHA` /
// `Install`）由 internal/proxy 的测试覆盖 —— 它们从那个包的角度看是
// "wireproxy 的安装流程"，换到这里只是换了实现位置。本文件补的是
// 这一层独有的东西：URL/文本小工具、探针、下载，以及
// "替换之前先执行一次"这条最要紧的保证。

func TestPathBase(t *testing.T) {
	cases := map[string]string{
		"https://example.com/a/b/warpgo_v0.2.0_linux_amd64.tar.gz": "warpgo_v0.2.0_linux_amd64.tar.gz",
		"./warpgo_v0.2.0_linux_amd64.tar.gz":                       "warpgo_v0.2.0_linux_amd64.tar.gz",
		"releases/wireproxy":                                       "wireproxy",
		`C:\tmp\warpgo`:                                            "warpgo",
		"warpgo":                                                   "warpgo",
		"":                                                         "",
	}
	for in, want := range cases {
		if got := PathBase(in); got != want {
			t.Errorf("PathBase(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"a\nb\nc":     "a",
		"a":           "a",
		"a\n":         "a",
		"  a  \n b":   "a",
		"":            "",
		"\n":          "",
		"a\r\nb":      "a",
		"warpgo 0.1.0": "warpgo 0.1.0",
	}
	for in, want := range cases {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestVersionProbe(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake")
	// 第二行是**另一个 echo**（不是裸的 `second line` —— 那会被 sh 当成命令，
	// 以 127 退出，于是探针"看起来失败"，而真正的原因跟探针毫无关系）。
	body := "#!/bin/sh\necho \"warpgo 9.9.9 (test)\"\necho \"second line\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := VersionProbe("version")(context.Background(), script)
	if err != nil {
		t.Fatalf("探针不该失败: %v", err)
	}
	// 只取第一行 —— 版本行后面通常还有 commit / 构建时间。
	if got != "warpgo 9.9.9 (test)" {
		t.Fatalf("探针结果 = %q", got)
	}

	// 跑不起来的东西必须报错，而不是"看起来成功了"。
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("这不是一个程序\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := VersionProbe("version")(context.Background(), bad); err == nil {
		t.Fatal("非可执行内容应当让探针失败")
	}

	// 没有输出也算失败：一个什么都不打印的二进制无法自证它是 warpgo。
	silent := filepath.Join(dir, "silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := VersionProbe("version")(context.Background(), silent); err == nil {
		t.Fatal("没有输出时应当报错")
	}
}

func TestGetReportsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := Get(context.Background(), srv.Client(), srv.URL, 1024); err == nil {
		t.Fatal("非 200 应当报错")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("报错里应带上状态码: %v", err)
	}
}

func TestDownloadToTemp(t *testing.T) {
	body := []byte("hello warpgo")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	path, sum, err := DownloadToTemp(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(path))

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, body) {
		t.Fatalf("下载内容不对: %q", b)
	}
	want, err := HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sum != want {
		t.Fatalf("返回的 sha256 (%s) 与文件实际值 (%s) 不一致", sum, want)
	}
}

// TestInstallCheckedDoesNotClobber 是这一层存在的核心理由：
// 产物跑不起来时，目标位置上那个能用的文件必须一个字节都没动。
func TestInstallCheckedDoesNotClobber(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "bin", "warpgo")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("旧的可执行文件"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 归档里的东西跑不起来。
	bad := filepath.Join(dir, "bad.tar.gz")
	if err := os.WriteFile(bad, tarGzBytes(t, "warpgo", "这不是一个程序\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallChecked(context.Background(), bad, dst, "bad.tar.gz", "warpgo",
		VersionProbe("version")); err == nil {
		t.Fatal("产物跑不起来时应当报错")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "旧的可执行文件" {
		t.Fatal("坏产物绝不能覆盖掉能用的那个")
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".warpgo-install.tmp") {
			t.Fatalf("留下了临时文件 %s", e.Name())
		}
	}
}

// TestInstallCheckedReplacesGoodArtifact 正面路径：能跑就换掉。
func TestInstallCheckedReplacesGoodArtifact(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "warpgo")
	if err := os.WriteFile(dst, []byte("旧的可执行文件"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "good.tar.gz")
	if err := os.WriteFile(src, tarGzBytes(t, "warpgo", "#!/bin/sh\necho \"warpgo 9.9.9\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ver, err := InstallChecked(context.Background(), src, dst, "good.tar.gz", "warpgo",
		VersionProbe("version"))
	if err != nil {
		t.Fatal(err)
	}
	if ver != "warpgo 9.9.9" {
		t.Fatalf("探针应返回新版本自报的那行，实得 %q", ver)
	}
	b, _ := os.ReadFile(dst)
	if !strings.Contains(string(b), "9.9.9") {
		t.Fatalf("文件没被换掉: %q", b)
	}
}

// TestStageFromEntryNameOnlyUsesBase 归档里的条目名只能取 Base：
// 归档来自网络、落盘又是 root，一个 `../../` 的条目就能覆盖系统文件。
func TestStageFromEntryNameOnlyUsesBase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.tar.gz")
	if err := os.WriteFile(src, tarGzBytes(t, "../../etc/passwd", "pwned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "warpgo")
	// 基名是 passwd，不等于 warpgo；但它是归档里唯一的常规文件 → 会被取用。
	// 关键是**它只能落到 dst**，不会写到 ../../etc/passwd。
	if err := Install(src, dst, "warpgo"); err != nil {
		t.Fatalf("唯一常规文件应当被取用: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "pwned\n" {
		t.Fatalf("内容不对: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "passwd")); err == nil {
		t.Fatal("条目路径不该被还原到磁盘上")
	}
}

func tarGzBytes(t *testing.T, name, body string) []byte {
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
