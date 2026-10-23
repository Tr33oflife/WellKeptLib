package core

// organize_apply_test.go — tests for the v0.3.0 Organize apply/undo
// engine. The centerpiece is the round-trip proof: snapshot a tree
// (every file's path + sha256, every folder), apply a plan, undo it,
// and require the second snapshot to be BYTE-IDENTICAL to the first.
// Around it: the scan-running refusal, the all-or-nothing
// re-validation gate, and undo surviving a service restart (manifests
// live on disk, plans do not).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// treeSnapshot captures a folder tree exactly: file contents by hash
// and the folder set, all root-relative.
type treeSnapshot struct {
	Files map[string]string // rel path -> sha256 hex
	Dirs  []string          // sorted rel dirs, root itself excluded
}

// snapshotTree walks root and records the snapshot.
func snapshotTree(t *testing.T, root string) treeSnapshot {
	t.Helper()
	snap := treeSnapshot{Files: map[string]string{}}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if rel != "." {
				snap.Dirs = append(snap.Dirs, rel)
			}
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		snap.Files[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	sort.Strings(snap.Dirs)
	return snap
}

// buildRoundTripTree makes the shared apply/undo fixture: a movie
// with a subtitle + folder poster in one folder, an episode in
// another. Returns the root.
func buildRoundTripTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeOrgFile(t, filepath.Join(root, "loose", "Alien (1979).mkv"), "alien-video")
	writeOrgFile(t, filepath.Join(root, "loose", "Alien (1979).en.srt"), "alien-subs")
	writeOrgFile(t, filepath.Join(root, "loose", "poster.jpg"), "alien-poster")
	writeOrgFile(t, filepath.Join(root, "show", "Cool Show S01E03.mkv"), "cool-episode")
	return root
}

// hashOf returns the sha256 hex of a file's contents.
func hashOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestApplyUndoRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	root := buildRoundTripTree(t)
	before := snapshotTree(t, root)

	plan, err := svc.OrganizePlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 4 {
		t.Fatalf("plan moves = %d (%v), want 4 (movie, subtitle, poster, episode)", len(plan.Moves), plan.Moves)
	}
	res, err := svc.OrganizeApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 4 || len(res.Failed) != 0 {
		t.Fatalf("apply = %+v, want 4 applied, no failures", res)
	}
	if res.ManifestID == "" {
		t.Fatal("apply returned no manifest ID")
	}
	// The emptied source folders are left in place and reported.
	if res.EmptyDirsLeft != 2 {
		t.Errorf("empty_dirs_left = %d, want 2 (loose + show)", res.EmptyDirsLeft)
	}

	// Post-apply: every target exists with the ORIGINAL bytes.
	wantTargets := map[string]string{ // target rel path -> original content hash source
		filepath.Join("Alien (1979)", "Alien (1979).mkv"):                 filepath.Join("loose", "Alien (1979).mkv"),
		filepath.Join("Alien (1979)", "Alien (1979).en.srt"):              filepath.Join("loose", "Alien (1979).en.srt"),
		filepath.Join("Alien (1979)", "poster.jpg"):                       filepath.Join("loose", "poster.jpg"),
		filepath.Join("Cool Show", "Season 01", "Cool Show - S01E03.mkv"): filepath.Join("show", "Cool Show S01E03.mkv"),
	}
	afterApply := snapshotTree(t, root)
	for rel, origRel := range wantTargets {
		got, ok := afterApply.Files[rel]
		if !ok {
			t.Errorf("target %s missing after apply (files: %v)", rel, afterApply.Files)
			continue
		}
		if got != before.Files[origRel] {
			t.Errorf("target %s content hash differs from the original %s", rel, origRel)
		}
	}
	// …and the originals are gone.
	for origRel := range before.Files {
		if _, ok := afterApply.Files[origRel]; ok {
			t.Errorf("original %s still present after apply", origRel)
		}
	}

	// The manifest on disk names the created folders and every move.
	mPath := filepath.Join(dataDir, "organize-manifests", res.ManifestID+".json")
	b, err := os.ReadFile(mPath)
	if err != nil {
		t.Fatalf("manifest not on disk: %v", err)
	}
	var m organizeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest unreadable: %v", err)
	}
	if len(m.Moves) != 4 {
		t.Errorf("manifest moves = %d, want 4", len(m.Moves))
	}
	for _, mv := range m.Moves {
		if !mv.Done {
			t.Errorf("manifest move %+v not marked done after a full apply", mv)
		}
	}
	created := map[string]bool{}
	for _, d := range m.CreatedDirs {
		created[d] = true
	}
	for _, want := range []string{
		filepath.Join(root, "Alien (1979)"),
		filepath.Join(root, "Cool Show"),
		filepath.Join(root, "Cool Show", "Season 01"),
	} {
		if !created[want] {
			t.Errorf("created_dirs %v missing %s", m.CreatedDirs, want)
		}
	}

	// Undo: the tree must come back BYTE-IDENTICAL.
	ures, err := svc.OrganizeUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Restored != 4 || len(ures.Failed) != 0 {
		t.Fatalf("undo = %+v, want 4 restored, no failures", ures)
	}
	afterUndo := snapshotTree(t, root)
	if !reflect.DeepEqual(before, afterUndo) {
		t.Errorf("tree after undo differs from before apply:\nbefore: %+v\nafter:  %+v", before, afterUndo)
	}

	// The manifest is now marked undone, and a second undo refuses.
	list, err := svc.OrganizeManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Undone {
		t.Errorf("manifest list = %+v, want the one manifest marked undone", list)
	}
	if _, err := svc.OrganizeUndo(res.ManifestID); err == nil {
		t.Error("second undo: want an error (already undone)")
	}
}

