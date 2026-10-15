# warpgo 设计文档（v2 · 完整菜单重写）

> **本文档取代 v1**（v1 只覆盖"前三项功能"）。现已按用户决定扩为**重写整个菜单**。
>
> 目标：用 Go 重写 fscarmen/warp 的**全部菜单功能**，**仅支持 Debian**，>   
> 不适配 Docker、macOS、路由器（OpenWrt）等平台。
>
> - 仓库：`C://Users//momo//Documents//GitHub//warpgo`（本文件即仓库内 `docs/DESIGN.md`，是唯一正式版本）
> - 状态：**M8 已完成**（官方 Linux Client + 钩子 + 交互菜单；`e` 分流与运行次数统计已按要求砍掉）
> - 编译 / 测试 / 调试**一律在 Debian 上进行**（见 §12）
> - §14 有两处我方保留意见，待确认（DD 重装系统、Teams OAuth）

---

## 1. 用户已拍板的决定

| #  | 决定                                       | 说明                                                                                                                           |
| -- | ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| 1  | **功能边界 = 全搬**                            | "不要其他客户端"的真实含义是**只要 Debian、不适配 Docker / macOS / 路由器**；<br />菜单里所有功能都要，**包括**官方 Linux Client（`c`/`l`/`r`）与 WireProxy（`w`/`y`） |
| 2  | **默认模式 = 非全局**                           | 只对指定源地址加策略路由，天然不会切断 SSH                                                                                                      |
| 3  | **reserved = 一等公民**                      | 需要时自动切用户态后端；最终**没有** vendor 补丁版 wireguard-go，改为包装 `conn.Bind`（§8.2）                                                          |
| 4  | **全新开始，不迁移旧安装**                          | 用户已卸载旧版；不需要"导入现有 warp.conf"模块；测试可随意                                                                                          |
| 5  | **DD 重装系统不编入**（2026-10-14 追加）            | 参数错一位即机器报废，且往往在远程无 IPMI 的情况下；见 §14.1                                                                                         |
| 6  | **Teams OAuth 砍掉**（2026-10-14 追加）        | 与"拿到出口"无关，约 300–500 行                                                                                                        |
| 7  | **只保留简体中文**（2026-10-15 追加）               | 移除英语与语言选择：不再按 `LANG` 推断、不再有 `--lang`；命令行 / 日志 / 菜单一律中文，见 §8.15                                                               |
| 8  | **账户 / license / 设备管理砍掉**（2026-10-15 追加） | WARP+ 绑定、设备列表、改名、注销都不做；`account` 只留纯本地只读视图（§8.7）                                                                             |
| 9  | **`e` 分流砍掉**（2026-10-15 追加）              | iptables + dnsmasq + ipset 那一套（上游 `iptables_solution()`，129 行 + 配置）；见 §3                                                     |
| 10 | **运行次数统计砍掉**（2026-10-15 追加）              | 上游每次运行都向 `stat.cloudflare.now.cc` 上报且无开关；warpgo **不做任何上报，也不做本地计数**；见 §8.14                                                   |

> 用户机器（Debian 12，非全局，原生 v4+v6）**已确认卸载干净** —— 见附录 A 的真机审计。>   
> 该机器可作开发与真机验收的测试机，可随意安装/卸载。

---

## 2. 功能矩阵（逐项对照现有菜单）

| 菜单项                                | 现有实现                                     | Go 模块                      | 里程碑                 | 备注                                                                                                           |
| ---------------------------------- | ---------------------------------------- | -------------------------- | ------------------- | ------------------------------------------------------------------------------------------------------------ |
| `4` `6` `d` 装接口/切栈                 | `install()` + `stack_switch()`           | `netcfg` + `tunnel`        | **M2**              | 已装则变为"切栈"（改 AllowedIPs 注释）                                                                                   |
| `o` 临时开关                           | `onoff()`                                | `cli` + `netcfg`           | M2                  | 关 = 拆接口但保留配置；开 = `up --force`                                                                                |
| `n` 获取 IP / 重连                     | `net()`（199 行）                           | `health` + `keepalive`     | ✅ M2/M4             | **新语义**：默认先体检，健康不动；`--force` 才重建。保活即 `watch`（M4）                                                             |
| `i` 换 IP（Netflix）                  | `change_ip()`（214 行）                     | `cloudflare` + `netcfg`    | ✅ **M5**            | 重新注册换出口；**不做**流媒体解锁探测（见 §8.16）                                                                               |
| `g` 全局/非全局切换                       | `working_mode_switch()`                  | `netcfg.route` + `switch`  | ✅ M3/M5             | 要连路由一起换，不是只改注释                                                                                               |
| `k` 内核↔用户态                         | `kernel_reserved_switch()`               | `tunnel` + `userspace`     | ✅ **M5**            | 同一套实现对两种后端；见 §8.2                                                                                            |
| `s 4/6/d` 栈优先级                     | `stack_priority()` + `result_priority()` | `gai` + `cli.priority`     | ✅ **M6**            | 只写 `/etc/gai.conf` 里那两行；**实测** `4` 生效、`6` 与默认等价，见 §8.8                                                       |
| `u` 卸载                             | `uninstall()`（93 行）                      | `cmd/uninstall.go`         | ✅ **M4**（DNS 还原已实测） | 见 §8.13 的"必须还原清单"                                                                                            |
| `v` 版本/同步                          | `ver()` + `check_cdn()`                  | `cli.version`              | M9                  | Go 版无"同步脚本"，改为查新版本 + 可选自更新                                                                                   |
| `b` 升级内核 + BBR                     | `bbrInstall()`                           | `system.bbr`               | M9                  | 只做 BBR；内核升级只做"检查 + 提示"                                                                                       |
| ~~`b` 里的 DD 重装系统~~                 | `bbrInstall()` 内                         | —                          | —                   | **已确认不编入**（§14.1）                                                                                            |
| ~~`e` 分流（iptables+dnsmasq+ipset）~~ | `iptables_solution()`（129 行）+ 配置         | —                          | —                   | **已确认砍掉**（2026-10-15，§3）：上游那段还带一个 P0 级 bug，且与"拿到出口"无关                                                        |
| `w` WireProxy 方案                   | `wireproxy_solution()`                   | `proxy`                    | ✅ **M7**            | 引入第三方 `wireproxy` 二进制（**下载 + 强制校验和**），见 §8.9                                                                 |
| `y` WireProxy 开关                   | `wireproxy_onoff()`                      | `proxy`                    | ✅ **M7**            | 开之后**经 socks5 实测** warp=on 才算成功                                                                              |
| `c` 官方 Client → socks5             | `client_install()`（137 行）                | `cfclient`                 | ✅ **M8**            | 依赖 Cloudflare 官方 `.deb`；**默认模式**（只开本地 socks5）                                                                |
| `l` 官方 Client → WARP 模式            | `client_install()`                       | `cfclient`                 | ✅ **M8**            | 会接管默认路由、可能切断 SSH，动手前明确警告（§8.10）                                                                              |
| `r` Client 开关                      | `client_onoff()`                         | `cfclient`                 | ✅ **M8**            |                                                                                                              |
| `z` / `x` 隐藏命令                     | `rule_add` / `rule_del`                  | `cfclient.HookAdd/HookDel` | ✅ **M8**            | 供 `warp-svc` 的 ExecStartPost/ExecStop 调用；用 drop-in 挂载                                                        |
| 交互菜单（裸 `warp`）                     | `menu()`                                 | `cmd/warpgo/menu.go`       | ✅ **M8**            | 薄封装：每个选项就是一条子命令                                                                                              |
| `h` 帮助                             | `help()` + E[6]/C[6]                     | `cli.help`                 | M2                  | 随功能逐步补全                                                                                                      |
| 菜单内状态显示                            | `check_system_info()` + `menu()`         | `health` + `cli.menu`      | M2                  | IPv4/IPv6 / 模式 / 内核 / 账户类型                                                                                   |
| ~~语言选择~~                           | `select_language()` + 141 条文案            | `msg`                      | —                   | **中英双语已移除**（2026-10-15）：只保留简体中文，见 §8.15                                                                      |
| 账户 / license / 设备                  | `api.sh` 的 account 子命令                   | —                          | —                   | **已确认砍掉**（2026-10-15）：WARP+ 绑定、设备列表、改名、注销都不做；`warpgo account` 只留一个纯本地的只读视图。唯一保留的设备管理动作是 `rotate` 内部给自己的旧设备收尾 |
| ~~Team 令牌注册~~                      | `api.sh` 的 Teams OAuth 流程                | —                          | —                   | **已确认砍掉**（§14.2）                                                                                             |
| ~~运行次数统计~~                         | `statistics_of_run-times()`              | —                          | —                   | **已确认砍掉**（2026-10-15，§8.14）：不做远程上报，**也不做本地计数**                                                               |

---

## 3. 明确不做

- Docker 方案（`docker.sh`，676 行）
- macOS 客户端（`pc/mac.sh`，416 行）
- 第三方内核 / OpenWrt / sing-box 导出（`warp-go.sh`，1471 行）
- 除 Debian 外的发行版适配（不做 Alpine / busybox / openresolv 分支）
- 纯 shell 实现（用编译产物换确定性与可观测性）
- **Teams OAuth 令牌注册**（2026-10-14 用户确认砍掉）：与"拿到 WARP 出口"无关，约 300–500 行
- **DD 重装系统**（2026-10-14 用户确认不编入）：见 §14.1
- **`e` 分流（iptables + dnsmasq + ipset）**（2026-10-15 用户确认砍掉）：    
  它要同时维护 ipset 集合、dnsmasq 配置、两套 iptables 后端，而上游那份实现里还带着    
  一个 P0 级 bug（`PostUp` 加的规则与 `PostDown` 删的规则根本不是同一条）。
- **运行次数统计**（2026-10-15 用户确认砍掉）：见 §8.14。既不做远程上报，也不做本地计数    
  —— 一个"今天运行了几次"的数字对使用者没有任何用处，而它换来的是一条"每次运行都联外网"    
  的行为。**没有遥测。**
- **账户 / license / 设备管理**（2026-10-15 用户确认砍掉）：见 §8.7 与 §2 的对照表。

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
cmd/warpgo             CLI / 菜单
   │
   ├── cloudflare ─────── 注册 / 启用 / reserved（不做 license、设备列表、Teams）
   │
   ├── tunnel ─────────── 抽象：Ensure / Status / Close / TearDown
   │      ├── kernel      （netlink 建接口 + UAPI 下发）
   │      └── userspace   （独立进程跑官方 wireguard-go + 包装 conn.Bind 注入 reserved）
   │
   ├── netcfg ─────────── 与传输无关：地址 / MTU / DNS / 路由 / 策略路由 / 回滚
   ├── gai ────────────── 地址族优先级（只动 /etc/gai.conf 里那两行）
   ├── proxy ──────────── WireProxy：下载校验、配置生成、单元与开关
   │
   ├── cfclient ───────── 官方 Linux Client 的安装 / 模式切换 / 开关 / z·x 钩子   （M8）
   │
   ├── health ─────────── 体检（trace，IP 字面量优先；可经接口或经 socks5）
   ├── keepalive ──────── 巡检与自愈（健康不动）
   └── state ──────────── 账户与运行态、单实例锁、卸载
