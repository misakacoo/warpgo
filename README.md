# warpgo

**Debian 上的 Cloudflare WARP 管理工具** —— 单个静态二进制，零运行时依赖。

warpgo 负责把一台 Debian 机器接入 Cloudflare WARP：注册设备、建隧道、配路由、
保活、换出口、做成 socks5 代理，或者直接接管整机流量 —— 全部由一个可执行文件完成，
不需要 `bash` 脚本、`jq`、`python`、`wg`、`wg-quick` 或任何网络工具包。

> **平台边界**：只支持 Debian（启动时检查 `/etc/debian_version`，非 Debian 直接拒绝）。
> 不适配 Alpine / OpenWrt / macOS，不含 Docker 方案，不做发行版探测矩阵。

> **安全底线**：默认**非全局** —— 只有以 WARP 接口地址为源的流量走 WARP，
> 本机自身流量仍走原生网络。这意味着它天然不会切断 SSH。
> 全局模式需要显式指定，并且自带两层回滚保险。

---

## 目录

- [特性](#特性)
- [安装](#安装)
- [快速开始](#快速开始)
- [命令参考](#命令参考)
- [工作模式：非全局（默认）与全局](#工作模式非全局默认与全局)
- [隧道后端：内核 / 用户态](#隧道后端内核--用户态)
- [地址族：`--stack` 与 `priority`](#地址族stack-与-priority)
- [临时关闭与恢复](#临时关闭与恢复)
- [保活与开机自启](#保活与开机自启)
- [换出口 IP](#换出口-ip)
- [在线互换](#在线互换)
- [WireProxy：把 WARP 做成一个本地 socks5](#wireproxy把-warp-做成一个本地-socks5)
- [官方 Linux Client](#官方-linux-client)
- [交互菜单](#交互菜单)
- [查询新版本与自我更新](#查询新版本与自我更新)
- [DNS 怎么处理](#dns-怎么处理)
- [文件与路径](#文件与路径)
- [卸载](#卸载)
- [构建、测试与发布](#构建测试与发布)
- [退出码](#退出码)
- [设计取舍](#设计取舍)
- [许可](#许可)

---

## 特性

| | |
|---|---|
| **单二进制** | `CGO_ENABLED=0` 的静态 ELF，拷进去就能跑。不要求系统上有任何网络工具 |
| **不切断 SSH** | 默认非全局模式，只对指定源地址加策略路由，本机流量不受影响 |
| **两种后端** | 内核 WireGuard（性能最好）与用户态 `wireguard-go`（能发 `reserved`）。默认 `auto`，实测不通会自己换 |
| **三种出口方式** | 网络接口（`up`）、本地 socks5（`proxy`）、官方客户端（`client`） |
| **幂等** | 任何命令重复执行结果一致；所有"添加"操作都先清后加，不会累积残留规则 |
| **可还原** | `down` / `off` / `uninstall` 会把自己改过的路由、策略规则、DNS、systemd 单元逐个还原 |
| **健康就不动** | 保活巡检只在确认链路断了才重建 —— 出口 IP 不会无谓抖动 |
| **零遥测** | 不做任何上报，也不做本地计数。除你显式要求的那几条路径（版本查询 / 自更新 / 下载 WireProxy / 安装官方客户端）外，不主动联外网 |
| **全中文** | 命令行、日志、菜单一律简体中文，不按 `LANG` 推断，也没有 `--lang` 开关 |

---

## 安装

### 从 Release 安装（推荐）

发布产物按架构打包：`warpgo_<版本>_linux_<架构>.tar.gz`，并附带 `checksums.txt`。

```bash
VERSION=v0.1.0
ARCH=amd64          # 或 arm64

BASE="https://github.com/misakacoo/warpgo/releases/download/${VERSION}"
curl -fLO "${BASE}/warpgo_${VERSION}_linux_${ARCH}.tar.gz"
curl -fLO "${BASE}/checksums.txt"

# 校验下载物（务必做 —— 这是唯一能确认文件没有被截断或篡改的办法）
sha256sum -c checksums.txt

tar -xzf "warpgo_${VERSION}_linux_${ARCH}.tar.gz"     # 解出 ./warpgo/{warpgo,README.md,LICENSE}
sudo install -m 0755 ./warpgo/warpgo /usr/bin/warpgo
```

检查一下：

```bash
warpgo version
```

### 从源码构建

需要 Go（版本见 `go.mod` 的 `go` 指令）与 `git`：

```bash
git clone https://github.com/misakacoo/warpgo.git
cd warpgo
VERSION=0.1.0 ./build.sh          # 产出 dist/warpgo
sudo install -m 0755 dist/warpgo /usr/bin/warpgo
```

`build.sh` 会把版本号、提交哈希与构建时间注入二进制（`-trimpath`、`CGO_ENABLED=0`），
所以 `warpgo version` 能如实报告"这个二进制是哪份代码编出来的"。

### 环境要求

- Debian —— 检查方式是读 `/etc/debian_version`，只判断"是不是 Debian"，不设最低版本。
  会改系统状态的子命令（`up` / `watch` / `install` / `switch` / `priority` / `proxy` 等）
  都会先做这一步，非 Debian 直接以退出码 `5` 拒绝并说明原因。发布产物提供 `amd64` 与 `arm64`。
- root 权限（配置接口、路由与 systemd 单元需要）
- 内核自带 `wireguard` 模块 —— **不需要 DKMS**。没有也不要紧，用
  `--backend userspace` 即可（用户态后端内嵌在二进制里）

---

## 快速开始

```bash
# 1. 注册一台 WARP 设备（私钥在本机生成，绝不外发）
sudo warpgo register

# 2. 建立隧道（默认非全局，不会碰你的 SSH）
sudo warpgo up

# 3. 看看状态
sudo warpgo status
```

验证隧道真的通了 —— 两条命令要给出相反的结果：

```bash
curl -4 -s https://1.1.1.1/cdn-cgi/trace | grep warp
# warp=off              本机流量走原生出口

curl -4 -s --interface warp https://1.1.1.1/cdn-cgi/trace | grep warp
# warp=on               经隧道出去的流量
```

装上保活，让它开机自启并在断线时自己修好：

```bash
sudo warpgo install
```

就这些。剩下的能力按"你需要什么"往下读。

---

## 命令参考

`warpgo <子命令> [选项]`。所有子命令都是非交互的，交互菜单只是它们的薄封装。

### 账户

| 命令 | 说明 |
|---|---|
| `warpgo register [--force]` | 注册一台新的 WARP 设备并保存账户。已有账户时会拒绝，除非 `--force`（会换掉你的 WARP IP） |
| `warpgo account [--json]` | 查看本机账户。**纯本地读取，不联网**；`--json` 输出脱敏视图（不含私钥与令牌） |
| `warpgo profile [--out FILE] [--family 4\|6\|d] [--table-off] [--no-dns]` | 输出可直接交给 `wg-quick` 的标准配置（逃生口） |

### 隧道

| 命令 | 说明 |
|---|---|
| `warpgo up [--mode ...] [--backend ...] [--stack ...] [--force]` | 建立隧道。幂等：已存在且配置一致就什么都不做 |
| `warpgo off` | 临时关闭：拆隧道、停保活，**保留配置** |
| `warpgo on` | 恢复：按上次的配置重建，并把保活拉回来 |
| `warpgo down [--keep-watch]` | 拆隧道并清空全部残留（含运行态） |
| `warpgo status [--json]` | 当前状态 |
| `warpgo rotate [--attempts N]` | 重新注册，换一个出口地址 |
| `warpgo switch [--backend ...] [--mode ...]` | 在线互换后端或工作模式 |
| `warpgo priority [4\|6\|d] [--json]` | 地址族优先级（`/etc/gai.conf`） |

### 保活

| 命令 | 说明 |
|---|---|
| `warpgo watch [--once] [--interval 5m]` | 巡检：健康不动，断了才重建 |
| `warpgo install [--interval 5m] [--no-start]` | 装成 systemd 服务（开机自启 + 立即启动） |
| `warpgo uninstall [--purge] [--keep-binary]` | 撤干净：单元、隧道、二进制、WireProxy、官方客户端、DNS 改动 |
| `warpgo confirm` | 保留本次变更并解除回滚保险丝 |

### 附加出口

| 命令 | 说明 |
|---|---|
| `warpgo proxy install [--port 40000] [--version v1.1.3] [--sha256 ...] [--from FILE]` | 安装 WireProxy 并做成服务 |
| `warpgo proxy on\|off\|status [--json]` | WireProxy 开关与状态 |
| `warpgo proxy uninstall [--keep-binary]` | 撤掉 WireProxy |
| `warpgo client install [--mode proxy\|warp] [--port 40000]` | 安装 Cloudflare 官方客户端 |
| `warpgo client mode proxy\|warp` | 切换官方客户端的模式 |
| `warpgo client on\|off\|status [--json]` | 官方客户端开关与状态 |
| `warpgo client uninstall [--keep-repo]` | 卸载官方客户端（含 apt 源、钩子与 warp-svc 自己留下的策略规则） |

### 其他

| 命令 | 说明 |
|---|---|
| `warpgo menu` | 交互菜单 |
| `warpgo version [--check] [--self-update]` | 版本信息 / 查新版本 / 自我更新 |
| `warpgo help` | 完整帮助 |

### 常用选项

```
--backend auto|kernel|userspace   隧道后端；auto 先用内核，实测不通再切用户态
--mode non-global|global          出口接管方式；默认非全局
--stack 4|6|d                     隧道携带哪几族地址（4=只 IPv4，6=只 IPv6，d=双栈）
--ifname / --mtu / --endpoint / --dns
                                  接口名 / MTU / 对端端点 / 隧道内 DNS
-v, --verbose                     调试日志
-q, --quiet                       只显示警告与错误
```

`reserved` 是 `userspace` 的别名 —— 在 WARP 语境里"带 reserved 的那个后端"就是用户态实现。

---

## 工作模式：非全局（默认）与全局

入口只有一条命令，差别在 `--mode`：

```bash
sudo warpgo up                      # 非全局（默认）
sudo warpgo up --mode global        # 全局：整机流量走 WARP
```

**非全局**下，只有**以 WARP 接口地址为源**的流量走隧道。这就是它在服务器上安全的原因：
策略规则只匹配那个源地址，你 SSH 进来的流量根本不在匹配范围内。

**全局**下所有流量走 WARP。它和全局模式的接口、地址与非全局**完全一样**，
差的只是规则 —— 所以切换模式不会重建接口。

判据在两种模式下正好相反：

```bash
curl -4 -s --interface warp https://1.1.1.1/cdn-cgi/trace | grep warp    # 两种模式都应为 on
curl -4 -s https://1.1.1.1/cdn-cgi/trace | grep warp
#   非全局 → off          本机流量仍走原生
#   全局   → on           本机流量也被接管
```

### 全局模式的两层保险

全局模式配错就意味着失去唯一的远程入口，所以内置了两道闸：

1. **自检** —— 应用之后立刻验证三件事：「经隧道 `warp=on`」「默认路径仍可用」
   「默认路由没有被改写」。三条缺一不可，任何一条不过就**立即回滚**（实测 16 秒内完成）。
2. **保险丝** —— 变更**之前**派生的独立进程（`setsid`，与你的 SSH 会话无关）。
   45 秒内没收到确认就无条件回滚。它是给"连自检都没跑完"的极端情况兜底的 ——
   比如你正是因为这个命令断了网，再也看不到任何输出。

```bash
sudo warpgo up --mode global --guard-timeout 90s   # 调超时
sudo warpgo up --mode global --no-confirm          # 先应用，把确认权留给你
sudo warpgo confirm                                # 表示"保留这次变更"
```

**回滚的目标是"回到没有 warpgo 隧道的干净状态"，不是"恢复到变更前"。**
后者要复原整套规则，而复原过程一旦出错，就把唯一的救命通道也搭进去了。
回到干净状态更稳，之后一条 `up` 就能重来。

---

## 隧道后端：内核 / 用户态

`reserved` 是 Cloudflare 对 WireGuard 的私有扩展（握手报文里的 3 个字节）。
**部分出口（如中国香港、洛杉矶）必须带上它才能连通**，而内核 WireGuard
在结构上带不了这三个字节。为此 warpgo 内嵌了一份标准的 `wireguard-go` 作为用户态后端。

```bash
sudo warpgo up --backend userspace   # 强制用户态（会把 reserved 发出去）
sudo warpgo up --backend kernel      # 强制内核（性能最好，但发不出 reserved）
sudo warpgo up                       # 默认 auto
```

`auto` 的取舍是"能上内核就上内核（性能），真需要 reserved 的地区能自己找到出路（可用性）"。
它只在账户带 reserved 时才多花一次体检的时间；reserved 全零的账户与非全局模式行为一致。

`auto` 是**自适应**的，不是"永远先试内核"：一旦某个后端实测不通、换到另一个之后，
后续的 `up` 与保活会优先沿用**现在跑着的那个** —— 否则一台"只能用用户态"的机器，
每修一次链路都要先拆掉可用的用户态隧道，去试一个已知不通的内核后端。
想主动换回内核：

```bash
sudo warpgo switch --backend kernel
```

**为什么不做 fork**：上游那些"打过补丁的 wireguard-go"，补丁的全部内容就是在收发
两个方向各改写类型字段的 3 个字节。warpgo 在 `conn.Bind` 上包一层即可逐字节等价 ——
于是不必在仓库里养一个一万多行的 fork。

> 这件事只有抓包能看清：服务端会把 reserved **原样盖回它的响应上**（响应变成 `02 RR RR RR`），
> 而 WireGuard 是按 4 字节小端读报文类型的，所以**只改发送方向必然失败**。
> 现象是"包发出去了、服务端也回了、握手永远完不成、而且没有任何报错"。

---

## 地址族：`--stack` 与 `priority`

这是两件不同的事，上游把它们的开关都放在 `4`/`6`/`d` 上，warpgo 刻意用两个名字区分：

| | 决定什么 | 落在哪 | 命令 |
|---|---|---|---|
| **`--stack`** | 隧道里**装了几族地址** | 接口地址 + 策略规则 + 路由 | `warpgo up --stack 4` |
| **`priority`** | 解析器**先问哪一族** | `/etc/gai.conf` | `warpgo priority 4` |

### `--stack`：隧道携带哪几族

```bash
sudo warpgo up --stack 4     # 只装 IPv4：接口上只有 v4 地址、只有 v4 的规则与路由
sudo warpgo up --stack 6     # 只装 IPv6
sudo warpgo up --stack d     # 双栈（默认）
```

- **换栈会先拆后建。** "先清后加"只清规则与它自己那张路由表，不碰接口地址 ——
  从双栈切到只用 v4 时，v6 地址会留在接口上，于是"只用 IPv4"名不副实。
  所以栈变了就自动 `--force` 重建，并在日志里说明原因
  （**静默重建是不能接受的**：那会换一次出口 IP）。
- **选择会被记住。** `watch` 重建、`switch` 互换、`rotate` 换 IP 都照运行态里的栈来，
  不会把"只用 IPv4"悄悄改回双栈。
- 双栈走快路径：不过滤、一个字节都不动。

### `priority`：解析器先问哪一族

同一台机器同时有 IPv4 与 IPv6 出口时，访问双栈域名先走哪一族由 glibc 决定，
`/etc/gai.conf` 可以影响它：

```bash
sudo warpgo priority        # 看当前状态 + 实测的解析顺序
sudo warpgo priority 4      # IPv4 优先
sudo warpgo priority 6      # IPv6 优先
sudo warpgo priority d      # 回到系统默认（= 移除写进去的那两行）
```

真机实测（Debian 12 / glibc 2.36）有个值得知道的事实：

| 状态 | `getent ahosts www.cloudflare.com` 的第一条 |
|---|---|
| 系统默认 | IPv6 |
| `4` | IPv4 —— **真的生效** |
| `6` | IPv6 —— **与默认完全一样** |

也就是说 **`4` 才是那个真正的开关**：`6` 之所以"也是 IPv6 优先"，是因为 glibc 的默认
本来就是 IPv6 优先。`priority 6` 真正起作用的一步是**把 IPv4 优先那行删掉**
（万一机器上有别的工具写过它）。命令会如实告诉你这一点，不假装对称。

实现上只增删 `/etc/gai.conf` 里那两行，其余内容（包括 glibc 自带的那行被注释掉的
v4 写法）逐字节保留 —— 所以 `d` 能把你进去时的文件原样还回来，不需要任何备份文件。

`priority` 无参数时会用 `getent ahosts` **实测**一次解析顺序：只看文件里有没有那行
是不够的（一个什么都没做的实现也能让文件"看起来对"），而 **Go 自己的解析器不读
`gai.conf`**，用它去测会得出"gai.conf 无效"的错误结论。

---

## 临时关闭与恢复

```bash
sudo warpgo off     # 拆隧道 + 停保活，但**保留配置**
sudo warpgo on      # 按上次的配置原样恢复，并把保活拉回来
```

`off` 与 `down` 只差一处，但那处很要紧：`down` 会删掉运行态（它的语义是"清空全部残留"），
而 `off` **留着** —— 于是 `on` 能按原来的模式 / 后端 / MTU / 端点 / DNS 重建，
而不是退回账户的默认配置。

几个刻意的细节：

- `on` 不重新实现一遍"建隧道"，而是把运行态翻译成一组 `up` 的参数再交给它 ——
  于是"恢复"与"建立"走**同一条代码路径**，不会出现"up 能修好、on 修不好"。
- 保活要不要拉回来，用 `systemctl is-enabled` 判断（**无状态**：重启过机器、
  换过 shell 都一样），而不是记一个标志位。
- `off` 只 `stop` 保活、**不动 `enabled`**，所以"临时"名副其实：重启之后它自己会把隧道拉回来。
- `off` 也会还原 `/etc/resolv.conf`（隧道一拆，WARP 的解析器就不可达了）；
  当初改成了什么记在运行态里，`on` 会照着改回去。

---

## 保活与开机自启

```bash
sudo warpgo watch --once         # 立刻检查一次并修好（脚本 / 手工都好用）
sudo warpgo watch                # 常驻巡检，默认每 5 分钟一轮
sudo warpgo watch --interval 2m  # 换频率（最小 10 秒）
```

它先做**只读**体检（绑接口探测，零 DNS），只有确认链路断了才重建：

| 情况 | 行为 |
|---|---|
| 接口在、经接口拿到 `warp=on` | **什么都不做**（一个字节都不改，出口 IP 不变） |
| 接口被删了 | 重建接口 |
| 接口在、配置也对，但就是不通 | **硬复位**一次，强制重新握手（出口 IP 仍不变 —— 它是账户里分配的固定地址） |
| 单实例锁被别人占着（比如你正在手工 `up`） | 本轮跳过，汇总里记一笔 `skipped 1`，**不会假装健康** |
| 重建失败 | 记录错误，**进程继续跑**、下一轮再试 |

装成开机自启的服务：

```bash
sudo warpgo install              # 装二进制 + 写单元 + 开机自启 + 启动
sudo warpgo install --interval 2m

systemctl status warpgo-watch    # 单元名：warpgo-watch.service
journalctl -u warpgo-watch -f    # 巡检时间线，每次都会说"健康 / 重建"
```

`down` 会**先停掉这个服务**再拆隧道 —— 否则保活下一轮就把隧道建回来了，
你会以为 `down` 没生效。（`down --keep-watch` 可以保留它。）

### 生成的单元长什么样

```ini
[Unit]
Description=warpgo WARP keepalive
Documentation=https://github.com/misakacoo/warpgo
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/warpgo watch --interval 5m0s --ifname warp --mode non-global
Restart=always
RestartSec=30s
TimeoutStopSec=60s

[Install]
WantedBy=multi-user.target
```

四个刻意的选择：

1. **路径一律写绝对路径。** 单元里的相对路径会随 systemd 的工作目录变化，而那个目录不是我们能控制的。
2. **刻意不写 `ExecStop`。** ExecStop 非零退出会让 systemd 把整个单元标成 failed，
   而且会让 restart 的语义变得难以推理。"清理现场"是 `down` / `uninstall` 的职责，不是停止服务的职责。
3. **`Restart=always` 敢用的前提是 `watch` 自身幂等**（健康时什么都不做），
   所以 restart 不会造成出口 IP 抖动。
4. **`down` 会先停服务再 `reset-failed`。** 前者是因为不停它就会在下一轮把隧道建回来；
   后者是因为单元一旦被标成 failed，`restart` 会退化成"只重启不重载"。

---

## 换出口 IP

```bash
sudo warpgo rotate                  # 重新注册，换一个新的 WARP 出口
sudo warpgo rotate --attempts 5     # 最多试 5 次（默认 3）
```

它会一直注册到"隧道地址确实变了"为止，然后用新账户重建隧道、体检通过才写盘。
**任何一步失败都会回退到原账户并把隧道建回来** —— 你的隧道可能是唯一的一条对外通道。

成功之后它会顺手把旧设备从服务端注销（否则每换一次就在账户里留一个孤儿设备），
并**如实汇报公网出口 IP 有没有变**：

> 账户与隧道地址一定会换，但出口 IP 由 Cloudflare 的 PoP 决定
> （同一 PoP 上的免费账户共享出口），所以**可能不变** —— 这两种结果在真机上都遇到过。

它不做流媒体解锁探测（那是另一件事，且依赖第三方页面结构，随时会静默失效）。

---

## 在线互换

```bash
sudo warpgo switch --backend userspace   # 内核 ↔ 用户态
sudo warpgo switch --mode global         # 全局 ↔ 非全局
```

出口地址不会因为互换而改变（它是账户里分配的固定地址）。
若保活服务已经装好，`switch` 会**同步改写它的启动参数并重启** ——
否则它下一轮就按旧参数把隧道重建回去，你会以为 `switch` 没生效。

---

## WireProxy：把 WARP 做成一个本地 socks5

```bash
sudo warpgo proxy install                # 下载 + 校验 + 生成配置 + 装成服务
sudo warpgo proxy install --port 25344   # 换端口（默认 40000）
sudo warpgo proxy on | off               # 开关
sudo warpgo proxy status [--json]        # 看状态（会经 socks5 实测一次）

curl -x socks5h://127.0.0.1:40000 https://1.1.1.1/cdn-cgi/trace | grep warp   # warp=on
```

它和隧道模式是**两条不同的路**：不起网络接口、不碰路由，只在用户态跑一个小的
WireGuard 客户端，把 socks5 挂在 **127.0.0.1**（不对外暴露）。
"哪些流量走 WARP"完全由你自己决定 —— 浏览器、`curl -x`、程序里的代理设置。

几个刻意的做法：

- **版本默认钉死**（`v1.1.3`），不追 latest —— 装上去的是以 root 运行的可执行文件，可复现优先。
- **校验和强制。** 从发布页取 `checksums.txt` 比对；取不到就报错，并给出两条出路
  （`--sha256` 手工指定，或 `--from FILE` 离线安装），**绝不静默降级成不校验**。
- 装完先 `wireproxy -n -c` 自检配置，再启动服务，最后**经 socks5 实测** `warp=on` 才算成功。
- **reserved 在这里发不出去**（wireproxy 用的是官方 wireguard-go）。账户带 reserved 时
  会明确警告 —— 需要 reserved 的地区请改用隧道模式：`warpgo switch --backend userspace`。
- `warpgo uninstall` 会一并撤掉它；`proxy uninstall --keep-binary` 可以留下那个二进制。

---

## 官方 Linux Client

用 Cloudflare **官方**的客户端（`cloudflare-warp` 包）做出口。默认装成
**socks5 代理模式** —— 它一个字节的路由都不动：

```bash
sudo warpgo client install                  # 默认 proxy 模式（安全）
sudo warpgo client install --port 25344     # 换 socks5 端口（默认 40000）
sudo warpgo client on | off
sudo warpgo client status [--json]

curl -x socks5h://127.0.0.1:40000 https://1.1.1.1/cdn-cgi/trace | grep warp   # warp=on

sudo warpgo client mode warp                # 切到 WARP 模式（会接管默认路由！）
sudo warpgo client uninstall                # 卸载（含 apt 源、钩子与残留规则）
```

> **警告：`--mode warp` 会接管默认路由**（整机流量走 WARP）。在一台只能靠 SSH
> 进入的服务器上，这有可能把你关在门外 —— 请确认能通过控制台 / VNC 恢复再用。
> 只想给本机多一个出口，用默认的 proxy 模式就够了。

几个刻意的做法：

- **`warp-cli -j` 才是稳的解析入口。** `settings` 的文本输出长这样：
  `(user set)	Mode: WarpProxy on port 40000` —— 前面多一列"取值来源"、值后面还跟着端口，
  按正则刮文本迟早被下一个版本打垮。所以**先试 `-j`（结构化 JSON）、不认才退回文本**。
- **apt 源与密钥环只删自己建的。** 安装时把"新建了哪几个文件"记进状态，
  卸载时只删那几份 —— 机器上本来就有官方源的话，那是你的配置，不是我们的。
- **钩子用 systemd drop-in 挂载**，而不是去 `sed` 包自带的单元文件：
  后者由 dpkg 管，下次 `apt upgrade` 会静默覆盖它（症状是"重启之后规则又没了"）。
  钩子用的表号与规则优先级**与 warpgo 自己的隧道完全错开**，两边的清理互不干扰。
- **`warp-svc` 自己写的策略规则也要收尾。** 官方客户端做整机接管时会往系统里插
  `not fwmark 0x100cf lookup 65743`（pref 8999）。正常注销时它自己会撤，但卸载是
  "停服务 → 卸包"，服务被硬杀，规则就留下了 —— 表随包消失、lookup 落空、实际无害，
  可"零残留"的承诺里不该有这么一条。卸载时按 pref + 取反 + fwmark + 表号
  **逐项核对后**才删，撞上同号的无关配置就拒绝动手。
- `warpgo uninstall` 会联动撤销它，但**没有状态文件就不动** ——
  机器上本来就装着官方客户端的话，我们无权把它删掉。

### 隐藏子命令 `hook`

`warpgo hook add [--wait 30s]` / `warpgo hook del` 供 `warp-svc` 的
`ExecStartPost` / `ExecStop` 调用。它们不出现在 `help` 里，也不该由用户手工执行 ——
并且**永远以 0 退出**（非零会让 systemd 把整个单元标成 failed，
之后 restart 的语义会变得难以推理）。

---

## 交互菜单

```bash
sudo warpgo menu
```

菜单是**薄封装**：每个选项最终都翻译成一条已经存在的子命令再执行。
好处是菜单与命令行走**同一条代码路径**（不会出现"命令行能修好、菜单修不好"），
而且每一项都能被脚本复现 —— 把菜单里的命令抄下来就是那条命令。

菜单顶部会先给一行状态（隧道 / 模式 / 后端 / 地址 / 账户类型）：

```
warpgo 交互菜单
  隧道 warp · 模式 non-global · 后端 kernel · 地址 172.16.0.2/32 2606:4700:110:…/128 · 账户 free
```

这一行**只读本机的运行态与账户文件** —— 不联网、不做探测、不改任何东西。
打开菜单是个纯观察动作，断网时也照样能看。

---

## 查询新版本与自我更新

```bash
sudo warpgo version                                  # 本地版本信息（只读）
sudo warpgo version --check                           # 查 GitHub 最新发布并比较
sudo warpgo version --self-update                     # 下载 + 校验和 + 替换自己
sudo warpgo version --self-update --force             # 重装同一个版本
sudo warpgo version --self-update --version v0.2.0    # 装指定版本
sudo warpgo version --self-update --no-restart        # 不重启保活服务
```

自我更新与 `proxy install` 走的是**同一套**下载 / 校验 / 替换原语：

- **先把校验和定下来，再下载。** 顺序反了就有"清单取不到时先装上去再说"的诱惑，
  而那等于静默降级成不校验。
- **在临时文件上先跑一次 `version`，确认它能执行，最后才 rename。**
  所以任何一步失败时，**磁盘上那个正在用的 warpgo 一个字节都没动** ——
  把工具自己更新坏了，用户就没有工具可用了，这是最要紧的一条。
- 更新完若保活服务在跑就**重启它**（进程起来时已经把旧的那份可执行文件映射好了，
  不重启等于"更新了但没生效"）；`--no-restart` 可以跳过。
- 资产名（`warpgo_<版本>_linux_<架构>.tar.gz`）与发布流水线生成的**是同一套规则**，
  所以"发布出来的"就是"能自动装上的"。

`--check` 的退出码始终是 `0`（查询成功）/ `1`（查询失败）—— "落后了"不算错误，它写在输出里。

可以用 `--repo` / `--api-base` / `--base` 覆盖发布仓库与 API / 下载前缀（镜像或自建发布页）。

---

## DNS 怎么处理

**默认不碰 `/etc/resolv.conf`。** 只有显式要求才改：

```bash
sudo warpgo up --dns 1.1.1.1,1.0.0.1
```

改之前的原文会被备份到 `/etc/warpgo/resolv.conf.origin`，`down` / `uninstall` 会还原它，
**并且只在还原成功之后才删备份**。

这一条是有来历的：旧版卸载后，`/etc/resolv.conf` 里还是安装时写进去的
`nameserver 1.1.1.1`，而备份 `/etc/resolv.conf.origin` 已经被删 ——
用户连"原来是什么"都无从得知。

另外，如果 `/etc/resolv.conf` 是符号链接（用 systemd-resolved 的机器就是），
warpgo **拒绝改写** —— 把它换成普通文件会让 resolved 的配置再也无法生效，
而那是用户没做过的事。

---

## 文件与路径

| 路径 | 内容 |
|---|---|
| `/usr/bin/warpgo` | 二进制（由 `install` / `self-update` 维护） |
| `/etc/warpgo/account.json` | 账户：设备 ID、隧道地址、`reserved`、私钥。权限 `0600` |
| `/etc/warpgo/runtime.json` | 运行态：接口名、模式、后端、栈、MTU、端点、DNS 改动记录 |
| `/etc/warpgo/resolv.conf.origin` | `/etc/resolv.conf` 的原文备份（仅在改过 DNS 时存在） |
| `/run/warpgo/net.lock` | 单实例锁（`目录` + PID + 心跳时间戳） |
| `/etc/systemd/system/warpgo-watch.service` | 保活单元 |

这些位置都可以用环境变量覆盖，便于在测试或非标准环境里跑：

```
WARPGO_ETC          状态目录（默认 /etc/warpgo）
WARPGO_RUN_DIR      锁与临时文件目录（默认 /run/warpgo）
WARPGO_BIN_PATH     二进制路径（默认 /usr/bin/warpgo）
WARPGO_UNIT_PATH    保活单元路径
WARPGO_PROXY_BIN / WARPGO_PROXY_UNIT        WireProxy 的二进制与单元
WARPGO_CLIENT_*     官方客户端的 CLI 路径 / apt 源 / 密钥环 / drop-in / 状态
```

### 单实例锁

同一时刻只允许一个 warpgo 操作 WARP 链路。锁文件里记的是 **PID + 心跳时间戳**，
而不是墙钟：

- 拿不到锁时**明确报错退出**（退出码 `4`），绝不静默返回成功 ——
  上游那个 `return 0` 曾经让人把"什么都没做"读成"一切都好"。
- 心跳停跳超过阈值才认为持锁者已失控。**接管前会按 `/proc/<pid>/cmdline` 的
  `argv[0]` 确认身份**，认不出来就只接管锁、**不杀进程**（防 PID 复用误杀）。
- 旧格式的锁（只有 PID、没有心跳）一律按失效处理。

---

## 卸载

```bash
sudo warpgo uninstall                 # 单元、隧道、二进制、DNS 改动全部还原
sudo warpgo uninstall --purge         # 连账户一起删（下次注册会拿到新的 WARP IP）
sudo warpgo uninstall --keep-binary
```

**默认保留账户**（`/etc/warpgo/account.json`）—— 它是一个身份，
删掉就意味着下次注册会换一个 WARP 出口地址。日志会明确告诉你它在哪。

`--purge` 只删自己认识的文件：状态目录里一旦出现陌生文件就**整个目录跳过**，
一个都不删并把清单打出来 —— 因为 `WARPGO_ETC` 可以指向任何地方，
无脑清空是不可逆的。

---

## 构建、测试与发布

### 构建

```bash
./build.sh                      # 只为本机构建 → dist/warpgo
VERSION=0.1.0 ./build.sh        # 指定版本号
TAG=v0.2.0 ./build.sh release   # 交叉编译 amd64 + arm64 → tar.gz + checksums.txt
```

### 单元测试

```bash
go test ./...                   # 不需要 root，也不需要网络
```

### 真机验收

`tests/e2e/` 下是一整套在**真实 Debian 机器**上跑的验收脚本。它们会真的改路由、
真的装 systemd 单元、真的下载第三方二进制、真的注册设备 —— 所以每个脚本都自带
`trap` 兜底，跑完会把机器恢复原状。

```bash
sudo BIN=/usr/bin/warpgo tests/e2e/m0-lock.sh        # 单实例锁：占用 / 心跳 / 接管 / 拒绝误杀
sudo BIN=/usr/bin/warpgo tests/e2e/m1-register.sh    # 注册、reserved 解算、profile 生成
sudo BIN=/usr/bin/warpgo tests/e2e/m2-updown.sh      # 幂等、零残留、临时开关、栈切换、SSH 安全
sudo BIN=/usr/bin/warpgo tests/e2e/m3-global.sh      # 全局模式、自检回滚、保险丝（会刻意断网几秒）
sudo BIN=/usr/bin/warpgo tests/e2e/m4-watch.sh       # 保活巡检、systemd、卸载与 DNS 还原
sudo BIN=/usr/bin/warpgo tests/e2e/m5-userspace.sh   # 用户态后端、抓包验 reserved、换 IP
sudo BIN=/usr/bin/warpgo tests/e2e/m6-priority.sh    # gai.conf 三态 + getent 实测顺序
sudo BIN=/usr/bin/warpgo tests/e2e/m7-proxy.sh       # 真实下载、socks5 实测、零残留
sudo BIN=/usr/bin/warpgo tests/e2e/m8-client.sh      # 真实装包、钩子、菜单
sudo BIN=/usr/bin/warpgo tests/e2e/m9-update.sh      # 版本查询、打包产物、真实自更新
```

几个脚本值得单独说明：

- **`m5-userspace.sh`** 会真的在两套后端之间来回切换、真的重新注册一个新账户
  （所以这台机器的 WARP 出口地址会变、旧设备会被从服务端注销），
  并且用 `tcpdump` 抓包来断言"报文的类型字段里到底带了什么字节" ——
  这是唯一能证明 reserved 真的生效 / 没生效的办法。
- **`m7-proxy.sh`** 会真的从 GitHub 下载第三方二进制、真的装 systemd 单元、真的起进程，
  并且每条连通性断言都是**另起一条 `curl --proxy socks5h://…`** —— 不看被测程序自己的结论。
- **`m6-priority.sh`** 会真的改 `/etc/gai.conf`，结束时按 sha256 逐字节还原；
  它用 `getent ahosts` 验效果，因为那才是用户实际会遇到的解析路径。
- **`m8-client.sh`** 会真的装 `cloudflare-warp`（加 apt 源 + 导公钥 + apt 装包）、
  真的注册设备、真的经 socks5 发请求。它有一条**安全边界**：真实客户端只跑
  **proxy 模式**（不动路由），"warp 模式接管默认路由"那一步要显式设
  `M8_TEST_WARP=1` 才会跑。

> 验收脚本会动真实的网络与 systemd，建议用 `setsid` 在后台跑再挂一个"死人开关"。

### 发布

打一个 `vX.Y.Z` 的 tag 并推送，`.github/workflows/release.yml` 会自动执行
`gofmt -l` / `go vet` / `go test`，然后交叉编译两个架构、生成 `checksums.txt`、
挂到 GitHub Release 上。流水线失败时**不会留下 Release**，所以 tag 可以安全地删掉重打。

```bash
git tag -a v0.2.0 -m "warpgo v0.2.0"
git push origin refs/tags/v0.2.0
```

产物验证：

```bash
sha256sum -c checksums.txt
```

归档内是 `warpgo/{warpgo,README.md,LICENSE}`。二进制里注入了版本号与提交哈希，
`strings` 就能看到 —— 那是"这个 tag 真的指向这份代码"的证据。

---

## 退出码

| 码 | 含义 |
|---|---|
| `0` | 成功 |
| `1` | 通用错误 |
| `2` | 体检不健康 |
| `3` | 需要 `reserved`，但当前后端不支持 |
| `4` | 已有另一个实例在运行（被单实例锁挡住） |
| `5` | 非 Debian 系统 |

`watch` 是唯一不遵守"失败即非零"的命令：单轮重建失败时它记录错误并继续跑
（退出会让 systemd 每 30 秒重启一次，日志里堆满没有信息量的重启记录，
而"端点不可达"并不会因为重启而自愈）。

---

## 设计取舍

以下每一条都对应一个真实发生过的故障或上游的坑。

- **零外部依赖。** 单个静态二进制，不要求系统装 `bash` / `jq` / `python` / `wg` / `wg-quick`。
  上游 shell 版的教训是：依赖越多，越容易被上游改动打穿。
- **不生成 PostUp / PostDown 钩子脚本。** 上游为这些生成物吃过不少苦
  （升级覆盖补丁、`[detached]` 规则清不掉、hook 非 0 中断 wg-quick）—— Go 进程自己持有状态。
- **所有"添加"操作先清后加。** 上游 `ip rule add` 不幂等，残留累积到过 4681 条。
- **删除一律按句柄 / pref，不解析文本。** 接口删掉后 `ip rule show` 会变成
  `oif warp [detached]`，按字符串匹配永远删不掉。
- **策略路由的规则自己实现**，不依赖第三方库的 `RuleAdd`。实测在较新的内核上
  那个调用会返回 `EINVAL`，而同样的消息手工构造就成功 —— 规则是幂等与零残留的唯一依据，
  不把它押在别人的内核适配上。（接口 / 地址 / 路由仍用成熟库。）
- **体检只打 IP 字面量**（零 DNS）。上游的兜底链全靠 DNS，DNS 一抖主路径与兜底一起失败。
- **体检必须只读。** 上游的"判断源"自己会重启接口、改路由 —— 于是"探测"变成了"故障放大器"。
  这里的体检只发 HTTP，不改任何状态。
- **"重建"要连"接口在但不通"一起管。** 只在接口消失时才重建是不够的：
  现实中最常见的故障是接口好好的、配置也对，就是不通。
- **健康就不动。** 保活巡检发现链路正常时什么都不做；只有确认断链才重建、才换 IP。
- **拿不到单实例锁时明确报错**，绝不静默返回成功。锁被占是正常情形，但不能沉默 ——
  跳过就跳过，可汇总里必须留下 `skipped 1`。
- **`reserved` 按 base64 原始字节解**，不做字符码点运算。上游用 `printf "%d" "'c"`
  在 UTF-8 locale 下把 161 算成 737，超过 255 后被补丁版 `wireguard-go` 静默回落成
  `[0,0,0]`，在需要 reserved 的地区握手被丢弃 —— 且完全没有任何报错。
- **`endpoint` 一律取 `host`，并修正端口为 0 的情况。** API 返回的 `endpoint.v4/.v6`
  端口是 `0`，照抄会得到一个"看着有值、永远连不上"的端点。
- **解析对服务端字段形态不敏感。** `created` / `updated` 有时是字符串、有时是 `{}`；
  用 `time.Time` 会让一个无关字段把整次注册搞崩，所以用宽容类型。
- **`account --json` 输出脱敏视图**（不含私钥与令牌），让"打印"这件事默认是安全的。
- **危险操作先上保险，再动手。** 全局模式的保险丝必须在变更**之前**就位 ——
  顺序反了（先改网络再派生）等于没有保险。
- **回滚目标是"网络一定能用"，不是"恢复原样"。** 见上文全局模式一节。
- **改过的东西要能还回去。** DNS 只是第一条：备份先写、还原成功才删备份；
  状态目录里出现不认识的文件就拒绝整目录清理。

---

## 许可

**GPL-3.0**。注册 API 的请求形状与常量取自 [fscarmen/warp](https://gitlab.com/fscarmen/warp)
的 `api.sh`，因此本作品是其派生作品，沿用 GPL-3.0 并保留原作者署名。
Go 侧依赖的 `golang.zx2c4.com/wireguard` 为 MIT，兼容。
