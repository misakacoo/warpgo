package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misakacoo/warpgo/internal/logx"
	"github.com/misakacoo/warpgo/internal/state"
)

// newTestApp 造一个把输出丢到 /dev/null 的 app。
//
// 注意 app 的 stdout/stderr 是 *os.File（不是 io.Writer），所以用 os.DevNull
// 而不是 io.Discard —— 菜单状态那一行只看返回值，但万一它以后开始打日志，
// 也不该污染测试输出。
func newTestApp(t *testing.T) *app {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	return &app{
		log:    logx.New(devnull, logx.LevelError),
		stdout: devnull,
		stderr: devnull,
	}
}

func writeStateFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state.Dir(), name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMenuStatusLineOnCleanMachine 覆盖菜单最常见的起点：什么都没建。
//
// 这一行必须能打出来 —— 恰恰是"隧道没起来、账户也没注册"的时候，
// 用户最需要看到菜单（而不是被一个报错挡住）。
func TestMenuStatusLineOnCleanMachine(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())

	got := newTestApp(t).menuStatusLine()
	if !strings.Contains(got, "隧道未建立") {
		t.Fatalf("没有运行态时应显示「隧道未建立」，实得 %q", got)
	}
	if !strings.Contains(got, "未注册") {
		t.Fatalf("没有账户时应显示「未注册」，实得 %q", got)
	}
}

func TestMenuStatusLineWithRuntimeAndAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)

	writeStateFile(t, state.RuntimeFile, `{
		"if_name": "warp",
		"mode": "non-global",
		"backend": "kernel",
		"mtu": 1280,
		"table": 51820,
		"families": [4, 6],
		"addrs": ["172.16.0.2/32", "2606:4700:110::1/128"],
		"endpoint": "engage.cloudflareclient.com:2408",
		"up_since": "2026-10-15T00:00:00Z"
	}`)
	writeStateFile(t, state.AccountFile, `{
		"id": "abc", "account_type": "free",
		"private_key": "x", "peer_public_key": "y",
		"endpoint": "engage.cloudflareclient.com:2408"
	}`)

	got := newTestApp(t).menuStatusLine()
	for _, want := range []string{"隧道 warp", "模式 non-global", "后端 kernel", "172.16.0.2/32", "账户 free"} {
		if !strings.Contains(got, want) {
			t.Fatalf("状态行里应包含 %q，实得 %q", want, got)
		}
	}
	// 单栈 / 无地址时不该出现空的分段。
	if strings.Contains(got, "地址 ·") || strings.HasSuffix(got, "·") {
		t.Fatalf("状态行里不该有空分段: %q", got)
	}
}

// TestMenuStatusLineIgnoresBrokenRuntime 是本条最要紧的性质：
// 运行态文件坏了（半截写入、手工改坏）不该让菜单打不开 ——
// 那正是用户需要菜单来"重来一次"的时候。
func TestMenuStatusLineIgnoresBrokenRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp"`) // 截断的 JSON

	got := newTestApp(t).menuStatusLine()
	if !strings.Contains(got, "隧道未建立") {
		t.Fatalf("运行态读不出来时应退化成「隧道未建立」，实得 %q", got)
	}
}

// TestMenuStatusLineNoAddrsOmitsSegment 覆盖"隧道起来了但地址列表为空"
// （理论上不该发生，但状态行不该因此多出一个空分段）。
func TestMenuStatusLineNoAddrsOmitsSegment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(state.EnvDir, dir)
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp", "mode": "global", "backend": "userspace"}`)

	got := newTestApp(t).menuStatusLine()
	if strings.Contains(got, "地址") {
		t.Fatalf("没有地址时不该有「地址」分段: %q", got)
	}
	if !strings.Contains(got, "隧道 warp") || !strings.Contains(got, "后端 userspace") {
		t.Fatalf("状态行不对: %q", got)
	}
}