```

**分层的意义**：`netcfg` 完全不知道隧道是内核还是用户态建的；将来 CF 若把 WireGuard  
换成 MASQUE，只需新增一个 `tunnel` 实现，其余层不动。

---

## 6. 目录结构

`✅` 表示已落地（M0–M5），其余为规划中的位置。

```
warpgo/
├── cmd/warpgo/
│   ├── main.go         ✅ CLI 分派、全局选项、dev 子命令
│   ├── opts.go         ✅ 极简选项解析（--name value / --name=value）
│   ├── register.go     ✅ 注册（与 rotate 共用 registerDevice）
│   ├── account.go      ✅ 账户查看（纯本地只读）
│   ├── profile.go      ✅ wg-quick 配置输出
│   ├── ensure.go       ✅ 建隧道的共用流程（up / watch / rotate / switch 走同一份）
│   ├── up.go           ✅ 建立隧道
│   ├── down.go         ✅ 先停保活，再拆隧道（含用户态守护进程），最后还原 DNS
│   ├── status.go       ✅ 状态
│   ├── rotate.go       ✅ 换 IP（换账户 + 回退保护 + 旧设备收尾）
│   ├── switch.go       ✅ 在线互换后端 / 模式（并同步改写保活单元）
│   ├── watch.go        ✅ 巡检命令
│   ├── install.go      ✅ 装成 systemd 服务（含单元渲染的共用函数）
│   ├── uninstall.go    ✅ 撤干净（含 DNS 还原）
│   ├── guard.go        ✅ confirm / rollback-guard
│   ├── userspace.go    ✅ userspace-daemon（内部命令）
│   ├── client.go       ✅ 官方 Linux Client 的编排（install / mode / on / off / status / uninstall）
│   ├── hook.go         ✅ hook add|del（隐藏子命令，供 warp-svc 的 ExecStartPost / ExecStop）
│   ├── menu.go         ✅ 交互菜单（每个选项就是一条子命令）
│   └── dns.go          ✅ resolv.conf 的备份与还原
├── internal/
│   ├── cloudflare/  ✅ api.go types.go reserved.go account.go   # 不做 teams.go
│   ├── wgconf/      ✅ keys.go profile.go        # X25519 密钥、wg-quick 配置
│   ├── tunnel/      ✅ tunnel.go common.go kernel.go userspace.go daemon.go reserved.go
│   ├── netcfg/      ✅ netcfg.go link.go route.go rulenl.go plan.go apply.go
│   ├── health/      ✅ probe.go                  # 只读探测（M3 自检 / M4 保活 / M5 换后端共用）
│   ├── keepalive/   ✅ keepalive.go              # 巡检策略与循环（依赖全部可注入）
│   ├── rollback/    ✅ rollback.go               # 快照 / 保险丝 / 回滚
│   ├── systemd/     ✅ systemd.go                # 单元渲染 + systemctl
│   ├── dns/         ✅ dns.go                    # resolv.conf 备份/改写/还原
│   ├── state/       ✅ fs.go lock.go proc*.go runtime.go
│   ├── system/      ✅ platform.go               # bbr.go / kernel.go / dd.go 属 M9
│   ├── logx/        ✅ logx.go
│   ├── version/     ✅ version.go
│   ├── msg/         ✅ keys.go text.go          # 文案表：只有简体中文
│   ├── gai/         ✅ gai.go                   # /etc/gai.conf 的受管两行（M6）
│   ├── proxy/       ✅ config.go proxy.go download.go                        # WireProxy（M7）
│   └── cfclient/    ✅ client.go install.go mode.go hooks.go                 # 官方 Linux Client（M8）
├── docs/DESIGN.md                                # 本文件（唯一正式版）
├── tests/e2e/       ✅ m0-lock.sh m1-register.sh m2-updown.sh m3-global.sh
│                       m4-watch.sh m5-userspace.sh m6-priority.sh m7-proxy.sh
│                       m8-client.sh
├── build.sh         ✅ 产出 dist/warpgo
└── （原计划的 third_party/wireguard-go/ 与 internal/split/ 都已**取消** —— 见 §8.2 与 §3）
```

与规划的两处偏差（都已落地，在此说明而不是留成「文档与现实不符」）：

- 单实例锁没有单独的 `state.go` / `uninstall.go` —— 锁在 `lock.go` + `proc*.go`，    
  卸载逻辑在 `cmd/warpgo/uninstall.go`：它要用到 msg 与 systemd，    
  放进 `internal/state` 会把依赖方向搞反（底层包不该依赖业务包）。
- 单元文件不随仓库分发，而是由 `install` 现场渲染 —— `ExecStart` 里要写安装时的    
  路径参数，预置一份静态文件反而会与实际不符。
- **不再 vendor 打过补丁的 wireguard-go**（原计划 `third_party/wireguard-go/`）。    
  真机验证表明补丁的全部内容就是"收发两个方向各改写类型字段的 3 个字节"，    
  在 `conn.Bind` 上包一层即可逐字节等价，见 §8.2。

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
    CFClient *CFClientState `json:"cfclient,omitempty"`   // M8
}
```


存放：`/etc/warpgo/account.json`（0600）、`/etc/warpgo/runtime.json`；  
WireProxy 另有两份：`/etc/warpgo/proxy.conf`（0600，含私钥）与  
`/etc/warpgo/proxy.json`（安装来源与端口，见 §8.9）。  
地址族优先级**没有**状态文件 —— 它的事实来源就是 `/etc/gai.conf` 本身（§8.8）。

**实现落点（M1）**：`Account` 定义在 `internal/cloudflare/account.go`，但密钥与地址存成  
字符串（base64 / CIDR）而不是定长数组与 `netip.Prefix` —— 因为 `account.json` 是要给人看、  
给人复制的文件。需要定长类型时由 `wgconf` 解析，依赖方向保持单向（`wgconf → cloudflare`）；  
`state` 只提供状态目录与原子写原语，不依赖任何业务类型。

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
- `bepass-org/warp-plus` fork 了 wireguard-go 的 device 来支持 reserved。
- 生态约定：把 reserved 写成注释行 `# Reserved = 171, 85, 205` 交给客户端读。

#### 那 3 个字节到底写在哪（真机抓包确认）

WireGuard 每个报文的前 4 字节是小端 `uint32` 的报文类型（01 发起 / 02 响应 /  
03 cookie / 04 数据）。**reserved 替换的是它的高 3 字节**（`byte[1:4]`），  
低字节仍是类型。上游补丁就是这么做的：

```go
// 发送：bepass-org/warp-plus  wireguard/device/peer.go
if len(buffers[i]) > 3 && buffers[i][0] > 0 && buffers[i][0] < 5 {
    copy(buffers[i][1:4], peer.reserved[:])
}
```

**关键发现（M5 真机抓包，抓了几轮才看清）：服务端会把 reserved 原样盖在它的  
响应上。** 内核后端（发送 `01 00 00 00`）收到的响应是 `02 00 00 00`；  
用户态后端（发送 `01 24 D4 90`）收到的响应就是 `02 24 D4 90`。

而 WireGuard 是按 **4 字节小端**读类型的：

```go
msgType := binary.LittleEndian.Uint32(packet[:4])   // 0x90D42402 ≠ 2
```

于是**只做发送方向的"打补丁"必然失败**：包发出去了、服务端也回了、  
握手永远完不成，而且**没有任何报错**（响应被当成未知报文丢掉）。  
所以上游补丁在收包侧还有第二处：

```go
// 接收：bepass-org/warp-plus  wireguard/device/receive.go
packet := bufsArrs[i][:size]
packet[1], packet[2], packet[3] = 0, 0, 0
msgType := binary.LittleEndian.Uint32(packet[:4])
```

判据只能靠抓包：`tcpdump -X` 看报文偏移 `0x0010` 那行的最后两组十六进制。

#### 实现形态：包装 Bind，不 vendor 一份 fork

设计文档 v1 写的是「vendor 一份打过补丁的 `third_party/wireguard-go/`」。  
**实测后改成包装 `conn.Bind`**，理由是补丁的全部内容就是上面那两处字节改写：

- 出网报文一定经过 `conn.Bind.Send`；
- 入网报文一定经过 `conn.Bind.Open` 返回的接收函数。

在这两个位置各包一层即可，**逐字节等价**，于是不必在仓库里养一个一万多行的  
fork（也不必再跟上游同步、不必维护 `replace` 指令）。  
实现见 `internal/tunnel/reserved.go` 的 `ReservedBind`。

> 如果将来 Cloudflare 改的不是"哪几个字节"而是更深的协议行为，>   
> 那时再考虑 vendor；现在的证据是：它只改字节。

#### 后端选择：内核优先，实测不通才换

- **内核后端**（`kernel`）性能最好，但结构上带不了那 3 个字节。    
  这条路径上绝对不发 `[0,0,0]` 之外的任何东西 —— 真机抓包守着它。
- **用户态后端**（`userspace`，别名 `reserved`）用内嵌的 wireguard-go    
  （`device` + `tun` + `ipc` 三个包，走 `golang.zx2c4.com/wireguard` 模块），    
  由 `warpgo userspace-daemon` 这个**独立进程**持有 TUN。
  - 为什么必须是独立进程：wireguard-go 的设备生命周期 = 持有 TUN 的进程。      
    嵌进 `up` 里的话，命令一退出隧道就没了。
  - 父进程通过 UAPI socket（`/var/run/wireguard/<if>.sock`）用 `wgctrl`      
    配置它 —— wgctrl 对内核设备与用户态设备一视同仁，所以"加地址 / 下发配置 /      
    读状态"三段与内核后端**共用同一份代码**（`internal/tunnel/common.go`）。
  - 守护进程把 MTU 与 reserved 记在 `/run/warpgo/userspace-<if>.json` 里；      
    参数变了就换一个进程（`rotate` 换了账户 → reserved 变了，这一步必然发生）。
- **选择规则**（`resolveBackends`）：
  1. 用户显式 `--backend kernel|userspace` → 只有一个候选，**不做回落**       
     （回落会让 `--backend kernel` 变成一个"建议"，与命令行说的不一致）。
  2. 没指定且 `reserved` 全零 → 内核。行为与 M2/M4 逐字节一致。
  3. 没指定且 `reserved` 非零 → **内核优先，用户态兜底**：       
     只有内核实测握不上手（经接口拿不到 `warp=on`）才自动换用户态，       
     并在日志里说明"已自动切换"。
  4. 没指定、带 reserved、且**现在跑着的就是用户态** → 用户态优先。
  第 4 条是第 3 条的补充，也是真机验收逼出来的：`up` 与 `watch` 每次都要重新    
  决策一遍，若顺序永远固定成"内核优先"，一台**只能用用户态**的机器每修一次链路    
  就得先把可用的用户态隧道拆掉、去试一个已知不通的内核后端、验证失败后再拆掉    
  重建回用户态 —— 多花一倍时间，还平白重建一次接口。    
  所以 auto 的准确语义是"**在两种后端之间自适应**"，而不是"永远先试内核"。    
  想换回内核：`up --backend kernel` 或 `switch --backend kernel`。
- **谁来验证**（`ensureOptions.ProbeAfterEnsure`）：
  - `up` / `switch` 打开：用户要的是"能用"，验不过就该去试另一个后端；      
    最后一个候选也验不过就如实报 `exitUnhealthy`（而不是"命令成功但网不通"）。
  - `watch` **关闭**：巡检刚从"不通"里恢复，立刻再探一次既是重复劳动，      
    也会把"我已经修好了"误报成"修不好"（它下一轮本来就会体检）。
  - `rotate` 自己显式验一次：它的全部安全承诺都建立在"新账户确实能用"之上，      
    而它可能被钉死了后端（那时不会触发自动切换里的验证）。
  - 验证**会重试**（3 次、间隔 1.5s、总预算 40s）。这是"用探测结果决定要不要      
    换后端"的策略，误判的代价是拆掉一条本来可用的隧道 —— 真机上就撞到过一次      
    误判（重跑三次都没复现，属瞬时抖动），多给几次机会比"宁可错杀"划算得多。
