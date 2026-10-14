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

	// 注册（M1）
	KeyRegStart        = "register.start"
	KeyRegDone         = "register.done"
	KeyRegSaved        = "register.saved"
	KeyRegExists       = "register.already_exists"
	KeyRegReservedOK   = "register.reserved"
	KeyRegReservedNone = "register.no_reserved"
	KeyRegEnableFailed = "register.enable_failed"
	KeyRegAddr         = "register.addresses"
	KeyRegEndpoint     = "register.endpoint"

	// 账户（M1）
	KeyAcctNone        = "account.none"
	KeyAcctTitle       = "account.title"
	KeyAcctRefreshed   = "account.refreshed"
	KeyAcctEnableOK    = "account.enable_ok"
	KeyAcctLicenseOK   = "account.license_ok"
	KeyAcctSecretsHint = "account.secrets_hidden"

	// 状态目录（M1）
	KeyNeedRoot = "state.need_root"

	// profile（M1）
	KeyProfNote         = "profile.note"
	KeyProfSaved        = "profile.saved"
	KeyProfSkipped      = "profile.skipped"
	KeyProfKernelWarn   = "profile.kernel_reserved_warning"
	KeyProfTableOffHint = "profile.table_off_hint"

	// 平台
	KeyPlatformDebian   = "platform.debian"
	KeyPlatformNotDebia = "platform.not_debian"
)
