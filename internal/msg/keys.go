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
	KeyGuardStale     = "guard.stale"

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

	// 隧道后端（M5）
	//
	// 三个名字（内核 / 用户态 / 自动）要分别有文案：日志里说的必须是
	// "现在实际跑的是哪个"，而"自动"是用户能填的一个取值，不是一种后端。
	KeyBackendKernel    = "backend.kernel"
	KeyBackendUserspace = "backend.userspace"
	KeyBackendAuto      = "backend.auto"

	// 用户态后端与 reserved（M5）
	KeyUpUserspaceReserved  = "up.userspace_reserved"
	KeyUpPinnedKernel       = "up.pinned_kernel"
	KeyUpDaemonStarted      = "up.daemon_started"
	KeyUpBackendFallback    = "up.backend_fallback"
	KeyUpBackendUnfit       = "up.backend_unfit"
	KeyUpBackendUnfitAll    = "up.backend_unfit_all"
	KeyUpBackendSwitched    = "up.backend_switched"
	KeyUpBackendOK          = "up.backend_ok"
	KeyUpNeedReserved       = "up.need_reserved"
	KeyUpReasonUnhealthy    = "up.reason.unhealthy"
	KeyUpReasonNeedReserved = "up.reason.need_reserved"
	KeyUpReasonLockBusy     = "up.reason.lock_busy"
	KeyUpReasonNotDebian    = "up.reason.not_debian"
	KeyUpReasonError        = "up.reason.error"

	// 换 IP（M5）
	KeyRotateStart       = "rotate.start"
	KeyRotateAttempt     = "rotate.attempt"
	KeyRotateSameAddr    = "rotate.same_address"
	KeyRotatePicked      = "rotate.picked"
	KeyRotateOldAddr     = "rotate.old_address"
	KeyRotateNewAddr     = "rotate.new_address"
	KeyRotateOldExit     = "rotate.old_exit_ip"
	KeyRotateExitSame    = "rotate.exit_unchanged"
	KeyRotateExitChanged = "rotate.exit_changed"
	KeyRotateExitUnknown = "rotate.exit_unknown"
	KeyRotateRebuild     = "rotate.rebuild"
	KeyRotateRestoring   = "rotate.restoring"
	KeyRotateRestored    = "rotate.restored"
	KeyRotateFailed      = "rotate.failed"
	KeyRotateOldDevice   = "rotate.old_device_removed"
	KeyRotateOldDevFail  = "rotate.old_device_failed"

	// 在线互换（M5）
	KeySwitchStart     = "switch.start"
	KeySwitchNoChange  = "switch.no_change"
	KeySwitchBackend   = "switch.backend"
	KeySwitchMode      = "switch.mode"
	KeySwitchUnit      = "switch.unit"
	KeySwitchDone      = "switch.done"
	KeySwitchNotDebian = "switch.not_debian"

	// 状态：后端与 reserved（M5）
	KeyStatusDaemon     = "status.daemon"
	KeyStatusDaemonNone = "status.daemon_none"
	KeyStatusResvKernel = "status.reserved_kernel_note"

	// 地址族优先级（M6，菜单 s）
	KeyPriorityTitle        = "priority.title"
	KeyPriorityV4           = "priority.mode_v4"
	KeyPriorityV6           = "priority.mode_v6"
	KeyPriorityDefault      = "priority.mode_default"
	KeyPrioritySet          = "priority.set"
	KeyPriorityUnchanged    = "priority.unchanged"
	KeyPriorityV6Caveat     = "priority.v6_caveat"
	KeyPriorityHint         = "priority.hint"
	KeyPriorityOrderV4      = "priority.order_v4"
	KeyPriorityOrderV6      = "priority.order_v6"
	KeyPriorityOrderUnknown = "priority.order_unknown"
	KeyPriorityNoGetent     = "priority.no_getent"
	KeyPriorityNotDebian    = "priority.not_debian"
	KeyPriorityTooMany      = "priority.too_many"

	// WireProxy（M7，菜单 w / y）
	KeyProxyUsage        = "proxy.usage"
	KeyProxyNotDebian    = "proxy.not_debian"
	KeyProxyPortBusy     = "proxy.port_busy"
	KeyProxyBinary       = "proxy.binary_installed"
	KeyProxyFromLocal    = "proxy.from_local"
	KeyProxyVersion      = "proxy.version"
	KeyProxyConfWritten  = "proxy.conf_written"
	KeyProxyConfigOK     = "proxy.config_ok"
	KeyProxyEnabled      = "proxy.enabled"
	KeyProxyStarted      = "proxy.started"
	KeyProxyNoStart      = "proxy.no_start"
	KeyProxyReserved     = "proxy.reserved_warning"
	KeyProxyVerifyRetry  = "proxy.verify_retry"
	KeyProxyOnOK         = "proxy.on_ok"
	KeyProxyUnhealthy    = "proxy.unhealthy"
	KeyProxyInstallDone  = "proxy.install_done"
	KeyProxyNotInstalled = "proxy.not_installed"
	KeyProxyNotRunning   = "proxy.not_running"
	KeyProxyAlreadyOff   = "proxy.already_off"
	KeyProxyOffOK        = "proxy.off_ok"
	KeyProxyKeptBinary   = "proxy.kept_binary"
	KeyProxyUninstalled  = "proxy.uninstalled"
	KeyProxyStatusTitle  = "proxy.status_title"
	KeyUninstallProxy    = "uninstall.proxy_removed"

	// 官方 Linux Client（M8，菜单 c / l / r）
	KeyClientUsage            = "client.usage"
	KeyClientNotDebian        = "client.not_debian"
	KeyClientArch             = "client.arch_unsupported"
	KeyClientAlreadyInstalled = "client.already_installed"
	KeyClientRepoCreated      = "client.repo_created"
	KeyClientRepoReady        = "client.repo_ready"
	KeyClientInstalling       = "client.installing"
	KeyClientInstalled        = "client.installed"
	KeyClientCLIMissing       = "client.cli_missing"
	KeyClientServiceWaiting   = "client.service_waiting"
	KeyClientServiceStarted   = "client.service_started"
	KeyClientServiceDown      = "client.service_down"
	KeyClientRegistering      = "client.registering"
	KeyClientRegistered       = "client.registered"
	KeyClientModeSet          = "client.mode_set"
	KeyClientNoStart          = "client.no_start"
	KeyClientIfaceWaiting     = "client.iface_waiting"
	KeyClientIfaceMissing     = "client.iface_missing"
	KeyClientIfaceUp          = "client.iface_up"
	KeyClientSocksWaiting     = "client.socks_waiting"
	KeyClientSocksMissing     = "client.socks_missing"
	KeyClientSocksOK          = "client.socks_ok"
	KeyClientHooksInstalled   = "client.hooks_installed"
	KeyClientHooksAdded       = "client.hooks_added"
	KeyClientHooksFailed      = "client.hooks_failed"
	KeyClientInstallDone      = "client.install_done"
	KeyClientNotInstalled     = "client.not_installed"
	KeyClientAlreadyOn        = "client.already_on"
	KeyClientAlreadyOff       = "client.already_off"
	KeyClientOnOK             = "client.on_ok"
	KeyClientOffOK            = "client.off_ok"
	KeyClientStatusTitle      = "client.status_title"
	KeyClientUninstallStart   = "client.uninstall_start"
	KeyClientUninstallDone    = "client.uninstall_done"
	KeyClientDropInRemoved    = "client.dropin_removed"
	KeyClientRepoRemoved      = "client.repo_removed"
	KeyClientRepoKept         = "client.repo_kept"
	KeyClientWarpRisk         = "client.warp_risk"
	KeyClientNotOurs          = "client.not_ours"

	// 钩子（M8，菜单 z / x —— 隐藏子命令）
	KeyHookUsage       = "hook.usage"
	KeyHookAdded       = "hook.added"
	KeyHookRemoved     = "hook.removed"
	KeyHookIfaceAbsent = "hook.iface_absent"
	KeyHookNeedRoot    = "hook.need_root"

	// 交互菜单（M8）
	KeyMenuTitle         = "menu.title"
	KeyMenuPrompt        = "menu.prompt"
	KeyMenuQuit          = "menu.quit"
	KeyMenuBye           = "menu.bye"
	KeyMenuBadChoice     = "menu.bad_choice"
	KeyMenuSubFailed     = "menu.sub_failed"
	KeyMenuNoRuntime     = "menu.no_runtime"
	KeyMenuSwitchTo      = "menu.switch_to"
	KeyMenuAskPriority   = "menu.ask_priority"
	KeyMenuUpNonGlobal   = "menu.up_non_global"
	KeyMenuUpGlobal      = "menu.up_global"
	KeyMenuDown          = "menu.down"
	KeyMenuStatus        = "menu.status"
	KeyMenuRotate        = "menu.rotate"
	KeyMenuSwitchBackend = "menu.switch_backend"
	KeyMenuSwitchMode    = "menu.switch_mode"
	KeyMenuPriority      = "menu.priority"
	KeyMenuClientProxy   = "menu.client_proxy"
	KeyMenuClientWarp    = "menu.client_warp"
	KeyMenuClientToggle  = "menu.client_toggle"
	KeyMenuClientStatus  = "menu.client_status"
	KeyMenuProxyInstall  = "menu.proxy_install"
	KeyMenuProxyToggle   = "menu.proxy_toggle"
	KeyMenuInstallWatch  = "menu.install_watch"
	KeyMenuUninstall     = "menu.uninstall"

	// 总卸载的客户端联动（M8）
	KeyUninstallClient = "uninstall.client_removed"

	// 版本与自更新（M9）
	KeyVerCheckFailed   = "version.check_failed"
	KeyVerNoCompare     = "version.no_compare"
	KeyVerNewer         = "version.newer"
	KeyVerUpdateHint    = "version.update_hint"
	KeyVerUpToDate      = "version.up_to_date"
	KeyVerReleasePage   = "version.release_page"
	KeyVerNoSelfPath    = "version.no_self_path"
	KeyVerUpdateStart   = "version.update_start"
	KeyVerUpdateFailed  = "version.update_failed"
	KeyVerNoChange      = "version.no_change"
	KeyVerForceHint     = "version.force_hint"
	KeyVerUpdateDone    = "version.update_done"
	KeyVerNoRestart     = "version.no_restart"
	KeyVerRestartFailed = "version.restart_failed"
	KeyVerRestarted     = "version.restarted"
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
		KeyGuardStale,
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
		KeyBackendKernel,
		KeyBackendUserspace,
		KeyBackendAuto,
		KeyUpUserspaceReserved,
		KeyUpPinnedKernel,
		KeyUpDaemonStarted,
		KeyUpBackendFallback,
		KeyUpBackendUnfit,
		KeyUpBackendUnfitAll,
		KeyUpBackendSwitched,
		KeyUpBackendOK,
		KeyUpNeedReserved,
		KeyUpReasonUnhealthy,
		KeyUpReasonNeedReserved,
		KeyUpReasonLockBusy,
		KeyUpReasonNotDebian,
		KeyUpReasonError,
		KeyRotateStart,
		KeyRotateAttempt,
		KeyRotateSameAddr,
		KeyRotatePicked,
		KeyRotateOldAddr,
		KeyRotateNewAddr,
		KeyRotateOldExit,
		KeyRotateExitSame,
		KeyRotateExitChanged,
		KeyRotateExitUnknown,
		KeyRotateRebuild,
		KeyRotateRestoring,
		KeyRotateRestored,
		KeyRotateFailed,
		KeyRotateOldDevice,
		KeyRotateOldDevFail,
		KeySwitchStart,
		KeySwitchNoChange,
		KeySwitchBackend,
		KeySwitchMode,
		KeySwitchUnit,
		KeySwitchDone,
		KeySwitchNotDebian,
		KeyStatusDaemon,
		KeyStatusDaemonNone,
		KeyStatusResvKernel,
		KeyPriorityTitle,
		KeyPriorityV4,
		KeyPriorityV6,
		KeyPriorityDefault,
		KeyPriorityOrderV4,
		KeyPriorityOrderV6,
		KeyPrioritySet,
		KeyPriorityUnchanged,
		KeyPriorityV6Caveat,
		KeyPriorityHint,

		KeyPriorityOrderUnknown,
		KeyPriorityNoGetent,
		KeyPriorityNotDebian,
		KeyPriorityTooMany,
		KeyProxyUsage,
		KeyProxyNotDebian,
		KeyProxyPortBusy,
		KeyProxyBinary,
		KeyProxyFromLocal,
		KeyProxyVersion,
		KeyProxyConfWritten,
		KeyProxyConfigOK,
		KeyProxyEnabled,
		KeyProxyStarted,
		KeyProxyNoStart,
		KeyProxyReserved,
		KeyProxyVerifyRetry,
		KeyProxyOnOK,
		KeyProxyUnhealthy,
		KeyProxyInstallDone,
		KeyProxyNotInstalled,
		KeyProxyNotRunning,
		KeyProxyAlreadyOff,
		KeyProxyOffOK,
		KeyProxyKeptBinary,
		KeyProxyUninstalled,
		KeyProxyStatusTitle,
		KeyUninstallProxy,
		KeyClientUsage,
		KeyClientNotDebian,
		KeyClientArch,
		KeyClientAlreadyInstalled,
		KeyClientRepoCreated,
		KeyClientRepoReady,
		KeyClientInstalling,
		KeyClientInstalled,
		KeyClientCLIMissing,
		KeyClientServiceWaiting,
		KeyClientServiceStarted,
		KeyClientServiceDown,
		KeyClientRegistering,
		KeyClientRegistered,
		KeyClientModeSet,
		KeyClientNoStart,
		KeyClientIfaceWaiting,
		KeyClientIfaceMissing,
		KeyClientIfaceUp,
		KeyClientSocksWaiting,
		KeyClientSocksMissing,
		KeyClientSocksOK,
		KeyClientHooksInstalled,
		KeyClientHooksAdded,
		KeyClientHooksFailed,
		KeyClientInstallDone,
		KeyClientNotInstalled,
		KeyClientAlreadyOn,
		KeyClientAlreadyOff,
		KeyClientOnOK,
		KeyClientOffOK,
		KeyClientStatusTitle,
		KeyClientUninstallStart,
		KeyClientUninstallDone,
		KeyClientDropInRemoved,
		KeyClientRepoRemoved,
		KeyClientRepoKept,
		KeyClientWarpRisk,
		KeyClientNotOurs,
		KeyHookUsage,
		KeyHookAdded,
		KeyHookRemoved,
		KeyHookIfaceAbsent,
		KeyHookNeedRoot,
		KeyMenuTitle,
		KeyMenuPrompt,
		KeyMenuQuit,
		KeyMenuBye,
		KeyMenuBadChoice,
		KeyMenuSubFailed,
		KeyMenuNoRuntime,
		KeyMenuSwitchTo,
		KeyMenuAskPriority,
		KeyMenuUpNonGlobal,
		KeyMenuUpGlobal,
		KeyMenuDown,
		KeyMenuStatus,
		KeyMenuRotate,
		KeyMenuSwitchBackend,
		KeyMenuSwitchMode,
		KeyMenuPriority,
		KeyMenuClientProxy,
		KeyMenuClientWarp,
		KeyMenuClientToggle,
		KeyMenuClientStatus,
		KeyMenuProxyInstall,
		KeyMenuProxyToggle,
		KeyMenuInstallWatch,
		KeyMenuUninstall,
		KeyUninstallClient,
		KeyVerCheckFailed,
		KeyVerNoCompare,
		KeyVerNewer,
		KeyVerUpdateHint,
		KeyVerUpToDate,
		KeyVerReleasePage,
		KeyVerNoSelfPath,
		KeyVerUpdateStart,
		KeyVerUpdateFailed,
		KeyVerNoChange,
		KeyVerForceHint,
		KeyVerUpdateDone,
		KeyVerNoRestart,
		KeyVerRestartFailed,
		KeyVerRestarted,
	}
}
