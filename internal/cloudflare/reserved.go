package cloudflare

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 哨兵错误。调用方用 errors.Is 判断，不要去比对错误字符串。
var (
	// ErrEmptyClientID 表示 client_id 为空（部分地区的注册响应确实不给它）。
	ErrEmptyClientID = errors.New("client_id 为空")
	// ErrShortClientID 表示 client_id 解码后不足 3 字节。
	ErrShortClientID = errors.New("client_id 解码后不足 3 字节")
	// ErrBadClientID 表示 client_id 不是合法 base64。
	ErrBadClientID = errors.New("client_id 不是合法的 base64")
)

// Reserved 是 WARP 握手报文里紧跟在 WireGuard 报文头之后的那 3 个字节。
//
// 它不在 WireGuard 协议本身里，也不在官方 UAPI 键集内 —— 是 Cloudflare 用来
// 识别「这台设备是不是自己发的」的私有扩展。设置它的唯一途径是使用打过补丁的
// 用户态实现（内核模块永远发 [0,0,0]）。详见 docs/DESIGN.md §8.2。
type Reserved [3]uint8

// ReservedFromClientID 从注册响应里的 client_id 解出 reserved。
//
// # 为什么必须按原始字节解
//
// client_id 是 base64 文本，它的**前 3 个解码字节**就是 reserved。
// 上游（fscarmen/warp）早期用 `printf "%d" "'c"` 逐个取字符码点，这在
// 字节 ≥ 0x80 且 locale 是 UTF-8 时会算错：
//
//	client_id = "y8eh"  →  正确 [203, 199, 161]，旧算法得到 [203, 199, 737]
//
// 737 > 255 是非法值，打补丁的 wireguard-go 会**静默回落成 [0,0,0]** ——
// 于是握手在需要 reserved 的地区被 CF 直接丢弃，表现为「连不上但没有任何报错」。
// base64 解码是完全按字节的，与 locale 无关，从根上消除这一整类问题。
//
// 约定：client_id 可能带也可能不带 base64 padding，两种都接受（RawStdEncoding 优先）。
func ReservedFromClientID(clientID string) (Reserved, error) {
	var r Reserved
	cid := strings.TrimSpace(clientID)
	if cid == "" {
		return r, ErrEmptyClientID
	}
	raw, err := decodeBase64Loose(cid)
	if err != nil {
		return r, fmt.Errorf("%w: %q", ErrBadClientID, clientID)
	}
	if len(raw) < 3 {
		return r, fmt.Errorf("%w（实得 %d 字节）", ErrShortClientID, len(raw))
	}
	copy(r[:], raw[:3])
	return r, nil
}

// MustReservedFromClientID 与 ReservedFromClientID 相同，但把错误转成零值。
// 只用于「拿不到 reserved 也不该中断主流程」的场景（例如展示）。
func MustReservedFromClientID(clientID string) Reserved {
	r, err := ReservedFromClientID(clientID)
	if err != nil {
		return Reserved{}
	}
	return r
}

// decodeBase64Loose 接受带 padding 与不带 padding 的标准 base64。
func decodeBase64Loose(s string) ([]byte, error) {
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, ErrBadClientID
}

// IsZero 报告 reserved 是否为全零（即"没有 reserved"）。
func (r Reserved) IsZero() bool { return r[0] == 0 && r[1] == 0 && r[2] == 0 }

// String 返回生态约定的十进制形式，如 "203, 199, 161"。
//
// 这个格式不是随便定的：`# Reserved = 203, 199, 161` 是 WARP 生态里客户端
// 互相传递 reserved 的既定写法（打补丁的 wireguard-go、wireproxy 都读它）。
func (r Reserved) String() string {
	return fmt.Sprintf("%d, %d, %d", r[0], r[1], r[2])
}

// MarshalJSON 输出为三元数组，便于账户文件被人直接阅读与复制。
func (r Reserved) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]uint8{r[0], r[1], r[2]})
}

// UnmarshalJSON 接受三元数组；null 视为全零。
func (r *Reserved) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*r = Reserved{}
		return nil
	}
	var arr [3]uint8
	if err := json.Unmarshal(b, &arr); err != nil {
		return fmt.Errorf("reserved 应为 3 个 0-255 的整数: %w", err)
	}
	*r = arr
	return nil
}
