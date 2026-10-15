#!/usr/bin/env bash
# 在 Debian 上构建 warpgo。
#
# 本项目的编译 / 测试 / 调试**一律在 Debian 上进行**（见 docs/DESIGN.md §12）：
# Windows 只是编辑代码的地方，不装工具链，也就不会出现「在我机器上能跑」的问题。
#
# 用法:
#   ./build.sh                       # 只为本机构建到 dist/warpgo（版本号记作 dev）
#   VERSION=0.1.1 ./build.sh
#   TAG=v0.1.1 ./build.sh release    # 交叉编译发布产物（amd64 + arm64）+ checksums.txt
set -euo pipefail

cd "$(dirname "$0")"

COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ldflags_for <version> —— 把版本信息注入进去（见 internal/version 的包注释）。
ldflags_for() {
  printf -- '-s -w'\
' -X github.com/misakacoo/warpgo/internal/version.Version=%s'\
' -X github.com/misakacoo/warpgo/internal/version.Commit=%s'\
' -X github.com/misakacoo/warpgo/internal/version.BuiltAt=%s' "$1" "$COMMIT" "$BUILT_AT"
}

build_host() {
  local version="${VERSION:-dev}"
  mkdir -p dist
  echo "==> go build (version=${version} commit=${COMMIT})"
  CGO_ENABLED=0 go build -trimpath -ldflags "$(ldflags_for "$version")" -o dist/warpgo ./cmd/warpgo
  echo "==> 产物:"
  ls -l dist/warpgo
  ./dist/warpgo version || true
}

# build_release —— 出发布产物：linux/amd64 与 linux/arm64，各打包成 tar.gz，
# 最后出一份 checksums.txt。
#
# 资产名 `warpgo_<tag>_linux_<arch>.tar.gz` **必须与 internal/update 的
# AssetName() 逐字符一致** —— 那是自更新唯一能用来找文件的东西，
# 对不上就表现为"发布里没有这个资产"，而用户只会看到一句干巴巴的报错。
build_release() {
  local tag="${TAG:-v${VERSION:-0.0.0}}"
  local version="${tag#v}"
  local out=dist/release
  rm -rf "$out"
  mkdir -p "$out"

  for arch in amd64 arm64; do
    local name="warpgo_${tag}_linux_${arch}"
    # 归档内的顶层目录名固定叫 `warpgo`（不含版本号）—— 解包出来就是
    # `warpgo/warpgo`、`warpgo/README.md`、`warpgo/LICENSE`，与常见发布物的
    # 习惯一致，也让人不必先看一眼文件名才知道里面叫什么。
    local stage="$out/warpgo"
    rm -rf "$stage"
    mkdir -p "$stage"
    echo "==> 构建 $name"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
      -ldflags "$(ldflags_for "$version")" -o "$stage/warpgo" ./cmd/warpgo
    # 归档里带上许可与说明：二进制是 root 运行的，拿到的应该是完整的一份东西。
    cp README.md LICENSE "$stage/"
    tar -C "$out" -czf "$out/$name.tar.gz" warpgo
    rm -rf "$stage"
  done

  # 校验和清单（`<sha256>  <文件名>`）—— 自更新会拿它核对下载物。
  ( cd "$out" && sha256sum ./*.tar.gz > checksums.txt )

  echo "==> 发布产物:"
  ls -l "$out"
  echo "==> checksums.txt:"
  cat "$out/checksums.txt"
}

case "${1:-build}" in
  build) build_host ;;
  release) build_release ;;
  *)
    echo "用法: $0 [build|release]" >&2
    exit 2
    ;;
esac
