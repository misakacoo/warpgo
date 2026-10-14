package msg

// 文案 key。集中定义，避免各处硬编码字符串（也就不会有"改了这里忘了那里"）。
//
// 曾经有个 KeyBadLang（不支持的语言）。语言机制已移除，它也随之删掉。
const (
	// 通用
	KeyUsage     = "usage"
	KeyVersion   = "version.label"
	KeyUnknownCm = "cli.unknown_command"
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
	KeyUpStart       = "up.start"
	KeyUpLinkCreated = "up.link_created"
	KeyUpLinkReused  = "up.link_reused"
	KeyUpAddrs       = "up.addresses"
	KeyUpConfigured  = "up.configured"
	KeyUpCfgMatched  = "up.config_matched"
	KeyUpNetcfg      = "up.netcfg_applied"
	KeyUpCleaned     = "up.cleaned_old"
	KeyUpForce       = "up.force_teardown"
	KeyUpDone        = "up.done"
	KeyUpNotDebian   = "up.not_debian"

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

	// 保活巡检（M4）
	KeyWatchStart      = "watch.start"
	KeyWatchRound      = "watch.round"
	KeyWatchHealthy    = "watch.healthy"
	KeyWatchUnhealthy  = "watch.unhealthy"
	KeyWatchRebuilding = "watch.rebuilding"
	KeyWatchRebuilt    = "watch.rebuilt"
	KeyWatchFailed     = "watch.rebuild_failed"
	KeyWatchLocked     = "watch.locked"
	KeyWatchStopped    = "watch.stopped"
	KeyWatchNotDebian  = "watch.not_debian"

	// 硬复位（M4）
	KeyUpHardReset = "up.hard_reset"

	// DNS 备份与还原（M4）
	KeyDNSSet      = "dns.set"
	KeyDNSBackup   = "dns.backup"
	KeyDNSRestored = "dns.restored"
	KeyDNSSymlink  = "dns.symlink"

	// 安装（M4）
	KeyInstallNotDebian  = "install.not_debian"
	KeyInstallBinary     = "install.binary"
	KeyInstallBinarySame = "install.binary_same"
	KeyInstallUnit       = "install.unit_written"
	KeyInstallEnabled    = "install.enabled"
	KeyInstallNoStart    = "install.no_start"
	KeyInstallStarted    = "install.started"
	KeyInstallDone       = "install.done"
	KeyPurgeRefused      = "install.purge_refused"

	// 卸载（M4）
	KeyUninstallStopped       = "uninstall.stopped"
	KeyUninstallDisabled      = "uninstall.disabled"
	KeyUninstallUnitRemoved   = "uninstall.unit_removed"
	KeyUninstallNoUnit        = "uninstall.no_unit"
	KeyUninstallLockBusy      = "uninstall.lock_busy"
	KeyUninstallTornDown      = "uninstall.torn_down"
	KeyUninstallNoDNS         = "uninstall.no_dns_backup"
	KeyUninstallBinaryRemoved = "uninstall.binary_removed"
	KeyUninstallNoBinary      = "uninstall.binary_missing"
	KeyUninstallKeptBinary    = "uninstall.binary_kept"
	KeyUninstallStateKept     = "uninstall.state_kept"
	KeyUninstallStatePurged   = "uninstall.state_purged"
	KeyUninstallDone          = "uninstall.done"

	// 拆除时的保活联动（M4）
	KeyDownWatchStopped = "down.watch_stopped"

	// 平台
	KeyPlatformDebian   = "platform.debian"
	KeyPlatformNotDebia = "platform.not_debian"
)


