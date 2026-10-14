package main

import (
	"testing"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// 后端候选顺序是 M5 里最关键的一条策略："reserved 一等公民"到底怎么落地。
//
// 逐条钉住它，是因为它同时决定了三件事：性能（能用内核就用内核）、
// 可用性（需要 reserved 的地区要能自己找到出路）、可预期性（用户指定了就别自作主张）。
func TestResolveBackends(t *testing.T) {
	withReserved := cloudflare.Reserved{36, 212, 144}
	zero := cloudflare.Reserved{}

	cases := []struct {
		name      string
		requested tunnel.Backend
		reserved  cloudflare.Reserved
		want      []tunnel.Backend
	}{
		{
			name:      "用户指定内核 → 只有一个候选，不做回落",
			requested: tunnel.BackendKernel,
			reserved:  withReserved,
			want:      []tunnel.Backend{tunnel.BackendKernel},
		},
		{
			name:      "用户指定用户态 → 同样只有一个候选",
			requested: tunnel.BackendUserspace,
			reserved:  zero,
			want:      []tunnel.Backend{tunnel.BackendUserspace},
		},
		{
			name:      "auto 且没有 reserved → 只有内核（与 M2/M4 行为一致）",
			requested: tunnel.BackendAuto,
			reserved:  zero,
			want:      []tunnel.Backend{tunnel.BackendKernel},
		},
		{
			name:      "auto 且带 reserved → 内核优先、用户态兜底",
			requested: tunnel.BackendAuto,
			reserved:  withReserved,
			want:      []tunnel.Backend{tunnel.BackendKernel, tunnel.BackendUserspace},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBackends(tc.requested, tc.reserved)
			if len(got) != len(tc.want) {
				t.Fatalf("实得 %v，期望 %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 个候选 = %q，期望 %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// "换到了一个不同的 IP"的判据。它必须只要求**至少一族**变化 ——
// 单栈账户只有一族，要求全部变化会让 rotate 永远认为没换成。
func TestAddressesDiffer(t *testing.T) {
	base := &cloudflare.Account{
		AddrV4: "172.16.0.2/32",
		AddrV6: "2606:4700:110:89ab:41b:83cd:f36:4865/128",
	}
	same := &cloudflare.Account{
		AddrV4: "172.16.0.2/32",
		AddrV6: "2606:4700:110:89ab:41b:83cd:f36:4865/128",
	}
	v4Only := &cloudflare.Account{
		AddrV4: "172.16.0.9/32",
		AddrV6: "2606:4700:110:89ab:41b:83cd:f36:4865/128",
	}
	v6Only := &cloudflare.Account{
		AddrV4: "172.16.0.2/32",
		AddrV6: "2606:4700:110::9/128",
	}
	singleStack := &cloudflare.Account{AddrV4: "172.16.0.2/32"}

	cases := []struct {
		name string
		old  *cloudflare.Account
		cand *cloudflare.Account
		want bool
	}{
		{"完全一样 → 没换成", base, same, false},
		{"只 v4 变了 → 换了", base, v4Only, true},
		{"只 v6 变了 → 换了", base, v6Only, true},
		{"地址族数量变了 → 换了", base, singleStack, true},
		{"单栈对单栈、同样地址 → 没换成", singleStack, &cloudflare.Account{AddrV4: "172.16.0.2/32"}, false},
		{"旧账户为空（理论上不该发生）→ 当作换了", nil, base, true},
		{"新账户为空 → 当作换了", base, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := addressesDiffer(tc.old, tc.cand); got != tc.want {
				t.Errorf("addressesDiffer = %v，期望 %v", got, tc.want)
			}
		})
	}
}
