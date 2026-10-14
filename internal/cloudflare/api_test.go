package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sampleRegisterResponse 是按真实响应形状构造的夹具（键名与嵌套层级都照抄）。
//
// 里面刻意混进两个"坑"：
//   - endpoint.v4 的端口是 0（真实 API 就是这样）
//   - created/updated 是字符串、tos 是字符串，而另有测试专门喂 {}
//
// 用它当夹具的好处是：只要我的结构体字段名写错，解析出来的地址/对端就会是空的，
// 断言立刻失败 —— 等于把"契约是否对得上"变成了自动化检查。
const sampleRegisterResponse = `{
  "id": "d1e2f3a4-0000-0000-0000-000000000001",
  "type": "PC",
  "model": "PC",
  "name": "",
  "key": "PUBKEY",
  "account": {
    "id": "acc-0001",
    "account_type": "free",
    "role": "",
    "license": "",
    "warp_plus": false,
    "premium_data": 0,
    "quota": 0,
    "usage": 0,
    "referral_count": 0,
    "referral_renewal_countdown": 0,
    "created": "2026-10-14T11:59:00.000Z",
    "updated": "2026-10-14T11:59:00.000Z"
  },
  "config": {
    "client_id": "y8eh",
    "peers": [
      {
        "public_key": "PEERPUBLICKEY000000000000000000000000000000=",
        "endpoint": {
          "v4": "162.159.192.1:0",
          "v6": "[2606:4700:d0::a29f:c001]:0",
          "host": "engage.cloudflareclient.com:2408"
        }
      }
    ],
    "interface": {
      "addresses": {
        "v4": "172.16.0.2",
        "v6": "2606:4700:110:8a1b:c1e2:9f3d:1a2b:3c4d"
      }
    }
  },
  "token": "tok-abcdef",
  "warp_enabled": true,
  "install_id": "AAAAAAAAAAAAAAAAAAAAAA",
  "fcm_token": "AAAAAAAAAAAAAAAAAAAAAA:APA91bAAAA",
  "locale": "zh_CN",
  "enabled": true,
  "created": "2026-10-14T12:00:00.000Z",
  "updated": "2026-10-14T12:00:00.000Z",
  "tos": "2026-10-14T12:00:00.000Z"
}`

// zeros 提供确定性的"随机源"：全零字节。
//
// crypto/rand.Int 从 reader 读取后做拒绝采样，全零字节永远落在合法区间，
// 于是每个位置都映射到字母表第 0 个字符 'A'。这让 install_id / fcm_token
// 变成可预测的字符串，从而能**精确断言长度与格式**。
func zeros(n int) io.Reader { return bytes.NewReader(make([]byte, n)) }

type recorded struct {
	method  string
	path    string
	ua      string
	version string
	ctype   string
	auth    string
	body    []byte
}

