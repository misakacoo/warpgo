// Package fetch 是"从网络取一个可执行文件、校验它、再安全地装上"的公共机制。
//
// 用到它的有两处，而两处的需求完全一样：
//   - internal/proxy：装第三方二进制 wireproxy；
//   - internal/update：自我更新（把 warpgo 自己换掉）。
//
// 抽出来而不是各写一份，理由很直接 —— 这里面每一步都有踩坑的记录：
//   - 校验和必须**在下载之前**确定，否则就有"先装上去再说"的诱惑，
//     而那等于静默降级成"不校验"；
//   - 替换之前必须**先执行一次**，否则一个跑不起来的产物（架构不对、
//     下载物其实是 HTML 错误页）会把原来能用的那个覆盖掉，
//     用户就从"能用"变成"什么都没有"；
//   - 归档里的条目名一律只取 Base：归档来自网络、落盘又是 root，
//     一个带 `../../etc/...` 的条目就能覆盖系统文件。
//
// 两份实现意味着两处都可能忘掉其中一条，所以只留一份。
package fetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxDownloadSize 是下载体积上限。
//
// 防御性的：它挡不住有备而来的攻击者，但能挡住"响应被劫持成一个无限流"
// 把磁盘写满这种最常见的翻车方式。
const MaxDownloadSize = 64 << 20

// Probe 跑一次刚下载的东西，确认它真的能执行。
//
// 返回值是它自报的版本（用于日志），错误表示"跑不起来"。
type Probe func(ctx context.Context, path string) (string, error)

// VersionProbe 返回一个"执行 `<path> <args...>` 并取第一行输出"的探针。
//
// 刻意不猜参数：wireproxy 用 `--version`，warpgo 用 `version`，
// 这是调用方才知道的事。
func VersionProbe(args ...string) Probe {
	return func(ctx context.Context, path string) (string, error) {
		out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
		text := strings.TrimSpace(string(out))
		if err != nil {
			return text, fmt.Errorf("执行 %s %s 失败: %w", path, strings.Join(args, " "), err)
		}
		if text == "" {
			return "", fmt.Errorf("%s %s 没有任何输出", path, strings.Join(args, " "))
		}
		return FirstLine(text), nil
	}
}

// DefaultClient 返回带总超时的 HTTP 客户端。
//
// 必须有总超时：curl 与 Go 的默认客户端都可以无限期地等下去，
// 而黑洞路由下那意味着命令永远挂着。
func DefaultClient() *http.Client { return &http.Client{Timeout: 3 * time.Minute} }

