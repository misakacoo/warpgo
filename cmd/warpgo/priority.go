package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/gai"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/system"
)

// defaultOrderHost 是观察解析顺序用的域名。
//
// 选它是因为它是最稳的双栈域名之一（A 与 AAAA 都有，且常年不变）。
// 用户可以用 --host 换掉 —— 换本地内网域名之类的场景需要它。
const defaultOrderHost = "www.cloudflare.com"

// priorityView 是 `priority --json` 的输出形状。
type priorityView struct {
	Path       string   `json:"path"`
	Mode       string   `json:"mode"`
	Managed    []string `json:"managed_lines"`
	Exists     bool     `json:"file_exists"`
	OrderHost  string   `json:"order_host,omitempty"`
	OrderFirst string   `json:"order_first,omitempty"`
	OrderNote  string   `json:"order_note,omitempty"`
}

// cmdPriority 实现菜单 `s`：IPv4 / IPv6 优先级。
//
// 用法上刻意同时接受两种写法：`priority 4`（与菜单一致，敲一个字符）
// 和 `priority --mode 4`（脚本友好）。
func (a *app) cmdPriority(args []string) int {
	var (
		posMode  string // 位置参数：`priority 4`
		flagMode string // 选项：`priority --mode 4`
		host     string
		asJSON   bool
	)

	sc := &argScanner{args: args}
	for !sc.done() {
		arg := sc.cur()
		switch {
		case arg == "--json":
			asJSON = true
		case arg == "--mode":
			v, ok := sc.next()
			if !ok {
				return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyNeedArg, arg)))
			}
			if flagMode != "" {
				return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyPriorityTooMany, v)))
			}
			flagMode = v
		case strings.HasPrefix(arg, "--mode="):
			v := strings.TrimPrefix(arg, "--mode=")
			if flagMode != "" {
				return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyPriorityTooMany, v)))
			}
			flagMode = v
		case arg == "--host":
			v, ok := sc.next()
			if !ok {
				return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyNeedArg, arg)))
			}
			host = v
		case strings.HasPrefix(arg, "--host="):
			host = strings.TrimPrefix(arg, "--host=")
		case strings.HasPrefix(arg, "-"):
			return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, arg)))
		default:
			// 位置参数：`warpgo priority 4`。
			if posMode != "" {
				return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyPriorityTooMany, arg)))
			}
			posMode = arg
		}
		sc.i++
	}

	// 两种写法**同时**出现时报错，而不是让后者盖掉前者。
	// `priority 4 --mode 6` 里到底该听谁的没有正确答案，而"悄悄选一个"会让
	// 用户以为自己的命令生效了 —— 这种含糊最贵。
	modeStr := posMode
	if flagMode != "" {
		if posMode != "" {
			return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyPriorityTooMany, "--mode "+flagMode)))
		}
		modeStr = flagMode
	}

	if host == "" {
		host = defaultOrderHost
	}
	mgr := gai.New(gai.DefaultPath)

	if modeStr == "" {
		return a.showPriority(mgr, host, asJSON)
	}
	return a.setPriority(mgr, modeStr)
}

// setPriority 写入优先级。
func (a *app) setPriority(mgr *gai.Manager, modeStr string) int {
	// 会改系统文件，非 Debian 直接拒绝（与 up / install / switch 一致）。
	if _, ok := system.DebianVersion(); !ok {
		a.log.Errorf("%s", msg.Tf(msg.KeyPriorityNotDebian, system.PrettyName()))
		return exitNotDebian
	}

	mode, err := gai.ParseMode(modeStr)
	if err != nil {
		return a.fail(err)
	}
	changed, err := mgr.Set(mode)
	if err != nil {
		return a.fail(err)
	}

	if !changed {
		a.log.Infof("%s", msg.Tf(msg.KeyPriorityUnchanged, priorityLabel(mode), mgr.Path))
		return exitOK
	}
	a.log.Infof("%s", msg.Tf(msg.KeyPrioritySet, priorityLabel(mode), mgr.Path))

	// ModeV6 在 Debian 12 上不改变任何行为 —— 这一点必须说出来，
	// 否则用户会以为"设了 6 却没效果"是程序坏了。见 internal/gai 的说明。
	if mode == gai.ModeV6 {
		a.log.Warnf("%s", msg.T(msg.KeyPriorityV6Caveat))
	}
	return exitOK
}

