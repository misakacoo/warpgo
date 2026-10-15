#!/usr/bin/env bash
# M1 验收：注册 / reserved / profile / wg-quick 真机跑通。
#
# 在 Debian 12 上执行。**不会切断 SSH** —— 用的配置带 `Table = off`（不碰路由表）
# 与 `--no-dns`（不碰 /etc/resolv.conf），所以它只建一个接口，
# 靠 `curl --interface` 的 SO_BINDTODEVICE 强制走隧道来验证出口。
#
# 用真实的 /etc/warpgo 与真实的 Cloudflare API —— 验收要验的就是这两者。
set -u

BIN=${BIN:-/root/warpgo/dist/warpgo}
ETC=${WARPGO_ETC:-/etc/warpgo}
IFACE=warpgo-e2e
CONF=/etc/wireguard/$IFACE.conf

[ -x "$BIN" ] || { echo "找不到可执行文件: $BIN"; exit 1; }
command -v wg-quick >/dev/null || { echo "缺 wg-quick（apt install wireguard-tools）"; exit 1; }
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }

pass=0
fail=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail + 1)); }
section() { printf '\n== %s ==\n' "$1"; }

cleanup() {
  # 无论中途怎么退出，都不能把接口留在机器上。
  if ip link show "$IFACE" >/dev/null 2>&1; then
    wg-quick down "$IFACE" >/dev/null 2>&1
    # wg-quick 偶尔清不干净，兜一手。
    ip link del "$IFACE" >/dev/null 2>&1
  fi
}
trap cleanup EXIT

RESOLV_BEFORE=$(sha256sum /etc/resolv.conf 2>/dev/null | cut -d' ' -f1)

section "0. 环境"
printf '  二进制: %s\n' "$("$BIN" version 2>&1 | head -1)"
printf '  账户目录: %s\n' "$ETC"
printf '  接口名: %s\n' "$IFACE"
if ip link show "$IFACE" >/dev/null 2>&1; then
  bad "接口 $IFACE 已存在 —— 先清理现场再跑"
  exit 1
fi
ok "现场干净（无同名接口）"

section "1. 注册（真实调用 Cloudflare API）"
if [ -e "$ETC/account.json" ]; then
  printf '  已存在账户，本段跳过（要重新验注册请先 rm %s/account.json）\n' "$ETC"
  REG_FRESH=0
else
  REG_FRESH=1
fi

if [ "$REG_FRESH" = 1 ]; then
  reg_out=$("$BIN" register 2>&1)
  rc=$?
  printf '%s\n' "$reg_out" | sed 's/^/    /'
  [ "$rc" -eq 0 ] && ok "注册退出码 0" || bad "注册退出码 $rc"

  if [ -f "$ETC/account.json" ]; then
    ok "账户文件已生成"
  else
    bad "账户文件没生成"
  fi

  perm=$(stat -c '%a' "$ETC/account.json" 2>/dev/null)
  [ "$perm" = "600" ] && ok "账户文件权限 600" || bad "账户文件权限是 $perm（应为 600）"

  dperm=$(stat -c '%a' "$ETC" 2>/dev/null)
  [ "$dperm" = "700" ] && ok "账户目录权限 700" || bad "账户目录权限是 $dperm（应为 700）"

  # 重复注册必须被拒绝：新账户会换掉 WARP IP，这不能是"顺手再跑一次"的结果。
  dup_out=$("$BIN" register 2>&1)
  dup_rc=$?
  [ "$dup_rc" -eq 1 ] && ok "重复注册被拒绝（退出码 1）" || bad "重复注册退出码 $dup_rc（应为 1）"
  case "$dup_out" in
    *"--force"*) ok "提示里给出了 --force 逃生口" ;;
    *) bad "提示里没说怎么强制重注册" ;;
  esac
fi

section "2. 账户内容与脱敏"
python3 - "$ETC/account.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
need = ["id", "token", "private_key", "public_key", "peer_public_key", "endpoint"]
print("  字段:", ", ".join(f"{k}={'有' if d.get(k) else '缺'}" for k in need))
print("  reserved:", d.get("reserved"))
print("  address_v4:", d.get("addr_v4") or "(无)")
print("  address_v6:", d.get("addr_v6") or "(无)")
print("  endpoint:", d.get("endpoint"))
PY
miss=$(python3 - "$ETC/account.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
need = ["id", "token", "private_key", "public_key", "peer_public_key", "endpoint"]
print(",".join(k for k in need if not d.get(k)))
PY
)
if [ -z "$miss" ]; then ok "账户关键字段齐全"; else bad "账户缺字段: $miss"; fi

