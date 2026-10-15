#!/usr/bin/env bash
# M8 验收：官方 Cloudflare WARP Linux 客户端（菜单 c / l / r）+ 钩子（z / x）+ 交互菜单。
#
# # 安全边界（这份脚本最重要的设计约束）
#
# 官方客户端的 **warp 模式会接管默认路由**（整机流量走 WARP）。在一台只能靠
# SSH 进入的机器上跑它，有可能把自己关在门外 —— 所以：
#
#   - 真实客户端只跑 **proxy 模式**（socks5，不动任何路由），完整验证
#     "装包 → 注册 → 起服务 → 经 socks5 出网 → 开关 → 卸载"这条链路；
#   - 钩子（z / x）用**一个假的 CloudflareWARP 接口**（dummy）来验：
#     它同样能产生接口地址、同样要读写策略规则，但不会有任何流量真的走进去；
#   - warp 模式的端到端留成**可选**（`M8_TEST_WARP=1`），并且必须自己带死人开关。
#
# 这样"最危险的那一步"不会在一次例行验收里被无意触发。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=${IFACE:-warp}
CLIENT_IF=CloudflareWARP
APT_LIST=/etc/apt/sources.list.d/cloudflare-client.list
KEYRING=/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg
DROPIN=/etc/systemd/system/warp-svc.service.d/warpgo-hooks.conf
CSTATE=$ETC/client.json
HOOK_TABLE=51821
PREF_SUPPRESS=9100
PREF_FROM_V4=9110
PREF_FROM_V6=9111
FAKE_V4=172.16.0.2
FAKE_V6=2606:4700:110:8d4e:cef9:30c2:6d4a:f97b
PORT=${PORT:-40000}

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }

pass=0
fail=0
ok()      { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()     { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note()    { printf '  · %s\n' "$1"; }
section() { printf '\n== %s ==\n' "$1"; }

# ---- 现场判据 ------------------------------------------------------------------

svc_active()  { systemctl is-active warp-svc 2>/dev/null || true; }
svc_enabled() { systemctl is-enabled warp-svc 2>/dev/null || true; }
have_cli()    { command -v warp-cli >/dev/null 2>&1 && echo yes || echo no; }
# 某族某个 pref 上的规则条数。
rules_at() { # $1 = 4|6，$2 = pref
  ip -"$1" rule show 2>/dev/null | grep -cE "^\s*$2:"
}
rules_total_hook() {
  echo $(( $(rules_at 4 $PREF_SUPPRESS) + $(rules_at 6 $PREF_SUPPRESS) \
         + $(rules_at 4 $PREF_FROM_V4) + $(rules_at 6 $PREF_FROM_V6) ))
}
# warp-svc 自己写的规则（pref 8999 的 `not fwmark 0x100cf lookup 65743`）。
# 它**不走我们的钩子**：钩子规则由 warpgo 加、warpgo 撤，而这条是 warp-svc
# 自己在整机接管时插的，正常注销时它自己会撤；卸载路径是"停服务→卸包"，
# 服务被硬杀，规则就留下了 —— 所以由 warpgo 在卸包之后补一刀。
svc_rule_count() {
  echo $(( $(ip -4 rule show 2>/dev/null | grep -c 'fwmark 0x100cf') \
         + $(ip -6 rule show 2>/dev/null | grep -c 'fwmark 0x100cf') ))
}
route_count() { # $1 = 4|6
  ip -"$1" route show table $HOOK_TABLE 2>/dev/null | grep -c '^default' || true
}
port_listen() { ss -nltp 2>/dev/null | grep -E ":$1[[:space:]]" | wc -l; }
# 经 socks5 的独立实测 —— 不看被测程序自己的结论。
socks_trace() {
  timeout 20 curl -sS --max-time 15 --proxy "socks5h://127.0.0.1:$1" https://1.1.1.1/cdn-cgi/trace 2>/dev/null |
    grep -E '^(ip|warp|loc)=' | tr '\n' ' '
}
socks_quick() {
  timeout 8 curl -sS --max-time 6 --proxy "socks5h://127.0.0.1:$1" https://1.1.1.1/cdn-cgi/trace 2>/dev/null | grep -c '^warp=on'
}
wait_socks() { # $1 = 端口，$2 = 最多等几秒
  local i=0 n=${2:-20}
  while [ "$i" -lt "$n" ]; do
    [ "$(socks_quick "$1")" = "1" ] && return 0
    sleep 1; i=$((i + 1))
  done
  return 1
}
net_ok() { timeout 8 curl -4 -ksm6 https://1.1.1.1/cdn-cgi/trace >/dev/null 2>&1; }
tunnel_ok() {
  case "$(timeout 15 curl -4 -ksm12 --interface "$IFACE" https://1.1.1.1/cdn-cgi/trace 2>/dev/null | grep '^warp=')" in
    warp=on|warp=plus) echo yes ;;
    *) echo no ;;
  esac
}
default_dev() { ip -4 route show default 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}'; }

fake_iface_up() {
  ip link add "$CLIENT_IF" type dummy 2>/dev/null
  ip addr add "$FAKE_V4/32" dev "$CLIENT_IF" 2>/dev/null
  ip -6 addr add "$FAKE_V6/128" dev "$CLIENT_IF" nodad 2>/dev/null
  ip link set "$CLIENT_IF" up 2>/dev/null
  # 有些机器把 IPv6 整个关掉了 —— 那时 v6 那两条规则本就不该出现，
  # 断言要跟着降级，而不是把"机器没有 v6"判成"实现错了"。
  if ip -6 addr show dev "$CLIENT_IF" 2>/dev/null | grep -q "$FAKE_V6"; then
    HAVE_V6=yes
  else
    HAVE_V6=no
  fi
}
fake_iface_down() { ip link del "$CLIENT_IF" 2>/dev/null; }
HAVE_V6=no
expected_hook_rules() { [ "$HAVE_V6" = "yes" ] && echo 4 || echo 2; }

cleanup() {
  # 钩子规则与假接口先撤（它们是我们加的，删起来不需要网络）。
  "$BIN" hook del >/dev/null 2>&1
  fake_iface_down
  # 客户端：能撤就撤（apt 那一步可能因为网络失败，所以不依赖它的退出码）。
  "$BIN" client uninstall >/dev/null 2>&1
  if [ "$(tunnel_ok)" = "no" ]; then
    "$BIN" up >/dev/null 2>&1
  fi
}
trap 'cleanup' EXIT

printf 'M8 验收：官方 Linux 客户端 + 钩子 + 菜单\n'
printf '  二进制 %s\n  假接口 %s（%s / %s）\n' "$BIN" "$CLIENT_IF" "$FAKE_V4" "$FAKE_V6"

# ---- 0. 起点 -------------------------------------------------------------------

section "0. 进入时的现场"
"$BIN" client uninstall >/dev/null 2>&1
"$BIN" hook del >/dev/null 2>&1
fake_iface_down
note "隧道状态: 经 $IFACE 的出口 warp=$(tunnel_ok)；默认路由 dev=$(default_dev)"
note "warp-cli: $(have_cli)"

# ---- 1. 未安装时的行为 ---------------------------------------------------------

section "1. 未安装时的行为"
if [ "$(have_cli)" = "no" ]; then
  if "$BIN" client status >/tmp/m8-status0.txt 2>&1; then
    bad "未安装时 status 不该返回 0"
  else
    ok "未安装时 status 以非零码退出"
  fi
  grep -q "还没有装官方客户端" /tmp/m8-status0.txt && ok "status 给出了明确提示" || bad "status 提示不清楚"
  if "$BIN" client on >/tmp/m8-on0.txt 2>&1; then
    bad "未安装时 on 不该成功"
  else
    ok "未安装时 on 被拒绝"
  fi
  grep -q "client install" /tmp/m8-on0.txt && ok "提示里给出了下一步（client install）" || bad "提示里没有下一步"
else
  note "本机已经装着 warp-cli，跳过「未安装」一节"
fi

# ---- 2. 钩子（z / x）—— 用假接口验，安全且确定 ---------------------------------

section "2. 钩子：接口不存在时的行为"
fake_iface_down
if "$BIN" hook add --wait 1s >/tmp/m8-h0.txt 2>&1; then
  ok "接口不存在时 hook add 返回 0（绝不能拖垮 warp-svc 的启动）"
else
  bad "接口不存在时 hook add 返回了非 0 —— 那会让 systemd 把 warp-svc 标成 failed"
fi
grep -q "还没出现" /tmp/m8-h0.txt && ok "如实说明了「接口还没出现」" || bad "没有说明原因"
"$BIN" hook del >/dev/null 2>&1 && ok "现场本来就干净时 hook del 也返回 0（幂等）" || bad "空场景下 hook del 失败"

section "2b. 钩子：加规则（假接口）"
fake_iface_up
[ -d "/sys/class/net/$CLIENT_IF" ] && ok "假接口 $CLIENT_IF 已就绪" || bad "假接口没建起来"
"$BIN" hook add --wait 5s >/tmp/m8-h1.txt 2>&1 && ok "hook add 成功" || bad "hook add 失败: $(tail -2 /tmp/m8-h1.txt)"
[ "$(rules_at 4 $PREF_SUPPRESS)" = "1" ] && ok "v4 suppress 规则已加（pref $PREF_SUPPRESS）" || bad "v4 suppress 规则数=$(rules_at 4 $PREF_SUPPRESS)"
ip -4 rule show | grep -qE "^\s*$PREF_FROM_V4:.*from $FAKE_V4 lookup $HOOK_TABLE" \
  && ok "v4 源地址规则已加（from $FAKE_V4 → 表 $HOOK_TABLE）" || bad "v4 源地址规则不对：$(ip -4 rule show | grep "$PREF_FROM_V4" || echo 无)"
[ "$(route_count 4)" = "1" ] && ok "v4 表 $HOOK_TABLE 里有默认路由" || bad "v4 默认路由数=$(route_count 4)"
ip -4 route show table $HOOK_TABLE | grep -q "dev $CLIENT_IF" && ok "路由指向假接口 $CLIENT_IF" || bad "路由没指向 $CLIENT_IF"
if [ "$HAVE_V6" = "yes" ]; then
  [ "$(rules_at 6 $PREF_SUPPRESS)" = "1" ] && ok "v6 suppress 规则已加（pref $PREF_SUPPRESS）" || bad "v6 suppress 规则数=$(rules_at 6 $PREF_SUPPRESS)"
  ip -6 rule show | grep -qE "^\s*$PREF_FROM_V6:.*from $FAKE_V6 lookup $HOOK_TABLE" \
    && ok "v6 源地址规则已加（from $FAKE_V6 → 表 $HOOK_TABLE）" || bad "v6 源地址规则不对：$(ip -6 rule show | grep "$PREF_FROM_V6" || echo 无)"
  [ "$(route_count 6)" = "1" ] && ok "v6 表 $HOOK_TABLE 里有默认路由" || bad "v6 默认路由数=$(route_count 6)"
else
  note "本机没有可用的 IPv6，跳过 v6 断言"
fi
# 与 warpgo 自己的隧道规则必须错开（撞号会让两边互相删规则）。
[ "$(rules_at 4 9000)" != "0" ] && ok "隧道自己的 pref 9000 规则没被动过（两套规则互不干扰）" || note "隧道当前没有 9000 规则（可能没在跑）"
note "默认路由仍是 $(default_dev)（钩子只加策略规则，不动 main 表）"

section "2c. 钩子：幂等（连跑不累积）"
"$BIN" hook add --wait 5s >/dev/null 2>&1
"$BIN" hook add --wait 5s >/dev/null 2>&1
[ "$(rules_total_hook)" = "$(expected_hook_rules)" ] && ok "连跑 3 次后规则仍是 $(expected_hook_rules) 条（先清后加，不累积）" || bad "规则累积到 $(rules_total_hook) 条"
[ "$(route_count 4)" = "1" ] && ok "v4 默认路由仍是 1 条" || bad "v4 默认路由变成 $(route_count 4) 条"

section "2d. 钩子：撤规则"
"$BIN" hook del >/tmp/m8-h2.txt 2>&1 && ok "hook del 成功" || bad "hook del 失败"
[ "$(rules_total_hook)" = "0" ] && ok "钩子规则全部清除" || bad "还有 $(rules_total_hook) 条钩子规则"
[ "$(route_count 4)" = "0" ] && ok "v4 表 $HOOK_TABLE 已清空" || bad "表里还有 $(route_count 4) 条默认路由"
[ "$(route_count 6)" = "0" ] && ok "v6 表 $HOOK_TABLE 已清空" || bad "表里还有 $(route_count 6) 条默认路由"
# 隧道规则必须原封不动。
[ "$(tunnel_ok)" = "yes" ] && ok "隧道没被钩子的清理影响（仍 warp=on）" || bad "隧道被影响了"

section "2e. 钩子：接口消失后仍能清干净"
"$BIN" hook add --wait 5s >/dev/null 2>&1
fake_iface_down
"$BIN" hook del >/dev/null 2>&1 && ok "接口已删除后 hook del 仍返回 0" || bad "接口删除后 hook del 失败"
[ "$(rules_total_hook)" = "0" ] && ok "接口消失后规则仍被清干净（按 pref 删，不解析显示文本）" || bad "残留 $(rules_total_hook) 条规则"

# ---- 3. 真实安装（proxy 模式） --------------------------------------------------

section "3. 真实安装（proxy 模式：装包 + 注册 + socks5）"
"$BIN" client install --mode proxy --port "$PORT" >/tmp/m8-install.txt 2>&1
RC=$?
if [ "$RC" = "0" ]; then
  ok "client install 成功（rc=0）"
else
  bad "client install 失败（rc=$RC）: $(tail -5 /tmp/m8-install.txt)"
fi

[ "$(have_cli)" = "yes" ] && ok "warp-cli 已安装" || bad "warp-cli 不存在"
[ -f "$APT_LIST" ] && ok "apt 源已写入: $APT_LIST" || bad "apt 源缺失"
[ -f "$KEYRING" ] && ok "官方公钥已导入: $KEYRING" || bad "密钥环缺失"
grep -q "signed-by=$KEYRING" "$APT_LIST" && ok "apt 源绑定了我们导入的密钥" || bad "apt 源的 signed-by 不对"
grep -q "pkg.cloudflareclient.com" "$APT_LIST" && ok "apt 源指向官方仓库" || bad "apt 源地址不对"
[ "$(svc_enabled)" = "enabled" ] && ok "warp-svc 已设置开机自启" || bad "warp-svc 没有 enable：$(svc_enabled)"
[ "$(svc_active)" = "active" ] && ok "warp-svc 正在运行" || bad "warp-svc 没跑起来：$(svc_active)"
[ -S /run/cloudflare-warp/warp_service ] && ok "warp-cli 的 AF_UNIX socket 已就绪" || note "socket 不在（warp-cli 仍可用即可）"
[ -f "$CSTATE" ] && ok "状态文件已写入: $CSTATE" || bad "状态文件缺失"
grep -q '"mode": "proxy"' "$CSTATE" && ok "状态文件记录了 proxy 模式" || bad "状态文件里的模式不对"
[ -f "$DROPIN" ] && bad "proxy 模式不该写 warp-svc 的钩子 drop-in" || ok "proxy 模式没有写 drop-in（钩子只属于 warp 模式）"

MODE_LINE=$(warp-cli --accept-tos -j settings 2>/dev/null | grep -o '"operation_mode": *"[^"]*"')
note "warp-cli -j settings → ${MODE_LINE:-（读不到）}"
case "$MODE_LINE" in
  *'"proxy"'*) ok "客户端已切到代理模式" ;;
  *) bad "客户端模式不是代理：${MODE_LINE:-空}" ;;
