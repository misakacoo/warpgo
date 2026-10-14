package tunnel

import (
	"errors"
	"testing"

	"golang.zx2c4.com/wireguard/conn"
)

// fakeBind 实现 conn.Bind，只记录 Send 收到的报文。
//
// 它存在的意义是让"reserved 到底有没有写进报文"这件事**可以脱离内核、
// 脱离网络、脱离 root 断言** —— 而它恰恰是本项目里最容易悄悄坏掉的一环：
// 写错的后果不是报错，而是"在某些地区握手被静默丢弃"。
type fakeBind struct {
	sent   [][]byte
	marks  []uint32
	opened int
	closed int
	// inbound 是底层"网卡"要吐出来的报文；Open 之后由测试驱动接收函数取走。
	inbound [][]byte
	// recvCalled 记录接收函数被调用了几次。
	recvCalled int
}

func (b *fakeBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.opened++
	recv := func(packets [][]byte, sizes []int, _ []conn.Endpoint) (int, error) {
		b.recvCalled++
		n := 0
		for i, pkt := range b.inbound {
			if i >= len(packets) || i >= len(sizes) {
				break
			}
			copy(packets[i], pkt)
			sizes[i] = len(pkt)
			n++
		}
		return n, nil
	}
	return []conn.ReceiveFunc{recv}, port, nil
}

func (b *fakeBind) Close() error { b.closed++; return nil }

func (b *fakeBind) SetMark(mark uint32) error { b.marks = append(b.marks, mark); return nil }

func (b *fakeBind) Send(bufs [][]byte, _ conn.Endpoint) error {
	// 深拷贝：reserved 是**原地改写**缓冲区，不复制的话测试断言看到的
	// 永远是改写后的内容，于是"有没有改写"就分不出来了。
	for _, buf := range bufs {
		cp := make([]byte, len(buf))
		copy(cp, buf)
		b.sent = append(b.sent, cp)
	}
	return nil
}

func (b *fakeBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	return nil, errors.New("fakeBind 不支持解析端点")
}

func (b *fakeBind) BatchSize() int { return 1 }

// 报文类型字节之后的 3 个字节必须被换成 reserved ——
// 这正是上游补丁 copy(buffers[i][1:4], peer.reserved[:]) 所做的事。
func TestApplyReserved(t *testing.T) {
	cases := []struct {
		name    string
		pkt     []byte
		want    []byte
		comment string
	}{
		{
			name:    "握手发起",
			pkt:     []byte{1, 0, 0, 0, 0xaa, 0xbb},
			want:    []byte{1, 11, 22, 33, 0xaa, 0xbb},
			comment: "byte0 是报文类型，必须原样保留",
		},
		{
			name:    "握手响应",
			pkt:     []byte{2, 0, 0, 0},
			want:    []byte{2, 11, 22, 33},
			comment: "响应也要带（服务端据此找到那台 NAT 设备）",
		},
		{
			name:    "cookie 回复",
			pkt:     []byte{3, 9, 9, 9},
			want:    []byte{3, 11, 22, 33},
			comment: "已被占用的旧值要被覆盖",
		},
		{
			name:    "传输数据",
			pkt:     []byte{4, 0, 0, 0, 1, 2, 3},
			want:    []byte{4, 11, 22, 33, 1, 2, 3},
			comment: "数据报文同样要带",
		},
		{
			name:    "长度不足 4 字节",
			pkt:     []byte{1, 2},
			want:    []byte{1, 2},
			comment: "短包原样放过，绝不越界写",
		},
		{
			name:    "未知类型",
			pkt:     []byte{9, 0, 0, 0},
			want:    []byte{9, 0, 0, 0},
			comment: "不是 WireGuard 报文的（比如保活探测）不该被改写",
		},
		{
			name:    "类型零",
			pkt:     []byte{0, 0, 0, 0},
			want:    []byte{0, 0, 0, 0},
			comment: "0 不是合法类型",
		},
	}
	r := [3]byte{11, 22, 33}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applyReserved(tc.pkt, r)
			if string(tc.pkt) != string(tc.want) {
				t.Errorf("%s: 实得 %v，期望 %v", tc.comment, tc.pkt, tc.want)
			}
		})
	}
}

