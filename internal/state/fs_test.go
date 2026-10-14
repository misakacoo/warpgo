package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "data.json")

	if err := WriteFileAtomic(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\n" {
		t.Fatalf("内容不符: %q", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("权限应为 0600，实得 %o", info.Mode().Perm())
	}

	if err := WriteFileAtomic(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "second\n" {
		t.Fatalf("覆盖后内容不符: %q", got)
	}

	// 不该留下临时文件。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("目录里应只剩目标文件，实得 %v", names)
	}
}

// 关键回归：WriteFileAtomic 也会被用来往 /etc/wireguard 这类系统目录写配置。
// 如果它顺手把父目录 chmod 成 0700，就会改变无关东西的权限。
func TestWriteFileAtomicDoesNotChmodExistingDir(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "wireguard")
	if err := os.MkdirAll(system, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(system, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(filepath.Join(system, "warpgo.conf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(system)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("已存在的目录权限被改动了：期望 0755，实得 %o", info.Mode().Perm())
	}
}

func TestEnsureDirTightensPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "warpgo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != DirPerm {
		t.Fatalf("状态目录应被收紧到 %o，实得 %o", DirPerm, info.Mode().Perm())
	}
}

func TestReadFileDistinguishesMissingFromBroken(t *testing.T) {
	dir := t.TempDir()

	b, found, err := ReadFile(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatalf("文件不存在不该当错误: %v", err)
	}
	if found || b != nil {
		t.Fatalf("应返回 (nil,false,nil)，实得 (%v,%v,%v)", b, found, err)
	}

	path := filepath.Join(dir, "here")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, found, err = ReadFile(path)
	if err != nil || !found || string(b) != "hi" {
		t.Fatalf("实得 (%q,%v,%v)", b, found, err)
	}
}

func TestDirEnvOverride(t *testing.T) {
	t.Setenv(EnvDir, "/tmp/warpgo-test-etc")
	if Dir() != "/tmp/warpgo-test-etc" {
		t.Fatalf("EnvDir 覆盖没生效: %s", Dir())
	}
	if AccountPath() != "/tmp/warpgo-test-etc/account.json" {
		t.Fatalf("AccountPath = %s", AccountPath())
	}
	if RuntimePath() != "/tmp/warpgo-test-etc/runtime.json" {
		t.Fatalf("RuntimePath = %s", RuntimePath())
	}

	t.Setenv(EnvDir, "")
	if Dir() != DefaultDir {
		t.Fatalf("未设置时应回落到默认目录: %s", Dir())
	}
}
