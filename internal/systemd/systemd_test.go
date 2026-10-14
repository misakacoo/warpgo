package systemd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 单元文件里最容易写错、后果又最严重的几行。
func TestRenderEssentials(t *testing.T) {
	content := Render(UnitParams{
		ExecStart: []string{"watch", "--interval", "5m0s", "--ifname", "warp", "--mode", "non-global"},
	})

	checks := []struct{ want, why string }{
		{"ExecStart=/usr/bin/warpgo watch --interval 5m0s --ifname warp --mode non-global",
			"ExecStart 必须是绝对路径 + 完整参数"},
		{"Restart=always", "服务必须自愈"},
		{"RestartSec=30s", "重启间隔"},
		{"WantedBy=multi-user.target", "开机自启靠这一行"},
		{"After=network-online.target", "要在网络就绪之后再开始巡检"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.want) {
			t.Errorf("缺少 %q（%s）\n--- 实际 ---\n%s", c.want, c.why, content)
		}
	}

	// ExecStop 是刻意不写的：它非零退出会让 systemd 把单元标成 failed，
	// restart 的语义也随之变得难以推理 —— 上游在这里翻过车。
	if strings.Contains(content, "ExecStop") {
		t.Error("不该定义 ExecStop（它失败会把单元标成 failed，并干扰 restart 语义）")
	}
}

func TestRenderDefaultBinPathAndRestartSec(t *testing.T) {
	content := Render(UnitParams{ExecStart: []string{"watch"}})
	if !strings.Contains(content, "ExecStart="+DefaultBinPath+" watch") {
		t.Errorf("未填 BinPath 时应当用默认值 %s\n%s", DefaultBinPath, content)
	}
	if !strings.Contains(content, "RestartSec="+DefaultRestartSec.String()) {
		t.Errorf("未填 RestartSec 时应当用默认值 %s", DefaultRestartSec)
	}
	// 语言机制已移除（只保留简体中文）：单元里不该再出现 --lang。
	if strings.Contains(content, "--lang") {
		t.Error("单元里不该再出现 --lang —— 语言机制已移除")
	}
}

// fakeRunner 记录每条命令行，并按脚本返回预设结果。
type fakeRunner struct {
	calls []string
	// codes 为"命令首参 → 退出码"；未命中时返回 0。
	codes map[string]int
	out   map[string]string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, int, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	key := ""
	if len(args) > 0 {
		key = args[0]
	}
	return f.out[key], f.codes[key], nil
}

func (f *fakeRunner) has(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestManagerLifecycleCallsSystemctlInOrder(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRunner{codes: map[string]int{"is-active": 3}}
	m := &Manager{
		Paths:  Paths{UnitPath: filepath.Join(dir, "warpgo-watch.service"), BinPath: filepath.Join(dir, "warpgo")},
		Runner: f.run,
	}

	if m.UnitExists() {
		t.Fatal("单元文件还不该存在")
	}
	if err := m.WriteUnit(Render(UnitParams{ExecStart: []string{"watch"}})); err != nil {
		t.Fatalf("写单元: %v", err)
	}
	if !m.UnitExists() {
		t.Fatal("单元文件应当已存在")
	}

	ctx := context.Background()
	for _, step := range []struct {
		call func() error
		want string
	}{
		{func() error { return m.DaemonReload(ctx) }, "systemctl daemon-reload"},
		{func() error { return m.Enable(ctx) }, "systemctl enable " + UnitName},
		{func() error { return m.Restart(ctx) }, "systemctl restart " + UnitName},
		{func() error { return m.ResetFailed(ctx) }, "systemctl reset-failed " + UnitName},
		{func() error { return m.Disable(ctx) }, "systemctl disable " + UnitName},
	} {
		if err := step.call(); err != nil {
			t.Fatalf("%s: %v", step.want, err)
		}
		if !f.has(step.want) {
			t.Errorf("没有执行 %q，实际调用: %v", step.want, f.calls)
		}
	}

	if err := m.RemoveUnit(); err != nil {
		t.Fatalf("删单元: %v", err)
	}
	if m.UnitExists() {
		t.Fatal("单元文件应当已被删除")
	}
	// 幂等：再删一次不该报错。
	if err := m.RemoveUnit(); err != nil {
		t.Fatalf("重复删单元不该报错: %v", err)
	}
}

// is-active 对未运行的单元返回 3 —— 那是**正常反馈**，不是错误。
// 把它当成错误会让 uninstall / down 在"服务本来就没跑"时报一堆假警报。
func TestIsActiveTreatsNonZeroAsAnswer(t *testing.T) {
	m := &Manager{Runner: (&fakeRunner{
		codes: map[string]int{"is-active": 3},
		out:   map[string]string{"is-active": "inactive\n"},
	}).run}
	active, err := m.IsActive(context.Background())
	if err != nil {
		t.Fatalf("退出码 3 不该被当成错误: %v", err)
	}
	if active {
		t.Fatal("inactive 应当报告为未运行")
	}

	m2 := &Manager{Runner: (&fakeRunner{out: map[string]string{"is-active": "active\n"}}).run}
	if active, err := m2.IsActive(context.Background()); err != nil || !active {
		t.Fatalf("active 应当报告为运行中（active=%v err=%v）", active, err)
	}
}

// 单元文件必须 0644：systemd 读取它时可能已经降权，0600 会让单元加载失败。
func TestWriteUnitPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warpgo-watch.service")
	m := &Manager{Paths: Paths{UnitPath: path}, Runner: (&fakeRunner{}).run}
	if err := m.WriteUnit("x"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("单元权限应为 0644，实际 %v", fi.Mode().Perm())
	}
}

// 覆盖安装位置靠环境变量，验收脚本与单测都依赖这一点。
func TestDefaultPathsEnvOverride(t *testing.T) {
	t.Setenv(EnvUnitPath, "/tmp/x/warpgo-watch.service")
	t.Setenv(EnvBinPath, "/tmp/x/warpgo")
	p := DefaultPaths()
	if p.UnitPath != "/tmp/x/warpgo-watch.service" || p.BinPath != "/tmp/x/warpgo" {
		t.Fatalf("环境变量未生效: %+v", p)
	}

	// 清掉之后回到默认值。
	os.Unsetenv(EnvUnitPath)
	os.Unsetenv(EnvBinPath)
	p = DefaultPaths()
	if p.UnitPath != DefaultUnitPath || p.BinPath != DefaultBinPath {
		t.Fatalf("默认值不对: %+v", p)
	}
}

// 失败要带上 systemctl 的输出 —— 否则用户只能看到"退出码 1"，
// 而真正的原因（比如单元文件有语法错）就在输出里。
func TestMustSurfacesOutput(t *testing.T) {
	m := &Manager{Runner: (&fakeRunner{
		codes: map[string]int{"enable": 1},
		out:   map[string]string{"enable": "Failed to enable unit: File exists\n"},
	}).run}
	err := m.Enable(context.Background())
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "File exists") {
		t.Fatalf("错误信息里应当带上 systemctl 的输出，实际: %v", err)
	}
}