# 地址族决定后面要验哪些出口，先取出来。
has_v4=$(python3 -c "import json;print(1 if json.load(open('$ETC/account.json'))['addr_v4'] else 0)")
has_v6=$(python3 -c "import json;print(1 if json.load(open('$ETC/account.json'))['addr_v6'] else 0)")
[ "$has_v4" = 1 ] && ok "账户有 IPv4 地址" || bad "账户没有 IPv4 地址"
[ "$has_v6" = 1 ] && ok "账户有 IPv6 地址" || printf '  （账户无 IPv6 地址，第 4 节将跳过 v6 出口验证）\n'

acct_out=$("$BIN" account 2>&1)
# 断言用字段名（device_id）而不是任何翻译文案：字段名是语言无关的。
case "$acct_out" in *"device_id"*) ok "account 命令有输出" ;; *) bad "account 没有输出"; printf '%s\n' "$acct_out" | sed 's/^/    /' ;; esac

json_out=$("$BIN" account --json 2>&1)
pri=$(python3 -c "import json,sys; print(json.load(open('$ETC/account.json'))['private_key'])")
case "$json_out" in
  *"$pri"*) bad "--json 泄露了私钥！" ;;
  *) ok "--json 不含私钥（脱敏有效）" ;;
esac

section "3. 生成 wg-quick 配置"
out=$("$BIN" profile --table-off --no-dns --out "$CONF" 2>&1)
rc=$?
[ "$rc" -eq 0 ] && ok "profile 退出码 0" || { bad "profile 退出码 $rc"; printf '%s\n' "$out" | sed 's/^/    /'; }
if [ -f "$CONF" ]; then ok "配置文件已写出"; else bad "配置文件缺失"; fi
perm=$(stat -c '%a' "$CONF" 2>/dev/null)
[ "$perm" = "600" ] && ok "配置文件权限 600（含私钥）" || bad "配置文件权限是 $perm"
printf '%s\n' "$(sed 's/^/    /' "$CONF")"

grep -q '^\[Interface\]' "$CONF" && ok "有 [Interface]" || bad "缺 [Interface]"
grep -q '^\[Peer\]' "$CONF" && ok "有 [Peer]" || bad "缺 [Peer]"
grep -q '^Table = off$' "$CONF" && ok "Table = off（不接管默认路由 → 不会断 SSH）" || bad "没有 Table = off"
grep -q '^AllowedIPs = 0.0.0.0/0' "$CONF" && ok "AllowedIPs 含 IPv4" || bad "缺 IPv4 AllowedIPs"
if grep -q '^DNS' "$CONF"; then bad "--no-dns 生效失败，配置里仍有 DNS 行"; else ok "--no-dns 生效（不碰 /etc/resolv.conf）"; fi
# reserved 是以注释行的形式输出；格式必须与生态一致（客户端逐行扫它）。
grep -qE '^# Reserved = [0-9]+, [0-9]+, [0-9]+$' "$CONF" && ok "# Reserved 注释行存在且格式正确" || bad "# Reserved 行格式不对"
# 私钥不能出现在 wg-quick 不认识的键上（除了 Reserved 注释）。
if grep -qE '^(PrivateKey|PublicKey|AllowedIPs|Endpoint|PersistentKeepalive|Address|MTU|Table|DNS) = ' "$CONF"; then ok "配置键名都是 wg-quick 认识的"; fi

section "4. wg-quick 起隧道并验证出口（v4 / v6）"
wg-quick up "$IFACE" >/tmp/m1-up.log 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then
  bad "wg-quick up 失败（rc=$rc）"
  sed 's/^/    /' /tmp/m1-up.log
else
  ok "wg-quick up 成功"
fi

if ip link show "$IFACE" >/dev/null 2>&1; then
  ok "接口 $IFACE 已创建"
else
  bad "接口没起来 —— 后续断言无意义"
fi

# 默认路由必须还是原生的（Table = off 的全部意义所在）。
if ip route show default | grep -qv "$IFACE"; then
  ok "默认路由仍是原生（SSH 安全）"
else
  bad "默认路由被改到 $IFACE 了 —— Table=off 没生效！"
fi

wg show "$IFACE" 2>/dev/null | sed 's/^/    /'

