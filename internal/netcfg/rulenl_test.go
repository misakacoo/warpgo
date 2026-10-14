package netcfg

import (
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
)

// 测试里自己解析属性，不调用被测代码的解码函数 ——
// 否则"编码和解码同时错了"这种情况会互相掩护、测不出来。

type rawAttr struct {
	typ  uint16
	data []byte
}

func scanAttrs(t *testing.T, b []byte) []rawAttr {
	t.Helper()
	var out []rawAttr
	for len(b) >= 4 {
		n := int(binary.NativeEndian.Uint16(b[0:]))
		if n < 4 || n > len(b) {
			t.Fatalf("属性长度非法: %d（剩余 %d 字节）", n, len(b))
		}
		out = append(out, rawAttr{
			typ:  binary.NativeEndian.Uint16(b[2:]),
			data: b[4:n],
		})
		b = b[n:]
	}
	if len(b) != 0 {
		t.Fatalf("属性区剩余 %d 字节未对齐", len(b))
	}
	return out
}

func attrByType(attrs []rawAttr, typ uint16) (rawAttr, bool) {
	for _, a := range attrs {
		if a.typ == typ {
			return a, true
		}
	}
	return rawAttr{}, false
}

func attrU32Value(t *testing.T, a rawAttr) uint32 {
	t.Helper()
	if len(a.data) != 4 {
		t.Fatalf("u32 属性的负载应是 4 字节，实得 %d", len(a.data))
	}
	return binary.NativeEndian.Uint32(a.data)
}

func TestEncodeRuleAddLayout(t *testing.T) {
	r := Rule{
		Family:            FamilyV4,
		Pref:              PrefFromV4,
		Table:             TableWarp,
		From:              netip.MustParseAddr("172.16.0.2"),
		SuppressPrefixLen: -1,
	}
	buf, err := EncodeRuleAdd(r)
	if err != nil {
		t.Fatal(err)
	}

	if len(buf) < fibRuleHdrLen {
		t.Fatalf("消息太短: %d", len(buf))
	}
	// 头部：family / src_len / table / action
	if buf[0] != 2 { // AF_INET
		t.Errorf("family 字节应为 2(AF_INET)，实得 %d", buf[0])
	}
	if buf[2] != 32 {
		t.Errorf("src_len 应为 32，实得 %d", buf[2])
	}
	if buf[4] != 0 {
		t.Errorf("表号超过 255 时 hdr.table 应留 0（靠属性表达），实得 %d", buf[4])
	}
	if buf[7] != actToTable {
		t.Errorf("action 应为 %d(查表)，实得 %d", actToTable, buf[7])
	}

	attrs := scanAttrs(t, buf[fibRuleHdrLen:])

	// 优先级
	a, ok := attrByType(attrs, fraPriority)
	if !ok {
		t.Fatal("缺少优先级属性")
	}
	if v := attrU32Value(t, a); v != uint32(PrefFromV4) {
		t.Errorf("优先级应为 %d，实得 %d", PrefFromV4, v)
	}

	// 表号
	a, ok = attrByType(attrs, fraTable)
	if !ok {
		t.Fatal("缺少表号属性")
	}
	if v := attrU32Value(t, a); v != uint32(TableWarp) {
		t.Errorf("表号应为 %d，实得 %d", TableWarp, v)
	}

	// 源地址
	a, ok = attrByType(attrs, fraSrc)
	if !ok {
		t.Fatal("缺少源地址属性")
	}
	if !net.IP(a.data).Equal(net.ParseIP("172.16.0.2")) {
		t.Errorf("源地址不对: %v", net.IP(a.data))
	}

	// SuppressPrefixLen 为 -1 时**不能**写这个属性（写了会被内核当成 0）。
	if _, ok := attrByType(attrs, fraSuppress); ok {
		t.Error("SuppressPrefixLen=-1 时不该写 suppress 属性")
	}
}

