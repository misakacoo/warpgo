// Package gai 管理 /etc/gai.conf 里的「地址族优先级」。
//
// 它解决的问题很具体：一台同时有 IPv4 和 IPv6 出口的机器，访问一个双栈域名时
// glibc 会先返回哪一族。上游把这个开关放在菜单 `s`，并让人以为有两种可选行为；
// 实测下来并不是（见下面的 LineV6 说明）—— 本包把这件事**验证过的结论**写进代码，
// 而不是照抄一个看起来对称、实际一半是空转的开关。
//
// # 与文件本身的关系
//
// 我们**只增删自己认识的那两行**，其余内容逐字节保留（包括注释与空行）。
// 正因为如此，`ModeDefault` 天然就是「把那两行去掉」，于是：
//   - 反复设置同一个模式是幂等的（第二次一个字节都不写）；
//   - 卸载时的还原不需要备份文件 —— 「回到默认」就是「移除我们写过的东西」。
//
// 这与 resolv.conf 的处理方式不同（那里我们是整文件替换，所以必须备份），
// 差别来自操作本身：这里是可逆的增量编辑，那里是不可逆的覆盖。
package gai

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/misakacoo/warpgo/internal/state"
)

// DefaultPath 是 glibc 的地址选择配置文件。
const DefaultPath = "/etc/gai.conf"

// Mode 是优先级取值。取值与上游菜单一致（4 / 6 / d），便于对照。
type Mode string

const (
	// ModeDefault 表示不做任何干预：glibc 按它内建的 RFC 6724 表决定顺序。
	ModeDefault Mode = "d"
	// ModeV4 表示优先 IPv4。
	ModeV4 Mode = "4"
	// ModeV6 表示优先 IPv6。
	ModeV6 Mode = "6"
)

// 受管的两行。
//
// `precedence ::ffff:0:0/96  100` 把 IPv4-mapped 地址的优先级提到最高，
// 于是 getaddrinfo 会先给出 A 记录。**真机实测有效**（Debian 12 / glibc 2.36，
// getent 的第一条从 2606:4700::… 变成 104.16.123.96）。
//
// `label 2002::/16   2` 是生态里流传的「IPv6 优先」写法，上游 fscarmen/warp 用的
// 正是它。但**它在 Debian 12 上不改变任何行为**：实测它与「两行都不写」的解析顺序
// 完全一致 —— 因为 glibc 的默认表（precedence ::/0 = 40 > ::ffff:0:0/96 = 10）
// 本来就是 IPv6 优先。保留它的理由只有两条：
//
//  1. 与上游逐行一致，排障时两边能对上；
//  2. ModeV6 会顺手删掉 v4 那一行 —— 万一机器上有别的工具写过它，
//     **那一步才是真正把顺序交还默认（= 优先 IPv6）的动作**。
//
// 取舍写在这里而不是藏进注释：宁可让读代码的人知道这行是"无害但无效"，
// 也不要让人以为菜单 s 的两个方向是对称的。
const (
	LineV4 = "precedence ::ffff:0:0/96  100"
	LineV6 = "label 2002::/16   2"
)

// ParseMode 解析用户给的取值。
//
// 刻意不接受空串：命令行漏参数与"明确要求默认"是两件事，前者必须报错。
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "4", "v4", "ipv4":
		return ModeV4, nil
	case "6", "v6", "ipv6":
		return ModeV6, nil
	case "d", "default":
		return ModeDefault, nil
	}
	return "", fmt.Errorf("未知的地址族优先级 %q（可用: 4 = IPv4 优先，6 = IPv6 优先，d = 系统默认）", s)
}

// Valid 报告取值是否有效。
func (m Mode) Valid() bool { return m == ModeDefault || m == ModeV4 || m == ModeV6 }

// Line 返回该模式需要写进文件的受管行；ModeDefault 返回空串（= 不写任何行）。
func (m Mode) Line() string {
	switch m {
	case ModeV4:
		return LineV4
	case ModeV6:
		return LineV6
	}
	return ""
}

// Manager 读写一个 gai.conf。
type Manager struct {
	// Path 一般就是 /etc/gai.conf（可覆盖，便于测试与非常规布局）。
	Path string
}

// New 创建 Manager；path 为空时用 /etc/gai.conf。
func New(path string) *Manager {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath
	}
	return &Manager{Path: path}
}

// Current 读取当前内容；文件不存在时返回 (nil, false, nil)。
func (m *Manager) Current() ([]byte, bool, error) {
	b, err := os.ReadFile(m.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读取 %s: %w", m.Path, err)
	}
	return b, true, nil
}