// AllKeys 列出本文件声明的全部 key。
//
// 它存在的唯一目的是让单测能守住「声明与文案一一对应」：
// 加了常量却忘了写文案 → 测试红；写了文案却没有常量 → 测试也红。
// 没有它就只能靠人眼比对两个文件，而那一定会漏。
func AllKeys() []string {
	return []string{
		KeyUsage,
		KeyVersion,
		KeyUnknownCm,
		KeyNeedArg,
		KeyBadOpt,
		KeyLockBusy,
		KeyLockAcquired,
		KeyLockReleased,
		KeyLockTakeoverStale,
		KeyLockTakeoverLegacy,
		KeyLockTakeoverUnverif,
		KeyLockLeftover,
		KeyLockKillFailed,
		KeyLockKilled,
		KeyLockTouch,
		KeyNotImplemented,
		KeyRegStart,
		KeyRegDone,
		KeyRegSaved,
		KeyRegExists,
		KeyRegReservedOK,
		KeyRegReservedNone,
		KeyRegEnableFailed,
		KeyRegAddr,
		KeyRegEndpoint,
		KeyAcctNone,
		KeyAcctTitle,
		KeyAcctRefreshed,
		KeyAcctEnableOK,
		KeyAcctLicenseOK,
		KeyAcctSecretsHint,
		KeyNeedRoot,
		KeyUpStart,
		KeyUpLinkCreated,
		KeyUpLinkReused,
		KeyUpAddrs,
		KeyUpConfigured,
		KeyUpCfgMatched,
		KeyUpNetcfg,
		KeyUpCleaned,
		KeyUpForce,
		KeyUpDone,
		KeyUpNotDebian,
		KeyDownStart,
		KeyDownNothing,
		KeyDownSummary,
		KeyDownDone,
		KeyGuardArmed,
		KeyGuardConfirmed,
		KeyGuardTimeout,
		KeyGuardRBRules,
		KeyGuardRBTable,
		KeyGuardRBLink,
		KeyGuardRBDone,
		KeyGuardNote,
		KeyConfirmOK,
		KeyConfirmNone,
		KeyUpGlobalStart,
		KeyUpGuardSpawned,
		KeyUpChecking,
		KeyUpCheckOK,
		KeyUpCheckFail,
		KeyUpRollingBack,
		KeyUpNoConfirm,
		KeyUpGlobalDone,
		KeyStatusTitle,
		KeyStatusNoRuntime,
		KeyStatusIfaceGone,
		KeyStatusProbeOK,
		KeyStatusProbeFail,
		KeyStatusHandshake,
		KeyStatusNoHandshake,
		KeyNetRemovedRule,
		KeyNetAddedRule,
		KeyNetAddedRoute,
		KeyNetFlushed,
		KeyNetDeletedLink,
		KeyNetRefuseTable,
		KeyProfNote,
		KeyProfSaved,
		KeyProfSkipped,
		KeyProfKernelWarn,
		KeyProfTableOffHint,
		KeyWatchStart,
		KeyWatchRound,
		KeyWatchHealthy,
		KeyWatchUnhealthy,
		KeyWatchRebuilding,
		KeyWatchRebuilt,
		KeyWatchFailed,
		KeyWatchLocked,
		KeyWatchStopped,
		KeyWatchNotDebian,
		KeyUpHardReset,
		KeyDNSSet,
		KeyDNSBackup,
		KeyDNSRestored,
		KeyDNSSymlink,
		KeyInstallNotDebian,
		KeyInstallBinary,
		KeyInstallBinarySame,
		KeyInstallUnit,
		KeyInstallEnabled,
		KeyInstallNoStart,
		KeyInstallStarted,
		KeyInstallDone,
		KeyPurgeRefused,
		KeyUninstallStopped,
		KeyUninstallDisabled,
		KeyUninstallUnitRemoved,
		KeyUninstallNoUnit,
		KeyUninstallLockBusy,
		KeyUninstallTornDown,
		KeyUninstallNoDNS,
		KeyUninstallBinaryRemoved,
		KeyUninstallNoBinary,
		KeyUninstallKeptBinary,
		KeyUninstallStateKept,
		KeyUninstallStatePurged,
		KeyUninstallDone,
		KeyDownWatchStopped,
		KeyPlatformDebian,
		KeyPlatformNotDebia,
	}
}
