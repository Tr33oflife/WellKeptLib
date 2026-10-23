//go:build !linux && !windows

package core

// fileid_fallback.go — platforms with no Stat_t route used by
// this project (v1.0.0). Identity is honestly "unknown": the
// duplicates math falls back to per-file arithmetic and labels
// it, rather than guessing an identity it cannot prove.

import "os"

// fileIdentity reports unknown on this platform.
func fileIdentity(info os.FileInfo) (dev, ino, nlink uint64, ok bool) {
	return 0, 0, 0, false
}
