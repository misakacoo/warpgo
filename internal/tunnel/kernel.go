package tunnel

import (
	"context"
	"fmt"

	"github.com/misakacoo/warpgo/internal/netcfg"
	"golang.zx2c4.com/wireguard/wgctrl"
)

// kernelTunnel 是内核 WireGuard 后端。
//
// 接口的创建/地址/MTU 走 netlink（vishvananda），WireGuard 参数走 wgctrl。
// 本文件里只剩"内核特有"的那几件事 —— 建接口、删接口、读状态；
// 地址 / 配置 / 比对都在 common.go 里与用户态后端共用。
type kernelTunnel struct {
	ifName string
	client *wgctrl.Client
}

// NewKernel 创建一个内核后端。
//
// 它的 `Status` / `Close` 对**用户态设备同样有效** —— wgctrl 会按名字去找
// `/var/run/wireguard/<if>.sock`，所以 `status` 命令不必先判断当前是哪一种后端。
// （只有 `Ensure` 是真正"内核专属"的：它走 netlink 建 wireguard 类型的接口。）
func NewKernel(ifName string) (*kernelTunnel, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("初始化 WireGuard 客户端: %w", err)
	}
	return &kernelTunnel{ifName: ifName, client: c}, nil
}

func (k *kernelTunnel) Backend() Backend { return BackendKernel }

func (k *kernelTunnel) Close() error { return k.client.Close() }

// Ensure 让隧道达到期望状态，且尽力少动东西。
//
// 顺序：建接口 → 加地址 → 起接口 → 下发 WireGuard 配置。
// 每一步都先判断"是否已经是要的样子"，只在需要时才动手。
//
// 注意这里**不因为 reserved 非零而失败**：内核结构上带不了那 3 个字节，
// 但绝大多数地区并不需要它 —— 要不要改用用户态后端是上层（CLI）的决定，
// 它在"内核实测握不上手"时才会换。库层在这里报错会让"内核优先"无从谈起。
func (k *kernelTunnel) Ensure(ctx context.Context, cfg Config) (Change, error) {
	var ch Change
	name := cfg.IfName
	if name == "" {
		name = k.ifName
	}

	link, created, err := netcfg.EnsureWireGuardLink(name, cfg.MTU)
	if err != nil {
		return ch, err
	}
	ch.LinkCreated = created

	added, up, err := applyAddrsAndUp(link, cfg)
	ch.AddrsAdded = added
	ch.LinkUp = up
	if err != nil {
		return ch, err
	}

	matched, sent, err := ensureConfig(k.client, cfg)
	if err != nil {
		return ch, err
	}
	ch.ConfigMatched, ch.Configured = matched, sent
	return ch, nil
}

func (k *kernelTunnel) TearDown(ctx context.Context) (bool, error) {
	return netcfg.DeleteLink(k.ifName)
}

func (k *kernelTunnel) Status(ctx context.Context) (Status, error) {
	return readStatus(k.client, k.ifName)
}