- 退出码 `3`（需要 reserved 但后端不支持）只在一种情况下出现：    
  用户**钉死**了内核、账户带 reserved、且隧道实测不通。

### 8.3 建接口

netlink 一次完成：建 `wireguard` 类型 link → 加地址（`172.16.0.2/32`、`2606:4700:110:xxxx::/128`）  
→ 设 MTU → 起 link → UAPI 下发（私钥、对端公钥、端点、AllowedIPs、`PersistentKeepalive=25`）。

用户态后端不做 `ip link add` —— TUN 由守护进程 `tun.CreateTUN(name, mtu)` 产生，  
`Ensure` 只是"保证守护进程在跑 + 之后走同一套地址/配置流程"。

**MTU 探测**照搬现在 `best_mtu()` 的策略（1420 起向下试到能过 1500 字节包），  
但用 Go 直接发探测包，不 fork `ping`/`curl`。

### 8.4 出口接管

**非全局（默认）** —— M2 的实现形态（pref 用固定号，便于按号精确清理）：

```
# 两族都有：让 main 表里的「具体」路由优先于 WARP 的默认路由
ip -4/-6 rule add pref 9000 lookup main suppress_prefixlength 0
# v4：以 WARP 地址为源的流量交给 51820 表
ip -4 rule add pref 9010 from 172.16.0.2 lookup 51820
# v6：同样用源地址匹配（上游用的是 `oif`，这里统一成语义更明确的 from）
ip -6 rule add pref 9011 from <warp-v6-addr> lookup 51820
# 表里放默认路由（谁来用由上面的规则决定）
ip -4/-6 route add default dev warp table 51820
```

**`suppress_prefixlength 0` 是必需的一条**，它解决一个很具体的问题：如果只写「源地址是 WARP 地址 → 查 51820 表」，那么以 WARP 地址为源的流量去访问**内网**时，51820 表里只有一条默认路由，包会被塞进隧道，内网直连就断了。有了优先的 suppress 规则，流量先查 main 表里的具体路由（内网、直连网段），只有 main 给不出结果时才轮到 WARP 的默认路由。

副作用为零：对普通流量只是多一次查表、结果相同；SSH 响应包的源地址是公网 IP，既不被 `from` 规则匹配，也能在最后回落到系统默认的 main 规则。

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

**M3 的落地形态**（§8.5 的三条做成这样）：

```
pref 9000: lookup main suppress_prefixlength 0     # 两族：内网/直连优先
pref 9020: not fwmark 0xca6c lookup 51820          # 两族：其余流量交给 WARP
接口侧：fwmark = 0xca6c（内核给隧道自己的封装包打这个标记）
```

四处值得单独说的：

1. **顺序**：快照 → 派生保险丝 → 应用 → 自检 → 确认。派生必须在应用**之前** ——     
   否则断网那一刻保险丝还没出生，等于没有保险。
2. **保险丝是独立进程**（`setsid`，日志 `/run/warpgo/guard.log`）。它不是「备用方案」，     
   而是唯一可靠的方案：全局模式一旦断网，发起变更的那个 SSH 会话多半已经收不到     
   任何东西了，同会话的进程也会被一并带走。
3. **自检失败立即回滚**（不等超时），把断网窗口从 45 秒缩到十几秒；     
   保险丝是给「连自检都没跑完」的极端情况兜底的。两者都要，不是二选一。
4. **回滚目标是「回到没有 warpgo 隧道」，不是「恢复到变更前一模一样」**。     
   后者听起来更贴心，但要保存并复原整套规则 —— 复原过程本身出错就把     
   唯一的救命通道也搭进去了。回到干净状态更稳：用户看到提示后一条命令就能重来。

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

**M4 的落地形态**（§8.6 那三条做成这样）：

1. **体检是只读的**，而且只打 IP 字面量：`netcfg.LinkExists` 先确认接口在，     
   再用 `health.ProbeAny` 绑接口探测 —— 零 DNS、秒级返回。
2. **巡检进程永不因链路问题退出**。重建失败只上报事件，下一轮继续试。     
   这条是刻意的：如果失败就退出，systemd 会每 30 秒重启一次，日志里堆满     
   没有信息量的重启记录，而"端点不可达"并不会因为重启而自愈。
3. **单实例锁被占是正常情形**，不是错误。用户可能正在手工 `up`，本轮跳过就好；     
   但汇总里必须记一笔（`skipped 1`），否则用户会以为"巡检定过了一切正常"。
4. **"重建"包含硬复位**。现实中最常见的故障不是"接口被删了"，而是     
   **接口在、配置对、就是不通**。这时单纯的 `Ensure` 会认为"什么都不用改"，     
   于是永远修不好 —— 所以要显式做一次硬复位强制重新握手。     
   注意出口 IP **不会**因此改变：它是账户里分配的固定地址，变的只是这一次握手。

### 8.7 单实例锁

`/run/warpgo/net.lock`，内容 `PID 心跳时间戳`：

1. 拿不到锁**必须明确报错**，不能静默 return 0 —— 上游静默返回 0 让人把"什么都没做"     
   当成"已成功"，本轮真机验证因此得出过两次错误结论。
2. 心跳超过 180 秒未刷新 → 判定失控，终止并接管 —— 否则一个卡住的实例会让保活永远修不回来，     
   直接违反"断联必须能自愈"。
3. 每轮重建前刷新心跳；旧格式（无心跳）遗留锁一律按失效处理。

### 8.8 地址族优先级（`s`）

同一台机器同时有 IPv4 与 IPv6 出口时，访问一个双栈域名会先走哪一族，由 glibc 的  
`getaddrinfo` 决定，而它可以被 `/etc/gai.conf` 影响。`stack_priority()` 做的就是这件事。

**真机实测（Debian 12 / glibc 2.36 / getent ahosts）**：

| 状态                                 | `getent ahosts www.cloudflare.com` 的第一条  |
| ---------------------------------- | ---------------------------------------- |
| 原厂文件（两行都不写）                        | `2606:4700::6810:7b60`（IPv6）             |
| 加上 `precedence ::ffff:0:0/96  100` | `104.16.123.96`（IPv4）                    |
| 加上 `label 2002::/16   2`           | `2606:4700::6810:7b60`（IPv6，**与不写完全一样**） |
| 两行都去掉                              | `2606:4700::6810:7b60`（IPv6）             |

结论有三条，都写进了实现与文案里：

1. **`4` 是真的开关** —— 加那一行确实把顺序改成 IPv4 优先。
2. **`6` 不是对称的开关** —— 上游用的 `label 2002::/16   2` 在 Debian 12 上**不改变任何行为**，     
   因为 glibc 的默认表（`precedence ::/0 = 40` > `::ffff:0:0/96 = 10`）本来就是 IPv6 优先。     
   实现仍然写上那一行（与上游逐行一致、便于对照），但 `priority 6` **真正起作用的一步是     
   把 IPv4 优先那行删掉** —— 万一机器上有别的工具写过它，那一步才有意义。
3. **`d` = 删除受管的两行** = 回到系统默认。于是"卸载时的还原"不需要任何备份文件。

因此 `internal/gai` 的取舍是：

- **只动自己认识的那两行**，其余内容逐字节保留（含注释与空行）。原厂文件里本来就有一行    
  **被注释掉的** v4 写法（glibc 自带的文档），识别受管行必须按分词判断、且要跳过注释，    
  否则会连它一起删掉；反过来，用 `strings.Contains` 做断言也是错的 —— 那会把注释行    
  当成生效的配置。
- `ModeDefault` 天然是"移除我们的行"，所以 `Set` 幂等（内容一致时不写盘、mtime 不变），    
  `down` / `uninstall` 也只需要调一次 `Set(ModeDefault)`。
- **唯一的偏差**：原文件没有结尾换行时，往返之后会补上一个（追加行时必须补换行，    
  而删掉它时无从知道那个换行是不是我们补的）。Debian 的文件本来就以换行结尾。

`warpgo priority`（无参数）除了读文件，还会用 **`getent ahosts`** 实测一次解析顺序 ——  
这是唯一能回答"到底生效没有"的办法：**Go 自己的解析器不读 gai.conf**，  
用它去测这件事会得出"gai.conf 无效"的错误结论。

> 原先计划放在同一节的 `e` 分流（iptables + dnsmasq + ipset）**已按用户决定砍掉**，见 §3。

### 8.9 WireProxy（`w` / `y`）

它和隧道模式是**两条完全不同的路**：不起网络接口、不碰路由，而是在用户态跑一个小的  
wireguard 客户端（第三方二进制 `wireproxy`），把一个 socks5 入口挂在回环地址上。  
"哪些流量走 WARP"由使用方自己决定（浏览器、`curl --proxy`、程序里的代理设置），  
而不是由系统路由决定 —— 这正是它相对隧道模式的全部价值。

实现里几个刻意的决定：

- **发布仓库是 `windtf/wireproxy`，不是 `pufferffish/wireproxy`**。上游已经迁移：    
  老的仓库路径会 301 到新的，而发布资产的下载地址写的都是后者。直接钉新仓库少跳一次重定向。
- **版本默认钉死 `v1.1.3`**，不追 latest。装上去的是一个以 root 运行的可执行文件，    
  "可复现"比"总是最新"重要；要最新版写 `--version latest`（走一次 API）。
- **校验和强制**：从同一发布页取 `checksums.txt`（`<sha256>  <文件名>` 每行一条）比对。    
  取不到就**报错**，而不是静默降级成"不校验"，并给出两条出路：`--sha256 <值>`（配合    
  `--url`）或 `--from <本地归档>`（离线安装）。    
  *已知局限*：校验和与归档来自同一个 HTTPS 主机，它挡得住"下载被截断/被缓存污染"，    
  挡不住"发布页本身被攻破"。
- **归档只取自己认识的那个文件**：解包时只认常规文件、且一律 `filepath.Base` 取名字    
  （`wireproxy`；没有的话退而取"归档里唯一的常规文件"）。归档来自网络、落盘又是 root，    
  一个带 `../../etc/...` 的条目就能覆盖系统文件。
- **装完先自检再启动**：`wireproxy -n -c <conf>` 是 wireproxy 自带的配置校验，白送的确定性    
  —— 与其让服务启动失败再去翻 journal，不如写盘之后立刻问一句"这份配置你自己认不认"。    
  顺便验证 `--version` 能跑（架构不对只有执行才会暴露）。
- **配置文件放 `/etc/warpgo/proxy.conf`（0600）**，单元名 `warpgo-proxy.service`    
  —— 与上游的 `proxy.conf` / `wireproxy.service` **刻意不同**：同名会让"这台机器以前装过    
  上游脚本"的情形难以诊断，两个来源都以为自己管着同一个单元。
- socks5 **只监听 127.0.0.1**：绑到 `0.0.0.0` 等于把一条通往 WARP 的开放代理挂到公网上。
- **端口冲突要分清"是不是我们自己"**：重复 `install` 是很自然的动作（改参数、修现场），    
  而那时端口必然被我们自己的 wireproxy 占着。第一版只看"端口有没有人在听"，    
  于是**在装好的机器上重跑 install 必然失败** —— 那会让"重装"退化成"先手动停服务"。    
  现在只有"占用者不是我们"才拒绝（`installed && active && 端口就是当前这个` 不算冲突）。    
  这一条是真机验收逼出来的，配了一条纯函数单测（`portConflict`）。
