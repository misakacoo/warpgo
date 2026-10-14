// Package proxy 管理 WireProxy 方案（菜单 `w` / `y`）。
//
// 它做的事与隧道模式完全不同：不起网络接口、不碰路由，而是在**用户态**跑一个
// 小的 wireguard 客户端（第三方二进制 wireproxy），把一个 socks5 入口挂在回环地址上。
// 于是"哪些流量走 WARP"由使用方（浏览器、curl --proxy、程序里的代理设置）
// 自己决定，而不是由系统路由决定。
//
// 因为要用到第三方二进制，这个包的策略比别处更保守：
//   - 版本默认**钉死**（DefaultVersion），不追 latest —— 装上去的是 root 权限的
//     可执行文件，可复现比"总是最新"重要得多；
//   - 校验和**强制**：从发布页取 checksums.txt 比对，取不到就报错并给出
//     --sha256 / --from 两条出路，不静默降级；
//   - 装完先 `wireproxy -n -c` 做一次配置自检，再启动服务，最后**经 socks5 实测**
//     warp=on 才算成功 —— 与隧道模式同一套"不靠退出码、靠实际连通"的验收习惯。
package proxy

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// 端口范围。默认值沿用生态约定（Cloudflare 官方客户端的 socks5 也是 40000）。
const (
	DefaultPort = 40000
	MinPort     = 1024
	MaxPort     = 65535
)

// DefaultDNS 是隧道内使用的解析器。
//
// 两个族都给：账户可能只有一族的地址，但隧道内多给一个不存在的族只会让
// wireproxy 少一条可用项，不会出错；反过来（v6 账户只给 v4 DNS）会直接不可用。
var DefaultDNS = []string{"1.1.1.1", "2606:4700:4700::1111"}

// Config 是生成 wireproxy 配置所需的全部输入。
type Config struct {
	// Addrs 是接口地址（CIDR）。wireproxy 要求 v4 用 /32、v6 用 /128。
	Addrs []string
	// MTU 是隧道内 MTU。
	MTU int
	// PrivateKey 是本机私钥（base64）—— 这个文件因此必须是 0600。
	PrivateKey string
	// PeerPublicKey / Endpoint 是 WARP 对端。
	PeerPublicKey string
	Endpoint      string
	// DNS 是隧道内使用的解析器；为空时用 DefaultDNS。
	DNS []string
	// Port 是 socks5 的监听端口（只监听 127.0.0.1）。
	Port int
	// Note 是写在文件头的溯源信息（设备 ID 之类）。
	Note string
	// Keepalive 是持久保活秒数；<= 0 时用 DefaultKeepalive。
	Keepalive int
}

// DefaultKeepalive 是持久保活间隔（秒）。与隧道模式保持同一个值。
const DefaultKeepalive = 25

// ValidatePort 校验端口取值。
//
// 下限取 1024 而不是 1：socks5 入口是给本机程序用的，占一个特权端口既没必要，
// 又容易与系统服务撞名。上限到 65535，只要不与已在监听的东西冲突即可。
func ValidatePort(port int) error {
	if port < MinPort || port > MaxPort {
		return fmt.Errorf("socks5 端口 %d 不在可用范围 %d-%d 内", port, MinPort, MaxPort)
	}
	return nil
}

// Validate 检查配置是否完整到可以生成文件。
//
// 刻意在**生成之前**把缺项挑出来：这一层的错误（缺私钥、缺对端）如果漏到
// "服务起不来"，用户看到的是 systemd 的一段报错，远不如一句
// "账户里没有对端公钥，请先 register" 有用。
func (c Config) Validate() error {
	if len(c.Addrs) == 0 {
		return fmt.Errorf("账户里没有任何接口地址，无法生成 WireProxy 配置")
	}
	for _, a := range c.Addrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(a))
		if err != nil {
			return fmt.Errorf("接口地址 %q 不是合法的 CIDR: %w", a, err)
		}
		if !p.Addr().IsValid() {
			return fmt.Errorf("接口地址 %q 解析后为空", a)
		}
	}
	if strings.TrimSpace(c.PrivateKey) == "" {
		return fmt.Errorf("账户里没有私钥，无法生成 WireProxy 配置")
	}
	if strings.TrimSpace(c.PeerPublicKey) == "" {
		return fmt.Errorf("账户里没有对端公钥，无法生成 WireProxy 配置")
	}
	if err := ValidateEndpoint(c.Endpoint); err != nil {
		return err
	}
	if err := ValidatePort(c.Port); err != nil {
		return err
	}
	if c.MTU < 0 {
		return fmt.Errorf("MTU %d 不合法", c.MTU)
	}
	return nil
}

