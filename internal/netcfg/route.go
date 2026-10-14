package netcfg

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// ListTableRoutes 列出某个路由表里某个地址族的全部路由。
func ListTableRoutes(family, table int) ([]netlink.Route, error) {
	routes, err := netlink.RouteListFiltered(
		nlFamily(family),
		&netlink.Route{Table: table},
		netlink.RT_FILTER_TABLE,
	)
	if err != nil {
		return nil, fmt.Errorf("读取路由表 %d (family %d): %w", table, family, err)
	}
	return routes, nil
}

// FlushTable 清空某个路由表里某个地址族的全部路由，返回删除条数。
//
// 这是"先清后加"的另一半：因为每次 Apply 都会清空自己的表，
// 无论历史上有过多少条残留，结果都收敛到"正好一组"。
// 只清我们自己管的表号（TableWarp），不会碰到 main/local 这些系统表。
func FlushTable(family, table int) (int, error) {
	if table == TableMain || table == 255 /* local */ {
		return 0, fmt.Errorf("拒绝清空系统路由表 %d", table)
	}
	routes, err := ListTableRoutes(family, table)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for i := range routes {
		r := routes[i]
		if err := netlink.RouteDel(&r); err != nil && !errors.Is(err, unix.ESRCH) {
			if firstErr == nil {
				firstErr = fmt.Errorf("删除路由 %v: %w", r, err)
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// EnsureDefaultRoute 在 table 里把该族的默认路由指向 link（幂等）。
func EnsureDefaultRoute(link netlink.Link, family, table int) error {
	rt := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       toIPNet(DefaultPrefix(family)),
		Table:     table,
		Family:    nlFamily(family),
	}
	if err := netlink.RouteAdd(rt); err != nil {
		// 已存在视为成功：调用方通常已经先 FlushTable，走到这里说明并发或
		// 内核把它合并了，两者都不算失败。
		if errors.Is(err, unix.EEXIST) {
			return nil
		}
		return fmt.Errorf("在表 %d 里添加默认路由 -> %s: %w", table, link.Attrs().Name, err)
	}
	return nil
}

// DefaultRouteCount 返回表里默认路由的条数（验收用：幂等性检查）。
func DefaultRouteCount(family, table int) (int, error) {
	routes, err := ListTableRoutes(family, table)
	if err != nil {
		return 0, err
	}
	want := DefaultPrefix(family).String()
	n := 0
	for _, r := range routes {
		if r.Dst == nil { // netlink 用 nil 表示默认路由
			n++
			continue
		}
		if p, err := netip.ParsePrefix(r.Dst.String()); err == nil && p.Masked().String() == want {
			n++
		}
	}
	return n, nil
}

// DefaultRouteDev 返回系统主表里该族默认路由所用的出口设备名；没有则返回空串。
//
// 回滚之后要拿它做校验：默认路由绝不能再指向我们删掉的那个接口。
func DefaultRouteDev(family int) (string, error) {
	routes, err := ListTableRoutes(family, TableMain)
	if err != nil {
		return "", err
	}
	for _, r := range routes {
		if !isDefaultRoute(r) {
			continue
		}
		link, lerr := netlink.LinkByIndex(r.LinkIndex)
		if lerr != nil {
			return "", nil
		}
		return link.Attrs().Name, nil
	}
	return "", nil
}

func isDefaultRoute(r netlink.Route) bool {
	if r.Dst == nil {
		return true
	}
	ones, bits := r.Dst.Mask.Size()
	return bits > 0 && ones == 0
}
