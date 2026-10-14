package i18n

import (
	"os"
	"sort"
	"testing"
)

// 两种语言的 key 必须完全一致 —— 漏翻译在运行期表现为「屏幕上出现 usage.xxx 这种丑字符串」，
// 与其等用户发现，不如在 CI 里就拦住。
func TestKeySetsAreIdentical(t *testing.T) {
	a, b := KeysEN(), KeysZH()
	sort.Strings(a)
	sort.Strings(b)

	set := func(s []string) map[string]bool {
		m := make(map[string]bool, len(s))
		for _, k := range s {
			m[k] = true
		}
		return m
	}
	ma, mb := set(a), set(b)

	for k := range ma {
		if !mb[k] {
			t.Errorf("中文缺少 key: %s", k)
		}
	}
	for k := range mb {
		if !ma[k] {
			t.Errorf("英文缺少 key: %s", k)
		}
	}
}

// 常量里声明的 key 必须真的存在，否则就是「声明了但没写文案」。
func TestDeclaredKeysExist(t *testing.T) {
	declared := []string{
		KeyUsage, KeyVersion, KeyUnknownCm, KeyBadLang, KeyNeedArg, KeyBadOpt,
		KeyLockBusy, KeyLockAcquired, KeyLockReleased, KeyLockTakeoverStale,
		KeyLockTakeoverLegacy, KeyLockTakeoverUnverif, KeyLockLeftover,
		KeyLockKillFailed, KeyLockTouch,
		KeyPlatformDebian, KeyPlatformNotDebia,
	}
	for _, k := range declared {
		if !New(EN).Has(k) {
			t.Errorf("英文文案缺少 %s", k)
		}
		if !New(ZH).Has(k) {
			t.Errorf("中文文案缺少 %s", k)
		}
	}
}

// 查不到 key 时必须返回 key 本身，**绝不能返回空串**。
// 这是上游踩出来的教训：文案取值为空会让整个菜单空白且毫无报错。
func TestMissingKeyReturnsKeyNotBlank(t *testing.T) {
	for _, l := range []Lang{EN, ZH} {
		got := New(l).T("definitely.not.a.key")
		if got == "" {
			t.Fatalf("%s: 缺失 key 时返回了空串", l)
		}
		if got != "definitely.not.a.key" {
			t.Fatalf("%s: 期望返回 key 本身，实际 %q", l, got)
		}
	}
}

// 空文案同样要退回 key（有人把某条翻译写成空串时不该静默消失）。
func TestEmptyTranslationFallsBackToKey(t *testing.T) {
	const k = "__test.empty__"
	en[k] = ""
	zh[k] = ""
	defer func() { delete(en, k); delete(zh, k) }()

	if got := New(EN).T(k); got != k {
		t.Fatalf("空文案应退回 key，实际 %q", got)
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Lang
		ok   bool
	}{
		{"zh", ZH, true},
		{"zh-CN", ZH, true},
		{"zh_CN.UTF-8", ZH, true},
		{"ZH-TW", ZH, true},
		{"en", EN, true},
		{"en_US.utf8", EN, true},
		{"C.UTF-8", "", false},
		{"", "", false},
		{"ja_JP", "", false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = (%q,%v)，期望 (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDetect(t *testing.T) {
	keys := []string{"LC_ALL", "LC_MESSAGES", "LANG"}
	saved := make(map[string]string, len(keys))
	for _, k := range keys {
		saved[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
				continue
			}
			os.Setenv(k, v)
		}
	}()

	if got := Detect(); got != EN {
		t.Fatalf("没有任何 locale 变量时应为 EN，实际 %s", got)
	}
	os.Setenv("LANG", "zh_CN.UTF-8")
	if got := Detect(); got != ZH {
		t.Fatalf("LANG=zh_CN.UTF-8 时应为 ZH，实际 %s", got)
	}
	// LC_ALL 优先级高于 LANG
	os.Setenv("LC_ALL", "en_US.UTF-8")
	if got := Detect(); got != EN {
		t.Fatalf("LC_ALL 应覆盖 LANG，实际 %s", got)
	}
}

func TestTf(t *testing.T) {
	b := New(ZH)
	got := b.Tf(KeyLockBusy, 1234)
	if got == "" || got == KeyLockBusy {
		t.Fatalf("Tf 应套用参数，实际 %q", got)
	}
	// %d 必须被替换掉
	for i := 0; i < len(got); i++ {
		if got[i] == '%' {
			t.Fatalf("格式化后仍残留 %%：%q", got)
		}
	}
}

func TestNewFallsBackToEN(t *testing.T) {
	b := New(Lang("ja"))
	if b.Lang() != EN {
		t.Fatalf("不支持的语言应退回 EN，实际 %s", b.Lang())
	}
}
