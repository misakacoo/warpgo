package netcfg

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

// toIPNet 把 netip.Prefix 转成 net.IPNet（vishvananda/netlink 用的类型）。
func toIPNet(p netip.Prefix) *net.IPNet {
	if !p.IsValid() {
		return nil
	}
	return &net.IPNet{
		IP:   p.Addr().AsSlice(),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}

func nlFamily(family int) int {
	if family == FamilyV6 {
		return netlink.FAMILY_V6
	}
	return netlink.FAMILY_V4
}

// LinkExists 报告接口是否存在。
func LinkExists(name string) bool {
	_, err := netlink.LinkByName(name)
	return err == nil
}

// IsNotExist 报告错误是否表示"接口不存在"。
func IsNotExist(err error) bool {
	var nf netlink.LinkNotFoundError
	return errors.As(err, &nf)
}

// EnsureWireGuardLink 确保一个 wireguard 类型的接口存在；已存在则复用。
//
// 复用而不是重建，是因为**重建会换掉 WARP 出口 IP**（设备按公钥分配地址，
// 但每次重注册会重新分配）。上游每 5 分钟无条件重建一次接口，用户看到的就是
// "访问的网站每 5 分钟看到一个不同的 IP"。幂等复用正好从根上消除这个行为。
//
// 返回值 created 表示这次是否真的新建了接口。
func EnsureWireGuardLink(name string, mtu int) (link netlink.Link, created bool, err error) {
	if l, err := netlink.LinkByName(name); err == nil {
		// 名字被别的东西占用了 —— 绝不能往上写 WARP 配置，直接拒绝。
		if _, ok := l.(*netlink.Wireguard); !ok {
			return nil, false, fmt.Errorf("接口 %s 已存在但不是 wireguard 类型（%T），拒绝复用", name, l)
		}
		if mtu > 0 && l.Attrs().MTU != mtu {
			if err := netlink.LinkSetMTU(l, mtu); err != nil {
				return nil, false, fmt.Errorf("把 %s 的 MTU 改为 %d: %w", name, mtu, err)
			}
		}
		return l, false, nil
	}

	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	if mtu > 0 {
		attrs.MTU = mtu
	}
	if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: attrs}); err != nil {
		return nil, false, fmt.Errorf("创建 wireguard 接口 %s: %w", name, err)
	}

	// LinkAdd 返回的 attrs 不含内核补齐的信息（ifindex 等），重新取一次。
	l, err := netlink.LinkByName(name)
	if err != nil {
		return nil, true, fmt.Errorf("创建后无法取到接口 %s: %w", name, err)
	}
	return l, true, nil
}

// SetLinkUp 把接口置为 up。
func SetLinkUp(link netlink.Link) error {
	if link.Attrs().Flags&net.FlagUp != 0 {
		return nil
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("启用接口 %s: %w", link.Attrs().Name, err)
	}
	return nil
}

// DeleteLink 删除接口。接口不存在时返回 (false, nil)。
func DeleteLink(name string) (bool, error) {
	l, err := netlink.LinkByName(name)
	if err != nil {
		if IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("查找接口 %s: %w", name, err)
	}
	if err := netlink.LinkDel(l); err != nil {
		return false, fmt.Errorf("删除接口 %s: %w", name, err)
	}
	return true, nil
}

// HasAddr 报告接口上是否已经有该地址。
func HasAddr(link netlink.Link, p netip.Prefix) (bool, error) {
	if !p.IsValid() {
		return false, nil
	}
	addrs, err := netlink.AddrList(link, nlFamily(familyOfAddr(p.Addr())))
	if err != nil {
		return false, fmt.Errorf("读取 %s 的地址: %w", link.Attrs().Name, err)
	}
	want := p.Masked().String()
	for _, a := range addrs {
		if a.IPNet == nil {
			continue
		}
		got, err := netip.ParsePrefix(a.IPNet.String())
		if err != nil {
			continue
		}
		if got.Masked().String() == want {
			return true, nil
		}
	}
	return false, nil
}

// EnsureAddr 确保接口上有该地址（幂等）。
func EnsureAddr(link netlink.Link, p netip.Prefix) (added bool, err error) {
	ok, err := HasAddr(link, p)
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	a := &netlink.Addr{IPNet: toIPNet(p)}
	if err := netlink.AddrAdd(link, a); err != nil {
		return false, fmt.Errorf("给 %s 添加地址 %s: %w", link.Attrs().Name, p, err)
	}
	return true, nil
}

// RemoveAddr 删除接口上的某个地址；不存在时返回 false。
func RemoveAddr(link netlink.Link, p netip.Prefix) (bool, error) {
	ok, err := HasAddr(link, p)
	if err != nil || !ok {
		return false, err
	}
	a := &netlink.Addr{IPNet: toIPNet(p)}
	if err := netlink.AddrDel(link, a); err != nil {
		return false, fmt.Errorf("删除 %s 上的地址 %s: %w", link.Attrs().Name, p, err)
	}
	return true, nil
}
