# warpgo

Debian 上的 Cloudflare WARP 管理工具 —— [fscarmen/warp](https://gitlab.com/fscarmen/warp) 的 Go 重写版。

> **边界**：只支持 Debian。不含 Docker，不适配 macOS / 路由器 / 第三方内核。
> **定位**：不是"加个功能"，而是把最难维护、最容易出错的那一半重写成静态单二进制 ——
> 换掉的是可维护性、可观测性与可预测性，**不是**带宽（数据面用的是同一个 WireGuard 实现）。

完整设计见 **[docs/DESIGN.md](docs/DESIGN.md)**。

## 状态

按里程碑推进。当前：**M8 完成（官方 Linux Client + 钩子 + 交互菜单）**

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 骨架、单实例锁、文案、日志 | ✅ 完成 |
| M1 | 注册 / reserved / profile 生成 | ✅ 完成（真机 39/39） |
| M2 | 内核后端 + 非全局出口 + up/down/status | ✅ 完成（真机 38/38） |
| M3 | 全局模式 + fwmark 防环路 + watchdog 回滚 | ✅ 完成（真机 34/34） |
| M4 | 体检 + 保活 + systemd + 卸载 | ✅ 完成（真机 69/69） |
| M5 | 用户态后端（reserved）+ rotate 换 IP + switch 在线互换 | ✅ 完成（真机 73/73） |
| M6 | 地址族优先级（菜单 s） | ✅ 完成（真机 30/30） |
| M7 | WireProxy（菜单 w / y）—— 本地 socks5 出口 | ✅ 完成（真机 84/84） |
| M8 | 官方 Linux Client（c/l/r/z/x）+ 交互菜单 | ✅ 完成（真机 76/76） |
| M9 | BBR + 版本自更新 + 打包 | 未开始 |

**明确不做**（见 [docs/DESIGN.md](docs/DESIGN.md) §3）：

- 账户 / license / 设备管理（WARP+ 绑定、设备列表、改名、注销）—— `warpgo account` 只留一个纯本地只读视图
- `e` 分流（iptables + dnsmasq + ipset）
- **运行次数统计**：不做远程上报，**也不做本地计数**。除了你显式要求的那几条路
  （`version --check`、`rotate`、`proxy install` 的下载），warpgo 不会主动联外网

## 用法（M1 已可用）

```bash
sudo ./dist/warpgo register            # 注册设备（私钥只在本机生成，绝不外发）
sudo ./dist/warpgo account             # 查看账户（纯本地读取；--json 输出脱敏视图）

# 生成一份可交给 wg-quick 的配置（逃生口：即使将来 API 变了也能用）
sudo ./dist/warpgo profile --out /etc/wireguard/warpgo.conf
sudo wg-quick up warpgo.conf
```

`profile` 默认输出标准 wg-quick 配置（会接管默认路由，**远程机器上注意 SSH**）。
`--table-off` 只建接口、不动路由；`--no-dns` 不写 DNS 行（wg-quick 见到 DNS 会改写
`/etc/resolv.conf`）。这两个选项合起来就是「安全地在远程机器上验证隧道」。

`--family 4|6|d` 选地址族（默认 `d`）。若账户只有单栈，配置会自动只保留可用的那一族
（而不是留下一个 `::/0` 却没有 v6 地址的静默黑洞）。

## 起隧道（M2 已可用）

```bash
sudo ./dist/warpgo up        # 建接口 + 配 WireGuard + 加策略路由（默认非全局）
sudo ./dist/warpgo status    # 看状态（--json 给脚本用）
sudo ./dist/warpgo down      # 拆隧道并清空现场
```

**默认是「非全局」**：只有**以 WARP 接口地址为源**的流量走 WARP，本机自己的流量仍走原生。
这意味着它天然不会切断 SSH —— 在服务器上这不是可选项，是底线。

```bash
curl -4 -s https://1.1.1.1/cdn-cgi/trace | grep warp          # warp=off（本机走原生）
curl -4 -s --interface warp https://1.1.1.1/cdn-cgi/trace | grep warp   # warp=on（经隧道）
```

`up` 是**幂等且温和**的：接口已存在就复用、配置一致就连参数都不重下发，
所以重复执行不会换 IP。要强制重建用 `up --force`。

## 全局模式（M3 已可用，有风险）

```bash
sudo ./dist/warpgo up --mode global      # 所有流量走 WARP
```

它和默认的非全局只差在**规则**上，接口与地址完全一样 —— 所以切换模式不会重建接口。
判据正好相反：非全局下「不绑接口」必须是 `warp=off`，全局下必须是 `warp=on`。

```bash
curl -4 -s https://1.1.1.1/cdn-cgi/trace | grep warp          # 全局下应为 warp=on
```

**它会切断 SSH，所以内置了两层保险**（这不是可选项 —— 配置写错就意味着重装系统）：

1. **自检**：应用之后立刻验证「经隧道 `warp=on`」「默认路径可用」「默认路由没被改」，
   三条缺一不可。任何一条不过就**立即回滚**（实测 16 秒内）。
2. **保险丝**：变更前派生一个独立进程（`setsid`，与你的 SSH 会话无关），
   45 秒内没收到确认就**无条件回滚**。它是给「连自检都没跑完」的极端情况兜底的 ——
   比如你正是因为这个命令断了网、再也看不到输出。

`--guard-timeout` 可调超时；`--no-confirm` 让它先应用、把确认权留给你
（想先看一眼再决定时用），此时 `sudo ./dist/warpgo confirm` 表示「保留」。

回滚的目标是「回到没有 warpgo 隧道」而不是「恢复到变更前」—— 后者要复原整套规则，
复原过程一旦出错就把救命通道也搭进去了。回到干净状态更稳，之后一条 `up` 就能重来。

## 保活与开机自启（M4 已可用）

`watch` 是巡检：**健康就不动，断了才重建**。

```bash
sudo ./dist/warpgo watch --once        # 立刻检查一次并修好（脚本/手工都好用）
sudo ./dist/warpgo watch               # 常驻巡检，默认每 5 分钟一轮
sudo ./dist/warpgo watch --interval 2m # 换频率（最小 10 秒）
```

它先做**只读**体检（绑接口探测，零 DNS），只有确认链路断了才重建：

| 情况 | 行为 |
|---|---|
| 接口在、经接口拿到 `warp=on` | **什么都不做**（一个字节都不改，出口 IP 不变） |
| 接口被删了 | 重建接口 |
| 接口在、配置也对，但就是不通 | **硬复位**一次，强制重新握手（出口 IP 仍不变 —— 它是账户里分配的固定地址） |
| 单实例锁被别人占着（比如你正在手工 `up`） | 本轮跳过，汇总里记一笔 `skipped 1`，**不会假装健康** |
| 重建失败 | 记录错误，**进程继续跑**、下一轮再试（退出反而会让 systemd 陷入重启循环） |

装成开机自启的服务：

```bash
sudo ./dist/warpgo install              # 装二进制 + 写单元 + 开机自启 + 启动
sudo ./dist/warpgo install --interval 2m
systemctl status warpgod-watch          # 看它一眼
journalctl -u warpgod-watch -f          # 巡检时间线，每次都会说"健康/重建"
```

`down` 会**先停掉这个服务**再拆隧道 —— 否则保活下一轮就把隧道建回来了，
你会以为 `down` 没生效。（`down --keep-watch` 可以保留它。）

## 后端：内核 or 用户态（M5 已可用）

`reserved` 是 Cloudflare 对 WireGuard 的私有扩展（握手报文里的 3 个字节），
**香港 / 洛杉矶等部分出口必须带上它才能连通**，而内核 WireGuard 在结构上带不了它。
为此本工具内嵌了一份标准的 `wireguard-go` 作为"用户态后端"。

```bash
sudo ./dist/warpgo up --backend userspace   # 强制用户态（会把 reserved 发出去）
sudo ./dist/warpgo up --backend kernel      # 强制内核（性能最好，但 reserved 发不出去）
sudo ./dist/warpgo up                       # 默认 auto：先用内核，实测不通再切用户态
```

`--backend auto`（默认）的取舍：能上内核就上内核（性能），
真需要 reserved 的地区能自己找到出路（可用性）。
它只在账户带 reserved 时才多花一次体检的时间；reserved 全零的账户行为与 M2/M4 一致。

auto 是**自适应**的，不是"永远先试内核"：一旦某个后端实测不通、换到另一个之后，
后续的 `up` / 保活会优先沿用现在跑着的那个 —— 否则一台"只能用用户态"的机器
每修一次链路都要先拆掉可用的用户态隧道去试一个已知不通的内核后端。
想换回内核：`sudo ./dist/warpgo switch --backend kernel`。

**不做 fork**：上游那些"打过补丁的 wireguard-go"补丁的全部内容就是在收发两个方向
各改写类型字段的 3 个字节，本工具在 `conn.Bind` 上包一层即可逐字节等价 ——
于是不必在仓库里养一个一万多行的 fork。

> 顺带一提，这件事只有抓包能看清：服务端会把 reserved **原样盖回它的响应上**
> （响应变成 `02 RR RR RR`），而 WireGuard 是按 4 字节小端读报文类型的，
> 所以**只改发送方向必然失败** —— 现象是"包发出去了、服务端也回了、握手永远完不成、
> 而且没有任何报错"。

## 换出口 IP（M5 已可用）

```bash
sudo ./dist/warpgo rotate                  # 重新注册，换一个新的 WARP 出口
sudo ./dist/warpgo rotate --attempts 5     # 最多试 5 次（默认 3）
```

它会一直注册到"隧道地址确实变了"为止，用新账户重建隧道、体检通过才写盘；
**任何一步失败都会回退到原账户并把隧道建回来** —— 你的隧道可能是唯一的对外通道。
成功之后它会顺手把旧设备从服务端注销，并**如实汇报公网出口 IP 有没有变**：
账户与隧道地址一定会换，但出口 IP 由 Cloudflare 的 PoP 决定（同一 PoP 上的免费账户
共享出口），所以**可能不变** —— 这两种结果在真机上都遇到过。

它不做流媒体解锁探测（那是另一件事，且依赖第三方页面结构，随时会静默失效）。

## 在线互换（M5 已可用）

```bash
sudo ./dist/warpgo switch --backend userspace   # 菜单 k：内核 ↔ 用户态
sudo ./dist/warpgo switch --mode global         # 菜单 g：全局 ↔ 非全局
```

出口地址不会因为互换而改变（它是账户里分配的固定地址）。
若保活服务已经装好，`switch` 会**同步改写它的启动参数并重启** ——
否则它下一轮就按旧参数把隧道重建回去，你会以为 `switch` 没生效。

## 地址族优先级（M6 已可用，菜单 s）

同一台机器同时有 IPv4 与 IPv6 出口时，访问双栈域名先走哪一族由 glibc 决定，
`/etc/gai.conf` 可以影响它：

```bash
sudo ./dist/warpgo priority        # 看当前状态 + 实测的解析顺序
sudo ./dist/warpgo priority 4      # IPv4 优先
sudo ./dist/warpgo priority 6      # IPv6 优先
sudo ./dist/warpgo priority d      # 回到系统默认（= 移除我们写的那两行）
```

真机实测（Debian 12 / glibc 2.36）有个值得知道的事实：

| 状态 | `getent ahosts www.cloudflare.com` 的第一条 |
|---|---|
| 系统默认 | IPv6 |
| `4` | IPv4 —— **真的生效** |
| `6` | IPv6 —— **与默认完全一样** |

也就是说 `4` 才是那个真正的开关：`6` 之所以"也是 IPv6 优先"，是因为 glibc 的默认
本来就是 IPv6 优先；`priority 6` 真正起作用的一步是**把 IPv4 优先那行删掉**
（万一机器上有别的工具写过它）。`priority` 命令会如实告诉你这一点，不假装对称。

实现上只增删 `/etc/gai.conf` 里那两行，其余内容（包括 glibc 自带的那行被注释掉的
v4 写法）逐字节保留 —— 所以 `d` 能把你进去时的文件原样还回来，不需要任何备份文件。

`priority` 无参数时会用 `getent ahosts` **实测**一次解析顺序：只看文件里有没有那行
是不够的（一个什么都没做的实现也能让文件"看起来对"），而 **Go 自己的解析器不读
gai.conf**，用它去测会得出"gai.conf 无效"的错误结论。

## WireProxy：把 WARP 做成一个本地 socks5（M7 已可用，菜单 w / y）

```bash
sudo ./dist/warpgo proxy install              # 下载 + 校验 + 生成配置 + 装成服务
sudo ./dist/warpgo proxy install --port 25344 # 换端口（默认 40000）
sudo ./dist/warpgo proxy on | off             # 开关
sudo ./dist/warpgo proxy status [--json]      # 看状态（会经 socks5 实测一次）

curl -x socks5h://127.0.0.1:40000 https://1.1.1.1/cdn-cgi/trace | grep warp   # warp=on
```

它和隧道模式是**两条不同的路**：不起网络接口、不碰路由，只在用户态跑一个小的
wireguard 客户端，把 socks5 挂在 **127.0.0.1**（不对外暴露）。
"哪些流量走 WARP"由你自己决定 —— 浏览器、`curl -x`、程序里的代理设置。

几个刻意的做法：

- **版本默认钉死**（`v1.1.3`），不追 latest —— 装上去的是 root 运行的可执行文件，可复现优先。
- **校验和强制**：从发布页取 `checksums.txt` 比对；取不到就报错，并给出两条出路
  （`--sha256` / `--from` 离线安装），**不静默降级成不校验**。
  发布仓库是 `windtf/wireproxy`（上游 `pufferffish` 已迁移到那里）。
- 装完先 `wireproxy -n -c` 自检配置，再启动服务，最后**经 socks5 实测** `warp=on` 才算成功。
- **reserved 在这里发不出去**（wireproxy 用的是官方 wireguard-go）。账户带 reserved 时
  会明确警告 —— 需要 reserved 的地区请改用隧道模式：`warpgo switch --backend userspace`。
- `warpgo uninstall` 会一并撤掉它；`proxy uninstall --keep-binary` 可以留下那个二进制。

## 官方 Linux Client（M8 已可用，菜单 c / l / r）

用 Cloudflare **官方**的客户端（`cloudflare-warp` 包）做出口。默认装成
**socks5 代理模式** —— 它一个字节的路由都不动：

```bash
sudo ./dist/warpgo client install                  # 默认 proxy 模式（安全）
sudo ./dist/warpgo client install --port 25344     # 换 socks5 端口（默认 40000）
sudo ./dist/warpgo client on | off                 # 开关（菜单 r）
sudo ./dist/warpgo client status [--json]          # 看状态

curl -x socks5h://127.0.0.1:40000 https://1.1.1.1/cdn-cgi/trace | grep warp   # warp=on

sudo ./dist/warpgo client mode warp                # 切到 WARP 模式（会接管默认路由！）
sudo ./dist/warpgo client uninstall                # 卸载（含 apt 源与钩子）
```

> ⚠️ **`--mode warp` 会接管默认路由**（整机流量走 WARP）。在一台只能靠 SSH
> 进入的服务器上，这有可能把你关在门外 —— 请确认能通过控制台 / VNC 恢复再用。
> 只想给本机多一个出口，用默认的 proxy 模式就够了。

几个刻意的做法：

- **`warp-cli -j` 才是稳的解析入口**。`settings` 的文本输出长这样：
  `(user set)	Mode: WarpProxy on port 40000` —— 前面多一列"取值来源"、值后面还跟着端口，
  按正则刮文本迟早被下一个版本打垮。所以**先试 `-j`（结构化 JSON）、不认才退回文本**。
- **apt 源与密钥环只删自己建的**：安装时把"新建了哪几个文件"记进状态，
  卸载时只删那几份 —— 机器上本来就有官方源的话，那是你的配置，不是我们的。
- **钩子（菜单 z / x）用 systemd drop-in 挂载**，而不是去 `sed` 包自带的单元文件：
  后者由 dpkg 管，下次 `apt upgrade` 会静默覆盖它（症状是"重启之后规则又没了"）。
  钩子用的表号与规则优先级**与 warpgo 自己的隧道完全错开**，清理互不干扰。
- `hook add|del` 是**隐藏子命令**，被 `warp-svc` 的 `ExecStartPost` / `ExecStop` 调用；
  它们**永远以 0 退出**（非零会让 systemd 把整个单元标成 failed）。
- 总卸载 `warpgo uninstall` 会联动撤销它，但**没有状态文件就不动** ——
  机器上本来就装着官方客户端的话，我们无权把它删掉。
- 真实客户端**只跑 proxy 模式**做验收；warp 模式那条路要显式设
  `M8_TEST_WARP=1` 才会跑（原因见上面的警告）。

## 交互菜单（M8 已可用）

```bash
sudo ./dist/warpgo menu
```

菜单是**薄封装**：每个选项最终都翻译成一条已经存在的子命令再执行。
好处是菜单与命令行走**同一条代码路径**（不会出现"命令行能修好、菜单修不好"），
而且每一项都能被脚本复现 —— 把菜单里的命令抄下来就是那条命令。

## 卸载（M4 已可用）

```bash
sudo ./dist/warpgo uninstall            # 单元、隧道、二进制、DNS 改动全部还原
sudo ./dist/warpgo uninstall --purge    # 连账户一起删（下次注册会拿到新的 WARP IP）
sudo ./dist/warpgo uninstall --keep-binary
```

**默认保留账户**（`/etc/warpgo/account.json`）—— 它是一个身份，
删掉就意味着下次注册会换一个 WARP 出口地址。日志会明确告诉你它在哪。

`--purge` 只删自己认识的文件：状态目录里一旦出现陌生文件就**整个目录跳过**，
一个都不删并把清单打出来 —— 因为 `WARPGO_ETC` 可以指向任何地方，
无脑清空是不可逆的。

## DNS 怎么处理

**默认不碰 `/etc/resolv.conf`。** 只有显式要求才改：

```bash
sudo ./dist/warpgo up --dns 1.1.1.1,1.0.0.1
```

改之前的原文会被备份到 `/etc/warpgo/resolv.conf.origin`，
`down` / `uninstall` 会还原它，**并且只在还原成功之后才删备份**。

这一条是有来历的：旧版卸载后，`/etc/resolv.conf` 里还是安装时写进去的
`nameserver 1.1.1.1`，而备份 `/etc/resolv.conf.origin` 已经被删 ——
用户连"原来是什么"都无从得知。另外，如果 `/etc/resolv.conf` 是符号链接
（systemd-resolved 的机器就是），我们**拒绝改写**，因为把它换成普通文件会让
resolved 的配置再也无法生效，而那是用户没做过的事。

## 构建与测试（在 Debian 上）

```bash
./build.sh                 # 产出 dist/warpgo
go test ./...              # 单元测试（不需要 root、不需要网络）
./dist/warpgo version
./dist/warpgo dev lock --hold 3s      # 演练单实例锁

tests/e2e/m0-lock.sh                   # M0 真机验收（单实例锁，16 项）
tests/e2e/m1-register.sh               # M1 真机验收（注册 + 隧道，39 项）
tests/e2e/m2-updown.sh                 # M2 真机验收（幂等 / 零残留 / SSH 安全，38 项）
tests/e2e/m3-global.sh                 # M3 真机验收（全局 + 回滚，34 项）
tests/e2e/m4-watch.sh                  # M4 真机验收（保活 + systemd + 卸载，69 项）
tests/e2e/m5-userspace.sh              # M5 真机验收（用户态后端 + 抓包验 reserved + 换 IP，73 项）
tests/e2e/m6-priority.sh               # M6 真机验收（gai.conf 三态 + getent 实测顺序，30 项）
tests/e2e/m7-proxy.sh                  # M7 真机验收（真实下载 + socks5 实测 + 零残留，84 项）
tests/e2e/m8-client.sh                 # M8 真机验收（真实装包 + 钩子 + 菜单，76 项）
```

`m5-userspace.sh` 会**真的在两套后端之间来回切换**、**真的重新注册一个新账户**
（所以这台机器的 WARP 出口地址会变、旧设备会被从服务端注销），
并且用 `tcpdump` 抓包来断言"报文的类型字段里到底带了什么字节" ——
这是唯一能证明 reserved 真的生效/没生效的办法（见上面那段）。

`m7-proxy.sh` 会**真的从 GitHub 下载 4MB 的第三方二进制**、真的装 systemd 单元、
真的起进程，并且每条连通性断言都是**另起一条 `curl --proxy socks5h://…`** ——
不看被测程序自己的结论。

`m6-priority.sh` 会真的改 `/etc/gai.conf`，结束时按 sha256 逐字节还原；
它用 `getent ahosts` 验效果，因为那才是用户实际会遇到的解析路径。

`m8-client.sh` 会**真的装 `cloudflare-warp`**（加 apt 源 + 导公钥 + apt 装包）、
真的注册设备、真的经 socks5 发请求。它有一条**安全边界**：真实客户端只跑
**proxy 模式**（不动路由），钩子用**一个 dummy 的 `CloudflareWARP` 接口**来验 ——
这样"warp 模式接管默认路由、可能切断 SSH"那一步不会在例行验收里被无意触发
（要跑它得显式设 `M8_TEST_WARP=1`）。

真机验收脚本会**动真实的网络与 systemd**，所以它们都自带 trap 兜底，
并且建议用 `setsid` 在后台跑、再挂一个"死人开关"（M3 那个脚本还会**故意断网几秒** ——
那正是它要验证的场景）。

本项目的编译 / 测试 / 调试**一律在 Debian 上进行**：Windows 只用来编辑代码，
不装工具链，也就不会出现"在我机器上能跑"的问题。

## 几条刻意的设计取舍

- **零外部依赖**（M0~M1 纯标准库）。上游 shell 版的教训是：依赖越多，越容易被上游改动打穿。
- **不生成 PostUp/PostDown 钩子脚本**。上游为这些生成物吃过不少苦（升级覆盖补丁、
  `[detached]` 规则清不掉、`Predown` 拼写错误、hook 非 0 中断 wg-quick）——Go 进程自己持有状态。
- **所有"添加"操作先清后加**。上游 `ip rule add` 不幂等，残留累积到过 4681 条。
- **删除一律按句柄/pref，不解析文本**。接口删掉后 `ip rule show` 会变成
  `oif warp [detached]`，按字符串匹配永远删不掉。
- **体检只打 IP 字面量**（零 DNS）。上游兜底链全靠 DNS，DNS 一抖主路径与兜底一起失败。
- **健康就不动**。保活巡检发现链路正常时什么都不做；只有确认断链才重建、才换 IP。
- **拿不到单实例锁时明确报错**，绝不静默返回成功。
- **`reserved` 按 base64 原始字节解**，不做字符码点运算。上游用 `printf "%d" "'c"`
  在 UTF-8 locale 下把 161 算成 737，超过 255 后被补丁版 `wireguard-go` 静默回落成
  `[0,0,0]`，在需要 reserved 的地区握手被丢弃 —— 且完全没有任何报错。
- **`endpoint` 一律取 `host`，并修正端口为 0 的情况**。API 返回的 `endpoint.v4/.v6`
  端口是 `0`，照抄会得到一个「看着有值、永远连不上」的端点。
- **解析对服务端字段形态不敏感**。`created`/`updated` 有时是字符串、有时是 `{}`；
  用 `time.Time` 会让一个无关字段把整次注册搞崩，所以用宽容类型。
- **`account --json` 输出脱敏视图**（不含私钥与令牌），让「打印」这件事默认是安全的。
- **规则与路由「先清后加」，删除只按 pref / 表号**。上游用 `ip rule add || true`
  吞掉 EEXIST，于是每跑一次多一条，实测累积到过 4681 条；删除又按 `ip rule show` 的
  文本匹配，接口一删就再也匹配不上。这里全程只用数字标识符。
- **策略路由的规则自己实现**（不依赖第三方库的 `RuleAdd`）。实测在较新的内核上
  那个调用会返回 EINVAL，而同样的消息手工构造就成功 —— 规则是幂等与零残留的唯一依据，
  不把它押在别人的内核适配上。接口/地址/路由仍用成熟库。
- **危险操作先上保险，再动手**。全局模式会派生一个独立进程作为「保险丝」，
  它必须在变更**之前**就位 —— 顺序反了（先改网络再派生）等于没有保险。
- **回滚目标是「网络一定能用」，不是「恢复原样」**。后者要复原整套规则，
  一旦复原过程出错就把唯一的救命通道也搭进去了。
- **体检必须只读**。上游的"判断源"自己会重启接口、改路由 —— 于是"探测"变成了
  "故障放大器"。这里的体检只发 HTTP，不改任何状态。
- **"重建"要连"接口在但不通"一起管**。只在接口消失时才重建是不够的：
  现实中最常见的故障是接口好好的、配置也对，就是不通。
- **保活失败时不退出**。退出会让 systemd 每 30 秒重启一次，日志里堆满没有信息量的
  重启记录，而"端点不可达"并不会因为重启而自愈。
- **锁被占是正常情形，但不能沉默**。跳过就跳过，可汇总里必须留下 `skipped 1` ——
  上游那个"静默 return 0"曾经让人把"什么都没做"读成"一切都好"。
- **改过的东西要能还回去**。DNS 只是第一条：备份先写、还原成功才删备份，
  目录里出现不认识的文件就拒绝整目录清理。
- **单个静态二进制，零运行时依赖**（不要求系统装 `wg`/`wg-quick`/`ip`/`jq`/`python`）。
  M2 起引入了几个 Go 库（netlink / wgctrl），它们编译进二进制，不改变这一点。

## 许可

GPL-3.0。注册 API 的请求形状与常量取自 fscarmen/warp 的 `api.sh`（派生作品），
沿用 GPL-3.0 并保留原作者署名。
