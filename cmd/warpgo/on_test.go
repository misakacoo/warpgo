package main

import (
	"strings"
	"testing"

	"github.com/misakacoo/warpgo/internal/state"
)

// onDefaults 是"恢复"能回到原配置的全部依据，逐条钉住。
//
// 它唯一的输入是 runtime.json；错了不会报错，只会静默地按**别的**配置重建 ——
// 那正是最难发现的一类问题。

func TestOnDefaultsWithoutRuntime(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())

	if got := newTestApp(t).onDefaults(); got != nil {
		t.Fatalf("没有运行态时应当返回 nil（交给 up 按账户默认建立），实得 %v", got)
	}
}

func TestOnDefaultsFull(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())
	writeStateFile(t, state.RuntimeFile, `{
		"if_name": "warp",
		"mode": "global",
		"backend": "userspace",
		"backend_requested": "auto",
		"mtu": 1280,
		"table": 51820,
		"families": [4, 6],
		"addrs": ["172.16.0.2/32"],
		"endpoint": "engage.cloudflareclient.com:2408",
		"stack": "both",
		"dns": ["1.1.1.1", "2606:4700:4700::1111"],
		"up_since": "2026-10-15T00:00:00Z"
	}`)

	got := strings.Join(newTestApp(t).onDefaults(), " ")
	for _, want := range []string{
		"--ifname warp",
		"--mode global",
		// 关键是 **auto**：用户当初要的是"随便你挑"，照搬实际值
		// 会把自动降级的结果钉成一个永久选择。
		"--backend auto",
		"--mtu 1280",
		"--stack both",
		"--endpoint engage.cloudflareclient.com:2408",
		"--dns 1.1.1.1,2606:4700:4700::1111",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("恢复参数里应包含 %q，实得 %q", want, got)
		}
	}
	if strings.Contains(got, "--backend userspace") {
		t.Fatalf("应当用 backend_requested（auto）而不是实际值：%q", got)
	}
}

// TestOnDefaultsFallsBackToActualBackend 覆盖老运行态文件（没有
// backend_requested 字段）：那时只能退回实际值，但**不能什么都不传** ——
// 不传就意味着"随便挑"，与当初钉死的选择不是一回事。
func TestOnDefaultsFallsBackToActualBackend(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())
	writeStateFile(t, state.RuntimeFile, `{
		"if_name": "warp", "mode": "non-global",
		"backend": "kernel", "mtu": 1420,
		"table": 51820, "addrs": ["172.16.0.2/32"],
		"endpoint": "e:2408", "up_since": "2026-10-15T00:00:00Z"
	}`)

	got := strings.Join(newTestApp(t).onDefaults(), " ")
	if !strings.Contains(got, "--backend kernel") {
		t.Fatalf("没有 backend_requested 时应退回实际值，实得 %q", got)
	}
}

func TestOnDefaultsSkipsEmptyFields(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp"}`)

	got := newTestApp(t).onDefaults()
	// `--stack` 是唯一一个"即使运行态里没有也会传"的：空值归一成 both，
	// 而 `on` 的语义就是"按上次那份配置原样恢复"，把栈显式写出来比依赖继承更清楚。
	if strings.Join(got, " ") != "--ifname warp --stack both" {
		t.Fatalf("只有接口名时应只传 --ifname 与 --stack，实得 %v", got)
	}
	// 其余字段为空时**一个字都不能传** —— 那会把 0 与空串当成用户的选择，
	// 覆盖掉 up 内部的默认值。
	for _, bad := range []string{"--mtu", "--endpoint", "--mode", "--backend", "--dns"} {
		for _, a := range got {
			if a == bad {
				t.Fatalf("不该出现 %s（值为空）：%v", bad, got)
			}
		}
	}
}

func TestOnDefaultsIgnoresBrokenRuntime(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())
	writeStateFile(t, state.RuntimeFile, `{"if_name": "warp"`) // 截断的 JSON

	if got := newTestApp(t).onDefaults(); got != nil {
		t.Fatalf("运行态坏了应当退化成 nil（按账户默认建立），实得 %v", got)
	}
}

// TestOnDefaultsCarriesStack 是本轮加栈之后最要紧的一条：
// 用户选了"只用 IPv4"，`off` / `on` 一个来回之后必须还是"只用 IPv4"。
// 丢了这一条，症状是"隧道照样通，只是悄悄多了一族"——极难发现。
func TestOnDefaultsCarriesStack(t *testing.T) {
	t.Setenv(state.EnvDir, t.TempDir())
	writeStateFile(t, state.RuntimeFile, `{
		"if_name": "warp", "mode": "non-global", "backend": "kernel",
		"stack": "4", "mtu": 1280, "table": 51820,
		"addrs": ["172.16.0.2/32"], "endpoint": "e:2408",
		"up_since": "2026-10-15T00:00:00Z"
	}`)

	got := strings.Join(newTestApp(t).onDefaults(), " ")
	if !strings.Contains(got, "--stack 4") {
		t.Fatalf("恢复参数里应保留栈 4，实得 %q", got)
	}
}
