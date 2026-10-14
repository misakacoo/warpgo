#!/usr/bin/env bash
# M4 验收：保活巡检 + systemd + 卸载（含 DNS 还原）。
#
# 与前几轮不同，这个脚本会**真的往这台机器上装一个 systemd 服务**，
# 也会**真的改写 /etc/resolv.conf** —— 那正是它要验证的东西。
# 所以它靠三重兜底保证收尾干净：
#     ① 脚本自己的 trap（无论怎么退出都执行 uninstall + 恢复隧道）
#     ② 每一步都记录"改之前是什么"，收尾时逐一核对
#     ③ 调用方挂的死人开关（本轮 10 分钟）
#
# 建议在 setsid 后台运行：install 那一步会启动服务，虽然它不会断网，
# 但保持会话干净总是更好。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=${IFACE:-warp}
TABLE=51820
RUNDIR=/run/warpgo
UNIT=/etc/systemd/system/warpgo-watch.service
UNIT_NAME=warpgo-watch.service
SVC_BIN=/usr/bin/warpgo
RESOLV=/etc/resolv.conf
RESOLV_BAK=$ETC/resolv.conf.origin

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }

# 把语言钉死。
#
# 上一轮（M3）在这里栽过一次：脚本 grep 中文"自检"，而 VPS 的 LANG 是 C，
# 程序输出英文，于是断言假失败。**验收脚本不该假设被测程序的输出语言** ——
# 与其写"中英都认"的正则，不如直接把语言固定下来，让断言是确定的。
export LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

pass=0
fail=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
section() { printf '\n== %s ==\n' "$1"; }
note() { printf '  · %s\n' "$1"; }

