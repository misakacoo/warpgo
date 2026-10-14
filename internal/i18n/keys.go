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

	// 建立隧道（M2）
	KeyUpStart        = "up.start"
	KeyUpLinkCreated  = "up.link_created"
	KeyUpLinkReused   = "up.link_reused"
	KeyUpAddrs        = "up.addresses"
	KeyUpConfigured   = "up.configured"
	KeyUpCfgMatched   = "up.config_matched"
	KeyUpNetcfg       = "up.netcfg_applied"
	KeyUpCleaned      = "up.cleaned_old"
	KeyUpForce        = "up.force_teardown"
	KeyUpDone         = "up.done"
	KeyUpNotDebian    = "up.not_debian"

	// 拆除隧道（M2）
	KeyDownStart   = "down.start"
	KeyDownNothing = "down.nothing"
	KeyDownSummary = "down.summary"
	KeyDownDone    = "down.done"

	// 回滚保险丝（M3）
	KeyGuardArmed     = "guard.armed"
	KeyGuardConfirmed = "guard.confirmed"
	KeyGuardTimeout   = "guard.timeout"
	KeyGuardRBRules   = "guard.rollback_rules"
	KeyGuardRBTable   = "guard.rollback_table"
	KeyGuardRBLink    = "guard.rollback_link"
	KeyGuardRBDone    = "guard.rollback_done"
	KeyGuardNote      = "guard.note"

	// 确认（M3）
	KeyConfirmOK   = "confirm.ok"
	KeyConfirmNone = "confirm.nothing_pending"

	// 全局模式（M3）
	KeyUpGlobalStart  = "up.global_start"
	KeyUpGuardSpawned = "up.guard_spawned"
	KeyUpChecking     = "up.self_check"
	KeyUpCheckOK      = "up.self_check_passed"
	KeyUpCheckFail    = "up.self_check_failed"
	KeyUpRollingBack  = "up.rolling_back"
	KeyUpNoConfirm    = "up.no_confirm"
	KeyUpGlobalDone   = "up.global_done"

	// 状态（M2）
	KeyStatusTitle       = "status.title"
	KeyStatusNoRuntime   = "status.no_runtime"
	KeyStatusIfaceGone   = "status.interface_missing"
	KeyStatusProbeOK     = "status.probe_ok"
	KeyStatusProbeFail   = "status.probe_failed"
	KeyStatusHandshake   = "status.handshake"
	KeyStatusNoHandshake = "status.no_handshake"

	// 网络配置明细（M2）
	KeyNetRemovedRule = "netcfg.removed_rule"
	KeyNetAddedRule   = "netcfg.added_rule"
	KeyNetAddedRoute  = "netcfg.added_route"
	KeyNetFlushed     = "netcfg.flushed_routes"
	KeyNetDeletedLink = "netcfg.deleted_link"
	KeyNetRefuseTable = "netcfg.refuse_system_table"

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