// ValidateEndpoint 校验 host:port 形式的端点。
func ValidateEndpoint(ep string) error {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return fmt.Errorf("账户里没有对端端点，无法生成 WireProxy 配置")
	}
	host, port, err := splitHostPort(ep)
	if err != nil {
		return fmt.Errorf("对端端点 %q 不是 host:port 形式: %w", ep, err)
	}
	if host == "" {
		return fmt.Errorf("对端端点 %q 缺少主机部分", ep)
	}
	if port <= 0 || port > MaxPort {
		return fmt.Errorf("对端端点 %q 的端口不合法", ep)
	}
	return nil
}

// splitHostPort 拆开 host:port，兼容 IPv6 字面量的 [::1]:2408 写法。
func splitHostPort(s string) (host string, port int, err error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", 0, fmt.Errorf("没有冒号")
	}
	host = strings.TrimSpace(s[:i])
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	p, cerr := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if cerr != nil {
		return "", 0, cerr
	}
	return host, p, nil
}

// Render 生成 wireproxy 配置文件的内容。
//
// 内容刻意写死成"一份可读的模板"而不是几行拼接：这个文件会被用户直接打开看
// （里面有私钥，他必须能一眼确认它是什么），也会被 `wireproxy -n -c` 校验。
func Render(c Config) string {
	dns := c.DNS
	if len(dns) == 0 {
		dns = DefaultDNS
	}
	keepalive := c.Keepalive
	if keepalive <= 0 {
		keepalive = DefaultKeepalive
	}

	var b strings.Builder
	b.WriteString("# 本文件由 `warpgo proxy install` 生成 —— 手工改动会在下次 install 时被覆盖。\n")
	b.WriteString("# **里面有私钥**（0600），请不要提交到任何仓库或贴进聊天窗口。\n")
	if c.Note != "" {
		b.WriteString("# 设备: " + c.Note + "\n")
	}
	b.WriteString("# 配置格式见 https://github.com/windtf/wireproxy\n\n")

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(c.Addrs, ", "))
	if c.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", c.MTU)
	}
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)
	fmt.Fprintf(&b, "DNS = %s\n", strings.Join(dns, ", "))
	b.WriteString("\n")

	b.WriteString("[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", c.PeerPublicKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", c.Endpoint)
	// 单对端时 wireproxy 默认就把所有流量交给它，但显式写出来更好：
	// 这个文件的意义之一就是"打开就能看懂流量去哪了"。
	b.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", keepalive)
	b.WriteString("\n")

	// 只监听回环：这个入口是给本机程序用的，绑到 0.0.0.0 等于把一条
	// 通往 WARP 的开放代理挂到公网上（上游的文档里也明确写的是 127.0.0.1）。
	b.WriteString("[Socks5]\n")
	fmt.Fprintf(&b, "BindAddress = 127.0.0.1:%d\n", c.Port)
	b.WriteString("\n")

	b.WriteString("[Resolve]\n")
	b.WriteString("# auto：接口只有 IPv4 地址时等价于 ipv4，否则等价于 ipv6。\n")
	b.WriteString("ResolveStrategy = auto\n")
	return b.String()
}

// ParsePort 从已生成的配置里读回 socks5 端口（`proxy status` 用）。
//
// 为什么不只依赖状态文件：状态文件可能被删（用户清理过现场），而配置文件还在，
// 那时"读不出端口"会让 status 变成一句没用的报错。
func ParsePort(conf string) (int, error) {
	section := ""
	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		if section != "socks5" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "BindAddress") {
			continue
		}
		_, port, err := splitHostPort(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("配置里的 Socks5.BindAddress 无法解析: %w", err)
		}
		return port, nil
	}
	return 0, fmt.Errorf("配置里没有找到 [Socks5] BindAddress")
}
