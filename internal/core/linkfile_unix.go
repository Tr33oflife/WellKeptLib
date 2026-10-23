//go:build !windows

package core

// linkfile_unix.go — hardlink creation on POSIX platforms
// (v1.1.0, LINK-LIBRARY-SPEC §10.2): plain os.Link. Argument
// order is (existing source, new target).

import (
	"errors"
	"os"
	"syscall"
)

// createHardlink creates target as a new hardlink name for the
// existing source file.
func createHardlink(source, target string) error {
	return os.Link(source, target)
}

// classifyLinkError maps a link-creation error to its
// Apply-facing class (LINK-LIBRARY-SPEC §10.4). os.Link wraps
// errno values in *os.LinkError; errors.Is unwraps it.
func classifyLinkError(err error) linkErrClass {
	switch {
	case errors.Is(err, syscall.EXDEV):
		return linkErrCrossDevice
	case errors.Is(err, syscall.EEXIST):
		return linkErrExists
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.ENOSYS), errors.Is(err, syscall.EOPNOTSUPP):
		return linkErrUnsupported
	}
	return linkErrOther
}

// simulatedEXDEVError is the error test doubles return to
// simulate a cross-device link attempt at the seam: a real
// *os.LinkError wrapping syscall.EXDEV, shaped exactly like
// the error os.Link itself produces.
func simulatedEXDEVError() error {
	return &os.LinkError{Op: "link", Old: "source", New: "target", Err: syscall.EXDEV}
}

// simulatedEEXISTError is the error test doubles return to
// simulate a taken target name at the seam: a real
// *os.LinkError wrapping syscall.EEXIST, shaped exactly like
// the error os.Link itself produces.
func simulatedEEXISTError() error {
	return &os.LinkError{Op: "link", Old: "source", New: "target", Err: syscall.EEXIST}
}
