package core

// lockpid_windows.go — PID liveness on Windows (v1.0.0). The
// stdlib exposes no cheap cross-version liveness probe, so the
// conservative answer applies: an existing lock is treated as
// LIVE (see datalock.go's stale-lock note — the refusal message
// tells the user exactly how to clear a genuinely stale lock).

func processAlive(pid int) bool {
	return true
}
