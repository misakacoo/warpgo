package netcfg

import (
	"fmt"
	"net/netip"
	"strings"
)

// Stack 是隧道**携带哪几族地址**（上游菜单 `4` / `6` / `d`）。
//
// 别和 §8.8 的"地址族优先级"混起来，它们解决的是两件不同的事：
//
//   - 这里决定**隧道里装了几族地址** —— 少一族就少一组地址、规则与路由；
//   - 优先级决定**解析器先问哪一族** —— 写的是 /etc/gai.conf。
//
// 上游把它们放在菜单的不同位置，名字却很像（都是 4 / 6 / d），
// 所以这里刻意用 `--stack` 而不是 `--family` 之类的名字。
type Stack string

const (
	// StackBoth 双栈（默认）：账户里有的族都装上。
	StackBoth Stack = "both"
	// StackV4 只装 IPv4。
	StackV4 Stack = "4"
	// StackV6 只装 IPv6。
	StackV6 Stack = "6"
)

// ParseStack 解析命令行给的取值。
//
// 空串按"双栈"处理（与 `up` 不带 `--stack` 时的语义一致）；
// 同时接受 `v4` / `ipv4` 这类写法，因为用户不一定记得上游那套单字符。
func ParseStack(s string) (Stack, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "both", "d", "dual", "all":
		return StackBoth, nil
	case "4", "v4", "ipv4":
		return StackV4, nil
	case "6", "v6", "ipv6":
		return StackV6, nil
	default:
		return "", fmt.Errorf("不认识的栈取值 %q（可用 4 / 6 / d）", s)
	}
}

// Families 返回这个栈允许的地址族；nil 表示**不限制**（双栈）。
func (s Stack) Families() []int {
	switch s {
	case StackV4:
		return []int{FamilyV4}
	case StackV6:
		return []int{FamilyV6}
	default:
		return nil
	}
}

// Allows 报告某个地址族是否在这个栈里。
func (s Stack) Allows(family int) bool {
	fams := s.Families()
	if fams == nil {
		return true
	}
	for _, f := range fams {
		if f == family {
			return true
		}
	}
	return false
}

// String 给界面用；空值按双栈显示（它与"没选过"是一回事）。
func (s Stack) String() string {
	if s == "" {
		return string(StackBoth)
	}
	return string(s)
}

// Normalize 把空值收敛成 StackBoth。
//
// 需要它是因为 `state.Runtime` 里的 Stack 可能是空串（老版本写的运行态文件），
// 而"空"在业务上就是双栈 —— 与其在每个使用点判空，不如在入口收敛一次。
func (s Stack) Normalize() Stack {
	if s == "" {
		return StackBoth
	}
	return s
}

// FamilyOfAddr 返回一个地址属于哪一族（FamilyV4 / FamilyV6）。
//
// 导出它是给"按栈过滤地址"用的 —— 那段逻辑在 cmd 层，而族判定只该有一份。
func FamilyOfAddr(a netip.Addr) int { return familyOfAddr(a) }