- `on` 之后**必须经 socks5 实测**（`health.Options.Proxy`，走 Go 的 socks5 拨号器）    
  拿到 `warp=on` 才算成功；探不通时**刻意不停服务**（WARP 对端偶尔短暂不可用，    
  wireproxy 会自己重连；停掉反而让"过一会儿就好"变成"需要人再来一次"），    
  但以退出码 2 如实报告。
- **reserved 在这里发不出去**：wireproxy 用的是官方 wireguard-go，没有 reserved 支持。    
  账户带 reserved 时会明确警告（需要 reserved 的地区会表现为"配置都对、就是不通"），    
  并指向正确出路：`warpgo switch --backend userspace`。    
  *真机数据*：本机账户 reserved = `99,70,83`，而这个端点并不要求它 ——    
  WireProxy 实测能通（`warp=on`）。警告是**条件触发**的，不是拦路虎。


`warpgo uninstall` 会一并撤掉这套东西（单元 → 二进制 → 配置/状态），  
`proxy.conf` / `proxy.json` 也在 `--purge` 的白名单里。

### 8.10 官方 Linux Client（`c` / `l` / `r` / `z` / `x`）

- 添加 Cloudflare 官方 apt 源并安装 `cloudflare-warp`（amd64 / arm64）。
- `c` = 装完设成 socks5 代理模式；`l` = 设成 WARP 模式；`r` = `warp-cli connect/disconnect`。
- `z` / `x` = 隐藏子命令，供 `warp-svc` 单元的 `ExecStartPost` / `ExecStop` 调用以加减 `ip rule`。
- 这一块的实现本质是"编排官方客户端"—— 我们能重写的只有调用与状态管理，**不重写 warp-svc 本身**。

#### M8 的落地形态（2026-10-15，真机 Debian 12，`warp-cli 2026.8.2110.0`）

真实客户端上试出来的五件事（都是实跑，不是猜的）：

1. **`warp-cli -j` 才是稳的解析入口。** `settings` 的文本输出是     
   `(user set)	Mode: WarpProxy on port 40000` —— 前面多了一列"取值来源"、     
   值后面还跟着端口，按正则刮文本迟早被下一个版本打垮。     
   而 `-j` 给的是 `{"settings":{"operation_mode":"proxy","proxy_port":40000}}`，     
   字段名是契约的一部分。实现是**先试 `-j`、不认才退回文本**     
   （文本那条路只服务"没有 `-j` 的老版本"）。
2. **`registration show` 的 JSON 是小写 `free`**，文本是 `Free`；     
   归一化必须大小写不敏感（`limited` → `WARP+`，与上游一致）。
3. **这个版本的默认隧道协议是 MASQUE**（`settings` 里     
   `WARP tunnel protocol: MASQUE`），不是 WireGuard —— 官方客户端的 `l`     
   模式与 warpgo 自己那条 WireGuard 隧道是**两条完全不同的路**。
4. **不执行上游的 `tunnel ip add-range 0.0.0.0/0`。** 那个子命令加的是分流列表里的     
   条目，而它意味着"纳入"还是"排除"取决于当时的 `tunnel mode`（`include`/`exclude`）；     
   在本机没有先设 mode 的情况下执行它，结果取决于客户端当时的默认值，     
   **而且没有任何输出能告诉你**。`mode warp` 本身就是"整机流量走 WARP"，     
   不需要它来促成。少一个语义不明、结果不可见的动作，就少一处"配置看着对、行为却不是那个"。
5. **warp 模式会接管默认路由**（`settings` 里默认是 `Exclude mode` 加一组内网网段），     
   远程服务器上**可能切断 SSH**。所以 `--mode warp` 在动手**之前**就明确警告，     
   而默认模式是 `proxy` —— 它只在本机开一个 socks5，一个字节的路由都不动。

#### 钩子（`z` / `x`）与 drop-in

钩子给 `CloudflareWARP` 接口加一组"以接口地址为源"的策略规则：  
表号 **51821**、pref **9100 / 9110 / 9111** —— 与 warpgo 自己那套隧道  
（表 51820、pref 9000 / 9010 / 9011）**完全错开**。上游两套共用表 51820，  
清理时是互相踩的；错开之后 `hook del` 不会碰隧道规则、`warpgo down`  
也不会碰钩子规则（单测 `TestHookPrefsDoNotCollideWithTunnel` 守着这条）。

挂载方式是 **systemd drop-in**  
（`/etc/systemd/system/warp-svc.service.d/warpgo-hooks.conf`），  
而不是像上游那样 `sed` 包自带的单元文件：后者由 dpkg 管，下次 `apt upgrade`  
会**静默覆盖**它，而症状是"重启之后规则又没了"——极难往"包升级"上想。

两条必须记住的约束：

- `ExecStartPost` 那一刻接口**还没建出来**（接口要等客户端连上才出现），    
  所以 `hook add` 会**先等接口**（默认 30s）——"等一会儿"是正常路径，不是异常处理。
- `hook` 的两个子命令**永远以 0 退出**：ExecStartPost / ExecStop 非零退出会让    
  systemd 把整个单元标成 failed，之后 restart 的语义会变得难以推理（M4 记过）。    
  所以那里的错误一律降级成警告。

#### 卸载：只撤我们装的那套

- `client uninstall`（显式）与 `warpgo uninstall`（联动）**走同一份实现**    
  （`clientRemove`），否则两条路径迟早分叉。
- 总卸载带一道额外判断：**没有状态文件就不动客户端**。机器上若本来就装着    
  官方客户端（用户自己装的），我们无权把它删掉 —— 要删请显式执行    
  `warpgo client uninstall`。
- apt 源与密钥环**只删我们建的那几份**（安装时把"新建了哪些"记进状态文件）。
- 卸包用 `apt-get purge`：它只会多删**这个包自己声明的**配置文件    
  （如 `/var/lib/cloudflare-warp` 下的注册信息），不会碰任何不属于它的东西    
  —— 与 `uninstall --purge` 的"只删自己认识的"同一条原则。

### 8.11 BBR 与内核（`b`）

- BBR：写 `/etc/sysctl.d/99-warpgo-bbr.conf` + `sysctl --system`；卸载时可选择还原。
- 内核升级：只做**检查 + 提示 + 建议命令**，不自动 `apt dist-upgrade`（在别人机器上自动升内核风险太大）。

### 8.12 DD 重装系统（`b` 内）—— 默认不编入

见 §14.1。

### 8.13 状态、幂等与卸载

**卸载必须还原的清单**（来自对"旧版卸载后"的真机审计，见附录 A）：

| 项                                    | 现状（旧版卸载后）                                                   | 本设计要求                                                                                                                                                                           |
| ------------------------------------ | ----------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| wireguard 接口 / 地址 / MTU              | 已清干净                                                        | 同                                                                                                                                                                               |
| `ip rule` / `ip route` / table 51820 | 已清干净                                                        | 同，且必须按句柄清（§8.4）                                                                                                                                                                 |
| 生成脚本（NonGlobal / Rule / keepalive）   | 已删                                                          | 本设计不生成脚本                                                                                                                                                                        |
| systemd 单元                           | 已删                                                          | 同                                                                                                                                                                               |
| `/usr/bin/warp`                      | 已删                                                          | `/usr/bin/warpgo`                                                                                                                                                               |
| ipset / dnsmasq 配置                   | 已清                                                          | 同                                                                                                                                                                               |
| **`/etc/resolv.conf`**               | ⚠️ **仍是 `nameserver 1.1.1.1`，且 `resolv.conf.origin` 备份已被删** | ✅ **M4 已实现**：`up --dns` 才改它（默认不碰）；改前备份到 `/etc/warpgo/resolv.conf.origin`，**第一次写入后不再被覆盖**；`down` / `uninstall` 还原并只在还原成功后删备份；`/etc/resolv.conf` 若是符号链接则拒绝改写（systemd-resolved 机器） |
| BBR sysctl                           | 保留（合理，无害）                                                   | 保留但提示                                                                                                                                                                           |
| 调试备份文件（`*.bak-*`）                    | 留在 `/etc/wireguard/`（14 个）                                  | 测试残留由 M0 统一移到 `/root/warp-old-backup/`                                                                                                                                          |

幂等要求：`up` 连跑 3 次规则数不变；`down` 后零残留；`install` 可重复执行；  
`uninstall` 可以连跑两次。

**`--purge` 只删自己认识的文件**：状态目录里一旦出现陌生文件，就整个目录跳过、  
一个都不删，并把清单打出来。理由是 `/etc/warpgo` 的位置可以被环境变量指向任何地方  
（验收脚本就是这么做的），无脑 `RemoveAll` 有可能把用户的目录清空 —— 那是不可逆的。  
默认**不删账户**：它是一个身份，删掉就意味着下次注册会拿到新的 WARP 出口地址。

### 8.14 版本与遥测

- `v` 变成 `warpgo version --check`：查 GitHub releases 提示新版本；`--self-update` 才替换二进制（带校验和）。
- **没有遥测，也没有运行次数统计**（2026-10-15 用户确认砍掉）。    
  上游实现（`statistics_of_run-times()`）在**每次运行**都向 `stat.cloudflare.now.cc/updateStats`    
  发一次请求，且没有开关；它的产物只是菜单里一句"当天运行次数: N，累计运行次数: M"。    
  这两样东西 warpgo 都不做：**不联外网、不写计数文件、不在输出里显示次数**。    
  理由很直接 —— 那个数字对使用者没有用处，而代价是"每次运行都联外网"这条行为本身。    
  唯一会联外网的是显式要求的那条路（`version --check`、`rotate`、`proxy install` 的下载）。    
  单测守着这条边界：`internal/msg` 与 `cmd` 里不允许出现任何与统计/上报相关的 key 或字符串。

### 8.15 文案（只有简体中文）

**只保留简体中文。** 2026-10-15 按用户要求移除英语与语言选择：

- 不再按 `LANG` / `LC_ALL` 推断语言，因此对运行环境的 locale 完全免疫；
- 没有 `--lang`，命令行、日志、systemd 单元、菜单统统中文；
- 包从 `internal/i18n` 改名为 `internal/msg` —— 叫 i18n 却只有一种语言，    
  会让人以为还存在多语言机制，`Bundle.Lang()` 这类接口更是死代码。

保留的是 key 间接层（`internal/msg/keys.go` + `text.go`）：文案集中一处，  
不散落在调用点。两条硬约定：

1. **查不到 key 时返回 key 本身，绝不返回空串**。上游用 nameref 取文案，     
   bash < 4.3 时所有文案变成空串，菜单整片空白却毫无报错 —— 用户只能看到     
   「什么都没显示」。宁可看到 `usage.lock_busy` 这种丑字符串，也不能看到空白。
2. **文案表里每条都必须是中文**，有单测守着（唯一白名单是程序名 `warpgo`）。     
   理由很实际：一条漏改的英文在屏幕上与「缺文案」很难区分，     
   用户只会觉得"这句话怎么怪怪的"，而在单测里拦住是一句话的事。

被移除的东西：`en.go`、`Lang` 类型、`Parse` / `Detect` / `New`、`Bundle`、  
`--lang` / `-L`、`KeyBadLang`，以及 `optionSet.parse` 那个「把 key 变成当前语言  
文案」的回调参数（语言只剩一种后，它就只剩一层空转）。

### 8.16 换 IP（`rotate`）为什么不做流媒体/地区探测

上游 `change_ip()` 是"反复重注册 + 用 Netflix 页面判断地区"，刷到能解锁流媒体的  
IP 为止。**我们只做"换一个出口"，不做解锁判定**，理由有三条：

