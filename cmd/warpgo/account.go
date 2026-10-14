package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/msg"
	"github.com/misakacoo/warpgo/internal/state"
)

// cmdAccount 打印本机账户。
//
// 它是**纯本地**的：读 account.json，不联网、不改任何东西。
// 账户与设备的管理动作（WARP+ 绑定、设备列表、注销、改名）已按用户决定砍掉，
// 这里留下的只有一个只读视图 —— 想看"我这台机器现在拿的是哪个 WARP 地址、
// reserved 是多少"时不必去翻 JSON 文件。
func (a *app) cmdAccount(args []string) int {
	var asJSON bool
	opts := newOptionSet().Bool("--json", &asJSON)
	if err := opts.parse(args); err != nil {
		return a.fail(err)
	}

	acctPath := state.AccountPath()
	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}

	if asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		// 输出脱敏视图：私钥与令牌不进 stdout，避免被重定向进日志或工单。
		if err := enc.Encode(acct.Redacted()); err != nil {
			return a.fail(err)
		}
		return exitOK
	}

	a.printAccount(acct, acctPath)
	return exitOK
}

// printAccount 打印人类可读的账户摘要。
//
// 字段名用 ASCII 固定英文（不翻译）：它们是 account.json 里的键名，
// 翻译反而会让人对不上文件内容。只有句子走 msg。
func (a *app) printAccount(acct *cloudflare.Account, path string) {
	fmt.Fprintln(a.stdout, msg.T(msg.KeyAcctTitle))

	registered := "-"
	if !acct.RegisteredAt.IsZero() {
		registered = acct.RegisteredAt.Format(time.RFC3339)
	}

	fields := [][2]string{
		{"device_id", joinOrDash(acct.ID)},
		{"account_id", joinOrDash(acct.AccountID)},
		{"account_type", joinOrDash(acct.AccountType)},
		{"license", joinOrDash(acct.License)},
		{"public_key", joinOrDash(acct.PublicKey)},
		{"reserved", acct.Reserved.String()},
		{"address_v4", joinOrDash(acct.AddrV4)},
		{"address_v6", joinOrDash(acct.AddrV6)},
		{"peer_public_key", joinOrDash(acct.PeerPublicKey)},
		{"endpoint", joinOrDash(acct.Endpoint)},
		{"registered_at", registered},
	}
	for _, f := range fields {
		fmt.Fprintf(a.stdout, "  %-16s %s\n", f[0], f[1])
	}
	a.log.Infof("%s", msg.Tf(msg.KeyAcctSecretsHint, path))
}