// Detect 报告文件里当前的受管状态。
//
// 返回的 Mode 只描述"我们那两行的组合"：恰好一行 → 就是那个模式；
// 两行都没有、或者两行都有（人为编辑出来的矛盾状态）→ ModeDefault，
// 因为对 glibc 来说那两种情况下 v4 那一行都不生效（见 DetectContent）。
func (m *Manager) Detect() (Mode, bool, error) {
	content, found, err := m.Current()
	if err != nil {
		return ModeDefault, false, err
	}
	return DetectContent(content), found, nil
}

// DetectContent 从内容判断受管状态（纯函数，便于单测）。
func DetectContent(content []byte) Mode {
	hasV4, hasV6 := false, false
	for _, line := range strings.Split(string(content), "\n") {
		switch managedKind(line) {
		case ModeV4:
			hasV4 = true
		case ModeV6:
			hasV6 = true
		}
	}
	switch {
	case hasV4 && !hasV6:
		return ModeV4
	case hasV6 && !hasV4:
		return ModeV6
	}
	// 两行都在时**不能**报 ModeV4：那样 `priority 4` 会变成"什么都不做"，
	// 而用户看到的状态与他的要求对不上。报默认，让 Set 去收敛。
	return ModeDefault
}

// Set 把文件调整成指定模式，返回本次是否真的改动了内容。
//
// 幂等：内容一致时直接返回 false，不写盘 —— 反复执行不会刷新 mtime，
// 也就不会有"看着像改了、其实没改"的错觉。
func (m *Manager) Set(mode Mode) (changed bool, err error) {
	if !mode.Valid() {
		return false, fmt.Errorf("无效的地址族优先级 %q", string(mode))
	}
	cur, _, err := m.Current()
	if err != nil {
		return false, err
	}
	next := Rewrite(cur, mode)
	if bytes.Equal(cur, next) {
		return false, nil
	}

	perm := fs.FileMode(0o644)
	if fi, serr := os.Stat(m.Path); serr == nil {
		perm = fi.Mode().Perm()
	}
	if err := state.WriteFileAtomic(m.Path, next, perm); err != nil {
		return false, fmt.Errorf("写入 %s: %w", m.Path, err)
	}
	return true, nil
}

// Rewrite 生成目标内容：删掉受管行、按模式追加一行，其余逐字节保留（纯函数）。
//
// 三个细节都是刻意处理的：
//   - 原文件**不以换行结尾**时，追加前必须补一个 `\n`，否则会把两行粘成一行；
//   - 只在 ModeDefault 且内容本来就空时才返回 nil（让 Set 把它当成"无需改动"）；
//   - 不 Trim 尾部的空行：用户的排版不该被我们顺手改掉。
//
// **唯一的例外**：如果原文件没有结尾换行，那么我们追加过一行之后它就有了
// （删掉那一行时无法知道当初的换行是不是我们补的）。Debian 的 gai.conf 本来就
// 以换行结尾，所以这条实际不会触发；记在这里是为了让"逐字节保留"这句话准确 ——
// 一个"大致保留"的承诺比一个明确的例外更危险。
func Rewrite(content []byte, mode Mode) []byte {
	lines := strings.Split(string(content), "\n")
	hadFinalNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if hadFinalNewline {
		lines = lines[:len(lines)-1]
	}

	kept := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		if managedKind(line) != "" {
			continue
		}
		kept = append(kept, line)
	}

	line := mode.Line()
	switch {
	case line != "":
		if len(kept) == 0 {
			return []byte(line + "\n")
		}
		return []byte(strings.Join(kept, "\n") + "\n" + line + "\n")
	case len(kept) == 0:
		return nil
	case hadFinalNewline:
		return []byte(strings.Join(kept, "\n") + "\n")
	default:
		return []byte(strings.Join(kept, "\n"))
	}
}

// managedKind 判断一行是不是受管行。
//
// 用分词比较而不是整串相等：`precedence  ::ffff:0:0/96   100`（多个空格）
// 或行尾带注释的写法同样是有效的指令，整串比较会漏掉它们 ——
// 漏掉的后果是"删不掉、又追加了一行"，文件里从此有两行互相矛盾的配置。
func managedKind(line string) Mode {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	// 行尾注释不影响语义，截掉再分词。
	if i := strings.IndexByte(trimmed, '#'); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	f := strings.Fields(trimmed)
	if len(f) < 3 {
		return ""
	}
	switch {
	case f[0] == "precedence" && f[1] == "::ffff:0:0/96" && f[2] == "100":
		return ModeV4
	case f[0] == "label" && f[1] == "2002::/16" && f[2] == "2":
		return ModeV6
	}
	return ""
}
