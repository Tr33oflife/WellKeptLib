package core

// fileid_windows.go — storage identity on Windows (v1.0.0,
// FEATURE-PLAN §2), stdlib only.
//
// Windows has no inode in os.FileInfo, so identity comes from
// GetFileInformationByHandle via a lazy kernel32 DLL handle
// (syscall.NewLazyDLL — the same stdlib mechanism x/sys/windows
// itself wraps; importing x/sys would break the stdlib-only
// rule). The BY_HANDLE_FILE_INFORMATION's volume serial number
// + file index high/low combine into the (Dev, Ino) pair —
// the Windows equivalent of (device, inode): two directory
// entries are the same physical file iff both halves match.
//
// The seam: fileIdentity takes the os.FileInfo only (the walk
// has nothing else in hand), so the Windows path needs the
// file's PATH to open a handle. The walk's fileRef carries it;
// fileIdentityForPath is the real entry point here, and
// fileIdentity(info) alone cannot work — it returns unknown.
// scanner.go calls fileIdentityFor(info, path), which is this
// file's implementation on Windows and a pass-through to
// fileIdentity(info) everywhere else (see fileid_other.go /
// fileid_linux.go for the shared shape).

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL              = syscall.NewLazyDLL("kernel32.dll")
	procGetFileInformationBy = kernel32DLL.NewProc("GetFileInformationByHandle")
)

// byHandleFileInformation mirrors BY_HANDLE_FILE_INFORMATION
// (only the identity fields are read, but the layout must be
// complete for the call to fill it correctly).
type byHandleFileInformation struct {
	FileAttributes     uint32
	CreationTime       syscall.Filetime
	LastAccessTime     syscall.Filetime
	LastWriteTime      syscall.Filetime
	VolumeSerialNumber uint32
	FileSizeHigh       uint32
	FileSizeLow        uint32
	NumberOfLinks      uint32
	FileIndexHigh      uint32
	FileIndexLow       uint32
}

// fileIdentity cannot produce an identity from FileInfo alone
// on Windows (see the file comment) — honestly unknown.
func fileIdentity(info os.FileInfo) (dev, ino, nlink uint64, ok bool) {
	return 0, 0, 0, false
}

// fileIdentityFor is the scanner's entry point: it opens the
// file briefly to read its handle identity. A failure of any
// step (open, call) is "unknown", never an error — the scan
// must not care that identity was unavailable.
func fileIdentityFor(info os.FileInfo, path string) (dev, ino, nlink uint64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, false
	}
	defer f.Close()
	var bhfi byHandleFileInformation
	r1, _, _ := procGetFileInformationBy.Call(f.Fd(), uintptr(unsafe.Pointer(&bhfi)))
	if r1 == 0 {
		return 0, 0, 0, false
	}
	ino = uint64(bhfi.FileIndexHigh)<<32 | uint64(bhfi.FileIndexLow)
	if ino == 0 {
		return 0, 0, 0, false
	}
	return uint64(bhfi.VolumeSerialNumber), ino, uint64(bhfi.NumberOfLinks), true
}