1. 解锁判定要依赖第三方页面（`URL_ORIGINAL` / `URL_REGIONAL`）的 HTML 结构，     
   对方一改页面就静默失效 —— 而上游是靠 awk 正则去匹配 `"requestCountry"` 的。
2. 它要引 UA 与地区映射表，而那与"拿到 WARP 出口"是两件独立的事。
3. 反复重注册会撞上注册接口的限流（1015），而"刷到满意为止"没有一个自然上界。

留下的是可以精确定义、可以验收的部分：  
**重新注册，直到隧道地址确实变化；换完体检；失败回退。**

`rotate` 会额外测一次"公网看到的出口 IP"并如实汇报是否变化 —— 因为  
账户与隧道地址**一定**会换，而出口 IP 由 Cloudflare 的 PoP 决定  
（同一 PoP 上的免费账户共享出口），**可能不变**。真机上这两种结果都遇到过，  
所以这一步不是锦上添花：不说清楚，用户会以为 rotate 没生效。

---

## 9. CLI 与菜单

子命令（非交互优先，菜单只是它的薄封装）：

```
warpgo menu                                   # 交互菜单（等价旧的裸 warp）—— M8
warpgo register [--force] [--locale zh_CN]
warpgo account [--json]                       # 纯本地读取，不联网
warpgo profile [--out FILE] [--family ...] [--table-off] [--no-dns]
warpgo up [--mode non-global|global] [--backend auto|kernel|userspace]
          [--ifname] [--mtu] [--endpoint] [--dns] [--force] [--no-confirm]
warpgo down [--ifname] [--keep-watch]
warpgo status [--json]
warpgo rotate [--attempts N] [--mode ...] [--backend ...]      # i 换 IP
warpgo switch [--backend ...] [--mode ...]                     # k 内核↔用户态 / g 全局↔非全局
warpgo watch [--once] [--max-rounds N] [--interval 5m] [--mode ...] [--backend ...]
warpgo install [--interval 5m] [--mode ...] [--backend ...] [--no-start]
warpgo uninstall [--purge] [--keep-binary]     # 连 WireProxy 一起撤掉
warpgo confirm
warpgo priority [4|6|d] [--mode ...] [--host ...] [--json]     # s   —— ✅ M6
warpgo proxy install [--port] [--version] [--url] [--sha256] [--from] [--no-start]
warpgo proxy on|off|status [--json]                            # w / y —— ✅ M7
warpgo proxy uninstall [--keep-binary]
warpgo client install [--mode proxy|warp] [--port] [--no-start] [--no-hooks]   # c / l —— ✅ M8
warpgo client mode proxy|warp [--port]                         # —— ✅ M8
warpgo client on|off|status [--json]                           # r   —— ✅ M8
warpgo client uninstall [--keep-repo]                          # —— ✅ M8
warpgo hook add [--wait 30s] | hook del                        # z / x（隐藏）—— ✅ M8
warpgo menu                                                    # 交互菜单 —— ✅ M8
warpgo bbr enable|disable          # b   —— M9
warpgo version [--check] [--self-update]                —— M9
```

> 原计划里的 `warpgo split …`（`e` 分流）**已砍掉**，见 §3。

内部子命令（不进 help、不由用户调用）：  
`rollback-guard`（由 `up` 派生的回滚保险丝）、  
`userspace-daemon`（由 `up`/`watch` 派生的用户态后端守护进程）、  
`hook add|del`（由 warp-svc 的 ExecStartPost / ExecStop 调用 —— 菜单 `z` / `x`）。  
后两者都必须**永远以 0 退出**：ExecStartPost / ExecStop 非零退出会把整个单元标成  
failed，之后 restart 的语义会变得难以推理。

**`--backend` 的取值**：`auto`（默认，见 §8.2 的三条规则）/ `kernel` / `userspace`。  
`reserved` 是 `userspace` 的别名 —— 在 WARP 语境里"带 reserved 的那个后端"  
就是用户态实现，菜单 `k` 与生态里的叫法都它。

**`priority`（栈优先级，菜单 `s`）**：既接受位置参数（`priority 4`，与菜单一致）  
也接受 `--mode 4`（脚本友好）；两种写法**同时**出现时报错，不让后者悄悄盖掉前者。  
无参数时打印当前状态 + 用 `getent` 实测到的解析顺序。细节见 §8.8。

**`proxy`（WireProxy，菜单 `w`/`y`）**：`install` 会真的下载并校验第三方二进制，  
`on`/`off` 是开关，`status` 报状态并**经 socks5 实测**一次。细节见 §8.9。

**`rotate`（换 IP）**：重新注册一台设备，直到拿到与当前**不同的隧道地址**  
（判据是"至少一族的地址变了"，因为单栈账户只有一族），然后用新账户重建隧道、  
体检通过才写盘。任何一步失败都会**回退到原账户并把隧道建回来** ——  
用户的隧道可能就是他唯一的对外通道。  
成功后顺带把旧设备从服务端注销（否则每换一次就在账户里留一个孤儿设备）。  
它会额外报告"公网看到的出口 IP 有没有变"：账户与隧道地址一定会换，  
但出口 IP 由 Cloudflare 的 PoP 决定（同一 PoP 上的免费账户共享出口），  
所以**可能不变** —— 用户真正在意的是后者，必须如实说清楚。

退出码：`0` 成功 / `1` 通用错误 / `2` 体检不健康 / `3` 需要 reserved 但后端不支持 /  
`4` 被单实例锁挡住 / `5` 非 Debian 系统。

---

## 10. systemd

由 `warpgo install` 生成，实际内容如下（省略了注释行）：

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

五个刻意的选择：

1. **路径一律写绝对路径**（`/usr/bin/warpgo`）。单元里的相对路径会随 systemd 的     
   工作目录变化，而那个目录不是我们能控制的。
2. **不写 `--lang`**。曾经要在这里按安装者的语言把日志语言钉住（systemd 没有登录     
   会话，`LANG` 是空的）；只保留简体中文之后这一条自然消失。
3. **刻意不写 `ExecStop`**。ExecStop 非零退出会让 systemd 把整个单元标成 failed，     
   而且会让 restart 的语义变得难以推理 —— 上游正是在这里翻过车。     
   "清理现场"是 `warpgo down` / `warpgo uninstall` 的职责，不是停止服务的职责。
4. **`watch` 自身幂等**（健康时什么都不做），所以 restart 不会造成出口 IP 抖动。     
   这一点是 `Restart=always` 敢用的前提。
5. **`down` 会先停这个服务**，然后 `reset-failed`。前者是因为不停它就会在下一轮     
   把隧道建回来（用户会以为 `down` 没生效）；后者是因为单元一旦被标成 failed，     
   `restart` 会退化成"只重启不重载"，后续行为难以推理。

---


## 11. 里程碑与验收

| #      | 内容                                              | 验收（真机 Debian 12）                                                                        |
| ------ | ----------------------------------------------- | --------------------------------------------------------------------------------------- |
| **M0** | 骨架 + 状态/锁/文案/日志 + 把调试残留移走                       | `warpgo version` 能跑；单实例锁三种行为（占用提示 / 心跳 / 接管）单测过                                         |
| **M1** | `cloudflare` + `wgconf`：注册、reserved、profile     | ✅ **完成**（2026-10-14）：真机验收 **39/39**，见下方                                                 |
| **M2** | `tunnel`(内核) + `netcfg` 非全局 + `up/down/status`  | ✅ **完成**（2026-10-14）：真机 **38/38** —— 见下方（菜单骨架未做，挪到功能齐了之后单独做）                            |
| **M3** | 全局模式 + fwmark 防环路 + watchdog 回滚                 | ✅ **完成**（2026-10-14）：真机 **32/32** —— 见下方                                                |
| **M4** | `health` + `keepalive` + systemd + **卸载**       | ✅ **完成**（2026-10-14）：真机 **70/70** —— 见下方                                                |
| **M5** | 用户态后端 + reserved + `rotate` + `switch`          | ✅ **完成**（2026-10-15）：真机 **73/73** —— 见下方。<br />（账户 / license / 设备管理按用户决定**砍掉**，不在交付范围内） |
| **M6** | `s` 栈优先级（`e` 分流已砍）                              | ✅ **完成**（2026-10-15）：真机 **30/30** —— 见下方                                                |
| **M7** | `w` / `y` WireProxy                             | ✅ **完成**（2026-10-15）：真机 **84/84** —— 见下方                                                |
| **M8** | `c` / `l` / `r` 官方 Client + `z` / `x` 钩子 + 交互菜单 | ✅ **完成**（2026-10-15）：真机 **76/76** —— 见下方                                                |
| **M9** | `b` BBR + 版本/自更新 + 打包                           | BBR 开关与还原正确；静态二进制可装可卸；`--check` 能查到新版本                                                  |

**M8 交付**（2026-10-15，真机 Debian 12，`warp-cli 2026.8.2110.0`）：

- `internal/cfclient`：`client.go`（warp-cli 包装 + 状态聚合）、`install.go`    
  （apt 源 / 公钥 / 装包 / 卸载）、`mode.go`（proxy / warp 模式）、`hooks.go`    
  （策略规则加减 + systemd drop-in）。设计取舍见 §8.10。
- `cmd/warpgo`：`client.go`（install / mode / on / off / status / uninstall）、    
  `hook.go`（`hook add|del`，隐藏）、`menu.go`（交互菜单）。
- `netcfg` 新增 `EnsureDefaultRouteDev`（按**接口名**加默认路由）——    
  接口不是我们建的（`CloudflareWARP`），所以不能只接受 `netlink.Link`。
- `warpgo uninstall` 联动撤销客户端；`client.json` 进 `--purge` 白名单。
- **真机 76/76 通过**：真实 apt 装包 + 真实注册 + 独立 `curl --proxy socks5h://`    
  拿到 `warp=on`（JP）+ off/on 往返 + 零残留（包 / 源 / 密钥环 / 状态 / 规则 / 端口）
  - 与 `warpgo uninstall` 的联动 + 菜单四条。
- **钩子用假接口（dummy `CloudflareWARP`）验**：加规则 → 幂等（连跑 3 次仍是 4 条）    
  → 撤干净 → 接口删掉后仍能按 pref 清干净；同时断言隧道自己的 pref 9000 规则    
  **一个都没被动过**。这样"最危险的那一步"（warp 模式接管默认路由）不会在一次    
  例行验收里被无意触发。
- **warp 模式的端到端留成可选**（`M8_TEST_WARP=1`）：它会把整机流量交给客户端，    
  在一台只能靠 SSH 进入的机器上有断连风险 —— 详见 §8.10 第 5 条。

**M7 交付**（2026-10-15，真机 Debian 12）：

- `internal/proxy`：`config.go`（配置生成与校验）、`download.go`（下载 / 校验和 / 解包 /    
  安装）、`proxy.go`（路径、单元渲染、systemctl、状态文件、撤销）。    
  设计取舍见 §8.9，其中三条是真机跑出来才算数：    
  **发布仓库是 `windtf/wireproxy`**（上游 `pufferffish` 已迁移）、    
  **`wireproxy -n -c` 可以当配置自检用**（白送的确定性）、    
  **reserved 在 WireProxy 模式下发不出去**（本机账户 reserved = `99,70,83`，    
  而这个端点不要求它 —— 实测能通；所以警告是条件触发的，不是拦路虎）。
- `health.Options.Proxy`：探测支持经 socks5 出去（`socks5h://…`），    
  与 `Options.Interface`（绑接口）并列 —— 两种"经 WARP 出去"的形态现在都能验。
- `systemd.Manager` 增加 `Name` 字段：一套 systemctl 动作管两个单元    
  （保活 + WireProxy），不必再抄一遍。
