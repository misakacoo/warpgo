#!/usr/bin/env bash
# M3 验收：全局模式 + fwmark 防环路 + 自动回滚。
#
# ⚠️ 这个脚本会**故意让这台机器失去网络若干秒** —— 那正是它要验证的场景。
# 它靠三层保险保证最后一定恢复：
#     ① warpgo 自己的 rollback-guard（被验证的对象）
#     ② 本脚本的 trap（无论怎么退出都执行 down）
#     ③ 独立的死人开关（由调用方用 setsid 挂，本轮是 6 分钟）
#
# 因此**必须在 setsid 后台运行**，不要挂在前台会话上：全局模式一旦生效，
# 前台 SSH 会断，脚本的进程也会被一并带走，那就永远测不到后面几步了。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=${IFACE:-warp}
TABLE=51820
RUNDIR=/run/warpgo

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }

pass=0
fail=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
section() { printf '\n== %s ==\n' "$1"; }
note() { printf '  · %s\n' "$1"; }

cleanup() {
  "$BIN" down >/dev/null 2>&1
  ip link del "$IFACE" >/dev/null 2>&1
  for p in 9000 9010 9011 9020; do
    ip -4 rule del pref $p 2>/dev/null
    ip -6 rule del pref $p 2>/dev/null
  done
  rm -f "$RUNDIR/rollback.confirm" "$RUNDIR/rollback.json"
}
trap cleanup EXIT

net_ok() { timeout 8 curl -4 -ksm6 https://1.1.1.1/cdn-cgi/trace >/dev/null 2>&1; }
wait_net() {
  local n=${1:-60} i=0
  while [ "$i" -lt "$n" ]; do
    net_ok && return 0
    sleep 1; i=$((i + 1))
  done
  return 1
}
trace4() { timeout 15 curl -4 -ksm10 https://1.1.1.1/cdn-cgi/trace 2>/dev/null | grep -E '^(ip|warp)=' | tr '\n' ' '; }
default_is_native() {
  local d; d=$(ip route show default)
  [ -n "$d" ] && ! printf '%s' "$d" | grep -q " dev $IFACE"
}
our_rules() { ip -4 rule show | grep -cE '^(9000|9010|9011|9020):'; }

section "0. 环境"
printf '  二进制: %s\n' "$("$BIN" version 2>&1 | head -1)"
"$BIN" down >/dev/null 2>&1
ip link del "$IFACE" >/dev/null 2>&1
sleep 1
if wait_net 30; then ok "基线：网络可用"; else bad "基线就没网，后面的结论不可信"; exit 1; fi
note "基线出口: $(trace4)"

section "1. 全局模式（正常端点）"
up_out=$(timeout 150 "$BIN" up --mode global 2>&1); rc=$?
printf '%s\n' "$up_out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "up --mode global 退出码 0" || bad "up --mode global 退出码 $rc"

ip link show "$IFACE" >/dev/null 2>&1 && ok "接口已创建" || bad "接口没起来"

# ★ 全局模式的定义：**不绑定接口**的流量也走 WARP。
out=$(trace4)
case "$out" in
  *warp=on* | *warp=plus*) ok "不绑定接口也走 WARP —— 全局模式生效 [$out]" ;;
  *) bad "不绑定接口时出口不是 WARP（全局没生效）[$out]" ;;
esac

# fwmark 防环路：规则里有它，接口上也真的设了。
ip -4 rule show | grep -q 'fwmark 0xca6c' && ok "IPv4 有 not fwmark 规则" || bad "IPv4 缺 not fwmark 规则"
ip -6 rule show | grep -q 'fwmark 0xca6c' && ok "IPv6 有 not fwmark 规则" || bad "IPv6 缺 not fwmark 规则"
if wg show "$IFACE" 2>/dev/null | grep -q 'fwmark'; then
  ok "接口上确实设了 fwmark（$(wg show "$IFACE" | awk '/fwmark/{print $2}')）"
else
  bad "接口上没有设 fwmark —— 封装包会被自己的规则再送进隧道"
fi

# 全局模式靠策略规则，不该去动默认路由。
default_is_native && ok "默认路由仍是原生的（全局靠规则而非改路由）" || bad "默认路由被改了"
ip -4 rule show | sed 's/^/    /'

# 自检通过 → 已自动确认 → 快照应被清掉
[ -f "$RUNDIR/rollback.json" ] && bad "快照仍在：up 没有走到确认那一步" || ok "已自动确认（快照已清除、保险丝解除）"

note "此刻 SSH 仍然可用 —— 脚本本身还在往下跑就是证据"

section "2. 退出全局模式"
"$BIN" down >/dev/null 2>&1
sleep 1
if wait_net 30; then ok "down 之后网络可用"; else bad "down 之后没网"; fi
out=$(trace4)
case "$out" in
  *warp=off*) ok "回到原生出口 [$out]" ;;
  *) bad "出口异常 [$out]" ;;
