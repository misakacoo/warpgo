// Package tunnel 抽象"一条 WireGuard 隧道"，把内核实现与用户态实现藏在同一个接口后面。
//
// 分层的意义：上层的 netcfg 完全不知道隧道是内核建的还是用户态建的；
// 将来 Cloudflare 若把传输换成 MASQUE，只需新增一个实现，其余层不动。
package tunnel

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/wgconf"
)

// Backend 是隧道后端。空字符串表示"由调用方决定"（见 ParseBackend）。
type Backend string

const (
	// BackendKernel 用内核 WireGuard 模块（性能最好，但设不了 reserved）。
	BackendKernel Backend = "kernel"
	// BackendUserspace 用内嵌的 wireguard-go（能在报文里带上 reserved）。
	BackendUserspace Backend = "userspace"
)

// BackendAuto 是"没有指定后端"的取值。
//
// 它不是一个真的后端，只是把"用户没说话"与"用户说要内核"区分开 ——
// 前者允许自动回落，后者必须严格照办（否则用户会以为开关没生效）。
const BackendAuto Backend = ""

// ParseBackend 解析 --backend 的取值。
//
// 接受 `reserved` 作为 userspace 的别名：在 WARP 语境里"带 reserved 的那个后端"
// 就是用户态实现，菜单 `k` 与生态里的叫法都是它。
func ParseBackend(s string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return BackendAuto, nil
	case "kernel", "k":
		return BackendKernel, nil
	case "userspace", "user", "reserved", "go":
		return BackendUserspace, nil
	default:
		return "", fmt.Errorf("不认识的后端 %q（可用 auto / kernel / userspace）", s)
	}
}

// Valid 报告它是不是一个已知的具体后端。
func (b Backend) Valid() bool {
	return b == BackendKernel || b == BackendUserspace
}

// Config 是建立一条隧道所需的全部参数。
type Config struct {
	IfName        string
	PrivateKey    wgconf.Key
	PublicKey     wgconf.Key
	PeerPublicKey wgconf.Key

	// Endpoint 是 "host:port"。可以是域名，但**优先传字面量** ——
	// 解析域名要过 DNS，而 DNS 恰恰是本项目历史上最常出问题的一环。
	Endpoint string

	AllowedIPs []netip.Prefix
	Keepalive  time.Duration
	MTU        int
	Addresses  []netip.Prefix

	// Reserved 是握手报文里紧跟报文头的那 3 个字节（见 cloudflare.Reserved）。
	//
	// 它在**用户态后端**里由守护进程在下发前写进每一个出网报文；
	// 内核后端在结构上做不到（那 3 个字节不在内核的接口里），
	// 因此当它非零而当前是内核后端时，上层会得到一条明确的提示。
	Reserved cloudflare.Reserved

	// FWMark 非零时设置 fwmark（全局模式防环路用，M3）。
	FWMark int
}

// Status 是隧道的当前状态。
type Status struct {
	Exists     bool
	IfName     string
	PublicKey  string
	ListenPort int
	FWMark     int
	Peers      []PeerStatus
}

// PeerStatus 是单个对端的状态。
type PeerStatus struct {
	PublicKey       string
	Endpoint        string
	AllowedIPs      []string
	LatestHandshake time.Time
	RxBytes         int64
	TxBytes         int64
	Keepalive       time.Duration
}

// HasHandshake 报告是否已经与对端完成过握手（判断"隧道真的通了"的依据之一）。
func (s Status) HasHandshake() bool {
	for _, p := range s.Peers {
		if !p.LatestHandshake.IsZero() {
			return true
		}
	}
	return false
}

// Tunnel 是一条隧道。
type Tunnel interface {
	// Backend 返回后端类型。
	Backend() Backend

	// Ensure 让隧道达到 cfg 描述的状态。
	//
	// 必须是**幂等且温和**的：接口已存在就复用（不重建、不换 IP），
	// 配置已经一致就连 WireGuard 参数都不重下发（避免无谓的重握手）。
	// 上游每 5 分钟无条件重建接口，用户看到的就是出口 IP 一直在变。
	//
	// 用户态后端的 Ensure 还负责"守护进程该不该在跑"这一层：
	// 没跑就拉起来、参数（MTU / reserved）变了就换一个。
	Ensure(ctx context.Context, cfg Config) (Change, error)

	// TearDown 拆除隧道（只删接口与守护进程）。规则与路由由 netcfg 负责。
	// 返回是否真的删掉了东西。
	TearDown(ctx context.Context) (bool, error)

	// Status 读回当前状态。
	Status(ctx context.Context) (Status, error)

	// Close 释放句柄（不代表拆隧道）。
	Close() error
}

// Change 记录 Ensure 实际改动了什么（供日志使用，也让幂等性可被观测）。
type Change struct {
	LinkCreated   bool
	AddrsAdded    int
	LinkUp        bool
	Configured    bool
	ConfigMatched bool

	// DaemonStarted 表示这次真的拉起了一个新的用户态守护进程
	// （内核后端恒为 false）。它同时也是 LinkCreated 的来源之一。
	DaemonStarted bool
}

// Any 报告本次是否有任何改动。
func (c Change) Any() bool {
	return c.LinkCreated || c.AddrsAdded > 0 || c.LinkUp || c.Configured || c.DaemonStarted
}

// New 按后端类型构造一条隧道。
//
// 这是两个后端唯一的"分岔点"：造出来之后，上层（netcfg / health / keepalive）
// 再也看不到它们的区别。reserved 不在这里传 —— 它属于一次具体配置，
// 由 Ensure 的 Config 提供，免得出现"构造时一个值、下发时另一个值"。
func New(be Backend, ifName string) (Tunnel, error) {
	switch be {
	case BackendUserspace:
		return NewUserspace(ifName)
	case BackendKernel:
		return NewKernel(ifName)
	default:
		return nil, fmt.Errorf("不认识的后端 %q", string(be))
	}
}

// SupportsReserved 报告该后端能不能把 reserved 写进报文。
//
// 只有用户态实现可以：那 3 个字节既不在官方 UAPI 键集内，
// 也不在内核的 netlink 属性里 —— 这是内核接口层面的既成事实，不是实现缺口。
func SupportsReserved(be Backend) bool { return be == BackendUserspace }
