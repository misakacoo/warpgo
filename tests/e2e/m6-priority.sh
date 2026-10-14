#!/usr/bin/env bash
# M6 验收：地址族优先级（菜单 s）。
#
# 这个脚本会**真的改 /etc/gai.conf**，并在结束时把它还原成进入时的样子
# （用 sha256 逐字节核对）。三重兜底：
#     ① 自己的 trap：无论怎么退出都还原
#     ② 每个断言都对着"文件内容 + getent 的真实顺序"两个层面来验
#     ③ 调用方挂的死人开关
#
# # 为什么断言里必须有 getent
#
# 只看"文件里那行在不在"是**不够的**：上游那行 `label 2002::/16   2` 在
# Debian 12（glibc 2.36）上实测**不改变任何行为**（默认就已经是 IPv6 优先）。
# 换句话说，如果只断言文件内容，一个"什么都没做"的实现也会全绿。
# 所以这里用 getent ahosts（走的是 getaddrinfo，也就是 glibc 真正的那条路）
# 观察解析顺序 —— 那才是用户能看到的效果。
#
# 注意不能用 Go 程序去测这件事：Go 的解析器不读 gai.conf，用它测会得出
# "gai.conf 无效"的错误结论。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
GAICONF=${GAICONF:-/etc/gai.conf}
HOST=${HOST:-www.cloudflare.com}
ORIG=/tmp/m6-gai.conf.origin

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }
[ -f "$GAICONF" ] || { echo "找不到 $GAICONF"; exit 1; }
command -v getent >/dev/null || { echo "需要 getent（libc-bin）"; exit 1; }

pass=0
fail=0
ok()      { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()     { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note()    { printf '  · %s\n' "$1"; }
section() { printf '\n== %s ==\n' "$1"; }

# ---- 现场判据 ------------------------------------------------------------------

sum() { sha256sum "$GAICONF" | cut -d' ' -f1; }
# 生效的受管行数（注释掉的不算 —— 原厂文件里本来就有一行被注释的 v4 写法）。
managed() { # $1 = 4|6
  grep -cE "^precedence[[:space:]]+::ffff:0:0/96[[:space:]]+100" "$GAICONF" 2>/dev/null || true
}
managed6() {
  grep -cE "^label[[:space:]]+2002::/16[[:space:]]+2" "$GAICONF" 2>/dev/null || true
}
# getent 的第一条：判断"实际优先级"的唯一硬判据。
first_family() {
  local first
  first=$(getent ahosts "$HOST" 2>/dev/null | awk 'NR==1{print $1}')
  case "$first" in
    *:*) echo "IPv6 $first" ;;
    '')  echo "none" ;;
    *)   echo "IPv4 $first" ;;
  esac
}

ORIG_SUM=$(sum)
cp -f "$GAICONF" "$ORIG"

restore() {
  [ -f "$ORIG" ] || return 0
  cp -f "$ORIG" "$GAICONF"
}
trap 'restore' EXIT

printf 'M6 验收：地址族优先级\n'
printf '  二进制 %s\n  目标文件 %s\n  观察域名 %s（走 getent → getaddrinfo）\n' "$BIN" "$GAICONF" "$HOST"

# ---- 1. 初始状态 ---------------------------------------------------------------

section "1. 初始状态（进入时的样子）"
note "原始 sha256: $ORIG_SUM"
note "受管行: v4=$(managed) v6=$(managed6)"
note "当前解析顺序: $(first_family)"
"$BIN" priority >/tmp/m6-show.txt 2>&1 && ok "priority（无参数）显示当前状态" || bad "priority 显示失败"
grep -q "gai.conf" /tmp/m6-show.txt && ok "输出里有 gai.conf 一项" || bad "输出缺少 gai.conf"
grep -q "resolve_order" /tmp/m6-show.txt && ok "输出里有实测的解析顺序" || bad "输出缺少 resolve_order"

# ---- 2. 4 = IPv4 优先 ----------------------------------------------------------

section "2. priority 4（IPv4 优先）"
"$BIN" priority 4 >/tmp/m6-set4.txt 2>&1 && ok "设置成功（rc=0）" || bad "设置失败: $(cat /tmp/m6-set4.txt)"
[ "$(managed)" = "1" ] && ok "文件里出现且仅出现一条生效的 v4 行" || bad "v4 行数 = $(managed)"
[ "$(managed6)" = "0" ] && ok "没有残留 v6 行" || bad "还留着 v6 行"
grep -q '^#precedence ::ffff:0:0/96  100' "$GAICONF" \
  && ok "被注释掉的原厂行没被动过（只改自己那两行）" \
  || bad "原厂的注释行被改动了"
case "$(first_family)" in
  IPv4*) ok "getent 的第一条是 IPv4（$(first_family)）—— 优先级真的生效" ;;
  *)     bad "getent 的第一条是 $(first_family)，期望 IPv4" ;;
esac

