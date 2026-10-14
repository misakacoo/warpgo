# warpgo 设计文档（v2 · 完整菜单重写）

> **本文档取代 v1**（v1 只覆盖"前三项功能"）。现已按用户决定扩为**重写整个菜单**。
>
> 目标：用 Go 重写 fscarmen/warp 的**全部菜单功能**，**仅支持 Debian**，
> 不适配 Docker、macOS、路由器（OpenWrt）等平台。
>
> - 仓库：`C://Users//momo//Documents//GitHub//warpgo`（本文件即仓库内 `docs/DESIGN.md`，是唯一正式版本）
> - 状态：**M0 进行中**（骨架 + 单实例锁 + i18n + 日志）
> - 编译 / 测试 / 调试**一律在 Debian 上进行**（见 §12）
> - §14 有两处我方保留意见，待确认（DD 重装系统、Teams OAuth）

---

## 1. 用户已拍板的决定

| # | 决定 | 说明 |
|---|---|---|
| 1 | **功能边界 = 全搬** | "不要其他客户端"的真实含义是**只要 Debian、不适配 Docker / macOS / 路由器**；<br>菜单里所有功能都要，**包括**官方 Linux Client（`c`/`l`/`r`）与 WireProxy（`w`/`y`） |
| 2 | **默认模式 = 非全局** | 只对指定源地址加策略路由，天然不会切断 SSH |
| 3 | **reserved = 一等公民** | 需要 vendor 一份打过补丁的 `wireguard-go`，需要时自动切用户态后端 |
| 4 | **全新开始，不迁移旧安装** | 用户已卸载旧版；不需要"导入现有 warp.conf"模块；测试可随意 |

> 用户机器（Debian 12，非全局，原生 v4+v6）**已确认卸载干净** —— 见附录 A 的真机审计。
> 该机器可作开发与真机验收的测试机，可随意安装/卸载。

---

## 2. 功能矩阵（逐项对照现有菜单）

| 菜单项 | 现有实现 | Go 模块 | 里程碑 | 备注 |
|---|---|---|---|---|
| `4` `6` `d` 装接口/切栈 | `install()` + `stack_switch()` | `netcfg` + `tunnel` | **M2** | 已装则变为"切栈"（改 AllowedIPs 注释） |
| `o` 临时开关 | `onoff()` | `cli` + `netcfg` | M2 | 关 = 拆接口但保留配置；开 = `up --force` |
| `n` 获取 IP / 重连 | `net()`（199 行） | `health` + `keepalive` | M2/M4 | **新语义**：默认先体检，健康不动；`--force` 才重建 |
| `i` 换 IP（Netflix） | `change_ip()`（214 行） | `cloudflare` + `netcfg` | M5 | 重新注册 + 切 reserved |
| `g` 全局/非全局切换 | `working_mode_switch()` | `netcfg.route` | M3 | 要连路由一起换，不是只改注释 |
| `k` 内核↔用户态 | `kernel_reserved_switch()` | `tunnel` + `userspace` | M5 | 同一套实现对两种后端 |
| `s 4/6/d` 栈优先级 | `stack_priority()` + `result_priority()` | `netcfg.priority` | M6 | Debian 上用 `gai.conf` + `ip rule` 优先级 |
| `u` 卸载 | `uninstall()`（93 行） | `state.uninstall` | **M4** | 见 §8.13 的"必须还原清单" |
| `v` 版本/同步 | `ver()` + `check_cdn()` | `cli.version` | M9 | Go 版无"同步脚本"，改为查新版本 + 可选自更新 |
| `b` 升级内核 + BBR | `bbrInstall()` | `system.bbr` | M9 | 只做 BBR；内核升级只做"检查 + 提示" |
| `b` 里的 DD 重装系统 | `bbrInstall()` 内 | `system.dd` | M9 | **默认不编入**（见 §14.1） |
| `e` 分流（iptables+dnsmasq+ipset） | `iptables_solution()`（129 行）+ 配置 | `split` | M6 | 流媒体解锁那套 |
| `w` WireProxy 方案 | `wireproxy_solution()` | `proxy` | M7 | 引入第三方 `wireproxy` 二进制 |
| `y` WireProxy 开关 | `wireproxy_onoff()` | `proxy` | M7 | |
| `c` 官方 Client → socks5 | `client_install()`（137 行） | `cfclient` | M8 | 依赖 Cloudflare 官方 `.deb` |
| `l` 官方 Client → WARP 模式 | `client_install()` | `cfclient` | M8 | |
| `r` Client 开关 | `client_onoff()` | `cfclient` | M8 | |
| `z` / `x` 隐藏命令 | `rule_add` / `rule_del` | `cfclient.hooks` | M8 | 供 `warp-svc` 的 ExecStartPost/ExecStop 调用 |
| `h` 帮助 | `help()` + E[6]/C[6] | `cli.help` | M2 | 随功能逐步补全 |
| 菜单内状态显示 | `check_system_info()` + `menu()` | `health` + `cli.menu` | M2 | IPv4/IPv6 / 模式 / 内核 / 账户类型 |
| 语言选择 | `select_language()` + 141 条文案 | `i18n` | M1 | 中英双语，跟 `LANG` 默认 |
| 账户 / license / 设备 | `api.sh` 的 account 子命令 | `cloudflare.account` | M5 | WARP+ 绑定、设备列表、注销 |
| Team 令牌注册 | `api.sh` 的 Teams OAuth 流程 | `cloudflare.teams` | M5 | 若不需要可砍，见 §14.2 |
| 运行次数统计 | `statistics_of_run-times()` | — | — | **建议默认关闭**远程上报（§8.14） |