- `warpgo uninstall` 会一并撤掉 WireProxy；`proxy.conf` / `proxy.json` 进 `--purge` 白名单。
- **真机 84/84 通过**（详见 §12）：真实下载 + 校验和通过 + `wireproxy -n` 通过 +    
  单元 enabled/active + **独立 curl 经 socks5 拿到 `warp=on`（JP）** + 只监听 127.0.0.1 +    
  改端口后旧端口释放 + `off`/`on` 往返 + 撤销后零残留 + 与 `warpgo uninstall` 的联动 +    
  离线 `--from` 安装。

**M6 交付**（2026-10-15，真机 Debian 12）：

- `internal/gai`：只增删 `/etc/gai.conf` 里受管的两行，其余逐字节保留；    
  `ModeDefault` = 移除那两行，于是"还原"不需要备份文件、且 `Set` 天然幂等。
- `cmd/warpgo/priority.go`：`priority [4|6|d]`；无参数时读文件 + 用 **`getent ahosts`**    
  实测解析顺序（Go 的解析器不读 gai.conf，用它测会得出错误结论）。    
  位置参数与 `--mode` 两种写法都接受，**同时出现则报错**（不让后者悄悄盖掉前者）。
- §8.8 那三条实测结论是这一轮最重要的产出：`4` 是真开关，`6` 与默认等价，    
  `d` 可逐字节还原。上游把 `s` 做成对称的三选一，但在这台机器上它其实只有两种状态。
- **真机 30/30 通过**：三轮切换后的 sha256 与进入时逐字节相同；`getent` 的第一条    
  确实随设置改变；注释掉的原厂行没被动过；重复设置报告"未改动"；非法取值与    
  冲突参数都被拒绝。

**M5 交付**（2026-10-15，真机 Debian 12）：

- `internal/tunnel` 重新分层：`common.go` 收走两种后端**真正共享**的部分    
  （加地址 / 起接口 / 下发配置 / 读状态，全部显式接收 `*wgctrl.Client`），    
  `kernel.go` 只剩"建接口、删接口、读状态"，用户态后端因此没有复制一行业务逻辑。    
  这是"同一套实现对两种后端"的落地方式。
- `internal/tunnel/reserved.go`：`ReservedBind` —— 出网报文写 reserved、    
  **入网报文抹掉类型字段的高 3 字节**（见 §8.2，后者是真机抓包才发现的必需项）。    
  零 fork：不 vendor 打过补丁的 wireguard-go。
- `internal/tunnel/userspace.go` + `daemon.go`：用户态后端。    
  `userspace-daemon` 是独立进程（持有 TUN、监听 UAPI socket），    
  父进程用 `wgctrl` 通过 socket 配置它 —— 于是 `up`/`status`/`down` 的代码不变。    
  守护进程把 MTU 与 reserved 记在 `/run/warpgo/userspace-<if>.json` 里，    
  参数变了就换一个进程（`rotate` 换了账户必然触发这一条）。
- 后端选择（`resolveBackends`）：用户显式指定 → 单候选不回落；`reserved` 全零 → 内核；    
  否则**优先沿用现在跑着的那个后端**，另一个兜底（auto 是"自适应"，不是"永远先试内核"。    
  见 §8.2 —— 固定顺序会让一台只能用用户态的机器每修一次链路都白白拆建两遍）。    
  再加一条"换后端前必须先验证"：`up`/`switch` 会验（`ProbeAfterEnsure`），    
  `watch` 不验（它下一轮本来就会体检，当场再探一次会把"我修好了"误报成"修不好"）。
- `cmd/warpgo/rotate.go`：换 IP。重新注册到"隧道地址确实不同"为止，    
  用新账户重建并体检通过才写盘，**任何失败都回退到原账户并把隧道建回来**；    
  成功后把旧设备从服务端注销；如实汇报公网出口 IP 有没有变。
- `cmd/warpgo/switch.go`：在线互换后端（`k`）与工作模式（`g`）。    
  若保活单元已安装，会**同步改写它的启动参数并重启** —— 否则它下一轮就会    
  按旧参数把隧道重建回去，用户看到的是"切了没用"。
- 账户 / license / 设备管理**砍掉**：删掉 `SetLicense` / `UnbindDevice` /    
  `RenameDevice` / `GetDevice` 与 `Account.ApplyDevice`（各带一条单测守着"别再长回来"），    
  `warpgo account` 退化成**纯本地只读**视图（`--refresh` / `--enable` / `--license` 一并移除）。    
  唯一保留的写动作是 `rotate` 内部的 `DeleteDevice` —— 不给自己的换 IP 留孤儿设备。
- **真机 73/73 通过**（详见 §12）：用户态后端双栈 `warp=on`；抓包证明出网/入网报文的    
  类型字段都带着 reserved、而内核后端 4 个报文全是 `xx 00 00 00`；`up` 幂等不重建守护进程；    
  `switch` 往返；`auto` 先选内核并实测验证；端点不通时确实会换后端再试；    
  `rotate` 换了地址且旧设备被注销；两种后端 `down` 后零残留。

**修掉的一个既有缺陷（M5 真机验收逼出来的）**：自动换后端让一次 `up` 会先后生出  
**两个**保险丝，于是"M3 里用解除标记让旧保险丝退出"这条路露了馅 ——  
确认标记是一个**被消费**的文件（谁先看到谁删），两个保险丝共用时必然有一个漏看，  
漏看的那个会在超时后拆掉**后来建立的隧道**。修法不是在标记上再加锁，而是让保险丝  
在拉闸前先自问一句"我守护的那次变更还在不在"（比对快照的 `CreatedAt`）：  
不在就不动手。见 `rollback.stillPending`。

**M4 交付**（2026-10-14，真机 Debian 12）：

- `internal/keepalive`：把"健康就不动、断了才重建"做成**依赖全部可注入**的循环    
  （体检 / 重建 / 加锁 / 休眠都是回调），于是四条分支（健康、断链、被锁挡住、    
  重建失败）都能在不碰真实网络的前提下单测。库层只发事件、不拼文案。
- `internal/systemd`：单元渲染是纯函数；`Manager` 的 `systemctl` 执行器与安装路径    
  都可注入，因此"装/卸"流程能在单测里完整跑一遍。
- `internal/dns`：`/etc/resolv.conf` 的**改前备份、用后还原**。备份写在状态目录里，    
  第一次写入后不再被覆盖（先到的那个才是原始文件）。
- `cmd/warpgo`：`watch`（含 `--once` / `--max-rounds`）、`install`、`uninstall`；    
  `up` 新增 `--dns`，`down` 会**先停保活**再拆隧道。
- 在 `up` 与 `watch` 之间抽出共用的 `ensureTunnel` —— 两条路径必须走**完全一样**的代码，    
  否则"手工执行能修好、保活却修不好"这类偏差迟早会出现。
- 真机 70/70 通过（详见 §12）：健康时连跑 5 次接口创建时间不变、删掉接口一轮内自愈、    
  坏端点触发硬复位、被锁挡住如实报告、旧格式锁接管且不误杀、install/uninstall 全链路、    
  DNS 还原（sha256 一致）、`--purge` 拒绝删除陌生文件。
- 单测累计 **116 条**全通过；`gofmt` / `go vet` 干净；M0–M3 全部回归通过。

**M3 交付**（2026-10-14，真机 Debian 12）：

- `internal/health`：只读探测（`SO_BINDTODEVICE` 绑接口 / 不绑），只用 IP 字面量、零 DNS。    
  M4 的保活会直接复用它。
- `internal/netcfg`：`Rule` 支持 fwmark / not-fwmark；全局模式的规则为    
  pref 9000（内网/直连优先）+ pref 9020（`not fwmark 0xca6c` → WARP）。
- `internal/rollback` + `rollback-guard` / `confirm`：快照 → 独立保险丝进程 →    
  超时无条件回滚。库层只发事件、不拼文案（文案属 CLI 层）。
- `up --mode global`：快照 → 派生保险丝 → 应用 → 自检 → 确认；    
  自检失败**立即回滚**，另加 `--no-confirm` / `--guard-timeout`。
- 真机 32/32 通过（详见 §12）：全局生效（不绑接口也 `warp=on`）、SSH 不断、    
  坏端点 16 秒内主动回滚、`--no-confirm` 下保险丝 25 秒超时自动回滚并恢复网络。
- 单测累计 **90 条**全通过；`gofmt` / `go vet` 干净；M0/M1/M2 全部回归通过。

**M2 交付**（2026-10-14，真机 Debian 12）：

- `internal/tunnel`：`Tunnel` 抽象 + 内核后端。**接口复用而非重建** ——    
  `up` 先比对当前 WireGuard 配置，一致就一行都不改（连重下发都省了），    
  于是 `up` 连跑多次也不会换 IP、不会触发无谓的重握手。
- `internal/netcfg`：把「期望状态」做成纯数据的 `Plan`（可脱离 root 单测），    
  实际下发走 netlink；规则与路由一律**先清后加**，删除**只按 pref / 表号**。
- CLI：`up [--mode] [--ifname] [--mtu] [--endpoint] [--force]` / `down` / `status [--json]`；    
  运行态写 `/etc/warpgo/runtime.json`，`down` 靠它知道该拆哪个接口，但不依赖它能否读 ——    
  即使运行态损坏也能把现场清干净。
- 真机 38/38 通过（详见 §12）：双栈经隧道 `warp=on`、不绑定接口时 `warp=off`、    
  `up` 连跑 3 次接口创建时间与规则数都不变、`down` 后连系统规则都原封不动。
- 单测累计 **85 条**全通过；`gofmt` / `go vet` 干净。

**M1 交付**（2026-10-14，真机 Debian 12）：

- `internal/cloudflare`：`Register` / `EnableWarp` / `GetDevice` / `RenameDevice` /    
  `SetLicense` / `UnbindDevice` / `DeleteDevice`；`reserved` 按 base64 原始字节解；    
  `account --json` 输出脱敏视图（不含私钥与令牌）。**不做 Teams OAuth。**    
  （M5 已按用户决定砍掉其中四个：只剩 `Register` / `EnableWarp` / `DeleteDevice`。）
- `internal/wgconf`：用 `crypto/ecdh` 生成已 clamp 的 X25519 密钥对（公钥与 RFC 7748    
  向量逐字节一致），profile 渲染是确定性输出。
- 真机验收链路：注册 → 账户 0600 → **重复注册被拒** → profile 0600 → `wg-quick up` →    
  **v4 与 v6 均经隧道取到 `warp=on`** → `down` 后零残留 → `resolv.conf` 前后一致。
- 单测 **64 条全通过**；`gofmt` / `go vet` 干净。

**排序理由**：主干（自己实现的接口与出口）先行 —— 分流 / 代理 / 官方客户端都依赖它；  
危险与边缘功能放最后。

---

## 12. 测试策略

### 单元（无 root、无网络）

reserved 解码（`y8eh` / `4A3x` / 非法输入）、UAPI 序列化（字节级，含结尾空行）、  
profile 生成、trace 解析（`on`/`plus`/`off`/空/半截）、路由规则生成（两模式、"先清后加"的顺序）。

M5 追加：`applyReserved` 的字节级断言（四种报文类型各一条，加"长度不足""未知类型"  
两条不该动的）、`ReservedBind` 的**收发两个方向**（出网写 reserved、入网抹掉类型高位、  
`SetMark` 等必须原样透传、接收错误必须上抛、`sizes` 里夹 0 的槽位不能碰）、  
`ParseBackend` / `resolveBackends` 的候选顺序、`addressesDiffer` 的判据、  
`ParseReservedArg` 命令行往返、以及 `rollback.stillPending`（保险丝该不该拉闸）。