func newRecorder(t *testing.T, respond func(w http.ResponseWriter, call int)) (*Client, *[]recorded) {
	t.Helper()
	var calls []recorded
	var n int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, recorded{
			method:  r.Method,
			path:    r.URL.Path,
			ua:      r.Header.Get("User-Agent"),
			version: r.Header.Get("CF-Client-Version"),
			ctype:   r.Header.Get("Content-Type"),
			auth:    r.Header.Get("Authorization"),
			body:    body,
		})
		i := int(atomic.AddInt64(&n, 1))
		respond(w, i)
	}))
	t.Cleanup(srv.Close)

	c := New(Options{
		BaseURL: srv.URL,
		Now:     func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) },
		Rand:    zeros(8192),
		Sleep:   func(time.Duration) {}, // 重试不真等
	})
	return c, &calls
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// 注册请求的**每一个字段与请求头**都要对上服务端的校验，错一个就可能被拒。
// 这条测试就是那份校验清单的可执行版本。
func TestRegisterRequestShape(t *testing.T) {
	c, calls := newRecorder(t, func(w http.ResponseWriter, _ int) {
		writeJSON(w, 200, sampleRegisterResponse)
	})

	dev, err := c.Register(context.Background(), "PUBKEY")
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("期望 1 次请求，实得 %d", len(*calls))
	}
	got := (*calls)[0]

	if got.method != http.MethodPost {
		t.Errorf("method = %s，期望 POST", got.method)
	}
	if got.path != "/v0a2158/reg" {
		t.Errorf("path = %s，期望 /v0a2158/reg", got.path)
	}
	if got.ua != "okhttp/3.12.1" {
		t.Errorf("User-Agent = %q", got.ua)
	}
	if got.version != "a-6.10-2158" {
		t.Errorf("CF-Client-Version = %q", got.version)
	}
	if got.ctype != "application/json" {
		t.Errorf("Content-Type = %q", got.ctype)
	}
	// 注册这一步**不能**带 Authorization：此时还没有 token。
	if got.auth != "" {
		t.Errorf("注册请求不该带 Authorization，实得 %q", got.auth)
	}

	var req RegisterRequest
	if err := json.Unmarshal(got.body, &req); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v", err)
	}
	if req.Key != "PUBKEY" {
		t.Errorf("key = %q", req.Key)
	}
	if len(req.InstallID) != 22 {
		t.Errorf("install_id 长度应为 22，实得 %d（%q）", len(req.InstallID), req.InstallID)
	}
	if req.SerialNumber != req.InstallID {
		t.Errorf("serial_number 应与 install_id 相同，实得 %q vs %q", req.SerialNumber, req.InstallID)
	}
	if !strings.HasPrefix(req.FCMToken, req.InstallID+":APA91b") {
		t.Errorf("fcm_token 前缀不对: %q", req.FCMToken)
	}
	if want := 22 + len(":APA91b") + 134; len(req.FCMToken) != want {
		t.Errorf("fcm_token 长度应为 %d，实得 %d", want, len(req.FCMToken))
	}
	if req.TOS != "2026-10-14T12:00:00.000Z" {
		t.Errorf("tos = %q，期望 ISO8601 带毫秒与 Z", req.TOS)
	}
	if req.Model != "PC" {
		t.Errorf("model = %q", req.Model)
	}
	if req.Locale != "zh_CN" {
		t.Errorf("locale = %q", req.Locale)
	}

	// 响应解析：字段名要是写错了，这里会拿到空值。
	if dev.ID != "d1e2f3a4-0000-0000-0000-000000000001" {
		t.Errorf("id 解析失败: %q", dev.ID)
	}
	if dev.Token != "tok-abcdef" {
		t.Errorf("token 解析失败: %q", dev.Token)
	}
	if dev.Config.ClientID != "y8eh" {
		t.Errorf("config.client_id 解析失败: %q（键名可能写错了）", dev.Config.ClientID)
	}
	if dev.Config.Interface.Addresses.V4 != "172.16.0.2" {
		t.Errorf("addresses.v4 解析失败: %q", dev.Config.Interface.Addresses.V4)
	}
	if len(dev.Config.Peers) != 1 || dev.Config.Peers[0].PublicKey == "" {
		t.Errorf("peers 解析失败: %+v", dev.Config.Peers)
	}
	if dev.Account.AccountType != "free" {
		t.Errorf("account.account_type 解析失败: %q", dev.Account.AccountType)
	}
	if !dev.WarpEnabled {
		t.Error("warp_enabled 解析失败")
	}
}

