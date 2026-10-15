#!/usr/bin/env bash
# M2 验收：非全局出口、幂等、零残留、SSH 安全。
#
# 这个脚本会在真实机器上改路由与策略规则 —— 但**全程不碰默认路由**，
# 因为非全局模式的规则只匹配「以 WARP 地址为源」的流量。这既是它的定义，
# 也是它在服务器上安全的原因。
#
# 判据纪律（本项目用血换来的，见 docs/DESIGN.md §12）：
#   · 「接口是否被重建」用 /sys/class/net/<if> 的 mtime，不用握手时间戳
#     （后者会随正常 re-handshake 自然推进）
#   · 每条断言前后都确认默认路由仍是原生；不是就把这条作废
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=${IFACE:-warp}
TABLE=51820

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }

pass=0
fail=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
section() { printf '\n== %s ==\n' "$1"; }

cleanup() {
  # 无论怎么退出，都不把机器留在一个"改过路由"的状态里。
  "$BIN" down >/dev/null 2>&1
  ip link del "$IFACE" >/dev/null 2>&1
  for p in 9000 9010 9011; do
    ip -4 rule del pref $p 2>/dev/null
    ip -6 rule del pref $p 2>/dev/null
  done
}
trap cleanup EXIT

native_default() { ip route show default | grep -v " dev $IFACE" | head -1; }
default_is_native() {
  local d; d=$(ip route show default)
  [ -n "$d" ] && ! printf '%s' "$d" | grep -q " dev $IFACE"
}
rule_count() { ip -4 rule show | wc -l; }
has_rule() { ip -"$1" rule show | grep -qE "^$2:"; }
iface_mtime() { stat -c %Y "/sys/class/net/$IFACE" 2>/dev/null || echo none; }

section "0. 环境与基线"
printf '  二进制: %s\n' "$("$BIN" version 2>&1 | head -1)"
printf '  接口: %s   表: %d\n' "$IFACE" "$TABLE"

"$BIN" down >/dev/null 2>&1
ip link del "$IFACE" >/dev/null 2>&1
ip link show "$IFACE" >/dev/null 2>&1 && { bad "接口 $IFACE 清理不掉，无法开始"; exit 1; }
ok "现场干净"

[ -f "$ETC/account.json" ] && ok "账户存在（M1 注册的）" || { bad "缺账户，先跑 warpgo register"; exit 1; }

RESOLV_BEFORE=$(sha256sum /etc/resolv.conf 2>/dev/null | cut -d' ' -f1)
DEFAULT_BEFORE=$(native_default)
printf '  原生默认路由: %s\n' "$DEFAULT_BEFORE"
[ -n "$DEFAULT_BEFORE" ] && ok "基线：默认路由是原生的" || bad "基线异常：找不到原生默认路由"

