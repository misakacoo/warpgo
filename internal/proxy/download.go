package proxy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 发布来源。
const (
	// DefaultRepo 是 wireproxy 的发布仓库。
	//
	// 上游 `pufferffish/wireproxy` **已经迁移**：它的 releases API 会把请求转到
	// windtf/wireproxy，资产里的下载地址也都是后者。这里直接写迁移后的仓库，
	// 少跳一次重定向；同时保留可覆盖 —— 将来再迁一次也不用改代码。
	DefaultRepo = "windtf/wireproxy"

	// DefaultVersion 是默认安装的版本。
	//
	// **刻意钉死，不追 latest**：装上去的是一个以 root 运行的可执行文件，
	// "可复现"比"总是最新"重要得多。要最新版就用 `--version latest`。
	DefaultVersion = "v1.1.3"

	// ChecksumFile 是发布页上的校验和清单（`<sha256>  <文件名>` 每行一条）。
	ChecksumFile = "checksums.txt"

	// DefaultBase 是发布页的下载前缀；DefaultAPIBase 是 GitHub API。
	//
	// 做成可覆盖的：单测要能把它们指向本地假服务器，从而在**不联网**的条件下
	// 覆盖"校验和不符""清单取不到"这些分支；真机上也因此能改用镜像。
	DefaultBase    = "https://github.com"
	DefaultAPIBase = "https://api.github.com"

	// maxDownloadSize 是下载体积上限。
	//
	// 防御性的：它挡不住有备而来的攻击者，但能挡住"响应被劫持成一个无限流"
	// 把磁盘写满这种最常见的翻车方式。
	maxDownloadSize = 64 << 20
)

// Installer 负责把 wireproxy 二进制取回来并装好。
//
// 每条路径都可注入（仓库 / 版本 / URL / 校验和 / 本地文件 / HTTP 客户端），
// 于是单测能覆盖"校验和不符""取不到清单""归档里没有二进制"这些分支，
// 而真机验收只需要跑通一条真实路径。
type Installer struct {
	// Repo 形如 "owner/name"；空则用 DefaultRepo。
	Repo string
	// Version 形如 "v1.1.3"；空则用 DefaultVersion；"latest" 会去查 API。
	Version string
	// URL 直接指定归档地址（覆盖 Repo/Version 推导出来的那个）。
	URL string
	// SHA256 手工指定校验和（与 URL 配套用）。留空时从发布页的清单里找。
	SHA256 string
	// From 本地已有的归档 / 二进制（离线安装）。
	From string
	// GOOS / GOARCH 留空则用运行时的。
	GOOS, GOARCH string
	// Base / APIBase 覆盖发布页与 API 前缀（镜像或测试用）；留空用默认值。
	Base, APIBase string
	// HTTP 可注入；留空则用带超时的默认客户端。
	HTTP *http.Client
	// Log 是可选的进度输出。
	Log func(string, ...any)
}

// Info 描述这次安装的来源。
type Info struct {
	Version string
	URL     string
	Asset   string
	SHA256  string
	Source  string // download / local
}

func (i *Installer) logf(format string, args ...any) {
	if i.Log != nil {
		i.Log(format, args...)
	}
}

func (i *Installer) goos() string {
	if i.GOOS != "" {
		return i.GOOS
	}
	return runtime.GOOS
}

func (i *Installer) goarch() string {
	if i.GOARCH != "" {
		return i.GOARCH
	}
	return runtime.GOARCH
}

func (i *Installer) base() string {
	if i.Base != "" {
		return strings.TrimSuffix(i.Base, "/")
	}
	return DefaultBase
}

func (i *Installer) apiBase() string {
	if i.APIBase != "" {
		return strings.TrimSuffix(i.APIBase, "/")
	}
	return DefaultAPIBase
}

func (i *Installer) client() *http.Client {
	if i.HTTP != nil {
		return i.HTTP
	}
	// 3 分钟：4MB 的资产在慢线路上也够用；再长就不该让命令一直挂着了。
	return &http.Client{Timeout: 3 * time.Minute}
}

