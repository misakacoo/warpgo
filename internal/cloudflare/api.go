// Package cloudflare 是 Cloudflare WARP 非公开 API 的最小客户端。
//
// 设计要点（对应 docs/DESIGN.md §8.1）：
//
//   - **上游是未公开 API，版本号会变。** 所有版本号与 UA 只出现在本文件顶部的
//     常量里；上游一改，只动这里、重编译。这是刻意把"易变的东西"收在一个点上。
//   - **reserved 必须按 base64 原始字节解**（见 reserved.go 的详细说明）。
//   - 只实现「注册 / 查询 / 启用 / 改名 / 改 license / 注销」。**不做 Teams OAuth** ——
//     那是 Zero Trust 组织流程，与"拿到 WARP 出口"无关，已按用户决定砍掉。
//   - 网络层可注入（BaseURL / HTTPClient / Rand / Now / Sleep），
//     所以单测能在无网络、无 root、可控时钟的条件下断言**请求的确切形状**。
package cloudflare

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 上游契约常量。**版本号一变，只改这里。**
const (
	// DefaultBaseURL 是 WARP 非公开 API 的根。
	DefaultBaseURL = "https://api.cloudflareclient.com"

	// APIVersion 是 WireGuard 通道用的 API 版本。
	// （MASQUE 用 v0a4471，本项目不做 MASQUE，仅在设计文档里记录。）
	APIVersion = "v0a2158"

	// ClientVersion 必须与 APIVersion 配套，服务端会校验两者是否匹配。
	ClientVersion = "a-6.10-2158"

	// UserAgent 伪装成官方 Android 客户端所用的 okhttp。
	UserAgent = "okhttp/3.12.1"

	// DefaultModel 与 DefaultLocale 是注册请求里的固定字段。
	DefaultModel  = "PC"
	DefaultLocale = "zh_CN"
)

const (
	// DefaultTimeout 是单次 HTTP 请求的总超时。
	DefaultTimeout = 30 * time.Second
	// maxBodyBytes 限制读取的响应体大小，避免异常响应把内存吃满。
	maxBodyBytes = 1 << 20

	// installIDLen 是 install_id 的长度（上游用 22）。
	installIDLen = 22
	// fcmSuffixLen 是 fcm_token 里固定前缀之后随机部分的长度（上游用 134）。
	fcmSuffixLen = 134
	// fcmPrefix 是 fcm_token 的固定中缀。
	fcmPrefix = ":APA91b"
	// tosLayout 是 tos 字段的时间格式（上游用 `date +"%Y-%m-%dT%H:%M:%S.000Z"`）。
	tosLayout = "2006-01-02T15:04:05.000Z"

	// defaultRetryLimit / defaultRetryDelay 针对限流退避。
	defaultRetryLimit = 3
	defaultRetryDelay = 8 * time.Second
)

// idAlphabet 是 install_id / fcm_token 的取值字符集（上游用 `tr -dc 'A-Za-z0-9'`）。
const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Client 是 WARP API 客户端。零值不可用，请用 New。
type Client struct {
	baseURL    string
	apiVersion string
	userAgent  string
	clientVer  string
	locale     string

	hc         *http.Client
	now        func() time.Time
	rand       io.Reader
	sleep      func(time.Duration)
	retryLimit int
	retryDelay time.Duration
}

// Options 允许调用方替换网络层依赖，测试时全部可注入。
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	Locale     string
	Now        func() time.Time
	Rand       io.Reader
	Sleep      func(time.Duration)
	RetryLimit int
	RetryDelay time.Duration
}

// New 构造客户端；未提供的选项一律取生产的默认值。
func New(opt Options) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(orDefault(opt.BaseURL, DefaultBaseURL)), "/"),
		apiVersion: APIVersion,
		userAgent:  UserAgent,
		clientVer:  ClientVersion,
		locale:     orDefault(strings.TrimSpace(opt.Locale), DefaultLocale),
		hc:         opt.HTTPClient,
		now:        opt.Now,
		rand:       opt.Rand,
		sleep:      opt.Sleep,
		retryLimit: opt.RetryLimit,
		retryDelay: opt.RetryDelay,
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: DefaultTimeout}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.rand == nil {
		c.rand = rand.Reader
	}
	if c.sleep == nil {
		c.sleep = time.Sleep
	}
	if c.retryLimit < 0 {
		c.retryLimit = 0
	}
	if c.retryDelay <= 0 {
		c.retryDelay = defaultRetryDelay
	}
	return c
}