# 端点要尽量用字面量（不查 DNS）。M1 注册时账户里还没有这两个字段，
# 这里刷新一次把它补上 —— 顺便验证 refresh 路径可用。
need_refresh=$(python3 -c "
import json
try:
    d=json.load(open('$ETC/account.json'))
    print(0 if d.get('endpoint_v4') else 1)
except Exception:
    print(1)
")
if [ "$need_refresh" = 1 ]; then
  printf '  账户里没有端点字面量，执行一次 account --refresh …\n'
  "$BIN" account --refresh >/tmp/m2-refresh.log 2>&1
  grep -q "endpoint_v4" "$ETC/account.json" 2>/dev/null || \
    printf '  （refresh 未补上字面量，将回落到域名端点）\n'
fi
ep4=$(python3 -c "import json;print(json.load(open('$ETC/account.json')).get('endpoint_v4',''))")
[ -n "$ep4" ] && ok "端点字面量可用: $ep4（不需要 DNS）" || printf '  （无 IPv4 端点字面量，将用域名）\n'

section "1. 建立隧道"
up_out=$("$BIN" up 2>&1)
rc=$?
printf '%s\n' "$up_out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "up 退出码 0" || bad "up 退出码 $rc"

ip link show "$IFACE" >/dev/null 2>&1 && ok "接口 $IFACE 已创建" || bad "接口没起来"

# ★ 最要紧的一条：默认路由必须还是原生的。
if default_is_native; then
  ok "默认路由仍是原生（非全局模式的核心保证，SSH 安全）"
else
  bad "默认路由被改到 $IFACE —— 非全局模式失效！"
  ip route show default | sed 's/^/    /'
fi

n4=$(ip -4 route show table $TABLE 2>/dev/null | wc -l)
n6=$(ip -6 route show table $TABLE 2>/dev/null | wc -l)
[ "$n4" -ge 1 ] && ok "表 $TABLE 有 $n4 条 IPv4 路由" || bad "表 $TABLE 没有 IPv4 路由"
[ "$n6" -ge 1 ] && ok "表 $TABLE 有 $n6 条 IPv6 路由" || bad "表 $TABLE 没有 IPv6 路由"
ip -4 route show table $TABLE 2>/dev/null | sed 's/^/    v4 /'
ip -6 route show table $TABLE 2>/dev/null | sed 's/^/    v6 /'

# pref 是按地址族独立编号的，所以两族用的号不同：
#   9000 = 让 main 表里的具体路由优先（两族都有）
#   9010 = IPv4 的「from WARP 地址」；9011 = IPv6 的（v4 上不该出现 9011）
for p in 9000 9010; do
  has_rule 4 "$p" && ok "IPv4 规则 pref $p 已就位" || bad "缺 IPv4 规则 pref $p"
done
for p in 9000 9011; do
  has_rule 6 "$p" && ok "IPv6 规则 pref $p 已就位" || bad "缺 IPv6 规则 pref $p"
done
has_rule 4 9011 && bad "IPv4 上出现了 IPv6 专用的 pref 9011"
has_rule 6 9010 && bad "IPv6 上出现了 IPv4 专用的 pref 9010"
printf '  ---- ip -4 rule ----\n'
ip -4 rule show | sed 's/^/    /'
printf '  ---- ip -6 rule ----\n'
ip -6 rule show | sed 's/^/    /'

[ -f "$ETC/runtime.json" ] && ok "运行态已保存" || bad "运行态文件没生成"
printf '  ---- runtime.json ----\n'
sed 's/^/    /' "$ETC/runtime.json" 2>/dev/null

section "2. 出口验证（非全局语义）"
probe_iface() {
  local fam=$1 url=$2 label=$3
  local out
  out=$(timeout 25 curl -"$fam" -ksm20 --interface "$IFACE" "$url" 2>/dev/null | grep -E '^(ip|warp|loc)=' | tr '\n' ' ')
  if printf '%s' "$out" | grep -qE 'warp=(on|plus)'; then
    ok "$label：绑定接口后经隧道出口 [$out]"
  else
    bad "$label：绑定接口后没拿到 warp=on（$out）"
  fi
}
probe_native() {
  local fam=$1 url=$2 label=$3
  local out
  out=$(timeout 20 curl -"$fam" -ksm15 "$url" 2>/dev/null | grep -E '^(ip|warp)=' | tr '\n' ' ')
  if printf '%s' "$out" | grep -q 'warp=off'; then
    ok "$label：不绑定接口时走原生（warp=off）[$out]"
  else
    bad "$label：不绑定接口时出口不是原生（$out）"
  fi
}

has4=$(python3 -c "import json;print(1 if json.load(open('$ETC/account.json')).get('addr_v4') else 0)")
has6=$(python3 -c "import json;print(1 if json.load(open('$ETC/account.json')).get('addr_v6') else 0)")
[ "$has4" = 1 ] && probe_iface 4 "https://1.1.1.1/cdn-cgi/trace" "IPv4 经隧道"
[ "$has6" = 1 ] && probe_iface 6 "https://[2606:4700:4700::1111]/cdn-cgi/trace" "IPv6 经隧道"

# ★ 这一条才是"非全局"的定义：本机自己的流量仍然走原生出口。
[ "$has4" = 1 ] && probe_native 4 "https://1.1.1.1/cdn-cgi/trace" "IPv4"

section "3. 幂等（up 连跑 3 次）"
MTIME_BEFORE=$(iface_mtime)
RULES_BEFORE=$(rule_count)
printf '  接口创建时间=%s  规则数=%s\n' "$MTIME_BEFORE" "$RULES_BEFORE"

for i in 1 2 3; do
  out=$("$BIN" up 2>&1); rc=$?
  if [ "$rc" -ne 0 ]; then
    bad "第 $i 次 up 失败（rc=$rc）"; printf '%s\n' "$out" | sed 's/^/    /'
  fi
done
[ "$rc" -eq 0 ] && ok "连跑 3 次 up 都成功"

MTIME_AFTER=$(iface_mtime)
RULES_AFTER=$(rule_count)
printf '  接口创建时间=%s  规则数=%s\n' "$MTIME_AFTER" "$RULES_AFTER"

[ "$MTIME_BEFORE" = "$MTIME_AFTER" ] \
  && ok "接口未被重建（创建时间不变 —— 出口 IP 不会变）" \
  || bad "接口被重建了！创建时间 $MTIME_BEFORE → $MTIME_AFTER"
[ "$RULES_BEFORE" = "$RULES_AFTER" ] \
  && ok "规则数不变（$RULES_AFTER 条）—— 没有累积残留" \
  || bad "规则数变了：$RULES_BEFORE → $RULES_AFTER（先清后加没生效）"

default_is_native && ok "默认路由仍是原生" || bad "默认路由被动过"

# 复用时应当明确说出来，而不是默默重做一遍。
case "$out" in
  *"复用"* | *"reusing"*) ok "输出明确说明「接口已存在，直接复用」" ;;
  *) bad "复用时没有明确说明（用户最关心这次有没有换 IP）" ;;