// assetName 返回该平台对应的发布资产名。
//
// 资产名是 `wireproxy_<goos>_<goarch>.tar.gz`；对不上就直接报错，
// 让调用方（用户）看到的是"这台机器的架构没有对应的发布产物"，
// 而不是下载一个 404 页面然后花时间解析它。
func assetName(goos, goarch string) string {
	if goos != "linux" {
		return ""
	}
	switch goarch {
	case "amd64", "arm64", "386", "arm", "mips", "mipsle", "riscv64", "s390x":
		return fmt.Sprintf("wireproxy_%s_%s.tar.gz", goos, goarch)
	}
	return ""
}

// resolve 决定"装哪个版本、从哪个地址下、资产叫什么"。
func (i *Installer) resolve(ctx context.Context) (version, dlURL, asset string, err error) {
	if i.URL != "" {
		version = i.Version
		if version == "" {
			version = "自定义地址"
		}
		asset = pathBase(i.URL)
		return version, i.URL, asset, nil
	}

	repo := i.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	version = i.Version
	if version == "" {
		version = DefaultVersion
	}
	if version == "latest" {
		version, err = latestVersion(ctx, i.client(), i.apiBase(), repo)
		if err != nil {
			return "", "", "", err
		}
	}
	asset = assetName(i.goos(), i.goarch())
	if asset == "" {
		return "", "", "", fmt.Errorf("没有 %s/%s 对应的 wireproxy 发布产物（只提供 Linux 的若干架构）",
			i.goos(), i.goarch())
	}
	dlURL = fmt.Sprintf("%s/%s/releases/download/%s/%s", i.base(), repo, version, asset)
	return version, dlURL, asset, nil
}

// Fetch 下载（或读取本地归档）并安装到 dst（0755）。
func (i *Installer) Fetch(ctx context.Context, dst string) (Info, error) {
	if i.From != "" {
		sum, err := hashFile(i.From)
		if err != nil {
			return Info{}, err
		}
		// 给了 --sha256 就核对（--from 不是"跳过校验"的意思）。
		if want := normalizeSHA(i.SHA256); want != "" && !strings.EqualFold(sum, want) {
			return Info{}, fmt.Errorf(
				"本地文件 %s 的校验和对不上：\n  期望 %s\n  实得 %s", i.From, want, sum)
		}
		in := Info{
			Version: i.Version,
			URL:     i.From,
			Asset:   filepath.Base(i.From),
			SHA256:  sum,
			Source:  "local",
		}
		if _, err := installChecked(ctx, i.From, dst, filepath.Base(i.From)); err != nil {
			return Info{}, err
		}
		return in, nil
	}

	version, dlURL, asset, err := i.resolve(ctx)
	if err != nil {
		return Info{}, err
	}

	// 校验和**必须在下载之前**确定。
	//
	// 顺序很要紧：先下后要校验和，就有了"清单取不到时先装上去再说"的诱惑，
	// 而那就是"静默降级成不校验"。这里宁可失败，并把两条出路说清楚
	// （--sha256 手工给、--from 本地装）。
	want, err := i.expectedSHA(ctx, version, asset)
	if err != nil {
		return Info{}, err
	}

	i.logf("正在下载 %s", dlURL)
	tmp, sum, err := downloadToTemp(ctx, i.client(), dlURL)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = os.Remove(tmp) }()

	if !strings.EqualFold(sum, want) {
		return Info{}, fmt.Errorf(
			"校验和不符，已放弃安装：\n  期望 %s\n  实得 %s\n下载源: %s\n"+
				"如果是你自己指定的地址，请用 --sha256 明确给出正确值；否则先不要用这份产物",
			want, sum, dlURL)
	}
	i.logf("校验和通过: %s", sum)

	if _, err := installChecked(ctx, tmp, dst, asset); err != nil {
		return Info{}, err
	}
	return Info{Version: version, URL: dlURL, Asset: asset, SHA256: sum, Source: "download"}, nil
}

