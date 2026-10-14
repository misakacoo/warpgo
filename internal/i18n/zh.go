package i18n

// zh 中文文案。
var zh = map[string]string{
	KeyUsage: `warpgo —— Debian 上的 Cloudflare WARP 管理工具
（fscarmen/warp 的 Go 重写版；仅支持 Debian，不含 Docker，不适配其他平台）

用法:
  warpgo <子命令> [选项]

子命令:
  register             注册新的 WARP 设备并保存账户
  account              显示已保存的账户（--refresh 从服务端拉最新）
  profile              为已保存的账户输出一份 wg-quick 配置
  version              显示版本信息
  help                 显示本帮助
  dev lock             演练单实例锁（开发调试用）
  dev platform         检查是否为 Debian 系统

全局选项:
  --lang en|zh         强制输出语言（默认按 LANG 推断）
  -v, --verbose        调试日志
  -q, --quiet          只显示警告与错误

计划中的子命令（按里程碑逐步实现）:
  up  down  status  watch  rotate  switch  priority
  split  proxy  client  bbr  uninstall  version --check

退出码:
  0 成功   1 通用错误   2 体检不健康   3 需要 reserved 但后端不支持
  4 已有实例在运行   5 不是 Debian 系统`,

	KeyVersion:   "warpgo",
	KeyUnknownCm: "未知子命令: %s",
	KeyBadLang:   "不支持的语言: %q（只支持 en 或 zh）",
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
	KeyRegEnableFailed: "无法在该设备上启用 WARP: %v（账户已保存，可稍后执行 `warpgo account --enable` 重试）",
	KeyRegAddr:         "接口地址: %s",
	KeyRegEndpoint:     "对端端点: %s",

	KeyAcctNone:        "本机还没有账户 —— 请先执行 `warpgo register`",
	KeyAcctTitle:       "WARP 账户",
	KeyAcctRefreshed:   "已从服务端刷新账户信息",
	KeyAcctEnableOK:    "已在该设备上启用 WARP",
	KeyAcctLicenseOK:   "已为该账户绑定 license",
	KeyAcctSecretsHint: "私钥与访问令牌保存在 %s，此处刻意不显示",

	KeyNeedRoot: "无法准备状态目录 %s（注册需要 root 权限，或把 WARPGO_ETC 指向可写目录）",

	KeyProfNote: `由 warpgo 生成（设备 %s）。重新生成请执行 ` + "`warpgo profile`" + `。
把它交给 wg-quick 会接管默认路由（所有流量都走 WARP）；
在远程服务器上请先确认这不会切断你的 SSH。
内核 WireGuard 会忽略 "# Reserved" 这一行，原因见 docs/DESIGN.md §8.2。`,
	KeyProfSaved:      "wg-quick 配置已写入 %s（权限 %s）",
	KeyProfSkipped:    "账户没有 %s 地址，已跳过该族",
	KeyProfKernelWarn: "注意：本配置带 reserved = %s，但内核 WireGuard 模块设置不了那 3 个字节 —— 需要 reserved 的地区要用用户态后端（里程碑 M5）",
	KeyProfTableOffHint: "Table = off：本配置不接管任何路由。IPv4 仍可用，因为接口持有地址、内核允许按出接口直发；" +
		"IPv6 必须有明确路由（M2 的非全局模式会负责这件事）。",

	KeyUpStart:        "正在建立 WARP 隧道（模式 %s，接口 %s）",
	KeyUpLinkCreated:  "已创建接口 %s（MTU %d）",
	KeyUpLinkReused:   "接口 %s 已存在，直接复用（不重建、不换 IP）",
	KeyUpAddrs:        "接口地址: %s",
	KeyUpConfigured:   "已下发 WireGuard 配置",
	KeyUpCfgMatched:   "WireGuard 配置已是最新，未重新下发（避免无谓的重握手）",
	KeyUpNetcfg:       "已应用网络配置: %d 条路由、%d 条规则",
	KeyUpCleaned:      "先清理了 %d 条旧规则",
	KeyUpForce:        "--force：会先拆掉现有隧道再重建",
	KeyUpDone:         "WARP 隧道已就绪（%s 模式）",
	KeyUpNotDebian:    "建立隧道会改动系统网络，因此拒绝在 %s 上运行",

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

	KeyConfirmOK: "已确认配置可用，保险丝解除",
	KeyConfirmNone: "当前没有待确认的变更（没有未确认的改动）",

	KeyUpGlobalStart:  "正在启用全局模式（所有流量将走 WARP）",
	KeyUpGuardSpawned: "已派生回滚保险丝：%s 内未确认将自动回滚",
	KeyUpChecking:     "自检中：隧道与默认路径都必须可用",
	KeyUpCheckOK:      "自检通过：%s",
	KeyUpCheckFail:    "自检未通过：%v",
	KeyUpRollingBack:  "正在回滚到变更前的状态",
	KeyUpNoConfirm:    "--no-confirm：配置已应用但未确认；%s 内不执行 `warpgo confirm` 就会自动回滚",
	KeyUpGlobalDone:   "全局模式已生效（所有流量经 WARP）",

	KeyPlatformDebian:   "系统检查: Debian %s",
	KeyPlatformNotDebia: "本命令只支持 Debian（检测到: %s）",
}