---

## 3. 明确不做

- Docker 方案（`docker.sh`，676 行）
- macOS 客户端（`pc/mac.sh`，416 行）
- 第三方内核 / OpenWrt / sing-box 导出（`warp-go.sh`，1471 行）
- 除 Debian 外的发行版适配（不做 Alpine / busybox / openresolv 分支）
- 纯 shell 实现（用编译产物换确定性与可观测性）

---

## 4. 硬约束

1. **只支持 Debian**：启动即检查 `/etc/debian_version`，非 Debian 直接拒绝并说明原因。
2. **不得切断 SSH**：默认非全局；全局模式必须 fwmark 防环路 + watchdog 自动回滚。
3. **不依赖运行时**：单个静态二进制；不要求 `bash`/`jq`/`python`/`wg`/`wg-quick`。
   （`w`/`y` 需要 `wireproxy`、`c`/`l` 需要官方 `warp-cli` —— 这两个是**功能本身**的依赖，
   由工具自己下载管理并校验。）
4. **幂等**：任何命令重复执行结果一致；所有"添加"操作都必须"先清后加"。
5. **卸载必须可还原**：见 §8.13。
6. **GPL-3.0**：注册 API 的形状与常量取自 fscarmen/warp 的 `api.sh`（派生作品），
   沿用 GPL-3.0 并保留原作者署名。Go 侧 `golang.zx2c4.com/wireguard` 为 MIT，兼容。

---

## 5. 架构分层

```
cmd/warpgo             CLI / 菜单 / i18n
   │
   ├── cloudflare ─────── 注册、账户、license、设备、Teams
   │
   ├── tunnel ─────────── 抽象：BringUp / Status / Close / SetReserved
   │      ├── kernel      （netlink 建接口 + UAPI 下发）
   │      └── userspace   （内嵌 vendored wireguard-go device）
   │
   ├── netcfg ─────────── 与传输无关：地址 / MTU / DNS / 路由 / 策略路由 / 回滚
   │      ├── split       （iptables + dnsmasq + ipset 分流）
   │      └── proxy       （WireProxy 生成与开关）
   │
   ├── cfclient ───────── 官方 Linux Client 的安装 / 模式切换 / 开关 / z·x 钩子
   │
   ├── health ─────────── 体检（trace，IP 字面量优先）
   ├── keepalive ──────── 巡检与自愈（健康不动）
   └── state ──────────── 账户与运行态、单实例锁、卸载
```