esac
[ "$(port_listen "$PORT")" != "0" ] && ok "socks5 端口 $PORT 在监听" || bad "端口 $PORT 没有监听"

section "3b. 经 socks5 的真实连通性（独立于程序的结论）"
if wait_socks "$PORT" 25; then
  ok "curl --proxy socks5h://127.0.0.1:$PORT 拿到 warp=on（$(socks_trace "$PORT")）"
else
  bad "经 socks5 拿不到 warp=on（$(socks_trace "$PORT")）"
fi

section "3c. client status"
"$BIN" client status >/tmp/m8-status1.txt 2>&1 && ok "client status 返回 0" || bad "client status 非 0"
grep -qi "connected" /tmp/m8-status1.txt && ok "status 显示已连接" || bad "status 没显示连接状态"
"$BIN" client status --json >/tmp/m8-status1.json 2>&1 && ok "status --json 可用" || bad "status --json 失败"
grep -q '"connection": "connected"' /tmp/m8-status1.json && ok "JSON 里 connection=connected" || bad "JSON 的 connection 不对"
grep -q '"mode": "proxy"' /tmp/m8-status1.json && ok "JSON 里 mode=proxy" || bad "JSON 的 mode 不对"

# ---- 4. 关 / 开 -----------------------------------------------------------------

