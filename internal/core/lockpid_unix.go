//go:build !windows

package core

// lockpid_unix.go — PID liveness for stale-lock takeover
// (v1.0.0). Signal 0 probes existence without delivering a
// signal: ESRCH = dead, EPERM = alive but not ours.

import "syscall"

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
