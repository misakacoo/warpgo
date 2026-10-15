#!/usr/bin/env bash
# warpgo 一键安装脚本。
#
#   curl -fsSL https://raw.githubusercontent.com/misakacoo/warpgo/main/install.sh | sudo bash
#
# 它做的事**只有**这些，别的都不碰：
#   ① 探架构 → ② 解析最新 tag → ③ 下载并校验 sha256 → ④ 装到 /usr/bin/warpgo
#   ⑤ 之后由你选要不要继续（注册账户 / 建隧道 / 装保活服务）
#
# 关于信任：这个脚本本身走 HTTPS 从 GitHub 取，但**没有独立的校验和** ——
# 想先读一遍再跑，把上面的 `| sudo bash` 换成 `| less`。
# 真正下载的产物是**有**校验的（同 Release 下的 checksums.txt），校验不过就停，
# 而且在校验通过之前不会动系统里的任何东西。
#
# 用法:
#   install.sh                       # 交互（推荐）
#   install.sh --version v0.1.1      # 装指定版本
#   install.sh --yes                 # 非交互：只装二进制，不做后续
#   install.sh --with-tunnel         # 非交互：装完接着注册 + 建非全局隧道
#   install.sh --prefix /usr/local/bin
#   install.sh --no-backup
#
# 一个必须知道的细节：`curl | bash` 时 **stdin 是脚本本身**，所以任何提问都不能
# 直接 `read`（那会把脚本文本当输入吃掉）。下面统一从 /dev/tty 读。
# 没有可用的 tty 时，自动退化成 `--yes` 的行为（只装二进制）。

set -euo pipefail

REPO=misakacoo/warpgo
DL_BASE="https://github.com/${REPO}/releases/download"
LATEST_URL="https://github.com/${REPO}/releases/latest"
SELF_URL="https://raw.githubusercontent.com/${REPO}/main/install.sh"

PREFIX=/usr/bin
WANT_TAG=
ASSUME_YES=no
WITH_TUNNEL=no
DO_BACKUP=yes
BACKUP_MADE=no

info() { printf '==> %s\n' "$*"; }
warn() { printf '警告: %s\n' "$*" >&2; }
die() {
	printf '错误: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
warpgo 一键安装脚本

用法:
  install.sh                       # 交互（推荐）
  install.sh --version ${WANT_TAG:-v0.1.1}      # 装指定版本
  install.sh --yes                 # 非交互：只装二进制，不做后续
  install.sh --with-tunnel         # 非交互：装完接着注册 + 建非全局隧道
  install.sh --prefix /usr/local/bin
  install.sh --no-backup

典型用法:
  curl -fsSL ${SELF_URL} | sudo bash

先读一遍再跑（不做校验和，脚本本身没有独立签名）:
  curl -fsSL ${SELF_URL} | less

它只做这些: 探架构 → 解析最新 tag → 下载并校验 sha256 → 装到 ${PREFIX}/warpgo
→ 之后由你选要不要继续（注册账户 / 建隧道 / 装保活服务）。
只支持 Debian，需要 root。
EOF
}

# ---------------------------------------------------------------- 参数

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die "--version 后面要给一个 tag，例如 --version v0.1.1"
		WANT_TAG=$2
		shift 2
		;;
	--prefix)
		[ $# -ge 2 ] || die "--prefix 后面要给一个目录"
		PREFIX=$2
		shift 2
		;;
	--yes | -y)
		ASSUME_YES=yes
		shift
		;;
	--with-tunnel)
		WITH_TUNNEL=yes
		shift
		;;
	--no-backup)
		DO_BACKUP=no
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "不认识的参数: $1（--help 看用法）"
		;;
	esac
done

# ---------------------------------------------------------------- 前置检查

# 需要 root：往 PREFIX 写、配路由、装 systemd 单元都要。
[ "$(id -u)" = 0 ] || die "需要 root。请用 sudo 重跑：curl -fsSL ${SELF_URL} | sudo bash"

# 判断方式与 Go 侧一致（读 /etc/debian_version，只判断"是不是 Debian"，不设最低版本）。
# 保持一致是有意的：安装脚本放行的系统，二进制也该放行，否则用户会在下一步才撞墙。
[ -f /etc/debian_version ] ||
	die "只支持 Debian（判据是 /etc/debian_version 存在）。当前系统不是 Debian。"

for c in curl tar sha256sum install mktemp awk; do
	command -v "$c" >/dev/null 2>&1 ||
		die "缺少命令 $c。Debian 上：apt-get update && apt-get install -y curl tar coreutils"
done

# ---------------------------------------------------------------- 架构

detect_arch() {
	local a
	a=$(dpkg --print-architecture 2>/dev/null || uname -m)
	case "$a" in
	amd64 | x86_64) printf 'amd64\n' ;;
	arm64 | aarch64) printf 'arm64\n' ;;
	*)
		# 发布产物只有这两个架构（与官方客户端的支持面一致）。
		# 别的架构不是"以后再说"，是明确不做 —— 从源码构建仍然可以。
		die "没有 $a 的发布产物（只有 amd64 与 arm64）。可以从源码构建，见 README 的「从源码构建」。"
		;;
	esac
}

