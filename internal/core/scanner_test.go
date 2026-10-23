package core

// scanner_test.go — tests for the scan-speed work: the per-file
// embedded-subtitle sampling budget (v0.3.1; the audit it caps
// moved out of probeOne into auditEmbeddedForItem in v0.3.4),
// CurrentSince progress stamps, and the raised default worker
// count. The v0.3.4 pipeline tests live in scanner_audit_test.go.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultWorkersIs4: the default config (fresh installs, no
// saved config) probes with 4 workers since v0.3.1 — 2 made first
// scans of real libraries crawl.
func TestDefaultWorkersIs4(t *testing.T) {
	if got := defaultConfig().Workers; got != 4 {
		t.Errorf("defaultConfig().Workers = %d, want 4", got)
	}
}

// TestSetProgressCurrentSince: the stamp is set when the current
// file changes, NOT refreshed while the same file continues,
// and cleared when the current file clears.
func TestSetProgressCurrentSince(t *testing.T) {
	svc := &Service{}
	svc.setProgress("probing", 10, 0, "/lib/a.mkv")
	if svc.state.CurrentSince == "" {
		t.Fatal("CurrentSince must be set when a current file appears")
	}
	// Freeze the stamp, then report progress on the SAME file:
	// the stamp must survive untouched.
	svc.state.CurrentSince = "2000-01-01T00:00:00Z"
	svc.setProgress("probing", -1, 1, "/lib/a.mkv")
	if svc.state.CurrentSince != "2000-01-01T00:00:00Z" {
		t.Errorf("same file must not refresh the stamp, got %q", svc.state.CurrentSince)
	}
	// A different file restamps.
	svc.setProgress("probing", -1, 2, "/lib/b.mkv")
	if svc.state.CurrentSince == "2000-01-01T00:00:00Z" || svc.state.CurrentSince == "" {
		t.Errorf("new file must restamp, got %q", svc.state.CurrentSince)
	}
	// Clearing the current file clears the stamp.
	svc.setProgress("hashing", -1, -1, "")
	if svc.state.Current != "" || svc.state.CurrentSince != "" {
		t.Errorf("clear: current=%q since=%q, want both empty", svc.state.Current, svc.state.CurrentSince)
	}
}

// TestEmbeddedAuditSubtitleBudget: a file with 8 text subtitle
// tracks gets exactly 6 extractions; tracks 7 and 8 are reported
// cannot-verify with the speed-cap note — and an image track in
// the same file neither consumes budget nor is capped. (Before
// v0.3.4 this audited inside probeOne; the budget now lives in
// auditEmbeddedForItem, driven directly here with the same
// assertions.)
func TestEmbeddedAuditSubtitleBudget(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := NewService(dataDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	path := filepath.Join(t.TempDir(), "Budget Case (2024).mkv")
	// Fake probe result: 8 text tracks + 1 image track.
	pr := &ProbeResult{OK: true, HasVideo: true, VideoCodec: "h264", Width: 1920, Height: 1080}
	for i := 0; i < 8; i++ {
		pr.Streams = append(pr.Streams, StreamInfo{Index: i, Codec: "subrip", Type: "subtitle", Language: "eng"})
	}
	pr.Streams = append(pr.Streams, StreamInfo{Index: 8, Codec: "hdmv_pgs_subtitle", Type: "subtitle", Language: "eng"})

	calls := 0
	orig := extractEmbeddedSubtitleFn
	extractEmbeddedSubtitleFn = func(ctx context.Context, ffmpegPath, mediaPath string, streamIndex, sampleSec, timeoutSec int) ([]string, error) {
		calls++
		return []string{
			"This is the story of a man who went to the market in the morning",
			"and he bought bread and milk for his family and walked home again",
		}, nil
	}
	defer func() { extractEmbeddedSubtitleFn = orig }()

	cfg := svc.GetConfig()
	item := &Item{Path: path}
	findings := auditEmbeddedForItem(context.Background(), &cfg, item, pr)

	if calls != maxTextSubtitleSamplesPerFile {
		t.Errorf("extractor calls = %d, want %d (the per-file budget)", calls, maxTextSubtitleSamplesPerFile)
	}
	var caps, images []SubtitleFinding
	for _, f := range findings {
		if strings.Contains(f.Note, "not sampled") {
			caps = append(caps, f)
		}
		if strings.Contains(f.Note, "image-based") {
			images = append(images, f)
		}
	}
	if len(caps) != 2 {
		t.Fatalf("cap findings = %d, want 2 (tracks 7 and 8): %+v", len(caps), caps)
	}
	if caps[0].StreamIdx != 6 || caps[1].StreamIdx != 7 {
		t.Errorf("capped stream indexes = %d, %d, want 6, 7", caps[0].StreamIdx, caps[1].StreamIdx)
	}
	for _, f := range caps {
		if f.Status != "cannot-verify" || !strings.Contains(f.Note, "scan speed cap") {
			t.Errorf("cap finding = %+v", f)
		}
	}
	if len(images) != 1 || images[0].StreamIdx != 8 {
		t.Errorf("image findings = %+v, want the one PGS track", images)
	}
}
