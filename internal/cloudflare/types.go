package cloudflare

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// RegisterRequest 是 POST /reg 的请求体。
//
// 字段名与取值范围完全照抄官方客户端（附录 B），服务端会校验：
// install_id 是 22 位字母数字，fcm_token 是 "<install_id>:APA91b" + 134 位字母数字，
// tos 是 ISO8601（毫秒 + Z）。少一个字段都可能被拒。
type RegisterRequest struct {
	Key          string `json:"key"`
	InstallID    string `json:"install_id"`
	FCMToken     string `json:"fcm_token"`
	TOS          string `json:"tos"`
	Model        string `json:"model"`
	SerialNumber string `json:"serial_number"`
	Locale       string `json:"locale"`
}

// Device 是 /reg 系列接口返回的设备对象（注册、查询、启用的响应体都是它）。
//
// 注意：CF 对某些字段的返回形态并不稳定 —— created/updated/tos 有时是 ISO8601
// 字符串、有时是 {} 空对象。所以它们用 Time 而不是 time.Time：后者遇到 {} 会
// 让**整个** UnmarshalJSON 失败，一个无关紧要的时间字段就能把注册流程搞崩。
type Device struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Model       string       `json:"model"`
	Name        string       `json:"name"`
	Key         string       `json:"key"`
	Account     AccountInfo  `json:"account"`
	Config      DeviceConfig `json:"config"`
	Token       string       `json:"token"`
	WarpEnabled bool         `json:"warp_enabled"`
	InstallID   string       `json:"install_id"`
	FCMToken    string       `json:"fcm_token"`
	Locale      string       `json:"locale"`
	Enabled     bool         `json:"enabled"`
	Created     Time         `json:"created"`
	Updated     Time         `json:"updated"`
	TOS         Time         `json:"tos"`
}

// AccountInfo 是设备对象里的 account 子对象。
type AccountInfo struct {
	ID                       string `json:"id"`
	AccountType              string `json:"account_type"`
	Role                     string `json:"role"`
	License                  string `json:"license"`
	WarpPlus                 bool   `json:"warp_plus"`
	PremiumData              int64  `json:"premium_data"`
	Quota                    int64  `json:"quota"`
	Usage                    int64  `json:"usage"`
	ReferralCount            int    `json:"referral_count"`
	ReferralRenewalCountdown int    `json:"referral_renewal_countdown"`
	Created                  Time   `json:"created"`
	Updated                  Time   `json:"updated"`
}

// DeviceConfig 是设备对象里的 config 子对象 —— 建隧道需要的东西全在这里。
type DeviceConfig struct {
	ClientID  string        `json:"client_id"`
	Peers     []Peer        `json:"peers"`
	Interface InterfaceConf `json:"interface"`
}

// InterfaceConf 里只有 addresses 是我们需要的（v4/v6 各一条）。
type InterfaceConf struct {
	Addresses struct {
		V4 string `json:"v4"`
		V6 string `json:"v6"`
	} `json:"addresses"`
}

// Peer 是 WARP 出口对端。
//
// Endpoint 里三个字段的端口都很不可靠：实测 v4/v6 的端口是 0，
// 只有 host 带正确端口（engage.cloudflareclient.com:2408），所以一律优先 host。
type Peer struct {
	PublicKey string   `json:"public_key"`
	Endpoint  Endpoint `json:"endpoint"`
}

// Endpoint 是对端的三种表示。
type Endpoint struct {
	V4   string `json:"v4"`
	V6   string `json:"v6"`
	Host string `json:"host"`
}

// DefaultWARPPort 是 WARP WireGuard 入口端口。
//
// 它必须存在的原因：API 返回的 endpoint.v4 / .v6 里的端口是 **0**，
// 只有 endpoint.host 带正确端口。若照抄 v4 会得到一个 ":0" 的端点 ——
// 一个看起来有值、实际永远连不上的配置。
const DefaultWARPPort = "2408"

// HostPort 返回可用的 "host:port" 形式，优先 host，其次 v6，最后 v4。
//
// 三件事都要处理：缺端口、端口为 0、裸 IPv6 字面量需要加方括号。
func (e Endpoint) HostPort() string {
	for _, cand := range []string{e.Host, e.V6, e.V4} {
		if v := literalHostPort(cand); v != "" {
			return v
		}
	}
	return ""
}

