package msg

import (
	"sort"
	"strings"
	"testing"
)

// 声明的 key 与文案表必须**一一对应**。
//
// 两个方向都要守：
//   - 声明了却没写文案 → 用户屏幕上出现 `usage.lock_busy` 这种丑字符串；
//   - 写了文案却没有常量 → 一条谁也用不到的死文案，改起来还会以为改对了地方。
//
// 在只有一种语言的时候这条反而更重要了：以前两份 map 至少能互相校对，
// 现在只剩一份，唯一的锚点就是这个常量清单。
func TestDeclaredKeysMatchText(t *testing.T) {
	declared := AllKeys()

	seen := map[string]bool{}
	for _, k := range declared {
		if seen[k] {
			t.Errorf("AllKeys() 里有重复项: %s", k)
		}
		seen[k] = true
		if !Has(k) {
			t.Errorf("声明了 %s 但没有对应文案", k)
		}
	}

	orphans := []string{}
	for _, k := range Keys() {
		if !seen[k] {
			orphans = append(orphans, k)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("文案表里有 %d 条没有声明的 key: %s", len(orphans), strings.Join(orphans, ", "))
	}
}

// 查不到 key 时必须返回 key 本身，**绝不能返回空串**。
// 这是上游踩出来的教训：文案取值为空会让整个菜单空白且毫无报错。
func TestMissingKeyReturnsKeyNotBlank(t *testing.T) {
	const k = "definitely.not.a.key"
	got := T(k)
	if got == "" {
		t.Fatal("缺失 key 时返回了空串 —— 屏幕上会是一片空白，而且没有任何报错")
	}
	if got != k {
		t.Fatalf("期望返回 key 本身，实际 %q", got)
	}
}

// 空文案同样要退回 key（有人把某条文案写成空串时不该静默消失）。
func TestEmptyTextFallsBackToKey(t *testing.T) {
	const k = "__test.empty__"
	text[k] = ""
	defer delete(text, k)

	if got := T(k); got != k {
		t.Fatalf("空文案应退回 key，实际 %q", got)
	}
	if Has(k) {
		t.Fatal("空文案不该被当成「有文案」")
	}
}

func TestTfFormats(t *testing.T) {
	got := Tf(KeyLockBusy, 1234)
	if got == "" || got == KeyLockBusy {
		t.Fatalf("Tf 应套用参数，实际 %q", got)
	}
	if strings.Contains(got, "%") {
		t.Fatalf("格式化后仍残留 %%：%q", got)
	}
	if !strings.Contains(got, "1234") {
		t.Fatalf("参数没有出现在结果里：%q", got)
	}
}

// nonChineseAllowed 列出"本来就不含汉字"的文案。
//
// 用白名单而不是放宽判据：一旦把规则改成"允许短文案没有汉字"，
// 这个守卫就形同虚设了 —— 漏改的英文恰好都是短句。
var nonChineseAllowed = map[string]string{
	// 程序名本身。它不翻译，也不该翻译。
	KeyVersion: "warpgo",
}

// 文案表必须是中文。
//
// 这条不检查"没有英文单词"（技术名词本来就该保留原文，例如 `WireGuard`、
// `resolv.conf`），只要求**每条文案都含汉字**。理由：一条漏改的英文文案
// 在屏幕上与"缺文案"很难区分，用户只会觉得"这句话怎么怪怪的"，
// 而在这里拦住是一句话的事。
func TestEveryTextIsChinese(t *testing.T) {
	for _, k := range AllKeys() {
		if _, ok := nonChineseAllowed[k]; ok {
			// 白名单里的条目要额外确认它没被改坏。
			if T(k) != nonChineseAllowed[k] {
				t.Errorf("%s 在白名单里，但内容变了（现在 %q，白名单记的是 %q）",
					k, T(k), nonChineseAllowed[k])
			}
			continue
		}
		v := T(k)
		if !hasHan(v) {
			t.Errorf("%s 的文案里没有任何汉字（可能是漏改的英文残留）:\n%s", k, v)
		}
	}
}

// 语言机制已经移除，相关痕迹不该残留。
func TestLanguageMachineryIsGone(t *testing.T) {
	if Has("cli.bad_language") {
		t.Error("cli.bad_language 应当随语言机制一起删掉")
	}
	usage := T(KeyUsage)
	if strings.Contains(usage, "--lang") {
		t.Error("usage 里不该再出现 --lang")
	}
	for _, bad := range []string{"LANG", "语言"} {
		if strings.Contains(usage, bad) {
			t.Errorf("usage 里不该再提到 %q", bad)
		}
	}
}

// hasHan 报告字符串里是否含汉字。
func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
