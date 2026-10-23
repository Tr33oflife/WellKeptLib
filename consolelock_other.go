//go:build !windows

package main

import "plexfilemanager/internal/core"

// installConsoleLockRelease is a no-op off Windows: there is
// no console window whose close button terminates the
// process without unwinding main, and a lock left behind by
// a killed process is taken over by the stale-lock probe
// (internal/core/datalock.go) once its PID is verifiably
// dead. The Windows implementation (consolelock_windows.go)
// releases the lock from a console control handler, so the
// next start usually finds no leftover at all.
func installConsoleLockRelease(lock *core.DataLock) {}