// HostPortV4 返回 **IPv4 字面量**端点（补上正确端口）。
//
// 建隧道时要它而不是 HostPort：字面量不经过 DNS，而 host 是域名。
// 本项目的取值链一度全靠 DNS，DNS 一抖就整条链失败 —— 能不查就不查。
func (e Endpoint) HostPortV4() string { return literalHostPort(e.V4) }

// HostPortV6 返回 **IPv6 字面量**端点。
func (e Endpoint) HostPortV6() string { return literalHostPort(e.V6) }

func literalHostPort(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(s); err == nil {
		if port == "" || port == "0" {
			return net.JoinHostPort(host, DefaultWARPPort)
		}
		return s
	}
	// 没有端口（也可能是裸 IPv6 字面量）—— 交给 JoinHostPort 补方括号。
	return net.JoinHostPort(strings.Trim(s, "[]"), DefaultWARPPort)
}

// Time 容忍 CF 返回的多种时间形态：ISO8601 字符串、{} 空对象、null、数字。
//
// 它的存在是为了「一个无关字段不能拖垮整次解析」：用 time.Time 时，
// 服务端把 created 返回成 {} 会让 json.Unmarshal 直接报错，注册流程就断了。
type Time struct {
	Value time.Time
	Raw   string
}

// UnmarshalJSON 实现宽松解析：认得出来就填 Value，认不出来只记 Raw，绝不报错。
func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "" || s == "null" || s == "{}" || s == "[]":
		t.Value, t.Raw = time.Time{}, ""
		return nil
	case strings.HasPrefix(s, "\""):
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			t.Raw = s
			return nil
		}
		t.Raw = str
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if v, err := time.Parse(layout, str); err == nil {
				t.Value = v
				break
			}
		}
		return nil
	default:
		// 数字或对象：留原文备查，不影响其他字段解析。
		t.Raw = s
		return nil
	}
}

// MarshalJSON 原样输出（有解析成功则用 RFC3339）。
func (t Time) MarshalJSON() ([]byte, error) {
	if !t.Value.IsZero() {
		return json.Marshal(t.Value.UTC().Format(time.RFC3339))
	}
	if t.Raw == "" {
		return []byte("null"), nil
	}
	return json.Marshal(t.Raw)
}

// IsZero 报告没有可用时间值。
func (t Time) IsZero() bool { return t.Value.IsZero() && t.Raw == "" }

// String 返回可显示的时间字符串。
func (t Time) String() string {
	if !t.Value.IsZero() {
		return t.Value.UTC().Format(time.RFC3339)
	}
	return t.Raw
}

// AddressV4Prefix / AddressV6Prefix 把响应里的裸地址补成 CIDR。
//
// CF 返回的 addresses.v4 是 "172.16.0.2"（无掩码），而 wg-quick 需要 "172.16.0.2/32"。
// 已经被调用方补过掩码的话原样返回，避免出现 "/32/32"。
func AddressV4Prefix(s string) string { return withHostMask(s, "/32") }

// AddressV6Prefix 同 AddressV4Prefix，掩码是 /128。
func AddressV6Prefix(s string) string { return withHostMask(s, "/128") }

func withHostMask(s, mask string) string {
	v := strings.TrimSpace(s)
	if v == "" || strings.Contains(v, "/") {
		return v
	}
	return v + mask
}

// apiErrorBody 是 CF 的错误响应体，形如 {"success":false,"errors":[{"code":1015,...}]}。
type apiErrorBody struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// APIError 表示服务端返回的非 2xx 响应。
type APIError struct {
	StatusCode int
	Code       int    // CF 业务错误码，如 1015（限流）
	Message    string // 人类可读信息
	Raw        string // 原始响应体（截断后），用于排查未知错误
}

func (e *APIError) Error() string {
	switch {
	case e.Code != 0 && e.Message != "":
		return fmt.Sprintf("Cloudflare API 错误 %d（HTTP %d）", e.Code, e.StatusCode)
	case e.Message != "":
		return fmt.Sprintf("Cloudflare API 返回 HTTP %d: %s", e.StatusCode, e.Message)
	default:
		return fmt.Sprintf("Cloudflare API 返回 HTTP %d", e.StatusCode)
	}
}

// IsRateLimited 报告这是不是限流（需要退避重试）。
//
// 1015 是 CF 的 "You are being rate limited."，上游在注册里专门 sleep 10 秒重试；
// 429 同理。把它们识别出来，才能既重试又不盲目重试。
func (e *APIError) IsRateLimited() bool {
	return e.StatusCode == 429 || e.Code == 1015
}
