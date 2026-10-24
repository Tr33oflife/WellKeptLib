package core

// kofi_132_test.go — the v1.3.2 Ko-fi username preference: a
// "kofi_username" key in config.json naming the account the
// floating Donate button points at. It is a PREFERENCE (like
// Theme), not tuning: ResetTuning must never touch it. An
// absent key means the default (carried in the public edition by owner exception); an EXPLICIT blank is
// honored as blank, because blank is the disable switch —
// the page must not even attempt the script load. A saved
// handle is trimmed and charset-checked; a bad one fails the
// save naming the field rather than silently pointing the
// button nowhere.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKofiDefaultAndLoadMerge: absent key -> default (carried in public); a
// stored handle is honored; an explicit blank stays blank
// (disabled, not "missing"); surrounding whitespace trims.
func TestKofiDefaultAndLoadMerge(t *testing.T) {
	dir := t.TempDir()
	cfg, warn := loadConfig(dir)
	if warn != "" {
		t.Fatalf("fresh dir: unexpected warning %q", warn)
	}
	if cfg.KofiUsername != defaultKofiUsername {
		t.Errorf("fresh dir: KofiUsername = %q, want default %q", cfg.KofiUsername, defaultKofiUsername)
	}

	dir2 := t.TempDir()
	writeConfigJSON(t, dir2, `{"kofi_username": "someone"}`)
	cfg, _ = loadConfig(dir2)
	if cfg.KofiUsername != "someone" {
		t.Errorf("stored handle: KofiUsername = %q, want someone", cfg.KofiUsername)
	}

	dir3 := t.TempDir()
	writeConfigJSON(t, dir3, `{"kofi_username": ""}`)
	cfg, _ = loadConfig(dir3)
	if cfg.KofiUsername != "" {
		t.Errorf("explicit blank: KofiUsername = %q, want blank (disabled)", cfg.KofiUsername)
	}

	dir4 := t.TempDir()
	writeConfigJSON(t, dir4, `{"kofi_username": "  padded  "}`)
	cfg, _ = loadConfig(dir4)
	if cfg.KofiUsername != "padded" {
		t.Errorf("padded handle: KofiUsername = %q, want padded", cfg.KofiUsername)
	}
}

// TestKofiSaveRoundTrip: SaveConfig persists a handle to
// config.json and it survives a restart; saving a blank
// persists the blank (disabled) rather than restoring the
// default; an invalid handle fails the save and leaves the
// stored value untouched.
func TestKofiSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	cfg.KofiUsername = "someone"
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(someone): %v", err)
	}
	if got := svc.GetConfig().KofiUsername; got != "someone" {
		t.Fatalf("after save: KofiUsername = %q, want someone", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if !strings.Contains(string(b), `"kofi_username": "someone"`) {
		t.Errorf("config.json does not carry the handle:\n%s", b)
	}

	// A restart (fresh service over the same dir) keeps it.
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService reload: %v", err)
	}
	if got := svc2.GetConfig().KofiUsername; got != "someone" {
		t.Errorf("after reload: KofiUsername = %q, want someone", got)
	}

	// Blank is a deliberate disable, not a reset to default.
	cfg = svc2.GetConfig()
	cfg.KofiUsername = ""
	if err := svc2.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(blank): %v", err)
	}
	if got := svc2.GetConfig().KofiUsername; got != "" {
		t.Errorf("after blank save: KofiUsername = %q, want blank", got)
	}
	svc3, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService reload 2: %v", err)
	}
	if got := svc3.GetConfig().KofiUsername; got != "" {
		t.Errorf("after blank reload: KofiUsername = %q, want blank", got)
	}

	// Invalid handles fail and change nothing.
	for _, bad := range []string{"has space", "has/slash", strings.Repeat("x", 65)} {
		cfg = svc3.GetConfig()
		cfg.KofiUsername = bad
		if err := svc3.SaveConfig(cfg); err == nil {
			t.Errorf("SaveConfig(%q): want error, got nil", bad)
		}
		if got := svc3.GetConfig().KofiUsername; got != "" {
			t.Errorf("after failed save %q: KofiUsername = %q, want still blank", bad, got)
		}
	}
}

// TestKofiToggleDefaultAndLoadMerge: the toggle defaults ON
// (the online-conditional behavior ships out of the box); an
// older config without the key stays ON; an explicit false
// is honored (OFF), not read as "missing".
func TestKofiToggleDefaultAndLoadMerge(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := loadConfig(dir)
	if !cfg.KofiEnabled {
		t.Errorf("fresh dir: KofiEnabled = false, want true (default ON)")
	}

	dir2 := t.TempDir()
	writeConfigJSON(t, dir2, `{"kofi_enabled": false}`)
	cfg, _ = loadConfig(dir2)
	if cfg.KofiEnabled {
		t.Errorf("explicit false: KofiEnabled = true, want false")
	}

	dir3 := t.TempDir()
	writeConfigJSON(t, dir3, `{"kofi_enabled": true}`)
	cfg, _ = loadConfig(dir3)
	if !cfg.KofiEnabled {
		t.Errorf("explicit true: KofiEnabled = false, want true")
	}

	dir4 := t.TempDir()
	writeConfigJSON(t, dir4, `{"kofi_username": "someone"}`)
	cfg, _ = loadConfig(dir4)
	if !cfg.KofiEnabled {
		t.Errorf("no toggle key: KofiEnabled = false, want true (default ON)")
	}
}

// TestKofiToggleSaveRoundTrip: OFF persists to config.json
// and survives a restart; ON persists the same way.
func TestKofiToggleSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	cfg.KofiEnabled = false
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(OFF): %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if !strings.Contains(string(b), `"kofi_enabled": false`) {
		t.Errorf("config.json does not carry the OFF toggle:\n%s", b)
	}
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService reload: %v", err)
	}
	if svc2.GetConfig().KofiEnabled {
		t.Errorf("after reload: KofiEnabled = true, want false")
	}
	cfg = svc2.GetConfig()
	cfg.KofiEnabled = true
	if err := svc2.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(ON): %v", err)
	}
	if !svc2.GetConfig().KofiEnabled {
		t.Errorf("after ON save: KofiEnabled = false, want true")
	}
}

// TestKofiSurvivesResetTuning: the username is a preference,
// not tuning — ResetTuning leaves it exactly alone (set,
// blank, and default alike).
func TestKofiSurvivesResetTuning(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	cfg.KofiUsername = "someone"
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	out, err := svc.ResetTuning()
	if err != nil {
		t.Fatalf("ResetTuning: %v", err)
	}
	if out.KofiUsername != "someone" {
		t.Errorf("after reset: KofiUsername = %q, want someone", out.KofiUsername)
	}
	cfg = svc.GetConfig()
	cfg.KofiUsername = ""
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig blank: %v", err)
	}
	out, err = svc.ResetTuning()
	if err != nil {
		t.Fatalf("ResetTuning 2: %v", err)
	}
	if out.KofiUsername != "" {
		t.Errorf("after reset 2: KofiUsername = %q, want blank", out.KofiUsername)
	}
	// The toggle is a preference too: OFF survives a reset.
	cfg = svc.GetConfig()
	cfg.KofiEnabled = false
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig OFF: %v", err)
	}
	out, err = svc.ResetTuning()
	if err != nil {
		t.Fatalf("ResetTuning 3: %v", err)
	}
	if out.KofiEnabled {
		t.Errorf("after reset 3: KofiEnabled = true, want false")
	}
}
