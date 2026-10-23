package core

// duplicates_actions_test.go — the Duplicates tab actions
// (v0.3.6): reveal (membership validation + honest headless
// mode) and the guarded delete (happy path on a REAL scan of
// the fixture library, last-copy protection, exclusion
// semantics, non-duplicate refusal, scan-running refusal).
// The fixture-baseline regression for this feature is the
// unchanged TestScanFixtureBaselineCounts in
// scanner_audit_test.go — the full suite runs both.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scannedDupService returns a service that has really scanned
// the fixture library (2 duplicate groups), plus the lib root
// and data dir.
func scannedDupService(t *testing.T) (*Service, string, string) {
	t.Helper()
	lib := copyFixtureLibrary(t)
	dataDir := t.TempDir()
	svc, err := NewService(dataDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	runScanToCompletion(t, svc, []string{lib})
	return svc, lib, dataDir
}

// nonKeeperCopy picks a group's non-keeper copy.
func nonKeeperCopy(t *testing.T, g DupGroup) DupCopy {
	t.Helper()
	for _, c := range g.Copies {
		if !c.IsKeeper {
			return c
		}
	}
	t.Fatalf("group %q has no non-keeper copy", g.Key)
	return DupCopy{}
}

func TestDuplicatesDeleteHappyPath(t *testing.T) {
	svc, _, dataDir := scannedDupService(t)
	groups := svc.Duplicates()
	if len(groups) != 2 {
		t.Fatalf("fixture scan groups = %d, want 2", len(groups))
	}
	g := groups[0]
	victim := nonKeeperCopy(t, g)
	before := svc.State().Scan.Summary
	if before.DuplicateGroups != 2 || before.MediaCount != 11 {
		t.Fatalf("before summary = %+v, want 2 dup groups / 11 media", before)
	}

	res, err := svc.DeleteDuplicateCopy(victim.Path)
	if err != nil {
		t.Fatalf("DeleteDuplicateCopy: %v", err)
	}
	if !res.OK || res.Path != victim.Path {
		t.Fatalf("result = %+v, want ok with the deleted path", res)
	}
	if _, err := os.Stat(victim.Path); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after delete (stat err = %v)", err)
	}

	// The 2-copy group dissolved; the other group is intact.
	afterGroups := svc.Duplicates()
	if len(afterGroups) != 1 {
		t.Fatalf("groups after delete = %d, want 1", len(afterGroups))
	}
	if afterGroups[0].Key == g.Key {
		t.Errorf("the dissolved group %q is still listed", g.Key)
	}
	after := svc.State().Scan.Summary
	if after.DuplicateGroups != 1 {
		t.Errorf("summary dup groups = %d, want 1", after.DuplicateGroups)
	}
	if after.DuplicateWasted != before.DuplicateWasted-g.WastedBytes {
		t.Errorf("summary wasted = %d, want %d (before %d minus group %d)",
			after.DuplicateWasted, before.DuplicateWasted-g.WastedBytes, before.DuplicateWasted, g.WastedBytes)
	}
	if after.MediaCount != before.MediaCount-1 {
		t.Errorf("media count %d -> %d, want exactly one fewer", before.MediaCount, after.MediaCount)
	}
	if res.Summary.DuplicateGroups != after.DuplicateGroups || res.Summary.MediaCount != after.MediaCount {
		t.Errorf("returned summary %+v disagrees with state summary %+v", res.Summary, after)
	}
	for _, it := range svc.Library("size", "desc") {
		if it.Path == victim.Path {
			t.Error("deleted item still in the library")
		}
	}

	// The audit log recorded the delete as a duplicate-copy,
	// with the group label in the record.
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
	if rec.Path != victim.Path || rec.Size != victim.Size || rec.FindingKind != "duplicate-copy" {
		t.Errorf("audit record = %+v, want path=%s size=%d kind=duplicate-copy", rec, victim.Path, victim.Size)
	}
	if !strings.Contains(rec.Detail, g.Title) {
		t.Errorf("audit record detail = %q, want the group label (containing %q)", rec.Detail, g.Title)
	}
	if _, err := time.Parse(time.RFC3339, rec.Time); err != nil {
		t.Errorf("audit record time %q is not RFC3339: %v", rec.Time, err)
	}

	// The mutation was persisted: a fresh service on the same
	// data dir must not resurrect the copy or its group.
	svc2, err := NewService(dataDir)
	if err != nil {
		t.Fatalf("NewService (reload): %v", err)
	}
	if got := len(svc2.Duplicates()); got != 1 {
		t.Errorf("reloaded dup groups = %d, want 1", got)
	}
	if got := svc2.State().Scan.Summary.MediaCount; got != after.MediaCount {
		t.Errorf("reloaded media count = %d, want %d", got, after.MediaCount)
	}
}

