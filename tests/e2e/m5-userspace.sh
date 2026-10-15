#!/usr/bin/env bash
# M5 验收：用户态后端（带 reserved）+ rotate 换 IP。
#
# 这个脚本会**真的在两套后端之间来回切换**、**真的重新注册一个新账户**
# （所以这台机器的 WARP 出口地址会变，旧设备会被从服务端注销）。
# 三重兜底保证收尾可用：
#     ① 自己的 trap：无论怎么退出，都清掉现场并重新把隧道拉起来
#     ② 每一步都记录"改之前是什么"，收尾时逐一核对
#     ③ 调用方挂的死人开关
#
# 建议 setsid 后台跑：切换后端会让隧道短暂中断（非全局模式下不影响 SSH）。
#
# # 为什么用抓包当硬判据
#
# Cloudflare 的服务端会**把 reserved 原样盖回它的响应上**（响应变成 `02 RR RR RR`），
# 而 WireGuard 是按 4 字节小端读报文类型的（0xRRRRRR02 ≠ 2）—— 所以**收也要抹掉
# 那 3 个字节**。只做发送方向的表现是"包发出去了、服务端也回了、握手永远完不成、
# 且没有任何报错"，只有抓包看得出来。因此：
#
#   · 用户态后端：出网握手必须是 `01 + reserved`，入网响应必须是 `02 + reserved`
#   · 内核后端：  出网握手必须是 `01 00 00 00`（内核结构上带不了那 3 个字节）
#
# 两个方向都抓，才能证明"那 3 个字节确实是我们按账户写进去的"，而不是巧合。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=${IFACE:-warp}
MTU=${MTU:-1280}
TABLE=51820
RUNDIR=${WARPGO_RUN_DIR:-/run/warpgo}
UAPISOCK=/var/run/wireguard/$IFACE.sock
ACCT=$ETC/account.json
ACCT_BAK=$ETC/account.json.m5-bak

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }
[ -f "$ACCT" ] || { echo "没有账户文件 $ACCT —— 请先 $BIN register"; exit 1; }
command -v tcpdump >/dev/null || { echo "需要 tcpdump"; exit 1; }