esac
default_is_native && ok "默认路由仍是原生" || bad "默认路由异常"

section "3. 坏端点 → 自检失败 → 立即回滚"
# 192.0.2.0/24 是 TEST-NET-1，保证不可达：隧道建起来但永远握不上手。
note "接下来约 30 秒本机将没有网络（这正是被验证的场景）"
bad_start=$(date +%s)
timeout 200 "$BIN" up --mode global --endpoint 192.0.2.1:2408 --guard-timeout 120s >/tmp/m3-selfcheck.log 2>&1
rc=$?
bad_elapsed=$(( $(date +%s) - bad_start ))
printf '%s\n' "$(tail -20 /tmp/m3-selfcheck.log)" | sed 's/^/    /'
note "耗时 ${bad_elapsed}s"

[ "$rc" -eq 2 ] && ok "退出码 2（体检不健康）—— 没有假装成功" || bad "退出码 $rc（期望 2）"
# 断言同时认中英两种措辞：验收脚本不该假设被测程序的输出语言。
grep -qE "自检|self-check" /tmp/m3-selfcheck.log && ok "日志说明了这是自检未通过" || bad "日志没说清失败原因"

# 关键：自检失败要**立即**回滚，而不是等保险丝超时（45s）。
[ "$bad_elapsed" -lt 60 ] && ok "远早于保险丝超时（120s）就主动回滚了：耗时 ${bad_elapsed}s" \
                          || bad "回滚太慢（${bad_elapsed}s）—— 说明走的是保险丝超时而非主动回滚"

if wait_net 60; then ok "网络已恢复"; else bad "网络没恢复！"; fi
ip link show "$IFACE" >/dev/null 2>&1 && bad "接口残留" || ok "接口已清理"
[ "$(our_rules)" = 0 ] && ok "我们加的规则已清空" || bad "规则残留 $(our_rules) 条"
default_is_native && ok "默认路由仍是原生" || bad "默认路由异常"

section "4. 命令中断场景：保险丝超时自动回滚"
# 这是保险丝存在的**唯一理由**：主流程没机会确认（会话断了、进程被杀了……），
# 保险丝必须在若干秒后无条件把网络救回来。
# --no-confirm 让我们能精确地模拟这种"没人确认"的状态。
rm -f "$RUNDIR/guard.log"
note "接下来约 25 秒本机将没有网络，然后由保险丝自动恢复"
timeout 60 "$BIN" up --mode global --endpoint 192.0.2.1:2408 --no-confirm --guard-timeout 25s >/tmp/m3-noconfirm.log 2>&1
rc=$?
printf '%s\n' "$(tail -8 /tmp/m3-noconfirm.log)" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "--no-confirm 下 up 正常返回（配置已应用，未确认）" || bad "退出码 $rc"

# 确认保险丝进程真的起来了（独立于我们的会话）
sleep 2
if pgrep -f "rollback-guard" >/dev/null; then
  ok "保险丝进程在运行（PID $(pgrep -f rollback-guard | head -1)）"
else
  bad "保险丝进程没起来 —— 这种情况下断网就真的救不回来了"
fi

net_ok && note "（此刻仍能出网：坏端点也可能短暂可用，不影响后续判定）" || ok "此刻确实不通（符合预期）"

note "等待保险丝超时（25s）…"
if wait_net 60; then
  ok "保险丝超时后网络自动恢复"
else
  bad "保险丝没有把网络救回来！"
fi

# 日志要留下"它回滚了"的证据
if [ -f "$RUNDIR/guard.log" ]; then
  ok "保险丝写了日志: $RUNDIR/guard.log"
  tail -12 "$RUNDIR/guard.log" | sed 's/^/      /'
  grep -qE "回滚|rollback" "$RUNDIR/guard.log" && ok "日志里有回滚记录" || bad "日志里没有回滚记录"
else
  bad "保险丝没有留下日志"
fi

ip link show "$IFACE" >/dev/null 2>&1 && bad "接口残留" || ok "接口已清理"
[ "$(our_rules)" = 0 ] && ok "我们加的规则已清空" || bad "规则残留 $(our_rules) 条"
default_is_native && ok "默认路由仍是原生" || bad "默认路由异常"

section "5. 收尾"
"$BIN" down >/dev/null 2>&1
sleep 1
wait_net 30 && ok "最终：网络可用" || bad "最终没网"
out=$(trace4)
printf '  最终出口: %s\n' "$out"
[ "$(our_rules)" = 0 ] && ok "最终现场干净" || bad "最终仍有 $(our_rules) 条规则残留"
if ip -4 rule show | grep -qE '^(0|32766|32767):'; then
  ok "系统自带的规则原封不动"
else
  bad "系统规则被破坏了"
fi

printf '\n通过 %d 项，失败 %d 项\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
