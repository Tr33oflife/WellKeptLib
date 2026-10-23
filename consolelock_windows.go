package main

// consolelock_windows.go — release the data-dir lock when
// the console window closes (v1.1.1), stdlib only:
// SetConsoleCtrlHandler through a lazy kernel32 DLL handle,
// the codebase's established Windows pattern (see
// internal/core/fileid_windows.go).
//
// The bug this backs up: a double-clicked app is normally
// closed with the console window's X button, which makes
// Windows TERMINATE the process — main never returns, so the
// deferred lock release in main.go never runs and app.lock
// survives holding a dead PID. v1.1.1's real liveness probe
// (internal/core/lockpid_windows.go) already makes such a
// leftover harmless (the next start takes it over), but
// releasing the lock on the way out means the next start
// usually finds nothing left behind at all.
//
// The handler runs on a thread Windows creates for the
// event; it only calls DataLock.Release (an idempotent
// os.Remove of our own lock file) and then returns FALSE so
// the default termination proceeds exactly as it would
// without a handler.

import (
	"syscall"

	"plexfilemanager/internal/core"
)

var (
	kernel32ForConsole        = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCtrlHandler = kernel32ForConsole.NewProc("SetConsoleCtrlHandler")
)

// Console control events that end the process without
// unwinding main (WinCon.h).
const (
	ctrlCloseEvent    = 2 // CTRL_CLOSE_EVENT — window X button / task manager End task
	ctrlLogoffEvent   = 5 // CTRL_LOGOFF_EVENT
	ctrlShutdownEvent = 6 // CTRL_SHUTDOWN_EVENT
)

// consoleLock is the data lock the console handler releases,
// set by installConsoleLockRelease before the server starts.
// The handler reads it from a system thread; it is assigned
// once, before registration, and never changes afterwards.
var consoleLock *core.DataLock

// consoleCtrlHandler is the registered HandlerRoutine. For
// the close/logoff/shutdown events it releases the data
// lock; it always returns FALSE (0 — "not handled"), so any
// remaining handlers and the system's default termination
// proceed unchanged.
func consoleCtrlHandler(ctrlType uint32) uintptr {
	switch ctrlType {
	case ctrlCloseEvent, ctrlLogoffEvent, ctrlShutdownEvent:
		if consoleLock != nil {
			consoleLock.Release()
		}
	}
	return 0
}

// consoleCtrlCallback keeps the handler's callback
// trampoline reachable for the process lifetime — a
// NewCallback value that is garbage-collected would leave
// the registered handler pointing at freed code.
var consoleCtrlCallback = syscall.NewCallback(consoleCtrlHandler)

// installConsoleLockRelease registers consoleCtrlHandler for
// console control events. Best-effort by design: if
// registration fails, the app runs exactly as v1.1.0 did,
// and the stale-lock takeover covers any leftover lock file.
func installConsoleLockRelease(lock *core.DataLock) {
	consoleLock = lock
	procSetConsoleCtrlHandler.Call(consoleCtrlCallback, 1) // Add = TRUE
}