// BaseURL 返回当前使用的 API 根（日志与排查用）。
func (c *Client) BaseURL() string { return c.baseURL }

// APIVersion 返回当前使用的 API 版本（日志与排查用）。
func (c *Client) APIVersion() string { return c.apiVersion }

// Register 用给定的公钥注册一台新设备。
//
// 私钥永远不出本机 —— 这里传进来的只有公钥，服务端只见到公钥。
func (c *Client) Register(ctx context.Context, publicKey string) (*Device, error) {
	if strings.TrimSpace(publicKey) == "" {
		return nil, errors.New("注册需要公钥，但公钥为空")
	}

	installID, err := c.randomString(installIDLen)
	if err != nil {
		return nil, fmt.Errorf("生成 install_id: %w", err)
	}
	suffix, err := c.randomString(fcmSuffixLen)
	if err != nil {
		return nil, fmt.Errorf("生成 fcm_token: %w", err)
	}

	req := RegisterRequest{
		Key:          publicKey,
		InstallID:    installID,
		FCMToken:     installID + fcmPrefix + suffix,
		TOS:          c.now().UTC().Format(tosLayout),
		Model:        DefaultModel,
		SerialNumber: installID,
		Locale:       c.locale,
	}

	var dev Device
	if err := c.do(ctx, http.MethodPost, c.path("/reg"), req, "", &dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// EnableWarp 打开设备上的 WARP（PATCH /reg/{id} {"warp_enabled":true}）。
//
// 这一步不能省：注册出来的设备默认 warp_enabled=false，不 PATCH 的话
// 拿到的 peers/addresses 用不了（上游同样是注册完立刻 PATCH）。
func (c *Client) EnableWarp(ctx context.Context, deviceID, token string) (*Device, error) {
	if err := c.requireDevice(deviceID, token); err != nil {
		return nil, err
	}
	var dev Device
	body := map[string]any{"warp_enabled": true}
	if err := c.do(ctx, http.MethodPatch, c.regPath(deviceID), body, token, &dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// GetDevice 拉取设备当前状态（含账户类型、用量）。
func (c *Client) GetDevice(ctx context.Context, deviceID, token string) (*Device, error) {
	if err := c.requireDevice(deviceID, token); err != nil {
		return nil, err
	}
	var dev Device
	if err := c.do(ctx, http.MethodGet, c.regPath(deviceID), nil, token, &dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// RenameDevice 改设备名（PATCH /reg/{id} {"name":"..."}）。
func (c *Client) RenameDevice(ctx context.Context, deviceID, token, name string) (*Device, error) {
	if err := c.requireDevice(deviceID, token); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("设备名不能为空")
	}
	var dev Device
	body := map[string]any{"name": name}
	if err := c.do(ctx, http.MethodPatch, c.regPath(deviceID), body, token, &dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// SetLicense 绑定 WARP+ 的 license（PUT /reg/{id}/account）。
func (c *Client) SetLicense(ctx context.Context, deviceID, token, license string) error {
	if err := c.requireDevice(deviceID, token); err != nil {
		return err
	}
	if strings.TrimSpace(license) == "" {
		return errors.New("license 不能为空")
	}
	body := map[string]any{"license": license}
	return c.do(ctx, http.MethodPut, c.regPath(deviceID)+"/account", body, token, nil)
}

// UnbindDevice 把设备从账户上解绑（PATCH /reg/{id}/account/reg/{id} {"active":false}）。
func (c *Client) UnbindDevice(ctx context.Context, deviceID, token string) error {
	if err := c.requireDevice(deviceID, token); err != nil {
		return err
	}
	body := map[string]any{"active": false}
	return c.do(ctx, http.MethodPatch, c.regPath(deviceID)+"/account/reg/"+url.PathEscape(deviceID), body, token, nil)
}

// DeleteDevice 注销设备（DELETE /reg/{id}）。成功时服务端返回 204、无响应体。
func (c *Client) DeleteDevice(ctx context.Context, deviceID, token string) error {
	if err := c.requireDevice(deviceID, token); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, c.regPath(deviceID), nil, token, nil)
}

func (c *Client) requireDevice(deviceID, token string) error {
	if strings.TrimSpace(deviceID) == "" {
		return errors.New("缺少设备 ID")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("缺少访问令牌")
	}
	return nil
}

// path 拼出带 API 版本前缀的路径。
func (c *Client) path(suffix string) string {
	return "/" + c.apiVersion + suffix
}

// regPath 拼出 /reg/{id}。
func (c *Client) regPath(deviceID string) string {
	return c.path("/reg/" + url.PathEscape(deviceID))
}

// do 发送请求并解析响应，对限流做退避重试。
//
// 只重试「明确是限流」的错误（HTTP 429 / CF 1015）。其他错误一律立刻返回 ——
// 盲目重试注册请求会在服务端留下一堆半成品设备，也会把限流窗口拖得更长。
func (c *Client) do(ctx context.Context, method, path string, body any, token string, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求体: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.retryLimit; attempt++ {
		if attempt > 0 {
			// 线性退避：8s、16s、24s。上游是固定 sleep 10s，这里略微拉长，
			// 因为 1015 的窗口通常比 10 秒长。
			c.sleep(c.retryDelay * time.Duration(attempt))
		}
		err := c.attempt(ctx, method, path, payload, token, out)
		if err == nil {
			return nil
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.IsRateLimited() {
			return err
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("被限流且重试 %d 次仍失败: %w", c.retryLimit, lastErr)
}

func (c *Client) attempt(ctx context.Context, method, path string, payload []byte, token string, out any) error {
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}
	full := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, full, bodyReader)
	if err != nil {
		return fmt.Errorf("构造请求 %s %s: %w", method, full, err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("CF-Client-Version", c.clientVer)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s %s: %w", method, full, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("读取 %s 的响应: %w", full, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return parseAPIError(resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析响应（前 %d 字节: %s）: %w", len(raw), truncate(string(raw), 200), err)
	}
	return nil
}

// parseAPIError 把错误响应变成结构化的 APIError。
//
// 两种情况都要认：JSON 形式 {"success":false,"errors":[{"code":1015,...}]}
// 和纯文本 "error code: 1015"（上游就是用后者判 1015 的）。
func parseAPIError(status int, raw []byte) *APIError {
	e := &APIError{StatusCode: status, Raw: truncate(string(raw), 400)}

	var body apiErrorBody
	if err := json.Unmarshal(raw, &body); err == nil && len(body.Errors) > 0 {
		e.Code = body.Errors[0].Code
		e.Message = body.Errors[0].Message
		return e
	}

	text := strings.TrimSpace(string(raw))
	if i := strings.Index(text, "error code: "); i >= 0 {
		num := strings.TrimSpace(text[i+len("error code: "):])
		if v, err := parseInt(num); err == nil {
			e.Code = v
		}
	}
	if len(text) <= 200 {
		e.Message = text
	}
	return e
}

func parseInt(s string) (int, error) {
	var v int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// randomString 用 crypto/rand 取 n 个字母数字。
//
// 用 rand.Int 而不是「读一个字节再取模」：取模会让字符集长度不整除 256 时产生偏置，
// 而 install_id / fcm_token 是设备标识，不该有可预测的偏置。
func (c *Client) randomString(n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	max := big.NewInt(int64(len(idAlphabet)))
	buf := make([]byte, n)
	for i := range buf {
		idx, err := rand.Int(c.rand, max)
		if err != nil {
			return "", err
		}
		buf[i] = idAlphabet[idx.Int64()]
	}
	return string(buf), nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
