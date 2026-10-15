package cfclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Fetcher 下载一个 URL 的内容。可注入（测试用）。
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// fetchTimeout 是下载公钥的上限。
//
// 只有几十 KB，给 30 秒已经很宽 —— 但它必须有个上限：
// 没有总超时的 HTTP 请求在黑洞路由下会一直挂着，而这条命令通常
// 跑在"用户正等着装完"的场景里。
const fetchTimeout = 30 * time.Second

// httpFetcher 是默认的下载器。
func httpFetcher(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 %s 返回 HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (m *Manager) fetch(ctx context.Context, url string) ([]byte, error) {
	f := m.Fetcher
	if f == nil {
		f = httpFetcher
	}
	return f(ctx, url)
}

// ---------------------------------------------------------------------------
// codename：apt 源按发行版代号分发，新版本常常还没有对应目录
// ---------------------------------------------------------------------------

// codenameAliases 把"官方源还没有目录的代号"回退到上一个有目录的代号。
//
// 为什么需要它：Cloudflare 的仓库是按 Ubuntu/Debian 的 codename 建目录的，
// 而新发行版发布后官方往往滞后几个月。上游遇到 Ubuntu 26.04（resolute）时
// 就是这么回退的 —— 不回退的话 `apt-get update` 会直接 404，
// 而报错信息（"仓库没有 Release 文件"）完全看不出是代号的问题。
var codenameAliases = map[string]string{
	"resolute": "noble", // Ubuntu 26.04 → 回退到 24.04
}

// CodenameFrom 从 /etc/os-release 的内容里读出 VERSION_CODENAME 并做回退。
//
// 优先 VERSION_CODENAME，其次 UBUNTU_CODENAME（Debian 的 os-release 里
// 有时只有后者），都没有就报错 —— 宁可明确失败，也不要猜一个代号
// 写进 apt 源里（那会得到一个 404 的源，比不写更难排查）。
func CodenameFrom(osRelease string) (string, error) {
	var version, ubuntu string
	for _, line := range strings.Split(osRelease, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "VERSION_CODENAME="); ok {
			version = strings.Trim(strings.TrimSpace(v), `"'`)
			continue
		}
		if v, ok := strings.CutPrefix(line, "UBUNTU_CODENAME="); ok {
			ubuntu = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	c := version
	if c == "" {
		c = ubuntu
	}
	if c == "" {
		return "", errors.New("从 /etc/os-release 里读不到 VERSION_CODENAME")
	}
	return MapCodename(c), nil
}

// MapCodename 对代号做回退映射（导出是为了单测能直接钉住这张表）。
func MapCodename(c string) string {
	if v, ok := codenameAliases[strings.ToLower(c)]; ok {
		return v
	}
	return c
}

// ---------------------------------------------------------------------------
// apt 源
// ---------------------------------------------------------------------------

// SourceListContent 生成 apt 源文件内容。
//
// `signed-by=` 是必须的：不加它 apt 会因为"仓库没有受信任的签名"而拒绝，
// 而那条报错同样不直白。绑到我们自己的 keyring 文件上，也就不会
// 往全局 trusted.gpg 里塞东西。
func SourceListContent(keyring, codename string) string {
	return fmt.Sprintf("deb [signed-by=%s] %s %s main\n", keyring, RepoURL, codename)
}

// RepoReady 报告"源文件 + 密钥环"是否都已在位。
func (m *Manager) RepoReady() bool {
	return fileExists(m.SourceListPath) && fileExists(m.KeyringPath)
}

// RepoAny 报告 apt 源或密钥环**至少有一个**在位。
//
// 存在的意义是撤销时的判断：只装了半个（例如上一次在写源文件那一步失败）
// 也算"动过手"，不能因为"没装全"就整件事跳过、把半份残留留在 /etc 里。
func (m *Manager) RepoAny() bool {
	return fileExists(m.SourceListPath) || fileExists(m.KeyringPath)
}

// EnsureRepo 准备好 apt 源：公钥 → 密钥环 → 源文件。
//
// 返回**本次真正新建的**文件清单（已存在的不算），调用方把它记进状态，
// 卸载时只删这些 —— 如果这台机器上原本就有官方源（用户手工配过），
// 我们不该把它一起带走。
func (m *Manager) EnsureRepo(ctx context.Context, codename string) ([]string, error) {
	var created []string

	// ① 密钥环。
	if !fileExists(m.KeyringPath) {
		if err := m.ensureGPG(ctx); err != nil {
			return created, err
		}
		key, err := m.fetch(ctx, PubkeyURL)
		if err != nil {
			return created, fmt.Errorf("下载官方公钥: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(m.KeyringPath), 0o755); err != nil {
			return created, err
		}
		tmp, err := os.CreateTemp(filepath.Dir(m.KeyringPath), ".warpgo-pubkey-*")
		if err != nil {
			return created, err
		}
		tmpName := tmp.Name()
		defer func() { _ = os.Remove(tmpName) }()
		if _, err := tmp.Write(key); err != nil {
			tmp.Close()
			return created, err
		}
		if err := tmp.Close(); err != nil {
			return created, err
		}
		// gpg --dearmor：官方发布的是二进制 GPG 密钥，而 apt 的 signed-by
		// 要的是去装甲后的 .gpg。少这一步 apt 会报"无效的密钥"。
		if out, code, err := m.run(ctx, "gpg", "--yes", "--dearmor", "--output", m.KeyringPath, tmpName); err != nil {
			return created, fmt.Errorf("执行 gpg --dearmor: %w", err)
		} else if code != 0 {
			return created, fmt.Errorf("gpg --dearmor 退出码 %d: %s", code, strings.TrimSpace(out))
		}
		created = append(created, m.KeyringPath)
	}

	// ② 源文件。
	if !fileExists(m.SourceListPath) {
		if err := os.MkdirAll(filepath.Dir(m.SourceListPath), 0o755); err != nil {
			return created, err
		}
		if err := os.WriteFile(m.SourceListPath, []byte(SourceListContent(m.KeyringPath, codename)), 0o644); err != nil {
			return created, fmt.Errorf("写入 %s: %w", m.SourceListPath, err)
		}
		created = append(created, m.SourceListPath)
	}
	return created, nil
}

// ensureGPG 确保 gpg 可用（精简安装的 Debian 上可能没有）。
func (m *Manager) ensureGPG(ctx context.Context) error {
	if _, err := exec.LookPath("gpg"); err == nil {
		return nil
	}
	if _, code, err := m.aptGet(ctx, "update"); err != nil {
		return fmt.Errorf("执行 apt-get update: %w", err)
	} else if code != 0 {
		return fmt.Errorf("apt-get update 退出码 %d", code)
	}
	if out, code, err := m.aptGet(ctx, "install", "-y", "--no-install-recommends", "gnupg"); err != nil {
		return fmt.Errorf("安装 gnupg: %w", err)
	} else if code != 0 {
		return fmt.Errorf("安装 gnupg 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// aptGet 调用 apt-get，并强制非交互。
//
// `DEBIAN_FRONTEND=noninteractive` 是必需的：没有它，apt 在某些
// 场景（重启服务、改配置文件）会停下来等人输入，而在脚本里那就是"卡住"。
// 用 `env` 前缀而不是改 Runner 的签名 —— 注入点越少，测试装配越简单。
func (m *Manager) aptGet(ctx context.Context, args ...string) (string, int, error) {
	full := append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get"}, args...)
	return m.run(ctx, "env", full...)
}

// InstallPackage 装上 `cloudflare-warp`。
func (m *Manager) InstallPackage(ctx context.Context) error {
	if out, code, err := m.aptGet(ctx, "update"); err != nil {
		return fmt.Errorf("执行 apt-get update: %w", err)
	} else if code != 0 {
		return fmt.Errorf("apt-get update 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	if out, code, err := m.aptGet(ctx, "install", "-y", "--no-install-recommends", "cloudflare-warp"); err != nil {
		return fmt.Errorf("安装 cloudflare-warp: %w", err)
	} else if code != 0 {
		return fmt.Errorf("安装 cloudflare-warp 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// RemovePackage 卸掉官方包。
//
// 用 `purge` 而不是 `remove`：目标是"撤干净"，而 `purge` 只会多删掉
// **这个包自己声明的**配置文件（例如 /var/lib/cloudflare-warp 下的注册信息），
// 不会碰任何不属于它的东西 —— 与 `uninstall --purge` 的"只删自己认识的"同一条原则。
//
// 先 disconnect + registration delete 是**调用方**的责任（见 cmd 层）——
// 这一层不管这些，它只负责"把包从系统里去掉"。
func (m *Manager) RemovePackage(ctx context.Context) error {
	out, code, err := m.aptGet(ctx, "purge", "-y", "cloudflare-warp")
	if err != nil {
		return fmt.Errorf("卸载 cloudflare-warp: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("apt-get purge cloudflare-warp 退出码 %d: %s", code, strings.TrimSpace(out))
	}
	return nil
}

// RemoveRepo 删掉**我们建的**源文件与密钥环。
//
// 传入的 created 是安装时记录的"我们建了哪些"，只删其中的；
// 本来就在位的那份一律保留 —— 它是用户的配置，不是我们的。
func (m *Manager) RemoveRepo(created []string) (removed []string, warn error) {
	for _, path := range created {
		switch err := os.Remove(path); {
		case err == nil:
			removed = append(removed, path)
		case errors.Is(err, os.ErrNotExist):
		default:
			warn = fmt.Errorf("删除 %s: %w", path, err)
		}
	}
	return removed, warn
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
