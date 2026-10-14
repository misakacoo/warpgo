package i18n

// 文案 key。集中定义，避免各处硬编码字符串导致漏翻译。
const (
	// 通用
	KeyUsage     = "usage"
	KeyVersion   = "version.label"
	KeyUnknownCm = "cli.unknown_command"
	KeyBadLang   = "cli.bad_language"
	KeyNeedArg   = "cli.missing_argument"
	KeyBadOpt    = "cli.bad_option"

	// 单实例锁
	KeyLockBusy            = "lock.busy"
	KeyLockAcquired        = "lock.acquired"
	KeyLockReleased        = "lock.released"
	KeyLockTakeoverStale   = "lock.takeover_stale"
	KeyLockTakeoverLegacy  = "lock.takeover_legacy"
	KeyLockTakeoverUnverif = "lock.takeover_unverified"
	KeyLockLeftover        = "lock.leftover_dead_holder"
	KeyLockKillFailed      = "lock.kill_failed"
	KeyLockKilled          = "lock.killed_stale_holder"
	KeyLockTouch           = "lock.touch"

	// 通用
	KeyNotImplemented = "cli.not_implemented"

	// 平台
	KeyPlatformDebian   = "platform.debian"
	KeyPlatformNotDebia = "platform.not_debian"
)