pass=0
fail=0
ok()      { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()     { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note()    { printf '  · %s\n' "$1"; }
section() { printf '\n== %s ==\n' "$1"; }

# ---- 现场判据 ------------------------------------------------------------------

# 接口创建时间：判断"接口有没有被重建"的硬判据
# （`wg show latest-handshakes` 会随正常 re-handshake 自然推进，不能单独当判据）。
iface_ctime() { stat -c %Y "/sys/class/net/$IFACE" 2>/dev/null || echo 0; }
daemon_pid() {
  [ -f "$RUNDIR/userspace-$IFACE.json" ] || { echo 0; return; }
  python3 -c "import json;print(json.load(open('$RUNDIR/userspace-$IFACE.json'))['pid'])" 2>/dev/null || echo 0
}
daemon_alive() { local p; p=$(daemon_pid); if [ "$p" != "0" ] && [ -d "/proc/$p" ]; then echo yes; else echo no; fi; }
acct_reserved() { python3 -c "import json;print(','.join(str(x) for x in json.load(open('$ACCT')).get('reserved',[0,0,0])))" 2>/dev/null; }
runtime_backend() { python3 -c "import json;print(json.load(open('$ETC/runtime.json'))['backend'])" 2>/dev/null || echo "-"; }
our_rules() { ip -4 rule show | grep -cE '^(9000|9010|9011|9020):'; }
table_routes() { { ip -4 route show table $TABLE 2>/dev/null; ip -6 route show table $TABLE 2>/dev/null; } | wc -l; }
link_exists() { if [ -e "/sys/class/net/$IFACE" ]; then echo yes; else echo no; fi; }
trace4() { timeout 15 curl -4 -ksm12 --interface $IFACE https://1.1.1.1/cdn-cgi/trace 2>/dev/null | grep -E '^(ip|warp|loc)=' | tr '\n' ' '; }
trace6() { timeout 15 curl -6 -ksm12 --interface $IFACE "https://[2606:4700:4700::1111]/cdn-cgi/trace" 2>/dev/null | grep -E '^(ip|warp|loc)=' | tr '\n' ' '; }
net_ok() { timeout 8 curl -4 -ksm6 https://1.1.1.1/cdn-cgi/trace >/dev/null 2>&1; }
wait_handshake() {
  local n=${1:-20} i=0 hs
  while [ "$i" -lt "$n" ]; do
    hs=$(wg show $IFACE latest-handshakes 2>/dev/null | awk '{print $2}' | head -1)
    [ "${hs:-0}" != "0" ] && return 0
    sleep 1; i=$((i + 1))
  done
  return 1
}

# 取出 dump 里某个方向上**每一个**报文的类型字段（"01a5 fe00" 这样的形式）。
# 每个出网/入网报文各贡献一行，用于断言"是不是所有报文都带着同一组字节"。
type_fields() { # $1=dump 文件  $2=方向标记
  grep -A2 -- "$2" "$1" | sed -n 's/.*0x0010:  //p' | cut -c1-39 | awk '{print $7, $8}'
}

# 抓一次握手（后台跑）。**必须在本 shell 里起**：用 $(...) 起的话 tcpdump 会是
# 子 shell 的子进程，主 shell 的 wait 就等不到它，于是读到一份空的抓包结果。
#
# 用 -c + timeout 双保险：抓够了就自己退出，抓不到也不会把整轮验收拖住。
CAP_PID=0
start_capture() { # $1=dump 文件
  timeout 12 tcpdump -i "$IFACE_DEV" -n -X -c 6 "udp port $PORT_ACTUAL" >"$1" 2>&1 &
  CAP_PID=$!
}

# 强制一次全新握手（内核后端用；用户态后端直接 up --force 重建即可）。
force_kernel_handshake() {
  local pub; pub=$(wg show $IFACE peers)
  wg set $IFACE peer "$pub" remove
  wg set $IFACE peer "$pub" endpoint "$ENDPOINT_ACTUAL" \
     allowed-ips 0.0.0.0/0,::/0 persistent-keepalive 25
}

# reserved 的 "36,212,144" → 类型字段高 3 字节的 tcpdump 写法 "24 d4 90"
hex_suffix() { # $1=reserved
  local r0 r1 r2
  r0=$(echo "$1" | cut -d, -f1); r1=$(echo "$1" | cut -d, -f2); r2=$(echo "$1" | cut -d, -f3)
  printf '%02x %02x%02x' "$r0" "$r1" "$r2"
}

# 断言"某个方向上的每个报文，类型字段的高 3 字节都等于 SUFFIX"。
# $1=抓到的字段清单（每行一个）  $2=期望的 SUFFIX  $3=方向名
# WireGuard 的报文类型只能是 01~04，所以整行必须形如 `0[1-4] + SUFFIX`。
assert_all_type_fields() {
  local fields=$1 suffix=$2 dir=$3 total bad
  total=$(printf '%s\n' "$fields" | grep -c .)
  if [ "$total" = "0" ]; then
    bad "$dir：一个报文都没抓到（抓包窗口内没有任何流量？）"
    return
  fi
  bad=$(printf '%s\n' "$fields" | grep -cvE "^(0[1-4])$suffix\$")
  if [ "$bad" = "0" ]; then
    ok "$dir：$total 个报文的类型字段都带着 $suffix"
  else
    bad "$dir：$total 个报文里有 $bad 个没带 $suffix（$(printf '%s\n' "$fields" | tr '\n' ' ')）"
  fi
}

# ---- 收尾 ---------------------------------------------------------------------

cleanup() {
  printf '\n== 收尾 ==\n'
  # 账户**不还原**：rotate 已经把旧设备从服务端注销了，还原旧账户只会得到一份
  # 指向已删设备的凭据 —— 那样隧道再也起不来。正确的收尾是"用当前账户把隧道建回来"。
  "$BIN" down >/dev/null 2>&1
  ip link del "$IFACE" >/dev/null 2>&1
  rm -f "$ACCT_BAK" >/dev/null 2>&1
  if "$BIN" up --force >/dev/null 2>&1; then
    wait_handshake 20 >/dev/null 2>&1
    printf '  · 隧道已恢复：后端=%s v4=%s v6=%s\n' \
      "$(runtime_backend)" "$(trace4)" "$(trace6)"
  else
    printf '  · \033[31m隧道恢复失败，请手工执行: %s up -v\033[0m\n' "$BIN"
  fi
  printf '  · 结果：%d 通过 / %d 失败\n' "$pass" "$fail"
}
trap cleanup EXIT

# ---- 0. 前置 ------------------------------------------------------------------

section "0. 前置"
ENDPOINT_ACTUAL=$(python3 -c "import json;d=json.load(open('$ACCT'));print(d.get('endpoint_v4') or d.get('endpoint') or '')")
[ -n "$ENDPOINT_ACTUAL" ] || { echo "账户里没有可用端点"; exit 1; }
PORT_ACTUAL=${ENDPOINT_ACTUAL##*:}
DEV=${ENDPOINT_ACTUAL%%:*}
# 只认字面量：端点若是个域名，抓包里就没有这个字面量可匹配。
case "$DEV" in *[a-zA-Z]*) DEV="" ;; esac
IFACE_DEV=$(ip route get 1.1.1.1 2>/dev/null | grep -o 'dev [^ ]*' | awk '{print $2}')
IFACE_DEV=${IFACE_DEV:-eth0}
RESERVED_ORIG=$(acct_reserved)
note "端点 $ENDPOINT_ACTUAL（抓包端口 $PORT_ACTUAL，对端 ${DEV:-未解析}）"
note "抓包网卡 $IFACE_DEV；账户 reserved = $RESERVED_ORIG"
cp -f "$ACCT" "$ACCT_BAK"
note "账户已备份到 $ACCT_BAK（只用于比对，收尾时不还原）"

if ! "$BIN" up --force >/dev/null 2>&1; then
  echo "前置失败：无法建立隧道"; exit 1
fi
wait_handshake 20 || { echo "前置失败：隧道起不来"; exit 1; }
note "起始后端 = $(runtime_backend)，v4: $(trace4)"
net_ok && ok "基线：机器本身能上外网（非全局模式下 SSH 不受影响）" || bad "基线检查失败"

# ---- 1. 用户态后端 -------------------------------------------------------------

section "1. 切到用户态后端（switch --backend userspace）"
if "$BIN" switch --backend userspace >/tmp/m5-switch-us.log 2>&1; then
  ok "switch --backend userspace 成功"
else
  bad "switch 失败"; sed -n '1,40p' /tmp/m5-switch-us.log
fi
[ "$(runtime_backend)" = "userspace" ] && ok "运行态记录 backend=userspace" || bad "运行态 backend=$(runtime_backend)"
[ "$(link_exists)" = "yes" ] && ok "接口 $IFACE 存在" || bad "接口不存在"
[ "$(daemon_alive)" = "yes" ] && ok "用户态守护进程在跑（PID $(daemon_pid)）" || bad "守护进程没在跑"
[ -S "$UAPISOCK" ] && ok "UAPI socket 已建立" || bad "UAPI socket 缺失"
if ip -d link show "$IFACE" 2>/dev/null | grep -q "tun type tun"; then
  ok "接口类型确实是 TUN（用户态 wireguard-go 建的）"
else
  bad "接口不是 TUN —— 可能根本没走用户态后端"
fi
if wait_handshake 30; then ok "用户态后端完成握手"; else bad "用户态后端 30 秒内没有握手"; fi
T4=$(trace4); T6=$(trace6)
case "$T4" in *warp=on*) ok "IPv4 经用户态隧道 warp=on（$T4）" ;; *) bad "IPv4 未通过：${T4:-无输出}" ;; esac
case "$T6" in *warp=on*) ok "IPv6 经用户态隧道 warp=on（$T6）" ;; *) bad "IPv6 未通过：${T6:-无输出}" ;; esac
[ "$(acct_reserved)" = "$RESERVED_ORIG" ] && ok "账户 reserved 未被改动" || bad "账户 reserved 变了"
grep -q "用户态守护进程" /tmp/m5-switch-us.log && ok "日志说明了守护进程的来由" || note "日志里没有守护进程相关提示"