func TestEnableWarpRequest(t *testing.T) {
	c, calls := newRecorder(t, func(w http.ResponseWriter, _ int) {
		writeJSON(w, 200, `{"id":"dev-1","token":"tok-1","account":{"account_type":"plus"}}`)
	})

	dev, err := c.EnableWarp(context.Background(), "dev-1", "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0]
	if got.method != http.MethodPatch {
		t.Errorf("method = %s，期望 PATCH", got.method)
	}
	if got.path != "/v0a2158/reg/dev-1" {
		t.Errorf("path = %s", got.path)
	}
	if got.auth != "Bearer tok-1" {
		t.Errorf("Authorization = %q", got.auth)
	}
	if !strings.Contains(string(got.body), `"warp_enabled":true`) {
		t.Errorf("请求体应含 warp_enabled:true，实得 %s", got.body)
	}
	if dev.Account.AccountType != "plus" {
		t.Errorf("未解析到 account_type: %q", dev.Account.AccountType)
	}
}

func TestGetAndDeleteDevice(t *testing.T) {
	c, calls := newRecorder(t, func(w http.ResponseWriter, call int) {
		if call == 1 {
			writeJSON(w, 200, sampleRegisterResponse)
			return
		}
		w.WriteHeader(http.StatusNoContent) // DELETE 返回 204、无响应体
	})

	if _, err := c.GetDevice(context.Background(), "dev-1", "tok-1"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].method != http.MethodGet || (*calls)[0].auth != "Bearer tok-1" {
		t.Errorf("GET 请求不对: %+v", (*calls)[0])
	}

	// 204 无响应体时不能当成解析失败。
	if err := c.DeleteDevice(context.Background(), "dev-1", "tok-1"); err != nil {
		t.Fatalf("DELETE 204 不该报错: %v", err)
	}
	if (*calls)[1].method != http.MethodDelete {
		t.Errorf("method = %s，期望 DELETE", (*calls)[1].method)
	}
}

func TestSetLicenseAndUnbind(t *testing.T) {
	c, calls := newRecorder(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.SetLicense(context.Background(), "dev-1", "tok-1", "LIC-123"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].method != http.MethodPut || !strings.HasSuffix((*calls)[0].path, "/account") {
		t.Errorf("SetLicense 请求不对: %+v", (*calls)[0])
	}
	if !strings.Contains(string((*calls)[0].body), "LIC-123") {
		t.Errorf("请求体应含 license: %s", (*calls)[0].body)
	}

	if err := c.UnbindDevice(context.Background(), "dev-1", "tok-1"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].method != http.MethodPatch || !strings.Contains((*calls)[1].path, "/account/reg/") {
		t.Errorf("Unbind 请求不对: %+v", (*calls)[1])
	}
}

// 限流要退避重试；其他错误必须立刻返回（盲目重试只会把限流窗口拖更长，
// 还会在服务端留下一堆半成品设备）。
func TestRateLimitRetry(t *testing.T) {
	t.Run("1015 会重试并最终成功", func(t *testing.T) {
		c, calls := newRecorder(t, func(w http.ResponseWriter, call int) {
			if call == 1 {
				writeJSON(w, 429, `{"success":false,"errors":[{"code":1015,"message":"You are being rate limited."}]}`)
				return
			}
			writeJSON(w, 200, sampleRegisterResponse)
		})
		c.retryLimit = 3

		if _, err := c.Register(context.Background(), "PUBKEY"); err != nil {
			t.Fatalf("重试后应成功: %v", err)
		}
		if len(*calls) != 2 {
			t.Fatalf("期望 2 次请求，实得 %d", len(*calls))
		}
	})

	t.Run("纯文本 error code 1015 也能识别", func(t *testing.T) {
		c, calls := newRecorder(t, func(w http.ResponseWriter, call int) {
			if call == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, "error code: 1015")
				return
			}
			writeJSON(w, 200, sampleRegisterResponse)
		})
		c.retryLimit = 2

		if _, err := c.Register(context.Background(), "PUBKEY"); err != nil {
			t.Fatalf("重试后应成功: %v", err)
		}
		if len(*calls) != 2 {
			t.Fatalf("期望 2 次请求，实得 %d", len(*calls))
		}
	})

	t.Run("重试耗尽后报错", func(t *testing.T) {
		c, calls := newRecorder(t, func(w http.ResponseWriter, _ int) {
			writeJSON(w, 429, `{"errors":[{"code":1015,"message":"rate limited"}]}`)
		})
		c.retryLimit = 1

		_, err := c.Register(context.Background(), "PUBKEY")
		if err == nil {
			t.Fatal("应当报错")
		}
		if len(*calls) != 2 {
			t.Fatalf("retryLimit=1 应共请求 2 次，实得 %d", len(*calls))
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.IsRateLimited() {
			t.Fatalf("错误里应能取到限流信息: %v", err)
		}
	})

	t.Run("非限流错误不重试", func(t *testing.T) {
		c, calls := newRecorder(t, func(w http.ResponseWriter, _ int) {
			writeJSON(w, 400, `{"success":false,"errors":[{"code":1001,"message":"bad request"}]}`)
		})
		c.retryLimit = 3

		_, err := c.Register(context.Background(), "PUBKEY")
		if err == nil {
			t.Fatal("应当报错")
		}
		if len(*calls) != 1 {
			t.Fatalf("非限流错误只应请求 1 次，实得 %d", len(*calls))
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("应返回 APIError: %v", err)
		}
		if apiErr.Code != 1001 || apiErr.StatusCode != 400 {
			t.Errorf("错误码解析不对: %+v", apiErr)
		}
	})
}

func TestRegisterRejectsEmptyKey(t *testing.T) {
	c, calls := newRecorder(t, func(w http.ResponseWriter, _ int) {
		t.Error("空公钥不该发出请求")
	})
	if _, err := c.Register(context.Background(), "  "); err == nil {
		t.Fatal("空公钥应报错")
	}
	if len(*calls) != 0 {
		t.Fatalf("不该发出请求，实得 %d 次", len(*calls))
	}
}

// 一个无关紧要的时间字段不能把整次解析搞崩 —— 这是 Time 类型存在的理由。
func TestDeviceTimeTolerance(t *testing.T) {
	raw := `{
	  "id":"x","token":"t",
	  "created": {},
	  "updated": null,
	  "tos": "",
	  "config":{"peers":[{"public_key":"p","endpoint":{"host":"h:1"}}]}
	}`
	var d Device
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("created 为 {} 时不该解析失败: %v", err)
	}
	if d.ID != "x" || d.Token != "t" {
		t.Fatalf("其他字段应正常解析: %+v", d)
	}
	if !d.Created.IsZero() {
		t.Errorf("空对象应视为零值，实得 %q", d.Created.String())
	}
}