# ---------------------------------------------------------------- 版本

# 用 /releases/latest 的**重定向**解析 tag，而不是调 API：
# 不撞未认证的速率限制，也不需要 jq 之类的 JSON 工具。
# 最终地址形如 https://github.com/<repo>/releases/tag/v0.1.1。
resolve_latest_tag() {
	local url
	url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LATEST_URL") ||
		die "取不到最新版本号（网络不通？或者仓库还没有 Release）。可以用 --version 指定。"
	printf '%s\n' "${url##*/}"
}

TAG=${WANT_TAG:-$(resolve_latest_tag)}
case "$TAG" in
v[0-9]*) ;;
*) die "版本号看起来不对: $TAG（应当形如 v0.1.1）" ;;
esac

ARCH=$(detect_arch)
NAME="warpgo_${TAG}_linux_${ARCH}.tar.gz"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

info "版本 ${TAG} / 架构 ${ARCH}"

# ---------------------------------------------------------------- 下载与校验

info "下载 ${NAME}"
curl -fL --retry 3 --retry-delay 2 -o "${WORK}/${NAME}" "${DL_BASE}/${TAG}/${NAME}" ||
	die "下载失败：${DL_BASE}/${TAG}/${NAME}（这个版本有 ${ARCH} 的产物吗？）"

curl -fL --retry 3 --retry-delay 2 -o "${WORK}/checksums.txt" "${DL_BASE}/${TAG}/checksums.txt" ||
	die "取不到校验和清单，拒绝继续：${DL_BASE}/${TAG}/checksums.txt"

# 只核对自己下的那一个文件，而不是整份 `sha256sum -c checksums.txt`。
# 清单里两个架构都列着，而我们只下了一个 —— 整份校验会因为另一个文件不在
# 而报 "FAILED open or read" 并把退出码变成 1。那是正常情况，不是文件坏了。
#
# 匹配时容忍清单里带不带 `./` 前缀（两种写法都出现过）：拿 basename 比。
WANT_SUM=$(awk -v n="$NAME" '{ f = $2; sub(/^\.\//, "", f); if (f == n) { print $1; exit } }' \
	"${WORK}/checksums.txt")
[ -n "$WANT_SUM" ] || die "校验和清单里没有 ${NAME}，拒绝安装"

GOT_SUM=$(sha256sum "${WORK}/${NAME}" | awk '{print $1}')
if [ "$WANT_SUM" != "$GOT_SUM" ]; then
	die "校验和不符，已停止（系统里的东西一个字节都没动）。
  期望 ${WANT_SUM}
  实际 ${GOT_SUM}
文件可能在传输中被截断或篡改。换个网络重试，或在本地下载后手动安装。"
fi
info "校验和 OK"

# ---------------------------------------------------------------- 安装

STAGE="${WORK}/stage"
mkdir -p "$STAGE"
tar -xzf "${WORK}/${NAME}" -C "$STAGE" || die "归档解不开（文件可能损坏）"
[ -f "${STAGE}/warpgo/warpgo" ] || die "归档结构不对：找不到 warpgo/warpgo"
[ -f "${STAGE}/warpgo/LICENSE" ] || warn "归档里没有 LICENSE，来路可疑"

BIN="${PREFIX}/warpgo"

if [ -e "$BIN" ]; then
	OLD=$("$BIN" version 2>/dev/null | head -1 || true)
	if [ "$DO_BACKUP" = yes ]; then
		cp -p "$BIN" "${BIN}.bak"
		BACKUP_MADE=yes
		info "已存在（${OLD:-无法识别版本}），旧文件备份到 ${BIN}.bak"
	else
		info "已存在（${OLD:-无法识别版本}），按 --no-backup 直接覆盖"
	fi
fi

# 先落到同目录的临时名再 rename —— 与内部 self-update 的 writeFileAtomic 同一个理由：
# 直接往正在执行的路径上写会撞 ETXTBSY，而且中途失败会留下半个二进制。
# 先装到 ${BIN}.new.$$，再 mv 覆盖，是原子的。
TMP_BIN="${BIN}.new.$$"
install -m 0755 "${STAGE}/warpgo/warpgo" "$TMP_BIN" || die "写入 ${TMP_BIN} 失败"
mv -f "$TMP_BIN" "$BIN" || {
	rm -f "$TMP_BIN"
	die "替换 ${BIN} 失败"
}
info "已安装到 ${BIN}"

# 保活服务在跑的话，它映射的还是旧的那份文件 —— 不重启等于"装了但没生效"。
if systemctl is-active --quiet warpgo-watch 2>/dev/null; then
	if systemctl restart warpgo-watch 2>/dev/null; then
		info "保活服务在运行，已重启以使用新二进制"
	else
		warn "保活服务重启失败，可手动：systemctl restart warpgo-watch"
	fi
fi

printf '\n'
"$BIN" version || true