esac

section "4. status"
st_out=$("$BIN" status 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "status 退出码 0" || bad "status 退出码 $rc"
printf '%s\n' "$st_out" | sed 's/^/    /'
case "$st_out" in *"$IFACE"*) ok "status 里报出了接口名" ;; *) bad "status 没报接口名" ;; esac

st_json=$("$BIN" status --json 2>&1)
printf '%s' "$st_json" | python3 -c "
import json,sys
d=json.load(sys.stdin)
assert d['if_name']=='$IFACE', d
assert d['interface_exists'] is True, d
assert d['mode']=='non-global', d
print('  ✓ --json 输出合法且字段正确')
" 2>/dev/null || bad "--json 输出有问题"

section "5. 拆除与零残留"
down_out=$("$BIN" down 2>&1); rc=$?
printf '%s\n' "$down_out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "down 退出码 0" || bad "down 退出码 $rc"

ip link show "$IFACE" >/dev/null 2>&1 && bad "接口仍然存在" || ok "接口已删除"

left=0
for p in 9000 9010 9011; do
  has_rule 4 "$p" && { bad "IPv4 规则 pref $p 残留"; left=$((left+1)); }
  has_rule 6 "$p" && { bad "IPv6 规则 pref $p 残留"; left=$((left+1)); }
done
[ "$left" = 0 ] && ok "我们创建的规则一条不剩（IPv4 + IPv6）"

n4=$(ip -4 route show table $TABLE 2>/dev/null | wc -l)
n6=$(ip -6 route show table $TABLE 2>/dev/null | wc -l)
[ "$n4" = 0 ] && [ "$n6" = 0 ] && ok "表 $TABLE 已清空" || bad "表 $TABLE 残留 v4=$n4 v6=$n6"

[ -f "$ETC/runtime.json" ] && bad "运行态文件仍在" || ok "运行态文件已删除"

# 系统自带的规则必须原封不动（0/32766/32767）。
ip -4 rule show | sed 's/^/    /'
ip -4 rule show | grep -qE "^0:" && ok "系统规则 local 还在" || bad "系统规则被误删"
ip -4 rule show | grep -qE "^32766:" && ok "系统规则 main 还在" || bad "系统规则被误删"

default_is_native && ok "默认路由仍是原生" || bad "默认路由被改动"

RESOLV_AFTER=$(sha256sum /etc/resolv.conf 2>/dev/null | cut -d' ' -f1)
[ "$RESOLV_BEFORE" = "$RESOLV_AFTER" ] && ok "/etc/resolv.conf 未被触碰" || bad "/etc/resolv.conf 被改动了"

