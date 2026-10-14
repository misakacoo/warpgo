// Package system 汇集与 Debian 主机相关的能力（平台探测、BBR、内核提示等）。
//
// 本项目**只支持 Debian**。不做发行版探测矩阵，也不写 Alpine/busybox/openresolv 分支 ——
// 这是刻意的取舍：上游 shell 版为此付出了大量分支与偶发故障，而我们只要一个平台。
package system

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// DebianVersionFile 是 Debian 的版本标识文件。
const DebianVersionFile = "/etc/debian_version"

// DebianVersionFrom 从指定路径读取 Debian 版本号。
// 第二个返回值为 false 表示这不是 Debian 系统（或文件不可读/为空）。
//
// 抽成带路径参数的函数是为了能在任意平台上做单元测试。
func DebianVersionFrom(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", false
	}
	return v, true
}

// DebianVersion 读取本机 Debian 版本。
func DebianVersion() (string, bool) { return DebianVersionFrom(DebianVersionFile) }

// RequireDebian 在非 Debian 系统上返回错误（错误文案由调用方负责本地化）。
func RequireDebian() error {
	if _, ok := DebianVersion(); !ok {
		return fmt.Errorf("not a Debian system: %s not found", DebianVersionFile)
	}
	return nil
}

// OSReleaseFile 是大多数发行版的标识文件。
const OSReleaseFile = "/etc/os-release"

// PrettyNameFrom 从 os-release 里读出 PRETTY_NAME。
func PrettyNameFrom(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		v, ok := strings.CutPrefix(line, "PRETTY_NAME=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

// PrettyName 返回人类可读的系统名。
//
// 存在的意义是让"只支持 Debian"这句拒绝话说得清楚：直接报 runtime.GOOS
// 会得到 "linux" —— 用户明明在 Linux 上，却被告知"检测到 linux"，
// 这比不说还让人困惑。读出 PRETTY_NAME（如 "Ubuntu 24.04.1 LTS"）
// 才能让人一眼看出问题在哪。
func PrettyName() string {
	if v := PrettyNameFrom(OSReleaseFile); v != "" {
		return v
	}
	return runtime.GOOS
}