**分层的意义**：`netcfg` 完全不知道隧道是内核还是用户态建的；将来 CF 若把 WireGuard
换成 MASQUE，只需新增一个 `tunnel` 实现，其余层不动。

---

## 6. 目录结构

```
warpgo/
├── cmd/warpgo/main.go
├── internal/
│   ├── cloudflare/  api.go types.go reserved.go account.go teams.go
│   ├── tunnel/      tunnel.go kernel.go userspace.go uapi.go
│   ├── netcfg/      link.go addr.go mtu.go dns.go route.go priority.go guard.go
│   ├── split/       iptables.go dnsmasq.go ipset.go
│   ├── proxy/       wireproxy.go
│   ├── cfclient/    install.go mode.go hooks.go
│   ├── system/      bbr.go kernel.go dd.go
│   ├── health/      probe.go
│   ├── keepalive/   loop.go
│   ├── state/       state.go lock.go uninstall.go
│   └── i18n/        en.go zh.go
├── third_party/wireguard-go/   # vendored，唯一补丁：reserved
├── systemd/         warpgo-watch.service
├── tests/           unit/ fake/ e2e/
└── packaging/       build.sh  (可选 .deb)
```

---

## 7. 核心数据结构

```go
type Account struct {
    ID           string       `json:"id"`
    Token        string       `json:"token"`
    PrivateKey   [32]byte     `json:"private_key"`   // 本地生成，永不出本机
    PublicKey    [32]byte     `json:"public_key"`
    DeviceID     string       `json:"device_id"`
    FCMToken     string       `json:"fcm_token"`
    ClientID     string       `json:"client_id"`
    Reserved     [3]uint8     `json:"reserved"`      // base64decode(client_id)
    AddrV4       netip.Prefix `json:"addr_v4"`
    AddrV6       netip.Prefix `json:"addr_v6"`
    Peer         Peer         `json:"peer"`
    AccountType  string       `json:"account_type"`  // free | plus | team
    RegisteredAt time.Time    `json:"registered_at"`
}

type Runtime struct {
    IfName   string      `json:"if_name"`   // 默认 "warp"
    Mode     Mode        `json:"mode"`      // non-global | global
    Backend  Backend     `json:"backend"`   // kernel | userspace
    MTU      int         `json:"mtu"`
    Table    int         `json:"table"`     // 51820
    FWMark   uint32      `json:"fwmark"`    // 0xca6c（仅全局）
    Family   Family      `json:"family"`    // v4 | v6 | dual
    UpSince  time.Time   `json:"up_since"`
    Split    *SplitState    `json:"split,omitempty"`
    Proxy    *ProxyState    `json:"proxy,omitempty"`
    CFClient *CFClientState `json:"cfclient,omitempty"`
}
```

存放：`/etc/warpgo/account.json`（0600）、`/etc/warpgo/runtime.json`、
`/etc/warpgo/warpgo.toml`（用户可编辑的默认值）。

---

## 8. 关键设计

### 8.1 注册与 API

契约见附录 B。要点：

- **版本号与 UA 只出现在 `internal/cloudflare/api.go` 顶部两个常量里**，上游变更时一处修改重编译。
- 流程：`POST /reg` → `PATCH /reg/{id} {"warp_enabled":true}` → 保存账户。
- 地址取 `config.interface.addresses.{v4,v6}`；对端取 `config.peers[0]`；`client_id` 取 `config.client_id`。
- `reserved` 用 base64 **原始字节**解码，不做任何字符码点运算 ——
  上游用 `printf "%d" "'c"` 在 UTF-8 locale 下把 161 算成 737，>255 后被打补丁的
  wireguard-go **静默回落 `[0,0,0]`**，在需要 reserved 的地区握手被丢。
- 单元测试必须含 `y8eh → [203,199,161]`。

### 8.2 reserved 与双后端