// 这是一条关键回归：内核把 table=0 解释成"未指定"，会让规则落到一个
// 与预期无关的表上（实测会变成 table 3）。所以绝不写值为 0 的表号属性。
func TestEncodeRuleAddNeverEmitsZeroTable(t *testing.T) {
	buf, err := EncodeRuleAdd(Rule{Family: FamilyV4, Pref: 9001, SuppressPrefixLen: -1})
	if err != nil {
		t.Fatal(err)
	}
	attrs := scanAttrs(t, buf[fibRuleHdrLen:])
	if a, ok := attrByType(attrs, fraTable); ok {
		t.Errorf("Table=0 时不该写表号属性，实得 %d", attrU32Value(t, a))
	}
	if buf[4] != 0 {
		t.Errorf("Table=0 时 hdr.table 应为 0，实得 %d", buf[4])
	}
}

func TestEncodeRuleAddSuppressAndV6(t *testing.T) {
	r := Rule{Family: FamilyV6, Pref: PrefSuppressMain, Table: TableMain, SuppressPrefixLen: 0}
	buf, err := EncodeRuleAdd(r)
	if err != nil {
		t.Fatal(err)
	}
	if buf[0] != 10 { // AF_INET6
		t.Errorf("family 字节应为 10(AF_INET6)，实得 %d", buf[0])
	}
	if buf[4] != uint8(TableMain) {
		t.Errorf("表号 <256 时应写进 hdr.table，实得 %d", buf[4])
	}
	attrs := scanAttrs(t, buf[fibRuleHdrLen:])
	a, ok := attrByType(attrs, fraSuppress)
	if !ok {
		t.Fatal("SuppressPrefixLen=0 时必须写这个属性 —— 否则它就变成「不设置」了")
	}
	if v := attrU32Value(t, a); v != 0 {
		t.Errorf("suppress 值应为 0，实得 %d", v)
	}
	if _, ok := attrByType(attrs, fraSrc); ok {
		t.Error("from all 时不该写源地址属性")
	}
}

func TestEncodeRuleAddValidation(t *testing.T) {
	cases := []struct {
		name string
		r    Rule
	}{
		{"优先级为 0", Rule{Family: FamilyV4, Pref: 0, Table: TableMain, SuppressPrefixLen: -1}},
		{"优先级为负", Rule{Family: FamilyV4, Pref: -1, Table: TableMain, SuppressPrefixLen: -1}},
		{"表号为负", Rule{Family: FamilyV4, Pref: 100, Table: -5, SuppressPrefixLen: -1}},
		{"源地址族与规则族不匹配", Rule{
			Family: FamilyV4, Pref: 100, Table: TableWarp,
			From: netip.MustParseAddr("2606:4700::1"), SuppressPrefixLen: -1,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := EncodeRuleAdd(c.r); err == nil {
				t.Fatal("应当报错")
			}
		})
	}
}

// 删除用的消息只带优先级：属性越少，内核匹配越宽，
// 才能把同一 pref 的历史残留一次清干净。
func TestEncodeRuleDelOnlyCarriesPriority(t *testing.T) {
	buf := EncodeRuleDel(FamilyV4, PrefFromV4)
	if buf[7] != actToTable {
		t.Errorf("action 应为 %d，实得 %d", actToTable, buf[7])
	}
	attrs := scanAttrs(t, buf[fibRuleHdrLen:])
	if len(attrs) != 1 {
		t.Fatalf("只应带一个属性，实得 %d", len(attrs))
	}
	if attrs[0].typ != fraPriority {
		t.Errorf("应只带优先级，实得类型 %d", attrs[0].typ)
	}
	if v := attrU32Value(t, attrs[0]); v != uint32(PrefFromV4) {
		t.Errorf("优先级应为 %d，实得 %d", PrefFromV4, v)
	}
}

// --- 解码：手工按内核的响应格式构造字节 ---

func kernelAttrU32(typ uint16, v uint32) []byte {
	b := make([]byte, 8)
	binary.NativeEndian.PutUint16(b[0:], 8)
	binary.NativeEndian.PutUint16(b[2:], typ)
	binary.NativeEndian.PutUint32(b[4:], v)
	return b
}

func kernelAttrBytes(typ uint16, v []byte) []byte {
	n := 4 + len(v)
	pad := (4 - len(v)%4) % 4
	b := make([]byte, n+pad)
	binary.NativeEndian.PutUint16(b[0:], uint16(n))
	binary.NativeEndian.PutUint16(b[2:], typ)
	copy(b[4:], v)
	return b
}

