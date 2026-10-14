// Package i18n 提供中英双语文案。
//
// 设计取舍：
//   - 不引入任何 i18n 框架。两个 map + 一个 T() 就够了，框架只会增加依赖和认知成本。
//   - **查不到 key 时返回 key 本身，绝不返回空串**。这是从上游踩出来的教训：
//     上游用 nameref 取文案，bash < 4.3 时所有文案变成空串，菜单整片空白却毫无报错，
//     用户只能看到「什么都没显示」。宁可让人看到 "usage.lock_busy" 这种丑字符串，
//     也不能让人看到空白 —— 前者一眼就知道是缺翻译，后者要查半天。
package i18n

import (
	"fmt"
	"os"
	"strings"
)

// Lang 语言。
type Lang string

// 支持的语言。
const (
	EN Lang = "en"
	ZH Lang = "zh"
)

// Parse 解析语言标识（"zh"/"zh-CN"/"zh_CN"/"en"/"en-US"...）。
func Parse(s string) (Lang, bool) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case v == "":
		return "", false
	case strings.HasPrefix(v, "zh"):
		return ZH, true
	case strings.HasPrefix(v, "en"):
		return EN, true
	default:
		return "", false
	}
}

// Detect 按 LC_ALL → LC_MESSAGES → LANG 的顺序推断语言，都不认识则返回 EN。
func Detect() Lang {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			if l, ok := Parse(v); ok {
				return l
			}
		}
	}
	return EN
}

// Bundle 某个语言的文案集合。
type Bundle struct {
	lang Lang
}

// New 创建指定语言的文案集合；不认识的语言退回 EN。
func New(l Lang) *Bundle {
	if l != ZH {
		l = EN
	}
	return &Bundle{lang: l}
}

// Lang 返回当前语言。
func (b *Bundle) Lang() Lang { return b.lang }

// T 取文案。查不到时返回 key 本身（见包注释）。
func (b *Bundle) T(key string) string {
	var m map[string]string
	if b.lang == ZH {
		m = zh
	} else {
		m = en
	}
	if s, ok := m[key]; ok && s != "" {
		return s
	}
	return key
}

// Tf 取文案并套用格式化参数。
func (b *Bundle) Tf(key string, a ...any) string {
	return fmt.Sprintf(b.T(key), a...)
}

// Has 报告某个 key 是否有翻译（自测用：两种语言必须覆盖同一批 key）。
func (b *Bundle) Has(key string) bool {
	m := en
	if b.lang == ZH {
		m = zh
	}
	s, ok := m[key]
	return ok && s != ""
}

// KeysEN / KeysZH 暴露 key 集合，供自测断言两种语言键集一致。
func KeysEN() []string { return keys(en) }
func KeysZH() []string { return keys(zh) }

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
