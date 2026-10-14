#!/usr/bin/env bash
# M0 验收：单实例锁的「占用 / 心跳 / 接管」三种行为，在真机 Debian 上跑。
#
# 为什么这些必须在真机上验：接管逻辑会真的 kill 进程，而「杀错人」和「杀不掉」
# 两种失败模式在假实现里都测不出来（假实现里 kill 永远成功）。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }

WORK=$(mktemp -d)
LOCK="$WORK/net.lock"
pass=0
fail=0
cleanup() {
  [ -n "${FAKE:-}" ] && kill -9 "$FAKE" 2>/dev/null
  [ -n "${INNO:-}" ] && kill -9 "$INNO" 2>/dev/null
  [ -n "${OLD:-}" ] && kill -9 "$OLD" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }

echo "== 环境 =="
printf '  二进制: %s\n' "$("$BIN" version 2>&1 | head -1)"
printf '  锁路径: %s\n' "$LOCK"
echo

echo "== 1. 正常获取与释放 =="
out=$("$BIN" dev lock --path "$LOCK" 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "退出码 0" || bad "退出码 $rc（应为 0）"
[ -e "$LOCK" ] && bad "释放后锁文件仍存在" || ok "释放后锁文件已删除"
case "$out" in *"$LOCK"*) ok "日志里出现锁路径" ;; *) bad "日志里没有锁路径" ;; esac

echo
echo "== 2. 第二个实例必须被挡回来（Busy 而不是成功）=="
"$BIN" dev lock --path "$LOCK" --hold 3s >"$WORK/hold.out" 2>&1 &
HOLDER=$!
sleep 1
out2=$("$BIN" dev lock --path "$LOCK" 2>&1)
rc2=$?
[ "$rc2" -eq 4 ] && ok "退出码 4（不是 0，说明没有静默成功）" || bad "退出码 $rc2（应为 4）"
case "$out2" in *"$HOLDER"*) ok "提示里报出了持锁 PID $HOLDER" ;; *) bad "提示里没有持锁 PID $HOLDER" ;; esac
case "$out2" in
  *"不做任何操作"* | *"does nothing"*) ok "明确说明本次什么都没做" ;;
  *) bad "没有说明「什么都没做」" ;;
esac
wait "$HOLDER" 2>/dev/null

echo
echo "== 3. 心跳停跳 + 身份确认 → 接管并终止旧持锁者 =="
cp /bin/sleep "$WORK/warpgo-hold"
"$WORK/warpgo-hold" 300 &
FAKE=$!
printf '%s %s\n' "$FAKE" "$(($(date +%s) - 600))" >"$LOCK"
out3=$("$BIN" dev lock --path "$LOCK" --stale 60s 2>&1)
rc3=$?
[ "$rc3" -eq 0 ] && ok "接管成功（退出码 0）" || bad "退出码 $rc3"
if kill -0 "$FAKE" 2>/dev/null; then
  bad "旧持锁者没被终止"
else
  ok "旧持锁者已被终止"
fi
case "$out3" in *"接管"* | *"taking the lock over"*) ok "日志说明了接管原因" ;; *) bad "日志没说明接管原因" ;; esac

echo
echo "== 4. 停跳但身份不明 → 只接管、绝不误杀 =="
cp /bin/sleep "$WORK/innocent-process"
"$WORK/innocent-process" 300 &
INNO=$!
printf '%s %s\n' "$INNO" "$(($(date +%s) - 600))" >"$LOCK"
out4=$("$BIN" dev lock --path "$LOCK" --stale 60s 2>&1)
rc4=$?
[ "$rc4" -eq 0 ] && ok "接管成功" || bad "退出码 $rc4"
if kill -0 "$INNO" 2>/dev/null; then
  ok "无辜进程没有被误杀（PID 复用防护有效）"
else
  bad "无辜进程被误杀了！"
fi
case "$out4" in *"无法确认"* | *"cannot be confirmed"*) ok "日志说明了未验证身份" ;; *) bad "日志没说明未验证身份" ;; esac

echo
echo "== 5. 旧格式锁（只有 PID、没有心跳）也要能接管 =="
cp /bin/sleep "$WORK/warpgo-legacy"
"$WORK/warpgo-legacy" 300 &
OLD=$!
printf '%s\n' "$OLD" >"$LOCK"
out5=$("$BIN" dev lock --path "$LOCK" 2>&1)
rc5=$?
[ "$rc5" -eq 0 ] && ok "旧格式锁被接管（退出码 0）" || bad "退出码 $rc5"
case "$out5" in *"旧版本"* | *"older version"*) ok "日志指出这是旧格式遗留" ;; *) bad "日志没指出旧格式" ;; esac

echo
echo "== 6. 崩溃残留（持锁进程已死）也要能接管 =="
printf '%s %s\n' 999999 "$(($(date +%s) - 600))" >"$LOCK"
out6=$("$BIN" dev lock --path "$LOCK" 2>&1)
rc6=$?
[ "$rc6" -eq 0 ] && ok "残留锁被接管" || bad "退出码 $rc6"
case "$out6" in *999999*) ok "日志报出了残留 PID" ;; *) bad "没有报出残留 PID" ;; esac

echo
printf '通过 %d 项，失败 %d 项\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