section "6. down 幂等 + 可恢复"
out=$("$BIN" down 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "重复 down 退出码 0" || bad "重复 down 退出码 $rc"
case "$out" in
  *"没有需要拆除"* | *"nothing to tear down"*) ok "明确说明「没有需要拆除的东西」" ;;
  *) bad "重复 down 没说明现场已空" ;;
esac

out=$("$BIN" up 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "down 之后还能重新 up（$rc=0）" || bad "重新 up 失败（rc=$rc）"
default_is_native && ok "重新 up 后默认路由仍是原生" || bad "重新 up 后默认路由异常"
if [ "$has4" = 1 ]; then
  probe_iface 4 "https://1.1.1.1/cdn-cgi/trace" "重新 up 后 IPv4 经隧道"
fi

section "7. off / on（临时开关，菜单 o）"

RUNTIME=$ETC/runtime.json
"$BIN" up >/dev/null 2>&1
[ -f "$RUNTIME" ] && ok "up 之后有运行态文件" || bad "没有运行态文件"

out=$("$BIN" off 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "off 退出码 0" || bad "off 退出码 $rc"
case "$out" in
  *"已保留运行态"*) ok "off 明确说明保留了运行态" ;;
  *) bad "off 没说保留运行态" ;;
esac
[ -e "/sys/class/net/$IFACE" ] && bad "off 之后接口还在" || ok "off 之后接口已删除"
has_rule 4 9000 && bad "off 之后 v4 规则还在" || ok "off 之后 v4 规则已清"
has_rule 6 9000 && bad "off 之后 v6 规则还在" || ok "off 之后 v6 规则已清"
default_is_native && ok "off 之后默认路由仍是原生" || bad "off 之后默认路由异常"

# 这一条是 off 与 down 的**唯一**差别，也是最要紧的一条：
# 没有它，on 就只能按账户默认重建，而不是"原样恢复"。
[ -f "$RUNTIME" ] && ok "off **保留**了运行态（这正是它与 down 的差别）" \
  || bad "off 把运行态删了 —— 那就退化成 down 了"

