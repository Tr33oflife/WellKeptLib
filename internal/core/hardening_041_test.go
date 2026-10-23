package core

// hardening_041_test.go — tests for the v0.4.1 hardening release
// (two bug sweeps: data-safety + scale/performance). One test per
// fixed defect:
//
//   1. The file-operation guard makes scan/organize exclusion
//      mutual in BOTH directions for the whole operation.
//   2. Undo confines every manifest move to the manifest's root.
//   3. The external-subtitle audit returns the line count from its
//      capped parse (no second, uncapped read), including for
//      files larger than the 64KB cap.
//   4. The incremental cache-save throttle decision.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFileOpGuardMutualExclusion pins the v0.4.1 guard: while it is
// held, StartScan refuses and a second apply/undo refuses; once
// released, the normal apply → undo round trip works again.
func TestFileOpGuardMutualExclusion(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeOrgFile(t, filepath.Join(root, "Film (2010).mkv"), "film")
	plan, err := svc.OrganizePlan(root)
	if err != nil {
		t.Fatal(err)
	}

	// A scan already running refuses the guard (the pre-v0.4.1 check,
	// now funnelled through beginFileOp).
	svc.mu.Lock()
	svc.state.Running = true
	svc.mu.Unlock()
	if err := svc.beginFileOp(); err == nil || !strings.Contains(err.Error(), "scan is running") {
		t.Errorf("beginFileOp during scan: err = %v, want the scan-running refusal", err)
	}
	svc.mu.Lock()
	svc.state.Running = false
	svc.mu.Unlock()

	if err := svc.beginFileOp(); err != nil {
		t.Fatalf("beginFileOp: %v", err)
	}
	// While held: a scan cannot START (the v0.4.1 hole — StartScan
	// used to check only state.Running).
	if _, err := svc.StartScan(nil); err == nil || !strings.Contains(err.Error(), "organize operation is in progress") {
		t.Errorf("StartScan during apply: err = %v, want the organize-in-progress refusal", err)
	}
	// While held: a second apply and an undo both refuse.
	if _, err := svc.OrganizeApply(plan.ID); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("second apply during apply: err = %v, want the busy refusal", err)
	}
	if _, err := svc.OrganizeUndo("organize-whatever"); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("undo during apply: err = %v, want the busy refusal", err)
	}
	svc.endFileOp()

	// Released: the normal apply → undo round trip still passes.
	res, err := svc.OrganizeApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || len(res.Failed) != 0 {
		t.Fatalf("apply after guard released = %+v, want 1 applied", res)
	}
	ures, err := svc.OrganizeUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Restored != 1 || len(ures.Failed) != 0 {
		t.Fatalf("undo after guard released = %+v, want 1 restored", ures)
	}
	// And the guard is free again afterwards (no leak on either path).
	if err := svc.beginFileOp(); err != nil {
		t.Errorf("guard should be free after apply+undo completed: %v", err)
	} else {
		svc.endFileOp()
	}
}