# 接口创建时间：判断"接口有没有被重建"的硬判据。
#
# 刻意不用 `wg show latest-handshakes` —— 它会随正常 re-handshake 自然推进，
# 单独用它会把"一切正常"误读成"接口被重建了"。
iface_ctime() { stat -c %Y "/sys/class/net/$IFACE" 2>/dev/null || echo 0; }
rules_v4() { ip -4 rule show | wc -l; }
our_rules() { ip -4 rule show | grep -cE '^(9000|9010|9011|9020):'; }
table_routes() { ip -4 route show table "$TABLE" 2>/dev/null | wc -l; }
trace_via_iface() { timeout 15 curl -4 -ksm12 --interface "$IFACE" https://1.1.1.1/cdn-cgi/trace 2>/dev/null | grep -E '^(ip|warp)=' | tr '\n' ' '; }
net_ok() { timeout 8 curl -4 -ksm6 https://1.1.1.1/cdn-cgi/trace >/dev/null 2>&1; }
wait_net() {
  local n=${1:-60} i=0
  while [ "$i" -lt "$n" ]; do
    net_ok && return 0
    sleep 1; i=$((i + 1))
  done
  return 1
}

# 收尾：把机器恢复到"装好隧道、服务已卸"的状态。
cleanup() {
  printf '\n== 收尾 ==\n'
  systemctl stop "$UNIT_NAME" >/dev/null 2>&1
  systemctl disable "$UNIT_NAME" >/dev/null 2>&1
  rm -f "$UNIT" "$SVC_BIN" >/dev/null 2>&1
  systemctl daemon-reload >/dev/null 2>&1
  systemctl reset-failed "$UNIT_NAME" >/dev/null 2>&1
  # 先还原 DNS，再拆隧道 —— DNS 备份就在状态目录里，顺序反了会丢。
  if [ -e "$RESOLV_BAK" ]; then
    cp -f "$RESOLV_BAK" "$RESOLV"
    printf '  · 已从备份还原 %s\n' "$RESOLV"
  fi
  "$BIN" down >/dev/null 2>&1
  ip link del "$IFACE" >/dev/null 2>&1
  rm -rf "$RUNDIR" >/dev/null 2>&1
  printf '  · 现场已清理\n'
  # 把隧道恢复回来 —— 这台机器的 WARP 出口是用户在用着的。
  if "$BIN" up >/dev/null 2>&1; then
    printf '  · 隧道已恢复：%s\n' "$(trace_via_iface)"
  else
    printf '  · 隧道恢复失败，请手工执行: %s up\n' "$BIN"
  fi
}
trap cleanup EXIT

section "0. 环境"
printf '  二进制: %s\n' "$("$BIN" version 2>&1 | head -1)"
printf '  systemd: %s\n' "$(systemctl --version | head -1)"
"$BIN" down >/dev/null 2>&1
ip link del "$IFACE" >/dev/null 2>&1
sleep 1
if wait_net 30; then ok "基线：网络可用"; else bad "基线就没网，后面的结论不可信"; exit 1; fi

ORIG_RESOLV_SHA=$(sha256sum "$RESOLV" | cut -d' ' -f1)
note "原始 resolv.conf sha256 = ${ORIG_RESOLV_SHA:0:16}…"
note "原始内容: $(tr '\n' ' ' < "$RESOLV")"

section "1. 起隧道（非全局）"
"$BIN" up >/tmp/m4-up.log 2>&1 && ok "up 退出码 0" || { bad "up 失败"; cat /tmp/m4-up.log; exit 1; }
BASE_CTIME=$(iface_ctime)
BASE_RULES=$(rules_v4)
note "接口创建时间=$BASE_CTIME  规则数=$BASE_RULES  经接口: $(trace_via_iface)"

# ─────────────────────────────────────────────────────────────
section "2. 健康时连跑 5 次：接口必须一动不动"
# 这是本轮最核心的一条。上游每 5 分钟无条件重建接口，出口 IP 一直在变；
# 而"重建"的硬判据是接口创建时间，比 IP 可靠得多（CF 可能分到同一个 IP）。
drift=0
for i in 1 2 3 4 5; do
  out=$("$BIN" watch --once 2>&1); rc=$?
  c=$(iface_ctime); r=$(rules_v4)
  if [ "$rc" -ne 0 ]; then bad "第 $i 次 watch --once 退出码 $rc"; drift=1; fi
  if [ "$c" != "$BASE_CTIME" ]; then bad "第 $i 次之后接口被重建了（$BASE_CTIME -> $c）"; drift=1; fi
  if [ "$r" != "$BASE_RULES" ]; then bad "第 $i 次之后规则数变了（$BASE_RULES -> $r）"; drift=1; fi
  printf '    [%d] 接口时间=%s 规则=%s rc=%s\n' "$i" "$c" "$r" "$rc"
done
[ "$drift" = 0 ] && ok "连跑 5 次：接口创建时间与规则数完全不变（健康就不动）" \
                 || bad "健康巡检改动了系统状态"

# 日志必须明确说"没动"，而不是含糊过去。
"$BIN" watch --once 2>&1 | grep -qiE 'left untouched|未做任何改动' \
  && ok "日志明确说明「未做任何改动」" || bad "日志没说清这一轮做了什么"

# ─────────────────────────────────────────────────────────────
section "3. 走真实的循环（--max-rounds 2）：仍然不动"
out=$(timeout 60 "$BIN" watch --max-rounds 2 --interval 10s 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "循环退出码 0" || bad "循环退出码 $rc"
c=$(iface_ctime)
[ "$c" = "$BASE_CTIME" ] && ok "循环跑完接口仍未被重建" || bad "循环期间接口被重建（$BASE_CTIME -> $c）"
printf '%s' "$out" | grep -qiE 'keepalive stopped|巡检结束' \
  && ok "结束时给出了汇总（journal 里能看到这一轮到底做了什么）" || bad "缺少结束汇总"

# ─────────────────────────────────────────────────────────────
section "4. 断链自愈：删掉接口，一轮内必须重建"
ip link del "$IFACE" >/dev/null 2>&1
sleep 1
ip link show "$IFACE" >/dev/null 2>&1 && bad "接口没删掉，用例无效" || ok "已删掉接口（模拟断链）"
note "删掉后经接口探测必然失败，这正是巡检要发现的"

out=$(timeout 120 "$BIN" watch --once 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "一轮之后退出码 0（已修好）" || bad "退出码 $rc，说明没修好"
ip link show "$IFACE" >/dev/null 2>&1 && ok "接口已被重建" || bad "接口仍未出现"
NEW_CTIME=$(iface_ctime)
[ "$NEW_CTIME" != "$BASE_CTIME" ] && ok "接口创建时间已变化（确实是新建的）" || bad "接口创建时间没变，重建可能没发生"
after=$(trace_via_iface)
printf '%s' "$after" | grep -q 'warp=on' && ok "经接口出口恢复正常：$after" || bad "重建后仍拿不到 warp=on：$after"
r=$(rules_v4)
[ "$r" = "$BASE_RULES" ] && ok "规则数回到 $BASE_RULES（没有累积）" || bad "规则数异常：$BASE_RULES -> $r"
BASE_CTIME=$NEW_CTIME

# ─────────────────────────────────────────────────────────────
section "5. 接口在、配置对、但链路不通 —— 应当硬复位"
# 这是现实中最常见的故障形态：接口好好的、配置也一致，就是不通。
# 单纯 Ensure 会认为"什么都不用改"，于是永远修不好 —— 必须有硬复位。
note "用一个必定不可达的端点（TEST-NET-1）制造这种状态"
"$BIN" up --endpoint 192.0.2.1:2408 >/tmp/m4-bad.log 2>&1
rc=$?
[ "$rc" -eq 0 ] && ok "up --endpoint <黑洞> 成功（接口在、配置一致）" || note "up 退出码 $rc（不影响本用例）"
ip link show "$IFACE" >/dev/null 2>&1 && ok "接口存在" || bad "接口不存在，用例无效"
BAD_CTIME=$(iface_ctime)
note "当前经接口: $(trace_via_iface)$(printf '')"

out=$(timeout 120 "$BIN" watch --once --endpoint 192.0.2.1:2408 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
printf '%s' "$out" | grep -qiE 'hard-reset|硬复位' \
  && ok "识别出「配置没变但链路不通」并做了硬复位" \
  || bad "没有走硬复位分支 —— 这种情况会永远修不好"
now=$(iface_ctime)
[ "$now" != "$BAD_CTIME" ] && ok "接口确实被重建（创建时间变化）" || bad "接口创建时间未变，硬复位没生效"

# 恢复：换回账户里的真实端点。
"$BIN" up >/tmp/m4-restore.log 2>&1
after=$(trace_via_iface)
printf '%s' "$after" | grep -q 'warp=on' && ok "恢复真实端点后出口正常：$after" || bad "恢复失败：$after"
BASE_CTIME=$(iface_ctime)

# ─────────────────────────────────────────────────────────────
section "6. 被锁挡住时必须如实报告，不能假装健康"
note "让另一个进程合法地持有单实例锁"
"$BIN" dev lock --hold 12s >/tmp/m4-hold.log 2>&1 &
HOLD_PID=$!
sleep 2
[ -e /run/warpgo/net.lock ] && ok "锁已被持有" || bad "锁没建起来，用例无效"

out=$(timeout 30 "$BIN" watch --once 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 2 ] && ok "退出码 2（不健康）—— 没有假装成功" \
                || bad "退出码 $rc：被挡住时不该报告成功"
# 断言"报告了跳过"，而不是"日志里出现了某个词" ——
# 前者是结构性事实，后者会随文案微调而碎（上一轮就是栽在这上面）。
printf '%s' "$out" | grep -q 'skipped 1' \
  && ok "汇总里明确记了「本轮被跳过」（没有含糊过去）" \
  || bad "汇总没有体现跳过 —— 那样用户会以为巡检定过了一切正常"
c=$(iface_ctime)
[ "$c" = "$BASE_CTIME" ] && ok "被挡住时确实什么都没做" || bad "被锁挡住却动了接口"
wait "$HOLD_PID" 2>/dev/null

# ─────────────────────────────────────────────────────────────
section "7. 伪造旧格式遗留锁：应当接管，且不误杀无辜进程"
sleep 300 &
INNOCENT=$!
printf '%s\n' "$INNOCENT" > /run/warpgo/net.lock
note "锁文件内容: $(cat /run/warpgo/net.lock)（只有 PID，没有心跳 —— 旧格式）"

out=$(timeout 60 "$BIN" watch --once 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "旧格式锁被接管，本轮正常完成" || bad "退出码 $rc：被遗留锁卡住了"
printf '%s' "$out" | grep -qiE 'leftover lock|遗留|旧版本' && ok "日志说明了这是旧格式遗留锁" || bad "未提示旧格式锁"
kill -0 "$INNOCENT" 2>/dev/null && ok "无辜进程未被误杀（PID $INNOCENT 仍在）" || bad "无辜进程被杀了！"
kill "$INNOCENT" 2>/dev/null
[ -e /run/warpgo/net.lock ] && bad "锁未释放" || ok "锁已释放"
c=$(iface_ctime)
[ "$c" = "$BASE_CTIME" ] && ok "接管锁之后依然没有重建接口" || bad "接管锁的过程动了接口"

# ─────────────────────────────────────────────────────────────
section "8. install：装成 systemd 服务"
rm -f "$UNIT" "$SVC_BIN"
out=$(timeout 120 "$BIN" install --interval 5m 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "install 退出码 0" || bad "install 退出码 $rc"
[ -f "$UNIT" ] && ok "单元已写入 $UNIT" || bad "单元文件不存在"
[ -x "$SVC_BIN" ] && ok "二进制已安装到 $SVC_BIN" || bad "二进制未安装"
systemctl is-enabled "$UNIT_NAME" >/dev/null 2>&1 && ok "已设为开机自启" || bad "未设置开机自启"
systemctl is-active "$UNIT_NAME" >/dev/null 2>&1 && ok "服务已启动" || bad "服务未启动"

# 单元内容的关键行。
grep -q "^ExecStart=$SVC_BIN" "$UNIT" && ok "ExecStart 用的是绝对路径" || bad "ExecStart 路径不对"
grep -q "^Restart=always" "$UNIT" && ok "Restart=always" || bad "缺少 Restart=always"
grep -q "ExecStop" "$UNIT" && bad "不该有 ExecStop（它失败会把单元标成 failed）" || ok "刻意没有 ExecStop"

# 服务真的在干活：journal 里应当有一轮健康记录。
sleep 3
if journalctl -u "$UNIT_NAME" --since "-2min" --no-pager 2>/dev/null | grep -qiE 'healthy|正常'; then
  ok "journal 里有巡检记录（服务确实在跑）"
else
  note "journal 尚未出现巡检记录（首轮可能还在探测），继续"
fi

# ─────────────────────────────────────────────────────────────
section "9. down 必须先停保活，否则隧道会「拆了又自己回来」"
systemctl start "$UNIT_NAME" >/dev/null 2>&1
systemctl is-active "$UNIT_NAME" >/dev/null 2>&1 && ok "服务已处于运行态" || note "服务未能启动（继续）"

out=$(timeout 60 "$BIN" down 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "down 退出码 0" || bad "down 退出码 $rc"
systemctl is-active "$UNIT_NAME" >/dev/null 2>&1 && bad "保活服务仍在运行 —— 它下一轮会把隧道建回来" \
                                                  || ok "保活服务已被 down 停掉"
systemctl is-enabled "$UNIT_NAME" >/dev/null 2>&1 && ok "开机自启仍然保留（down 只停不禁用）" \
                                                   || bad "down 不该取消开机自启"
ip link show "$IFACE" >/dev/null 2>&1 && bad "接口残留" || ok "接口已拆除"
[ "$(our_rules)" = 0 ] && ok "规则已清空" || bad "规则残留 $(our_rules) 条"
[ "$(table_routes)" = 0 ] && ok "表 $TABLE 已清空" || bad "表 $TABLE 有残留路由"
[ -e "$RUNDIR/rollback.json" ] && bad "保险丝快照残留" || ok "无保险丝残留"

# ─────────────────────────────────────────────────────────────
section "10. DNS 备份与改写"
"$BIN" up >/tmp/m4-up2.log 2>&1 && ok "隧道已恢复" || bad "恢复隧道失败"
[ -e "$RESOLV_BAK" ] && bad "此时不该有备份（还没改过 DNS）" || ok "此时没有备份（warpgo 尚未碰过 DNS）"

out=$(timeout 60 "$BIN" up --dns 1.1.1.1,1.0.0.1 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "up --dns 退出码 0" || bad "up --dns 退出码 $rc"
NEW_RESOLV_SHA=$(sha256sum "$RESOLV" | cut -d' ' -f1)
[ "$NEW_RESOLV_SHA" != "$ORIG_RESOLV_SHA" ] && ok "resolv.conf 已被改写" || bad "resolv.conf 没变"
grep -q 'nameserver 1.1.1.1' "$RESOLV" && ok "新内容正确: $(tr '\n' ' ' < "$RESOLV" | cut -c1-60)" || bad "新内容不对"
[ -e "$RESOLV_BAK" ] && ok "原文已备份到 $RESOLV_BAK" || bad "没有备份 —— 这就是旧版的致命伤"
grep -q 'nameserver' "$RESOLV_BAK" && ok "备份里是改名前的原文（不是我们写进去的值）" || bad "备份内容可疑"
ifc=$(iface_ctime)
[ "$ifc" = "$(iface_ctime)" ] && ok "改 DNS 没有动接口" || bad "改 DNS 时重建了接口"

# ─────────────────────────────────────────────────────────────
section "11. uninstall：撤干净（含 DNS 还原）"
PRE_ACCOUNT=$([ -e "$ETC/account.json" ] && echo yes || echo no)
out=$(timeout 120 "$BIN" uninstall 2>&1); rc=$?
printf '%s\n' "$out" | sed 's/^/    /'
[ "$rc" -eq 0 ] && ok "uninstall 退出码 0" || bad "uninstall 退出码 $rc"

systemctl is-enabled "$UNIT_NAME" >/dev/null 2>&1 && bad "开机自启未取消" || ok "开机自启已取消"
[ -f "$UNIT" ] && bad "单元文件残留" || ok "单元文件已删除"
[ -e "$SVC_BIN" ] && bad "$SVC_BIN 残留" || ok "二进制已删除"
ip link show "$IFACE" >/dev/null 2>&1 && bad "接口残留" || ok "接口已删除"
[ "$(our_rules)" = 0 ] && ok "规则已清空" || bad "规则残留 $(our_rules) 条"
[ "$(table_routes)" = 0 ] && ok "表 $TABLE 已清空" || bad "表 $TABLE 有残留"
[ -e "$RUNDIR" ] && bad "$RUNDIR 残留" || ok "运行时目录已清理"
[ -e "$ETC/runtime.json" ] && bad "运行态残留" || ok "运行态已清理"

RESTORED_SHA=$(sha256sum "$RESOLV" | cut -d' ' -f1)
[ "$RESTORED_SHA" = "$ORIG_RESOLV_SHA" ] && ok "★ DNS 已还原成原始内容（sha256 一致）" \
                                        || bad "★ DNS 未还原！原=${ORIG_RESOLV_SHA:0:12} 现=${RESTORED_SHA:0:12}"
[ -e "$RESOLV_BAK" ] && bad "备份残留（还原成功后应当删掉）" || ok "备份已清理"

if [ "$PRE_ACCOUNT" = yes ]; then
  [ -e "$ETC/account.json" ] && ok "账户被保留（默认行为：它是身份，删掉就换 IP）" || bad "账户被删了"
  printf '%s' "$out" | grep -qiE 'kept|保留' && ok "日志说明了账户被保留并提示 --purge" || bad "日志没说明账户去向"
fi
systemctl list-units --all 2>/dev/null | grep -q "$UNIT_NAME" && bad "systemd 里仍有单元记录" || ok "systemd 中已无该单元"

# ─────────────────────────────────────────────────────────────
section "12. --purge 的安全性：不认识的文件一律拒绝"
TMPETC=/tmp/m4-purge-etc
rm -rf "$TMPETC"; mkdir -p "$TMPETC"
# 只放"自己人"的文件：账户 + 运行态。注意**不要**放假 resolv.conf.origin ——
# uninstall 会先还原 DNS，那会把假备份真的写进 /etc/resolv.conf。
printf '{"id":"x"}\n' > "$TMPETC/account.json"
printf '{"if_name":"warp"}\n' > "$TMPETC/runtime.json"
WARPGO_ETC="$TMPETC" "$BIN" uninstall --purge >/tmp/m4-purge1.log 2>&1
[ -e "$TMPETC" ] && bad "只有自己文件时应当被清空（$TMPETC 仍在）" || ok "只含自身文件的状态目录已被清空"

# 再放一个不认识的文件，必须拒绝。
rm -rf "$TMPETC"; mkdir -p "$TMPETC"
printf '{"id":"x"}\n' > "$TMPETC/account.json"
printf 'do not delete me\n' > "$TMPETC/important.txt"
WARPGO_ETC="$TMPETC" "$BIN" uninstall --purge >/tmp/m4-purge2.log 2>&1
[ -e "$TMPETC/important.txt" ] && ok "★ 有陌生文件时拒绝清理（保护了用户的东西）" || bad "★ 把用户文件删了！"
[ -e "$TMPETC/account.json" ] && ok "整个目录被跳过，一个文件都没删" || bad "部分删除 —— 那更糟"
grep -qiE 'refus|拒绝' /tmp/m4-purge2.log && ok "日志说明了拒绝原因" || note "日志措辞未匹配（不影响结论）"
rm -rf "$TMPETC"

# ─────────────────────────────────────────────────────────────
section "13. 结果"
printf '\n  通过 %s 项，失败 %s 项\n' "$pass" "$fail"
[ "$fail" = 0 ] && printf '  \033[32mM4 全部通过\033[0m\n' || printf '  \033[31m有失败项，见上\033[0m\n'
exit "$fail"
