# warpgo

Debian 上的 Cloudflare WARP 管理工具 —— [fscarmen/warp](https://gitlab.com/fscarmen/warp) 的 Go 重写版。

> **边界**：只支持 Debian。不含 Docker，不适配 macOS / 路由器 / 第三方内核。
> **定位**：不是"加个功能"，而是把最难维护、最容易出错的那一半重写成静态单二进制 ——
> 换掉的是可维护性、可观测性与可预测性，**不是**带宽（数据面用的是同一个 WireGuard 实现）。

完整设计见 **[docs/DESIGN.md](docs/DESIGN.md)**。

## 状态

按里程碑推进。当前：**M3 完成（全局模式 + fwmark 防环路 + 自动回滚）**

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 骨架、单实例锁、i18n、日志 | ✅ 完成 |
| M1 | 注册 / reserved / profile 生成 | ✅ 完成（真机 39/39） |
| M2 | 内核后端 + 非全局出口 + up/down/status | ✅ 完成（真机 38/38） |
| M3 | 全局模式 + fwmark 防环路 + watchdog 回滚 | ✅ 完成（真机 31/31） |
| M4 | 体检 + 保活 + systemd + 卸载 | 未开始 |
| M5 | 用户态后端（reserved）+ rotate + 账户/license | 未开始 |
| M6 | 栈优先级 + iptables/dnsmasq/ipset 分流 | 未开始 |
| M7 | WireProxy | 未开始 |
| M8 | 官方 Linux Client（c/l/r/z/x） | 未开始 |
| M9 | BBR + 版本自更新 + 打包 | 未开始 |

## 用法（M1 已可用）

```bash
sudo ./dist/warpgo register            # 注册设备（私钥只在本机生成，绝不外发）
sudo ./dist/warpgo account             # 查看账户（--json 输出脱敏视图）
sudo ./dist/warpgo account --refresh   # 从服务端刷新
sudo ./dist/warpgo account --enable    # 只补做「启用 WARP」这一步

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

## 构建与测试（在 Debian 上）

```bash
./build.sh                 # 产出 dist/warpgo
go test ./...              # 单元测试（不需要 root、不需要网络）
./dist/warpgo version
./dist/warpgo dev lock --hold 3s      # 演练单实例锁

tests/e2e/m0-lock.sh                   # M0 真机验收（单实例锁，16 项）
tests/e2e/m1-register.sh               # M1 真机验收（注册 + 隧道，39 项）
```

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
- **单个静态二进制，零运行时依赖**（不要求系统装 `wg`/`wg-quick`/`ip`/`jq`/`python`）。
  M2 起引入了几个 Go 库（netlink / wgctrl），它们编译进二进制，不改变这一点。

## 许可

GPL-3.0。注册 API 的请求形状与常量取自 fscarmen/warp 的 `api.sh`（派生作品），
沿用 GPL-3.0 并保留原作者署名。