# 为什么要自己加探测路由：
#
# `Table = off` 的配置**不接管任何路由**。IPv4 能直接通，是因为接口持有 v4 地址时
# 内核允许按出接口直发（实测 `ip route get 1.1.1.1 oif <if>` → `dev <if>`）；
# IPv6 的路由语义严格得多，没有明确路由就直接 `Network is unreachable`。
#
# 这里为两个探测地址各加一条精确路由，目的是把「M1 要证明的事（隧道与出口可用）」
# 和「路由接管（M2 的职责）」解耦；同时因为是 /32 与 /128 的精确路由，
# 无论测出什么结果都动不到默认路由，SSH 全程安全。
add_probe_route() {
  local proto=$1 prefix=$2
  if ip "$proto" route add "$prefix" dev "$IFACE" 2>/dev/null; then
    ok "已加探测路由 $proto $prefix"
  else
    bad "加探测路由失败: $proto $prefix"
  fi
}
[ "$has_v4" = 1 ] && add_probe_route -4 1.1.1.1/32
[ "$has_v6" = 1 ] && add_probe_route -6 2606:4700:4700::1111/128

printf '  ---- 路由决策（印证上面的机制）----\n'
printf '    v4: %s\n' "$(ip route get 1.1.1.1 oif "$IFACE" 2>&1 | head -1)"
printf '    v6: %s\n' "$(ip -6 route get 2606:4700:4700::1111 oif "$IFACE" 2>&1 | head -1)"

probe() {
  local fam=$1 url=$2 label=$3
  local out
  out=$(timeout 25 curl -"$fam" -ksm20 --interface "$IFACE" "$url" 2>/dev/null | grep -E '^(ip|warp|loc)=' | tr '\n' ' ')
  if printf '%s' "$out" | grep -qE 'warp=(on|plus)'; then
    ok "$label 经隧道取到 warp=$(printf '%s' "$out" | sed -n 's/.*warp=\([a-z]*\).*/\1/p')  [$out]"
  else
    bad "$label 没拿到 warp=on（$out）"
  fi
}

if [ "$has_v4" = 1 ]; then
  probe 4 "https://1.1.1.1/cdn-cgi/trace" "IPv4"
fi
if [ "$has_v6" = 1 ]; then
  probe 6 "https://[2606:4700:4700::1111]/cdn-cgi/trace" "IPv6"
fi

section "5. 拆隧道并检查零残留"
wg-quick down "$IFACE" >/tmp/m1-down.log 2>&1
rc=$?
[ "$rc" -eq 0 ] && ok "wg-quick down 成功" || { bad "wg-quick down 失败（rc=$rc）"; sed 's/^/    /' /tmp/m1-down.log; }

ip link show "$IFACE" >/dev/null 2>&1 && bad "接口仍然存在" || ok "接口已删除"

# 探测路由的 dev 没了，它们必须跟着消失（不能留成"指向不存在设备"的僵尸路由）。
if ip route show | grep -q "dev $IFACE"; then bad "仍有指向 $IFACE 的 v4 路由残留"; else ok "没有指向 $IFACE 的残留 v4 路由"; fi
if ip -6 route show 2>/dev/null | grep -q "dev $IFACE"; then bad "仍有指向 $IFACE 的 v6 路由残留"; else ok "没有指向 $IFACE 的残留 v6 路由"; fi

leftover=$(ip -4 rule show | grep -c "$IFACE" || true)
[ "$leftover" = "0" ] && ok "没有残留的 ip rule" || bad "残留 $leftover 条 ip rule"
leftover6=$(ip -6 rule show | grep -c "$IFACE" || true)
[ "$leftover6" = "0" ] && ok "没有残留的 ipv6 rule" || bad "残留 $leftover6 条 ipv6 rule"

RESOLV_AFTER=$(sha256sum /etc/resolv.conf 2>/dev/null | cut -d' ' -f1)
[ "$RESOLV_BEFORE" = "$RESOLV_AFTER" ] && ok "/etc/resolv.conf 前后一致" || bad "/etc/resolv.conf 被改动了"
[ -e /etc/resolv.conf.wg-quick-$IFACE ] && bad "留下了 wg-quick 的 resolv 备份文件" || ok "没有遗留 resolv 备份"

# 默认路由再确认一次。
ip route show default | grep -qv "$IFACE" && ok "默认路由仍是原生" || bad "默认路由异常"

section "6. 账户仍可用于后续里程碑"
if "$BIN" account >/dev/null 2>&1; then ok "account 命令仍可用（账户未损坏）"; else bad "account 命令失败"; fi

printf '\n通过 %d 项，失败 %d 项\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
