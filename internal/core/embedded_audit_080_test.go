package core

// embedded_audit_080_test.go — the v0.8.0 embedded-audit switch:
//
//   - EmbeddedAudit defaults ON (every older build's behavior),
//     an older config.json without the key loads ON, and an
//     explicitly saved OFF is honored (the bool merge needs key
//     presence, not the numeric fields' non-zero rule);
//   - the switch persists through SaveConfig + a restart, and
//     ResetTuning restores it to ON while roots survive;
//   - a scan with the audit OFF creates no audit jobs: the
//     extractor is invoked ZERO times (counting stub, the
//     v0.3.4 pattern), AuditPending stays false, AuditSkipped is
//     set, and only external findings are published — cached
//     embedded findings from an earlier audit-on scan are NOT
//     republished;
//   - toggling back ON re-audits from the existing per-file
//     cache: unchanged files are not re-extracted, and the
//     embedded findings return.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// frenchStub installs the counting extractor stub (French text,
// matching the fixture corpus's French track tagged eng) and
// returns the call counter + a restore function.
func frenchStub(t *testing.T) (*int, func()) {
	t.Helper()
	calls := new(int)
	orig := extractEmbeddedSubtitleFn
	extractEmbeddedSubtitleFn = func(ctx context.Context, ffmpegPath, mediaPath string, streamIndex, sampleSec, timeoutSec int) ([]string, error) {
		*calls++
		return []string{
			"Bonjour mon ami, ceci est une histoire de la famille et de la maison",
			"Le chat et le chien sont dans le jardin avec les enfants de la ville",
			"Nous allons au marche le matin pour acheter du pain et du fromage",
		}, nil
	}
	return calls, func() { extractEmbeddedSubtitleFn = orig }
}

// findingsBySource counts the service's published subtitle
// findings by source ("external" / "embedded").
func findingsBySource(svc *Service) (external, embedded int) {
	for _, f := range svc.Subtitles() {
		switch f.Source {
		case "external":
			external++
		case "embedded":
			embedded++
		}
	}
	return external, embedded
}

// TestEmbeddedAuditLoadMerge: the default is ON, a config
// written before the field existed (no key) loads ON, and an
// explicit saved false/true is honored exactly.
func TestEmbeddedAuditLoadMerge(t *testing.T) {
	if !defaultConfig().EmbeddedAudit {
		t.Fatal("defaultConfig().EmbeddedAudit = false, want true (the pre-v0.8.0 behavior)")
	}
	dir := t.TempDir()
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Older build's config: no embedded_audit key at all.
	write(`{"workers": 9}`)
	cfg, warn := loadConfig(dir)
	if warn != "" {
		t.Fatalf("warning = %q, want empty for a valid config", warn)
	}
	if !cfg.EmbeddedAudit {
		t.Error("config without the key: EmbeddedAudit = false, want true (absent = default ON)")
	}
	// Explicitly saved OFF must survive the merge.
	write(`{"embedded_audit": false}`)
	cfg, _ = loadConfig(dir)
	if cfg.EmbeddedAudit {
		t.Error("config with embedded_audit:false: EmbeddedAudit = true, want false (saved OFF honored)")
	}
	// Explicitly saved ON, for completeness.
	write(`{"embedded_audit": true}`)
	cfg, _ = loadConfig(dir)
	if !cfg.EmbeddedAudit {
		t.Error("config with embedded_audit:true: EmbeddedAudit = false, want true")
	}
}