# ---------------------------------------------------------------- 后续步骤

# 拿二进制自己当探针，而不是去拼 /etc/warpgo/account.json 的路径 ——
# 它自己认 WARPGO_ETC 覆盖，路径知识只该有一处（internal/state）。
# 没有账户时 `account` 退出码非 0。
have_account() { "$BIN" account >/dev/null 2>&1; }

# 注册是**幂等但不重复**的：已有账户时 register 会拒绝（换注册等于换出口 IP，
# 必须是显式决定）。所以这里先探测，别让用户在菜单里撞一个注定失败的选项。
do_register() {
	if have_account; then
		info "已有 WARP 账户，跳过注册（要换出口 IP 用 warpgo rotate）"
		return 0
	fi
	info "注册账户……"
	"$BIN" register || die "注册失败。可单独重跑：warpgo register"
}

do_up() {
	info "建立隧道（非全局：只让指定源地址走 WARP，不动默认路由、不断 SSH）……"
	"$BIN" up || die "建隧道失败。可用 warpgo status 看状态，或重跑 warpgo up"
}

do_keepalive() {
	info "把保活装成 systemd 服务……"
	"$BIN" install || die "装保活失败。可单独重跑：warpgo install"
}

finish() {
	cat <<EOF

完成。常用命令：
  warpgo status                   看当前状态
  warpgo menu                     交互菜单（每个选项就是一条子命令）
  warpgo version --self-update    升级到最新版本
  warpgo uninstall                撤干净（单元、隧道、二进制、DNS 改动）
EOF

	# 备份是我们自己留下的东西，得告诉用户它在哪、什么时候能删 ——
	# 否则它会一直躺在 /usr/bin 里，成为"某次安装的遗迹"。
	if [ "$BACKUP_MADE" = yes ]; then
		printf '（旧二进制备份在 %s.bak，确认新版没问题后可以删掉）\n' "$BIN"
	fi
}

if [ "$ASSUME_YES" = yes ]; then
	if [ "$WITH_TUNNEL" = yes ]; then
		do_register
		do_up
	fi
	finish
	exit 0
fi

# 真的能不能打开 /dev/tty？—— 不能只用 `[ -r /dev/tty ]` / `[ -w /dev/tty ]`：
# 设备节点权限通常是 666，access(2) 会说"可读写"；但没有控制终端时 open(2) 给的是
# ENXIO。于是那个检查会放过一个注定读不到输入的现场 —— 菜单照印，输入直接落默认值。
# 实测确认过（ssh 不带 -t 时 `[ -rw /dev/tty ]` 真、`(exec </dev/tty)` 假）。
has_tty() { (exec </dev/tty) 2>/dev/null; }

# 没有可用的终端（无人值守）→ 退化成"只装二进制"，不猜你的意图。
if ! has_tty; then
	warn "没有可用的终端，跳过后续步骤（等同于 --yes）。"
	finish
	exit 0
fi

# 提问必须走 /dev/tty：`curl | bash` 时 stdin 是脚本本身。
ask() {
	local ans=
	printf '%s' "$1" >/dev/tty
	IFS= read -r ans </dev/tty || ans=
	printf '%s' "$ans"
}

cat <<'EOF'

接下来做什么？（下面这些只动 warpgo 自己的配置；非全局模式不动默认路由、也不会断 SSH）

EOF

# 按现场状态给菜单：已有账户就不摆"注册"这项，否则那一步注定失败。
HAS_ACCOUNT=no
if have_account; then
	HAS_ACCOUNT=yes
	cat <<'EOF'
检测到本机已有 WARP 账户，跳过注册相关选项
（要换出口 IP：warpgo rotate；想重新注册：warpgo register --force）。

  1) 就到这里 —— 什么都不做
  2) 建立隧道（非全局）
  3) 2 + 把保活装成 systemd 服务（开机自启，断线自动重建）

EOF
else
	cat <<'EOF'
  1) 就到这里 —— 什么都不做
  2) 注册一个免费的 WARP 账户
  3) 2 + 建立隧道（非全局）
  4) 3 + 把保活装成 systemd 服务（开机自启，断线自动重建）

EOF
fi

while :; do
	choice=$(ask '选择（回车 = 1）: ')
	case "$choice" in
	'' | 1)
		break
		;;
	2)
		if [ "$HAS_ACCOUNT" = yes ]; then do_up; else do_register; fi
		break
		;;
	3)
		if [ "$HAS_ACCOUNT" = yes ]; then
			do_up
			do_keepalive
		else
			do_register
			do_up
		fi
		break
		;;
	4)
		if [ "$HAS_ACCOUNT" = yes ]; then
			printf '请输入 1 到 3。\n' >/dev/tty
			continue
		fi
		do_register
		do_up
		do_keepalive
		break
		;;
	*)
		if [ "$HAS_ACCOUNT" = yes ]; then
			printf '请输入 1 到 3。\n' >/dev/tty
		else
			printf '请输入 1 到 4。\n' >/dev/tty
		fi
		;;
	esac
done

finish
