package i18n

// zh 中文文案。
var zh = map[string]string{
	KeyUsage: `warpgo —— Debian 上的 Cloudflare WARP 管理工具
（fscarmen/warp 的 Go 重写版；仅支持 Debian，不含 Docker，不适配其他平台）

用法:
  warpgo <子命令> [选项]

子命令:
  version              显示版本信息
  help                 显示本帮助
  dev lock             演练单实例锁（开发调试用）

全局选项:
  --lang en|zh         强制输出语言（默认按 LANG 推断）
  -v, --verbose        调试日志
  -q, --quiet          只显示警告与错误

计划中的子命令（按里程碑逐步实现）:
  register  up  down  status  watch  rotate  switch  priority
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

	KeyPlatformDebian:   "系统检查: Debian %s",
	KeyPlatformNotDebia: "本命令只支持 Debian（检测到: %s）",
}