func TestOrganizeRefusesWhileScanRunning(t *testing.T) {
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
	svc.mu.Lock()
	svc.state.Running = true
	svc.mu.Unlock()
	if _, err := svc.OrganizePlan(root); err == nil || !strings.Contains(err.Error(), "scan is running") {
		t.Errorf("plan during scan: err = %v, want the scan-running refusal", err)
	}
	if _, err := svc.OrganizeApply(plan.ID); err == nil || !strings.Contains(err.Error(), "scan is running") {
		t.Errorf("apply during scan: err = %v, want the scan-running refusal", err)
	}
	svc.mu.Lock()
	svc.state.Running = false
	svc.mu.Unlock()
	// And with the scan flag cleared, the same plan applies fine.
	res, err := svc.OrganizeApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 {
		t.Errorf("apply after scan cleared = %+v, want 1 applied", res)
	}
}

func TestApplyRevalidationGate(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := filepath.Join(root, "Alpha (2001).mkv")
	b := filepath.Join(root, "Beta (2002).mkv")
	writeOrgFile(t, a, "a")
	writeOrgFile(t, b, "b")
	plan, err := svc.OrganizePlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 2 {
		t.Fatalf("plan moves = %v, want 2", plan.Moves)
	}
	// The tree changes between preview and apply: one source vanishes.
	if err := os.Remove(b); err != nil {
		t.Fatal(err)
	}
	res, err := svc.OrganizeApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	// All-or-nothing at the gate: NOTHING moved, the failure is named.
	if res.Applied != 0 {
		t.Errorf("applied = %d, want 0", res.Applied)
	}
	if len(res.Failed) != 1 || res.Failed[0].Path != b {
		t.Errorf("failed = %v, want exactly the vanished source", res.Failed)
	}
	if _, err := os.Stat(a); err != nil {
		t.Errorf("the untouched source should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Alpha (2001)", "Alpha (2001).mkv")); !os.IsNotExist(err) {
		t.Errorf("no target should have been created, stat err = %v", err)
	}
	if res.ManifestID != "" {
		t.Errorf("manifest_id = %q, want empty when the gate rejects", res.ManifestID)
	}
	list, err := svc.OrganizeManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("manifests = %v, want none written when the gate rejects", list)
	}
}

func TestUndoAfterRestart(t *testing.T) {
	// Apply with one Service instance, undo with a FRESH one over the
	// same data dir: plans are gone (in-memory), manifests persist.
	dataDir := t.TempDir()
	svc1, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	root := buildRoundTripTree(t)
	before := snapshotTree(t, root)
	plan, err := svc1.OrganizePlan(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc1.OrganizeApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}

	svc2, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc2.OrganizeManifests()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != res.ManifestID || list[0].Undone {
		t.Fatalf("manifest list after restart = %+v, want the applied manifest, not undone", list)
	}
	ures, err := svc2.OrganizeUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Restored != len(plan.Moves) || len(ures.Failed) != 0 {
		t.Fatalf("undo after restart = %+v, want all %d restored", ures, len(plan.Moves))
	}
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("tree after restart-undo differs:\nbefore: %+v\nafter:  %+v", before, after)
	}
	// Spot-check one file's bytes directly, not only via hashes.
	if got := hashOf(t, filepath.Join(root, "loose", "Alien (1979).mkv")); got != before.Files[filepath.Join("loose", "Alien (1979).mkv")] {
		t.Errorf("restored file hash mismatch")
	}
}