M6 追加（`internal/gai`）：`DetectContent` 的七种输入（含"多余空格""行尾注释"  
"被注释掉的写法"）、`Rewrite` 的逐字节保真（非受管行一个都不许动）、  
"原文件没有结尾换行"时的行为、`Set` 的幂等与权限保留、以及 **"注释行不是受管行"**  
—— 原厂文件里本来就有一行被注释的 v4 写法，用 `strings.Contains` 做断言会把它  
当成生效的配置（这一条是我自己先写错了测试才发现的）。

M7 追加（`internal/proxy`）：`Render` 的**逐字节**固定（含 socks5 必须绑回环）、  
`Validate` 的九种缺项、`ParsePort` 与 `Port()` 的三级回退、  
`assetName` 的平台映射（含四个"没有发布产物"的平台）、`lookupSum` 的两种清单写法、  
`normalizeSHA`、tar.gz / 裸二进制 / 单文件归档 / 多文件归档（拒绝）/  
带 `../` 的条目名（只能落到目标路径）、以及用 `httptest` 假服务器跑的  
**校验和正确 / 校验和不符 / 取不到清单 / `--url` 缺 `--sha256`** 四条下载路径。

M8 追加（`internal/cfclient`）：`connStateFromWord` 的多种输入（含
**"Disconnected 里含 connected"** 这个顺序陷阱、以及 `-j` 的驼峰
`RegistrationMissing`）、`modeFromWord` 的 **"WarpProxy 里含 Warp"**、
`ParseProxyPort` 的两种真实形态（老版 `Proxy port: 40000`、
新版 `(user set)\tMode: WarpProxy on port 40000`）、`-j` 三条 JSON 解析、
`CodenameFrom` 的四种 os-release 形态（含新发行版代号回退）、
`HookRules` 的双栈 / 单栈 / 同族去重、`HookFamilies` 的顺序、
`HookRoutes` 的表号与设备、`RenderDropIn` 的两个 Exec 行与"只用 drop-in"、
`hookManagedRules` 覆盖两族，以及 **`TestHookPrefsDoNotCollideWithTunnel`**
—— 钩子的表号与 pref 必须与 warpgo 自己的隧道**完全错开**
（撞号会让两边互相删掉对方的规则，而症状是"有时通有时不通"，极难排查）。

### 假实现（`tests/fake/`）

- **假 netlink**：断言 ① `up` 三次后规则各 1 条 ② `down` 后完全空    
  ③ 模拟"接口已删 + `[detached]` 显示形态"时仍按句柄清干净。
- **假 UAPI socket**：断言握手序列、`reserved` 确实下发到用户态后端。    
  （M5 之后 reserved 不再走 UAPI，而是在 `conn.Bind` 上收发出网入网报文 ——    
  对应的断言改成了"假 Bind"的形式，见上。）
- **假发布页**（M7）：`httptest` 提供 `checksums.txt` 与 `.tar.gz`，覆盖下载路径的    
  成功与三种失败；真机验收那一条则走真实的 GitHub。

### M8 真机实测（2026-10-15）

真机 **76/76** 通过（`tests/e2e/m8-client.sh`）。这个脚本会**真的装  
`cloudflare-warp`**（加 apt 源 + 导公钥 + apt 装包）、真的注册一台设备、  
真的经 socks5 发请求，靠 trap + 死人开关保证收尾。

**这份脚本最重要的设计约束是"安全边界"**：真实客户端只跑 **proxy 模式**  
（不动任何路由），而钩子用一个 **dummy 的 `CloudflareWARP` 接口**来验 ——  
它同样能产生接口地址、同样要读写策略规则，但不会有任何流量真的走进去。  
这样"最危险的那一步"（warp 模式接管默认路由、可能切断 SSH）不会在一次例行  
验收里被无意触发；要跑它得显式设 `M8_TEST_WARP=1`。

验到的关键点：

- 装包后 `warp-svc` 自动 enabled + active；`warp-cli` 的 AF_UNIX socket    
  （`/run/cloudflare-warp/warp_service`）就绪 —— **等的是 socket 而不是    
  `systemctl is-active`**，因为进程活着但 socket 没建好时任何 warp-cli 调用都会失败。
- **连通性断言都另起一条 `curl --proxy socks5h://…`**，不看被测程序自己的结论    
  （与 M7 同一条原则）：拿到 `ip=104.28.211.105 loc=JP warp=on`。
- 钩子：加 4 条规则（两族各 2）→ 连跑 3 次仍是 4 条（先清后加）→    
  `hook del` 后规则与表 51821 全空 → **把 dummy 接口删掉后仍能按 pref 清干净**    
  （不解析显示文本）。全程隧道自己的 pref 9000 规则**一个都没被动过**。
- 接口不存在时 `hook add` 返回 **0**（附警告），因为它是被 `ExecStartPost` 调的    
  —— 非零退出会把 warp-svc 标成 failed。
- 卸载零残留：包、apt 源、密钥环、状态文件、规则、端口全部清掉，隧道不受影响。
- `warpgo uninstall` 会联动撤销客户端；**没有状态文件时不动它**    
  （机器上本来就装着官方客户端的话，我们无权删）。

### M7 真机实测（2026-10-15）

真机 **84/84** 通过（`tests/e2e/m7-proxy.sh`）。这个脚本会**真的从 GitHub 下载 4MB 的  
第三方二进制**、真的装 systemd 单元、真的起进程，靠 trap + 死人开关保证收尾  
（进入时先撤销已有的安装，于是每次运行都从同一个起点开始）。

**怎么判"这个代理到底通不通"：另起一条 `curl --proxy socks5h://…`，不看程序自己的结论。**

程序自己也会经 socks5 探一次（那是它的自检），但验收不能拿被测代码的结论当证据：  
脚本里每条连通性断言都是一条独立的 curl，直接读 trace 里的 `warp=` 字段。  
"端口在听"与"链路真的通"是两件不同的事 —— 这一点在隧道模式（M2/M4）里已经吃过亏。

顺带验到的两件事：

- `ss -nltp` 里那一行必须是 `127.0.0.1:40000`。**断言写法有两个坑**：`ss` 的输出    
  前面还有 `LISTEN / 队列 / 收发包计数`，所以模式两端都要通配（只写 `127.0.0.1:PORT*`    
  永远匹配不上）；而 `pgrep -c` 在"一个都没找到"时会**打印 0 但以 1 退出**，    
  写成 `pgrep -c ... || echo 0` 会拼出 `"0\n0"`，比较必然失败（这个坑让第一轮的    
  断言误报了两处，报出来的现象是"还有 0 个 wireproxy 进程"）。
- `wireproxy -n -c <conf>` 真的能校验我们的配置文件（包括 `AllowedIPs` 与    
  逗号加空格的 `DNS` 列表写法）—— 这一条从"我以为格式对"变成了"工具认这份格式"。

**收尾必须把隧道拉回来**：脚本里有一节专门跑 `warpgo uninstall`（验证它会一并  
撤掉 WireProxy），而 `uninstall` 本来就会拆掉隧道 —— 所以脚本结尾会重新  
`warpgo up` 并确认 `warp=on`，避免"验收把机器的网络弄没了"。

### M6 真机实测（2026-10-15）

真机 **30/30** 通过（`tests/e2e/m6-priority.sh`）。这个脚本会真的改 `/etc/gai.conf`，  
并在结束时还原成进入时的样子（sha256 逐字节核对）。

**怎么判"优先级到底生效没有"：`getent ahosts`，不能看文件里有没有那行。**

`getent` 走的是 glibc 的 `getaddrinfo`，也就是用户真正会遇到的那条路；  
而 **Go 自己的解析器不读 gai.conf**，用它去测这件事会得出"gai.conf 无效"的错误结论。  
只看文件内容的断言更糟：它会让一个"什么都没做"的实现在 `label 2002::/16 2`  
这一项上拿到绿灯（而那行实测毫无作用，见 §8.8）。

因此脚本里每一轮设置都同时验两件事：文件里受管行的**条数与形态**、  
以及 **`getent` 第一条的地址族**。

### M5 真机实测（2026-10-15）

真机 **73/73** 通过（`tests/e2e/m5-userspace.sh`）。这个脚本会**真的在两套后端之间  
来回切换**、**真的重新注册一个新账户**（所以这台机器的 WARP 出口地址会变、  
旧设备会被从服务端注销），靠 trap + 死人开关保证收尾可用。

**怎么判"reserved 到底有没有生效"：抓包，而不是看握手成不成功。**

这一条是整轮里最有价值的方法论。第一版实现只做了发送方向，结果现象是  
"包发出去了、服务端也回了（抓包看得见 92 字节的响应）、握手永远完不成、  
而且**没有任何报错**"。只有把 `tcpdump -X` 的十六进制摊开才看出：  
服务端的响应是 `02 24 D4 90`，而 WireGuard 按 4 字节小端读类型会得到  
`0x90D42402 ≠ 2`，于是被当成未知报文丢掉。补上接收方向的抹字节之后立刻通了。

抓包的三个细节（都踩过）：

- 类型字段在帧偏移 `0x0010` 那一行 —— `grep -A1` 只会拿到 `0x0000:`（IP 头），    
  必须 `-A2`；用错了的表现是"没抓到"，很难往"取错了行"上想。
- 那一行末尾还有一列 ASCII 回显，里面可能有空格，所以 `awk '{print $NF}'` 会飘；    
  十六进制部分固定 39 个字符，先 `cut -c1-39` 再取第 7、8 组。
- 后台起 tcpdump 必须在**本 shell** 里起：用 `$(...)` 起的话它是子 shell 的子进程，    
  主 shell 的 `wait` 等不到它，于是读到一份空结果。

断言（两条方向都要，才排除巧合）：

- 用户态后端：出网方向出现 `01 + reserved`，且**该方向全部报文**的类型字段都带着    
  同一组字节（含 `04` 数据报文）；入网方向出现 `02 + reserved`。
- 内核后端：出网方向出现 `0100 0000`，且**全部报文**都是 `xx 00 00 00` ——    
  这才叫"内核结构上带不了那 3 个字节"。

其它：

- 用户态后端的接口类型是 `tun type tun`（`ip -d link show`），不是内核 `wireguard`；    
  UAPI socket 在 `/var/run/wireguard/warp.sock`；守护进程状态文件在 `/run/warpgo/`。
- **幂等**：连续两次 `up --backend userspace`，接口创建时间与守护进程 PID 都不变。
- `switch --backend kernel` 之后守护进程、状态文件、UAPI socket 全部消失。
- `auto` 先选内核并实测验证通过（运行态 `backend=kernel`）；    
  给一个不通的端点（`--endpoint x:9999`）时日志依次出现    
  "换一个后端再试" → "所有可用后端都没能让隧道真正通起来"，退出码 2。    
  之后再 `up` 会**沿用**已经落到用户态的那个后端（不重复拆建），    
  换回内核走 `switch --backend kernel`。
- `rotate` 换了设备与隧道地址，旧设备被注销；**reserved 从 `165,254,0` 变成    
  `22,247,116` 之后守护进程跟着换了**（这正是"参数变了就换一个进程"那条路径）。    
  它还会如实汇报公网出口 IP：真机上两种结果都遇到过    
  （一次 `104.28.243.105 → 104.28.211.105` 变了，一次没变）。
- **零残留**（两种后端各测一遍）：`down` 后接口、策略规则、表 51820、运行态、    
  守护进程、状态文件、UAPI socket 全部消失，系统本身仍能上外网。
