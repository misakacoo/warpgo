package msg

// text 是**唯一**的文案表：简体中文。
//
// 曾经还有一份 en.go。已按要求移除，见包注释。
var text = map[string]string{
	KeyUsage: `warpgo —— Debian 上的 Cloudflare WARP 管理工具
（fscarmen/warp 的 Go 重写版；只支持 Debian，不含 Docker，不适配其他平台）

用法:
  warpgo <子命令> [选项]

账户:
  register             注册一个新的 WARP 设备并保存账户
  account              查看本机账户（纯本地读取，不联网）
  profile              输出可直接交给 wg-quick 的配置（逃生口）

隧道:
  up                   建立隧道（幂等；--mode global 为全局接管）
  down                 拆除隧道并清空全部残留
  status               查看当前状态（--json 给脚本用）
  rotate               重新注册换一个 WARP 出口地址
  switch               在线互换后端（内核 / 用户态）或工作模式
  priority             地址族优先级：4 = IPv4 优先，6 = IPv6 优先，d = 系统默认
  proxy                WireProxy：把 WARP 做成一个本地 socks5 出口
  client               官方 WARP 客户端：install / mode / on / off / status / uninstall

保活:
  watch                巡检：健康就不动，断了才重建
  install              把保活装成 systemd 服务（开机自启 + 立即启动）
  uninstall            撤干净：单元、隧道、二进制、WireProxy、官方客户端、DNS 改动
  confirm              保留本次变更并解除回滚保险丝

其他:
  menu                 交互菜单（每个选项就是一条子命令）
  version              显示版本信息（--check 会去 GitHub 查新版本）
  help                 显示本帮助
  dev lock             演练单实例锁（开发调试用）
  dev platform         检查本机是否 Debian

常用选项:
  --backend auto|kernel|userspace
                       隧道后端；auto 会先用内核，实测不通再切用户态
  --mode non-global|global
                       出口接管方式；默认非全局（不会切断 SSH）
  --ifname / --mtu / --endpoint / --dns
                       接口名 / MTU / 对端端点 / 隧道内 DNS

全局选项:
  -v, --verbose        调试日志
  -q, --quiet          只显示警告与错误

退出码:
  0 成功   1 通用错误   2 不健康   3 需要 reserved
  4 已有实例在运行   5 非 Debian 系统`,

	KeyVersion:   "warpgo",
	KeyUnknownCm: "未知子命令: %s",
	KeyNeedArg:   "选项 %s 需要提供取值",
	KeyBadOpt:    "未知选项: %s",

	KeyLockBusy:            "已有另一个 warpgo 实例（PID %d）正在操作 WARP 链路，本次不做任何操作直接退出",
	KeyLockAcquired:        "已获取锁 %s",
	KeyLockReleased:        "已释放锁 %s",
	KeyLockTakeoverStale:   "上一个实例（PID %d）已停止刷新心跳 %s，正在接管锁",
	KeyLockTakeoverLegacy:  "发现旧版本遗留的锁（PID %d，没有心跳字段），正在接管",
	KeyLockTakeoverUnverif: "持锁进程 PID %d 已停跳但无法确认是 warpgo，只接管锁而不终止它",
	KeyLockLeftover:        "发现 PID %d 遗留的锁文件，但该进程已不存在（上一次运行很可能是崩了），正在接管",
	KeyLockKillFailed:      "无法终止已失控的持锁进程 PID %d: %v",
	KeyLockKilled:          "已终止失控的持锁进程 PID %d",
	KeyNotImplemented:      "尚未实现（计划在里程碑 %s 中完成）",
	KeyLockTouch:           "已刷新心跳（持有者 PID %d）",

	KeyRegStart:        "正在注册新的 WARP 设备……",
	KeyRegDone:         "已注册设备 %s（账户类型: %s）",
	KeyRegSaved:        "账户已保存到 %s（权限 0600）",
	KeyRegExists:       "%s 已存在账户；重新注册会更换你的 WARP IP —— 确实要这样做请加 --force",
	KeyRegReservedOK:   "reserved = %s（由 client_id %q 解出）",
	KeyRegReservedNone: "服务端没有返回 client_id，reserved 记为 0, 0, 0 —— 在需要 reserved 的地区握手可能被丢弃",
	KeyRegEnableFailed: "无法在该设备上启用 WARP: %v（账户已保存，可执行 `warpgo up` 前先重试 register）",
	KeyRegAddr:         "接口地址: %s",
	KeyRegEndpoint:     "对端端点: %s",

	KeyAcctNone:        "本机还没有账户 —— 请先执行 `warpgo register`",
	KeyAcctTitle:       "WARP 账户",
	KeyAcctSecretsHint: "私钥与访问令牌保存在 %s，此处刻意不显示",

	KeyNeedRoot: "无法准备状态目录 %s（注册需要 root 权限，或把 WARPGO_ETC 指向可写目录）",

	KeyProfNote: `由 warpgo 生成（设备 %s）。重新生成请执行 ` + "`warpgo profile`" + `。
把它交给 wg-quick 会接管默认路由（所有流量都走 WARP）；
在远程服务器上请先确认这不会切断你的 SSH。
内核 WireGuard 会忽略 "# Reserved" 这一行，原因见 docs/DESIGN.md §8.2。`,
	KeyProfSaved:      "wg-quick 配置已写入 %s（权限 %s）",
	KeyProfSkipped:    "账户没有 %s 地址，已跳过该族",
	KeyProfKernelWarn: "注意：本配置带 reserved = %s，而内核 WireGuard 模块设置不了那 3 个字节 —— 需要 reserved 的地区请用 `warpgo switch --backend userspace`",
	KeyProfTableOffHint: "Table = off：本配置不接管任何路由。IPv4 仍可用，因为接口持有地址、内核允许按出接口直发；" +
		"IPv6 必须有明确路由（M2 的非全局模式会负责这件事）。",

	KeyUpStart:       "正在建立 WARP 隧道（模式 %s，后端 %s，接口 %s）",
	KeyUpLinkCreated: "已创建接口 %s（MTU %d）",
	KeyUpLinkReused:  "接口 %s 已存在，直接复用（不重建、不换 IP）",
	KeyUpAddrs:       "接口地址: %s",
	KeyUpConfigured:  "已下发 WireGuard 配置",
	KeyUpCfgMatched:  "WireGuard 配置已是最新，未重新下发（避免无谓的重握手）",
	KeyUpNetcfg:      "已应用网络配置: %d 条路由、%d 条规则",
	KeyUpCleaned:     "先清理了 %d 条旧规则",
	KeyUpForce:       "--force：会先拆掉现有隧道再重建",
	KeyUpDone:        "WARP 隧道已就绪（%s 模式）",
	KeyUpNotDebian:   "建立隧道会改动系统网络，因此拒绝在 %s 上运行",

	KeyBackendKernel:    "内核",
	KeyBackendUserspace: "用户态",
	KeyBackendAuto:      "自动",

	KeyUpUserspaceReserved: "用户态后端会把 reserved = %s 写进每个出网报文",
	KeyUpPinnedKernel:      "现在用的是内核后端，而本账户带 reserved = %s —— 内核在报文里发的是 0, 0, 0。多数地区不受影响，若隧道不通请改用 --backend userspace",
	KeyUpDaemonStarted:     "已拉起用户态守护进程并创建接口 %s（MTU %d）",
	KeyUpBackendFallback:   "%s后端没能把隧道建起来（%s），改用下一个后端重试",
	KeyUpBackendUnfit:      "%s后端建立后经隧道仍拿不到 warp=on（%v），换一个后端再试",
	KeyUpBackendUnfitAll:   "所有可用后端都没能让隧道真正通起来，最后一个失败原因: %v",
	KeyUpBackendSwitched:   "已自动切换到%s后端并验证通过: %s",
	KeyUpBackendOK:         "%s后端验证通过: %s",
	KeyUpNeedReserved:      "内核实测握不上手，而本账户需要 reserved = %s —— 请去掉 --backend kernel（让程序自己选），或直接用 --backend userspace",

	KeyUpReasonUnhealthy:    "隧道不通",
	KeyUpReasonNeedReserved: "需要 reserved 但当前后端不支持",
	KeyUpReasonLockBusy:     "被单实例锁挡住",
	KeyUpReasonNotDebian:    "不是 Debian 系统",
	KeyUpReasonError:        "执行出错",

	KeyDownStart:   "正在拆除 WARP 隧道",
	KeyDownNothing: "没有需要拆除的东西（接口、残留规则与路由都不存在）",
	KeyDownSummary: "已删除 %d 条规则、清空 %d 条路由",
	KeyDownDone:    "隧道已拆除，现场已清空",

	KeyStatusTitle:       "WARP 隧道状态",
	KeyStatusNoRuntime:   "当前没有运行中的隧道（磁盘上没有运行态）",
	KeyStatusIfaceGone:   "接口 %s 已不存在 —— 可能仍有残留规则/路由，请执行 `warpgo down`",
	KeyStatusProbeOK:     "经隧道出口: %s（%s）warp=%s",
	KeyStatusProbeFail:   "经隧道探测失败: %s",
	KeyStatusHandshake:   "最近握手: %s",
	KeyStatusNoHandshake: "尚未握手",
	KeyStatusDaemon:      "用户态守护进程: %s（PID %d）",
	KeyStatusDaemonNone:  "用户态守护进程: 未运行",
	KeyStatusResvKernel:  "（内核后端在报文里发的是 0, 0, 0）",

	KeyNetRemovedRule: "已删除规则: %s",
	KeyNetAddedRule:   "已添加规则: %s",
	KeyNetAddedRoute:  "已添加路由: %s",
	KeyNetFlushed:     "已从表 %d 清空 %d 条路由",
	KeyNetDeletedLink: "已删除接口 %s",
	KeyNetRefuseTable: "拒绝改动系统路由表 %d",

	KeyGuardArmed:     "保险丝已就位：等待确认，最多 %s（快照 %s）",
	KeyGuardConfirmed: "收到确认，本次变更保留（未回滚）",
	KeyGuardTimeout:   "超时 %s 仍未收到确认 —— 判定变更失败，开始无条件回滚",
	KeyGuardRBRules:   "回滚 1/3：删除 warpgo 添加的策略路由规则",
	KeyGuardRBTable:   "回滚 2/3：清空路由表 %d",
	KeyGuardRBLink:    "回滚 3/3：删除接口 %s",
	KeyGuardRBDone:    "回滚完成：网络已回到没有 WARP 隧道的状态",
	KeyGuardNote:      "（细节）%s",
	KeyGuardStale:     "保险丝超时了，但它守护的那次变更已经不在待确认状态（快照被新的变更换掉或被清除了）—— 不动手，直接退出。%s",

	KeyConfirmOK:   "已确认配置可用，保险丝解除",
	KeyConfirmNone: "当前没有待确认的变更（没有未确认的改动）",

	KeyUpGlobalStart:  "正在启用全局模式（所有流量将走 WARP）",
	KeyUpGuardSpawned: "已派生回滚保险丝：%s 内未确认将自动回滚",
	KeyUpChecking:     "自检中：隧道与默认路径都必须可用",
	KeyUpCheckOK:      "自检通过：%s",
	KeyUpCheckFail:    "自检未通过：%v",
	KeyUpRollingBack:  "正在回滚到变更前的状态",
	KeyUpNoConfirm:    "--no-confirm：配置已应用但未确认；%s 内不执行 `warpgo confirm` 就会自动回滚",
	KeyUpGlobalDone:   "全局模式已生效（所有流量经 WARP）",

	KeyWatchStart:      "保活巡检已启动：每 %s 一次（接口 %s，模式 %s，后端 %s）",
	KeyWatchRound:      "第 %d 轮",
	KeyWatchHealthy:    "链路正常（%s）—— 未做任何改动",
	KeyWatchUnhealthy:  "链路已断：%v —— 开始重建",
	KeyWatchRebuilding: "正在重建隧道",
	KeyWatchRebuilt:    "已重建：%s",
	KeyWatchFailed:     "重建失败：%v（下一轮会重试 —— 进程刻意不退出）",
	KeyWatchLocked:     "第 %d 轮跳过：单实例锁被别的进程持有",
	KeyWatchStopped:    "巡检结束：共 %d 轮 —— 健康 %d、重建 %d、失败 %d、跳过 %d",
	KeyWatchNotDebian:  "保活会建立隧道并改动系统网络，因此拒绝在 %s 上运行",

	KeyUpHardReset: "接口与配置都正常但链路不通 —— 硬复位以强制重新握手（出口 IP 不变，变的只是这一次握手）",

	KeyDNSSet:      "已将 nameserver 设置为 %s",
	KeyDNSBackup:   "原始 resolv.conf 已备份到 %s",
	KeyDNSRestored: "已从备份还原 %s",
	KeyDNSSymlink:  "%s 是符号链接（多半由 systemd-resolved 管理），拒绝覆盖它",

	KeyInstallNotDebian:  "装上去的服务会自动改动系统网络，因此拒绝在 %s 上运行",
	KeyInstallBinary:     "已安装 %s -> %s",
	KeyInstallBinarySame: "%s 已经就是当前运行的二进制 —— 未改动",
	KeyInstallUnit:       "已写入单元 %s",
	KeyInstallEnabled:    "已启用 %s（开机自启）",
	KeyInstallNoStart:    "--no-start：未启动 %s",
	KeyInstallStarted:    "已启动 %s",
	KeyInstallDone:       "安装完成：%s 将每 %s 巡检一次链路",
	KeyPurgeRefused:      "拒绝清理 %s：里面有 warpgo 不认识的文件（%s）—— 确认要删请先自己处理它们",

	KeyUninstallStopped:       "已停止 %s",
	KeyUninstallDisabled:      "已禁用 %s",
	KeyUninstallUnitRemoved:   "已删除单元 %s",
	KeyUninstallNoUnit:        "%s 处没有单元文件 —— 无需停止",
	KeyUninstallLockBusy:      "另有实例持有单实例锁（可能正在手工 up/down）；仍继续卸载",
	KeyUninstallTornDown:      "隧道已拆除；规则、路由与运行态均已清理",
	KeyUninstallNoDNS:         "没有 resolv.conf 备份 —— warpgo 从未改过 DNS",
	KeyUninstallBinaryRemoved: "已删除 %s",
	KeyUninstallNoBinary:      "%s 本来就不存在",
	KeyUninstallKeptBinary:    "--keep-binary：保留 %s",
	KeyUninstallStateKept:     "已保留 %s（里面有你的 WARP 账户；要一并删除用 --purge）",
	KeyUninstallStatePurged:   "已删除 %s（%d 个文件）",
	KeyUninstallDone:          "卸载完成",

	KeyDownWatchStopped: "已停止保活服务 %s（否则它下一轮会把隧道重新建起来）",

	KeyPlatformDebian:   "系统检查: Debian %s",
	KeyPlatformNotDebia: "本命令只支持 Debian（检测到: %s）",

	// 换 IP（M5）
	//
	// 语义：重新注册，直到拿到一个与旧地址不同的 WARP 出口。
	KeyRotateStart:       "正在换一个新的 WARP 出口地址（重新注册，最多尝试 %d 次）",
	KeyRotateAttempt:     "第 %d 次尝试：正在注册新的 WARP 设备……",
	KeyRotateSameAddr:    "注册出来的地址与当前完全相同（%s），继续再试一次",
	KeyRotatePicked:      "已选定的新地址: %s",
	KeyRotateOldAddr:     "当前地址: %s",
	KeyRotateNewAddr:     "新地址:   %s",
	KeyRotateOldExit:     "当前公网出口 IP: %s",
	KeyRotateExitSame:    "出口 IP 仍然是 %s —— 账户与隧道地址确实换了，但这个 PoP 上用的是共享出口，所以公网看到的地址没变。想换到别的地区可以多试几次，或配合 --endpoint 指定别的入口",
	KeyRotateExitChanged: "公网出口 IP 已变化: %s → %s",
	KeyRotateExitUnknown: "公网出口 IP 没能测出来（隧道未运行或探测失败），无法比较换了没有",
	KeyRotateRebuild:     "正在用新账户重建隧道",
	KeyRotateRestoring:   "换地址没能通起来 —— 正在恢复原账户并重建（保证网络可用比换 IP 重要）",
	KeyRotateRestored:    "已恢复原账户与隧道（%s），出口地址未变",
	KeyRotateFailed:      "换 IP 失败: %v",
	KeyRotateOldDevice:   "已从服务端注销旧设备 %s（避免孤儿设备在账户里越积越多）",
	KeyRotateOldDevFail:  "旧设备 %s 未能从服务端注销（不影响使用，只是会留在设备列表里）: %v",

	// 在线互换（M5）
	KeySwitchStart:     "正在在线互换设置（会重建隧道）",
	KeySwitchNoChange:  "设置没有任何变化（后端 %s，模式 %s）—— 未做任何改动",
	KeySwitchBackend:   "隧道后端: %s → %s",
	KeySwitchMode:      "工作模式: %s → %s",
	KeySwitchUnit:      "保活单元的启动参数已同步更新并重启（否则它下一轮会把它建回来）",
	KeySwitchDone:      "互换完成：后端 %s，模式 %s，出口地址不变",
	KeySwitchNotDebian: "互换设置会重构系统网络，因此拒绝在 %s 上运行",

	// 地址族优先级（M6，菜单 s）
	KeyPriorityTitle:     "地址族优先级",
	KeyPriorityV4:        "IPv4 优先",
	KeyPriorityV6:        "IPv6 优先",
	KeyPriorityDefault:   "系统默认",
	KeyPrioritySet:       "已把地址族优先级设为 %s（写入 %s）",
	KeyPriorityUnchanged: "优先级已经是 %s —— 未改动 %s",
	KeyPriorityV6Caveat: "说明：Debian 12 的 glibc 默认就是 IPv6 优先，而 `label 2002::/16   2` 这一行" +
		"实测并不改变解析顺序 —— 所以这里真正起作用的一步是「把 IPv4 优先那一行删掉」。",
	KeyPriorityHint: "gai.conf 只影响 glibc 的 getaddrinfo（curl / wget / apt / ssh 这类程序）；" +
		"静态链接的程序与 Go 程序不读它，顺序由各自实现决定。",
	KeyPriorityOrderV4:      "IPv4 优先（%s —— getent ahosts %s 的第一条）",
	KeyPriorityOrderV6:      "IPv6 优先（%s —— getent ahosts %s 的第一条）",
	KeyPriorityOrderUnknown: "无法测定解析顺序（%s）",
	KeyPriorityNoGetent:     "无法用 getent 观察解析顺序（%s: %v）",
	KeyPriorityNotDebian:    "改地址族优先级会改动系统文件，因此拒绝在 %s 上运行",
	KeyPriorityTooMany:      "只能指定一个优先级（多出来的: %s）",

	// WireProxy（M7，菜单 w / y）
	KeyProxyUsage: `用法: warpgo proxy <install|uninstall|on|off|status>

  install    下载 wireproxy、生成配置并装成 systemd 服务（菜单 w）
  on | off   开 / 关这个 socks5 出口（菜单 y）
  status     查看状态（--json 给脚本用）
  uninstall  撤掉：停服务、删单元、删二进制与配置

install 的选项:
  --port <端口>          socks5 监听端口（默认 40000，只监听 127.0.0.1）
  --version <版本>       默认钉死在 v1.1.3；写 latest 会去查最新版
  --url <归档地址>       自己指定下载地址（必须同时给 --sha256）
  --sha256 <校验和>      手工给出的校验和
  --from <本地文件>      离线安装：用本机已有的归档或二进制
  --mtu <MTU>            隧道内 MTU（默认与外层隧道一致）
  --no-start             只装好，不立刻启动`,
	KeyProxyNotDebian: "装的是开机自启的服务，因此拒绝在 %s 上运行",
	KeyProxyPortBusy:  "端口 %d 已经有人在监听 —— 换一个端口（--port）或先停掉那个进程",
	KeyProxyBinary:    "已安装 %s（版本 %s，来源 %s，sha256 %s）",
	KeyProxyFromLocal: "离线安装：使用本机文件 %s（没有联网，也没有发布页清单可核对；" +
		"给了 --sha256 才会核对它）",
	KeyProxyVersion:     "wireproxy 版本: %s",
	KeyProxyConfWritten: "配置已写入 %s（0600；里面有私钥）",
	KeyProxyConfigOK:    "配置自检通过（wireproxy -n 认这份配置）",
	KeyProxyEnabled:     "已启用 %s（开机自启）",
	KeyProxyStarted:     "已启动 %s",
	KeyProxyNoStart:     "--no-start：没有启动 %s",
	KeyProxyReserved: "注意：本账户带 reserved = %s，而 wireproxy 用的是官方 wireguard-go，" +
		"**发不出那 3 个字节** —— 需要 reserved 的地区会表现为「配置都对、就是不通」。" +
		"那种情况请改用隧道模式：`warpgo switch --backend userspace`",
	KeyProxyVerifyRetry: "第 %d/%d 次探测还没通过（%v），稍后再试",
	KeyProxyOnOK:        "WireProxy 已就绪：经 %s 访问的流量是 WARP（%s）",
	KeyProxyUnhealthy: "WireProxy 在运行，但经它出去的流量还不是 WARP：%v\n" +
		"（端口在听只说明进程活着，不代表链路通。）服务刻意保持运行 —— 对端恢复后它会自己接上；要关掉用 `warpgo proxy off`",
	KeyProxyInstallDone:  "安装完成：socks5 已可用（%s，端口 %d）",
	KeyProxyNotInstalled: "本机还没有装 WireProxy —— 先执行 `warpgo proxy install`",
	KeyProxyNotRunning:   "%s 没有在运行（用 `warpgo proxy on` 启动，或重跑 install）",
	KeyProxyAlreadyOff:   "WireProxy 本来就没在运行",
	KeyProxyOffOK:        "已停止 WireProxy（端口 %d 已释放）",
	KeyProxyKeptBinary:   "--keep-binary：保留 %s",
	KeyProxyUninstalled:  "已撤销 WireProxy（处理了 %d 项：单元、二进制、配置与状态）",
	KeyProxyStatusTitle:  "WireProxy 状态",
	KeyUninstallProxy:    "已一并撤销 WireProxy（菜单 w 装的那套）",

	// 官方 Linux Client（M8）
	KeyClientUsage:            "用法: warpgo client install|uninstall|mode|on|off|status [选项]",
	KeyClientNotDebian:        "官方客户端只提供 Debian 系（apt）的包，因此拒绝在 %s 上运行",
	KeyClientArch:             "官方客户端只发布 amd64 / arm64 的包，当前架构是 %s",
	KeyClientAlreadyInstalled: "本机已经装过官方客户端 —— 继续按新参数重新配置",
	KeyClientRepoCreated:      "已配置 apt 源（新建 %d 个文件，codename=%s）",
	KeyClientRepoReady:        "apt 源已在位（codename=%s）",
	KeyClientInstalling:       "正在安装 cloudflare-warp（apt）…",
	KeyClientInstalled:        "已安装 %s",
	KeyClientCLIMissing:       "装完之后仍然找不到 warp-cli —— 安装很可能失败了，请回看 apt 的输出",
	KeyClientServiceWaiting:   "正在等待客户端服务就绪（/run/cloudflare-warp/warp_service）…",
	KeyClientServiceStarted:   "客户端服务已就绪",
	KeyClientServiceDown:      "%s 没有在运行（用 `warpgo client on` 启动，或重跑 install）",
	KeyClientRegistering:      "正在注册本机设备…",
	KeyClientRegistered:       "注册完成",
	KeyClientModeSet:          "已设置工作模式: %s",
	KeyClientNoStart:          "--no-start：只配置好，没有连接",
	KeyClientIfaceWaiting:     "正在等待接口 CloudflareWARP 出现…",
	KeyClientIfaceMissing: "等不到 CloudflareWARP 接口: %v\n" +
		"（多半是客户端没能连上，用 `warpgo client status` 看它的状态）",
	KeyClientIfaceUp:        "接口 %s 已就绪",
	KeyClientSocksWaiting:   "正在等待 socks5 端口开始监听…",
	KeyClientSocksMissing:   "等不到 socks5 端口: %v",
	KeyClientSocksOK:        "socks5 已可用（端口 %d）",
	KeyClientHooksInstalled: "已写入钩子 %s（指向 %s）：服务起来后自动补策略规则、停下时撤掉",
	KeyClientHooksAdded:     "已加上策略规则（规则 %d 条、路由 %d 条）",
	KeyClientHooksFailed: "策略规则没能加上：%v\n" +
		"（客户端本身可用；缺的只是「让以接口地址为源的流量走 WARP」这一步）",
	KeyClientInstallDone:    "官方客户端安装完成（模式 %s）",
	KeyClientNotInstalled:   "本机还没有装官方客户端 —— 先执行 `warpgo client install`",
	KeyClientAlreadyOn:      "官方客户端本来就已经连着",
	KeyClientAlreadyOff:     "官方客户端本来就没连",
	KeyClientOnOK:           "官方客户端已连接",
	KeyClientOffOK:          "官方客户端已断开",
	KeyClientStatusTitle:    "官方客户端状态",
	KeyClientUninstallStart: "正在撤销官方客户端…",
	KeyClientUninstallDone:  "官方客户端已撤销",
	KeyClientDropInRemoved:  "已删除钩子 %s",
	KeyClientRepoRemoved:    "已删除我们建的 apt 源文件（%d 个）",
	KeyClientRepoKept:       "apt 源与密钥环保持不变（不是我们建的，或用了 --keep-repo）",
	KeyClientWarpRisk: "注意：官方客户端的 warp 模式会**接管默认路由**（整机流量走 WARP）。\n" +
		"  在远程服务器上这有可能切断 SSH —— 请确认能通过控制台 / VNC 恢复再用；\n" +
		"  只想让本机多一个出口而不动路由的话，用默认的 proxy 模式。",
	KeyClientNotOurs: "本机装着官方客户端，但不是 warpgo 装的 —— 总卸载不去动它" +
		"（要删请显式执行 `warpgo client uninstall`）",

	// 钩子（M8，菜单 z / x）
	KeyHookUsage:       "用法: warpgo hook add [--wait 30s] | warpgo hook del（隐藏命令，供 warp-svc 的 ExecStartPost / ExecStop 调用）",
	KeyHookAdded:       "已加上钩子规则（规则 %d 条、路由 %d 条）",
	KeyHookRemoved:     "已撤掉钩子规则（规则 %d 条、路由 %d 条）",
	KeyHookIfaceAbsent: "接口 %s 还没出现，本次不加规则（客户端多半不在 warp 模式下，或还没连上）",
	KeyHookNeedRoot:    "%s 需要 root 权限（要改策略路由）",

	// 交互菜单（M8）
	KeyMenuTitle:         "warpgo 交互菜单",
	KeyMenuPrompt:        "请选择> ",
	KeyMenuQuit:          "退出",
	KeyMenuBye:           "再见。",
	KeyMenuBadChoice:     "不认识的选择: %s",
	KeyMenuSubFailed:     "`%s` 以退出码 %d 结束",
	KeyMenuNoRuntime:     "还没有运行态（先 `up` 一次），不知道当前用的是哪个",
	KeyMenuSwitchTo:      "当前 %s → 将切到 %s",
	KeyMenuAskPriority:   "地址族优先级（4 = IPv4 优先，6 = IPv6 优先，d = 系统默认）> ",
	KeyMenuUpNonGlobal:   "建立隧道（非全局，不会切断 SSH）",
	KeyMenuUpGlobal:      "建立隧道（全局接管，带自动回滚）",
	KeyMenuDown:          "拆除隧道",
	KeyMenuStatus:        "查看状态",
	KeyMenuRotate:        "换一个出口 IP（重新注册）",
	KeyMenuSwitchBackend: "在线互换后端（内核 ↔ 用户态）",
	KeyMenuSwitchMode:    "在线互换工作模式（非全局 ↔ 全局）",
	KeyMenuPrio