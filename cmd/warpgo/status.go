package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/state"
	"github.com/misakacoo/warpgo/internal/tunnel"
)

// statusView 是 `status --json` 的输出形状（不含任何机密）。
type statusView struct {
	IfName     string     `json:"if_name"`
	Mode       string     `json:"mode"`
	Backend    string     `json:"backend"`
	MTU        int        `json:"mtu"`
	Table      int        `json:"table"`
	Families   []int      `json:"families,omitempty"`
	Addrs      []string   `json:"addresses,omitempty"`
	Endpoint   string     `json:"endpoint,omitempty"`
	UpSince    time.Time  `json:"up_since"`
	Exists     bool       `json:"interface_exists"`
	WGPublic   string     `json:"wg_public_key,omitempty"`
	WGPort     int        `json:"wg_listen_port,omitempty"`
	Handshaked bool       `json:"handshaked"`
	Peers      []peerView `json:"peers,omitempty"`
}

type peerView struct {
	PublicKey       string   `json:"public_key,omitempty"`
	Endpoint        string   `json:"endpoint,omitempty"`
	AllowedIPs      []string `json:"allowed_ips,omitempty"`
	Keepalive       string   `json:"persistent_keepalive,omitempty"`
	LatestHandshake string   `json:"latest_handshake,omitempty"`
	RxBytes         int64    `json:"rx_bytes"`
	TxBytes         int64    `json:"tx_bytes"`
}

func (a *app) cmdStatus(args []string) int {
	var asJSON bool
	opts := newOptionSet().Bool("--json", &asJSON)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil {
		if errors.Is(err, state.ErrNoRuntime) {
			// 「没在跑」既不是崩溃也不是健康：用退出码 2（体检不健康）表达，
			// 这样脚本能区分"明确没起"与"起了但有问题"。
			a.log.Errorf("%s", msg.T(msg.KeyStatusNoRuntime))
			return exitUnhealthy
		}
		return a.fail(err)
	}

	tun, err := tunnel.NewKernel(rt.IfName)
	if err != nil {
		return a.fail(err)
	}
	defer tun.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := tun.Status(ctx)
	if err != nil {
		return a.fail(err)
	}

	if asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(newStatusView(rt, st)); err != nil {
			return a.fail(err)
		}
	} else {
		a.printStatus(rt, st)
	}

	if !st.Exists {
		a.log.Errorf("%s", msg.Tf(msg.KeyStatusIfaceGone, rt.IfName))
		return exitUnhealthy
	}
	return exitOK
}

func newStatusView(rt *state.Runtime, st tunnel.Status) statusView {
	v := statusView{
		IfName:     rt.IfName,
		Mode:       rt.Mode,
		Backend:    rt.Backend,
		MTU:        rt.MTU,
		Table:      rt.Table,
		Families:   rt.Families,
		Addrs:      rt.Addrs,
		Endpoint:   rt.Endpoint,
		UpSince:    rt.UpSince,
		Exists:     st.Exists,
		WGPublic:   st.PublicKey,
		WGPort:     st.ListenPort,
		Handshaked: st.HasHandshake(),
	}
	for _, p := range st.Peers {
		pv := peerView{
			PublicKey:  p.PublicKey,
			Endpoint:   p.Endpoint,
			AllowedIPs: p.AllowedIPs,
			RxBytes:    p.RxBytes,
			TxBytes:    p.TxBytes,
		}
		if p.Keepalive > 0 {
			pv.Keepalive = p.Keepalive.String()
		}
		if !p.LatestHandshake.IsZero() {
			pv.LatestHandshake = p.LatestHandshake.UTC().Format(time.RFC3339)
		}
		v.Peers = append(v.Peers, pv)
	}
	return v
}

// printStatus 打印人类可读的状态。
//
// 字段名用 ASCII 固定英文：它们与 runtime.json 里的键一一对应，
// 翻译反而会让人对不上文件内容。
func (a *app) printStatus(rt *state.Runtime, st tunnel.Status) {
	fmt.Fprintln(a.stdout, msg.T(msg.KeyStatusTitle))

	fields := [][2]string{
		{"if_name", rt.IfName},
		{"mode", rt.Mode},
		{"backend", rt.Backend},
		{"mtu", fmt.Sprint(rt.MTU)},
		{"table", fmt.Sprint(rt.Table)},
		{"addresses", joinOrDash(strings.Join(rt.Addrs, ", "))},
		{"endpoint", joinOrDash(rt.Endpoint)},
		{"up_since", rt.UpSince.UTC().Format(time.RFC3339)},
		{"interface_exists", yesNo(st.Exists)},
	}
	if st.Exists {
		fields = append(fields,
			[2]string{"wg_public_key", joinOrDash(st.PublicKey)},
			[2]string{"wg_listen_port", fmt.Sprint(st.ListenPort)},
		)
		hs := msg.T(msg.KeyStatusNoHandshake)
		for _, p := range st.Peers {
			if !p.LatestHandshake.IsZero() {
				hs = p.LatestHandshake.UTC().Format(time.RFC3339)
				break
			}
		}
		fields = append(fields,
			[2]string{"handshake", hs},
			[2]string{"peer_endpoint", joinOrDash(peerEndpoint(st))},
			[2]string{"peer_rx_tx", fmt.Sprintf("%s / %s", humanBytes(rxBytes(st)), humanBytes(txBytes(st)))},
		)
	}
	for _, f := range fields {
		fmt.Fprintf(a.stdout, "  %-18s %s\n", f[0], f[1])
	}
}

func peerEndpoint(st tunnel.Status) string {
	for _, p := range st.Peers {
		if p.Endpoint != "" {
			return p.Endpoint
		}
	}
	return ""
}

func rxBytes(st tunnel.Status) int64 {
	var n int64
	for _, p := range st.Peers {
		n += p.RxBytes
	}
	return n
}

func txBytes(st tunnel.Status) int64 {
	var n int64
	for _, p := range st.Peers {
		n += p.TxBytes
	}
	return n
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// humanBytes 把字节数格式化成人看的形式。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	for i, u := range units {
		v /= unit
		if v < unit || i == len(units)-1 {
			return fmt.Sprintf("%.2f %s", v, u)
		}
	}
	return fmt.Sprintf("%d B", n)
}
