#!/usr/bin/env bash
# M7 验收：WireProxy（菜单 w / y）—— 把 WARP 做成一个本地 socks5 出口。
#
# 这个脚本会**真的从 GitHub 下载 wireproxy**（约 4MB）、**真的装一个 systemd 单元**、
# 真的起进程并真的经 socks5 发请求。三重兜底：
#     ① 自己的 trap：无论怎么退出都撤销 WireProxy 并把隧道拉回来
#     ② 结束时逐项核对零残留（二进制 / 单元 / 配置 / 状态 / 进程 / 端口）
#     ③ 调用方挂的死人开关
#
# 进入时会先把本机已有的 WireProxy 撤掉，于是每次运行都从同一个起点开始。
#
# # 为什么每条断言都另起一条 curl
#
# 程序自己也会经 socks5 探一次（那是它的自检），但验收不能拿被测代码的结论
# 当证据：这里用独立的 `curl --proxy socks5h://…` 直接看 trace 里的 `warp=` 字段。
# 两者同时成立才说明"端口在听"与"链路真的通"是同一件事。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=${IFACE:-warp}
PORT=${PORT:-40000}
PORT2=${PORT2:-25344}
WP_BIN=/usr/local/bin/wireproxy
WP_UNIT=/etc/systemd/system/warpgo-proxy.service
WP_CONF=$ETC/proxy.conf
WP_STATE=$ETC/proxy.json
TARBALL=/tmp/m7-wireproxy.tar.gz

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }
[ -f "$ETC/account.json" ] || { echo "没有账户文件 —— 请先 $BIN register"; exit 1; }

pass=0
fail=0
ok()      { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad()     { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
note()    { printf '  · %s\n' "$1"; }
section() { printf '\n== %s ==\n' "$1"; }

# ---- 现场判据 ------------------------------------------------------------------

svc_active()  { systemctl is-active warpgo-proxy 2>/dev/null || true; }
svc_enabled() { systemctl is-enabled warpgo-proxy 2>/dev/null || true; }
# pgrep -c 在"一个都没找到"时会**打印 0 但以 1 退出**，所以不能用 `|| echo 0`
# —— 那会拼成 "0\n0"，比较必然失败（这个坑让第一轮的断言误报了两处）。
wp_procs() {
  local n
  n=$(pgrep -c -f "$WP_BIN" 2>/dev/null)
  echo "${n:-0}"
}
port_listen() { ss -nltp 2>/dev/null | grep -E ":$1[[:space:]]" | wc -l; }
# 只允许绑在回环上：绑到 0.0.0.0 等于把一条通往 WARP 的开放代理挂到公网。
# 判据是"这一行必须以 127.0.0.1 开头"—— ss 的输出里前面还有 LISTEN/队列/收发包计数，
# 所以模式两端都要有通配。
port_loopback_only() {
  local line
  line=$(ss -nltp 2>/dev/null | grep -E ":$1[[:space:]]")
  [ -n "$line" ] || { echo "no"; return; }
  case "$line" in
    *" 127.0.0.1:$1"*) echo "yes" ;;
    *) echo "no" ;;
  esac
}
# 经 socks5 的独立实测（不用被测代码的结论）。
socks_trace() { # $1 = 端口
  timeout 20 curl -sS --max-time 15 --proxy "socks5h://127.0.0.1:$1" https://1.1.1.1/cdn-cgi/trace 2>/dev/null |
    grep -E '^(ip|warp|loc)=' | tr '\n' ' '
}
socks_trace6() {
  timeout 20 curl -sS --max-time 15 --proxy "socks5h://127.0.0.1:$1" "https://[2606:4700:4700::1111]/cdn-cgi/trace" 2>/dev/null |
    grep -E '^(ip|warp|loc)=' | tr '\n' ' '
}
# 等待用的轻量探测：超时短得多。
#
# 用它而不是 socks_trace 是有原因的：等待循环最多转 20 次，如果每次都可能挂 15 秒，
# 一次失败的验收就会白耗几分钟 —— 而"等不到"这件事本身只需几秒就能确认。
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
reserved() {
  python3 -c "import json;print(','.join(str(x) for x in json.load(open('$ETC/account.json')).get('reserved',[0,0,0])))" 2>/dev/null
}

