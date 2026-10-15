#!/usr/bin/env bash
# M9 验收：版本查询 + 打包 + 自我更新。
#
# # 为什么用"本地假发布页"而不是真的 GitHub
#
# 自更新最要紧的性质是"**失败时磁盘上那个能用的文件一个字节都没动**"，
# 而要验这一条就必须能构造出"下载物是坏的 / 校验和对不上 / 清单取不到"这些情形 ——
# 真 GitHub 上做不到（我们不能往发布里塞一个坏产物）。
#
# 所以这里搭一个**目录结构的假发布页**（`python3 -m http.server` 直接服务文件），
# 目录布局与 GitHub 一致：
#
#   <root>/repos/<repo>/releases/latest                       ← API 响应
#   <root>/<repo>/releases/download/<tag>/<asset>.tar.gz
#   <root>/<repo>/releases/download/<tag>/checksums.txt
#
# 自更新那边只需要 `--api-base` / `--base` / `--repo` 三个可覆盖项
# （它们本来就是为了这个而存在的）。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
REPO=${REPO:-test/warpgo}
PORT=${PORT:-38571}
BASE="http://127.0.0.1:$PORT"
TAG_OK=v9.9.9
TAG_SAME=v0.1.0
WORK=/tmp/m9
SRV=$WORK/www
GOBIN=${GOBIN:-$(command -v go || echo /usr/local/go/bin/go)}
# 源码根目录（BIN 在 <repo>/dist/warpgo）。
REPO_DIR=$(cd "$(dirname "$BIN")/.." && pwd)

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }
[ -d "$REPO_DIR/cmd/warpgo" ] || { echo "找不到源码目录: $REPO_DIR"; exit 1; }