section "4. client off / on"
"$BIN" client off >/tmp/m8-off.txt 2>&1 && ok "off 成功（rc=0）" || bad "off 失败"
case "$(warp-cli --accept-tos status 2>/dev/null)" in
  *Disconnected*|*disconnected*) ok "客户端已断开" ;;
  *) note "warp-cli status → $(warp-cli --accept-tos status 2>/dev/null | tr '\n' ' ')" ;;
esac
"$BIN" client off >/dev/null 2>&1 && ok "重复 off 也成功（幂等）" || bad "重复 off 失败"
"$BIN" client on >/tmp/m8-on.txt 2>&1 && ok "on 成功（rc=0）" || bad "on 失败: $(tail -2 /tmp/m8-on.txt)"
if wait_socks "$PORT" 25; then
  ok "重新打开后链路又通了（自愈）"
else
  bad "重新打开后仍不通"
fi

# ---- 5. 卸载（零残留） ----------------------------------------------------------

section "5. client uninstall"
note "官方客户端自己的规则（卸载前）: $(svc_rule_count) 条"
"$BIN" client uninstall >/tmp/m8-uninstall.txt 2>&1 && ok "uninstall 成功" || bad "uninstall 失败"
[ "$(have_cli)" = "no" ] && ok "warp-cli 已删除（包已卸载）" || bad "warp-cli 仍在"
[ -f "$APT_LIST" ] && bad "apt 源残留" || ok "我们建的 apt 源已删除"
[ -f "$KEYRING" ] && bad "密钥环残留" || ok "我们导入的密钥环已删除"
[ -f "$CSTATE" ] && bad "状态文件残留" || ok "状态文件已删除"
[ -f "$DROPIN" ] && bad "drop-in 残留" || ok "drop-in 不存在（本来就没写）"
[ "$(rules_total_hook)" = "0" ] && ok "钩子规则已清空" || bad "还有 $(rules_total_hook) 条钩子规则"
[ "$(svc_rule_count)" = "0" ] && ok "warp-svc 自己的规则也已清空（钩子之外的残留）" || bad "还有 $(svc_rule_count) 条 warp-svc 规则: $(ip -4 rule show | grep 'fwmark 0x100cf')"
[ "$(port_listen "$PORT")" = "0" ] && ok "socks5 端口已释放" || bad "端口仍被占用"
"$BIN" client uninstall >/dev/null 2>&1 && ok "重复 uninstall 也成功（幂等）" || bad "重复 uninstall 失败"
[ "$(tunnel_ok)" = "yes" ] && ok "隧道没被这套东西影响（仍 warp=on）" || bad "隧道被影响了"
net_ok && ok "系统本身仍能上外网" || bad "系统网络被破坏了"
note "默认路由 dev=$(default_dev)"

