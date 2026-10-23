package core

// settings_050_test.go — the v0.5.0 Settings pass:
//
//   - the four new tuning fields merge over defaults on load
//     (an older build's config keeps the old literals' behavior)
//     and are clamped to their ranges in load AND save;
//   - corrupt config.json is preserved as config.json.corrupt
//     (suffixing when needed), defaults load, and a warning is
//     exposed via State(); a valid config warns about nothing
//     and is never renamed;
//   - the diagnostics snapshot has the promised shape and can
//     NEVER carry a Plex token (planted fake tokens asserted
//     absent from the serialized output);
//   - ResetTuning restores every tuning knob to default while
//     roots, exclusions, and tool paths survive, persisted;
//   - the audit honors a configured track cap and threads the
//     configured sample/timeout into the extractor, and
//     probeFile honors its timeout parameter.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTuningFieldsMergeAndClampOnLoad: a config written by an
// older build (no v0.5.0 fields) yields exactly the old literals;
// saved values override; out-of-range values clamp to the bounds.
func TestTuningFieldsMergeAndClampOnLoad(t *testing.T) {
	dir := t.TempDir()
	// Older-build config: none of the new fields present.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"workers": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, warn := loadConfig(dir)
	if warn != "" {
		t.Fatalf("warning = %q, want empty for a valid config", warn)
	}
	if cfg.Workers != 9 {
		t.Errorf("Workers = %d, want 9 (saved value kept)", cfg.Workers)
	}
	if cfg.ProbeTimeoutSec != 60 || cfg.SubtitleSampleSec != 120 ||
		cfg.SubtitleExtractTimeoutSec != 45 || cfg.SubtitleMaxTracks != 6 {
		t.Errorf("defaults = %d/%d/%d/%d, want 60/120/45/6 (the pre-v0.5.0 literals)",
			cfg.ProbeTimeoutSec, cfg.SubtitleSampleSec, cfg.SubtitleExtractTimeoutSec, cfg.SubtitleMaxTracks)
	}
	// Saved values override; one out-of-range value clamps high,
	// another clamps low.
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"probe_timeout_sec": 33, "subtitle_sample_sec": 99999, "subtitle_extract_timeout_sec": 2, "subtitle_max_tracks": 12}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig(dir)
	if cfg.ProbeTimeoutSec != 33 {
		t.Errorf("ProbeTimeoutSec = %d, want 33 (saved)", cfg.ProbeTimeoutSec)
	}
	if cfg.SubtitleSampleSec != 900 {
		t.Errorf("SubtitleSampleSec = %d, want 900 (clamped to max)", cfg.SubtitleSampleSec)
	}
	if cfg.SubtitleExtractTimeoutSec != 5 {
		t.Errorf("SubtitleExtractTimeoutSec = %d, want 5 (clamped to min)", cfg.SubtitleExtractTimeoutSec)
	}
	if cfg.SubtitleMaxTracks != 12 {
		t.Errorf("SubtitleMaxTracks = %d, want 12 (saved)", cfg.SubtitleMaxTracks)
	}
}

// TestSaveConfigClampsTuning: the same clamping happens on the
// SaveConfig path, and a zero (unset) value becomes the default.
func TestSaveConfigClampsTuning(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	in := svc.GetConfig()
	in.ProbeTimeoutSec = 0    // unset -> default
	in.SubtitleMaxTracks = 99 // over max -> 20
	if err := svc.SaveConfig(in); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got := svc.GetConfig()
	if got.ProbeTimeoutSec != 60 {
		t.Errorf("ProbeTimeoutSec = %d, want 60 (default for unset)", got.ProbeTimeoutSec)
	}
	if got.SubtitleMaxTracks != 20 {
		t.Errorf("SubtitleMaxTracks = %d, want 20 (clamped)", got.SubtitleMaxTracks)
	}
}