func TestDuplicatesDeleteLastCopyProtected(t *testing.T) {
	svc, _, _ := scannedDupService(t)
	groups := svc.Duplicates()
	if len(groups) != 2 {
		t.Fatalf("fixture scan groups = %d, want 2", len(groups))
	}
	g := groups[0]
	if len(g.Copies) != 2 {
		t.Fatalf("group %q has %d copies, want 2 for this test", g.Key, len(g.Copies))
	}
	first, last := g.Copies[0], g.Copies[1]
	if _, err := svc.DeleteDuplicateCopy(first.Path); err != nil {
		t.Fatalf("delete of the first copy: %v", err)
	}
	// The group has dissolved; the surviving file is no longer
	// a duplicate copy, and deleting it must be refused — a
	// delete can never take a group to zero copies.
	if _, err := svc.DeleteDuplicateCopy(last.Path); err == nil {
		t.Error("delete of the last remaining copy succeeded — last-copy protection broken")
	}
	if _, err := os.Stat(last.Path); err != nil {
		t.Errorf("last remaining copy was deleted anyway: %v", err)
	}
}

func TestDuplicatesDeleteRefusesNonDuplicate(t *testing.T) {
	svc, lib, _ := scannedDupService(t)
	// A scanned library item that is in no duplicate group.
	solo := filepath.Join(lib, "Movies", "Control Film (2022)", "Control Film (2022).mkv")
	if _, err := os.Stat(solo); err != nil {
		t.Fatalf("test setup: solo fixture missing: %v", err)
	}
	if _, err := svc.DeleteDuplicateCopy(solo); err == nil {
		t.Error("delete of a non-duplicate library file succeeded — membership guard broken")
	}
	if _, err := os.Stat(solo); err != nil {
		t.Errorf("non-duplicate file was deleted anyway: %v", err)
	}
	// And a path that does not exist at all.
	if _, err := svc.DeleteDuplicateCopy(filepath.Join(lib, "ghost.mkv")); err == nil {
		t.Error("delete of a nonexistent path succeeded — membership guard broken")
	}
}

func TestDuplicatesDeleteRespectsExclusions(t *testing.T) {
	svc, _, _ := scannedDupService(t)
	groups := svc.Duplicates()
	if len(groups) != 2 {
		t.Fatalf("fixture scan groups = %d, want 2", len(groups))
	}
	g := groups[0]
	a, b := g.Copies[0], g.Copies[1]
	// Excluding one copy for "duplicates" dissolves the group
	// as displayed — so NEITHER copy is actionable any more.
	if _, err := svc.AddExclusion(Exclusion{Path: b.Path, Scope: "duplicates"}); err != nil {
		t.Fatalf("AddExclusion: %v", err)
	}
	for _, grp := range svc.Duplicates() {
		if grp.Key == g.Key {
			t.Fatalf("group %q still displayed after excluding a copy", g.Key)
		}
	}
	if _, err := svc.DeleteDuplicateCopy(a.Path); err == nil {
		t.Error("delete of a copy whose group is dissolved by exclusion succeeded")
	}
	if _, err := svc.DeleteDuplicateCopy(b.Path); err == nil {
		t.Error("delete of the excluded copy succeeded")
	}
	for _, p := range []string{a.Path, b.Path} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file %s was deleted anyway: %v", p, err)
		}
	}
}

func TestDuplicatesDeleteRefusesWhileScanning(t *testing.T) {
	svc, _, _ := scannedDupService(t)
	groups := svc.Duplicates()
	if len(groups) == 0 {
		t.Fatal("fixture scan produced no groups")
	}
	victim := groups[0].Copies[0]
	svc.mu.Lock()
	svc.state.Running = true
	svc.mu.Unlock()
	defer func() {
		svc.mu.Lock()
		svc.state.Running = false
		svc.mu.Unlock()
	}()
	if _, err := svc.DeleteDuplicateCopy(victim.Path); err == nil {
		t.Error("delete during a scan succeeded — scan-running guard broken")
	}
	if _, err := os.Stat(victim.Path); err != nil {
		t.Errorf("file was deleted during a scan anyway: %v", err)
	}
}

func TestDuplicatesRevealModes(t *testing.T) {
	// Deterministic headless environment: the real launcher
	// must decline and the answer is the copy-path mode.
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	svc, lib, _ := scannedDupService(t)
	groups := svc.Duplicates()
	if len(groups) == 0 {
		t.Fatal("fixture scan produced no groups")
	}
	copyPath := groups[0].Copies[0].Path

	res, err := svc.RevealDuplicatePath(copyPath)
	if err != nil {
		t.Fatalf("RevealDuplicatePath: %v", err)
	}
	if res.Mode != "path" || res.Path != copyPath {
		t.Errorf("headless reveal = %+v, want mode \"path\" with the path", res)
	}

	// A library file in no duplicate group is refused.
	solo := filepath.Join(lib, "Movies", "Control Film (2022)", "Control Film (2022).mkv")
	if _, err := svc.RevealDuplicatePath(solo); err == nil {
		t.Error("reveal of a non-duplicate path succeeded — validation broken")
	}
}