- 顺带把更早的里程碑全部回归了一遍：M0 **16/16**、M1 **33/33**、M2 **38/38**、    
  M3 **34/34**（修完保险丝那处缺陷之后）、M4 **69/69**。    
  （M4 比它当年的 70 少一项是**刻意的**：那个用例写的是"up 退出码非 0 就只记一条    
  note"，而黑洞端点下 `up` 现在会如实返回 2。其余断言全过。）

### M4 真机实测（2026-10-14）

真机 **70/70** 通过。这个脚本会**真的装一个 systemd 服务**、**真的改写  
`/etc/resolv.conf`** —— 那正是它要验证的东西，所以它靠三重兜底保证收尾干净  
（脚本自己的 trap、每一步记录"改之前是什么"、调用方挂的死人开关）。

- **健康时连跑 5 次，接口创建时间与规则数完全不变**；再走一遍真实的循环    
  （`--max-rounds 2 --interval 10s`）也一样。判据仍然是 `/sys/class/net/warp` 的 mtime。
- **`ip link del warp` 之后一轮内自愈**：日志依次是"接口 warp 不存在 → 正在重建 →    
  已重建"，接口创建时间变化、规则数回到 5（没有累积）。
- **坏端点（TEST-NET-1）触发硬复位**：`up --endpoint 192.0.2.1:2408` 让接口在、    
  配置一致、链路不通，`watch --once` 识别出这一点并重建接口。    
  两个地址族各超时 8 秒，所以这一轮约 16 秒 —— 日志里能清楚看到是哪两族失败。
- **被锁挡住时如实报告**：退出码 2、汇总里记 `跳过 1`、接口一动不动。    
  （这条断言最初写的是"日志里要出现『锁』这个字"，而程序当时输出英文，    
  用的是 "another warpgo instance…" —— 又一次栽在断言依赖措辞上。当时加了    
  "把脚本 `LANG` 钉死 + 断言改成结构性事实"两层补救；**语言机制现已整体移除，    
  这类问题从根上不会再有了**。）
- **旧格式遗留锁被接管，无辜进程未被误杀**：假锁里放的是 `sleep 300` 的 PID，    
  日志显示"无法确认身份，只接管不杀"。
- **install / uninstall 全链路**：单元写入 `/etc/systemd/system/warpgo-watch.service`    
  （0644、`Restart=always`、**刻意没有 `ExecStop`**）、二进制装到 `/usr/bin/warpgo`、    
  开机自启生效、服务真的在跑（journal 里有巡检记录）。
- **`down` 会先停保活**：不然它下一轮会把隧道建回来，用户看到的是"拆了又自己回来"，    
  而且会怀疑 `down` 根本没生效。停完之后**开机自启仍然保留**（停 ≠ 禁用）。
- **DNS 全流程**：改之前没有备份；`up --dns 1.1.1.1,1.0.0.1` 之后原文被存到    
  `/etc/warpgo/resolv.conf.origin`、`/etc/resolv.conf` 被改写、接口没有被重建；    
  `uninstall` 之后 resolv.conf 的 **sha256 与最初完全一致**、备份被清掉。    
  这正是旧版丢掉的那一环 —— 它卸载后留下了 `nameserver 1.1.1.1` 而备份已删。
- **`--purge` 的安全性**：目录里只有自己的文件时清空；一旦出现陌生文件就**整目录跳过**，    
  一个都不删，并把清单打出来。

### M3 真机实测（2026-10-14）

真机 **32/32** 通过。这个脚本会**故意让机器失去网络若干秒** —— 那正是它要验证的场景。

- 全局模式生效的判据是「**不绑定接口**也拿到 `warp=on`」：这与 M2 的非全局正好反向    
  （那边不绑接口必须 `warp=off`）。两条合起来才说明模式真的切换了。
- `not fwmark 0xca6c` 要**两处同时正确**才有效：规则里的 flags 与 fwmark 属性、    
  以及接口侧的 `fwmark = 0xca6c`。少一处就会变成「所有流量（含隧道自己的封装包）    
  都走 WARP」，表现是握手永远不成功、整机断网。单测对这两处都有字节级断言。
- 全局模式**不改默认路由**（靠策略规则接管），自检里专门有一条在验证这件事 ——    
  万一哪天实现走偏成「改默认路由」，它会在放行前就红。
- 坏端点（TEST-NET-1）下的实测：自检 16 秒判定失败并**主动回滚**，    
  远早于 120 秒的保险丝超时；网络随之恢复。
- `--no-confirm` 场景（模拟「主流程没能确认」）：保险丝在 25 秒后**自动回滚**，    
  网络恢复，`/run/warpgo/guard.log` 留下完整回滚记录。这一条才是保险丝存在的理由。
- 脚本自己踩的坑记一笔：断言不能只 grep 中文（比如「自检」）——    
  当时被测程序的输出语言取决于 locale。**语言机制现已移除（只保留简体中文），    
  这类问题从根上消失了**；验收脚本里「中英都认」的正则也已清理成只认中文。

### M2 真机实测（2026-10-14）

真机 **38/38** 通过。几条值得记下来的结论：

- **第三方 netlink 库的 `RuleAdd` 在本机内核（6.18.55-x86-64-xanmod）上一律返回 EINVAL** ——    
  连 `{Family: V4, Priority: 9010}` 这种最简输入都失败。而 ① `ip rule add`、    
  ② 手工构造的 netlink 消息 都能成功；同一个库的 `RuleList`（读）也是好的。    
  规则是"幂等与零残留"的唯一依据，所以这部分自己实现（`internal/netcfg/rulenl.go`，    
  约 200 行），接口 / 地址 / 路由继续用成熟库。
- **绝不要写值为 0 的 `FRA_TABLE` 属性**。内核把 table=0 解释成"未指定"，    
  会让规则落到一个与预期无关的表上（实测变成 table 3）。上面那个库的判断是    
  `rule.Table >= 0`（int 零值恒真），所以它**总会**写这个属性。    
  另外库对 `SuppressPrefixlen` 的判断是 `>= 0`：零值恰好是我们想要的"设成 0"，    
  而"不设置"必须显式传 -1 —— 这两个语义是反的，容易写错。
- 判据纪律再次生效：**"接口是否被重建"用 `/sys/class/net/warp` 的 mtime**。    
  `up` 连跑 3 次后该值不变，证明复用的是同一个接口（也就是同一个出口 IP）。
- 非全局的语义用两条 `curl` 对照验证：绑定接口 → `warp=on`；**不绑定 → `warp=off`**。    
  后者才是"非全局"的定义，也解释了为什么它不会切断 SSH。
- `down` 之后系统自带的 `local` / `main` / `default` 三条规则原封不动 ——    
  清理全程按 pref 精确删除，没有做任何"按范围扫一遍"的动作。

### M1 真机实测（2026-10-14）

- **API 契约按真实响应核对过了**：`endpoint.v4` / `.v6` 的端口是 **0**（只有 `endpoint.host`    
  带 2408）—— 照抄 v4/v6 会得到一个 `:0` 的端点，看着有值、实际永远连不上。
- `created` / `updated` / `tos` 的返回形态不稳定（字符串或 `{}`）：解析层用宽容类型，    
  不让一个无关字段把整次注册搞崩。
- **`Table = off` 不接管任何路由，而 v4 与 v6 表现不同**：接口持有 v4 地址时内核允许    
  按出接口直发（实测 `ip route get 1.1.1.1 oif <if>` → `dev <if>`），v4 直接可用；    
  IPv6 路由语义严格，没有明确路由就是 `Network is unreachable`。    
  → M1 验收为两个探测地址各加一条 /32 与 /128 精确路由，把"隧道与出口可用"与    
  "路由接管（M2 的职责）"解耦，同时全程不动默认路由（SSH 安全）。

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

| 风险                                    | 影响               | 缓解                                                          |
| ------------------------------------- | ---------------- | ----------------------------------------------------------- |
| 非公开 API 版本号变更                         | 注册全挂             | 版本号/UA 集中在 1 个文件；`profile` 支持离线导入；必要时可回落调用 `wgcf`           |
| CF 推进 MASQUE                          | WireGuard 未来可能失效 | 分层设计，换传输不动 `netcfg`                                         |
| reserved 需用户态                         | 内核模式在 HK/LA 握手失败 | 内置 vendored patched device；`SetReserved` 明确报不支持并提示切后端       |
| **全局模式切断 SSH**                        | 机器只能重装           | 默认非全局；全局走 fwmark + watchdog（§8.5）                           |
| **DD 重装系统**                           | 一次参数错即报废         | 默认不编入（§14.1）                                                |
| 第三方二进制依赖（wireproxy / cloudflare-warp） | 上游改了就没法用         | 版本固定 + 校验和；失败时明确报错，不影响主干功能                                  |
| iptables/nftables 分裂                  | 分流不生效或残留         | 优先 nft；两套后端都做 e2e                                           |
| 二进制分发                                 | 用户拿到旧版           | `--check` + `--self-update`（带校验和）；CI 出 amd64/arm64 与 `.deb` |

---

## 14. 保留意见的处理结果

**两处都已在 2026-10-14 由用户确认，按建议执行。**

### 14.1 DD 重装系统：✅ 已确认 —— 不编入主程序

`b` 里的 DD 会**把整台机器重装成另一个系统**，参数错一位就是机器报废  
（而且往往是在远程、无 IPMI 的情况下）。上游把它和 BBR 放在同一个菜单项里，是很危险的耦合。

建议：**默认不编入**。如果你确实要用，我做成  
① 独立子命令 `warpgo dd --i-know-what-i-am-doing`，  
② 必须显式提供镜像 URL 与校验和，  
③ 交互二次确认 + 打印"即将执行的确切命令"，  
④ 不做任何"自动选镜像"逻辑。

**已按此执行：不编入 —— 代码里没有任何 dd 相关实现。**

### 14.2 Team 令牌注册（Teams OAuth）：✅ 已确认 —— 砍掉

`api.sh` 里有一套完整的 Teams Token 验证码流程（curl + cookie 手工复刻 OAuth），  
用于加入 Cloudflare Zero Trust 组织。这与"拿到 WARP 出口"无关，实现量约 300–500 行。

**已按此执行**：`account` 只保留 free / WARP+ 与设备列表，代码里没有 Teams 相关实现。

---

## 附录 A：旧版卸载后的真机审计（2026-10-14 实测）

测试机：Debian 12，非全局，原生 v4+v6，用户已手动卸载旧版。

| 检查项                               | 结果                                                                                              |
| --------------------------------- | ----------------------------------------------------------------------------------------------- |
| wireguard 接口                      | 已删                                                                                              |
| 默认路由                              | 已回到原生（`default via <原生网关> dev <原生网卡>`）                                                          |
| `ip -4 rule`                      | 只剩 `local` / `main` / `default` 三条基本规则                                                          |
| `table 51820`                     | 空                                                                                               |
| `wg-quick@warp` 单元                | 不存在                                                                                             |
| `warp-svc` 单元                     | 不存在                                                                                             |
| `/usr/bin/warp`                   | 已删                                                                                              |
| `/etc/wireguard/` 生成物             | 已删（只剩调试时建的 14 个 `*.bak-*`）                                                                      |
| ipset 规则集 / dnsmasq 配置            | 已清                                                                                              |
| crontab                           | 空                                                                                               |
| **`/etc/resolv.conf`**            | ⚠️ **仍是 `nameserver 1.1.1.1`；`/etc/resolv.conf.origin` 备份已被删** → 无法判断是原值还是 WARP 留下的，且用户再也无法自行还原 |
| BBR（`tcp_congestion_control=bbr`） | 保留（合理）                                                                                          |

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