section "1b. 抓包：报文类型字段里必须带上 reserved"
DUMP=/tmp/m5-dump-us.txt
SUF=$(hex_suffix "$RESERVED_ORIG")
start_capture "$DUMP"
sleep 1
"$BIN" up --backend userspace --force >/dev/null 2>&1
wait $CAP_PID
OUT_FIELDS=$(type_fields "$DUMP" "> $DEV.$PORT_ACTUAL:")
IN_FIELDS=$(type_fields "$DUMP" "IP $DEV.$PORT_ACTUAL >")
if [ -n "$DEV" ]; then
  printf '%s\n' "$OUT_FIELDS" | grep -qx "01$SUF" \
    && ok "出网方向出现了握手发起 01$SUF" \
    || bad "出网方向没有 01$SUF 的报文（实得: $(printf '%s\n' "$OUT_FIELDS" | tr '\n' ' '))"
  assert_all_type_fields "$OUT_FIELDS" "$SUF" "出网全部报文"
  printf '%s\n' "$IN_FIELDS" | grep -qx "02$SUF" \
    && ok "服务端响应 02$SUF —— 它也带着 reserved，收包必须抹掉那 3 个字节" \
    || bad "没看到带 reserved 的握手响应（实得: $(printf '%s\n' "$IN_FIELDS" | tr '\n' ' '))"
