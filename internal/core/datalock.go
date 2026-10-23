package core

// datalock.go — the single-instance data-dir lock (v1.0.0,
// FEATURE-PLAN §7).
//
// Exactly one process may own a data dir: its scan store,
// probe cache, config, and secrets are all single-writer
// files. The lock is a small file (app.lock) created with
// O_CREATE|O_EXCL holding the owner's PID. A second instance —
// server OR `-scan-once` CLI — refuses to start rather than
// interleave writes with the owner (the CLI maps this to exit
// code 2, "a server is running against this data dir").
//
// Stale locks: the file carries a PID; when the PID is
// verifiably NOT alive, the lock is treated as stale and
// taken over, so a crashed process does not brick the data
// dir. Liveness is probed per platform (lockpid_unix.go:
// signal 0; lockpid_windows.go: OpenProcess +
// GetExitCodeProcess). v1.0.0–v1.1.0's Windows probe was a
// stub that always answered "alive", so no leftover lock
// could ever be taken over there — fixed in v1.1.1. Where a
// probe genuinely cannot decide (e.g. Windows access denied
// against a protected process), it answers alive — the
// conservative direction: a false refusal prints
// instructions, a false takeover could corrupt two writers'
// stores.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DataLock is a held data-dir lock. Release removes the file.
type DataLock struct {
	path string
}

// DataLockError reports a lock held by another process.
type DataLockError struct {
	Path string
	PID  int
}

func (e *DataLockError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("data dir is locked by another WellKeptLib process (pid %d, lock %s) — stop it first, or delete the lock file if that process is really gone", e.PID, e.Path)
	}
	return fmt.Sprintf("data dir is locked by another WellKeptLib process (lock %s) — stop it first, or delete the lock file if that process is really gone", e.Path)
}

// processAliveFn is the injectable PID-liveness probe behind
// stale-lock takeover, mirroring createHardlinkFn in
// linkfile.go: the production value is the platform
// processAlive (lockpid_unix.go / lockpid_windows.go); tests
// replace it (saving and restoring the original, never in
// parallel) to pin the takeover/refusal matrix on any
// platform — including the dead-PID incident case, which the
// real probe can otherwise only reproduce with a genuinely
// dead PID (datalock_test.go).
var processAliveFn = processAlive

// AcquireDataLock takes the data dir's lock, taking over a
// verifiably stale one.
func AcquireDataLock(dataDir string) (*DataLock, error) {
	path := filepath.Join(dataDir, "app.lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return &DataLock{path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// Someone holds it. Read the PID; steal only when the
		// owner is verifiably dead.
		pid := 0
		if b, rerr := os.ReadFile(path); rerr == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
				pid = n
			}
		}
		if pid > 0 && pid != os.Getpid() && !processAliveFn(pid) {
			_ = os.Remove(path)
			continue // retry the exclusive create
		}
		return nil, &DataLockError{Path: path, PID: pid}
	}
	return nil, &DataLockError{Path: path}
}

// Release removes the lock file (idempotent).
func (l *DataLock) Release() {
	if l != nil && l.path != "" {
		_ = os.Remove(l.path)
		l.path = ""
	}
}
