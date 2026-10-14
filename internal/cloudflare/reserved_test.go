package cloudflare

import (
	"encoding/json"
	"errors"
	"testing"
)

// y8eh 与 4A3x 是上游 api.sh 注释里给出的实测样例。
// y8eh → [203,199,161] 正是旧算法（按字符码点）算错的那一个：
// 它把 0xA1 当成码点 161 后再按 UTF-8 处理，得到 737，>255 被补丁版
// wireguard-go 静默回落成 [0,0,0]。这个用例是整条 reserved 修复的锚点，
// 一旦有人"优化"回字符运算，它会立刻红。
func TestReservedFromClientID(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Reserved
		wantErr error
	}{
		{"上游实测样例 y8eh", "y8eh", Reserved{203, 199, 161}, nil},
		{"第二个样例 4A3x", "4A3x", Reserved{224, 13, 241}, nil},
		{"带前后空白也能解", "  y8eh\t", Reserved{203, 199, 161}, nil},
		{"带 padding 的标准 base64", "y8eh", Reserved{203, 199, 161}, nil},
		{"空字符串", "", Reserved{}, ErrEmptyClientID},
		{"只有空白", "   ", Reserved{}, ErrEmptyClientID},
		{"不是 base64", "!!!!", Reserved{}, ErrBadClientID},
		{"解码后不足 3 字节", "y8", Reserved{}, ErrShortClientID},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReservedFromClientID(c.in)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("期望错误 %v，实得 %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if got != c.want {
				t.Fatalf("期望 %v，实得 %v", c.want, got)
			}
		})
	}
}

// 显式钉住"旧算法会得到不同答案"这件事。
//
// 如果哪天有人用字符码点重写这段逻辑，[203,199,161] 会变成 [203,199,737]，
// 本条会失败并指出原因。它比注释更难被忽略。
func TestReservedIsNotCodepointArithmetic(t *testing.T) {
	got, err := ReservedFromClientID("y8eh")
	if err != nil {
		t.Fatal(err)
	}
	if got[2] != 161 {
		t.Fatalf("第三字节应为 161，实得 %d", got[2])
	}
	// 0x61 = 'a' 的码点。若实现退化成取字符码点，第 3 个字符 'e'(101) 或
	// UTF-8 组合出来的值会落在这里，而不是 161。
	if got[2] == 101 || got[2] == 737%256 {
		t.Fatalf("第三字节 %d 看起来是按字符码点算的", got[2])
	}
}

func TestReservedJSONRoundTrip(t *testing.T) {
	orig := Reserved{203, 199, 161}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[203,199,161]" {
		t.Fatalf("JSON 形式应为三元数组，实得 %s", b)
	}

	var back Reserved
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != orig {
		t.Fatalf("往返后不一致: %v != %v", back, orig)
	}

	// null 视为全零，不该报错（旧账户文件里可能没有这个字段）。
	var zero Reserved
	if err := json.Unmarshal([]byte("null"), &zero); err != nil {
		t.Fatalf("null 不该报错: %v", err)
	}
	if !zero.IsZero() {
		t.Fatalf("null 应解成全零，实得 %v", zero)
	}

	// 越界值必须报错，而不是静默截断 —— 截断正是上游那个 bug 的表现形式。
	var bad Reserved
	if err := json.Unmarshal([]byte("[203,199,737]"), &bad); err == nil {
		t.Fatal("737 超出 uint8 范围，应该报错而不是截断")
	}
}

func TestReservedString(t *testing.T) {
	// 这个格式是生态约定（`# Reserved = 203, 199, 161`），不能随便改。
	if got := (Reserved{203, 199, 161}).String(); got != "203, 199, 161" {
		t.Fatalf("实得 %q", got)
	}
	if !(Reserved{}).IsZero() {
		t.Fatal("全零应判为 IsZero")
	}
	if (Reserved{0, 0, 1}).IsZero() {
		t.Fatal("非全零不该判为 IsZero")
	}
}

func TestMustReservedSwallowsError(t *testing.T) {
	// 展示路径上拿不到 reserved 不该中断主流程，只应退化成全零。
	if got := MustReservedFromClientID("!!!"); !got.IsZero() {
		t.Fatalf("非法输入应退化成全零，实得 %v", got)
	}
	if got := MustReservedFromClientID("y8eh"); got != (Reserved{203, 199, 161}) {
		t.Fatalf("合法输入不该受影响，实得 %v", got)
	}
}
