package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/misakacoo/warpgo/internal/fetch"
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

	// binaryName 是归档里那个可执行文件的名字。
	binaryName = "wireproxy"
)

// Installer 负责把 wireproxy 二进制取回来并装好。
//
// 每条路径都可注入（仓库 / 版本 / URL / 校验和 / 本地文件 / HTTP 客户端），
// 于是单测能覆盖"校验和不符""取不到清单""归档里没有二进制"这些分支，
// 而真机验收只需要跑通一条真实路径。
//
// 下载 / 校验 / 解包 / "先执行再替换"那一套在 internal/fetch 里 ——
// 自我更新（internal/update）要的是同一件事，所以只留一份实现。
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
	return fetch.DefaultClient()
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
		asset = fetch.PathBase(i.URL)
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
		sum, err := fetch.HashFile(i.From)
		if err != nil {
			return Info{}, err
		}
		// 给了 --sha256 就核对（--from 不是"跳过校验"的意思）。
		if want := fetch.NormalizeSHA(i.SHA256); want != "" && !strings.EqualFold(sum, want) {
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
		if _, err := fetch.InstallChecked(ctx, i.From, dst, filepath.Base(i.From),
			binaryName, fetch.VersionProbe("--version")); err != nil {
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
	tmp, sum, err := fetch.DownloadToTemp(ctx, i.client(), dlURL, "")
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

	if _, err := fetch.InstallChecked(ctx, tmp, dst, asset,
		binaryName, fetch.VersionProbe("--version")); err != nil {
		return Info{}, err
	}
	return Info{Version: version, URL: dlURL, Asset: asset, SHA256: sum, Source: "download"}, nil
}

// expectedSHA 找到该资产的期望校验和。
func (i *Installer) expectedSHA(ctx context.Context, version, asset string) (string, error) {
	if v := fetch.NormalizeSHA(i.SHA256); v != "" {
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

	body, err := fetch.Get(ctx, i.client(), sumURL, 1<<20)
	if err != nil {
		return "", fmt.Errorf(
			"拿不到校验和清单（%v）。可用的两条出路：\n"+
				"  · 自己核对发布页后传 --sha256 <sha256>\n"+
				"  · 先把归档下到本机再 --from <文件>", err)
	}
	if v := fetch.LookupSum(string(body), asset); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("校验和清单里没有 %s（清单可能不完整，换一个版本或改用 --sha256/--from）", asset)
}

// latestVersion 查 GitHub API 拿最新 tag。
func latestVersion(ctx context.Context, client *http.Client, apiBase, repo string) (string, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo)
	body, err := fetch.Get(ctx, client, apiURL, 1<<20)
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

// BinaryVersion 跑一次 `wireproxy --version`，确认装上的确实能执行。
//
// "文件存在且可执行"与"它真的能跑"是两件事：架构不对（比如在 arm 机器上装了
// amd64 的包）只有真正执行才会暴露，而那正是最该在 install 阶段发现的问题。
func BinaryVersion(ctx context.Context, bin string) (string, error) {
	return fetch.VersionProbe("--version")(ctx, bin)
}

// ConfigTest 用 wireproxy 自己的 `-n` 校验配置文件。
//
// 这一步是白送的确定性：与其让服务启动失败再去翻 journal，
// 不如在写盘之后立刻问一句"这份配置你自己认不认"。
func ConfigTest(ctx context.Context, bin, conf string) error {
	out, err := exec.CommandContext(ctx, bin, "-n", "-c", conf).CombinedOutput()
	if err != nil {
		return fmt.Errorf("配置自检未通过: %s", fetch.FirstLine(strings.TrimSpace(string(out))))
	}
	return nil
}