// showPriority 打印当前状态。
//
// 它做两件事：读文件（确定的事实）+ 用 getent 观察 glibc 真正给出的顺序。
// 后者是必要的 —— 光看文件里的行无法说明"到底有没有生效"。
func (a *app) showPriority(mgr *gai.Manager, host string, asJSON bool) int {
	mode, exists, err := mgr.Detect()
	if err != nil {
		return a.fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, note := observeOrder(ctx, host)

	if asJSON {
		v := priorityView{
			Path:       mgr.Path,
			Mode:       string(mode),
			Managed:    managedLines(mgr),
			Exists:     exists,
			OrderHost:  host,
			OrderFirst: first,
			OrderNote:  note,
		}
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return a.fail(err)
		}
		return exitOK
	}

	fmt.Fprintln(a.stdout, msg.T(msg.KeyPriorityTitle))
	fields := [][2]string{
		{"gai.conf", priorityLabel(mode)},
		{"path", fmt.Sprintf("%s（存在: %s）", mgr.Path, yesNo(exists))},
		{"managed", joinOrDash(strings.Join(managedLines(mgr), " / "))},
		{"resolve_order", describeOrder(host, first)},
	}
	for _, f := range fields {
		fmt.Fprintf(a.stdout, "  %-18s %s\n", f[0], f[1])
	}
	if note != "" {
		a.log.Warnf("%s", note)
	}
	a.log.Infof("%s", msg.T(msg.KeyPriorityHint))
	return exitOK
}

// managedLines 列出文件里当前存在的受管行。
func managedLines(mgr *gai.Manager) []string {
	content, found, err := mgr.Current()
	if err != nil || !found {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(content), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "precedence ::ffff:0:0/96") || strings.HasPrefix(t, "label 2002::/16") {
			out = append(out, t)
		}
	}
	return out
}

// observeOrder 用 getent 观察 glibc 真正给出的地址顺序。
//
// 为什么必须借外部命令：**Go 自己的解析器不读 gai.conf**。它按 RFC 6724 自己排，
// 用它去测这件事会得出"gai.conf 无效"的错误结论 —— 而这正是本包想避免的
// 那类"用错误的工具验证行为"的坑。
//
// 返回值：第一行地址（可能为空）、以及一句人话说明。
func observeOrder(ctx context.Context, host string) (first, note string) {
	out, err := exec.CommandContext(ctx, "getent", "ahosts", host).Output()
	if err != nil {
		return "", msg.Tf(msg.KeyPriorityNoGetent, host, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 {
			return f[0], ""
		}
	}
	return "", msg.Tf(msg.KeyPriorityNoGetent, host, "没有解析结果")
}

// describeOrder 把"第一条解析结果"转成人话。
func describeOrder(host, first string) string {
	if first == "" {
		return msg.Tf(msg.KeyPriorityOrderUnknown, host)
	}
	// 文案只有两条（v4 优先 / v6 优先），而不是"族名 + 优先"拼出来：
	// 本项目有一条硬规矩 —— 每条文案都必须含汉字，纯英文的族名标签过不了单测，
	// 而"IPv4 优先（…）"这种整句本来也更好读。
	key := msg.KeyPriorityOrderV6
	if addr, err := netip.ParseAddr(first); err == nil && addr.Is4() {
		key = msg.KeyPriorityOrderV4
	}
	return msg.Tf(key, first, host)
}

// priorityLabel 返回模式的中文名。
func priorityLabel(mode gai.Mode) string {
	switch mode {
	case gai.ModeV4:
		return msg.T(msg.KeyPriorityV4)
	case gai.ModeV6:
		return msg.T(msg.KeyPriorityV6)
	default:
		return msg.T(msg.KeyPriorityDefault)
	}
}
