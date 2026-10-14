// Package dns 负责 /etc/resolv.conf 的"改前备份、用后还原"。
//
// 这个包存在的唯一理由是**别把用户的原文件弄丢**。
//
// 旧版卸载后的真机审计给出的结论很直接：接口、路由、规则、单元都清干净了，
// 唯独 /etc/resolv.conf 还是安装时写进去的 `nameserver 1.1.1.1`，
// 而它的备份 `/etc/resolv.conf.origin` 已被删除 —— 于是**谁也没法知道
// 原来是什么**，用户失去了自行还原的依据。
//
// 所以这里定了两条规则：
//  1. 备份一旦写下就**不再被覆盖**（先到的那个才是"原始"的）；
//  2. 备份在状态目录里（/etc/warpgo），只有"还原成功之后"才会被删。
package dns

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultResolvPath 是系统解析器配置。
const DefaultResolvPath = "/etc/resolv.conf"

// 备份文件名（放在 warpgo 的状态目录里，与账户、运行态同处一地）。
const (
	backupName = "resolv.conf.origin"
	metaName   = "resolv.conf.origin.meta"

	// BackupFileName / MetaFileName 是它们的公开名字 ——
	// `uninstall --purge` 要按名字判断"哪些文件是自己的"，不能靠猜。
	BackupFileName = backupName
	MetaFileName   = metaName
)

// ErrSymlink 表示 /etc/resolv.conf 是符号链接，我们拒绝改写。
//
// 这不是胆小：在装了 systemd-resolved 的机器上，这个文件就是一个指向
// /run/systemd/resolve/stub-resolv.conf 的符号链接。把它替换成普通文件
// 会让 resolved 的配置再也无法生效 —— 而那是用户自己没做过的事，
// 我们不能顺手替他做。
var ErrSymlink = errors.New("resolv.conf 是符号链接，拒绝改写")

// ErrNoBackup 表示没有备份可还原。
var ErrNoBackup = errors.New("没有 resolv.conf 的备份")

// meta 记录备份时的原始属性。
type meta struct {
	BackedUpAt time.Time `json:"backed_up_at"`
	Mode       uint32    `json:"mode"`
	Symlink    bool      `json:"symlink"`
	Target     string    `json:"target,omitempty"`
}

// Manager 管理一个 resolv.conf 的备份与还原。
type Manager struct {
	// ResolvPath 一般就是 /etc/resolv.conf（可覆盖以便测试）。
	ResolvPath string
	// BackupPath 是备份文件；它的同目录下还会写一个 .meta。
	BackupPath string
}

// New 创建 Manager；stateDir 是 warpgo 的状态目录（/etc/warpgo）。
func New(stateDir string) *Manager {
	return &Manager{
		ResolvPath: DefaultResolvPath,
		BackupPath: filepath.Join(stateDir, backupName),
	}
}

// IsSymlink 报告目标文件是否是符号链接。
func (m *Manager) IsSymlink() bool {
	fi, err := os.Lstat(m.ResolvPath)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// Current 读取当前内容（目标不存在时返回空切片）。
func (m *Manager) Current() ([]byte, error) {
	b, err := os.ReadFile(m.ResolvPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", m.ResolvPath, err)
	}
	return b, nil
}

// Servers 解析出当前的 nameserver 列表（用于展示与验收断言）。
func (m *Manager) Servers() ([]string, error) {
	b, err := m.Current()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		if v := strings.TrimSpace(strings.TrimPrefix(line, "nameserver")); v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// HasBackup 报告备份是否存在。
func (m *Manager) HasBackup() bool {
	_, err := os.Stat(m.BackupPath)
	return err == nil
}

// Backup 备份当前内容。
//
// **已存在则不覆盖** —— 这是整个包最重要的一行：第二次调用时磁盘上那个
// 才是原始文件，被覆盖就等于把用户的答案擦掉了。
//
// created 报告本次是否真的新建了备份。
func (m *Manager) Backup() (created bool, err error) {
	if m.HasBackup() {
		return false, nil
	}
	if m.IsSymlink() {
		return false, ErrSymlink
	}
	cur, err := m.Current()
	if err != nil {
		return false, err
	}
	fi, statErr := os.Stat(m.ResolvPath)
	mode := uint32(0o644)
	if statErr == nil {
		mode = uint32(fi.Mode().Perm())
	}

	if err := os.MkdirAll(filepath.Dir(m.BackupPath), 0o700); err != nil {
		return false, fmt.Errorf("创建备份目录: %w", err)
	}
	if err := writeFile(m.BackupPath, cur, 0o600); err != nil {
		return false, err
	}
	mb, _ := json.MarshalIndent(meta{
		BackedUpAt: time.Now().UTC(),
		Mode:       mode,
	}, "", "  ")
	mb = append(mb, '\n')
	if err := writeFile(m.metaPath(), mb, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// Set 把 nameserver 换成 servers（会先备份）。
func (m *Manager) Set(servers []string) error {
	if len(servers) == 0 {
		return errors.New("没有给出任何 DNS 服务器")
	}
	if m.IsSymlink() {
		return ErrSymlink
	}
	if _, err := m.Backup(); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# 由 warpgo 写入；原始内容保存在 " + m.BackupPath + "，\n")
	b.WriteString("# `warpgo down` 或 `warpgo uninstall` 会还原它。\n")
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	perm := os.FileMode(0o644)
	if md, err := m.readMeta(); err == nil && md.Mode != 0 {
		perm = os.FileMode(md.Mode)
	}
	if err := writeFile(m.ResolvPath, []byte(b.String()), perm); err != nil {
		return err
	}
	return nil
}

// Restore 用备份覆盖回去，然后删掉备份与 meta。
//
// 顺序是"先写回、再删备份"：反过来的话，一旦写回失败，备份已经没了，
// 用户就再也没机会还原 —— 那正是旧版造成的结果。
func (m *Manager) Restore() error {
	if !m.HasBackup() {
		return ErrNoBackup
	}
	if m.IsSymlink() {
		// 目标变成了符号链接，说明期间有别的东西接管了这个文件。
		// 这时候覆盖它等于砸掉别人的配置，所以拒绝并把备份留着。
		return ErrSymlink
	}
	orig, err := os.ReadFile(m.BackupPath)
	if err != nil {
		return fmt.Errorf("读取备份 %s: %w", m.BackupPath, err)
	}
	perm := os.FileMode(0o644)
	if md, merr := m.readMeta(); merr == nil && md.Mode != 0 {
		perm = os.FileMode(md.Mode)
	}
	if err := writeFile(m.ResolvPath, orig, perm); err != nil {
		return err
	}
	// 写回成功了才删备份。
	if err := os.Remove(m.BackupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除备份 %s: %w", m.BackupPath, err)
	}
	_ = os.Remove(m.metaPath())
	return nil
}

// RemoveBackup 丢弃备份（不还原）。用于"用户已明确表示不要还原"的场景。
func (m *Manager) RemoveBackup() error {
	if err := os.Remove(m.BackupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(m.metaPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) metaPath() string { return m.BackupPath + ".meta" }

func (m *Manager) readMeta() (meta, error) {
	var md meta
	b, err := os.ReadFile(m.metaPath())
	if err != nil {
		return md, err
	}
	return md, json.Unmarshal(b, &md)
}

// writeFile 写文件（临时文件 + rename，权限显式设置）。
func writeFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("设置权限: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("刷盘: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("重命名到 %s: %w", path, err)
	}
	return nil
}