cleanup() {
  "$BIN" proxy uninstall >/dev/null 2>&1
  # 隧道在验收过程中不该被破坏，但还是核对一次 —— 断了就重建。
  if [ "$(tunnel_ok)" = "no" ]; then
    "$BIN" up >/dev/null 2>&1
  fi
}
trap 'cleanup' EXIT

printf 'M7 验收：WireProxy（socks5 出口）\n'
printf '  二进制 %s\n  socks5 端口 %s / %s\n' "$BIN" "$PORT" "$PORT2"

# ---- 0. 起点：先把已有的撤掉 ---------------------------------------------------

section "0. 进入时的现场"
"$BIN" proxy uninstall >/dev/null 2>&1
[ -e "$WP_BIN" ] && bad "二进制还在：$WP_BIN" || ok "起点干净：没有 wireproxy 二进制"
[ -e "$WP_UNIT" ] && bad "单元还在：$WP_UNIT" || ok "起点干净：没有单元文件"
note "隧道状态: 经 $IFACE 的出口 warp=$(tunnel_ok)"
note "账户 reserved = $(reserved)"

# ---- 1. 未安装时的行为 ---------------------------------------------------------

section "1. 未安装时的行为"
if "$BIN" proxy status >/tmp/m7-status0.txt 2>&1; then
  bad "未安装时 status 不该返回 0"
else
  ok "未安装时 status 以非零码退出"
fi
grep -q "还没有装 WireProxy" /tmp/m7-status0.txt && ok "status 给出了明确提示" || bad "status 提示不清楚"
if "$BIN" proxy on >/tmp/m7-on0.txt 2>&1; then
  bad "未安装时 on 不该成功"
else
  ok "未安装时 on 被拒绝"
fi
grep -q "proxy install" /tmp/m7-on0.txt && ok "提示里给出了下一步（proxy install）" || bad "提示里没有下一步"

# ---- 2. 下载与校验的错误路径 ---------------------------------------------------

section "2. 错误路径（都不该破坏现场）"
if "$BIN" proxy install --url https://example.invalid/x.tar.gz >/tmp/m7-e1.txt 2>&1; then
  bad "--url 没配 --sha256 居然成功了（等于不校验）"
else
  ok "--url 缺 --sha256 被拒绝"
fi
grep -q -- "--sha256" /tmp/m7-e1.txt && ok "报错里指出了 --sha256" || bad "报错没说清出路"

WRONG=$(printf '0%.0s' $(seq 1 64))
if "$BIN" proxy install --sha256 "$WRONG" >/tmp/m7-e2.txt 2>&1; then
  bad "校验和故意写错居然装成功了"
else
  ok "校验和不符时拒绝安装"
fi
grep -q "校验和不符" /tmp/m7-e2.txt && ok "报错说明了「校验和不符」" || bad "报错没说清原因"
[ -e "$WP_BIN" ] && bad "校验失败却留下了二进制！" || ok "校验失败没有留下二进制"
if "$BIN" proxy install --version v9.9.9 >/tmp/m7-e3.txt 2>&1; then
  bad "不存在的版本居然成功了"
else
  ok "不存在的版本被拒绝"
fi
if "$BIN" proxy install --port 80 >/tmp/m7-e4.txt 2>&1; then
  bad "端口 80 居然被接受（越界）"
else
  ok "端口越界被拒绝"
fi
# 端口被占：占一个端口再装。
python3 -c "
import socket,time
s=socket.socket(); s.bind(('127.0.0.1',30001)); s.listen()
time.sleep(40)
" &
HOLDER=$!
sleep 1
if "$BIN" proxy install --port 30001 >/tmp/m7-e5.txt 2>&1; then
  bad "端口被占时居然装成功了"
else
  ok "端口被占用时被拒绝"
fi
grep -q "已经有人在监听" /tmp/m7-e5.txt && ok "报错说清了端口被占" || bad "报错没说清端口问题"
kill "$HOLDER" 2>/dev/null || true

# ---- 3. 真实安装 ---------------------------------------------------------------