`reserved` **不在官方 UAPI 键集内**（官方只有 `private_key`/`listen_port`/`fwmark`/`peer`/
`endpoint`/`preshared_key`/`persistent_keepalive_interval`/`allowed_ip`），
**内核 WireGuard 无法设置握手报文里那 3 个字节**。已知事实：

- Xray-core 自 v1.8.0 起新增 `reserved` 参数，注释明确写「香港/洛杉矶部分 IP 必须带 reserved 才能连通」。
- `bepass-org/warp-plus` 已经 fork 了 wireguard-go 的 device，让 UAPI 支持 reserved。
- 生态约定：把 reserved 写成注释行 `# Reserved = 171, 85, 205` 交给客户端读
  （用户机器上跑的正是读这一行的 patched `wireguard-go`）。

设计：

- `tunnel/kernel.go`：netlink 建 `wireguard` 类型接口 + UAPI 下发；`SetReserved` 返回 `ErrUnsupported`。
- `tunnel/userspace.go`：内嵌 `third_party/wireguard-go`（唯一补丁 = reserved），
  并给它加 UAPI 扩展键 `reserved=`，让上层只跟 UAPI 打交道。
- 选择规则：默认内核；`reserved` 非零且端点属于需要 reserved 的区域、或用户 `--backend userspace`
  → 切用户态，并在日志里说明原因。
- `warpgo profile` 输出始终带 `# Reserved = a, b, c` 注释（与生态一致）。

### 8.3 建接口

netlink 一次完成：建 `wireguard` 类型 link → 加地址（`172.16.0.2/32`、`2606:4700:110:xxxx::/128`）
→ 设 MTU → 起 link → UAPI 下发（私钥、对端公钥、端点、AllowedIPs、`PersistentKeepalive=25`）。

**MTU 探测**照搬现在 `best_mtu()` 的策略（1420 起向下试到能过 1500 字节包），
但用 Go 直接发探测包，不 fork `ping`/`curl`。

### 8.4 出口接管

**非全局（默认）**：

```
ip rule add from 172.16.0.2 lookup 51820
ip rule add from <内网网段>… lookup main      # 内网直连保留
ip route add default dev warp table 51820
```

**全局**：

```
link.fwmark = 0xca6c
ip rule add not fwmark 0xca6c lookup 51820
ip route add default dev warp table 51820
ip rule add table main suppress_prefixlength 0
```

**两条硬性设计约束**（都是在现有项目里踩过的坑，直接在实现层消除）：

1. **所有规则操作"先清后加"**。上游 `ip rule add` 不幂等（`|| true` 把 EEXIST 吞掉），
   残留累积到过 **4681 条**。Go 里每次 `up` 先按句柄清空同名规则再添加，天然收敛到 1 条。
2. **删除一律按句柄/pref，不解析文本**。上游按字符串匹配删除，而接口已删后
   `ip rule show` 会变成 `from all oif warp [detached] lookup 51820`，
   `grep -F "oif warp lookup 51820"` 永远匹配不上，规则一份都清不掉（实测残留 3 份）。

### 8.5 防 SSH 失联与自动回滚

只在**全局模式**下需要，风险最高，因此做两道保险：

1. 变更前把当前 `ip rule` / `ip route` / DNS 全量快照到 `/run/warpgo/rollback.json`。
2. `setsid` 派生 `warpgo rollback-guard --timeout 45s`：45 秒内没收到 `warpgo confirm` 就无条件回滚。
3. 自检（缺一不可）才允许 `confirm`：
   经接口拿到 `warp=on`；原生默认路由仍在；至少一个外部地址在原生路径上仍可建连。

### 8.6 体检与保活

```
健康 ⟺ 接口存在 ∧ （至少一个「经 warp 转发」的地址族能经接口拿到 warp=on/plus）
```

- **只读**：探针只发 HTTP，不改接口/路由、不重启服务。
- **只打 IP 字面量**：v4 `https://1.1.1.1/cdn-cgi/trace`，v6 `https://[2606:4700:4700::1111]/cdn-cgi/trace`。
  **不用域名** —— 上游兜底链全靠 DNS，DNS 一抖"主路径 + 兜底"一起失败，IPv6 那行直接空白。
