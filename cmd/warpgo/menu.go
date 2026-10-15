package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/misakacoo/warpgo/internal/cfclient"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/proxy"
	"github.com/misakacoo/warpgo/internal/state"
)

// cmdMenu 是交互菜单（等价于旧版脚本的裸 `warp`）。
//
// 它是**薄封装**：每个选项最终都翻译成一条已经存在的子命令，再交给 run() 执行。
// 这样做而不是"在菜单里直接调内部函数"，有三个实际好处：
//   - 菜单与命令行走的是**同一条代码路径**，不会出现"命令行能修好、菜单修不好"；
//   - 每一项都能被脚本复现（把菜单里的 argv 抄下来就是那条命令）；
//   - 新增功能时不必改菜单框架，加一行映射即可。
func (a *app) cmdMenu(args []string) int {
	if len(args) > 0 {
		return a.fail(fmt.Errorf("%s", msg.Tf(msg.KeyBadOpt, args[0])))
	}
	in := bufio.NewReader(os.Stdin)

	for {
		a.printMenu()
		choice, err := a.ask(in, msg.T(msg.KeyMenuPrompt))
		if err != nil {
			// stdin 关闭（管道里跑）不算错误，安静退出即可。
			fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuBye))
			return exitOK
		}
		choice = strings.ToLower(strings.TrimSpace(choice))
		switch choice {
		case "":
			continue
		case "0", "q", "quit", "exit":
			fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuBye))
			return exitOK
		}

		argv, ok := a.menuArgv(choice, in)
		if !ok {
			a.log.Warnf("%s", msg.Tf(msg.KeyMenuBadChoice, choice))
			continue
		}
		if argv == nil {
			continue // 用户在追问里取消
		}
		fmt.Fprintln(a.stdout)
		if code := run(argv); code != exitOK {
			a.log.Warnf("%s", msg.Tf(msg.KeyMenuSubFailed, strings.Join(argv, " "), code))
		}
		fmt.Fprintln(a.stdout)
	}
}

// menuEntry 是一个菜单项。
type menuEntry struct {
	choice string
	label  string // 文案 key
	argv   []string
}

// menuEntries 是菜单的静态部分（不需要追问当前状态的那些）。
func menuEntries() []menuEntry {
	return []menuEntry{
		{"1", msg.KeyMenuUpNonGlobal, []string{"up"}},
		{"2", msg.KeyMenuUpGlobal, []string{"up", "--mode", "global"}},
		{"3", msg.KeyMenuDown, []string{"down"}},
		{"4", msg.KeyMenuStatus, []string{"status"}},
		{"5", msg.KeyMenuRotate, []string{"rotate"}},
		{"6", msg.KeyMenuSwitchBackend, nil}, // 需要读运行态决定方向
		{"7", msg.KeyMenuSwitchMode, nil},    // 同上
		{"8", msg.KeyMenuPriority, nil},      // 需要追问 4 / 6 / d
		{"9", msg.KeyMenuClientProxy, []string{"client", "install", "--mode", "proxy"}},
		{"10", msg.KeyMenuClientWarp, []string{"client", "install", "--mode", "warp"}},
		{"11", msg.KeyMenuClientToggle, nil}, // 按当前状态决定 on / off
		{"12", msg.KeyMenuClientStatus, []string{"client", "status"}},
		{"13", msg.KeyMenuProxyInstall, []string{"proxy", "install"}},
		{"14", msg.KeyMenuProxyToggle, nil}, // 按当前状态决定 on / off
		{"15", msg.KeyMenuInstallWatch, []string{"install"}},
		{"16", msg.KeyMenuUninstall, []string{"uninstall"}},
	}
}

// printMenu 打印菜单。
func (a *app) printMenu() {
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, msg.T(msg.KeyMenuTitle))
	for _, e := range menuEntries() {
		fmt.Fprintf(a.stdout, "  %2s) %s\n", e.choice, msg.T(e.label))
	}
	fmt.Fprintf(a.stdout, "   %s) %s\n", "0", msg.T(msg.KeyMenuQuit))
	fmt.Fprintln(a.stdout)
}

// menuArgv 把选项翻译成一条子命令。返回 (argv, ok)；
// argv 为 nil 表示"用户取消了"（不是错误）。
func (a *app) menuArgv(choice string, in *bufio.Reader) ([]string, bool) {
	for _, e := range menuEntries() {
		if e.choice != choice {
			continue
		}
		switch choice {
		case "6":
			return a.menuSwitchBackend(in)
		case "7":
			return a.menuSwitchMode(in)
		case "8":
			return a.menuPriority(in)
		case "11":
			return a.menuClientToggle(in)
		case "14":
			return a.menuProxyToggle(in)
		default:
			return e.argv, true
		}
	}
	return nil, false
}

// menuSwitchBackend 读运行态，给出"换到另一个后端"的命令。
func (a *app) menuSwitchBackend(in *bufio.Reader) ([]string, bool) {
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil || rt.Backend == "" {
		a.log.Warnf("%s", msg.T(msg.KeyMenuNoRuntime))
		return nil, true
	}
	target := "userspace"
	if rt.Backend == "userspace" {
		target = "kernel"
	}
	a.log.Infof("%s", msg.Tf(msg.KeyMenuSwitchTo, rt.Backend, target))
	return []string{"switch", "--backend", target}, true
}

// menuSwitchMode 读运行态，给出"换到另一个工作模式"的命令。
func (a *app) menuSwitchMode(in *bufio.Reader) ([]string, bool) {
	rt, err := state.LoadRuntime(state.RuntimePath())
	if err != nil || rt.Mode == "" {
		a.log.Warnf("%s", msg.T(msg.KeyMenuNoRuntime))
		return nil, true
	}
	target := "global"
	if rt.Mode == "global" {
		target = "non-global"
	}
	a.log.Infof("%s", msg.Tf(msg.KeyMenuSwitchTo, rt.Mode, target))
	return []string{"switch", "--mode", target}, true
}

// menuPriority 追问地址族优先级。
func (a *app) menuPriority(in *bufio.Reader) ([]string, bool) {
	v, err := a.ask(in, msg.T(msg.KeyMenuAskPriority))
	if err != nil {
		return nil, true
	}
	v = strings.TrimSpace(v)
	switch v {
	case "4", "6", "d":
		return []string{"priority", v}, true
	default:
		a.log.Warnf("%s", msg.Tf(msg.KeyMenuBadChoice, v))
		return nil, true
	}
}

// menuClientToggle 按当前状态决定是开还是关。
//
// 这里直接查状态，而不是跑一次 `client status` 再拿它的退出码当判据 ——
// 后者会把一大段 JSON 打到屏幕上，而用户只是选了个菜单项。
func (a *app) menuClientToggle(in *bufio.Reader) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	if st, err := cfclient.New().Query(ctx); err == nil && st.Connected() {
		return []string{"client", "off"}, true
	}
	return []string{"client", "on"}, true
}

// menuProxyToggle 同上，针对 WireProxy。
func (a *app) menuProxyToggle(in *bufio.Reader) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), upTimeout)
	defer cancel()
	if active, err := proxy.New().Systemd().IsActive(ctx); err == nil && active {
		return []string{"proxy", "off"}, true
	}
	return []string{"proxy", "on"}, true
}

// ask 打印提示并读一行。
func (a *app) ask(in *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(a.stdout, prompt)
	line, err := in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && strings.TrimSpace(line) != "" {
			// 最后一行没有换行符也要收下（管道输入常见）。
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}
