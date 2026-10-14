// Package systemd 负责 systemd 单元的渲染与生命周期管理。
//
// 分成两层：
//   - Render 是**纯函数**，"参数 → 单元文本"，可以逐字节单测；
//   - Manager 负责调 systemctl，命令执行器与路径都可注入，于是"装/卸"的
//     流程也能在单测里跑一遍而不用真的碰这台机器的 systemd。
//
// 为什么用一个常驻服务而不是 timer：巡检要能表达"连续的时间线"，
// `journalctl -u warpgo-watch` 一眼就能看到"每 5 分钟一次、每次都说健康"。
// timer 把这些拆成一次次独立进程，排查时反而更难拼出全貌。
package systemd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 默认位置。
const (
	// UnitName 是保活单元名。
	UnitName = "warpgo-watch.service"
	// DefaultUnitDir 是 systemd 的单元目录。
	DefaultUnitDir = "/etc/systemd/system"
	// DefaultUnitPath 是单元文件完整路径。
	DefaultUnitPath = DefaultUnitDir + "/" + UnitName
	// DefaultBinPath 是安装后的二进制路径。
	//
	// 装到 /usr/bin 而不是留在解压目录：单元里必须写绝对路径，
	// 而"解压目录"对用户来说随时可能被删掉或改名，那样重启就失效了。
	DefaultBinPath = "/usr/bin/warpgo"
)

// 环境变量：覆盖安装位置。
//
// 存在的意义是让验收脚本与单测能在临时目录里把整套"装/卸"跑一遍，
// 而不必真的往这台机器的 /etc 与 /usr/bin 里写东西。
const (
	EnvUnitPath = "WARPGO_UNIT_PATH"
	EnvBinPath  = "WARPGO_BIN_PATH"
)

// DefaultRestartSec 是服务异常退出后的重启间隔。
//
// 30 秒不是随手取的：巡检本身是幂等的（健康时什么都不做），所以重启不会
// 造成出口 IP 抖动；但也没必要更短 —— 更短只会把一个持续存在的配置错误
// 变成一串刷屏日志。
const DefaultRestartSec = 30 * time.Second

// Paths 是安装位置。
type Paths struct {
	UnitPath string
	BinPath  string
}

// DefaultPaths 返回安装位置，环境变量可覆盖。
func DefaultPaths() Paths {
	p := Paths{UnitPath: DefaultUnitPath, BinPath: DefaultBinPath}
	if v := os.Getenv(EnvUnitPath); v != "" {
		p.UnitPath = v
	}
	if v := os.Getenv(EnvBinPath); v != "" {
		p.BinPath = v
	}
	return p
}

// UnitParams 是渲染单元所需的一切。
type UnitParams struct {
	// BinPath 是可执行文件路径；留空则用 DefaultBinPath。
	BinPath string
	// ExecStart 是 watch 子命令的完整参数（不含可执行文件路径）。
	ExecStart []string
	// RestartSec 是重启间隔；留空则用 DefaultRestartSec。
	RestartSec time.Duration
}

