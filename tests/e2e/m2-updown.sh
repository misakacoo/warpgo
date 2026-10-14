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

printf '\n通过 %d 项，失败 %d 项\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
