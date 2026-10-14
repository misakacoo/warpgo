package dns

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestManager 造一个完全在临时目录里的 Manager —— 绝不碰真的 /etc/resolv.conf。
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	resolv := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(resolv, []byte("nameserver 10.0.0.53\nsearch lan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "etc")
	return New(stateDir), resolv
}

// 备份 → 改写 → 还原，是全流程里最关键的一条。
//
// 旧版卸载后的真机审计给出的结论是：接口、路由、规则、单元都清干净了，
// 唯独 /etc/resolv.conf 还是安装时写进去的值，而备份已经被删 ——
// 用户连"原来是什么"都无从得知。这个用例守的就是这件事。
func TestBackupSetRestoreRoundtrip(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv

	original, err := os.ReadFile(resolv)
	if err != nil {
		t.Fatal(err)
	}

	created, err := m.Backup()
	if err != nil {
		t.Fatalf("备份: %v", err)
	}
	if !created {
		t.Fatal("第一次备份应当真的创建文件")
	}
	if !m.HasBackup() {
		t.Fatal("备份应当存在")
	}

	if err := m.Set([]string{"1.1.1.1", "1.0.0.1"}); err != nil {
		t.Fatalf("设置 DNS: %v", err)
	}
	got, _ := os.ReadFile(resolv)
	if !strings.Contains(string(got), "nameserver 1.1.1.1") {
		t.Fatalf("DNS 未被改写: %s", got)
	}
	servers, err := m.Servers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0] != "1.1.1.1" {
		t.Fatalf("解析出的 nameserver 不对: %v", servers)
	}

	if err := m.Restore(); err != nil {
		t.Fatalf("还原: %v", err)
	}
	back, _ := os.ReadFile(resolv)
	if string(back) != string(original) {
		t.Fatalf("还原后内容不一致\n原始: %q\n现在: %q", original, back)
	}
	if m.HasBackup() {
		t.Fatal("还原成功后备份应当被删掉")
	}
}

// 第二次 Backup **不能覆盖**第一次 —— 先到的那个才是"原始"的。
// 覆盖它就等于把用户的答案擦掉。
func TestBackupIsNotOverwritten(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv

	if _, err := m.Backup(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(m.BackupPath)

	// 期间文件被改了（可能是用户自己，也可能是我们上一次 Set），再备份一次。
	if err := os.WriteFile(resolv, []byte("nameserver 9.9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := m.Backup()
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("已有备份时不该再次创建")
	}
	second, _ := os.ReadFile(m.BackupPath)
	if string(first) != string(second) {
		t.Fatalf("备份被覆盖了！\n原来: %q\n现在: %q", first, second)
	}
	if !strings.Contains(string(first), "10.0.0.53") {
		t.Fatalf("备份里应当是更早的那一份: %q", first)
	}
}

// 备份写的必须是"改写之前"的内容 —— 顺序反了会把我们写的东西当成原文存下来，
// 那还原就等于什么也没做（旧版就是这个结局）。
func TestSetBacksUpBeforeWriting(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv

	if err := m.Set([]string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(m.BackupPath)
	if err != nil {
		t.Fatalf("Set 应当自动备份: %v", err)
	}
	if !strings.Contains(string(bak), "10.0.0.53") {
		t.Fatalf("备份里应当是最初的内容，实际: %q", bak)
	}
	if strings.Contains(string(bak), "1.1.1.1") {
		t.Fatal("备份里出现了我们写进去的值 —— 说明顺序反了")
	}
}

// systemd-resolved 的机器上 /etc/resolv.conf 是符号链接。
// 把它替换成普通文件会让 resolved 的配置再也无法生效，而那是用户没做过的事。
func TestSymlinkIsRefused(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv

	target := filepath.Join(filepath.Dir(resolv), "stub-resolv.conf")
	if err := os.WriteFile(target, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(resolv); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, resolv); err != nil {
		t.Skipf("无法创建符号链接（%v），跳过", err)
	}

	if !m.IsSymlink() {
		t.Fatal("应当识别出符号链接")
	}
	if _, err := m.Backup(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("备份应当拒绝符号链接，实际: %v", err)
	}
	if err := m.Set([]string{"1.1.1.1"}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("设置应当拒绝符号链接，实际: %v", err)
	}
	// 链接本身必须原样保留。
	fi, err := os.Lstat(resolv)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("符号链接被破坏了")
	}
}

func TestRestoreWithoutBackup(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv

	if m.HasBackup() {
		t.Fatal("此时不该有备份")
	}
	if err := m.Restore(); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("没有备份时应当返回 ErrNoBackup，实际: %v", err)
	}
}

// 还原要恢复原来的权限位 —— 有些工具会因为 resolv.conf 变成 0600 而读不到它。
func TestRestoreKeepsMode(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv
	if err := os.Chmod(resolv, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := m.Set([]string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	// 期间权限被别的东西改了。
	if err := os.Chmod(resolv, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(resolv)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("还原后权限应为 0640，实际 %v", fi.Mode().Perm())
	}
}

func TestSetRejectsEmptyServers(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv
	if err := m.Set(nil); err == nil {
		t.Fatal("空的服务器列表应当被拒绝 —— 那会写出一个没有 nameserver 的 resolv.conf")
	}
}

func TestServersIgnoresCommentsAndOtherDirectives(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = resolv
	content := "# comment\nnameserver 1.1.1.1\nsearch lan\noptions ndots:1\nnameserver   8.8.8.8\n"
	if err := os.WriteFile(resolv, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	servers, err := m.Servers()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0] != "1.1.1.1" || servers[1] != "8.8.8.8" {
		t.Fatalf("解析结果不对: %v", servers)
	}
}

func TestCurrentMissingFileIsNotAnError(t *testing.T) {
	m, resolv := newTestManager(t)
	m.ResolvPath = filepath.Join(filepath.Dir(resolv), "nope.conf")
	b, err := m.Current()
	if err != nil {
		t.Fatalf("文件不存在不该报错: %v", err)
	}
	if len(b) != 0 {
		t.Fatalf("应当返回空内容，实际 %q", b)
	}
}