SUM_AFTER_4=$(sum)
"$BIN" priority 4 >/tmp/m6-set4b.txt 2>&1
case "$(cat /tmp/m6-set4b.txt)" in
  *未改动*) ok "重复设置同一模式报告「未改动」" ;;
  *)        bad "重复设置没有报告未改动: $(cat /tmp/m6-set4b.txt)" ;;
esac
[ "$(sum)" = "$SUM_AFTER_4" ] && ok "重复设置没有改变文件（幂等）" || bad "重复设置改动了文件"

# ---- 3. 6 = IPv6 优先 ----------------------------------------------------------

section "3. priority 6（IPv6 优先）"
"$BIN" priority 6 >/tmp/m6-set6.txt 2>&1 && ok "设置成功（rc=0）" || bad "设置失败"
[ "$(managed)" = "0" ] && ok "v4 优先行已被删掉（这一步才是真正生效的动作）" || bad "v4 行还在"
[ "$(managed6)" = "1" ] && ok "写入了 v6 行（与上游逐行一致）" || bad "没有 v6 行"
case "$(first_family)" in
  IPv6*) ok "getent 的第一条是 IPv6（$(first_family)）" ;;
  *)     bad "getent 的第一条是 $(first_family)，期望 IPv6" ;;
esac
grep -q "默认就是 IPv6 优先" /tmp/m6-set6.txt \
  && ok "如实说明了「这一行在本机不改变行为」" \
  || bad "没有给出 gai.conf 语义的说明（会让人以为设了没效果）"

# ---- 4. d = 系统默认，且必须逐字节还原 -----------------------------------------

section "4. priority d（系统默认）+ 零改动"
"$BIN" priority d >/tmp/m6-setd.txt 2>&1 && ok "设置成功（rc=0）" || bad "设置失败"
[ "$(managed)" = "0" ] && [ "$(managed6)" = "0" ] && ok "两行都不在了" || bad "还有受管行残留"
[ "$(sum)" = "$ORIG_SUM" ] \
  && ok "文件与进入时逐字节相同（零改动）" \
  || bad "文件变了：$(sum) != $ORIG_SUM"
"$BIN" priority d >/tmp/m6-setd2.txt 2>&1
case "$(cat /tmp/m6-setd2.txt)" in
  *未改动*) ok "再次切默认报告「未改动」" ;;
  *)        bad "再次切默认没有报告未改动" ;;
esac

# ---- 5. 别人写的行不能被误伤 ---------------------------------------------------

section "5. 不误伤别人的内容"
echo "# m6-验收标记行" >>"$GAICONF"
MARK_SUM=$(sum)
for mode in 4 6 d; do
  "$BIN" priority "$mode" >/dev/null 2>&1
done
grep -q '^# m6-验收标记行$' "$GAICONF" \
  && ok "三轮切换之后我们自己加的行仍在" \
  || bad "别的行被吃掉了"
[ "$(sum)" = "$MARK_SUM" ] \
  && ok "三轮切换之后文件回到标记时的状态" \
  || bad "文件状态对不上"
cp -f "$ORIG" "$GAICONF"

# ---- 6. 参数与报错 -------------------------------------------------------------

section "6. 参数形态与报错"
"$BIN" priority --mode 4 >/dev/null 2>&1 && ok "--mode 4 也被接受" || bad "--mode 4 失败"
"$BIN" priority --mode 6 >/dev/null 2>&1 && ok "--mode 6 也被接受" || bad "--mode 6 失败"
if "$BIN" priority 5 >/tmp/m6-bad.txt 2>&1; then
  bad "非法取值 5 居然成功了"
else
  ok "非法取值 5 以非零码退出"
fi
grep -qE "未知的地址族优先级" /tmp/m6-bad.txt && ok "报错说清了可用取值" || bad "报错不清楚: $(cat /tmp/m6-bad.txt)"
if "$BIN" priority 4 --mode 6 >/dev/null 2>&1; then
  bad "同时给位置参数与 --mode 居然成功了"
else
  ok "同时给位置参数与 --mode 会报错"
fi
if "$BIN" priority --frobnicate >/dev/null 2>&1; then
  bad "未知选项居然被接受"
else
  ok "未知选项被拒绝"
fi
"$BIN" priority --json >/tmp/m6-json.txt 2>&1 && ok "--json 可用" || bad "--json 失败"
grep -q '"mode"' /tmp/m6-json.txt && grep -q '"managed_lines"' /tmp/m6-json.txt \
  && ok "JSON 里有 mode 与 managed_lines" || bad "JSON 字段不全: $(cat /tmp/m6-json.txt)"

# ---- 7. 收尾：还原 -------------------------------------------------------------

section "7. 收尾"
cp -f "$ORIG" "$GAICONF"
[ "$(sum)" = "$ORIG_SUM" ] && ok "已还原成进入时的内容" || bad "还原失败"
"$BIN" priority >/tmp/m6-final.txt 2>&1
note "$(grep gai.conf /tmp/m6-final.txt)"

printf '\n== 结果 ==\n  通过 %d / 失败 %d\n' "$pass" "$fail"
if [ "$fail" != "0" ]; then exit 1; fi
