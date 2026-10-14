// Package tunnel 抽象"一条 WireGuard 隧道"，把内核实现与用户态实现藏在同一个接口后面。
//
// 分层的意义：上层的 netcfg 完全不知道隧道是内核建的还是用户态建的；
// 将来 Cloudflare 若把传输换成 MASQUE，只需新增一个实现，其余层不动。
package tunnel

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/wgconf"
)

// Backend 是隧道后端。
type Backend string

const (
	// BackendKernel 用内核 WireGuard 模块（性能最好，但设不了 reserved）。
	BackendKernel Backend = "kernel"
	// BackendUserspace 用内嵌的 wireguard-go（能在握手报文里带 reserved）。
	BackendUserspace Backend = "userspace"
)

// ErrReservedUnsupported 表示当前后端无法设置 reserved。
//
// 这不是"还没写"的占位：**内核 WireGuard 在设计上就设不了那 3 个字节** ——
// 它们不在官方 UAPI 键集内，也不在内核的 netlink 接口里。需要 reserved 的地区
// （HK / JP / LA 的某些出口）必须换用户态后端。
var ErrReservedUnsupported = errors.New(
	"内核 WireGuard 无法设置 reserved（那 3 个字节不在内核接口里）；需要 reserved 的地区要用用户态后端")

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
	Ensure(ctx context.Context, cfg Config) (Change, error)

	// TearDown 拆除隧道（只删接口）。规则与路由由 netcfg 负责。
	// 返回是否真的删掉了东西。
	TearDown(ctx context.Context) (bool, error)

	// SetReserved 设置握手报文的 reserved 字节。内核后端返回 ErrReservedUnsupported。
	SetReserved(ctx context.Context, r cloudflare.Reserved) error

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
}

// Any 报告本次是否有任何改动。
func (c Change) Any() bool {
	return c.LinkCreated || c.AddrsAdded > 0 || c.LinkUp || c.Configured
}
