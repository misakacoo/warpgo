// Package update 负责"查有没有新版本"与"把自己换掉"。
//
// 它和 internal/proxy 装第三方二进制是同一件事，只是被换掉的是 warpgo 自己 ——
// 下载 / 校验 / 解包 / "先执行再替换"那一套在 internal/fetch 里，这里只写
// 与"发布页"有关的部分：查 tag、推资产名、对校验和。
//
// 一条刻意的边界：**这个包不重启任何服务**。把新二进制放到位是它的事，
// "要不要重启保活单元"是 cmd 层的事（那里才知道当前跑的是不是 systemd 服务）。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/misakacoo/warpgo/internal/fetch"
)

// 发布来源。默认值就是本项目自己的仓库。
const (
	DefaultRepo = "misakacoo/warpgo"

	// DefaultAPIBase 是 GitHub API；DefaultBase 是发布页下载前缀。
	//
	// 两者都可覆盖：单测要能把它们指向本地假发布页，从而在**不联网**的条件下
	// 覆盖"没有对应资产""校验和取不到"这些分支。
	DefaultAPIBase = "https://api.github.com"
	DefaultBase    = "https://github.com"

	// ChecksumFile 是发布页上的校验和清单（`<sha256>  <文件名>` 每行一条）。
	ChecksumFile = "checksums.txt"

	// binaryName 是归档里那个可执行文件的名字。
	binaryName = "warpgo"
)

// Release 是一次发布查询的结果。
type Release struct {
	Tag string
	// Version 是去掉前导 `v` 的版本号（`v0.2.0` → `0.2.0`），仅用于显示与比较。
	Version string
	// Assets 是"资产名 → 下载地址"。
	Assets map[string]string
	// PageURL 是这个发布的网页地址（给人看的）。
	PageURL string
}

// Client 查版本、做自更新。
type Client struct {
	// Repo 形如 "owner/name"；空则用 DefaultRepo。
	Repo string
	// APIBase / Base 覆盖 GitHub API 与下载前缀（镜像或测试用）。
	APIBase, Base string
	// GOOS / GOARCH 留空则用运行时的。
	GOOS, GOARCH string
	// HTTP 可注入；留空则用带总超时的默认客户端。
	HTTP *http.Client
	// Log 是可选的进度输出。
	Log func(string, ...any)
}

func (c *Client) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimSuffix(c.APIBase, "/")
	}
	return DefaultAPIBase
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimSuffix(c.Base, "/")
	}
	return DefaultBase
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return fetch.DefaultClient()
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// Latest 查最新发布。
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/releases/latest", c.apiBase(), c.repo())
	body, err := fetch.Get(ctx, c.client(), apiURL, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("查询最新版本失败（%v）", err)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	tag := strings.TrimSpace(payload.TagName)
	if tag == "" {
		return nil, errors.New("GitHub 没有返回 tag_name（这个仓库可能还没有发布过）")
	}
	rel := &Release{
		Tag:     tag,
		Version: strings.TrimPrefix(tag, "v"),
		Assets:  make(map[string]string, len(payload.Assets)),
		PageURL: payload.HTMLURL,
	}
	for _, a := range payload.Assets {
		if a.Name != "" {
			rel.Assets[a.Name] = a.URL
		}
	}
	return rel, nil
}

// AssetName 返回某个发布在本机平台上对应的资产名。
//
// 命名是 `warpgo_<tag>_<goos>_<goarch>.tar.gz` —— **tag 原样带 `v`**。
// 刻意不做"去掉 v"这种美化：资产名要由 `build.sh release` 生成、再被这里
// 反推回来，中间任何一次转换都是一个可能对不上的地方。
// 只提供 linux/amd64 与 linux/arm64（与官方客户端的支持面一致）。
func AssetName(tag, goos, goarch string) string {
	if goos != "linux" {
		return ""
	}
	switch goarch {
	case "amd64", "arm64":
		return fmt.Sprintf("warpgo_%s_%s_%s.tar.gz", tag, goos, goarch)
	}
	return ""
}

// CompareVersions 比较两个版本号（`v` 前缀可有可无）。
//
// 返回 -1 / 0 / 1；任一边解析不了时 ok 为 false。
// 用 x/mod/semver 而不是自己写：`1.10.0 > 1.9.0` 这类比较不能按字符串来，
// 而预发布版本的排序规则（`1.0.0-rc1 < 1.0.0`）更容易写错。
func CompareVersions(a, b string) (int, bool) {
	na, nb := normalizeVersion(a), normalizeVersion(b)
	if na == "" || nb == "" {
		return 0, false
	}
	return semver.Compare(na, nb), true
}

// normalizeVersion 给版本号补上 `v` 前缀；不是合法语义化版本时返回空串。
//
// 为什么必须补：`semver` 包只认带 `v` 的形式，而构建时注入的
// `version.Version` 是 `0.1.0`（不带 v），发布 tag 是 `v0.1.0`（带 v）。
func normalizeVersion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "v") {
		s = "v" + s
	}
	if !semver.IsValid(s) {
		return ""
	}
	return s
}

// CheckResult 是 `version --check` 的结论。
type CheckResult struct {
	Current string
	Latest  string
	// Newer 表示 Latest 比 Current 新。
	Newer bool
	// Comparable 为假表示两边没法比（例如当前是 `dev` 构建）——
	// 那时 Newer 恒为 false，调用方必须把这件事说清楚，
	// 而不是让人误以为"已经是最新"。
	Comparable bool
	PageURL    string
	Asset      string
}