func TestEndpointHostPort(t *testing.T) {
	cases := []struct {
		name string
		ep   Endpoint
		want string
	}{
		{"优先 host", Endpoint{V4: "162.159.192.1:0", V6: "[2606:4700:d0::1]:0", Host: "engage.cloudflareclient.com:2408"}, "engage.cloudflareclient.com:2408"},
		{"host 缺端口时补 2408", Endpoint{Host: "engage.cloudflareclient.com"}, "engage.cloudflareclient.com:2408"},
		{"host 端口为 0 时替换（上游 v4/v6 的端口就是 0）", Endpoint{Host: "engage.cloudflareclient.com:0"}, "engage.cloudflareclient.com:2408"},
		{"host 缺失时用 v6 并保留方括号", Endpoint{V6: "[2606:4700:d0::a29f:c001]:0"}, "[2606:4700:d0::a29f:c001]:2408"},
		{"裸 IPv6 字面量要加方括号", Endpoint{V6: "2606:4700:d0::a29f:c001"}, "[2606:4700:d0::a29f:c001]:2408"},
		{"全空", Endpoint{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ep.HostPort(); got != c.want {
				t.Fatalf("期望 %q，实得 %q", c.want, got)
			}
		})
	}
}

func TestAddressPrefix(t *testing.T) {
	if got := AddressV4Prefix("172.16.0.2"); got != "172.16.0.2/32" {
		t.Errorf("实得 %q", got)
	}
	if got := AddressV6Prefix("2606:4700:110::1"); got != "2606:4700:110::1/128" {
		t.Errorf("实得 %q", got)
	}
	// 已经有掩码时不该再叠一层（否则会出现 /32/32）。
	if got := AddressV4Prefix("172.16.0.2/32"); got != "172.16.0.2/32" {
		t.Errorf("实得 %q", got)
	}
	if got := AddressV4Prefix(""); got != "" {
		t.Errorf("空串应保持空串，实得 %q", got)
	}
}
