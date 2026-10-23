package core

// lockpid_windows.go — PID liveness on Windows (v1.0.0;
// real probe v1.1.1), stdlib only: OpenProcess +
// GetExitCodeProcess through lazy kernel32 DLL handles, the
// same pattern the Windows file-identity code uses
// (fileid_windows.go).
//
// v1.0.0–v1.1.0 shipped a stub that always answered "alive"
// (the stdlib exposes no one-line liveness probe, so the
// conservative answer was chosen). Combined with the way a
// double-clicked app is normally closed — the console
// window's X button terminates the process without running
// deferred cleanup, so app.lock survived holding a dead
// PID — no leftover lock could EVER be taken over: every
// later start refused, claiming another copy was running,
// until the user deleted the lock file by hand. The probe
// below decides for real:
//
//   - OpenProcess fails with ERROR_INVALID_PARAMETER (87):
//     no process with that PID exists — verifiably DEAD.
//   - OpenProcess succeeds: GetExitCodeProcess reports
//     STILL_ACTIVE (259) for a running process; any other
//     exit code means it has finished — DEAD.
//   - Any other failure (access denied against a protected
//     process, the exit-code call itself failing): liveness
//     is genuinely unknown, and the answer stays
//     conservatively ALIVE (see datalock.go's stale-lock
//     note — a false refusal prints instructions, a false
//     takeover could corrupt two writers' stores).

import (
	"syscall"
	"unsafe"
)

var (
	kernel32ForLock     = syscall.NewLazyDLL("kernel32.dll")
	procLockOpenProcess = kernel32ForLock.NewProc("OpenProcess")
	procLockGetExitCode = kernel32ForLock.NewProc("GetExitCodeProcess")
	procLockCloseHandle = kernel32ForLock.NewProc("CloseHandle")
)

// Windows constants read by processAlive.
const (
	// PROCESS_QUERY_LIMITED_INFORMATION: enough access for
	// GetExitCodeProcess; grantable for almost any process.
	processQueryLimitedInformation = 0x1000
	// STILL_ACTIVE: the exit code of a process still running.
	stillActive = 259
)

// processAlive reports whether a process with the given PID
// currently exists and is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, _, callErr := procLockOpenProcess.Call(
		processQueryLimitedInformation, // dwDesiredAccess
		0,                              // bInheritHandle = FALSE
		uintptr(uint32(pid)),           // dwProcessId
	)
	if h == 0 {
		// winErrInvalidParameter (ERROR_INVALID_PARAMETER,
		// declared in linkfile_windows.go): the PID names no
		// process — verifiably dead. Any other error leaves
		// liveness undecidable — conservatively alive.
		if errno, ok := callErr.(syscall.Errno); ok && uintptr(errno) == winErrInvalidParameter {
			return false
		}
		return true
	}
	defer procLockCloseHandle.Call(h)
	var exitCode uint32
	r1, _, _ := procLockGetExitCode.Call(h, uintptr(unsafe.Pointer(&exitCode)))
	if r1 == 0 {
		return true // undecidable — conservatively alive
	}
	return exitCode == stillActive
}