- **字面量必须与协议匹配**：`1.1.1.1` 是 IPv4 字面量，配 `-6` 实测 `rc=7`（curl 只能按 `::ffff:` 映射）。
- **不要求「所有族都好」**：WARP 的 v6 通路本身抖动更大，要求全好会让 v6 偶发抖动的机器
  每 5 分钟被重建、IP 一直变。
- **巡检语义：健康就不动，断链才重建**。强制重建用 `--force`；只换 IP 用 `rotate`。

### 8.7 单实例锁

`/run/warpgo/net.lock`，内容 `PID 心跳时间戳`：

1. 拿不到锁**必须明确报错**，不能静默 return 0 —— 上游静默返回 0 让人把"什么都没做"
   当成"已成功"，本轮真机验证因此得出过两次错误结论。
2. 心跳超过 180 秒未刷新 → 判定失控，终止并接管 —— 否则一个卡住的实例会让保活永远修不回来，
   直接违反"断联必须能自愈"。
3. 每轮重建前刷新心跳；旧格式（无心跳）遗留锁一律按失效处理。

### 8.8 分流（`e`）

iptables + dnsmasq + ipset：把"需要走 WARP 的域名/IP"打进 ipset，
再由规则把命中流量导向 warp 接口。Debian 12 默认 nftables 后端，
实现里要同时处理 `iptables-nft` 与 legacy，**优先 nft**（Debian 12 原生）。

**注意**：现有实现已发现 P0 级 bug —— 生成的 `NonGlobalUp/Down` 里
`PostUp` 加的是 `ip -4 rule add from 172.16.0.2 …`，而 `PostDown` 删的是
`ip -4 rule delete oif warp …`（从未添加过），导致 `wg-quick down` 返回非 0、
systemd 中止 `restart`。新实现里所有规则必须**成对生成、由同一份数据结构派生**，
不允许两处各写一半。

### 8.9 WireProxy（`w` / `y`）

- 从上游 release 下载 `wireproxy`（校验和验证），生成 `proxy.conf`（WireGuard 段 + socks5 段）。
- 用 systemd 单元托管；`y` 就是它的开关。
- 这引入一个**第三方二进制依赖**，需要固定版本 + 校验和。

### 8.10 官方 Linux Client（`c` / `l` / `r` / `z` / `x`）

- 添加 Cloudflare 官方 apt 源并安装 `cloudflare-warp`（amd64 / arm64）。
- `c` = 装完设成 socks5 代理模式；`l` = 设成 WARP 模式；`r` = `warp-cli connect/disconnect`。
- `z` / `x` = 隐藏子命令，供 `warp-svc` 单元的 `ExecStartPost` / `ExecStop` 调用以加减 `ip rule`。
- 这一块的实现本质是"编排官方客户端"—— 我们能重写的只有调用与状态管理，**不重写 warp-svc 本身**。

### 8.11 BBR 与内核（`b`）

- BBR：写 `/etc/sysctl.d/99-warpgo-bbr.conf` + `sysctl --system`；卸载时可选择还原。
- 内核升级：只做**检查 + 提示 + 建议命令**，不自动 `apt dist-upgrade`（在别人机器上自动升内核风险太大）。

### 8.12 DD 重装系统（`b` 内）—— 默认不编入

见 §14.1。

### 8.13 状态、幂等与卸载

**卸载必须还原的清单**（来自对"旧版卸载后"的真机审计，见附录 A）：