// Get 取一个小体积的文本资源（校验和清单 / API 响应）。
//
// 带上 GitHub 的 Accept 头：它对别的站点无害，而少了它在 GitHub 上
// 会拿到一份"给人看"的 HTML。
func Get(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 %s", rawURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// DownloadToTemp 把 url 下载到临时文件，返回路径与实际 sha256。
//
// 边写边算哈希，而不是先读进内存：产物有几 MB，而这是 root 权限的进程。
// dir 留空则用系统临时目录；要"装到某个位置"时建议传目标目录
// （同文件系统才能原子 rename）。
func DownloadToTemp(ctx context.Context, client *http.Client, rawURL, dir string) (path, sum string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("构造下载请求: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("下载 %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("下载 %s 返回 %s", rawURL, resp.Status)
	}

	tmpDir, err := os.MkdirTemp(dir, "warpgo-fetch-*")
	if err != nil {
		return "", "", fmt.Errorf("创建临时目录: %w", err)
	}
	path = filepath.Join(tmpDir, "download")
	f, err := os.Create(path)
	if err != nil {
		return "", "", fmt.Errorf("创建临时文件: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, MaxDownloadSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("写入 %s: %w", path, err)
	}
	if n > MaxDownloadSize {
		_ = os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("下载体积超过上限 %d 字节，已放弃", MaxDownloadSize)
	}
	return path, hex.EncodeToString(h.Sum(nil)), nil
}

// LookupSum 从 checksums.txt 的内容里找出某个文件的 sha256。
//
// 兼容两种常见写法：`<sha>  <name>`（sha256sum 的长格式）
// 与 `<sha> *<name>`（二进制模式），以及带目录前缀的名字。
func LookupSum(content, asset string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if PathBase(name) != asset {
			continue
		}
		if v := NormalizeSHA(fields[0]); v != "" {
			return v
		}
	}
	return ""
}

// NormalizeSHA 把各种写法的校验和收敛成小写十六进制；不合法时返回空串。
func NormalizeSHA(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 {
		return ""
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return s
}

// HashFile 算文件的 sha256。
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读取 %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StageFrom 从归档（tar.gz）或裸二进制解出内容，放到 dst 同目录的临时文件（0755）。
//
// `binaryName` 是归档里那个可执行文件的名字（`wireproxy` / `warpgo`），
// 只用于"优先取哪个条目"的判断 —— 取不到时会退化成"归档里唯一的常规文件"。
//
// 分成"staged → 验证 → rename"三步而不是一步到位，是为了让"替换"成为唯一的、
// 可失败的最后一步（见 InstallChecked 的说明）。
func StageFrom(src, dst, binaryName string) (string, error) {
	magic, err := headBytes(src, 2)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".warpgo-install.tmp*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()

	if magic[0] == 0x1f && magic[1] == 0x8b {
		// 两遍扫描：第一遍只找"该取哪个条目"，第二遍才写出。
		//
		// tar 是流式的、无法回退，用一遍扫描来做就要么把不确定的候选先写出去
		// （写错了就得回滚），要么在内存里缓存整个归档。两遍扫描最省事也最直白，
		// 而归档只有几 MB，重开一次文件的代价可以忽略。
		entry, err := tarEntryName(src, binaryName)
		if err != nil {
			tmp.Close()
			_ = os.Remove(tmpName)
			return "", err
		}
		if err := extractEntry(src, entry, tmp); err != nil {
			tmp.Close()
			_ = os.Remove(tmpName)
			return "", err
		}
	} else {
		// 裸二进制：`--url` 指向单文件发布物时走这条路。
		if err := copyFileLimited(src, tmp); err != nil {
			tmp.Close()
			_ = os.Remove(tmpName)
			return "", err
		}
	}

	// 先设权限、刷盘，再交出去。
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("设置执行权限: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("刷盘: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}

// Install 解包并安装（**不做**执行验证）。保留它是为了单测能只测"文件层"的行为。
func Install(src, dst, binaryName string) error {
	staged, err := StageFrom(src, dst, binaryName)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staged) }()
	return os.Rename(staged, dst)
}

// InstallChecked 把 src 解出来装到 dst，**替换之前先用 probe 跑一次**。
//
// 为什么必须先跑：dst 位置上通常已经躺着一个能用的二进制，而一个装不上去的产物
// 一旦覆盖上去，用户就从"能用"变成"什么都没有"了 —— 而报错只会说一句
// "刚装的那个跑不起来"。
//
// 校验发生在临时文件上、rename 之前，所以失败时磁盘上的旧文件一个字节都没动。
func InstallChecked(ctx context.Context, src, dst, asset, binaryName string, probe Probe) (string, error) {
	staged, err := StageFrom(src, dst, binaryName)
	if err != nil {
		return "", err
	}
	// rename 成功后这个路径已不存在，Remove 的错误可以忽略。
	defer func() { _ = os.Remove(staged) }()

	ver, err := probe(ctx, staged)
	if err != nil {
		return "", fmt.Errorf("下载到的东西不是可执行的 %s：%w", asset, err)
	}
	if err := os.Rename(staged, dst); err != nil {
		return "", fmt.Errorf("安装到 %s: %w", dst, err)
	}
	return ver, nil
}

// PathBase 是 filepath.Base 的 URL 友好版本（URL 里是 `/`）。
func PathBase(s string) string {
	if u, err := url.Parse(s); err == nil && u.Path != "" {
		s = u.Path
	}
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// FirstLine 取多行输出的第一行。
func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// ---------------------------------------------------------------------------
// 内部：归档与文件
// ---------------------------------------------------------------------------

// headBytes 读取文件开头的 n 个字节（用于判断是不是 gzip）。
func headBytes(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("读取 %s 的头部: %w", path, err)
	}
	return buf, nil
}

// tarEntryName 决定从归档里取哪个条目。
//
// 优先取基名等于 binaryName 的那个；没有的话退而取"归档里唯一的常规文件"
// （发布物偶尔会把名字改掉）。都找不到就报错 —— 宁可失败，也不装一个
// 来路不明的文件到 /usr/bin。
//
// 只认常规文件、并一律用 PathBase 去掉目录：归档来自网络，而这里是以 root
// 身份落盘，一个名字里带 `../../etc/...` 的条目就能覆盖系统文件。
func tarEntryName(path, binaryName string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("解压 gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var only string
	count := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取归档: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		count++
		if PathBase(hdr.Name) == binaryName {
			return hdr.Name, nil
		}
		if count == 1 {
			only = hdr.Name
		} else {
			only = ""
		}
	}
	if only != "" {
		return only, nil
	}
	return "", fmt.Errorf("归档里没有可执行文件（期望一个名为 %s 的常规文件）", binaryName)
}

// extractEntry 把归档里指定的条目写到 w。
func extractEntry(path, entry string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开 %s: %w", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("解压 gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读取归档: %w", err)
		}
		if hdr.Name != entry {
			continue
		}
		if _, err := io.Copy(w, io.LimitReader(tr, MaxDownloadSize)); err != nil {
			return fmt.Errorf("写出二进制: %w", err)
		}
		return nil
	}
	return fmt.Errorf("归档里找不到条目 %s", entry)
}

// copyFileLimited 拷贝普通文件（带上限）。
func copyFileLimited(src string, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开 %s: %w", src, err)
	}
	defer f.Close()
	if _, err := io.Copy(w, io.LimitReader(f, MaxDownloadSize)); err != nil {
		return fmt.Errorf("写出二进制: %w", err)
	}
	return nil
}