section "3. 真实安装（下载 + 校验 + 自检 + 起服务）"
"$BIN" proxy install -v >/tmp/m7-install.txt 2>&1
RC=$?
[ "$RC" = "0" ] && ok "install 成功（rc=0）" || bad "install 失败（rc=$RC）: $(tail -3 /tmp/m7-install.txt)"
grep -q "校验和通过" /tmp/m7-install.txt && ok "下载后核对了校验和" || bad "日志里没有校验和通过的记录"
grep -q "https://github.com/windtf/wireproxy/releases/download/" /tmp/m7-install.txt \
  && ok "下载来源是 windtf/wireproxy 的发布页" \
  || note "下载地址与预期不同（可能用了 --url/--from）"

[ -x "$WP_BIN" ] && ok "二进制已安装且可执行: $WP_BIN" || bad "二进制缺失或不可执行"
"$WP_BIN" --version >/tmp/m7-wpver.txt 2>&1 && ok "wireproxy 能独立执行: $(head -1 /tmp/m7-wpver.txt)" || bad "装出来的二进制跑不起来"
[ -f "$WP_CONF" ] && ok "配置已生成: $WP_CONF" || bad "配置缺失"
[ "$(stat -c %a "$WP_CONF" 2>/dev/null)" = "600" ] && ok "配置权限是 0600（里面有私钥）" || bad "配置权限是 $(stat -c %a "$WP_CONF" 2>/dev/null)"
for sec in Interface Peer Socks5 Resolve; do
  grep -q "^\[$sec\]" "$WP_CONF" && ok "配置里有 [$sec] 段" || bad "配置缺 [$sec] 段"
done
grep -q "BindAddress = 127.0.0.1:$PORT$" "$WP_CONF" && ok "socks5 绑在回环地址上" || bad "socks5 绑定地址不对"
PRIV=$(python3 -c "import json;print(json.load(open('$ETC/account.json'))['private_key'])")
grep -q "PrivateKey = $PRIV" "$WP_CONF" && ok "配置里的私钥与账户一致" || bad "配置里的私钥对不上"
PUB=$(python3 -c "import json;print(json.load(open('$ETC/account.json'))['peer_public_key'])")
grep -q "PublicKey = $PUB" "$WP_CONF" && ok "配置里的对端与账户一致" || bad "对端对不上"
[ -f "$WP_UNIT" ] && ok "单元已写入: $WP_UNIT" || bad "单元缺失"
grep -q "ExecStart=$WP_BIN -c $WP_CONF" "$WP_UNIT" && ok "单元里的路径是绝对路径且指向我们的配置" || bad "单元的 ExecStart 不对"
grep -q "ExecStop" "$WP_UNIT" && bad "不该写 ExecStop" || ok "单元没有 ExecStop（与保活单元同一取舍）"
[ "$(svc_enabled)" = "enabled" ] && ok "单元已设置开机自启" || bad "单元没有 enable：$(svc_enabled)"
[ "$(svc_active)" = "active" ] && ok "单元正在运行" || bad "单元没跑起来：$(svc_active)"
[ -f "$WP_STATE" ] && ok "状态文件已写入: $WP_STATE" || bad "状态文件缺失"
grep -q '"sha256"' "$WP_STATE" && ok "状态文件里记了校验和（可追溯）" || bad "状态文件里没有校验和"
if [ "$(reserved)" != "0,0,0" ]; then
  grep -q "发不出那 3 个字节" /tmp/m7-install.txt \
    && ok "账户带 reserved —— 已明确警告 wireproxy 带不了它" \
    || bad "带 reserved 却没有给出警告（用户会一头雾水）"
else
  note "账户不带 reserved，跳过该警告的断言"
fi

# ---- 4. 经 socks5 的真实连通性（独立于程序的结论） -----------------------------

section "4. 经 socks5 的实测"
[ "$(port_listen "$PORT")" != "0" ] && ok "端口 $PORT 在监听" || bad "端口 $PORT 没有监听"
[ "$(port_loopback_only "$PORT")" = "yes" ] && ok "只监听 127.0.0.1（没有暴露到公网）" || bad "监听地址不是回环：$(ss -nltp | grep ":$PORT")"
if wait_socks "$PORT" 20; then
  ok "curl --proxy socks5h://127.0.0.1:$PORT 拿到 warp=on（$(socks_trace "$PORT")）"
else
  bad "经 socks5 访问拿不到 warp=on（$(socks_trace "$PORT")）"