| 项 | 现状（旧版卸载后） | 本设计要求 |
|---|---|---|
| wireguard 接口 / 地址 / MTU | 已清干净 | 同 |
| `ip rule` / `ip route` / table 51820 | 已清干净 | 同，且必须按句柄清（§8.4） |
| 生成脚本（NonGlobal / Rule / keepalive） | 已删 | 本设计不生成脚本 |
| systemd 单元 | 已删 | 同 |
| `/usr/bin/warp` | 已删 | `/usr/bin/warpgo` |
| ipset / dnsmasq 配置 | 已清 | 同 |
| **`/etc/resolv.conf`** | ⚠️ **仍是 `nameserver 1.1.1.1`，且 `resolv.conf.origin` 备份已被删** | **必须还原**；且原文件备份保留到用户显式确认后才删 |
| BBR sysctl | 保留（合理，无害） | 保留但提示 |
| 调试备份文件（`*.bak-*`） | 留在 `/etc/wireguard/`（14 个） | 测试残留由 M0 统一移到 `/root/warp-old-backup/` |

幂等要求：`up` 连跑 3 次规则数不变；`down` 后零残留；`install` 可重复执行。

### 8.14 版本与遥测

- `v` 变成 `warpgo version --check`：查 GitHub releases 提示新版本；`--self-update` 才替换二进制（带校验和）。
- 现有实现**每次运行都向 `stat.cloudflare.now.cc` 上报运行次数且无开关**。
  新实现**默认不上报**，只保留本地计数；如需远程统计做成显式 opt-in。

### 8.15 i18n

中英双语，按 `LANG` 判断（`zh_*` → 中文）。文案集中在 `internal/i18n`，
**不引入任何 i18n 框架**（两个 map + 一个 `T(key)` 函数足够）。`--lang en|zh` 可覆盖。

---

## 9. CLI 与菜单

子命令（非交互优先，菜单只是它的薄封装）：

```
warpgo menu                                   # 交互菜单（等价旧的裸 warp）
warpgo register | rotate | account ...
warpgo up [--mode non-global|global] [--family 4|6|d] [--backend kernel|userspace] [--force]
warpgo down | status [--json] | watch [--once]
warpgo switch --mode global        # g
warpgo switch --backend reserved   # k
warpgo priority 4|6|d              # s
warpgo split enable|disable|status # e
warpgo proxy install|on|off        # w / y
warpgo client install --mode proxy|warp | on | off    # c / l / r
warpgo hook add|del                # z / x（隐藏，供 warp-svc 调用）
warpgo bbr enable|disable          # b
warpgo uninstall [--purge]
warpgo version [--check] [--self-update]
```

退出码：`0` 成功 / `1` 通用错误 / `2` 体检不健康 / `3` 需要 reserved 但后端不支持 /
`4` 被单实例锁挡住 / `5` 非 Debian 系统。

---

## 10. systemd

```ini
# warpgo-watch.service —— 保活常驻
[Service]
Type=simple
ExecStart=/usr/bin/warpgo watch
Restart=always
RestartSec=30s
```

`watch` 自身幂等（健康时什么都不做），所以 restart 不会造成 IP 抖动。
`down` 时执行 `systemctl reset-failed warpgo-watch.service`
（照搬"ExecStop 失败会中止 restart"那轮的教训）。

---

## 11. 里程碑与验收

