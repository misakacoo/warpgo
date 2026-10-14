// Package version 保存构建期注入的版本信息。
//
// 构建时通过 -ldflags 注入，例如：
//
//	go build -ldflags "-X github.com/misakacoo/warpgo/internal/version.Version=1.0.0 \
//	                   -X github.com/misakacoo/warpgo/internal/version.Commit=abc1234 \
//	                   -X github.com/misakacoo/warpgo/internal/version.BuiltAt=2026-10-14T22:00:00Z"
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version 语义化版本号，如 "0.1.0"；未注入时为 "dev"。
	Version = "dev"
	// Commit 构建时的 git 短哈希。
	Commit = "none"
	// BuiltAt 构建时间（RFC3339）。
	BuiltAt = "unknown"
)

// Info 汇总一行可读的版本信息。
func Info() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s, %s/%s)",
		Version, Commit, BuiltAt, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// Short 只返回版本号。
func Short() string { return Version }