else
  note "端点是域名，无法按字面量匹配收发方向 —— 跳过抓包断言"
fi
if wait_handshake 30; then ok "强制重建后依然握手成功（收包抹字节这一环是必需的）"; else bad "强制重建后握不上手"; fi
case "$(trace4)" in *warp=on*) ok "抓包轮之后 IPv4 依然 warp=on" ;; *) bad "抓包轮之后 IPv4 不通" ;; esac

section "1c. 幂等：再 up 一次不该重建守护进程"
CT0=$(iface_ctime); DPID0=$(daemon_pid)
"$BIN" up --backend userspace >/tmp/m5-up2.log 2>&1
CT1=$(iface_ctime); DPID1=$(daemon_pid)
[ "$CT0" = "$CT1" ] && ok "接口创建时间未变（没有重建接口）" || bad "接口被重建了（$CT0 → $CT1）"
[ "$DPID0" = "$DPID1" ] && ok "守护进程 PID 未变（$DPID0）" || bad "守护进程被重建了（$DPID0 → $DPID1）"
grep -q '已存在，直接复用' /tmp/m5-up2.log && ok "日志明确说是复用" || bad "日志里没有复用提示"

# ---- 1c-2. 不带 --backend 的 up 要沿用上次的选择 ------------------------------
#
# 这一条挡的是一次很具体的意外：用户用菜单换成"用户态兼容"之后，再点一次
# "建立隧道"，结果被悄悄换回内核 —— 而那个账户恰恰是要 reserved 才通的。
# 判据取的是 `backend_requested`（用户的**要求**），不是 `backend`（实际用上的）：
# 后者可能是 auto 自动回落的结果，拿它当默认会把 auto 永久钉死。
"$BIN" up >/tmp/m5-up-inherit.log 2>&1 && ok "不带 --backend 的 up 成功" || bad "不带 --backend 的 up 失败"
[ "$(runtime_backend)" = "userspace" ] \
  && ok "普通 up 之后后端仍是 userspace（没有悄悄换回内核）" \
  || bad "普通 up 把后端改成了 $(runtime_backend)"