// installChecked 把 src 解出来装到 dst，**替换之前先执行一次**。
//
// 为什么要先执行：dst 位置上通常已经躺着一个能用的 wireproxy，而一个装不上去的
// 二进制（架构不对、下载物其实是个 HTML 错误页、用户 --from 给错了文件）一旦
// 覆盖上去，用户就从"能代理"变成"什么都没有"了 —— 而报错信息里只会说一句
// "刚装的那个跑不起来"。先拿它跑一次 `--version`，通了才 rename。
//
// 校验发生在临时文件上、rename 之前，所以失败时磁盘上的旧文件一个字节都没动。
func installChecked(ctx context.Context, src, dst, asset string) (string, error) {
	staged, err := stageFrom(src, dst)
	if err != nil {
		return "", err
	}
	// rename 成功后这个路径已不存在，Remove 的错误可以忽略。
	defer func() { _ = os.Remove(staged) }()

	ver, err := BinaryVersion(ctx, staged)
	if err != nil {
		return "", fmt.Errorf("下载到的东西不是可执行的 wireproxy（%s）：%w", asset, err)
	}
	if err := os.Rename(staged, dst); err != nil {
		return "", fmt.Errorf("安装到 %s: %w", dst, err)
	}
	return ver, nil
}

// installFrom 解包并安装（不做执行验证）。保留它是为了单测能只测"文件层"的行为。
func installFrom(src, dst string) error {
	staged, err := stageFrom(src, dst)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(staged) }()
	return os.Rename(staged, dst)
}

// stageFrom 从归档（tar.gz）或裸二进制解出内容，放到 dst 同目录的临时文件（0755）。
//
// 分成"staged → 验证 → rename"三步而不是一步到位，是为了让"替换"成为唯一的、
// 可失败的最后一步（见 installChecked 的说明）。
func stageFrom(src, dst string) (string, error) {
	magic, err := headBytes(src, 2)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".wireproxy.tmp*")
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
		entry, err := tarEntryName(src)
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

	// 先设权限、刷盘，再交出去 —— 与 installBinary 同一套做法。
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

// expectedSHA 找到该资产的期望校验和。
func (i *Installer) expectedSHA(ctx context.Context, version, asset string) (string, error) {
	if v := normalizeSHA(i.SHA256); v != "" {
		return v, nil
	}
	if i.URL != "" {
		return "", fmt.Errorf(
			"用 --url 指定下载地址时必须同时给出 --sha256（无法从发布页清单里核对）")
	}
	repo := i.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	sumURL := fmt.Sprintf("%s/%s/releases/download/%s/%s", i.base(), repo, version, ChecksumFile)
	i.logf("正在获取校验和清单 %s", sumURL)

	body, err := httpGet(ctx, i.client(), sumURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf(
			"拿不到校验和清单（%v）。可用的两条出路：\n"+
				"  · 自己核对发布页后传 --sha256 <sha256>\n"+
				"  · 先把归档下到本机再 --from <文件>", err)
	}
	if v := lookupSum(string(body), asset); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("校验和清单里没有 %s（清单可能不完整，换一个版本或改用 --sha256/--from）", asset)
}

// lookupSum 从 checksums.txt 内容里找出某个文件的 sha256。
//
// 兼容两种常见写法：`<sha>  <name>`（sha256sum 的长格式）
// 与 `<sha> *<name>`（二进制模式），以及带目录前缀的名字。
func lookupSum(content, asset string) string {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if pathBase(name) != asset {
			continue
		}
		if v := normalizeSHA(fields[0]); v != "" {
			return v
		}
	}
	return ""
}

