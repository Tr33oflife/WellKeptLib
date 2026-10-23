package core

// fileid_linux.go — storage identity from the walk's os.FileInfo
// (v1.0.0, FEATURE-PLAN §2). On Linux the FileInfo's Sys() is a
// *syscall.Stat_t carrying Dev / Ino / Nlink directly — stdlib
// only, no extra IO: the walk already stats every file.

import (
	"os"
	"syscall"
)

// fileIdentity extracts (device, inode, link count) from a file
// info. ok=false means the platform value is unavailable and
// the identity is honestly "unknown" (zeros on the Item).
func fileIdentity(info os.FileInfo) (dev, ino, nlink uint64, ok bool) {
	if info == nil {
		return 0, 0, 0, false
	}
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), uint64(st.Nlink), st.Ino != 0
}