[ "$(python3 -c "import json;print(json.load(open('$ETC/runtime.json')).get('backend_requested',''))" 2>/dev/null)" = "userspace" ] \
  && ok "backend_requested 仍是 userspace（沿用的是用户的要求）" \
  || bad "backend_requested 被改掉了"

section "1d. 切回内核：报文里不该有 reserved"
if "$BIN" switch --backend kernel >/tmp/m5-switch-k.log 2>&1; then
  ok "switch --backend kernel 成功"
else
  bad "切回内核失败"
fi
[ "$(daemon_alive)" = "no" ] && ok "用户态守护进程已停止" || bad "守护进程还在跑（PID $(daemon_pid)）"
[ -e "$UAPISOCK" ] && bad "UAPI socket 残留" || ok "UAPI socket 已清理"
[ -e "$RUNDIR/userspace-$IFACE.json" ] && bad "守护进程状态文件残留" || ok "守护进程状态文件已清理"
if ip -d link show "$IFACE" 2>/dev/null | grep -q wireguard; then
  ok "接口类型是内核 wireguard"
else
  bad "接口不是内核 wireguard"
fi
DUMP=/tmp/m5-dump-k.txt
start_capture "$DUMP"
sleep 1
force_kernel_handshake
wait $CAP_PID
K_FIELDS=$(type_fields "$DUMP" "> $DEV.$PORT_ACTUAL:")
if [ -n "$DEV" ]; then
  printf '%s\n' "$K_FIELDS" | grep -qx "0100 0000" \
    && ok "内核后端出网出现 0100 0000（reserved 发不出去，与设计一致）" \
    || bad "内核后端没抓到 0100 0000 的握手发起（实得: $(printf '%s\n' "$K_FIELDS" | tr '\n' ' '))"
  # 内核后端**所有**报文的类型高位都必须是 0 —— 这才叫"结构上带不了 reserved"。
  assert_all_type_fields "$K_FIELDS" "00 0000" "内核后端出网全部报文"
else
  note "端点是域名，跳过抓包断言"
fi
if wait_handshake 20; then ok "内核后端握手正常"; else bad "内核后端握不上手"; fi
case "$(trace4)" in *warp=on*) ok "切回内核后 IPv4 依然 warp=on" ;; *) bad "切回内核后不通" ;; esac

# ---- 2. auto：内核优先，不通才换用户态 -----------------------------------------

section "2. auto 的策略：内核优先 + 实测验证"
if "$BIN" up --force -v >/tmp/m5-auto.log 2>&1; then ok "auto 建立成功"; else bad "auto 建立失败"; fi
grep -q '后端 内核' /tmp/m5-auto.log && ok "auto 先选内核（没有一上来就退到用户态）" || bad "auto 没有先用内核"
[ "$(runtime_backend)" = "kernel" ] && ok "运行态 backend=kernel" || bad "运行态 backend=$(runtime_backend)"
grep -q '内核后端验证通过' /tmp/m5-auto.log && ok "内核实测验证通过（auto 才会收手）" \
  || note "未匹配到验证提示（-v 下才输出，可能被日志级别过滤）"

section "2b. 端点不通时，是否真的会换后端再试"
if [ -z "$DEV" ]; then
  note "端点是域名，抓包与强制握手都做不了 —— 跳过本段"
