#!/usr/bin/env bash
# 在 Debian 上构建 warpgo。
#
# 本项目的编译 / 测试 / 调试**一律在 Debian 上进行**（见 docs/DESIGN.md §12）：
# Windows 只是编辑代码的地方，不装工具链，也就不会出现「在我机器上能跑」的问题。
#
# 用法:
#   ./build.sh              # 构建到 dist/warpgo
#   VERSION=0.1.0 ./build.sh
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${VERSION:-dev}"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS=(
  -s -w
  -X "github.com/misakacoo/warpgo/internal/version.Version=${VERSION}"
  -X "github.com/misakacoo/warpgo/internal/version.Commit=${COMMIT}"
  -X "github.com/misakacoo/warpgo/internal/version.BuiltAt=${BUILT_AT}"
)

mkdir -p dist
echo "==> go build (version=${VERSION} commit=${COMMIT})"
CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS[*]}" -o dist/warpgo ./cmd/warpgo
echo "==> 产物:"
ls -l dist/warpgo
./dist/warpgo version || true
