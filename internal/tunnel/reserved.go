package tunnel

import "golang.zx2c4.com/wireguard/conn"

// reserved 是 Cloudflare 对 WireGuard 的私有扩展：握手与数据报文里紧跟在
// **报文类型字节之后**的那 3 个字节。它不在官方协议里，内核模块永远发 [0,0,0]，
// 而香港 / 洛杉矶等部分出口必须带上正确的值才会被服务端接受。
//
// # 具体写在哪
//
// WireGuard 每个报文的前 4 个字节是小端 uint32 的报文类型：
//
//	01 00 00 00   handshake initiation
//	02 00 00 00   handshake response
//	03 00 00 00   cookie reply
//	04 00 00 00   transport data
//
// reserved 替换的是**高 3 个字节**（也就是 byte[1:4]），低字节仍是报文类型 ——
// 服务端靠它分辨报文种类。上游打补丁的 wireguard-go（含 bepass-org/warp-plus
// 与 fscarmen/warp 分发的那些二进制）做的正是这件事：
//
//	if len(buffers[i]) > 3 && buffers[i][0] > 0 && buffers[i][0] < 5 {
//	    copy(buffers[i][1:4], peer.reserved[:])
//	}
//
// 本实现与之逐字节等价，但**不改 wireguard-go 的源码**：所有出网报文最终都要
// 经过 conn.Bind.Send，所有入网报文都要经过 conn.Bind.Open 返回的接收函数，
// 在这两个位置上各包一层就够了（见 ReservedBind）。
// 这样既拿到"reserved 一等公民"，又不需要在仓库里养一个一万多行的 fork。
//
// （接收方向为什么也要处理，见 ReservedBind 的注释 —— 那是真机抓包抓出来的，
// 而且它的失败形态是"完全没有报错"。）
const maxWireGuardPacketType = 5

// applyReserved 把 reserved 写进一个报文的 byte[1:4]。
//
// 长度不足 4 字节、或首字节不是已知报文类型的，一律原样放过 ——
// 保活探测、随机填充之类的非标准报文不该被改写（改了也只会让对端困惑）。
func applyReserved(pkt []byte, r [3]byte) {
	if len(pkt) < 4 {
		return
	}
	if pkt[0] == 0 || pkt[0] >= maxWireGuardPacketType {
		return
	}
	copy(pkt[1:4], r[:])
}

// ReservedBind 是一个在收发两个方向上都处理 reserved 的 conn.Bind 包装。
//
// 用嵌入（而不是重新实现整个接口）是为了让 Close / SetMark / ParseEndpoint /
// BatchSize 全部原样透传 —— 其中 SetMark 尤其重要：全局模式的 fwmark 防环路
// 就是靠它设到 UDP socket 上的，少转发一次就会变成"封装包被自己的规则再送进隧道"。
//
// # 为什么收也要处理
//
// 这是真机抓包抓出来的：Cloudflare 的服务端**会把 reserved 原样盖在它的响应上**。
// 内核后端发 `01 00 00 00`，收到的响应是 `02 00 00 00`；用户态后端发 `01 24 D4 90`，
// 收到的响应就是 `02 24 D4 90`。
//
// 而 WireGuard 的实现是按**4 字节小端**读报文类型的：
//
//	msgType := binary.LittleEndian.Uint32(packet[:4])
//
// 于是 `02 24 D4 90` 被读成 0x90D42402 ≠ 2，响应被当成未知报文直接丢掉 ——
// 现象是"握手包发出去了、服务端也回了、但握手永远完不成"，而且没有任何报错。
// 这与上游补丁一致：它在解析类型之前加了一句
//
//	packet[1], packet[2], packet[3] = 0, 0, 0
type ReservedBind struct {
	conn.Bind
	reserved [3]byte
}

// NewReservedBind 包装一个 Bind，让它的出网报文带上 reserved、入网报文先抹掉它。
//
// reserved 为全零时发送方向不改写任何字节（与内核后端行为一致，也省掉一次遍历）；
// 接收方向仍然照抹 —— 那 3 个字节本来就不属于协议，抹掉是无条件正确的。
func NewReservedBind(inner conn.Bind, r [3]byte) *ReservedBind {
	return &ReservedBind{Bind: inner, reserved: r}
}

// Reserved 返回当前生效的 reserved（日志与自检用）。
func (b *ReservedBind) Reserved() [3]byte { return b.reserved }

// Send 先把 reserved 写进每个报文，再交给真正的 Bind 发出去。
func (b *ReservedBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	if b.reserved != ([3]byte{}) {
		for _, buf := range bufs {
			applyReserved(buf, b.reserved)
		}
	}
	return b.Bind.Send(bufs, ep)
}

// Open 把底层返回的每一个接收函数都包一层：先抹掉类型字段的高 3 字节，
// 再交回给 wireguard-go 去解析。
//
// 抹的位置必须是**收包之后、解析之前** —— 一旦被解析过就已经晚了（那次响应
// 已经被丢弃，而它同时带走了握手所需的临时公钥）。
func (b *ReservedBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actual, err := b.Bind.Open(port)
	if err != nil {
		return nil, actual, err
	}
	out := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		out[i] = maskReservedOnReceive(fn)
	}
	return out, actual, nil
}

// maskReservedOnReceive 返回一个"收到报文先把类型字段的高 3 字节清零"的接收函数。
func maskReservedOnReceive(inner conn.ReceiveFunc) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, err := inner(packets, sizes, eps)
		if err != nil {
			return n, err
		}
		for i := 0; i < n && i < len(sizes); i++ {
			size := sizes[i]
			// sizes 里可能夹着 0（调用方契约允许），那种元素要忽略。
			if size < 4 || i >= len(packets) || size > len(packets[i]) {
				continue
			}
			clearReserved(packets[i])
		}
		return n, nil
	}
}

// clearReserved 把报文类型字段的高 3 字节清零（保留低字节 = 报文类型）。
func clearReserved(pkt []byte) {
	if len(pkt) < 4 {
		return
	}
	pkt[1], pkt[2], pkt[3] = 0, 0, 0
}
