# warpgo

Debian 上的 Cloudflare WARP 管理工具 —— [fscarmen/warp](https://gitlab.com/fscarmen/warp) 的 Go 重写版。

> **边界**：只支持 Debian。不含 Docker，不适配 macOS / 路由器 / 第三方内核。
> **定位**：不是"加个功能"，而是把最难维护、最容易出错的那一半重写成静态单二进制 ——
> 换掉的是可维护性、可观测性与可预测性，**不是**带宽（数据面用的是同一个 WireGuard 实现）。

完整设计见 **[docs/DESIGN.md](docs/DESIGN.md)**。

## 状态

按里程碑推进。当前：**M1 完成（注册 / reserved / profile 生成）**

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M0 | 骨架、单实例锁、i18n、日志 | ✅ 完成 |
| M1 | 注册 / reserved / profile 生成 | ✅ 完成（真机 39/39） |
| M2 | 内核后端 + 非全局出口 + up/down/status + 菜单骨架 | 未开始 |
| M3 | 全局模式 + fwmark 防环路 + watchdog 回滚 | 未开始 |
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

## 许可

GPL-3.0。注册 API 的请求形状与常量取自 fscarmen/warp 的 `api.sh`（派生作品），
沿用 GPL-3.0 并保留原作者署名。