| # | 内容 | 验收（真机 Debian 12） |
|---|---|---|
| **M0** | 骨架 + 状态/锁/i18n/日志 + 把调试残留移走 | `warpgo version` 能跑；`--lang` 生效；单实例锁三种行为（占用提示 / 心跳 / 接管）单测过 |
| **M1** | `cloudflare` + `wgconf`：注册、reserved、profile | `warpgo profile > w.conf` 后 `wg-quick` 起得来且得 `warp=on`；`y8eh→[203,199,161]` 单测过 |
| **M2** | `tunnel`(内核) + `netcfg` 非全局 + `up/down/status` + 菜单骨架 | 经接口得 `warp=on`、**SSH 不断**、默认路由仍原生；`up` 连跑 3 次规则数不变；`down` 零残留 |
| **M3** | 全局模式 + fwmark + watchdog 回滚 | 全局下 SSH 不断；人为把端点改黑洞 → 45 秒内自动回滚且网络恢复 |
| **M4** | `health` + `keepalive` + systemd + **卸载** | ① 健康时连跑 5 次**接口创建时间不变**；② `ip link del warp` 后一轮内自愈；③ 伪造旧格式锁能接管；④ `uninstall` 后系统回到 M0 前的干净状态（含 DNS 还原） |
| **M5** | 用户态后端 + reserved + `rotate` + 账户/license | 需要 reserved 的区域握手成功；`rotate` 换 IP 后仍 `warp=on`；账户类型显示正确 |
| **M6** | `s` 栈优先级 + `e` 分流 | 优先级切换后 `curl -4/-6` 出口符合预期；分流开启后命中走 warp、未命中走原生；关闭后 iptables/ipset/dnsmasq 零残留 |
| **M7** | `w` / `y` WireProxy | socks5 可用；开关后进程 / 单元 / 配置干净 |
| **M8** | `c` / `l` / `r` 官方 Client + `z` / `x` 钩子 | 三种模式切换正确；`z`/`x` 的 ip rule 加减成对；`r` 开关后 `warp-cli status` 符合预期 |
| **M9** | `b` BBR + 版本/自更新 + 打包 | BBR 开关与还原正确；静态二进制 + `.deb` 可装可卸；`--check` 能查到新版本 |

**排序理由**：主干（自己实现的接口与出口）先行 —— 分流 / 代理 / 官方客户端都依赖它；
危险与边缘功能放最后。

---

## 12. 测试策略

### 单元（无 root、无网络）

reserved 解码（`y8eh` / `4A3x` / 非法输入）、UAPI 序列化（字节级，含结尾空行）、
profile 生成、trace 解析（`on`/`plus`/`off`/空/半截）、路由规则生成（两模式、"先清后加"的顺序）。

### 假实现（`tests/fake/`）

- **假 netlink**：断言 ① `up` 三次后规则各 1 条 ② `down` 后完全空
  ③ 模拟"接口已删 + `[detached]` 显示形态"时仍按句柄清干净。
- **假 UAPI socket**：断言握手序列、`reserved` 确实下发到用户态后端。

### 真机（`tests/e2e/`，Debian 12）

按里程碑逐条跑，并**沿用本项目已踩出来的实验纪律**：

1. 测 DNS 失效用 `unshare -m --propagation private` + `mount --bind` 伪造
   `/etc/hosts` 与 `/etc/resolv.conf`，**绝不改宿主**。
2. 测保活/重建前**先等单实例锁空闲**，否则会把"被挡住、什么都没做"误读成"成功"。
3. 判断"接口是否被重建"用 **`/sys/class/net/warp` 的 mtime**，不要用 `wg latest-handshakes`
   （它会随正常 re-handshake 自然推进）。
4. 测前停巡检、测完拉回，并挂"死人开关"。

---

## 13. 风险登记

| 风险 | 影响 | 缓解 |
|---|---|---|
| 非公开 API 版本号变更 | 注册全挂 | 版本号/UA 集中在 1 个文件；`profile` 支持离线导入；必要时可回落调用 `wgcf` |
| CF 推进 MASQUE | WireGuard 未来可能失效 | 分层设计，换传输不动 `netcfg` |
| reserved 需用户态 | 内核模式在 HK/LA 握手失败 | 内置 vendored patched device；`SetReserved` 明确报不支持并提示切后端 |
| **全局模式切断 SSH** | 机器只能重装 | 默认非全局；全局走 fwmark + watchdog（§8.5） |
| **DD 重装系统** | 一次参数错即报废 | 默认不编入（§14.1） |
| 第三方二进制依赖（wireproxy / cloudflare-warp） | 上游改了就没法用 | 版本固定 + 校验和；失败时明确报错，不影响主干功能 |
| iptables/nftables 分裂 | 分流不生效或残留 | 优先 nft；两套后端都做 e2e |
| 二进制分发 | 用户拿到旧版 | `--check` + `--self-update`（带校验和）；CI 出 amd64/arm64 与 `.deb` |

---

