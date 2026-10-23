package core

// health_actions_test.go — the Health tab actions: reveal (mode
// behaviour on a headless host + the launched branch via the seam)
// and the guarded delete (happy path on a REAL scan, plus each
// refusal in the guard chain). The fixture-baseline regression for
// this feature is the unchanged TestScanFixtureBaselineCounts in
// scanner_audit_test.go — the full suite runs both.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// seedHealthLibrary writes a tiny library with two findings — a
// zero-byte .mkv ("empty") and a text-bytes .mkv ("probe-failure")
// — plus an empty folder ("empty-folder"), all under one root.
func seedHealthLibrary(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Empty Movie (2020).mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Broken Movie (2021).mkv"),
		[]byte("this is not a video file, just text bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Empty Season"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// scannedHealthService returns a service that has really scanned
// the seeded library, plus its data dir (for the audit log).
func scannedHealthService(t *testing.T) (*Service, string, string) {
	t.Helper()
	root := seedHealthLibrary(t)
	dataDir := t.TempDir()
	svc, err := NewService(dataDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	runScanToCompletion(t, svc, []string{root})
	return svc, dataDir, root
}

func findingFor(svc *Service, path string) (HealthFinding, bool) {
	for _, f := range svc.Health() {
		if f.Path == path {
			return f, true
		}
	}
	return HealthFinding{}, false
}

func TestHealthDeleteHappyPath(t *testing.T) {
	svc, dataDir, root := scannedHealthService(t)
	emptyPath := filepath.Join(root, "Empty Movie (2020).mkv")
	f, ok := findingFor(svc, emptyPath)
	if !ok || f.Type != "empty" {
		t.Fatalf("seeded zero-byte file finding = %+v (ok=%v), want type \"empty\"", f, ok)
	}
	before := svc.State().Scan.Summary

	res, err := svc.DeleteHealthFile(emptyPath)
	if err != nil {
		t.Fatalf("DeleteHealthFile: %v", err)
	}
	if !res.OK || res.Path != emptyPath {
		t.Fatalf("result = %+v, want ok with the deleted path", res)
	}
	if _, err := os.Stat(emptyPath); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after delete (stat err = %v)", err)
	}
	if _, ok := findingFor(svc, emptyPath); ok {
		t.Error("finding still listed after delete")
	}
	after := svc.State().Scan.Summary
	if after.HealthFindings != before.HealthFindings-1 {
		t.Errorf("health findings %d -> %d, want exactly one fewer", before.HealthFindings, after.HealthFindings)
	}
	if after.MediaCount != before.MediaCount-1 {
		t.Errorf("media count %d -> %d, want exactly one fewer", before.MediaCount, after.MediaCount)
	}
	if res.Summary.HealthFindings != after.HealthFindings || res.Summary.MediaCount != after.MediaCount {
		t.Errorf("returned summary %+v disagrees with state summary %+v", res.Summary, after)
	}
	for _, it := range svc.Library("size", "desc") {
		if it.Path == emptyPath {
			t.Error("deleted item still in the library")
		}
	}

	// The audit log recorded exactly this delete.
	b, err := os.ReadFile(filepath.Join(dataDir, "deleted-actions.json"))
	if err != nil {
		t.Fatalf("read deleted-actions.json: %v", err)
	}
	var log []DeletedAction
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatalf("parse deleted-actions.json: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("audit log has %d entries, want 1", len(log))
	}
	rec := log[0]
	if rec.Path != emptyPath || rec.Size != 0 || rec.FindingKind != "empty" {
		t.Errorf("audit record = %+v, want path=%s size=0 kind=empty", rec, emptyPath)
	}
	if _, err := time.Parse(time.RFC3339, rec.Time); err != nil {
		t.Errorf("audit record time %q is not RFC3339: %v", rec.Time, err)
	}

	// The mutation was persisted: a fresh service on the same data
	// dir must not resurrect the file.
	svc2, err := NewService(dataDir)
	if err != nil {
		t.Fatalf("NewService (reload): %v", err)
	}
	if _, ok := findingFor(svc2, emptyPath); ok {
		t.Error("finding resurrected after reload — results were not persisted")
	}
	if got := svc2.State().Scan.Summary.MediaCount; got != after.MediaCount {
		t.Errorf("reloaded media count = %d, want %d", got, after.MediaCount)
	}
}

func TestHealthDeleteRefusesNonFinding(t *testing.T) {
	svc, _, root := scannedHealthService(t)
	// A real file inside the root that the scan never flagged.
	clean := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(clean, []byte("shopping list\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteHealthFile(clean); err == nil {
		t.Error("delete of a non-finding path succeeded — guard chain broken")
	}
	if _, err := os.Stat(clean); err != nil {
		t.Errorf("non-finding file was deleted anyway: %v", err)
	}
	// And a path that does not exist at all.
	if _, err := svc.DeleteHealthFile(filepath.Join(root, "ghost.mkv")); err == nil {
		t.Error("delete of a nonexistent path succeeded — guard chain broken")
	}
}

func TestHealthDeleteRefusesOutsideRoots(t *testing.T) {
	svc, _, root := scannedHealthService(t)
	// A real file in a DIFFERENT tree, hand-seeded as a finding
	// (the scan could never produce this state; if it somehow
	// existed, the root confinement must still refuse).
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "stray.mkv")
	if err := os.WriteFile(outside, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.results.Health = append(svc.results.Health,
		HealthFinding{Path: outside, Type: "empty", Size: 0, Evidence: "seeded by test"})
	svc.mu.Unlock()
	if got := svc.cfg.Roots; len(got) != 1 || got[0] != root {
		t.Fatalf("test setup: roots = %v, want [%s]", got, root)
	}
	if _, err := svc.DeleteHealthFile(outside); err == nil {
		t.Error("delete of an out-of-roots finding succeeded — confinement broken")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("out-of-roots file was deleted anyway: %v", err)
	}
}

func TestHealthDeleteRefusesDirectory(t *testing.T) {
	svc, _, root := scannedHealthService(t)
	dir := filepath.Join(root, "Empty Season")
	f, ok := findingFor(svc, dir)
	if !ok || f.Type != "empty-folder" {
		t.Fatalf("empty folder finding = %+v (ok=%v), want type \"empty-folder\"", f, ok)
	}
	if _, err := svc.DeleteHealthFile(dir); err == nil {
		t.Error("delete of a folder finding succeeded — files-only rule broken")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("folder was removed anyway (stat err = %v)", err)
	}
}

func TestHealthRevealModes(t *testing.T) {
	// Deterministic headless environment regardless of the host
	// running the tests: no DISPLAY/WAYLAND_DISPLAY, so the real
	// launcher must decline and the answer is the copy-path mode.
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	svc, _, root := scannedHealthService(t)
	emptyPath := filepath.Join(root, "Empty Movie (2020).mkv")

	res, err := svc.RevealHealthPath(emptyPath)
	if err != nil {
		t.Fatalf("RevealHealthPath: %v", err)
	}
	if res.Mode != "path" || res.Path != emptyPath {
		t.Errorf("headless reveal = %+v, want mode \"path\" with the path", res)
	}

	// The launched branch (stubbed at the seam the scanner tests
	// also use): the answer must claim "revealed".
	orig := revealInFileManagerFn
	revealInFileManagerFn = func(string) bool { return true }
	defer func() { revealInFileManagerFn = orig }()
	res, err = svc.RevealHealthPath(emptyPath)
	if err != nil {
		t.Fatalf("RevealHealthPath (stubbed launcher): %v", err)
	}
	if res.Mode != "revealed" {
		t.Errorf("launched reveal mode = %q, want \"revealed\"", res.Mode)
	}

	// A non-finding is refused in both modes.
	if _, err := svc.RevealHealthPath(filepath.Join(root, "notes.txt")); err == nil {
		t.Error("reveal of a non-finding path succeeded — validation broken")
	}
}

// TestRevealCommandSpecs pins the exact per-platform command
// the reveal launches — above all the Windows spec, whose
// v0.3.5 single-argument form ("/select,"+path as ONE
// argument) put the switch itself inside the quotes os/exec
// adds around a space-containing argument, so Explorer ignored
// it and opened its default view (the user's Documents) on
// every real library path. The fixed spec is two arguments:
// "/select," bare, then the path on its own.
func TestRevealCommandSpecs(t *testing.T) {
	winPath := `C:\Media\TV Shows\Some Show\Some Show - S01E01.mkv`
	name, args, ok := revealCommand("windows", winPath, false, false)
	if !ok || name != "explorer" || !reflect.DeepEqual(args, []string{"/select,", winPath}) {
		t.Errorf("windows spec = %q %v (ok=%v), want explorer [/select, %s]", name, args, ok, winPath)
	}

	name, args, ok = revealCommand("darwin", "/media/Movie (2020).mkv", false, false)
	if !ok || name != "open" || !reflect.DeepEqual(args, []string{"-R", "/media/Movie (2020).mkv"}) {
		t.Errorf("darwin spec = %q %v (ok=%v), want open [-R <path>]", name, args, ok)
	}

	// Linux with a desktop session and xdg-open: open the
	// file's containing folder.
	name, args, ok = revealCommand("linux", "/media/Movie (2020).mkv", true, true)
	if !ok || name != "xdg-open" || !reflect.DeepEqual(args, []string{"/media"}) {
		t.Errorf("linux desktop spec = %q %v (ok=%v), want xdg-open [/media]", name, args, ok)
	}
	// Headless (the Ultra shape): nothing to launch.
	if _, _, ok := revealCommand("linux", "/media/Movie (2020).mkv", false, true); ok {
		t.Error("linux headless spec ok=true, want false (no desktop session)")
	}
	// Desktop session but no xdg-open on PATH: nothing to
	// launch with.
	if _, _, ok := revealCommand("linux", "/media/Movie (2020).mkv", true, false); ok {
		t.Error("linux no-xdg-open spec ok=true, want false")
	}
}
