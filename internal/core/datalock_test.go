package core

// datalock_test.go — the stale-lock takeover matrix at the
// processAliveFn seam (v1.1.1). The pre-existing
// TestDataLockRefusalAndStaleTakeover exercises the REAL
// platform probe (a genuinely dead PID); these tests stub
// the probe so every cell of the matrix pins on any
// platform — above all the incident case behind the hotfix:
// on Windows, v1.0.0–v1.1.0's probe always answered "alive",
// so a lock left behind by a closed window could never be
// taken over and the app refused to start forever. Here the
// probe answers "dead" exactly as the fixed Windows probe
// does for a PID that no longer exists, and the takeover
// must succeed.
//
// Seam discipline mirrors the link tests: save and restore
// processAliveFn, never t.Parallel.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stubProcessAlive replaces the liveness probe for the
// duration of a test.
func stubProcessAlive(t *testing.T, alive bool) {
	t.Helper()
	orig := processAliveFn
	processAliveFn = func(pid int) bool { return alive }
	t.Cleanup(func() { processAliveFn = orig })
}

// writeLockFile plants an app.lock with the given contents.
func writeLockFile(t *testing.T, dir, contents string) string {
	t.Helper()
	p := filepath.Join(dir, "app.lock")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// lockError asserts err is a *DataLockError and returns it.
func lockError(t *testing.T, err error) *DataLockError {
	t.Helper()
	if err == nil {
		t.Fatal("acquire succeeded — the lock must refuse")
	}
	var dle *DataLockError
	if !errors.As(err, &dle) {
		t.Fatalf("refusal error type = %T, want *DataLockError (%v)", err, err)
	}
	return dle
}

// The incident reproduction: a lock naming a DEAD pid must
// not block startup — it is taken over, and the lock becomes
// ours.
func TestDataLockSeamDeadPIDTakeover(t *testing.T) {
	stubProcessAlive(t, false)
	dir := t.TempDir()
	p := writeLockFile(t, dir, "424242\n")
	l, err := AcquireDataLock(dir)
	if err != nil {
		t.Fatalf("lock naming a dead pid blocked startup: %v", err)
	}
	defer l.Release()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("taken-over lock names pid %q, want our pid %d", got, os.Getpid())
	}
}

// A lock naming a LIVE pid refuses, with the owner's PID in
// the error, and the lock file is left untouched.
func TestDataLockSeamLivePIDRefusal(t *testing.T) {
	stubProcessAlive(t, true)
	dir := t.TempDir()
	p := writeLockFile(t, dir, "424242\n")
	_, err := AcquireDataLock(dir)
	if dle := lockError(t, err); dle.PID != 424242 {
		t.Errorf("refusal PID = %d, want 424242", dle.PID)
	}
	b, rerr := os.ReadFile(p)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if got := strings.TrimSpace(string(b)); got != "424242" {
		t.Errorf("live lock file changed to %q, want untouched 424242", got)
	}
}

// Garbage contents: the PID never parses (pid 0), the probe
// is never consulted, and the lock refuses — the behavior
// the app has always had for a corrupt lock file.
func TestDataLockSeamGarbageContentRefused(t *testing.T) {
	stubProcessAlive(t, false)
	dir := t.TempDir()
	writeLockFile(t, dir, "not-a-pid\n")
	_, err := AcquireDataLock(dir)
	if dle := lockError(t, err); dle.PID != 0 {
		t.Errorf("refusal PID = %d, want 0 for garbage contents", dle.PID)
	}
}

// An empty lock file is the same corrupt case: pid 0, refuse.
func TestDataLockSeamEmptyContentRefused(t *testing.T) {
	stubProcessAlive(t, false)
	dir := t.TempDir()
	writeLockFile(t, dir, "")
	_, err := AcquireDataLock(dir)
	if dle := lockError(t, err); dle.PID != 0 {
		t.Errorf("refusal PID = %d, want 0 for empty contents", dle.PID)
	}
}

// A lock naming OUR OWN pid refuses even when the probe
// (wrongly) reports it dead: the takeover guard never steals
// from the current process itself.
func TestDataLockSeamOwnPIDRefused(t *testing.T) {
	stubProcessAlive(t, false)
	dir := t.TempDir()
	writeLockFile(t, dir, strconv.Itoa(os.Getpid())+"\n")
	_, err := AcquireDataLock(dir)
	if dle := lockError(t, err); dle.PID != os.Getpid() {
		t.Errorf("refusal PID = %d, want our own pid %d", dle.PID, os.Getpid())
	}
}