fi
case "$(socks_trace6 "$PORT")" in
  *warp=on*|*warp=plus*) ok "经 socks5 的 IPv6 出口也是 WARP（$(socks_trace6 "$PORT")）" ;;
  *) note "经 socks5 的 IPv6 未拿到 warp=on（$(socks_trace6 "$PORT")）—— 账户或本机可能没有 v6" ;;
esac
"$BIN" proxy status >/tmp/m7-status1.txt 2>&1 && ok "proxy status 返回 0" || bad "proxy status 非 0"
grep -q "warp *on" /tmp/m7-status1.txt && ok "status 里能看到 warp=on" || bad "status 里没有 warp 字段"
"$BIN" proxy status --json >/tmp/m7-status1.json 2>&1 && ok "status --json 可用" || bad "status --json 失败"
grep -q '"listening": true' /tmp/m7-status1.json && ok "JSON 报告端口在监听" || bad "JSON 的 listening 不对"

# ---- 5. 换端口与"原端口重装" -----------------------------------------------

section "5. 重复安装（端口不变）必须成功"
# 这一条是真机验收逼出来的：第一版只看"端口有没有人在听"，
# 而重装时占着那个端口的正是我们自己的 wireproxy —— 于是"重装"必然失败。
"$BIN" proxy install >/tmp/m7-reinstall-same.txt 2>&1 \
  && ok "在已运行的情况下重跑 install（同端口）成功" \
  || bad "重复安装失败: $(tail -2 /tmp/m7-reinstall-same.txt)"
case "$(socks_trace "$PORT")" in
  *warp=on*) ok "重装后仍通（服务被 restart 顶掉后自己接上）" ;;
  *)         bad "重装后不通（$(socks_trace "$PORT")）" ;;
esac

section "5b. 换端口（$PORT → $PORT2）"
"$BIN" proxy install --port "$PORT2" >/tmp/m7-port2.txt 2>&1 && ok "用新端口重装成功" || bad "换端口失败: $(tail -2 /tmp/m7-port2.txt)"
grep -q "BindAddress = 127.0.0.1:$PORT2$" "$WP_CONF" && ok "配置已改成新端口" || bad "配置没改端口"
[ "$(port_listen "$PORT2")" != "0" ] && ok "新端口在监听" || bad "新端口没有监听"
[ "$(port_listen "$PORT")" = "0" ] && ok "旧端口已释放" || bad "旧端口仍在监听"
case "$(socks_trace "$PORT2")" in
  *warp=on*) ok "经新端口的访问仍是 WARP" ;;
  *)         bad "经新端口拿不到 warp=on（$(socks_trace "$PORT2")）" ;;
esac

# ---- 6. 关 / 开 ---------------------------------------------------------------

section "6. proxy off / on"
"$BIN" proxy off >/tmp/m7-off.txt 2>&1 && ok "off 成功（rc=0）" || bad "off 失败"
[ "$(svc_active)" = "inactive" ] && ok "服务已停止" || bad "服务状态是 $(svc_active)"
[ "$(port_listen "$PORT2")" = "0" ] && ok "端口已释放" || bad "端口仍被占用"
[ "$(wp_procs)" = "0" ] && ok "wireproxy 进程已退出" || bad "还有 $(wp_procs) 个 wireproxy 进程"
"$BIN" proxy off >/dev/null 2>&1 && ok "重复 off 也成功（幂等）" || bad "重复 off 失败"
"$BIN" proxy on >/tmp/m7-on.txt 2>&1 && ok "on 成功（rc=0）" || bad "on 失败: $(tail -2 /tmp/m7-on.txt)"
if wait_socks "$PORT2" 20; then
  ok "重新打开后链路又通了（自愈）"
else
  bad "重新打开后仍不通"
fi

# ---- 7. 撤销 -------------------------------------------------------------------

section "7. proxy uninstall（零残留）"
"$BIN" proxy uninstall >/tmp/m7-uninstall.txt 2>&1 && ok "uninstall 成功" || bad "uninstall 失败"
[ -e "$WP_BIN" ] && bad "二进制残留" || ok "二进制已删除"
[ -e "$WP_UNIT" ] && bad "单元残留" || ok "单元已删除"
[ -e "$WP_CONF" ] && bad "配置残留（里面有私钥！）" || ok "配置已删除"
[ -e "$WP_STATE" ] && bad "状态文件残留" || ok "状态文件已删除"
[ "$(port_listen "$PORT2")" = "0" ] && ok "端口已释放" || bad "端口仍被占用"
[ "$(wp_procs)" = "0" ] && ok "没有遗留进程" || bad "还有 $(wp_procs) 个 wireproxy 进程"
act=$(svc_active)
case "$act" in
  inactive|unknown|failed) ok "systemd 里没有活跃的单元（$act）" ;;
  *) bad "单元仍在 active：$act" ;;