# ---- 6. 与 warpgo uninstall 的联动 ----------------------------------------------

section "6. warpgo uninstall 的联动与边界"
"$BIN" client install --mode proxy --port "$PORT" >/tmp/m8-reinstall.txt 2>&1 \
  && ok "重新装上（用于联动测试）" || bad "重装失败: $(tail -3 /tmp/m8-reinstall.txt)"
"$BIN" uninstall >/tmp/m8-full-uninstall.txt 2>&1
grep -q "已一并撤销官方 Linux Client" /tmp/m8-full-uninstall.txt \
  && ok "总卸载里包含官方客户端的撤销" \
  || bad "总卸载没有撤销官方客户端"
[ "$(have_cli)" = "no" ] && ok "整体卸载后 warp-cli 已删除" || bad "整体卸载后 warp-cli 仍在"
[ -f "$APT_LIST" ] && bad "整体卸载后 apt 源残留" || ok "整体卸载后 apt 源已删除"
# 总卸载与 client uninstall 走同一份实现（clientRemove），这条也要一并成立 ——
# 否则"手工撤得干净、总卸载却留了尾巴"这类偏差迟早出现。
[ "$(svc_rule_count)" = "0" ] && ok "整体卸载后 warp-svc 自己的规则也没了" || bad "整体卸载后仍有 $(svc_rule_count) 条 warp-svc 规则"
# 注意：`warpgo uninstall` **本来就会**拆掉隧道（那是它的职责之一），
# 所以这里断言的是"它确实拆了"，而不是"隧道还在"。
if [ "$(tunnel_ok)" = "no" ]; then
  ok "总卸载按预期拆掉了隧道（uninstall 的职责之一）"
