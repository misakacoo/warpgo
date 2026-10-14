package i18n

// en 英文文案。
var en = map[string]string{
	KeyUsage: `warpgo -- Cloudflare WARP manager for Debian
(a Go rewrite of fscarmen/warp; Debian only, no Docker, no other platforms)

Usage:
  warpgo <command> [options]

Commands:
  version              print version information
  help                 print this help
  dev lock             exercise the single-instance lock (development helper)

Global options:
  --lang en|zh         force output language (default: inferred from LANG)
  -v, --verbose        debug logging
  -q, --quiet          warnings and errors only

Planned commands (implemented milestone by milestone):
  register  up  down  status  watch  rotate  switch  priority
  split  proxy  client  bbr  uninstall  version --check

Exit codes:
  0 ok   1 general error   2 unhealthy   3 reserved unsupported
  4 another instance is running   5 not a Debian system`,

	KeyVersion:   "warpgo",
	KeyUnknownCm: "unknown command: %s",
	KeyBadLang:   "unsupported language: %q (expected en or zh)",
	KeyNeedArg:   "option %s requires a value",
	KeyBadOpt:    "unknown option: %s",

	KeyLockBusy:            "another warpgo instance (PID %d) is working on the WARP link; this invocation does nothing and exits",
	KeyLockAcquired:        "acquired lock %s",
	KeyLockReleased:        "released lock %s",
	KeyLockTakeoverStale:   "previous instance (PID %d) stopped refreshing its heartbeat %s ago; taking the lock over",
	KeyLockTakeoverLegacy:  "found a leftover lock from an older version (PID %d, no heartbeat field); taking the lock over",
	KeyLockTakeoverUnverif: "stale lock holder PID %d cannot be confirmed to be warpgo; taking the lock over without killing it",
	KeyLockLeftover:        "found a leftover lock from PID %d which is no longer running (previous run probably crashed); taking the lock over",
	KeyLockKillFailed:      "could not terminate stale lock holder PID %d: %v",
	KeyLockKilled:          "terminated stale lock holder PID %d",
	KeyNotImplemented:      "not implemented yet (planned for milestone %s)",
	KeyLockTouch:           "heartbeat refreshed (held by PID %d)",

	KeyPlatformDebian:   "system check: Debian %s",
	KeyPlatformNotDebia: "this command only supports Debian (detected: %s)",
}
