package cloudflare

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misakacoo/warpgo/internal/state"
)

// ErrNoAccount 表示本机还没有注册过账户。
//
// 单独定义它而不是复用 fs.ErrNotExist：调用方需要区分「还没注册」与
// 「账户文件读坏了」—— 前者要引导用户去注册，后者要报错让人来查。
var ErrNoAccount = errors.New("本机还没有 WARP 账户，请先执行 register")

// Account 是本机持有的账户与建隧道所需的全部材料。
//
// 与设计文档 §7 的差别：密钥与地址用字符串（base64 / CIDR）而不是定长数组与
// netip.Prefix。原因是 account.json 会被用户直接打开看，字符串形态可读、可复制；
// 需要定长类型时由 wgconf 负责解析，依赖方向保持单向（wgconf → cloudflare）。
type Account struct {
	// ID 是设备（注册）ID，后续 PATCH / GET / DELETE 都用它。
	ID string `json:"id"`
	// Token 是访问令牌（Bearer）。
	Token string `json:"token"`

	AccountID   string `json:"account_id,omitempty"`
	AccountType string `json:"account_type,omitempty"`
	License     string `json:"license,omitempty"`

	InstallID string `json:"install_id"`
	FCMToken  string `json:"fcm_token"`

	// PublicKey / PrivateKey 是本地生成的 X25519 密钥（base64）。
	// **私钥只在本文件里出现，永远不发给任何人。**
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`

	// ClientID 是服务端给这台设备的标识；Reserved 由它的前 3 个解码字节得到。
	ClientID string   `json:"client_id,omitempty"`
	Reserved Reserved `json:"reserved"`

	// AddrV4 / AddrV6 是接口地址（CIDR 形式，如 172.16.0.2/32）。
	AddrV4 string `json:"addr_v4,omitempty"`
	AddrV6 string `json:"addr_v6,omitempty"`

	PeerPublicKey string `json:"peer_public_key"`
	// Endpoint 是服务端给的 host:port（可读性好，日志里用它）。
	Endpoint string `json:"endpoint"`
	// EndpointV4 / EndpointV6 是同一对端的**字面量**形式，建隧道时优先用它们 ——
	// API 返回的 v4/v6 端口是 0，这里已补成 2408；用字面量就完全不经过 DNS。
	EndpointV4 string `json:"endpoint_v4,omitempty"`
	EndpointV6 string `json:"endpoint_v6,omitempty"`

	RegisteredAt time.Time `json:"registered_at"`
}

// PreferredEndpoint 返回建隧道时该用的端点。
//
// 顺序：IPv4 字面量 → IPv6 字面量 → host。前两者不查 DNS，是绝大多数情况；
// host 只在服务端没给字面量时才用（那时才不得不解析域名）。
func (a *Account) PreferredEndpoint() string {
	for _, ep := range []string{a.EndpointV4, a.EndpointV6, a.Endpoint} {
		if strings.TrimSpace(ep) != "" {
			return ep
		}
	}
	return ""
}

// FromDevice 把注册/查询接口的响应与本地密钥合成本机账户。
//
// 只接受「已经能建隧道」的响应：缺 ID、缺 token、缺对端都直接报错。
// 宁可注册流程明确失败，也不要存下一份看起来成功、用起来建不起隧道的账户。
func FromDevice(dev *Device, privateKey, publicKey string) (*Account, error) {
	if dev == nil {
		return nil, errors.New("注册响应为空")
	}
	if strings.TrimSpace(dev.ID) == "" {
		return nil, errors.New("注册响应里没有设备 ID（id）")
	}
	if strings.TrimSpace(dev.Token) == "" {
		return nil, errors.New("注册响应里没有访问令牌（token）")
	}
	if len(dev.Config.Peers) == 0 {
		return nil, errors.New("注册响应里没有对端（config.peers）")
	}

	peer := dev.Config.Peers[0]
	if strings.TrimSpace(peer.PublicKey) == "" {
		return nil, errors.New("对端没有公钥（config.peers[0].public_key）")
	}
	endpoint := peer.Endpoint.HostPort()
	if endpoint == "" {
		return nil, errors.New("对端没有可用端点（config.peers[0].endpoint）")
	}

	return &Account{
		ID:            dev.ID,
		Token:         dev.Token,
		AccountID:     dev.Account.ID,
		AccountType:   dev.Account.AccountType,
		License:       dev.Account.License,
		InstallID:     dev.InstallID,
		FCMToken:      dev.FCMToken,
		PublicKey:     publicKey,
		PrivateKey:    privateKey,
		ClientID:      dev.Config.ClientID,
		Reserved:      MustReservedFromClientID(dev.Config.ClientID),
		AddrV4:        AddressV4Prefix(dev.Config.Interface.Addresses.V4),
		AddrV6:        AddressV6Prefix(dev.Config.Interface.Addresses.V6),
		PeerPublicKey: peer.PublicKey,
		Endpoint:      peer.Endpoint.HostPort(),
		EndpointV4:    peer.Endpoint.HostPortV4(),
		EndpointV6:    peer.Endpoint.HostPortV6(),
		RegisteredAt:  time.Now().UTC(),
	}, nil
}

// HasReserved 报告这台设备是否带 reserved。
//
// 只有在需要 reserved 的地区（HK / JP / LA 的某些出口）才非零；
// 它是"要不要用用户态后端"的判据之一（见设计文档 §8.2）。
func (a *Account) HasReserved() bool { return !a.Reserved.IsZero() }

// Addresses 返回接口地址列表，顺序固定为 v4 → v6（输出稳定才好做快照测试）。
func (a *Account) Addresses() []string {
	var out []string
	if a.AddrV4 != "" {
		out = append(out, a.AddrV4)
	}
	if a.AddrV6 != "" {
		out = append(out, a.AddrV6)
	}
	return out
}

// HasV4 / HasV6 报告账户是否具备该地址族。
func (a *Account) HasV4() bool { return a.AddrV4 != "" }
func (a *Account) HasV6() bool { return a.AddrV6 != "" }

// Save 把账户写进 path（0600、原子写）。
func (a *Account) Save(path string) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("编码账户: %w", err)
	}
	data = append(data, '\n')
	if err := state.WriteFileAtomic(path, data, state.FilePerm); err != nil {
		return fmt.Errorf("保存账户到 %s: %w", path, err)
	}
	return nil
}

// LoadAccount 读取账户文件。
func LoadAccount(path string) (*Account, error) {
	raw, found, err := state.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNoAccount
	}
	var a Account
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("解析账户文件 %s: %w", path, err)
	}
	return &a, nil
}

// RedactedAccount 是可安全打印/上报的账户视图：不含私钥、令牌、fcm_token。
//
// 存在的意义是「让打印这件事默认安全」—— 只要 `account` 命令输出的是这个结构，
// 就不会有人不小心把私钥贴进工单或聊天窗口。
type RedactedAccount struct {
	ID            string    `json:"id"`
	AccountID     string    `json:"account_id,omitempty"`
	AccountType   string    `json:"account_type,omitempty"`
	License       string    `json:"license,omitempty"`
	PublicKey     string    `json:"public_key"`
	ClientID      string    `json:"client_id,omitempty"`
	Reserved      Reserved  `json:"reserved"`
	AddrV4        string    `json:"addr_v4,omitempty"`
	AddrV6        string    `json:"addr_v6,omitempty"`
	PeerPublicKey string    `json:"peer_public_key"`
	Endpoint      string    `json:"endpoint"`
	EndpointV4    string    `json:"endpoint_v4,omitempty"`
	EndpointV6    string    `json:"endpoint_v6,omitempty"`
	RegisteredAt  time.Time `json:"registered_at"`
	HasToken      bool      `json:"has_token"`
	HasPrivateKey bool      `json:"has_private_key"`
}

// Redacted 返回脱敏视图。
func (a *Account) Redacted() RedactedAccount {
	return RedactedAccount{
		ID:            a.ID,
		AccountID:     a.AccountID,
		AccountType:   a.AccountType,
		License:       a.License,
		PublicKey:     a.PublicKey,
		ClientID:      a.ClientID,
		Reserved:      a.Reserved,
		AddrV4:        a.AddrV4,
		AddrV6:        a.AddrV6,
		PeerPublicKey: a.PeerPublicKey,
		Endpoint:      a.Endpoint,
		EndpointV4:    a.EndpointV4,
		EndpointV6:    a.EndpointV6,
		RegisteredAt:  a.RegisteredAt,
		HasToken:      a.Token != "",
		HasPrivateKey: a.PrivateKey != "",
	}
}
