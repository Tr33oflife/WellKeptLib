package core

// linkfile_windows.go — hardlink creation on Windows (v1.1.0,
// LINK-LIBRARY-SPEC §10.3), stdlib only: CreateHardLinkW
// through a lazy kernel32 DLL handle, the same pattern the
// Windows file-identity code uses (fileid_windows.go).
//
// ARGUMENT ORDER IS THE REVERSE OF os.Link:
//
//	CreateHardLinkW(
//	    lpFileName         = the NEW target path,
//	    lpExistingFileName = the EXISTING source path,
//	    lpSecurityAttributes = nil)
//
// NTFS hardlinks work only for files on the same volume; a
// target on another drive letter is the ordinary cross-device
// case (ERROR_NOT_SAME_DEVICE), which preview classifies as
// unlinkable from the device comparison before Apply ever runs.

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32ForLink     = syscall.NewLazyDLL("kernel32.dll")
	procCreateHardLinkW = kernel32ForLink.NewProc("CreateHardLinkW")
)

// Windows error codes read by classifyLinkError.
const (
	winErrInvalidFunction  = 1   // ERROR_INVALID_FUNCTION
	winErrNotSameDevice    = 17  // ERROR_NOT_SAME_DEVICE
	winErrNotSupported     = 50  // ERROR_NOT_SUPPORTED
	winErrFileExists       = 80  // ERROR_FILE_EXISTS
	winErrAlreadyExists    = 183 // ERROR_ALREADY_EXISTS
	winErrInvalidParameter = 87  // ERROR_INVALID_PARAMETER
)

// createHardlink creates target as a new hardlink name for the
// existing source file. NOTE the argument order when calling
// CreateHardLinkW: NEW name first, EXISTING file second.
func createHardlink(source, target string) error {
	newName, err := syscall.UTF16PtrFromString(target) // lpFileName: the NEW link name
	if err != nil {
		return err
	}
	existing, err := syscall.UTF16PtrFromString(source) // lpExistingFileName: the file that already exists
	if err != nil {
		return err
	}
	r1, _, callErr := procCreateHardLinkW.Call(
		uintptr(unsafe.Pointer(newName)),
		uintptr(unsafe.Pointer(existing)),
		0, // lpSecurityAttributes = nil
	)
	if r1 == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("CreateHardLinkW failed")
	}
	return nil
}

// classifyLinkError maps a link-creation error to its
// Apply-facing class (LINK-LIBRARY-SPEC §10.4).
func classifyLinkError(err error) linkErrClass {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch uintptr(errno) {
		case winErrNotSameDevice:
			return linkErrCrossDevice
		case winErrFileExists, winErrAlreadyExists:
			return linkErrExists
		case winErrInvalidFunction, winErrNotSupported:
			return linkErrUnsupported
		}
	}
	return linkErrOther
}

// simulatedEXDEVError is the error test doubles return to
// simulate a cross-device link attempt at the seam: the
// ERROR_NOT_SAME_DEVICE errno the Windows classifier reads.
func simulatedEXDEVError() error {
	return syscall.Errno(winErrNotSameDevice)
}

// simulatedEEXISTError is the error test doubles return to
// simulate a taken target name at the seam: the
// ERROR_FILE_EXISTS errno the Windows classifier reads.
func simulatedEEXISTError() error {
	return syscall.Errno(winErrFileExists)
}
