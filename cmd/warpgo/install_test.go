package main

import (
	"strings"
	"testing"

	"github.com/misakacoo/warpgo/internal/state"
)

// TestInstallArgsFromRuntime 盯住"给 install 的那组参数是从运行态推出来的"。
//
// 这件事的后果很实际：单元里钉的参数与现场不一致时，保活下一轮就会按另一套
// 配置把隧道重建出来 —— 用户看到的是"装了个保活，隧道反倒变了样"。
func TestInstallArgsFromRuntime(t *testing.T) {
	rt := &state.Runtime{
		IfName:           "warp",
		Mode:             "global",
		Backend:          "userspace",
		BackendRequested: "auto",
		MTU:              1280,
		Endpoint:         "engage.cloudflareclient.com:2408",
		DNS:              []string{"1.1.1.1", "1.0.0.1"},
	}
	got := strings.Join(installArgsFromRuntime(rt), " ")

	for _, want := range []string{
		"--ifname warp",
		"--mode global",
		"--backend auto", // 用**要求**的那个，不是实际用上的 userspace
		"--mtu 1280",
		"--endpoint engage.cloudflareclient.com:2408",
		"--dns 1.1.1.1,1.0.0.1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("参数里应包含 %q，实得 %q", want, got)
		}
	}
	// 要求是 auto（= 让保活自己挑）时**不能**把实际值当成要求写进单元：
	// 那会把一次自动回落永久钉死成 userspace（state.Runtime 的注释）。
	if strings.Contains(got, "userspace") {
		t.Fatalf("应沿用 BackendRequested（auto）而不是实际值，实得 %q", got)
	}
}

// TestInstallArgsFromRuntimeFallbacks 覆盖几处"缺字段就别写"：
// 缺什么就少一个参数，绝不留一个空的 `--mode ""` 给 install 去解析。
func TestInstallArgsFromRuntimeFallbacks(t *testing.T) {
	rt := &state.Runtime{IfName: "warp"} // 老版本写的运行态：只有接口名
	if got := installArgsFromRuntime(rt); len(got) != 2 || got[0] != "--ifname" {
		t.Fatalf("只该给 --ifname warp，实得 %q", got)
	}

	// 没有 BackendRequested 时退回实际值（至少不是空的）。
	rt2 := &state.Runtime{IfName: "warp", Backend: "kernel"}
	if got := strings.Join(installArgsFromRuntime(rt2), " "); !strings.Contains(got, "--backend kernel") {
		t.Fatalf("该退回实际后端，实得 %q", got)
	}

	// 没有接口名（＝隧道没建成）→ 什么都不给，调用方据此跳过。
	if got := installArgsFromRuntime(&state.Runtime{Mode: "global"}); got != nil {
		t.Fatalf("没有接口名时该返回 nil，实得 %q", got)
	}
	if got := installArgsFromRuntime(nil); got != nil {
		t.Fatalf("nil 运行态该返回 nil，实得 %q", got)
	}
}