// TestUndoManifestConfinement pins the v0.4.1 undo hardening: a
// manifest move whose recorded path escapes the organized root is
// reported and skipped — never renamed — while an in-root move in
// the same manifest restores normally, and the partial undo does
// NOT mark the manifest undone.
func TestUndoManifestConfinement(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	outside := t.TempDir()

	// In-root move: file currently sits at its organized location.
	inTo := filepath.Join(root, "Film (2010)", "Film (2010).mkv")
	writeOrgFile(t, inTo, "in-root-bytes")
	inFrom := filepath.Join(root, "Film (2010).mkv")
	// Out-of-root move: the file sits OUTSIDE the root; a trusting
	// undo would rename it to the recorded in-root From path.
	evilTo := filepath.Join(outside, "evil.mkv")
	writeOrgFile(t, evilTo, "evil-bytes")
	evilFrom := filepath.Join(root, "evil.mkv")

	m := &organizeManifest{
		ID:   "organize-test-confine",
		Root: root,
		Moves: []organizeManifestMove{
			{From: inFrom, To: inTo, Done: true},
			{From: evilFrom, To: evilTo, Done: true},
		},
	}
	if err := svc.writeOrganizeManifest(m); err != nil {
		t.Fatal(err)
	}

	res, err := svc.OrganizeUndo(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 {
		t.Errorf("restored = %d, want 1 (only the in-root move)", res.Restored)
	}
	if len(res.Failed) != 1 || res.Failed[0].Path != evilTo ||
		!strings.Contains(res.Failed[0].Error, "outside the organized folder") {
		t.Errorf("failed = %+v, want exactly the out-of-root move refused as outside the organized folder", res.Failed)
	}
	// The in-root file is back at its original path, byte-identical.
	if got, err := os.ReadFile(inFrom); err != nil || string(got) != "in-root-bytes" {
		t.Errorf("in-root restore: read = %q, %v", got, err)
	}
	// The out-of-root file is UNTOUCHED: still there, same bytes,
	// and nothing appeared at its recorded in-root destination.
	if got, err := os.ReadFile(evilTo); err != nil || string(got) != "evil-bytes" {
		t.Errorf("out-of-root file must be untouched: read = %q, %v", got, err)
	}
	if _, err := os.Lstat(evilFrom); !os.IsNotExist(err) {
		t.Errorf("nothing should have been renamed to %s (stat err = %v)", evilFrom, err)
	}
	// Partial undo: the manifest is NOT marked undone, and the
	// refused move stays Done (retryable), the restored one flipped.
	got, err := svc.loadOrganizeManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UndoneAt != "" {
		t.Errorf("manifest marked undone after a PARTIAL undo (undone_at = %q)", got.UndoneAt)
	}
	if got.Moves[0].Done {
		t.Errorf("restored move should be flipped to done=false in the manifest")
	}
	if !got.Moves[1].Done {
		t.Errorf("refused move should stay done=true (still outstanding)")
	}
}

// TestStoreSaveDue pins the v0.4.1 throttle decision: the first
// attempt saves, an immediate second does not, and after the
// interval it does again.
func TestStoreSaveDue(t *testing.T) {
	now := time.Now()
	if !storeSaveDue(time.Time{}, now) {
		t.Error("no previous save: want due")
	}
	if storeSaveDue(now, now) {
		t.Error("same instant as last save: want not due")
	}
	if storeSaveDue(now.Add(-4*time.Second), now) {
		t.Error("4s after last save (< 5s interval): want not due")
	}
	if !storeSaveDue(now.Add(-5*time.Second), now) {
		t.Error("exactly at the 5s interval: want due")
	}
	if !storeSaveDue(now.Add(-time.Minute), now) {
		t.Error("a minute after last save: want due")
	}
}

// TestAuditExternalSubtitleLargeFile pins the v0.4.1 line-count
// change: a subtitle file LARGER than the audit's 64KB read cap
// still yields cues and lines from that capped parse (the scanner
// no longer re-reads the file in full to recount them), so the
// health check's Lines==0 test keeps passing for real files.
func TestAuditExternalSubtitleLargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Big Movie.en.srt")
	var b strings.Builder
	for i := 1; b.Len() <= 70000; i++ {
		// Plain English dialogue lines — enough text per cue for
		// the parser to count a line for every cue.
		b.WriteString(strconv.Itoa(i) + "\n00:00:01,000 --> 00:00:02,000\n" +
			"The quick brown fox jumps over the lazy dog near the river bank.\n\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() <= 65536 {
		t.Fatalf("fixture must exceed the 64KB cap: size = %v, err = %v", info, err)
	}
	_, cues, lines, err := auditExternalSubtitle(path, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if cues <= 0 {
		t.Errorf("cues = %d, want > 0 for a >64KB subtitle file", cues)
	}
	if lines <= 0 {
		t.Errorf("lines = %d, want > 0 (health tests Lines==0 for the subtitle-empty finding)", lines)
	}
}