// TestEmbeddedAuditPersistAndReset: the switch survives a
// SaveConfig + restart round trip, and ResetTuning restores it
// to ON (persisted) without touching roots.
func TestEmbeddedAuditPersistAndReset(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	cfg.EmbeddedAudit = false
	cfg.Roots = []string{root}
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"embedded_audit": false`) {
		t.Errorf("config.json does not carry the saved switch:\n%s", raw)
	}
	// Restart: the OFF must come back.
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (restart): %v", err)
	}
	if svc2.GetConfig().EmbeddedAudit {
		t.Fatal("after restart: EmbeddedAudit = true, want false (persisted OFF)")
	}
	if st := svc2.State(); st.EmbeddedAudit {
		t.Error("State().EmbeddedAudit = true, want false (mirrors the saved switch)")
	}
	// Reset tuning: switch back ON, root untouched, persisted.
	out, err := svc2.ResetTuning()
	if err != nil {
		t.Fatalf("ResetTuning: %v", err)
	}
	if !out.EmbeddedAudit {
		t.Error("ResetTuning: EmbeddedAudit = false, want true (reset restores ON)")
	}
	if len(out.Roots) != 1 || out.Roots[0] != root {
		t.Errorf("ResetTuning: Roots = %v, want [%s] (setup survives)", out.Roots, root)
	}
	svc3, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (post-reset): %v", err)
	}
	if !svc3.GetConfig().EmbeddedAudit {
		t.Error("after reset + restart: EmbeddedAudit = false, want true (reset persisted)")
	}
}

// TestScanEmbeddedAuditOffColdCache: with the switch OFF from
// the start, a scan of the fixture library invokes the
// extractor ZERO times, publishes the external finding only,
// and lands with AuditPending false / AuditSkipped true.
func TestScanEmbeddedAuditOffColdCache(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.cfg.EmbeddedAudit = false
	calls, restore := frenchStub(t)
	defer restore()

	runScanToCompletion(t, svc, []string{lib})
	if *calls != 0 {
		t.Errorf("extractor calls = %d, want 0 (audit off creates no audit jobs)", *calls)
	}
	st := svc.State()
	if st.Scan.AuditPending {
		t.Error("AuditPending = true after an audit-off scan, want false")
	}
	if !st.Scan.AuditSkipped {
		t.Error("AuditSkipped = false after an audit-off scan, want true")
	}
	external, embedded := findingsBySource(svc)
	if external != 1 || embedded != 0 {
		t.Errorf("findings = %d external, %d embedded; want 1 + 0 (external only)", external, embedded)
	}
	if got := st.Scan.Summary.SubtitleFindings; got != 1 {
		t.Errorf("summary subtitle findings = %d, want 1 (external only)", got)
	}
}

// TestScanEmbeddedAuditToggleCycle: ON scan banks the audit in
// the cache; an OFF scan extracts nothing new and publishes
// external findings only (the cached embedded finding is NOT
// republished); toggling back ON republishes the embedded
// finding from cache with ZERO further extractions.
func TestScanEmbeddedAuditToggleCycle(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	calls, restore := frenchStub(t)
	defer restore()

	// Scan 1 (ON, the default): one extraction, both findings.
	runScanToCompletion(t, svc, []string{lib})
	if *calls != 1 {
		t.Fatalf("scan 1 (on) extractor calls = %d, want 1", *calls)
	}
	external, embedded := findingsBySource(svc)
	if external != 1 || embedded != 1 {
		t.Fatalf("scan 1 (on) findings = %d external, %d embedded; want 1 + 1", external, embedded)
	}
	if svc.State().Scan.AuditSkipped {
		t.Error("scan 1 (on): AuditSkipped = true, want false")
	}

	// Scan 2 (OFF): no new extractions, external only.
	svc.cfg.EmbeddedAudit = false
	runScanToCompletion(t, svc, []string{lib})
	if *calls != 1 {
		t.Errorf("scan 2 (off) extractor calls = %d total, want still 1 (zero new)", *calls)
	}
	external, embedded = findingsBySource(svc)
	if external != 1 || embedded != 0 {
		t.Errorf("scan 2 (off) findings = %d external, %d embedded; want 1 + 0 (cached embedded NOT republished)", external, embedded)
	}
	st := svc.State()
	if st.Scan.AuditPending {
		t.Error("scan 2 (off): AuditPending = true, want false")
	}
	if !st.Scan.AuditSkipped {
		t.Error("scan 2 (off): AuditSkipped = false, want true")
	}

	// Scan 3 (ON again): the audit re-runs from cache — still
	// no new extractions — and the embedded finding returns.
	svc.cfg.EmbeddedAudit = true
	runScanToCompletion(t, svc, []string{lib})
	if *calls != 1 {
		t.Errorf("scan 3 (on again) extractor calls = %d total, want still 1 (cache reuse)", *calls)
	}
	external, embedded = findingsBySource(svc)
	if external != 1 || embedded != 1 {
		t.Errorf("scan 3 (on again) findings = %d external, %d embedded; want 1 + 1", external, embedded)
	}
	if svc.State().Scan.AuditSkipped {
		t.Error("scan 3 (on again): AuditSkipped = true, want false")
	}
}