pass=0
fail=0
ok()      { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()     { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note()    { printf '  · %s\n' "$1"; }
section() { printf '\n== %s ==\n' "$1"; }

ARCH=$(uname -m)
case "$ARCH" in
  x86_64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "不支持的架构 $ARCH"; exit 1 ;;
esac
ASSET="warpgo_${TAG_OK}_linux_${GOARCH}.tar.gz"

cleanup() {
  [ -n "${SRV_PID:-}" ] && kill "$SRV_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap 'cleanup' EXIT

# ---- 0. 准备 -------------------------------------------------------------------

section "0. 准备"
rm -rf "$WORK"
mkdir -p "$SRV/repos/$REPO/releases" \
         "$SRV/$REPO/releases/download/$TAG_OK" \
         "$SRV/$REPO/releases/download/$TAG_SAME"

echo "==> 构建一个 9.9.9 的假发布产物"
( cd "$REPO_DIR" && "$GOBIN" build -trimpath \
    -ldflags "-s -w -X github.com/misakacoo/warpgo/internal/version.Version=9.9.9" \
    -o "$WORK/warpgo-9.9.9" ./cmd/warpgo )
[ -x "$WORK/warpgo-9.9.9" ] && ok "9.9.9 的产物已构建" || bad "构建 9.9.9 产物失败"

mkdir -p "$WORK/pkg/warpgo"
cp "$WORK/warpgo-9.9.9" "$WORK/pkg/warpgo/warpgo"
tar -C "$WORK/pkg" -czf "$SRV/$REPO/releases/download/$TAG_OK/$ASSET" warpgo
( cd "$SRV/$REPO/releases/download/$TAG_OK" && sha256sum ./*.tar.gz > checksums.txt )
ok "假发布资产已就位（$ASSET，$(stat -c%s "$SRV/$REPO/releases/download/$TAG_OK/$ASSET") 字节）"

# 同一份归档再放一份到 v0.1.0 那个 tag 下，**文件名要跟着 tag 走** ——
# 自更新是按 AssetName(tag) 找文件的，名字对不上就会报"发布里没有这个资产"。
ASSET_SAME="warpgo_${TAG_SAME}_linux_${GOARCH}.tar.gz"
cp "$SRV/$REPO/releases/download/$TAG_OK/$ASSET" "$SRV/$REPO/releases/download/$TAG_SAME/$ASSET_SAME"
( cd "$SRV/$REPO/releases/download/$TAG_SAME" && sha256sum ./*.tar.gz > checksums.txt )

# `--version <tag>` 走的是 `/releases/tags/<tag>`，所以那条路径也要有静态响应。
# 它必须**回显被请求的那个 tag** —— 真 GitHub 就是这样；不回显的话，
# "指定版本"会看起来装成功、实际装的是另一个版本。
mkdir -p "$SRV/repos/$REPO/releases/tags"
cat > "$SRV/repos/$REPO/releases/tags/$TAG_SAME" <<JSON
{"tag_name":"$TAG_SAME","html_url":"$BASE/releases/$TAG_SAME","assets":[{"name":"$ASSET_SAME","browser_download_url":"$BASE/$REPO/releases/download/$TAG_SAME/$ASSET_SAME"}]}
JSON

# API 响应。latest 指向哪个 tag 由下面的用例切换。
write_latest() { # $1 = tag，$2 = 资产名（留空则用 9.9.9 那个）
  local tag=$1 asset=${2:-$ASSET}
  local url="$BASE/$REPO/releases/download/$tag/$asset"
  cat > "$SRV/repos/$REPO/releases/latest" <<JSON
{"tag_name":"$tag","html_url":"$BASE/releases/$tag","assets":[{"name":"$asset","browser_download_url":"$url"}]}
JSON
}
write_latest "$TAG_OK"

python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$SRV" >/tmp/m9-server.log 2>&1 &
SRV_PID=$!
sleep 1
curl -fsS "$BASE/repos/$REPO/releases/latest" >/dev/null 2>&1 \
  && ok "假发布页已就绪（$BASE）" || bad "假发布页没起来"

# 自更新要动二进制，所以拿一份**副本**来跑，绝不碰 dist/warpgo。
CAND=$WORK/warpgo-candidate
cp "$BIN" "$CAND"
chmod +x "$CAND"

# ---- 1. version 基本行为 -------------------------------------------------------

section "1. version"
"$CAND" version >/tmp/m9-v.txt 2>&1 && ok "version 返回 0" || bad "version 失败"
grep -q "warpgo" /tmp/m9-v.txt && ok "version 打印了程序名与版本" || bad "version 输出不对"
grep -q "Debian" /tmp/m9-v.txt && ok "识别出 Debian 平台" || bad "没识别出 Debian"
"$CAND" version --bogus >/tmp/m9-vb.txt 2>&1 && bad "未知选项应当被拒绝" || ok "未知选项被拒绝"

# ---- 2. 打包（./build.sh release）-----------------------------------------------

section "2. 打包"
if [ -x "$REPO_DIR/build.sh" ]; then
  ( cd "$REPO_DIR" && TAG=v0.1.0 ./build.sh release >/tmp/m9-rel.txt 2>&1 ) \
    && ok "build.sh release 成功" || bad "build.sh release 失败: $(tail -3 /tmp/m9-rel.txt)"
  for a in amd64 arm64; do
    f="$REPO_DIR/dist/release/warpgo_v0.1.0_linux_$a.tar.gz"
    [ -f "$f" ] && ok "产出了 $a 的归档" || bad "缺 $a 的归档"
  done
  [ -f "$REPO_DIR/dist/release/checksums.txt" ] && ok "产出了 checksums.txt" || bad "缺 checksums.txt"
  n=$(grep -cE '^[0-9a-f]{64}  \./warpgo_v0.1.0_linux_' "$REPO_DIR/dist/release/checksums.txt" || true)
  [ "$n" = "2" ] && ok "checksums.txt 里两条记录格式正确" || bad "checksums.txt 记录数=$n"
  # 归档里应当带上 LICENSE 与 README（二进制是 root 运行的，应该拿到完整一份东西）。
  ( cd "$REPO_DIR/dist/release" && tar -tzf "warpgo_v0.1.0_linux_$GOARCH.tar.gz" ) > /tmp/m9-toc.txt 2>&1
  grep -q "warpgo/LICENSE" /tmp/m9-toc.txt && ok "归档里带了 LICENSE" || bad "归档里没有 LICENSE"
  grep -q "warpgo/README.md" /tmp/m9-toc.txt && ok "归档里带了 README.md" || bad "归档里没有 README.md"
  grep -q "warpgo/warpgo$" /tmp/m9-toc.txt && ok "归档里有二进制" || bad "归档里没有二进制"
  # 归档里那个二进制要真能跑。
  ( cd "$REPO_DIR/dist/release" && tar -xzf "warpgo_v0.1.0_linux_$GOARCH.tar.gz" -C /tmp \
      warpgo/warpgo && /tmp/warpgo/warpgo version >/tmp/m9-inner.txt 2>&1 ) \
    && ok "归档里的二进制能执行（$(head -1 /tmp/m9-inner.txt)）" \
    || bad "归档里的二进制跑不起来"
else
  note "找不到 $REPO_DIR/build.sh，跳过打包一节"
fi

# ---- 3. --check 的三种结论 -----------------------------------------------------

section "3. --check"
"$CAND" version --check --repo "$REPO" --api-base "$BASE" --base "$BASE" >/tmp/m9-c1.txt 2>&1
[ $? = 0 ] && ok "--check 返回 0（落后不是错误）" || bad "--check 非 0"
grep -q "有新版本" /tmp/m9-c1.txt && ok "判定「有新版本」" || bad "没判出新版本：$(tail -2 /tmp/m9-c1.txt)"
grep -q "9.9.9" /tmp/m9-c1.txt && ok "报出了新版本号" || bad "没报出新版本号"

write_latest "$TAG_SAME" "$ASSET_SAME"
"$CAND" version --check --repo "$REPO" --api-base "$BASE" --base "$BASE" >/tmp/m9-c2.txt 2>&1
grep -q "已是最新" /tmp/m9-c2.txt && ok "同版本判成「已是最新」" || bad "同版本判定不对：$(tail -2 /tmp/m9-c2.txt)"

# dev 构建（没注入版本号）应当被明确标成"不可比"，而不是静默地说"已是最新"。
DEV=$WORK/warpgo-dev
( cd "$REPO_DIR" && "$GOBIN" build -trimpath -o "$DEV" ./cmd/warpgo ) 2>/dev/null
if [ -x "$DEV" ]; then
  "$DEV" version --check --repo "$REPO" --api-base "$BASE" --base "$BASE" >/tmp/m9-c3.txt 2>&1
  grep -q "没法与最新发布" /tmp/m9-c3.txt && ok "dev 构建被标成「不可比」" || bad "dev 判定不对：$(tail -2 /tmp/m9-c3.txt)"
else
  note "dev 构建失败，跳过该用例"
fi

# 发布页不存在时要说清楚，而不是崩掉。
"$CAND" version --check --repo "$REPO" --api-base "$BASE/nonexistent" --base "$BASE" >/tmp/m9-c4.txt 2>&1
[ $? != 0 ] && ok "查不到发布时以非 0 退出" || bad "查不到发布却返回 0"
grep -q "查询新版本失败" /tmp/m9-c4.txt && ok "查不到发布时给出了明确报错" || bad "报错不清楚"

# ---- 4. 自我更新 ---------------------------------------------------------------

section "4. --self-update"
write_latest "$TAG_OK"
"$CAND" version >/tmp/m9-before.txt 2>&1
BEFORE=$(grep -o "warpgo [0-9.]*" /tmp/m9-before.txt | head -1)
note "更新前: $BEFORE"

"$CAND" version --self-update --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-u1.txt 2>&1
[ $? = 0 ] && ok "--self-update 成功" || bad "--self-update 失败: $(tail -3 /tmp/m9-u1.txt)"
grep -q "已更新到 9.9.9" /tmp/m9-u1.txt && ok "报出了更新后的版本" || bad "更新输出不对"
"$CAND" version >/tmp/m9-after.txt 2>&1
grep -q "warpgo 9.9.9" /tmp/m9-after.txt && ok "候选文件确实换成了 9.9.9" || bad "文件没被换掉：$(head -1 /tmp/m9-after.txt)"
[ -x "$CAND" ] && ok "替换后仍是可执行" || bad "替换后丢了执行权限"

# 再次 --check 应当说"已是最新"（当前 9.9.9 == 最新 9.9.9）。
"$CAND" version --check --repo "$REPO" --api-base "$BASE" --base "$BASE" >/tmp/m9-u2.txt 2>&1
grep -q "已是最新" /tmp/m9-u2.txt && ok "更新后再查是「已是最新」" || bad "更新后的检查不对"

# 同版本再更新一次：应当不动手，并提示 --force。
"$CAND" version --self-update --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-u3.txt 2>&1
grep -q "没有变化" /tmp/m9-u3.txt && ok "同版本时不重复下载（提示 --force）" || bad "同版本判定不对: $(tail -2 /tmp/m9-u3.txt)"

# --force 重装。
"$CAND" version --self-update --force --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-u4.txt 2>&1
grep -q "已更新到 9.9.9" /tmp/m9-u4.txt && ok "--force 会重装同一个版本" || bad "--force 重装失败"

# 指定版本（降级回 0.1.0 那个 tag —— 它的资产其实是同一份 9.9.9，
# 这里验的是"指定 tag 时不做更新方向拦截"）。
"$CAND" version --self-update --version "$TAG_SAME" --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-u5.txt 2>&1
grep -q "已更新到 0.1.0" /tmp/m9-u5.txt && ok "--version 指定 tag 时不拦截方向" || bad "指定版本失败: $(tail -2 /tmp/m9-u5.txt)"

# ---- 5. 坏产物绝不能覆盖能用的那个 ---------------------------------------------

section "5. 失败路径（旧文件必须一个字节都没动）"

# 5a 校验和不符。
cp "$BIN" "$CAND"; chmod +x "$CAND"
( cd "$SRV/$REPO/releases/download/$TAG_OK" && printf '%064d  %s\n' 0 "$ASSET" > checksums.txt )
"$CAND" version --self-update --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-f1.txt 2>&1
[ $? != 0 ] && ok "校验和不符时以非 0 退出" || bad "校验和不符却成功了"
grep -q "校验和不符" /tmp/m9-f1.txt && ok "报错说明了是校验和不符" || bad "报错不清楚"
"$CAND" version >/tmp/m9-f1b.txt 2>&1
grep -q "$BEFORE" /tmp/m9-f1b.txt && ok "旧文件没被动过（仍是 $BEFORE）" || bad "旧文件被改动了"

# 5b 清单取不到。
rm -f "$SRV/$REPO/releases/download/$TAG_OK/checksums.txt"
"$CAND" version --self-update --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-f2.txt 2>&1
[ $? != 0 ] && ok "拿不到清单时以非 0 退出（不静默跳过校验）" || bad "拿不到清单却成功了"
grep -q "拿不到校验和清单" /tmp/m9-f2.txt && ok "报错说明了清单取不到" || bad "报错不清楚"
"$CAND" version >/tmp/m9-f2b.txt 2>&1
grep -q "$BEFORE" /tmp/m9-f2b.txt && ok "旧文件仍没被动过" || bad "旧文件被改动了"

# 5c 产物跑不起来（归档里是一段普通文本，校验和是对的）。
echo "这不是一个程序" > "$WORK/pkg/warpgo/warpgo"
tar -C "$WORK/pkg" -czf "$SRV/$REPO/releases/download/$TAG_OK/$ASSET" warpgo
( cd "$SRV/$REPO/releases/download/$TAG_OK" && sha256sum ./*.tar.gz > checksums.txt )
"$CAND" version --self-update --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-f3.txt 2>&1
[ $? != 0 ] && ok "产物跑不起来时以非 0 退出" || bad "坏产物却装成功了"
"$CAND" version >/tmp/m9-f3b.txt 2>&1
grep -q "$BEFORE" /tmp/m9-f3b.txt && ok "坏产物没有覆盖掉能用的那个" || bad "旧文件被坏产物覆盖了！"
ls "$WORK"/.warpgo-install.tmp* >/dev/null 2>&1 && bad "留下了临时文件" || ok "没有留下临时文件"

# 5d 没有对应架构的资产。
cat > "$SRV/repos/$REPO/releases/latest" <<JSON
{"tag_name":"$TAG_OK","html_url":"$BASE/releases/$TAG_OK","assets":[{"name":"warpgo_${TAG_OK}_linux_riscv64.tar.gz","browser_download_url":"$BASE/x"}]}
JSON
"$CAND" version --self-update --repo "$REPO" --api-base "$BASE" --base "$BASE" --no-restart >/tmp/m9-f4.txt 2>&1
[ $? != 0 ] && ok "没有对应资产时以非 0 退出" || bad "缺资产却成功了"
grep -q "riscv64" /tmp/m9-f4.txt && ok "报错里列出了实际有哪些资产" || bad "报错没列出实际资产"

# ---- 6. 与真实 GitHub 的一次接触（网络相关，失败只记 note）---------------------

section "6. 真实 GitHub（可选）"
if timeout 40 "$CAND" version --check --repo misakacoo/warpgo >/tmp/m9-real.txt 2>&1; then
  ok "对真实 GitHub 的查询成功: $(tail -1 /tmp/m9-real.txt)"
else
  note "对真实 GitHub 的查询未成功（网络或限流），跳过：$(tail -1 /tmp/m9-real.txt)"
fi

printf '\n== 结果 ==\n  通过 %d / 失败 %d\n' "$pass" "$fail"
if [ "$fail" != "0" ]; then exit 1; fi