// kernelRuleMsg 按内核 dump 的形态拼一条规则消息。
func kernelRuleMsg(family uint8, srcLen, hdrTable uint8, attrs ...[]byte) []byte {
	h := make([]byte, fibRuleHdrLen)
	h[0] = family
	h[2] = srcLen
	h[4] = hdrTable
	h[7] = actToTable
	for _, a := range attrs {
		h = append(h, a...)
	}
	return h
}

func TestParseRuleMsgFromAddress(t *testing.T) {
	// 对应 `ip -4 rule add pref 9010 from 172.16.0.2 lookup 51820` 的读回形态：
	// hdr.table 是 252(RT_TABLE_COMPAT)，真正的表号在 FRA_TABLE 里。
	msg := kernelRuleMsg(2, 32, 252,
		kernelAttrBytes(fraSrc, []byte{172, 16, 0, 2}),
		kernelAttrU32(fraPriority, uint32(PrefFromV4)),
		kernelAttrU32(fraTable, uint32(TableWarp)),
	)
	r, err := ParseRuleMsg(msg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Family != FamilyV4 {
		t.Errorf("family 应为 V4，实得 %d", r.Family)
	}
	if r.Pref != PrefFromV4 {
		t.Errorf("pref 应为 %d，实得 %d", PrefFromV4, r.Pref)
	}
	if r.Table != TableWarp {
		t.Errorf("表号应由 FRA_TABLE 覆盖为 %d，实得 %d", TableWarp, r.Table)
	}
	if r.From != netip.MustParseAddr("172.16.0.2") {
		t.Errorf("源地址实得 %v", r.From)
	}
	if r.SuppressPrefixLen != -1 {
		t.Errorf("没有该属性时应为 -1，实得 %d", r.SuppressPrefixLen)
	}
}

func TestParseRuleMsgSuppress(t *testing.T) {
	msg := kernelRuleMsg(2, 0, uint8(TableMain),
		kernelAttrU32(fraPriority, uint32(PrefSuppressMain)),
		kernelAttrU32(fraTable, uint32(TableMain)),
		kernelAttrU32(fraSuppress, 0),
	)
	r, err := ParseRuleMsg(msg)
	if err != nil {
		t.Fatal(err)
	}
	if r.SuppressPrefixLen != 0 {
		t.Errorf("suppress 应为 0，实得 %d", r.SuppressPrefixLen)
	}
	if r.From.IsValid() {
		t.Errorf("from all 不该有源地址，实得 %v", r.From)
	}
}

// 内核用 0xFFFFFFFF 表示"没有 suppress 属性"，必须还原成 -1 而不是 4294967295。
func TestParseRuleMsgSuppressSentinel(t *testing.T) {
	msg := kernelRuleMsg(2, 0, uint8(TableMain),
		kernelAttrU32(fraPriority, 1234),
		kernelAttrU32(fraSuppress, suppressUnset),
	)
	r, err := ParseRuleMsg(msg)
	if err != nil {
		t.Fatal(err)
	}
	if r.SuppressPrefixLen != -1 {
		t.Fatalf("0xFFFFFFFF 应还原成 -1，实得 %d", r.SuppressPrefixLen)
	}
}

func TestParseRuleMsgV6(t *testing.T) {
	src := netip.MustParseAddr("2606:4700:110::2").As16()
	msg := kernelRuleMsg(10, 128, 252,
		kernelAttrBytes(fraSrc, src[:]),
		kernelAttrU32(fraPriority, uint32(PrefFromV6)),
		kernelAttrU32(fraTable, uint32(TableWarp)),
	)
	r, err := ParseRuleMsg(msg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Family != FamilyV6 {
		t.Errorf("family 应为 V6，实得 %d", r.Family)
	}
	if r.From != netip.MustParseAddr("2606:4700:110::2") {
		t.Errorf("源地址实得 %v", r.From)
	}
}

func TestParseRuleMsgTooShort(t *testing.T) {
	if _, err := ParseRuleMsg([]byte{1, 2, 3}); err == nil {
		t.Fatal("过短的消息应报错")
	}
}

func TestFindRule(t *testing.T) {
	rules := []Rule{
		{Family: FamilyV4, Pref: 9000, Table: TableMain},
		{Family: FamilyV6, Pref: 9000, Table: TableMain},
		{Family: FamilyV4, Pref: 9010, Table: TableWarp},
	}
	if r, ok := FindRule(rules, FamilyV4, 9010); !ok || r.Table != TableWarp {
		t.Errorf("没找到 V4/9010: %v %v", r, ok)
	}
	if _, ok := FindRule(rules, FamilyV6, 9010); ok {
		t.Error("V6/9010 不存在，不该找到")
	}
	// 同 pref 不同族必须能区分开（v4/v6 的 suppress 规则 pref 相同）。
	if r, ok := FindRule(rules, FamilyV6, 9000); !ok || r.Family != FamilyV6 {
		t.Errorf("没找到 V6/9000: %v %v", r, ok)
	}
}

// --- 全局模式的规则编码 ---

// kernelRuleMsgWithFlags 与 kernelRuleMsg 相同，但可以指定 header 的 flags。
func kernelRuleMsgWithFlags(family uint8, srcLen, hdrTable uint8, flags uint32, attrs ...[]byte) []byte {
	h := make([]byte, fibRuleHdrLen)
	h[0] = family
	h[2] = srcLen
	h[4] = hdrTable
	h[7] = actToTable
	binary.NativeEndian.PutUint32(h[8:], flags)
	for _, a := range attrs {
		h = append(h, a...)
	}
	return h
}

// `not fwmark 0xca6c lookup 51820` 要靠**两个**地方同时正确：
// header 的 FIB_RULE_INVERT 标志位，以及 FRA_FWMARK 属性。少一个都会变成
// "所有流量都走 WARP（包括隧道自己的封装包）" —— 整机断网。
func TestEncodeRuleAddFwMarkInvert(t *testing.T) {
	r := Rule{
		Family: FamilyV4, Pref: PrefGlobalTunnel, Table: TableWarp,
		FWMark: FWMarkWarp, Invert: true, SuppressPrefixLen: -1,
	}
	buf, err := EncodeRuleAdd(r)
	if err != nil {
		t.Fatal(err)
	}
	if flags := binary.NativeEndian.Uint32(buf[8:12]); flags&fibRuleInvert == 0 {
		t.Errorf("`not` 必须写进 header 的 flags（FIB_RULE_INVERT），实得 0x%x", flags)
	}
	attrs := scanAttrs(t, buf[fibRuleHdrLen:])
	a, ok := attrByType(attrs, fraFwMark)
	if !ok {
		t.Fatal("缺少 fwmark 属性 —— 规则会退化成「所有流量都走 WARP」")
	}
	if v := attrU32Value(t, a); v != uint32(FWMarkWarp) {
		t.Errorf("fwmark 应为 0x%x，实得 0x%x", FWMarkWarp, v)
	}
	if _, ok := attrByType(attrs, fraSrc); ok {
		t.Error("fwmark 规则不该带源地址")
	}
}

func TestEncodeRuleAddRejectsMixedMatch(t *testing.T) {
	cases := []struct {
		name string
		r    Rule
	}{
		{"fwmark 与 from 同时出现", Rule{
			Family: FamilyV4, Pref: 100, Table: TableWarp,
			FWMark: 1, From: netip.MustParseAddr("172.16.0.2"), SuppressPrefixLen: -1,
		}},
		{"只有 not 而没有 fwmark", Rule{
			Family: FamilyV4, Pref: 100, Table: TableWarp, Invert: true, SuppressPrefixLen: -1,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := EncodeRuleAdd(c.r); err == nil {
				t.Fatal("应当报错")
			}
		})
	}
}

func TestParseRuleMsgFwMark(t *testing.T) {
	msg := kernelRuleMsgWithFlags(2, 0, 252, fibRuleInvert,
		kernelAttrU32(fraPriority, uint32(PrefGlobalTunnel)),
		kernelAttrU32(fraTable, uint32(TableWarp)),
		kernelAttrU32(fraFwMark, uint32(FWMarkWarp)),
	)
	r, err := ParseRuleMsg(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Invert {
		t.Error("没读出 `not` 标志")
	}
	if r.FWMark != FWMarkWarp {
		t.Errorf("fwmark 应为 0x%x，实得 0x%x", FWMarkWarp, r.FWMark)
	}
	if r.Table != TableWarp {
		t.Errorf("表号应为 %d，实得 %d", TableWarp, r.Table)
	}
}
