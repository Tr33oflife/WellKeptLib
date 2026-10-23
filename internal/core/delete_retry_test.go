package core

// delete_retry_test.go — v0.3.7 hardening: removeFile retries
// once after a chmod when the first remove fails (Windows
// refuses to delete read-only files). POSIX cannot be made to
// fail os.Remove through the file's own mode bits, so the
// test pins the LOGIC at the removeFileFn/chmodFileFn seam:
// failure → chmod 0666 → exactly one retry → the retry's
// outcome is the result.

import (
	"errors"
	"os"
	"testing"
)

func stubRemoveSeams(t *testing.T, remove func(string) error, chmod func(string, os.FileMode) error) {
	t.Helper()
	origR, origC := removeFileFn, chmodFileFn
	removeFileFn, chmodFileFn = remove, chmod
	t.Cleanup(func() { removeFileFn, chmodFileFn = origR, origC })
}

// TestRemoveFileRetriesAfterChmod: first remove fails (the
// Windows read-only refusal), chmod runs with 0666, the retry
// succeeds — overall success, exactly two removes, one chmod.
func TestRemoveFileRetriesAfterChmod(t *testing.T) {
	removes, chmods := 0, 0
	var chmodMode os.FileMode
	stubRemoveSeams(t,
		func(string) error {
			removes++
			if removes == 1 {
				return errors.New("permission denied")
			}
			return nil
		},
		func(_ string, m os.FileMode) error { chmods++; chmodMode = m; return nil })
	if err := removeFile("/x/file.mkv"); err != nil {
		t.Fatalf("removeFile = %v, want nil after the retry", err)
	}
	if removes != 2 || chmods != 1 || chmodMode != 0o666 {
		t.Fatalf("removes=%d chmods=%d mode=%o, want 2/1/0666", removes, chmods, chmodMode)
	}
}

// TestRemoveFileSurfacesSecondError: when the retry also
// fails, the SECOND error is what surfaces (the first failure
// is superseded by the retry's own verdict).
func TestRemoveFileSurfacesSecondError(t *testing.T) {
	errFirst := errors.New("first failure")
	errSecond := errors.New("second failure")
	removes := 0
	stubRemoveSeams(t,
		func(string) error {
			removes++
			if removes == 1 {
				return errFirst
			}
			return errSecond
		},
		func(string, os.FileMode) error { return nil })
	if err := removeFile("/x/file.mkv"); !errors.Is(err, errSecond) {
		t.Fatalf("removeFile = %v, want the second error %v", err, errSecond)
	}
	if removes != 2 {
		t.Fatalf("removes = %d, want exactly 2 (one retry, never more)", removes)
	}
}

// TestRemoveFileNoRetryOnSuccess: a successful first remove
// never touches chmod.
func TestRemoveFileNoRetryOnSuccess(t *testing.T) {
	chmods := 0
	stubRemoveSeams(t,
		func(string) error { return nil },
		func(string, os.FileMode) error { chmods++; return nil })
	if err := removeFile("/x/file.mkv"); err != nil {
		t.Fatalf("removeFile = %v, want nil", err)
	}
	if chmods != 0 {
		t.Fatalf("chmods = %d, want 0 on first-try success", chmods)
	}
}