// normalizeSHA 把各种写法的校验和收敛成小写十六进制；不合法时返回空串。
func normalizeSHA(s string) string {
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

// latestVersion 查 GitHub API 拿最新 tag。
func latestVersion(ctx context.Context, client *http.Client, apiBase, repo string) (string, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo)
	body, err := httpGet(ctx, client, apiURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf("查询最新版本失败（%v）；可以直接指定版本，例如 --version %s", err, DefaultVersion)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	if strings.TrimSpace(payload.TagName) == "" {
		return "", errors.New("GitHub 没有返回 tag_name")
	}
	return payload.TagName, nil
}

// downloadToTemp 把 url 下载到临时文件，返回路径与实际 sha256。
//
// 边写边算哈希，而不是先读进内存：资产有几 MB，而这是 root 权限的进程。
func downloadToTemp(ctx context.Context, client *http.Client, rawURL string) (path, sum string, err error) {
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

	dir, err := os.MkdirTemp("", "warpgo-proxy-*")
	if err != nil {
		return "", "", fmt.Errorf("创建临时目录: %w", err)
	}
	path = filepath.Join(dir, "wireproxy.download")
	f, err := os.Create(path)
	if err != nil {
		return "", "", fmt.Errorf("创建临时文件: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDownloadSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("写入 %s: %w", path, err)
	}
	if n > maxDownloadSize {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("下载体积超过上限 %d 字节，已放弃", maxDownloadSize)
	}
	return path, hex.EncodeToString(h.Sum(nil)), nil
}

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
// 优先取基名为 wireproxy 的那个；没有的话退而取"归档里唯一的常规文件"
// （发布物偶尔会把名字改掉）。都找不到就报错 —— 宁可失败，也不装一个
// 来路不明的文件到 /usr/local/bin。
//
// 只认常规文件、并一律用 filepath.Base 去掉目录：归档来自网络，而这里是以 root
// 身份落盘，一个名字里带 `../../etc/...` 的条目就能覆盖系统文件。
func tarEntryName(path string) (string, error) {
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
		base := pathBase(hdr.Name)
		if base == "wireproxy" {
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
	return "", errors.New("归档里没有可执行文件（期望一个名为 wireproxy 的常规文件）")
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
		if _, err := io.Copy(w, io.LimitReader(tr, maxDownloadSize)); err != nil {
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
	if _, err := io.Copy(w, io.LimitReader(f, maxDownloadSize)); err != nil {
		return fmt.Errorf("写出二进制: %w", err)
	}
	return nil
}

// BinaryVersion 跑一次 `wireproxy --version`，确认装上的确实能执行。
//
// "文件存在且可执行"与"它真的能跑"是两件事：架构不对（比如在 arm 机器上装了
// amd64 的包）只有真正执行才会暴露，而那正是最该在 install 阶段发现的问题。
func BinaryVersion(ctx context.Context, bin string) (string, error) {
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("执行 %s --version 失败: %w", bin, err)
	}
	if text == "" {
		return "", fmt.Errorf("%s --version 没有任何输出", bin)
	}
	return firstLine(text), nil
}

// ConfigTest 用 wireproxy 自己的 `-n` 校验配置文件。
//
// 这一步是白送的确定性：与其让服务启动失败再去翻 journal，
// 不如在写盘之后立刻问一句"这份配置你自己认不认"。
func ConfigTest(ctx context.Context, bin, conf string) error {
	out, err := exec.CommandContext(ctx, bin, "-n", "-c", conf).CombinedOutput()
	if err != nil {
		return fmt.Errorf("配置自检未通过: %s", firstLine(strings.TrimSpace(string(out))))
	}
	return nil
}

// httpGet 取一个小体积的文本资源（清单 / API）。
func httpGet(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
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

// hashFile 算文件的 sha256。
func hashFile(path string) (string, error) {
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

// pathBase 是 filepath.Base 的 URL 友好版本（URL 里是 `/`）。
func pathBase(s string) string {
	if u, err := url.Parse(s); err == nil && u.Path != "" {
		s = u.Path
	}
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// firstLine 取多行输出的第一行。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