// 收到报文必须先把类型字段的高 3 字节抹掉，再交给 wireguard-go 解析。
//
// 这一段是 M5 里最难发现的一处：Cloudflare 的服务端会把 reserved 原样盖在
// 它的响应上（`02 24 D4 90`），而 WireGuard 是按 4 字节小端读类型的，
// 于是 0x90D42402 ≠ 2，响应被当成未知报文丢掉 —— 握手永远完不成，
// 而且**没有任何报错**，只有抓包才看得出来。
func TestReservedBindMasksIncomingTypeField(t *testing.T) {
	inner := &fakeBind{inbound: [][]byte{
		// 服务端把 reserved 盖在了响应上
		{0x02, 0x24, 0xD4, 0x90, 0xaa, 0xbb, 0xcc, 0xdd},
		// 数据报文同样被盖了
		{0x04, 0x24, 0xD4, 0x90, 0x11},
		// 长度不足 4 字节：不能碰
		{0x02, 0x24},
	}}
	b := NewReservedBind(inner, [3]byte{36, 212, 144})
	fns, _, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fns) != 1 {
		t.Fatalf("Open 应返回 1 个接收函数，实得 %d", len(fns))
	}

	packets := [][]byte{make([]byte, 64), make([]byte, 64), make([]byte, 64)}
	sizes := []int{8, 5, 2}
	n, err := fns[0](packets, sizes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应收到 3 个报文，实得 %d", n)
	}
	if inner.recvCalled != 1 {
		t.Errorf("底层接收函数应被调用 1 次，实得 %d", inner.recvCalled)
	}

	want := [][]byte{
		{0x02, 0x00, 0x00, 0x00, 0xaa, 0xbb, 0xcc, 0xdd},
		{0x04, 0x00, 0x00, 0x00, 0x11},
		{0x02, 0x24},
	}
	for i, w := range want {
		if string(packets[i][:len(w)]) != string(w) {
			t.Errorf("第 %d 个报文实得 %v，期望 %v", i, packets[i][:len(w)], w)
		}
	}
}

// sizes 里夹 0 是调用方契约允许的（"Some elements of sizes may be zero,
// and callers should ignore them"）。那些槽位必须整体忽略：
// 既不能越界，也不能把不属于本次接收的内容改掉。
func TestReservedBindIgnoresEmptySlots(t *testing.T) {
	inner := &slotBind{
		sizes:   []int{5, 0}, // 报告 2 个元素，第 2 个是空的
		payload: map[int][]byte{0: {0x02, 0x24, 0xD4, 0x90, 0x01}},
	}
	b := NewReservedBind(inner, [3]byte{36, 212, 144})
	fns, _, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	packets := [][]byte{make([]byte, 16), make([]byte, 16)}
	// 第 2 个槽位里先塞满哨兵值：它在契约上属于"本次没收到东西"的位置。
	for i := range packets[1] {
		packets[1][i] = 0x7f
	}
	sizes := make([]int, 2)

	n, err := fns[0](packets, sizes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("底层报告 2 个元素，实得 %d", n)
	}
	if packets[0][1] != 0 || packets[0][2] != 0 || packets[0][3] != 0 {
		t.Errorf("第一个报文应被抹掉类型高位，实得 %v", packets[0][:4])
	}
	for i := range packets[1] {
		if packets[1][i] != 0x7f {
			t.Fatalf("空槽位（sizes=0）不该被写入任何内容: %v", packets[1])
		}
	}
}

// slotBind 是一个"按预设 sizes 报告接收结果"的 Bind，用来演练空槽位这种契约情形。
type slotBind struct {
	sizes   []int
	payload map[int][]byte
}

func (b *slotBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	return []conn.ReceiveFunc{
		func(packets [][]byte, sizes []int, _ []conn.Endpoint) (int, error) {
			for i, sz := range b.sizes {
				if i >= len(sizes) {
					break
				}
				sizes[i] = sz
				if sz > 0 && i < len(packets) {
					copy(packets[i], b.payload[i])
				}
			}
			return len(b.sizes), nil
		},
	}, port, nil
}
func (b *slotBind) Close() error                       { return nil }
func (b *slotBind) SetMark(uint32) error               { return nil }
func (b *slotBind) Send([][]byte, conn.Endpoint) error { return nil }
func (b *slotBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return nil, errors.New("不支持")
}
func (b *slotBind) BatchSize() int { return 2 }

// 真实接收函数返回错误时必须原样上抛，不能吞掉
// （吞掉的话，socket 关闭时设备不会退出，守护进程就变成了僵尸）。
func TestReservedBindPropagatesRecvError(t *testing.T) {
	want := errors.New("boom")
	b := NewReservedBind(&erroringBind{err: want}, [3]byte{1, 2, 3})
	fns, _, err := b.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(fns) != 1 {
		t.Fatalf("Open 应返回 1 个接收函数，实得 %d", len(fns))
	}
	if _, rerr := fns[0](nil, nil, nil); !errors.Is(rerr, want) {
		t.Errorf("接收错误应原样上抛，实得 %v", rerr)
	}
}

// erroringBind 是一个"接收必然失败"的最小 Bind，只用于验证错误透传。
type erroringBind struct{ err error }

