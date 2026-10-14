package wgconf

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// RFC 7748 §6.1 的 X25519 测试向量。
//
// 用它而不是"自己生成再自己验算"：后者只能证明代码自洽，证明不了它与
// 其他实现一致。WARP 的服务端是别人的实现，我们必须和标准算得一模一样。
const (
	rfc7748AlicePrivHex = "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"
	rfc7748AlicePubHex  = "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"
)

func hexKey(t *testing.T, s string) Key {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != KeyLen {
		t.Fatalf("向量长度不对: %d", len(raw))
	}
	var k Key
	copy(k[:], raw)
	return k
}

func TestPublicFromPrivateMatchesRFC7748(t *testing.T) {
	priv := hexKey(t, rfc7748AlicePrivHex)
	want := hexKey(t, rfc7748AlicePubHex)

	got, err := PublicFromPrivate(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("公钥与 RFC 7748 不一致\n 期望 %x\n 实得 %x", want[:], got[:])
	}
}

func TestParseKeyUsesStandardBase64(t *testing.T) {
	priv := hexKey(t, rfc7748AlicePrivHex)

	// 输出必须与 `wg genkey` 的格式一致：标准 base64、带 padding、无换行。
	s := priv.String()
	if strings.ContainsAny(s, "\n\r ") {
		t.Fatalf("不该含空白: %q", s)
	}
	if !strings.HasSuffix(s, "=") {
		t.Fatalf("32 字节的 base64 应带 padding: %q（长度 %d）", s, len(s))
	}
	if len(s) != 44 {
		t.Fatalf("32 字节 base64 长度应为 44，实得 %d", len(s))
	}
	if _, err := base64.StdEncoding.DecodeString(s); err != nil {
		t.Fatalf("应是标准 base64: %v", err)
	}

	back, err := ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	if back != priv {
		t.Fatal("ParseKey 往返不一致")
	}

	// 去 padding 的写法也要接受（有些工具会去掉）。
	if _, err := ParseKey(strings.TrimRight(s, "=")); err != nil {
		t.Fatalf("不带 padding 也应能解析: %v", err)
	}
}

func TestParseKeyErrors(t *testing.T) {
	if _, err := ParseKey(""); err == nil {
		t.Error("空串应报错")
	}
	if _, err := ParseKey("!!!!"); err == nil {
		t.Error("非 base64 应报错")
	}
	// 16 字节不是合法的 WireGuard 密钥长度。
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := ParseKey(short); err == nil {
		t.Error("长度不对应报错")
	}
}

func TestGenerateKeyPairClamps(t *testing.T) {
	seen := map[Key]bool{}
	for i := 0; i < 32; i++ {
		kp, err := GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}

		// clamping（RFC 7748）：首字节低 3 位清零；末字节最高位清零、次高位置 1。
		if kp.Private[0]&0b111 != 0 {
			t.Fatalf("首字节低 3 位应清零，实得 %08b", kp.Private[0])
		}
		if kp.Private[31]&0b10000000 != 0 {
			t.Fatalf("末字节最高位应清零，实得 %08b", kp.Private[31])
		}
		if kp.Private[31]&0b01000000 == 0 {
			t.Fatalf("末字节次高位应置 1，实得 %08b", kp.Private[31])
		}
		if kp.Private.IsZero() || kp.Public.IsZero() {
			t.Fatal("不该出现全零密钥")
		}

		// 公钥必须能从私钥推出来（否则落盘后无法复现）。
		again, err := PublicFromPrivate(kp.Private)
		if err != nil {
			t.Fatal(err)
		}
		if again != kp.Public {
			t.Fatal("公钥推导不稳定")
		}

		if seen[kp.Private] {
			t.Fatal("生成了重复私钥 —— 随机源有问题")
		}
		seen[kp.Private] = true
	}
}

func TestClampIsIdempotent(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	before := kp.Private
	clampScalar(kp.Private[:])
	if kp.Private != before {
		t.Fatal("clamp 应是幂等的（对已 clamp 的私钥再夹一次不该改变它）")
	}
}

func TestKeyBytesIsCopy(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	b := kp.Private.Bytes()
	b[0] = ^b[0]
	if kp.Private[0] == b[0] {
		t.Fatal("Bytes() 应返回副本，改它不该影响原 Key")
	}
}