esac
"$BIN" proxy uninstall >/dev/null 2>&1 && ok "重复 uninstall 也成功（幂等）" || bad "重复 uninstall 失败"
if "$BIN" proxy status >/dev/null 2>&1; then
  bad "撤销后 status 不该返回 0"
else
  ok "撤销后 status 以非零码退出"
fi
[ "$(tunnel_ok)" = "yes" ] && ok "隧道没被这套东西影响（仍 warp=on）" || bad "隧道被影响了"
net_ok && ok "系统本身仍能上外网" || bad "系统网络被破坏了"

# ---- 8. 离线安装 --from ---------------------------------------------------------

section "8. 离线安装（--from 本地归档）"
if curl -sSL --max-time 120 -o "$TARBALL" \
    https://github.com/windtf/wireproxy/releases/download/v1.1.3/wireproxy_linux_amd64.tar.gz; then
  ok "已下载归档用于离线安装（$(stat -c%s "$TARBALL") 字节）"
  "$BIN" proxy install --from "$TARBALL" >/tmp/m7-from.txt 2>&1 && ok "--from 安装成功" || bad "--from 安装失败: $(tail -2 /tmp/m7-from.txt)"
  grep -q "离线安装" /tmp/m7-from.txt && ok "如实说明了这是离线安装（没联网、没有清单可核对）" || bad "日志没说清来源"
  # --from 与 --sha256 同时给出时也要核对（--from 不等于"跳过校验"）。
  if "$BIN" proxy install --from "$TARBALL" --sha256 "$WRONG" >/tmp/m7-from-bad.txt 2>&1; then
    bad "--from 配错校验和居然成功了"
  else
    ok "--from 配错校验和会被拒绝"
  fi
  grep -q "校验和对不上" /tmp/m7-from-bad.txt && ok "报错说清了本地文件校验和对不上" || bad "报错没说清原因"
  case "$(socks_trace "$PORT")" in
    *warp=on*) ok "离线装出来的也能用（经 socks5 warp=on）" ;;
    *)         bad "离线安装后经 socks5 不通（$(socks_trace "$PORT")）" ;;
  esac
  "$BIN" proxy uninstall >/dev/null 2>&1
else
  note "下载失败（网络受限），跳过 --from 一节"
fi

# ---- 9. 与 warpgo uninstall 的联动 ---------------------------------------------

section "9. warpgo uninstall 要一并撤掉 WireProxy"
"$BIN" proxy install >/tmp/m7-reinstall.txt 2>&1 && ok "重新装上（用于联动测试）" || bad "重装失败"
"$BIN" uninstall >/tmp/m7-full-uninstall.txt 2>&1
grep -q "已一并撤销 WireProxy" /tmp/m7-full-uninstall.txt \
  && ok "uninstall 里包含 WireProxy 的撤销" \
  || bad "uninstall 没有撤销 WireProxy"
[ -e "$WP_BIN" ] && bad "整体卸载后二进制残留" || ok "整体卸载后二进制已删除"
[ -e "$WP_UNIT" ] && bad "整体卸载后单元残留" || ok "整体卸载后单元已删除"
[ -e "$WP_CONF" ] && bad "整体卸载后配置残留" || ok "整体卸载后配置已删除"

# 把隧道恢复回去（uninstall 会把它拆掉 —— 这正是它该做的）。
"$BIN" up >/tmp/m7-reup.txt 2>&1 && ok "已重新建立隧道" || bad "重建隧道失败: $(tail -2 /tmp/m7-reup.txt)"
if [ "$(tunnel_ok)" = "yes" ]; then
  ok "隧道恢复后 warp=on"
else
  bad "隧道恢复后仍不通"
fi

printf '\n== 结果 ==\n  通过 %d / 失败 %d\n' "$pass" "$fail"
if [ "$fail" != "0" ]; then exit 1; fi