else
  PORT_SAVE=$PORT_ACTUAL; PORT_ACTUAL=9999
  timeout 200 "$BIN" up --force --endpoint "$DEV:9999" -v >/tmp/m5-fallback.log 2>&1
  RC=$?
  PORT_ACTUAL=$PORT_SAVE
  [ "$RC" != "0" ] && ok "不通的端点让 up 以非零码结束（退出码 $RC）" || note "端点不通却成功退出，跳过后续断言"
  grep -q '换一个后端再试' /tmp/m5-fallback.log && ok '自动回落被触发（日志里有"换一个后端再试"）' \
    || bad "没有触发自动回落"
  grep -q '所有可用后端都没能让隧道真正通起来' /tmp/m5-fallback.log && ok "两个后端都试过之后给出明确结论" \
    || note "未匹配到总结性提示"
fi
"$BIN" up --force >/dev/null 2>&1
if wait_handshake 20; then ok "恢复默认端点后握手正常"; else bad "恢复端点后握不上手"; fi
case "$(trace4)" in *warp=on*) ok "恢复后 IPv4 warp=on" ;; *) bad "恢复后 IPv4 不通" ;; esac

# ---- 3. rotate 换 IP ----------------------------------------------------------

section "3. rotate：换一个新的 WARP 出口地址"
OLD_ADDRS=$(python3 -c "import json;d=json.load(open('$ACCT'));print(d.get('addr_v4',''),d.get('addr_v6',''))")
OLD_ID=$(python3 -c "import json;print(json.load(open('$ACCT'))['id'])")
note "旧地址 $OLD_ADDRS（设备 $OLD_ID）"
if "$BIN" rotate -v >/tmp/m5-rotate.log 2>&1; then ok "rotate 成功"; else bad "rotate 失败"; tail -20 /tmp/m5-rotate.log; fi
NEW_ADDRS=$(python3 -c "import json;d=json.load(open('$ACCT'));print(d.get('addr_v4',''),d.get('addr_v6',''))")
NEW_ID=$(python3 -c "import json;print(json.load(open('$ACCT'))['id'])")
note "新地址 $NEW_ADDRS（设备 $NEW_ID）"
[ "$OLD_ADDRS" != "$NEW_ADDRS" ] && ok "出口地址确实变了" || bad "出口地址没有变化"
[ "$OLD_ID" != "$NEW_ID" ] && ok "账户换成了新设备" || bad "设备 ID 没有变"
grep -q '已从服务端注销旧设备' /tmp/m5-rotate.log && ok "旧设备已从服务端注销（不会越积越多）" \
  || note "旧设备注销的提示没出现（不影响使用）"
# 出口 IP 是否变化由 PoP 决定，不当作通过条件；但 rotate 必须如实说清楚。
if grep -q '公网出口 IP 已变化' /tmp/m5-rotate.log; then
  ok "rotate 如实汇报了公网出口 IP 的变化: $(grep '公网出口 IP 已变化' /tmp/m5-rotate.log | head -1 | sed 's/.*INFO *//')"
elif grep -q '出口 IP 仍然是' /tmp/m5-rotate.log; then
  ok "rotate 如实汇报了出口 IP 未变（PoP 共享出口，账户与隧道地址确已更换）"
else
  note "rotate 没有给出出口 IP 的比较结果（隧道未运行或两次探测有一侧失败）"
fi
if wait_handshake 30; then ok "换 IP 后完成握手"; else bad "换 IP 后握不上手"; fi
case "$(trace4)" in *warp=on*) ok "换 IP 后 IPv4 warp=on（$(trace4)）" ;; *) bad "换 IP 后 IPv4 不通" ;; esac
case "$(trace6)" in *warp=on*) ok "换 IP 后 IPv6 warp=on" ;; *) bad "换 IP 后 IPv6 不通" ;; esac
NEW_RESERVED=$(acct_reserved)
[ -n "$NEW_RESERVED" ] && ok "新账户的 reserved = $NEW_RESERVED" || bad "读不出新账户的 reserved"