// Render 生成单元文件内容。
//
// 刻意**不写 ExecStop**：ExecStop 非零退出会让 systemd 把整个单元标成 failed，
// 并且会让 restart 的语义变得难以推理 —— 上游正是在这里翻过车。
// "清理现场"是 `warpgo down` / `warpgo uninstall` 的职责，不是停止服务的职责。
func Render(p UnitParams) string {
	bin := p.BinPath
	if bin == "" {
		bin = DefaultBinPath
	}
	restartSec := p.RestartSec
	if restartSec <= 0 {
		restartSec = DefaultRestartSec
	}

	args := make([]string, 0, len(p.ExecStart)+1)
	args = append(args, bin)
	args = append(args, p.ExecStart...)

	var b strings.Builder
	b.WriteString("# 本文件由 `warpgo install` 生成 —— 手工改动会在下次 install 时被覆盖。\n")
	b.WriteString("# 需要改参数就重跑 install（例如：warpgo install --interval 2m）。\n\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=warpgo WARP keepalive\n")
	b.WriteString("Documentation=https://github.com/misakacoo/warpgo\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(args, " "))
	b.WriteString("# 巡检是幂等的：健康时它一个字节都不改，所以重启不会造成出口 IP 抖动。\n")
	b.WriteString("Restart=always\n")
	fmt.Fprintf(&b, "RestartSec=%s\n", restartSec)
	b.WriteString("# 给一轮巡检留出收尾时间（探测 + 可能的重建）。\n")
	b.WriteString("TimeoutStopSec=60s\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// Runner 执行一条外部命令。
//
// 返回 (输出, 退出码, 错误)：**退出码非零不算错误** —— 那是命令的正常反馈
// （`systemctl is-active` 对未运行的单元就返回 3）。只有"根本跑不起来"
// （找不到可执行文件之类）才返回 err。
type Runner func(ctx context.Context, name string, args ...string) (string, int, error)

// ExecRunner 是默认的命令执行器。
func ExecRunner(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return buf.String(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return buf.String(), ee.ExitCode(), nil
	}
	return buf.String(), -1, err
}

// Manager 管理这个单元。
type Manager struct {
	// Paths 是安装位置；零值时用默认值。
	Paths Paths
	// Runner 可注入（测试用）。
	Runner Runner
}

// New 创建使用真实路径与真实 systemctl 的 Manager。
func New() *Manager { return &Manager{Paths: DefaultPaths(), Runner: ExecRunner} }

func (m *Manager) paths() Paths {
	p := m.Paths
	if p.UnitPath == "" {
		p.UnitPath = DefaultUnitPath
	}
	if p.BinPath == "" {
		p.BinPath = DefaultBinPath
	}
	return p
}

// UnitPath 返回单元文件路径。
func (m *Manager) UnitPath() string { return m.paths().UnitPath }

// BinPath 返回安装后的二进制路径。
func (m *Manager) BinPath() string { return m.paths().BinPath }

func (m *Manager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return ExecRunner
}

// WriteUnit 写入单元文件（0644，原子写）。
//
// 单元文件必须是 0644：systemd 读取它时可能已经降权，0600 会让单元无法加载，
// 而报错信息（"unit file is not readable"）并不直观。
func (m *Manager) WriteUnit(content string) error {
	path := m.UnitPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时单元文件: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时单元文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("重命名到 %s: %w", path, err)
	}
	return nil
}

// UnitExists 报告单元文件是否存在。
func (m *Manager) UnitExists() bool {
	_, err := os.Stat(m.UnitPath())
	return err == nil
}

// RemoveUnit 删除单元文件（不存在不算错）。
func (m *Manager) RemoveUnit() error {
	if err := os.Remove(m.UnitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除单元 %s: %w", m.UnitPath(), err)
	}
	return nil
}

// systemctl 动作。
func (m *Manager) systemctl(ctx context.Context, args ...string) (string, int, error) {
	return m.runner()(ctx, "systemctl", args...)
}

// DaemonReload 让 systemd 重新读取单元文件。
//
// 写完单元必须调用它，否则 systemctl 看到的还是旧内容 —— 这会表现为
// "明明改了参数却没生效"，重试一次还是同样结果，很容易被误判成
// "参数根本没写进去"。
func (m *Manager) DaemonReload(ctx context.Context) error {
	out, code, err := m.systemctl(ctx, "daemon-reload")
	if err != nil {
		return fmt.Errorf("执行 systemctl daemon-reload: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl daemon-reload 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// Enable 设置开机自启。
func (m *Manager) Enable(ctx context.Context) error { return m.must(ctx, "启用", "enable", UnitName) }

// Disable 取消开机自启。
func (m *Manager) Disable(ctx context.Context) error {
	return m.must(ctx, "禁用", "disable", UnitName)
}

// Start 启动。
func (m *Manager) Start(ctx context.Context) error { return m.must(ctx, "启动", "start", UnitName) }

// Restart 重启（也用于"参数改了要生效"）。
func (m *Manager) Restart(ctx context.Context) error {
	return m.must(ctx, "重启", "restart", UnitName)
}

// Stop 停止。
func (m *Manager) Stop(ctx context.Context) error { return m.must(ctx, "停止", "stop", UnitName) }

// ResetFailed 清掉 failed 计数。
//
// 这一步是必须的：单元一旦被标成 failed，restart 就退化成"只重启不重载"，
// 后续行为会变得难以推理。`down` / `uninstall` 之后都要清一次。
func (m *Manager) ResetFailed(ctx context.Context) error {
	return m.must(ctx, "清除失败状态", "reset-failed", UnitName)
}

// IsActive 报告单元是否在运行。
func (m *Manager) IsActive(ctx context.Context) (bool, error) {
	out, code, err := m.systemctl(ctx, "is-active", UnitName)
	if err != nil {
		return false, fmt.Errorf("执行 systemctl is-active: %w", err)
	}
	// is-active 对未运行的单元返回 3，那是正常反馈而不是错误。
	return code == 0 && strings.TrimSpace(out) == "active", nil
}

// IsEnabled 报告单元是否开机自启。
func (m *Manager) IsEnabled(ctx context.Context) (bool, error) {
	_, code, err := m.systemctl(ctx, "is-enabled", UnitName)
	if err != nil {
		return false, fmt.Errorf("执行 systemctl is-enabled: %w", err)
	}
	return code == 0, nil
}

// must 执行一个"失败就是失败"的 systemctl 动作。
func (m *Manager) must(ctx context.Context, label string, args ...string) error {
	out, code, err := m.systemctl(ctx, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if code != 0 {
		return fmt.Errorf("%s失败（systemctl %s 退出码 %d）: %s",
			label, strings.Join(args, " "), code, strings.TrimSpace(out))
	}
	return nil
}
