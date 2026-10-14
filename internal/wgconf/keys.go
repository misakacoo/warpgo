// Package wgconf 负责 WireGuard 侧的本地材料：X25519 密钥与 wg-quick 配置。
//
// 它刻意不依赖 wireguard-tools —— 上游要 `wg genkey` 才能生成密钥，没有就退化到
// openssl 甚至某个第三方 HTTP 接口拿密钥（把私钥送到别人服务器上）。用 Go 的
// crypto/ecdh 自己做，既快又不依赖外部命令，也不会把私钥发出去。
package wgconf

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeyLen 是 WireGuard 密钥的字节长度（X25519 标量）。
const KeyLen = 32

// 错误。
var (
	// ErrBadKeyLength 表示 base64 解出来不是 32 字节。
	ErrBadKeyLength = errors.New("密钥必须是 32 字节")
	// ErrBadKeyEncoding 表示不是合法 base64。
	ErrBadKeyEncoding = errors.New("密钥不是合法的 base64")
)

// Key 是 32 字节的 WireGuard 密钥（私钥或公钥都是这个类型）。
type Key [32]byte

// String 返回 base64 形式，与 `wg genkey` / `wg pubkey` 的输出格式一致
// （标准 base64、带 padding、无换行）。
func (k Key) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

// IsZero 报告是否全零（通常是"没设置"）。
func (k Key) IsZero() bool {
	for _, b := range k {
		if b != 0 {
			return false
		}
	}
	return true
}

// Bytes 返回底层字节的副本，避免调用方改到 Key 内部。
func (k Key) Bytes() []byte { return append([]byte(nil), k[:]...) }

// ParseKey 解析 base64 密钥。带不带 padding 都接受。
func ParseKey(s string) (Key, error) {
	var k Key
	v := strings.TrimSpace(s)
	if v == "" {
		return k, fmt.Errorf("密钥为空")
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(v); err != nil {
			return k, fmt.Errorf("%w: %q", ErrBadKeyEncoding, s)
		}
	}
	if len(raw) != KeyLen {
		return k, fmt.Errorf("%w（实得 %d 字节）", ErrBadKeyLength, len(raw))
	}
	copy(k[:], raw)
	return k, nil
}

// KeyPair 是一对 X25519 密钥。
type KeyPair struct {
	Private Key
	Public  Key
}

// GenerateKeyPair 生成一对 X25519 密钥。
//
// # 关于 clamp
//
// WireGuard 的私钥在设计上要求做 clamping（见 RFC 7748：首字节清低 3 位、
// 末字节清最高位并置次高位）。Go 的 crypto/ecdh 在**计算时**会 clamp，
// 所以即使不预clamp也能算出正确的公钥；这里仍然显式 clamp 再落盘，理由是：
//
//   - 用户可能拿我们的私钥去 `wg pubkey` 或别的实现里验算，clamp 过的私钥
//     在任何实现下都得到同一个公钥，行为可预期；
//   - 与 `wg genkey` 的产物形态一致（它输出的就是 clamped 私钥）。
//
// clamp 是幂等的，多做一次没有代价。
func GenerateKeyPair() (KeyPair, error) {
	var kp KeyPair
	if _, err := rand.Read(kp.Private[:]); err != nil {
		return KeyPair{}, fmt.Errorf("读取随机数: %w", err)
	}
	clampScalar(kp.Private[:])

	pub, err := PublicFromPrivate(kp.Private)
	if err != nil {
		return KeyPair{}, err
	}
	kp.Public = pub
	return kp, nil
}

// PublicFromPrivate 由私钥推导公钥。
func PublicFromPrivate(priv Key) (Key, error) {
	var pub Key
	curve := ecdh.X25519()
	sk, err := curve.NewPrivateKey(priv[:])
	if err != nil {
		return pub, fmt.Errorf("构造 X25519 私钥: %w", err)
	}
	copy(pub[:], sk.PublicKey().Bytes())
	return pub, nil
}

// clampScalar 按 RFC 7748 对 X25519 标量做 clamping（就地修改、幂等）。
func clampScalar(b []byte) {
	b[0] &= 248
	b[31] &= 127
	b[31] |= 64
}