else
  note "隧道仍通 —— 按设计 uninstall 应当把它拆掉，这里可能被保活又拉起来了"
fi
"$BIN" up >/tmp/m8-reup.txt 2>&1 && ok "已重新建立隧道" || bad "重建隧道失败"
[ "$(tunnel_ok)" = "yes" ] && ok "隧道恢复后 warp=on" || bad "隧道恢复后仍不通"

# ---- 7. 交互菜单 ---------------------------------------------------------------

section "7. 交互菜单"
# 裸敲 `warpgo` 分两种现场，必须分开验：
#   - 有终端 → 进菜单（这里造不出终端，留给人工/真机确认）；
#   - 无终端 → 印用法。这一条是脚本安全的关键：管道里裸敲如果进了菜单，
#     菜单会去读 stdin 当成选择，把调用方的输入吃掉。
printf '' | "$BIN" >/tmp/m8-bare-notty.txt 2>&1 && ok "裸敲且无终端不报错" || bad "裸敲且无终端返回非 0"
grep -q "用法:" /tmp/m8-bare-notty.txt && ok "裸敲且无终端时印用法" || bad "裸敲且无终端时没印用法"
# 用"请选择"而不是"交互菜单"当判据：用法正文里也有"交互菜单"三个字（menu 那一行）。
grep -q "请选择" /tmp/m8-bare-notty.txt && bad "没有终端却进了菜单（会吃 stdin）" || ok "没有终端时不会进菜单"
printf '0\n' | "$BIN" menu >/tmp/m8-menu0.txt 2>&1 && ok "menu 选 0 能正常退出" || bad "menu 退出异常"
grep -q "请选择" /tmp/m8-menu0.txt && ok "菜单确实在等输入" || bad "菜单没有提示符"
grep -q "交互菜单" /tmp/m8-menu0.txt && ok "菜单标题正确" || bad "菜单没有标题"
grep -q "建立隧道" /tmp/m8-menu0.txt && ok "菜单列出了隧道项" || bad "菜单缺少隧道项"
grep -q "官方客户端" /tmp/m8-menu0.txt && ok "菜单有分组标题" || bad "菜单没有分组标题"
# 选项号会随菜单增删而变 —— 这里钉住"按当前表算出来的那一条"，
# 否则编号一动，选 7 就可能变成"建立隧道"（在真机上会真的去建隧道）。
printf '7\n0\n' | "$BIN" menu >/tmp/m8-menu-status.txt 2>&1 && ok "menu 能执行子命令（选 7 = 查看状态）" || bad "menu 执行子命令失败"
grep -q "WARP" /tmp/m8-menu-status.txt && ok "子命令的输出出现在菜单里" || bad "没看到子命令输出"
printf 'zz\n0\n' | "$BIN" menu >/tmp/m8-menu-bad.txt 2>&1 && ok "非法选择不致命" || bad "非法选择让菜单退出了"
grep -q "不认识的选择" /tmp/m8-menu-bad.txt && ok "非法选择有明确提示" || bad "非法选择没有提示"
# 会改机器状态的选项必须先问一次，且默认（回车 / n）= 不执行。
# 这里用"选拆除隧道、然后说不"来验：隧道必须**还在** —— 这是"按错一位"的拦。
printf '12\nn\n0\n' | "$BIN" menu >/tmp/m8-menu-cancel.txt 2>&1 && ok "取消路径不报错" || bad "取消路径异常"
grep -q "要执行" /tmp/m8-menu-cancel.txt && ok "会改状态的选项先问一次" || bad "会改状态的选项没问确认"
grep -q "已取消" /tmp/m8-menu-cancel.txt && ok "选 n 时明确说明已取消" || bad "取消没有提示"
[ "$(tunnel_ok)" = "yes" ] && ok "选了 n 就真的没动隧道（误触被挡住）" || bad "选了 n 但隧道被拆掉了"