// TestCorruptConfigPreservedAndWarned: an unparseable config.json
// is renamed aside with its bytes intact, defaults are used, the
// warning reaches State(), and a pre-existing .corrupt file
// pushes the new preserve to the -2 suffix.
func TestCorruptConfigPreservedAndWarned(t *testing.T) {
	dir := t.TempDir()
	garbage := []byte("{ this is not json ]")
	if err := os.WriteFile(filepath.Join(dir, "config.json.corrupt"), []byte("earlier preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := svc.GetConfig()
	if cfg.Workers != 4 || cfg.ProbeTimeoutSec != 60 || cfg.SubtitleMaxTracks != 6 {
		t.Errorf("config after corruption = workers %d probeTimeout %d maxTracks %d, want defaults 4/60/6",
			cfg.Workers, cfg.ProbeTimeoutSec, cfg.SubtitleMaxTracks)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Errorf("config.json still present after preserve (stat err = %v)", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json.corrupt-2"))
	if err != nil {
		t.Fatalf("preserved file missing: %v", err)
	}
	if string(b) != string(garbage) {
		t.Errorf("preserved bytes = %q, want the original corrupt bytes %q", b, garbage)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json.corrupt")); string(b) != "earlier preserve" {
		t.Errorf("earlier .corrupt file was clobbered: %q", b)
	}
	if w := svc.State().ConfigWarning; w == "" {
		t.Error("State().ConfigWarning is empty, want the corruption warning")
	} else if !strings.Contains(w, "config.json.corrupt-2") {
		t.Errorf("warning %q does not name the preserved file", w)
	}
	if d := svc.Diagnostics(); d.ConfigWarning == "" {
		t.Error("Diagnostics().ConfigWarning is empty, want the same warning")
	}
}

// TestValidConfigNoWarningNoRename: the control case — a
// parseable config loads its values, produces no warning, and
// leaves every file where it was.
func TestValidConfigNoWarningNoRename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"workers": 7, "probe_timeout_sec": 44}`), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if w := svc.State().ConfigWarning; w != "" {
		t.Errorf("ConfigWarning = %q, want empty for a valid config", w)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json.corrupt")); !os.IsNotExist(err) {
		t.Error("config.json.corrupt exists for a valid config — nothing should be preserved")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("config.json missing after a valid load: %v", err)
	}
	cfg := svc.GetConfig()
	if cfg.Workers != 7 || cfg.ProbeTimeoutSec != 44 {
		t.Errorf("loaded config = workers %d probeTimeout %d, want 7/44", cfg.Workers, cfg.ProbeTimeoutSec)
	}
}

// TestDiagnosticsShapeAndNoToken: the snapshot carries the
// promised fields, and the serialized bytes NEVER contain a
// Plex token — the fixture plex.json plants two fake secrets
// (server token + account token) that must not leak.
func TestDiagnosticsShapeAndNoToken(t *testing.T) {
	dir := t.TempDir()
	const fakeToken = "FAKE-SERVER-TOKEN-050"
	const fakeAccount = "FAKE-ACCOUNT-TOKEN-050"
	plexJSON := `{"url":"http://127.0.0.1:32400","token":"` + fakeToken +
		`","server_name":"Fixture Server","account_token":"` + fakeAccount + `","mappings":[]}`
	if err := os.WriteFile(filepath.Join(dir, "plex.json"), []byte(plexJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Point the tool paths at known files: one real, one absent.
	realTool := filepath.Join(dir, "ffprobe-real")
	if err := os.WriteFile(realTool, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := svc.GetConfig()
	cfg.FFprobePath = realTool
	cfg.FFmpegPath = filepath.Join(dir, "ffmpeg-absent")
	cfg.Roots = []string{dir}
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	d := svc.Diagnostics()
	if d.Version != Version || d.GOOS == "" || d.GOARCH == "" || d.DataDir != dir {
		t.Errorf("identity fields wrong: %+v", d)
	}
	if !d.FFprobeExists || d.FFmpegExists {
		t.Errorf("tool existence = ffprobe %v / ffmpeg %v, want true/false", d.FFprobeExists, d.FFmpegExists)
	}
	if d.RootsCount != 1 || d.ExclusionsCount != 0 {
		t.Errorf("counts = roots %d exclusions %d, want 1/0", d.RootsCount, d.ExclusionsCount)
	}
	if d.Tuning.ProbeTimeoutSec != 60 || d.Tuning.SubtitleMaxTracks != 6 {
		t.Errorf("tuning snapshot wrong: %+v", d.Tuning)
	}
	if !d.PlexConfigured || d.PlexServerName != "Fixture Server" {
		t.Errorf("plex summary = configured %v name %q, want true/\"Fixture Server\"", d.PlexConfigured, d.PlexServerName)
	}
	blob, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	if strings.Contains(string(blob), fakeToken) || strings.Contains(string(blob), fakeAccount) {
		t.Fatalf("diagnostics leaked a Plex token: %s", blob)
	}
	// Shape: the keys a support reader relies on are all present.
	var asMap map[string]any
	if err := json.Unmarshal(blob, &asMap); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "goos", "goarch", "data_dir", "ffprobe_path", "ffprobe_exists",
		"ffmpeg_path", "ffmpeg_exists", "roots_count", "exclusions_count", "tuning",
		"plex_configured", "last_scan_summary", "probe_cache_entries", "organize_manifests"} {
		if _, ok := asMap[k]; !ok {
			t.Errorf("diagnostics missing key %q", k)
		}
	}
}

// TestResetTuningKeepsSetup: every tuning knob returns to its
// default; roots, exclusions, and tool paths are untouched —
// in memory AND in the persisted config a restart would load.
func TestResetTuningKeepsSetup(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	root := filepath.Join(dir, "library")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	in := svc.GetConfig()
	in.Roots = []string{root}
	in.Exclusions = []Exclusion{{Path: filepath.Join(root, "leave-me"), Scope: "all"}}
	in.FFprobePath = "/custom/ffprobe"
	in.FFmpegPath = "/custom/ffmpeg"
	in.Workers = 9
	in.SubtitleConfidence = 0.5
	in.DurationTolerancePct = 9
	in.DurationToleranceSec = 30
	in.TinyFileFloorBytes = 42
	in.ProbeTimeoutSec = 33
	in.SubtitleSampleSec = 55
	in.SubtitleExtractTimeoutSec = 21
	in.SubtitleMaxTracks = 12
	if err := svc.SaveConfig(in); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	out, err := svc.ResetTuning()
	if err != nil {
		t.Fatalf("ResetTuning: %v", err)
	}
	if out.Workers != 4 || out.SubtitleConfidence != 0.75 || out.DurationTolerancePct != 3 ||
		out.DurationToleranceSec != 120 || out.TinyFileFloorBytes != 1024*1024 ||
		out.ProbeTimeoutSec != 60 || out.SubtitleSampleSec != 120 ||
		out.SubtitleExtractTimeoutSec != 45 || out.SubtitleMaxTracks != 6 {
		t.Errorf("tuning after reset is not the defaults: %+v", out)
	}
	if len(out.Roots) != 1 || out.Roots[0] != root {
		t.Errorf("roots changed by reset: %v", out.Roots)
	}
	if len(out.Exclusions) != 1 || out.Exclusions[0].Scope != "all" {
		t.Errorf("exclusions changed by reset: %v", out.Exclusions)
	}
	if out.FFprobePath != "/custom/ffprobe" || out.FFmpegPath != "/custom/ffmpeg" {
		t.Errorf("tool paths changed by reset: %q / %q", out.FFprobePath, out.FFmpegPath)
	}
	// Persisted: a fresh service over the same dir sees the same.
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (reload): %v", err)
	}
	re := svc2.GetConfig()
	if re.ProbeTimeoutSec != 60 || re.Workers != 4 || len(re.Roots) != 1 || re.FFprobePath != "/custom/ffprobe" {
		t.Errorf("persisted config after reset wrong: %+v", re)
	}
}

// TestAuditHonorsConfiguredTrackCapAndThreading: with
// SubtitleMaxTracks = 2 the audit extracts exactly 2 tracks,
// reports the rest as capped with the configured number in the
// note, and the extractor receives the configured
// sample/timeout values.
func TestAuditHonorsConfiguredTrackCapAndThreading(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	pr := &ProbeResult{OK: true, HasVideo: true, VideoCodec: "h264", Width: 1920, Height: 1080}
	for i := 0; i < 5; i++ {
		pr.Streams = append(pr.Streams, StreamInfo{Index: i, Codec: "subrip", Type: "subtitle", Language: "eng"})
	}
	calls := 0
	var gotSample, gotTimeout int
	orig := extractEmbeddedSubtitleFn
	extractEmbeddedSubtitleFn = func(ctx context.Context, ffmpegPath, mediaPath string, streamIndex, sampleSec, timeoutSec int) ([]string, error) {
		calls++
		gotSample, gotTimeout = sampleSec, timeoutSec
		return []string{
			"This is the story of a man who went to the market in the morning",
			"and he bought bread and milk for his family and walked home again",
		}, nil
	}
	defer func() { extractEmbeddedSubtitleFn = orig }()

	cfg := svc.GetConfig()
	cfg.SubtitleMaxTracks = 2
	cfg.SubtitleSampleSec = 77
	cfg.SubtitleExtractTimeoutSec = 33
	findings := auditEmbeddedForItem(context.Background(), &cfg, &Item{Path: "cap.mkv"}, pr)
	if calls != 2 {
		t.Errorf("extractor calls = %d, want 2 (configured cap)", calls)
	}
	if gotSample != 77 || gotTimeout != 33 {
		t.Errorf("extractor received sample/timeout %d/%d, want 77/33 (configured)", gotSample, gotTimeout)
	}
	caps := 0
	for _, f := range findings {
		if strings.Contains(f.Note, "not sampled") {
			caps++
			if !strings.Contains(f.Note, "more than 2") {
				t.Errorf("cap note %q does not carry the configured cap", f.Note)
			}
		}
	}
	if caps != 3 {
		t.Errorf("cap findings = %d, want 3 (tracks 3-5)", caps)
	}
}

// TestProbeFileHonorsTimeout: a fake ffprobe that sleeps is cut
// off by the configured timeout instead of running to sleep's
// end (the timeout argument is genuinely threaded, not decorative).
func TestProbeFileHonorsTimeout(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ffprobe-slow")
	// `exec` so the shell BECOMES sleep: a plain `sleep 10` child
	// would inherit ffprobe's output pipes, and cmd.Run would
	// wait for the pipes to close even after the context kills
	// the shell — measuring the harness, not the timeout.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	pr := probeFile(context.Background(), script, "whatever.mkv", 1)
	elapsed := time.Since(start)
	if pr.OK || pr.Error == "" {
		t.Errorf("probe of a sleeping ffprobe = %+v, want a timeout error", pr)
	}
	if elapsed > 5*time.Second {
		t.Errorf("probe took %v with a 1s timeout — the timeout is not threaded", elapsed)
	}
}
