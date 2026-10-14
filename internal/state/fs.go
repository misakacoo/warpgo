package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// 状态目录与文件名。
//
// 之所以把目录做成可覆盖的（WARPGO_ETC），是因为真机验收脚本与非 root 的
// 冒烟测试都需要一个不碰 /etc 的位置 —— 而「测试时必须能隔离现场」是本项目
// 用血换来的纪律（见 docs/DESIGN.md §12）。
const (
	// DefaultDir 是默认状态目录。
	DefaultDir = "/etc/warpgo"
	// EnvDir 可覆盖 DefaultDir（测试与打包用）。
	EnvDir = "WARPGO_ETC"

	// AccountFile 保存注册得到的账户与本地密钥。
	AccountFile = "account.json"
	// RuntimeFile 保存运行态（接口名、模式、后端、MTU…）。
	RuntimeFile = "runtime.json"

	// DirPerm 状态目录权限：只有 root 能进。
	DirPerm fs.FileMode = 0o700
	// FilePerm 状态文件权限：账户文件里有私钥，必须 0600。
	FilePerm fs.FileMode = 0o600
)

// Dir 返回状态目录。
func Dir() string {
	if v := os.Getenv(EnvDir); v != "" {
		return v
	}
	return DefaultDir
}

// AccountPath 返回账户文件路径。
func AccountPath() string { return filepath.Join(Dir(), AccountFile) }

// RuntimePath 返回运行态文件路径。
func RuntimePath() string { return filepath.Join(Dir(), RuntimeFile) }

// EnsureDir 创建状态目录（幂等），并把权限收紧到 0700。
//
// 之所以显式 Chmod 而不是只依赖 MkdirAll：目录已存在时 MkdirAll 不会改权限，
// 而账户文件里放着私钥，一个 0755 的父目录意味着同机其他用户能列目录。
//
// 注意：**只对自己管辖的状态目录调用它**。对 /etc/wireguard 这种系统目录
// 擅自 chmod 会改变别的东西的权限（WriteFileAtomic 内部就只 MkdirAll）。
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("创建状态目录 %s: %w", dir, err)
	}
	if err := os.Chmod(dir, DirPerm); err != nil {
		return fmt.Errorf("收紧状态目录权限 %s: %w", dir, err)
	}
	return nil
}

// WriteFileAtomic 原子地写入文件：同目录临时文件 → fsync → rename。
//
// 为什么不用「直接 WriteFile」：本项目的状态文件会被保活进程与手动命令并发读写，
// 一次断电或一次 kill 就可能留下半截 JSON，而半截 JSON 会让下一次启动直接失败。
// rename 在同一文件系统上是原子的，读者要么看到旧内容、要么看到新内容。
//
// 父目录不存在才创建；**不修改已存在目录的权限** —— 这个函数也会被用来往
// /etc/wireguard 这类系统目录里写文件，擅自 chmod 会波及无关的东西。
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("创建目录 %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	// 失败路径上清掉临时文件；成功路径上 rename 后它已不存在，Remove 返回错误可忽略。
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限: %w", err)
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
		return fmt.Errorf("关闭临时文件: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("重命名到 %s: %w", path, err)
	}
	return nil
}

// ReadFile 读状态文件；文件不存在时返回 (nil, false, nil)，让调用方能区分
// 「还没初始化」与「读失败」——上游把这两种情况混在一起，导致"没安装"被当成
// "读取出错"从而误报。
func ReadFile(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读取 %s: %w", path, err)
	}
	return b, true, nil
}
