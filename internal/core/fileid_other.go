//go:build !windows

package core

// fileid_other.go — the non-Windows half of the scanner's
// identity entry point (v1.0.0). On Linux the identity rides the
// walk's os.FileInfo (fileid_linux.go), so no path is needed;
// on any other Unix the FileInfo route is attempted the same
// way and honestly reports unknown where Stat_t is not the
// backing type. The Windows implementation (fileid_windows.go)
// defines its own fileIdentityFor and is excluded by the build
// tag above.

import "os"

// fileIdentityFor returns the identity for the file described
// by info (path is accepted for signature parity with Windows
// and ignored here — the walk already paid for the stat).
func fileIdentityFor(info os.FileInfo, path string) (dev, ino, nlink uint64, ok bool) {
	return fileIdentity(info)
}