func (b *erroringBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	return []conn.ReceiveFunc{
		func([][]byte, []int, []conn.Endpoint) (int, error) { return 0, b.err },
	}, port, nil
}
func (b *erroringBind) Close() error                       { return nil }
func (b *erroringBind) SetMark(uint32) error               { return nil }
func (b *erroringBind) Send([][]byte, conn.Endpoint) error { return nil }
func (b *erroringBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return nil, errors.New("不支持")
}
func (b *erroringBind) BatchSize() int { return 1 }

// 全零 reserved 不该改写任何字节 —— 与内核后端的行为保持一致，
// 也避免在不需要它的大部分地区平白多一次遍历。
func TestReservedBindZeroIsNoop(t *testing.T) {
	inner := &fakeBind{}
	b := NewReservedBind(inner, [3]byte{})
	pkt := []byte{1, 7, 7, 7}
	if err := b.Send([][]byte{pkt}, nil); err != nil {
		t.Fatal(err)
	}
	if got := inner.sent[0]; string(got) != string([]byte{1, 7, 7, 7}) {
		t.Errorf("全零 reserved 不该改写报文，实得 %v", got)
	}
}

// ReservedBind 必须把非 Send 的方法**原样透传**。
//
// SetMark 尤其要紧：全局模式的 fwmark 防环路就靠它设到 UDP socket 上，
// 少转发一次，封装包就会被自己的规则再送进隧道。
func TestReservedBindForwardsEverythingElse(t *testing.T) {
	inner := &fakeBind{}
	b := NewReservedBind(inner, [3]byte{36, 212, 144})

	if _, _, err := b.Open(51820); err != nil {
		t.Fatal(err)
	}
	if err := b.SetMark(0xca6c); err != nil {
		t.Fatal(err)
	}
	if b.BatchSize() != 1 {
		t.Errorf("BatchSize 应透传，实得 %d", b.BatchSize())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if inner.opened != 1 || inner.closed != 1 {
		t.Errorf("Open/Close 未透传: opened=%d closed=%d", inner.opened, inner.closed)
	}
	if len(inner.marks) != 1 || inner.marks[0] != 0xca6c {
		t.Errorf("SetMark 未透传: %v", inner.marks)
	}
	if b.Reserved() != [3]byte{36, 212, 144} {
		t.Errorf("Reserved() 实得 %v", b.Reserved())
	}
}

func TestParseBackend(t *testing.T) {
	cases := []struct {
		in   string
		want Backend
		bad  bool
	}{
		{"", BackendAuto, false},
		{"auto", BackendAuto, false},
		{"AUTO", BackendAuto, false},
		{"kernel", BackendKernel, false},
		{"Kernel", BackendKernel, false},
		{"userspace", BackendUserspace, false},
		{"reserved", BackendUserspace, false}, // 生态里的别名
		{"go", BackendUserspace, false},
		{" user ", BackendUserspace, false},
		{"wireguard", "", true},
	}
	for _, tc := range cases {
		got, err := ParseBackend(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseBackend(%q) 应当报错", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseBackend(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseBackend(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestSupportsReserved(t *testing.T) {
	if SupportsReserved(BackendKernel) {
		t.Error("内核后端带不了 reserved —— 那 3 个字节不在内核接口里")
	}
	if !SupportsReserved(BackendUserspace) {
		t.Error("用户态后端必须支持 reserved")
	}
}

// reserved 的命令行表示必须能原样往来 —— 守护进程的参数就是它。
func TestReservedArgRoundTrip(t *testing.T) {
	cases := []string{
		"0,0,0",
		"36,212,144",
		"255,255,255",
		"1,2,3",
	}
	for _, s := range cases {
		r, err := ParseReservedArg(s)
		if err != nil {
			t.Fatalf("ParseReservedArg(%q): %v", s, err)
		}
		if got := ReservedArg(r); got != s {
			t.Errorf("往返后 %q != %q", got, s)
		}
	}

	for _, bad := range []string{"1,2", "1,2,3,4", "1,2,x", "1,2,256", "-1,2,3"} {
		if _, err := ParseReservedArg(bad); err == nil {
			t.Errorf("ParseReservedArg(%q) 应当报错", bad)
		}
	}
	// 空串是合法的：表示"没有 reserved"。
	if r, err := ParseReservedArg("  "); err != nil || !r.IsZero() {
		t.Errorf("空串应解析成全零，实得 %v / %v", r, err)
	}
}

// DaemonArgs 是父进程拉起守护进程时唯一的凭据来源，形状必须固定。
func TestDaemonArgs(t *testing.T) {
	got := DaemonArgs("warp", 1280, [3]byte{36, 212, 144})
	want := []string{
		"userspace-daemon", "--ifname", "warp", "--mtu", "1280", "--reserved", "36,212,144",
	}
	if len(got) != len(want) {
		t.Fatalf("参数个数 %d，期望 %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项 = %q，期望 %q（完整: %v）", i, got[i], want[i], got)
		}
	}
}