section "3b. 换 IP 之后，用户态后端同样可用"
if "$BIN" switch --backend userspace >/tmp/m5-after-rotate.log 2>&1; then
  if wait_handshake 30; then ok "换 IP 后用户态后端也能握手"; else bad "换 IP 后用户态后端握不上手"; fi
  case "$(trace4)" in *warp=on*) ok "换 IP + 用户态：IPv4 warp=on" ;; *) bad "换 IP + 用户态：IPv4 不通" ;; esac
  if [ "$NEW_RESERVED" != "$RESERVED_ORIG" ]; then
    note "reserved 从 $RESERVED_ORIG 变成了 $NEW_RESERVED —— 守护进程必须跟着重启，这一步正好验到了"
    grep -A2 '"reserved"' "$RUNDIR/userspace-$IFACE.json" | grep -q "$(echo "$NEW_RESERVED" | cut -d, -f1)" \
      && ok "新守护进程记录了新的 reserved" || note "守护进程状态文件里没读到新 reserved"
  fi
else
  bad "换 IP 后切用户态失败"
fi

# ---- 4. down 零残留 -----------------------------------------------------------

section "4. down：两种后端都要零残留"
for be in userspace kernel; do
  # 先把后端钉到本轮的这一种 —— 用它起、再用 down 拆，才真的验到了这条路径。
  "$BIN" up --backend "$be" --force >/dev/null 2>&1
  wait_handshake 20 >/dev/null 2>&1
  [ "$(runtime_backend)" = "$be" ] && ok "[$be] 已按该后端建立" || bad "[$be] 没能切换到该后端（实得 $(runtime_backend)）"
  "$BIN" down >/tmp/m5-down-$be.log 2>&1
  [ "$(link_exists)" = "no" ] && ok "[$be] 接口已删除" || bad "[$be] 接口还在"
  [ "$(our_rules)" = "0" ] && ok "[$be] 策略规则零残留" || bad "[$be] 还剩 $(our_rules) 条规则"
  [ "$(table_routes)" = "0" ] && ok "[$be] 表 $TABLE 零残留" || bad "[$be] 表里还剩 $(table_routes) 条路由"
  [ "$(daemon_alive)" = "no" ] && ok "[$be] 守护进程已退出" || bad "[$be] 守护进程还在（PID $(daemon_pid)）"
  [ -e "$RUNDIR/userspace-$IFACE.json" ] && bad "[$be] 守护进程状态文件残留" || ok "[$be] 守护进程状态文件已清理"
  [ -e "$UAPISOCK" ] && bad "[$be] UAPI socket 残留" || ok "[$be] UAPI socket 已清理"
  [ -e "$ETC/runtime.json" ] && bad "[$be] 运行态残留" || ok "[$be] 运行态已清理"
  net_ok && ok "[$be] 系统本身仍能上外网" || bad "[$be] 系统网络被破坏了"
done
"$BIN" up >/dev/null 2>&1 || true
wait_handshake 20 >/dev/null 2>&1 || true

# ---- 5. 账户面板 ---------------------------------------------------------------

section "5. 账户面板是纯本地的（账户管理确实已砍掉）"
if timeout 10 "$BIN" account --json >/tmp/m5-acct.json 2>/tmp/m5-acct.err; then
  ok "account --json 成功"
else
  bad "account --json 失败: $(cat /tmp/m5-acct.err)"
fi
grep -q '"reserved"' /tmp/m5-acct.json && ok "账户里能看到 reserved" || bad "账户里没有 reserved"
grep -q '"private_key"' /tmp/m5-acct.json && bad "账户面板泄露了私钥！" || ok "账户面板不含私钥"
if "$BIN" account --refresh >/dev/null 2>&1; then
  bad "--refresh 居然还被接受（账户管理本该砍掉）"
else
  ok "--refresh 已被移除"
fi
if "$BIN" account --license ABC >/dev/null 2>&1; then
  bad "--license 居然还被接受（WARP+ 绑定本该砍掉）"
else
  ok "--license 已被移除"
fi

printf '\n== 结果 ==\n  通过 %d / 失败 %d\n' "$pass" "$fail"
if [ "$fail" != "0" ]; then exit 1; fi
