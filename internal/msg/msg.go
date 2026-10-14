// Package msg 是本项目的文案表 —— **只保留简体中文**。
//
// 这里曾经是中英双语的（按 `LANG` 推断，并可用 `--lang` 强制）。2026-10-15
// 按用户要求移除英文与语言选择：命令行、日志、菜单**一律中文**，不再有任何
// 语言分支。
//
// 改名为 `msg` 是刻意的：包名叫 `i18n`（internationalization）却只装一种语言，
// 会让人以为还存在多语言机制；留着 `Bundle.Lang()` 这类接口更是死代码。
//
// 保留下来的硬约定只有一条：
//
//   - **查不到 key 时返回 key 本身，绝不返回空串**。这是从上游踩出来的教训 ——
//     上游用 nameref 取文案，bash < 4.3 时所有文案变成空串，菜单整片空白却毫无
//     报错，用户只能看到「什么都没显示」。宁可让人看到 `usage.lock_busy`
//     这种丑字符串，也不能让人看到空白：前者一眼就知道是缺文案，后者要查半天。
package msg

import (
	"fmt"
	"sort"
)

// T 取文案。查不到时返回 key 本身（见包注释）。
func T(key string) string {
	if s, ok := text[key]; ok && s != "" {
		return s
	}
	return key
}

// Tf 取文案并套用格式化参数。
func Tf(key string, a ...any) string {
	return fmt.Sprintf(T(key), a...)
}

// Has 报告某个 key 是否有文案。
func Has(key string) bool {
	s, ok := text[key]
	return ok && s != ""
}

// Keys 返回文案表里实际存在的全部 key（自测用）。
//
// 排序后返回：断言失败时的输出才是稳定的。否则每次跑出来的顺序都不一样，
// 排查时得重新读一遍才知道到底差在哪。
func Keys() []string {
	out := make([]string, 0, len(text))
	for k := range text {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
