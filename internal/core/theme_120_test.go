package core

// theme_120_test.go — the v1.2.0 theme preference: a "theme"
// key in config.json naming one of the page's palettes
// (themes.go). It is a PREFERENCE (like WantedLanguages), not
// tuning: ResetTuning must never touch it. An absent key means
// the default ("midnight", the original palette); an unknown
// stored value falls back to the default WITHOUT error — a
// config written by a newer build must still open here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigJSON(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// TestThemeDefaultAndLoadMerge: absent key -> midnight; a known
// id is honored; an unknown id falls back to midnight cleanly.
func TestThemeDefaultAndLoadMerge(t *testing.T) {
	dir := t.TempDir()
	cfg, warn := loadConfig(dir)
	if warn != "" {
		t.Fatalf("fresh dir: unexpected warning %q", warn)
	}
	if cfg.Theme != "midnight" {
		t.Errorf("fresh dir: Theme = %q, want midnight", cfg.Theme)
	}

	dir2 := t.TempDir()
	writeConfigJSON(t, dir2, `{"theme": "ocean"}`)
	cfg, _ = loadConfig(dir2)
	if cfg.Theme != "ocean" {
		t.Errorf("stored ocean: Theme = %q, want ocean", cfg.Theme)
	}

	dir3 := t.TempDir()
	writeConfigJSON(t, dir3, `{"theme": "ultraviolet"}`)
	cfg, warn = loadConfig(dir3)
	if warn != "" {
		t.Errorf("unknown theme: unexpected warning %q (must fall back silently)", warn)
	}
	if cfg.Theme != "midnight" {
		t.Errorf("unknown theme: Theme = %q, want midnight fallback", cfg.Theme)
	}
}

// TestThemeSaveRoundTrip: SaveConfig persists a valid theme to
// config.json, keeps the current theme when the save carries an
// empty one (an older page has no theme field), normalizes an
// unknown one to the default, and the value survives a restart.
func TestThemeSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	cfg.Theme = "plum"
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(plum): %v", err)
	}
	if got := svc.GetConfig().Theme; got != "plum" {
		t.Fatalf("after save: Theme = %q, want plum", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if !strings.Contains(string(b), `"theme": "plum"`) {
		t.Errorf("config.json does not carry the theme:\n%s", b)
	}

	// Empty = "not part of this save": the choice is kept.
	cfg = svc.GetConfig()
	cfg.Theme = ""
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(empty theme): %v", err)
	}
	if got := svc.GetConfig().Theme; got != "plum" {
		t.Errorf("empty-theme save: Theme = %q, want plum (kept)", got)
	}

	// Unknown submitted value normalizes to the default, no error.
	cfg = svc.GetConfig()
	cfg.Theme = "bogus"
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(bogus theme): %v", err)
	}
	if got := svc.GetConfig().Theme; got != "midnight" {
		t.Errorf("bogus-theme save: Theme = %q, want midnight", got)
	}

	// Restart: the persisted value (now midnight) loads back.
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (restart): %v", err)
	}
	if got := svc2.GetConfig().Theme; got != "midnight" {
		t.Errorf("after restart: Theme = %q, want midnight", got)
	}
}

// TestThemeSurvivesResetTuning: the theme is a preference, not
// a tuning knob — resetting tuning must leave it exactly as set.
func TestThemeSurvivesResetTuning(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	cfg.Theme = "ember"
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig(ember): %v", err)
	}
	out, err := svc.ResetTuning()
	if err != nil {
		t.Fatalf("ResetTuning: %v", err)
	}
	if out.Theme != "ember" {
		t.Errorf("ResetTuning: Theme = %q, want ember (preferences are not tuning)", out.Theme)
	}
	if got := svc.GetConfig().Theme; got != "ember" {
		t.Errorf("after ResetTuning: Theme = %q, want ember", got)
	}
}

// TestNormalizeTheme pins the fallback table directly.
func TestNormalizeTheme(t *testing.T) {
	for _, id := range themeIDs {
		if got := normalizeTheme(id); got != id {
			t.Errorf("normalizeTheme(%q) = %q, want itself", id, got)
		}
	}
	for _, bad := range []string{"", "Midnight", "MIDNIGHT", " ultraviolet ", "default"} {
		if got := normalizeTheme(bad); got != "midnight" {
			t.Errorf("normalizeTheme(%q) = %q, want midnight", bad, got)
		}
	}
}
