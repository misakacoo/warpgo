package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/misakacoo/warpgo/internal/cloudflare"
	"github.com/misakacoo/warpgo/internal/i18n"
	"github.com/misakacoo/warpgo/internal/state"
)

func (a *app) cmdAccount(args []string) int {
	var (
		asJSON  bool
		refresh bool
		enable  bool
		license string
	)
	opts := newOptionSet().
		Bool("--json", &asJSON).
		Bool("--refresh", &refresh).
		Bool("--enable", &enable).
		Str("--license", &license)
	if err := opts.parse(args, a.bundle.Tf); err != nil {
		return a.fail(err)
	}

	acctPath := state.AccountPath()
	acct, code := a.loadAccount()
	if code != exitOK {
		return code
	}

	// 只有明确要求时才联网。默认路径是纯本地读，这样 `warpgo account`
	// 在没有网络、或被限流时依然可用（排查现场往往正是网络有问题的时候）。
	if refresh || enable || license != "" {
		ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
		defer cancel()
		client := cloudflare.New(cloudflare.Options{})

		if enable {
			if _, err := client.EnableWarp(ctx, acct.ID, acct.Token); err != nil {
				return a.fail(err)
			}
			a.log.Infof("%s", a.bundle.T(i18n.KeyAcctEnableOK))
		}

		if license != "" {
			if err := client.SetLicense(ctx, acct.ID, acct.Token, license); err != nil {
				return a.fail(err)
			}
			acct.License = license
			a.log.Infof("%s", a.bundle.T(i18n.KeyAcctLicenseOK))
		}

		if refresh {
			dev, err := client.GetDevice(ctx, acct.ID, acct.Token)
			if err != nil {
				return a.fail(err)
			}
			acct.ApplyDevice(dev)
			a.log.Infof("%s", a.bundle.T(i18n.KeyAcctRefreshed))
		}

		if err := acct.Save(acctPath); err != nil {
			return a.fail(err)
		}
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
// 翻译反而会让人对不上文件内容。只有句子走 i18n。
func (a *app) printAccount(acct *cloudflare.Account, path string) {
	fmt.Fprintln(a.stdout, a.bundle.T(i18n.KeyAcctTitle))

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
	a.log.Infof("%s", a.bundle.Tf(i18n.KeyAcctSecretsHint, path))
}
