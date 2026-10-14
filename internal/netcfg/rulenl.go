package netcfg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// 这一段是**自己实现**的策略路由规则读写，没有用 vishvananda/netlink 的 Rule*。
//
// 原因（真机实测，不是猜的）：在本项目验证过的内核（6.18.55-x86-64-xanmod）上，
// vishvananda/netlink v1.3.1 的 RuleAdd 对**任何**输入都返回 EINVAL ——
// 连 `{Family: V4, Priority: 9010}` 这种最简输入都失败；而同样内容
// 用 `ip rule add` 或这里手工构造的 netlink 消息都成功。它的 RuleList（读）是好的。
//
// 规则是本项目最要紧的一处（幂等与零残留全押在它上面），所以宁可自带一份
// 两三百行的实现，也不把正确性押在第三方库对新内核的适配上。
// 顺带的好处：编码/解码是纯粹的字节操作，可以脱离 root 与真实内核做单测。

// 属性号来自 linux/rtnetlink.h 的 FRA_* 枚举。
const (
	fraSrc        = 2
	fraPriority   = 6
	fraTable      = 15
	fraSuppress   = 14
	fraOifName    = 17
	fraProtocol   = 21
	fraFwMark     = 10
	fraDst        = 1
	fraIIfName    = 3
	fraFwMask     = 16
	fraFlow       = 11
	fraTunID      = 12
	fraL3Mdev     = 19
	fraUIDRange   = 20
	fraIPProto    = 22
	fraSportRange = 23
	fraDPortRange = 24
)

// FR_ACT_TO_TBL：规则的动作是"查表"。
const actToTable = 1

// fibRuleHdrLen 是 struct fib_rule_hdr 的固定长度。
const fibRuleHdrLen = 12

// suppressUnset 是内核用来表示"没有 suppress_prefixlength 属性"的哨兵值。
const suppressUnset = 0xFFFFFFFF

// ErrRuleExists 表示同名（同 family + pref）的规则已存在。
var ErrRuleExists = errors.New("规则已存在")

func afFamily(family int) uint8 {
	if family == FamilyV6 {
		return unix.AF_INET6
	}
	return unix.AF_INET
}

func familyFromAF(af uint8) int {
	if af == unix.AF_INET6 {
		return FamilyV6
	}
	return FamilyV4
}

// addrLen 返回该地址族的地址字节数。
func addrLen(family int) int {
	if family == FamilyV6 {
		return 16
	}
	return 4
}

// srcMaskLen 返回单个源地址对应的前缀长度（v4 → 32，v6 → 128）。
func srcMaskLen(family int) uint8 {
	if family == FamilyV6 {
		return 128
	}
	return 32
}

// ---------------------------------------------------------------------------
// 编码
// ---------------------------------------------------------------------------

// fibRuleHdr 构造 12 字节的规则头。res1/res2 一律留 0。
func fibRuleHdr(family int, srcLen uint8, table uint8) []byte {
	h := make([]byte, fibRuleHdrLen)
	h[0] = afFamily(family)
	h[1] = 0 // dst_len
	h[2] = srcLen
	h[3] = 0 // tos
	h[4] = table
	h[5] = 0 // res1
	h[6] = 0 // res2
	h[7] = actToTable
	binary.NativeEndian.PutUint32(h[8:], 0) // flags
	return h
}

func attrU8(buf []byte, typ uint16, v uint8) []byte {
	a := make([]byte, 8)
	binary.NativeEndian.PutUint16(a[0:], 5)
	binary.NativeEndian.PutUint16(a[2:], typ)
	a[4] = v
	return append(buf, a...)
}

func attrU32(buf []byte, typ uint16, v uint32) []byte {
	a := make([]byte, 8)
	binary.NativeEndian.PutUint16(a[0:], 8)
	binary.NativeEndian.PutUint16(a[2:], typ)
	binary.NativeEndian.PutUint32(a[4:], v)
	return append(buf, a...)
}

// attrFixed 写入一个定长负载属性（地址、接口名等），按 4 字节对齐补零。
func attrFixed(buf []byte, typ uint16, v []byte) []byte {
	n := 4 + len(v)
	pad := (4 - len(v)%4) % 4
	a := make([]byte, n+pad)
	binary.NativeEndian.PutUint16(a[0:], uint16(n))
	binary.NativeEndian.PutUint16(a[2:], typ)
	copy(a[4:], v)
	return append(buf, a...)
}

func attrString(buf []byte, typ uint16, s string) []byte {
	// 内核期待以 NUL 结尾的字符串。
	return attrFixed(buf, typ, append([]byte(s), 0))
}

// EncodeRuleAdd 把一条 Rule 编成 RTM_NEWRULE 的负载。
//
// 注意一个坑：**不要写 table 属性为 0**。内核把 table=0 解释成"未指定"，
// 会让规则落到一个与预期无关的表上（实测会变成 table 3）。
// 因此这里只在 Table > 0 时才写 FRA_TABLE，并且 hdr.table 只放能用 8 位表达的表号。
func EncodeRuleAdd(r Rule) ([]byte, error) {
	if r.Pref <= 0 {
		return nil, fmt.Errorf("规则优先级必须是正数（实得 %d）", r.Pref)
	}
	if r.Table < 0 {
		return nil, fmt.Errorf("表号不能为负（实得 %d）", r.Table)
	}
	if r.From.IsValid() && !r.From.Is4() && !r.From.Is6() {
		return nil, fmt.Errorf("源地址不是合法的 IP: %v", r.From)
	}
	if r.From.IsValid() && familyOfAddr(r.From) != r.Family {
		return nil, fmt.Errorf("源地址 %v 与地址族 %d 不匹配", r.From, r.Family)
	}

	var srcLen uint8
	var hdrTable uint8
	if r.Table > 0 && r.Table < 256 {
		hdrTable = uint8(r.Table)
	}
	if r.From.IsValid() {
		srcLen = srcMaskLen(r.Family)
	}

	buf := fibRuleHdr(r.Family, srcLen, hdrTable)
	buf = attrU32(buf, fraPriority, uint32(r.Pref))
	// Table 为 0 时**不写**该属性；大于 255 时只能靠属性表达。
	if r.Table > 0 {
		buf = attrU32(buf, fraTable, uint32(r.Table))
	}
	if r.From.IsValid() {
		buf = attrFixed(buf, fraSrc, addrBytes(r.From))
	}
	if r.SuppressPrefixLen >= 0 {
		buf = attrU32(buf, fraSuppress, uint32(r.SuppressPrefixLen))
	}
	return buf, nil
}

