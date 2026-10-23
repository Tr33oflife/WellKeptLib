package core

// roots_test.go — tests for the v0.1.1 root input hygiene.
// These exist because v0.1.0 silently scanned zero files for any root
// that didn't resolve exactly as typed (the v0.1.0 Windows live test).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanRootInputQuotes: surrounding quotes (Windows "Copy as path"
// pastes "D:\Movies" WITH the quotes) must be stripped.
func TestCleanRootInputQuotes(t *testing.T) {
	dir := t.TempDir()
	if got := cleanRootInput(`"` + dir + `"`); got != filepath.Clean(dir) {
		t.Errorf("double-quoted root: got %q want %q", got, filepath.Clean(dir))
	}
	if got := cleanRootInput("  '" + dir + "'  "); got != filepath.Clean(dir) {
		t.Errorf("single-quoted root with spaces: got %q want %q", got, filepath.Clean(dir))
	}
}

// TestCleanRootInputTilde: a leading ~ expands to the home directory.
func TestCleanRootInputTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	want := filepath.Join(home, "media")
	if got := cleanRootInput("~/media"); got != want {
		t.Errorf("tilde root: got %q want %q", got, want)
	}
	if got := cleanRootInput("~"); got != filepath.Clean(home) {
		t.Errorf("bare tilde: got %q want %q", got, filepath.Clean(home))
	}
}

// TestCleanRootInputEnv: $VAR and %VAR% references expand when the
// variable exists; an unknown %NAME% (a literal % in a folder name)
// must survive untouched.
func TestCleanRootInputEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PFM_TEST_ROOT", dir)
	if got := cleanRootInput("$PFM_TEST_ROOT/sub"); got != filepath.Join(dir, "sub") {
		t.Errorf("$VAR root: got %q want %q", got, filepath.Join(dir, "sub"))
	}
	if got := cleanRootInput("%PFM_TEST_ROOT%" + string(os.PathSeparator) + "sub"); got != filepath.Join(dir, "sub") {
		t.Errorf("%%VAR%% root: got %q want %q", got, filepath.Join(dir, "sub"))
	}
	if got := cleanRootInput("100%COVERAGE%"); !strings.Contains(got, "%COVERAGE%") {
		t.Errorf("unknown %%VAR%% must survive: got %q", got)
	}
}

// TestValidateRoots: valid dirs pass (even quoted), missing paths and
// plain files are rejected WITH problem strings, duplicates collapse.
func TestValidateRoots(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist")
	valid, problems := validateRoots([]string{`"` + dir + `"`, dir, missing, file})
	if len(valid) != 1 || valid[0] != filepath.Clean(dir) {
		t.Errorf("valid = %v, want exactly [%s]", valid, filepath.Clean(dir))
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want 2 (missing + file)", problems)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "root not found: "+missing) {
		t.Errorf("missing-root problem absent: %q", joined)
	}
	if !strings.Contains(joined, "root is not a folder: "+file) {
		t.Errorf("file-root problem absent: %q", joined)
	}
}

// TestValidateRootsEmpty: no candidates means no valid roots and no
// problems (StartScan turns that into "no scan roots configured").
func TestValidateRootsEmpty(t *testing.T) {
	valid, problems := validateRoots([]string{"", "   ", `""`})
	if len(valid) != 0 || len(problems) != 0 {
		t.Errorf("valid=%v problems=%v, want both empty", valid, problems)
	}
}