out=$("$BIN" off 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "重复 off 退出码 0（幂等）" || bad "重复 off 退出码 $rc"

out=$("$BIN" on 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "on 退出码 0" || bad "on 退出码 $rc"
[ -e "/sys/class/net/$IFACE" ] && ok "on 之后接口回来了" || bad "on 之后接口没回来"
has_rule 4 9000 && ok "on 之后 v4 规则回来了" || bad "on 之后 v4 规则没回来"
default_is_native && ok "on 之后默认路由仍是原生" || bad "on 之后默认路由异常"
if [ "$has4" = 1 ]; then
  probe_iface 4 "https://1.1.1.1/cdn-cgi/trace" "on 之后 IPv4 经隧道"
fi

# "原样恢复"：模式与后端都该保持 off 之前的值。
if [ -f "$RUNTIME" ]; then
  mode_now=$(python3 -c "import json;print(json.load(open('$RUNTIME'))['mode'])")
  [ "$mode_now" = "non-global" ] && ok "on 之后模式仍是 non-global（按运行态恢复）" \
    || bad "on 之后模式变成了 $mode_now"
  back_now=$(python3 -c "import json;print(json.load(open('$RUNTIME')).get('backend_requested',''))")
  ok "运行态里记的 backend_requested = ${back_now:-（空）}"
fi

# down 与 off 的另一半差别：down 必须把运行态清掉。
"$BIN" down >/dev/null 2>&1
[ -f "$RUNTIME" ] && bad "down 之后运行态还在（down 的语义是零残留）" \
  || ok "down 之后运行态已清掉"

out=$("$BIN" on 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "没有运行态时 on 也能成功（退化成按账户默认建立）" || bad "没有运行态时 on 失败（rc=$rc）"
default_is_native && ok "再次 on 之后默认路由仍是原生" || bad "再次 on 之后默认路由异常"


section "8. --stack（隧道携带哪几族，上游菜单 4/6/d）"

stack_of() { python3 -c "import json;print(json.load(open('$ETC/runtime.json')).get('stack',''))" 2>/dev/null; }
# 只数**全局**地址：v6 上通常还挂着一个链路本地地址，数进去就永远对不上。
v4_addrs() { ip -4 addr show "$IFACE" 2>/dev/null | grep -c "inet "; }
v6_addrs() { ip -6 addr show "$IFACE" 2>/dev/null | grep "inet6 " | grep -c "scope global"; }

if [ "$has4" = 1 ]; then
  out=$("$BIN" up --stack 4 2>&1); rc=$?
  [ "$rc" -eq 0 ] && ok "--stack 4 退出码 0" || bad "--stack 4 失败（rc=$rc）"
  [ "$(v4_addrs)" = "1" ] && ok "只用 IPv4 时接口上只有 v4 地址" || bad "v4 地址数=$(v4_addrs)"
  [ "$(v6_addrs)" = "0" ] && ok "只用 IPv4 时接口上没有 v6 地址" || bad "v6 地址数=$(v6_addrs)"
  has_rule 6 9000 && bad "只用 IPv4 时仍有 v6 规则" || ok "只用 IPv4 时没有 v6 规则"
  has_rule 4 9000 && ok "只用 IPv4 时 v4 规则在" || bad "只用 IPv4 时 v4 规则不在"
  [ "$(stack_of)" = "4" ] && ok "运行态记下了 stack=4" || bad "运行态里的 stack=$(stack_of)"
  default_is_native && ok "只用 IPv4 时默认路由仍是原生" || bad "默认路由异常"
  probe_iface 4 "https://1.1.1.1/cdn-cgi/trace" "只用 IPv4 时经隧道"

  # 重建不该把栈改回双栈 —— 这是最难发现的一类回归
  # （隧道照样通，只是悄悄多了一族）。
  "$BIN" off >/dev/null 2>&1
  "$BIN" on >/dev/null 2>&1
  [ "$(stack_of)" = "4" ] && ok "off/on 一个来回后栈仍是 4" || bad "off/on 之后栈变成了 $(stack_of)"
  [ "$(v6_addrs)" = "0" ] && ok "off/on 之后接口上仍没有 v6 地址" || bad "off/on 之后多出了 v6 地址"

  "$BIN" watch --once >/dev/null 2>&1
  [ "$(stack_of)" = "4" ] && ok "watch 一轮之后栈仍是 4（继承逻辑生效）" || bad "watch 之后栈变成了 $(stack_of)"
fi

if [ "$has6" = 1 ]; then
  "$BIN" up --stack 6 >/dev/null 2>&1
  [ "$(v4_addrs)" = "0" ] && ok "只用 IPv6 时接口上没有 v4 地址" || bad "v4 地址数=$(v4_addrs)"
  [ "$(v6_addrs)" = "1" ] && ok "只用 IPv6 时接口上有 v6 地址" || bad "v6 地址数=$(v6_addrs)"
fi

out=$("$BIN" up --stack d 2>&1)
case "$out" in
  *"栈从"*) ok "切回双栈时日志说明了「栈变了所以要重建」" ;;
  *) bad "切回双栈时没说为什么重建（静默重建会换一次出口 IP）" ;;
esac
[ "$(stack_of)" = "both" ] && ok "运行态记下 stack=both" || bad "运行态里的 stack=$(stack_of)"
if [ "$has4" = 1 ] && [ "$has6" = 1 ]; then
  [ "$(v4_addrs)" = "1" ] && [ "$(v6_addrs)" = "1" ] && ok "双栈时两族地址都在" \
    || bad "双栈地址数 v4=$(v4_addrs) v6=$(v6_addrs)"
fi
default_is_native && ok "双栈时默认路由仍是原生" || bad "默认路由异常"

out=$("$BIN" up --stack 5 2>&1); rc=$?
[ "$rc" != "0" ] && ok "非法的栈取值被拒绝" || bad "非法栈取值居然成功了"
case "$out" in
  *"不认识的栈取值"*) ok "报错说清了取值不合法" ;;
  *) bad "非法取值的报错不清楚" ;;
esac


printf '\n通过 %d 项，失败 %d 项\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
