// Package health 回答一个问题：链路到底通不通。
//
// 它只做**只读**探测：发 HTTP、读 trace，绝不改接口、路由或服务状态。
// 这一点是刻意的 —— 上游的"判断源"本身会动系统（重启接口、改路由），
// 于是"探测"变成了"故障放大器"。
package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// 探测地址**只用 IP 字面量**，绝不依赖 DNS。
//
// 这是本项目最贵的一课：上游的取值链全靠域名，DNS 一抖"主路径 + 兜底"一起失败，
// 表现成"IPv6 那一行是空白"。字面量完全不经过域名解析，从根上消除这一环。
//
// 另外，字面量必须与协议匹配：`1.1.1.1` 配 `-6` 实测连不上（curl 只能按 ::ffff: 映射），
// 所以 v6 那条用的是 v6 字面量，而不是"换个协议查同一个地址"。
const (
	TraceURLV4 = "https://1.1.1.1/cdn-cgi/trace"
	TraceURLV6 = "https://[2606:4700:4700::1111]/cdn-cgi/trace"
)

// 地址族。
const (
	FamilyV4 = 4
	FamilyV6 = 6
)

// Family 返回地址族的显示名。
func FamilyName(family int) string {
	if family == FamilyV6 {
		return "IPv6"
	}
	return "IPv4"
}

// TraceURL 返回该族对应的探测地址。
func TraceURL(family int) string {
	if family == FamilyV6 {
		return TraceURLV6
	}
	return TraceURLV4
}

// Result 是一次探测的结果。
type Result struct {
	Family   int
	Warp     string // on / plus / off（空表示没读到）
	IP       string
	Location string
	Raw      string
}

// WarpOn 报告这次探测的出口是不是 WARP。
func (r Result) WarpOn() bool { return r.Warp == "on" || r.Warp == "plus" }

// String 输出一行摘要（日志用）。
func (r Result) String() string {
	s := fmt.Sprintf("IPv%d warp=%s", r.Family, r.Warp)
	if r.IP != "" {
		s += " ip=" + r.IP
	}
	if r.Location != "" {
		s += " loc=" + r.Location
	}
	return s
}

// Options 是探测参数。
type Options struct {
	// Interface 非空时把 socket 绑定到该接口（等价于 curl --interface）。
	//
	// 用 SO_BINDTODEVICE 而不是靠路由规则，好处是"经隧道出去"这件事不依赖
	// 任何策略路由是否配对 —— 这也是 M1/M2 的验收能在真实生产机上验证隧道、
	// 又不担心切断 SSH 的原因。
	Interface string

	// Proxy 非空时经该代理访问（形如 socks5h://127.0.0.1:40000）。
	//
	// 它与 Interface 是两种完全不同的"经 WARP 出去"：WireProxy 那条路不起接口、
	// 不碰路由，只在用户态把流量转发到 WARP 对端。探测它是唯一能证明
	// "这条链路真的通"的方式 —— 端口在听只能说明进程活着。
	Proxy string

	// Timeout 是单次探测的总超时。
	Timeout time.Duration

	// Client 可注入（单测用）。
	Client *http.Client
}

// DefaultTimeout 是单次探测的默认超时。
const DefaultTimeout = 8 * time.Second

// 索引 / 错误。
var (
	// ErrEmptyTrace 表示响应里没有任何 trace 字段。
	ErrEmptyTrace = fmt.Errorf("trace 响应为空")
)

// Probe 探测某一族的出口状态。
func Probe(ctx context.Context, family int, opt Options) (Result, error) {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := opt.Client
	if client == nil {
		var err error
		if client, err = newClient(opt.Interface, opt.Proxy, timeout); err != nil {
			return Result{Family: family}, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, TraceURL(family), nil)
	if err != nil {
		return Result{Family: family}, fmt.Errorf("构造探测请求: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Family: family}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return Result{Family: family}, fmt.Errorf("读取 trace 响应: %w", err)
	}
	res := ParseTrace(string(body))
	res.Family = family
	if res.Warp == "" && res.IP == "" {
		return res, ErrEmptyTrace
	}
	return res, nil
}

// ProbeAny 依次尝试给出的地址族，返回第一个成功的。
//
// 为什么是"任一成功"而不是"全部成功"：WARP 的 v6 通路本身抖动更大，
// 要求全好会让一台 v6 偶发抖动的机器被反复重建、出口 IP 一直变。
//
// 失败时会把**每一族**的原因都带出来。只报最后一个会让人误以为
// "只有那一族有问题"，从而在排查时看错方向。
func ProbeAny(ctx context.Context, families []int, opt Options) (Result, error) {
	var failures []string
	for _, fam := range families {
		res, err := Probe(ctx, fam, opt)
		switch {
		case err != nil:
			failures = append(failures, fmt.Sprintf("%s: %v", FamilyName(fam), err))
		case !res.WarpOn():
			failures = append(failures, fmt.Sprintf("%s: 出口不是 WARP（warp=%s）", FamilyName(fam), res.Warp))
		default:
			return res, nil
		}
	}
	if len(failures) == 0 {
		return Result{}, fmt.Errorf("没有任何地址族可供探测")
	}
	return Result{}, fmt.Errorf("每一族都失败 —— %s", strings.Join(failures, "; "))
}

// ParseTrace 解析 cdn-cgi/trace 的响应（key=value 逐行）。
func ParseTrace(body string) Result {
	r := Result{Raw: body}
	for _, line := range strings.Split(body, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "warp":
			r.Warp = v
		case "ip":
			r.IP = v
		case "loc":
			r.Location = v
		}
	}
	return r
}

func newClient(ifName, proxyURL string, timeout time.Duration) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: timeout}
	if ifName != "" {
		dialer.Control = BindToDevice(ifName)
	}
	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: timeout,
		// 探测是一次性的：不复用连接，避免拿到上一次的陈旧状态。
		DisableKeepAlives: true,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("解析代理地址 %q: %w", proxyURL, err)
		}
		// socks5 / socks5h 在 Go 里都走"把域名交给代理解析"这条路
		// （net/http 用 x/net/proxy 的 SOCKS5 拨号器，它发送的是域名）。
		// 写 socks5h 是为了与 curl 的习惯对齐、也让意思更明确。
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Timeout: timeout, Transport: tr}, nil
}

// BindToDevice 返回一个把 socket 绑到指定网络接口的 Control 函数。
func BindToDevice(ifName string) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, ifName)
		}); err != nil {
			return fmt.Errorf("获取 socket 控制权: %w", err)
		}
		if setErr != nil {
			return fmt.Errorf("绑定到接口 %s: %w", ifName, setErr)
		}
		return nil
	}
}