## 14. 我方保留意见（需要你一句话确认）

### 14.1 DD 重装系统：建议不编入主程序

`b` 里的 DD 会**把整台机器重装成另一个系统**，参数错一位就是机器报废
（而且往往是在远程、无 IPMI 的情况下）。上游把它和 BBR 放在同一个菜单项里，是很危险的耦合。

建议：**默认不编入**。如果你确实要用，我做成
① 独立子命令 `warpgo dd --i-know-what-i-am-doing`，
② 必须显式提供镜像 URL 与校验和，
③ 交互二次确认 + 打印"即将执行的确切命令"，
④ 不做任何"自动选镜像"逻辑。

### 14.2 Team 令牌注册（Teams OAuth）：要不要？

`api.sh` 里有一套完整的 Teams Token 验证码流程（curl + cookie 手工复刻 OAuth），
用于加入 Cloudflare Zero Trust 组织。这与"拿到 WARP 出口"无关，实现量约 300–500 行。

如果你不用 Teams，建议砍掉（`account` 只保留 free / WARP+ 与设备列表）。

---

## 附录 A：旧版卸载后的真机审计（2026-10-14 实测）

测试机：Debian 12，非全局，原生 v4+v6，用户已手动卸载旧版。

| 检查项 | 结果 |
|---|---|
| wireguard 接口 | 已删 |
| 默认路由 | 已回到原生（`default via 31.57.218.1 dev enp1s0`） |
| `ip -4 rule` | 只剩 `local` / `main` / `default` 三条基本规则 |
| `table 51820` | 空 |
| `wg-quick@warp` 单元 | 不存在 |
| `warp-svc` 单元 | 不存在 |
| `/usr/bin/warp` | 已删 |
| `/etc/wireguard/` 生成物 | 已删（只剩调试时建的 14 个 `*.bak-*`） |
| ipset 规则集 / dnsmasq 配置 | 已清 |
| crontab | 空 |
| **`/etc/resolv.conf`** | ⚠️ **仍是 `nameserver 1.1.1.1`；`/etc/resolv.conf.origin` 备份已被删** → 无法判断是原值还是 WARP 留下的，且用户再也无法自行还原 |
| BBR（`tcp_congestion_control=bbr`） | 保留（合理） |

**结论**：旧版卸载整体干净，唯一真实缺口是 **DNS 没有还原、且原文件备份被销毁**。
本设计的 `uninstall` 必须：还原 DNS；**并把原文件备份保留到用户显式确认后才删除**。

---

## 附录 B：复刻要用的 API 契约（已从 `api.sh` 逐条核对）

```
WireGuard / X25519（本设计使用）
  POST  https://api.cloudflareclient.com/v0a2158/reg
        User-Agent: okhttp/3.12.1
        CF-Client-Version: a-6.10-2158
        Content-Type: application/json
        {"key":<pubkey>,"install_id":<id>,"fcm_token":<id>,"tos":"<ISO8601>",
         "model":"PC","serial_number":<id>,"locale":"zh_CN"}
  PATCH https://api.cloudflareclient.com/v0a2158/reg/{id}
        {"warp_enabled":true}
  GET   https://api.cloudflareclient.com/v0a2158/reg/{id}              # 账户类型 / 设备
  PUT   https://api.cloudflareclient.com/v0a2158/reg/{id}/account      # license 绑定
  DELETE https://api.cloudflareclient.com/v0a2158/reg/{id}             # 注销

MASQUE（本设计不做，仅记录）
  POST  https://api.cloudflareclient.com/v0a4471/reg
        User-Agent: WARP for Android ; CF-Client-Version: a-6.35-4471
        {"key":<p256-pub>,"install_id":…,"fcm_token":…,"type":"Android",
         "locale":"en-US","key_type":"secp256r1","tunnel_type":"masque"}

从响应中取：
  config.interface.addresses.v4 / .v6    → 接口地址
  config.peers[0].public_key / .endpoint → 对端
  config.client_id                       → reserved = base64decode(前 3 字节)
```