// Check 查一次是否有新版本。
func (c *Client) Check(ctx context.Context, current string) (*CheckResult, error) {
	rel, err := c.Latest(ctx)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{
		Current: current,
		Latest:  rel.Version,
		PageURL: rel.PageURL,
		Asset:   AssetName(rel.Tag, c.goos(), c.goarch()),
	}
	if cmp, ok := CompareVersions(rel.Tag, current); ok {
		res.Comparable = true
		res.Newer = cmp > 0
	}
	return res, nil
}

// UpdateOptions 是自更新的可调项。
type UpdateOptions struct {
	// Tag 指定要装的版本（`v0.2.0`）；留空则装最新。
	Tag string
	// Force 允许"重装同一个版本"或"降级"。
	Force bool
	// Current 是当前版本（用于判断要不要拦）。
	Current string
}

// Result 描述一次自更新做了什么。
type Result struct {
	From    string
	To      string
	Path    string
	Asset   string
	SHA256  string
	Changed bool
}

// Update 把 dst 换成新版本。
//
// 顺序与 proxy 装 wireproxy 完全一致（都走 fetch）：**先把校验和定下来，
// 再下载，再在临时文件上跑一次确认它能执行，最后才 rename**。
// 所以任何一步失败时，磁盘上那个正在用的 warpgo 一个字节都没动 ——
// 这是自我更新最要紧的性质：把工具自己更新坏了，用户就没有工具可用了。
func (c *Client) Update(ctx context.Context, dst string, opt UpdateOptions) (*Result, error) {
	var rel *Release
	var err error
	if opt.Tag != "" {
		rel, err = c.releaseByTag(ctx, opt.Tag)
	} else {
		rel, err = c.Latest(ctx)
	}
	if err != nil {
		return nil, err
	}

	// 只在"确实更新"时才动手。重装同一个版本、或者往回装旧版本，
	// 都必须是显式的（--force）—— 否则一次手滑就把机器上的版本降回去了。
	if opt.Tag == "" && !opt.Force {
		if cmp, ok := CompareVersions(rel.Tag, opt.Current); ok && cmp <= 0 {
			return &Result{From: opt.Current, To: rel.Version, Changed: false}, nil
		}
	}

	asset := AssetName(rel.Tag, c.goos(), c.goarch())
	if asset == "" {
		return nil, fmt.Errorf("没有 %s/%s 对应的发布产物（只提供 linux/amd64 与 linux/arm64）",
			c.goos(), c.goarch())
	}
	dlURL, ok := rel.Assets[asset]
	if !ok {
		return nil, fmt.Errorf("发布 %s 里没有资产 %s（现有: %s）",
			rel.Tag, asset, strings.Join(sortedKeys(rel.Assets), ", "))
	}

	// 校验和**必须在下载之前**确定 —— 顺序反了就有"清单取不到时先装上去再说"
	// 的诱惑，而那等于静默降级成不校验。
	sumURL := fmt.Sprintf("%s/%s/releases/download/%s/%s", c.base(), c.repo(), rel.Tag, ChecksumFile)
	c.logf("正在获取校验和清单 %s", sumURL)
	body, err := fetch.Get(ctx, c.client(), sumURL, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("拿不到校验和清单（%v），已放弃更新 —— 宁可不动，也不用一份没核对过的产物", err)
	}
	want := fetch.LookupSum(string(body), asset)
	if want == "" {
		return nil, fmt.Errorf("校验和清单里没有 %s，已放弃更新", asset)
	}

	c.logf("正在下载 %s", dlURL)
	tmp, sum, err := fetch.DownloadToTemp(ctx, c.client(), dlURL, "")
	if err != nil {
		return nil, err
	}
	defer func() {
		if rmErr := os.Remove(tmp); rmErr != nil {
			c.logf("清理临时文件失败: %v", rmErr)
		}
	}()

	if !strings.EqualFold(sum, want) {
		return nil, fmt.Errorf("校验和不符，已放弃更新：\n  期望 %s\n  实得 %s\n下载源: %s", want, sum, dlURL)
	}
	c.logf("校验和通过: %s", sum)

	ver, err := fetch.InstallChecked(ctx, tmp, dst, asset, binaryName, fetch.VersionProbe("version"))
	if err != nil {
		return nil, err
	}
	c.logf("新版本自报: %s", ver)

	return &Result{
		From:    opt.Current,
		To:      rel.Version,
		Path:    dst,
		Asset:   asset,
		SHA256:  sum,
		Changed: true,
	}, nil
}

// releaseByTag 查某个具体 tag 的发布（`--version v0.2.0` 走这条）。
func (c *Client) releaseByTag(ctx context.Context, tag string) (*Release, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.apiBase(), c.repo(), tag)
	body, err := fetch.Get(ctx, c.client(), apiURL, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("查询发布 %s 失败（%v）", tag, err)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	if strings.TrimSpace(payload.TagName) == "" {
		payload.TagName = tag
	}
	rel := &Release{
		Tag:     payload.TagName,
		Version: strings.TrimPrefix(payload.TagName, "v"),
		Assets:  make(map[string]string, len(payload.Assets)),
		PageURL: payload.HTMLURL,
	}
	for _, a := range payload.Assets {
		if a.Name != "" {
			rel.Assets[a.Name] = a.URL
		}
	}
	return rel, nil
}

func (c *Client) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

func (c *Client) goarch() string {
	if c.GOARCH != "" {
		return c.GOARCH
	}
	return runtime.GOARCH
}

// sortedKeys 把资产名排好序（错误信息里要稳定，否则每次输出都不一样）。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