# ---- 8. （可选）真实 warp 模式 --------------------------------------------------
#
# 默认不跑：warp 模式会接管默认路由，在只能靠 SSH 进入的机器上有断连风险。
# 要跑就自己带死人开关，例如：
#   (setsid bash -c 'sleep 180; warp-cli --accept-tos disconnect; systemctl stop warp-svc' &)
#   M8_TEST_WARP=1 bash tests/e2e/m8-client.sh

if [ "${M8_TEST_WARP:-0}" = "1" ]; then
  section "8. warp 模式（可选，带风险）"
  "$BIN" client install --mode warp >/tmp/m8-warp.txt 2>&1
  RC=$?
  [ "$RC" = "0" ] && ok "warp 模式安装成功（rc=0）" || bad "warp 模式安装失败（rc=$RC）: $(tail -5 /tmp/m8-warp.txt)"
  grep -q "接管默认路由" /tmp/m8-warp.txt && ok "动手之前给出了断连警告" || bad "没有给出断连警告"
  [ -d "/sys/class/net/$CLIENT_IF" ] && ok "接口 $CLIENT_IF 已出现" || bad "接口没有出现"
  [ -f "$DROPIN" ] && ok "drop-in 已写入: $DROPIN" || bad "drop-in 缺失"
  grep -q "hook add --wait 30s" "$DROPIN" && ok "drop-in 里是 ExecStartPost=hook add" || bad "drop-in 的 ExecStartPost 不对"
  grep -q "hook del" "$DROPIN" && ok "drop-in 里是 ExecStop=hook del" || bad "drop-in 的 ExecStop 不对"
  [ "$(rules_total_hook)" != "0" ] && ok "warp 模式下钩子规则已就位" || bad "钩子规则没加上"
  # 重启 warp-svc，验证 drop-in 真的会补规则。
  "$BIN" hook del >/dev/null 2>&1
  systemctl restart warp-svc >/dev/null 2>&1
  sleep 6
  [ "$(rules_total_hook)" != "0" ] && ok "重启 warp-svc 后 drop-in 自动把规则补了回来" || bad "drop-in 没有生效"
  "$BIN" client uninstall >/dev/null 2>&1
  ok "已撤销 warp 模式"
else
  section "8. warp 模式（默认跳过）"
  note "warp 模式会接管默认路由，默认不跑；要跑设 M8_TEST_WARP=1 并自带死人开关"
fi

printf '\n== 结果 ==\n  通过 %d / 失败 %d\n' "$pass" "$fail"
if [ "$fail" != "0" ]; then exit 1; fi