// EncodeRuleDel 编一条"按 family + pref 删除"的负载。
//
// 只带优先级，不带 table/src —— 内核按提供的属性做匹配，属性越少匹配越宽，
// 这样才能把同一 pref 的历史残留一次清干净。误删风险由调用方先用 ListRules
// 核对内容来规避（见 CleanupRules）。
func EncodeRuleDel(family, pref int) []byte {
	buf := fibRuleHdr(family, 0, 0)
	return attrU32(buf, fraPriority, uint32(pref))
}

func familyOfAddr(a netip.Addr) int {
	if a.Is4() {
		return FamilyV4
	}
	return FamilyV6
}

func addrBytes(a netip.Addr) []byte {
	if a.Is4() {
		b := a.As4()
		return b[:]
	}
	b := a.As16()
	return b[:]
}

// ---------------------------------------------------------------------------
// 解码
// ---------------------------------------------------------------------------

// ParseRuleMsg 解析内核返回的一条规则消息（RTM_NEWRULE 负载）。
func ParseRuleMsg(b []byte) (Rule, error) {
	if len(b) < fibRuleHdrLen {
		return Rule{}, fmt.Errorf("规则消息长度 %d 不足 %d", len(b), fibRuleHdrLen)
	}
	r := Rule{
		Family:            familyFromAF(b[0]),
		Table:             int(b[4]),
		SuppressPrefixLen: -1,
	}

	attrs, err := netlink.UnmarshalAttributes(b[fibRuleHdrLen:])
	if err != nil {
		return Rule{}, fmt.Errorf("解析规则属性: %w", err)
	}
	for _, a := range attrs {
		switch a.Type {
		case fraSrc:
			if addr, ok := netip.AddrFromSlice(a.Data); ok {
				r.From = addr.Unmap()
			}
		case fraPriority:
			if len(a.Data) == 4 {
				r.Pref = int(binary.NativeEndian.Uint32(a.Data))
			}
		case fraTable:
			if len(a.Data) == 4 {
				r.Table = int(binary.NativeEndian.Uint32(a.Data))
			}
		case fraSuppress:
			if len(a.Data) == 4 {
				if v := binary.NativeEndian.Uint32(a.Data); v != suppressUnset {
					r.SuppressPrefixLen = int(v)
				}
			}
		}
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// 与内核交互
// ---------------------------------------------------------------------------

func dialRoute() (*netlink.Conn, error) {
	c, err := netlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		return nil, fmt.Errorf("打开 NETLINK_ROUTE: %w", err)
	}
	return c, nil
}

// AddRule 添加一条策略路由规则。
func AddRule(r Rule) error {
	req, err := EncodeRuleAdd(r)
	if err != nil {
		return err
	}
	conn, err := dialRoute()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.Execute(netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(unix.RTM_NEWRULE),
			Flags: netlink.Request | netlink.Create | netlink.Excl | netlink.Acknowledge,
		},
		Data: req,
	})
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrRuleExists
		}
		return fmt.Errorf("添加规则 %s: %w", r, err)
	}
	return nil
}

// DelRule 按 family + pref 删除规则。规则不存在时返回 nil（幂等）。
func DelRule(family, pref int) error {
	conn, err := dialRoute()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.Execute(netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(unix.RTM_DELRULE),
			Flags: netlink.Request | netlink.Acknowledge,
		},
		Data: EncodeRuleDel(family, pref),
	})
	if err != nil {
		// ENOENT / EINVAL 都表示"没有这条规则"。内核在这两种情况下返回码不一致
		// （没有匹配项时 ENOENT，属性组合无意义时 EINVAL），两种都当"已不存在"。
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EINVAL) {
			return nil
		}
		return fmt.Errorf("删除规则 family=%d pref=%d: %w", family, pref, err)
	}
	return nil
}

// ListRules 列出某个地址族的全部规则。
func ListRules(family int) ([]Rule, error) {
	conn, err := dialRoute()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	msgs, err := conn.Execute(netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(unix.RTM_GETRULE),
			Flags: netlink.Request | netlink.Dump,
		},
		Data: fibRuleHdr(family, 0, 0),
	})
	if err != nil {
		return nil, fmt.Errorf("读取规则(family=%d): %w", family, err)
	}

	out := make([]Rule, 0, len(msgs))
	for _, m := range msgs {
		// dump 会带回结束用的 NLMSG_DONE（已由库过滤），但要防御空负载。
		if len(m.Data) < fibRuleHdrLen {
			continue
		}
		r, err := ParseRuleMsg(m.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// FindRule 在列表里找出同 family + pref 的规则。
func FindRule(rules []Rule, family, pref int) (Rule, bool) {
	for _, r := range rules {
		if r.Family == family && r.Pref == pref {
			return r, true
		}
	}
	return Rule{}, false
}
