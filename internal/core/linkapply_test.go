package core

// linkapply_test.go — tests for the v1.1.0 link Apply / Undo
// engine (LINK-LIBRARY-SPEC §§17.5–17.7). The centerpiece is
// the prime invariant: after Apply + Undo the SOURCE tree is
// exactly what it was — paths, content hashes, sizes, mtimes,
// devices, inodes, AND link counts. Cross-device and EEXIST
// races are simulated at the injectable link seam
// (createHardlinkFn); a one-volume temp dir cannot produce
// them for real, and the tests say so where they simulate.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sourceEntry is one row of the full-fidelity source snapshot.
type sourceEntry struct {
	Rel       string
	IsDir     bool
	Size      int64
	Hash      string
	MtimeNano int64
	Dev       uint64
	Ino       uint64
	Nlink     uint64
}

// snapshotSourceTree records every entry under root with its
// content hash and storage identity — the yardstick for the
// prime invariant.
func snapshotSourceTree(t *testing.T, root string) map[string]sourceEntry {
	t.Helper()
	out := map[string]sourceEntry{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		e := sourceEntry{Rel: rel, IsDir: d.IsDir(), Size: info.Size(), MtimeNano: info.ModTime().UnixNano()}
		e.Dev, e.Ino, e.Nlink, _ = fileIdentity(info)
		if !d.IsDir() && info.Mode().IsRegular() {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(b)
			e.Hash = hex.EncodeToString(sum[:])
		}
		out[rel] = e
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func assertSnapshotsEqual(t *testing.T, want, got map[string]sourceEntry, context string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: entry count = %d, want %d", context, len(got), len(want))
	}
	for rel, w := range want {
		g, ok := got[rel]
		if !ok {
			t.Errorf("%s: %s missing after the operation", context, rel)
			continue
		}
		if g != w {
			t.Errorf("%s: %s changed:\n got %+v\nwant %+v", context, rel, g, w)
		}
	}
}

// linkSvc builds a Service over a fresh data dir.
func linkSvc(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// simpleLinkTree: one movie with a subtitle in a messy folder.
// Returns source root, target root (not yet created), and the
// two source paths.
func simpleLinkTree(t *testing.T) (string, string, string, string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	video := filepath.Join(src, "downloads", "The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv")
	sub := filepath.Join(src, "downloads", "The.Matrix.1999.1080p.BluRay.x264-GROUP.en.srt")
	writeOrgFile(t, video, "matrix-video-content")
	writeOrgFile(t, sub, "matrix-subtitle-content")
	return src, tgt, video, sub
}

func mustSvcLinkPlan(t *testing.T, svc *Service, src, tgt string) *LinkPlan {
	t.Helper()
	plan, err := svc.LinkPlan(src, tgt)
	if err != nil {
		t.Fatalf("LinkPlan: %v", err)
	}
	return plan
}

func identityOf(t *testing.T, path string) (uint64, uint64, uint64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, nlink, ok := fileIdentityFor(info, path)
	if !ok {
		t.Skip("host test filesystem cannot report storage identity")
	}
	return dev, ino, nlink
}

// §17.5.1: Apply creates REAL hardlinks — identity and content
// equal, source link counts up by exactly the created count.
func TestLinkApplyCreatesRealHardlinks(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, sub := simpleLinkTree(t)
	_, _, nlinkBefore := identityOf(t, video)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan.LinksToCreate) != 2 {
		t.Fatalf("plan links = %d, want 2", len(plan.LinksToCreate))
	}
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || res.ManifestID == "" || res.Partial {
		t.Fatalf("apply = %+v, want 2 created with a manifest, not partial", res)
	}
	for _, e := range plan.LinksToCreate {
		sd, si, _ := identityOf(t, e.Source)
		td, ti, _ := identityOf(t, e.Target)
		if sd != td || si != ti {
			t.Errorf("target %s identity (%d,%d) != source (%d,%d)", e.Target, td, ti, sd, si)
		}
		sb, _ := os.ReadFile(e.Source)
		tb, _ := os.ReadFile(e.Target)
		if string(sb) != string(tb) {
			t.Errorf("target %s content differs from source", e.Target)
		}
	}
	_, _, nlinkAfter := identityOf(t, video)
	if nlinkAfter != nlinkBefore+1 {
		t.Errorf("video nlink = %d, want %d (+1 created link)", nlinkAfter, nlinkBefore+1)
	}
	_, _, subNlink := identityOf(t, sub)
	if subNlink != 2 {
		t.Errorf("subtitle nlink = %d, want 2", subNlink)
	}
	// The manifest on disk records both links as done.
	m, err := svc.loadLinkManifest(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Links) != 2 || !m.Links[0].Done || !m.Links[1].Done {
		t.Errorf("manifest links = %+v, want 2 done", m.Links)
	}
	if len(m.CreatedDirs) == 0 {
		t.Errorf("manifest recorded no created dirs, want the target tree folders")
	}
}

// §17.5.2: the manifest exists — with the intent record for
// the link being attempted — BEFORE the first link is created.
func TestLinkManifestWrittenBeforeFirstLink(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	dataDir := svc.dataDir
	orig := createHardlinkFn
	checked := false
	var checkErr string
	createHardlinkFn = func(source, target string) error {
		if !checked {
			checked = true
			entries, err := os.ReadDir(filepath.Join(dataDir, "link-manifests"))
			if err != nil {
				checkErr = "manifest dir unreadable at first link: " + err.Error()
			} else if len(entries) != 1 {
				checkErr = "manifest files at first link != 1"
			} else {
				b, rerr := os.ReadFile(filepath.Join(dataDir, "link-manifests", entries[0].Name()))
				if rerr != nil {
					checkErr = rerr.Error()
				} else if !strings.Contains(string(b), `"source": "`+filepath.ToSlash(source)+`"`) &&
					!strings.Contains(string(b), source) {
					checkErr = "manifest at first link does not name the source being linked"
				}
			}
		}
		return orig(source, target)
	}
	defer func() { createHardlinkFn = orig }()
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("link function was never called")
	}
	if checkErr != "" {
		t.Errorf("manifest-before-first-link violated: %s", checkErr)
	}
	if res.Created != 2 {
		t.Errorf("created = %d, want 2", res.Created)
	}
}

// §17.5.3: a second preview/Apply cycle creates zero links,
// writes no new manifest, and does not disturb inodes.
func TestLinkApplyIncrementalSecondCycle(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil || res.Created != 2 {
		t.Fatalf("first apply = %+v, %v", res, err)
	}
	inodes := map[string]uint64{}
	for _, e := range plan.LinksToCreate {
		_, ino, _ := identityOf(t, e.Target)
		inodes[e.Target] = ino
	}
	plan2 := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan2.LinksToCreate) != 0 || len(plan2.AlreadyLinked) != 2 {
		t.Fatalf("second plan = %d to create / %d already linked, want 0/2",
			len(plan2.LinksToCreate), len(plan2.AlreadyLinked))
	}
	res2, err := svc.LinkApply(plan2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Created != 0 || res2.ManifestID != "" {
		t.Errorf("second apply = %+v, want 0 created and no new manifest", res2)
	}
	for target, ino := range inodes {
		_, now, _ := identityOf(t, target)
		if now != ino {
			t.Errorf("target %s inode changed across the second cycle", target)
		}
	}
}

// §17.5.4: a partial Apply (one entry fails at the seam)
// leaves a truthful manifest, and a fresh preview + Apply
// creates only the missing link.
func TestLinkApplyPartialAndResume(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	orig := createHardlinkFn
	createHardlinkFn = func(source, target string) error {
		if source == video {
			return errors.New("simulated link failure")
		}
		return orig(source, target)
	}
	res, err := svc.LinkApply(plan.ID)
	createHardlinkFn = orig
	if err != nil {
		t.Fatal(err)
	}
	// The video failed; its attached subtitle is withheld as
	// a dependency skip (a sidecar is never linked without its
	// video). Nothing is created, and the build is partial.
	if res.Created != 0 || !res.Partial || len(res.Failed) != 1 {
		t.Fatalf("partial apply = %+v, want 0 created + 1 failed + partial", res)
	}
	depFound := false
	for _, sk := range res.Skipped {
		if sk.Category == "dependency" {
			depFound = true
		}
	}
	if !depFound {
		t.Errorf("skipped = %+v, want the subtitle withheld as a dependency", res.Skipped)
	}
	plan2 := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan2.LinksToCreate) != 2 {
		t.Fatalf("resume plan = %+v, want the video and its subtitle", plan2.LinksToCreate)
	}
	res2, err := svc.LinkApply(plan2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Created != 2 {
		t.Errorf("resume apply created = %d, want 2", res2.Created)
	}
}

// §17.5.5: an injected EXDEV at the link seam is reported as
// unlinkable and is not marked done in the manifest.
// SIMULATION: a one-volume temp dir cannot produce a real
// cross-device kernel error.
func TestLinkApplyInjectedEXDEV(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	orig := createHardlinkFn
	createHardlinkFn = func(source, target string) error {
		if source == video {
			return simulatedEXDEVError()
		}
		return orig(source, target)
	}
	res, err := svc.LinkApply(plan.ID)
	createHardlinkFn = orig
	if err != nil {
		t.Fatal(err)
	}
	// The video is unlinkable; its attached subtitle is
	// withheld as a dependency, so nothing is created — but
	// the apply completes and reports both.
	if res.Created != 0 {
		t.Fatalf("created = %d, want 0 (video unlinkable, subtitle depends on it)", res.Created)
	}
	found := false
	for _, sk := range res.Skipped {
		if sk.Source == video && sk.Category == "unlinkable" {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped = %+v, want the video as unlinkable", res.Skipped)
	}
	m, err := svc.loadLinkManifest(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range m.Links {
		if l.Source == video && l.Done {
			t.Errorf("the EXDEV entry was marked done in the manifest")
		}
	}
}

// §17.5.6: link-time EEXIST race — the seam creates the real
// hardlink and then reports EEXIST. Apply reclassifies the
// target as already linked and does not count it as created.
func TestLinkApplyEEXISTRaceSameInode(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	orig := createHardlinkFn
	createHardlinkFn = func(source, target string) error {
		if source == video {
			if err := orig(source, target); err != nil {
				return err
			}
			return simulatedEEXISTError()
		}
		return orig(source, target)
	}
	defer func() { createHardlinkFn = orig }()
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.AlreadyLinked != 1 {
		t.Errorf("apply = created %d / already %d, want 1/1 (subtitle created, video reclassified)", res.Created, res.AlreadyLinked)
	}
	m, err := svc.loadLinkManifest(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range m.Links {
		if l.Source == video && l.Done {
			t.Errorf("the raced entry was recorded as a created link")
		}
	}
}

// §17.5.7: link-time EEXIST race — a DIFFERENT file lands at
// the target. Apply reports a target conflict and leaves that
// file's bytes and inode untouched.
func TestLinkApplyEEXISTRaceDifferentFile(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	var occupant string
	for _, e := range plan.LinksToCreate {
		if e.Source == video {
			occupant = e.Target
		}
	}
	orig := createHardlinkFn
	createHardlinkFn = func(source, target string) error {
		if source == video {
			if err := os.WriteFile(target, []byte("someone-else"), 0o644); err != nil {
				return err
			}
			return simulatedEEXISTError()
		}
		return orig(source, target)
	}
	defer func() { createHardlinkFn = orig }()
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sk := range res.Skipped {
		if sk.Source == video && sk.Category == "target-conflict" {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped = %+v, want the video as target-conflict", res.Skipped)
	}
	b, err := os.ReadFile(occupant)
	if err != nil || string(b) != "someone-else" {
		t.Errorf("occupant bytes = %q, %v — want untouched", b, err)
	}
}

// §17.5.8: a source replaced after the preview is skipped as
// changed; no link is made from the new file under the stale
// plan.
func TestLinkApplySourceChanged(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	if err := os.WriteFile(video, []byte("a-different-longer-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The changed video is skipped, and its attached subtitle
	// is withheld as a dependency — nothing is created.
	if res.Created != 0 {
		t.Errorf("created = %d, want 0 (video changed, subtitle depends on it)", res.Created)
	}
	found := false
	for _, sk := range res.Skipped {
		if sk.Source == video && sk.Category == "source-changed" {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped = %+v, want the video as source-changed", res.Skipped)
	}
}

// Preview performs no filesystem writes (L1 acceptance): the
// target does not exist before or after, and the source is
// untouched.
func TestLinkPreviewWritesNothing(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	before := snapshotSourceTree(t, src)
	if _, err := svc.LinkPlan(src, tgt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tgt); !os.IsNotExist(err) {
		t.Errorf("preview created the target root")
	}
	assertSnapshotsEqual(t, before, snapshotSourceTree(t, src), "preview")
}

// §17.6.1 + §17.5.9: THE PRIME INVARIANT. Snapshot the source,
// Apply, Undo, and require the final source snapshot to be
// exactly the initial one — including link counts. Created
// target links and created directories are gone afterwards.
func TestLinkApplyUndoSourceUntouched(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	before := snapshotSourceTree(t, src)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil || res.Created != 2 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	// Content-level check mid-flight: sources' bytes/mtimes
	// unchanged by Apply itself (only Nlink moves, by design).
	mid := snapshotSourceTree(t, src)
	for rel, w := range before {
		g := mid[rel]
		if g.Hash != w.Hash || g.MtimeNano != w.MtimeNano || g.Ino != w.Ino || g.Dev != w.Dev || g.Size != w.Size {
			t.Errorf("apply changed source entry %s: got %+v want %+v (nlink aside)", rel, g, w)
		}
	}
	ures, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Removed != 2 || !ures.Undone {
		t.Fatalf("undo = %+v, want 2 removed and the manifest undone", ures)
	}
	assertSnapshotsEqual(t, before, snapshotSourceTree(t, src), "apply+undo")
	if _, err := os.Lstat(tgt); !os.IsNotExist(err) {
		t.Errorf("target root created by Apply survived Undo")
	}
	// Nothing the manifest did not create may remain in a
	// fresh target tree: the whole tree is gone.
	if _, err := os.Lstat(filepath.Join(tgt, "The Matrix (1999)")); !os.IsNotExist(err) {
		t.Errorf("created target folders survived Undo")
	}
}

// §17.6.3: pre-existing target directories survive Undo.
func TestLinkUndoPreexistingDirsSurvive(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	if err := os.MkdirAll(filepath.Join(tgt, "The Matrix (1999)"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil || res.Created != 2 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if res.CreatedDirs != 0 {
		t.Errorf("created dirs = %d, want 0 (both folders pre-existed)", res.CreatedDirs)
	}
	ures, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.DirsRemoved != 0 {
		t.Errorf("dirs removed = %d, want 0", ures.DirsRemoved)
	}
	if _, err := os.Lstat(filepath.Join(tgt, "The Matrix (1999)")); err != nil {
		t.Errorf("pre-existing target folder was removed: %v", err)
	}
}

// §17.6.4: a user file placed in a created directory survives
// Undo; the directory is reported as left in place and does
// not block the build being marked undone.
func TestLinkUndoNonEmptyDirSurvives(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(tgt, "The Matrix (1999)", "my-notes.txt")
	if err := os.WriteFile(userFile, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	ures, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Removed != 2 || !ures.Undone {
		t.Errorf("undo = %+v, want links removed and manifest undone despite the left folder", ures)
	}
	// Both the work folder and the target root above it stay
	// (the root is not empty while the folder remains) and
	// both are reported.
	if len(ures.DirsLeft) != 2 {
		t.Errorf("dirs left = %+v, want the non-empty folder and its root reported", ures.DirsLeft)
	}
	b, err := os.ReadFile(userFile)
	if err != nil || string(b) != "mine" {
		t.Errorf("user file disturbed: %q, %v", b, err)
	}
}

// §17.6.5: THE REPLACED-TARGET SAFETY CASE. A target whose
// identity no longer matches the recorded source is skipped,
// reported, and left byte-identical; the manifest stays
// active; the rest of the build still undoes; a retry after
// removing the replacement completes the manifest.
func TestLinkUndoReplacedTargetSafety(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var videoTarget string
	for _, e := range plan.LinksToCreate {
		if e.Source == video {
			videoTarget = e.Target
		}
	}
	if err := os.Remove(videoTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(videoTarget, []byte("a-brand-new-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, replIno, _ := identityOf(t, videoTarget)
	ures, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Undone {
		t.Errorf("manifest marked undone while a replaced target remains")
	}
	if ures.Removed != 1 || len(ures.Skipped) != 1 {
		t.Errorf("undo = %+v, want 1 removed (subtitle) + 1 skipped (replaced video)", ures)
	}
	b, err := os.ReadFile(videoTarget)
	if err != nil || string(b) != "a-brand-new-file" {
		t.Fatalf("replacement file disturbed: %q, %v", b, err)
	}
	_, inoNow, _ := identityOf(t, videoTarget)
	if inoNow != replIno {
		t.Errorf("replacement inode changed across Undo")
	}
	// Retry after the user removes the replacement themselves:
	// the entry resolves as already gone and the build undoes.
	if err := os.Remove(videoTarget); err != nil {
		t.Fatal(err)
	}
	ures2, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing new is removed; the entries whose targets are
	// gone (the replaced video, and the subtitle removed by
	// the first pass) resolve as already gone.
	if !ures2.Undone || ures2.Removed != 0 || ures2.AlreadyGone < 1 {
		t.Errorf("retry undo = %+v, want undone with the missing entries already gone", ures2)
	}
}

// §17.6.6: a target removed by hand resolves as already gone.
func TestLinkUndoAlreadyGone(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(plan.LinksToCreate[0].Target); err != nil {
		t.Fatal(err)
	}
	ures, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.AlreadyGone != 1 || ures.Removed != 1 || !ures.Undone {
		t.Errorf("undo = %+v, want 1 already gone / 1 removed / undone", ures)
	}
}

// §17.6.7: Undo works from a fresh Service over the same data
// dir (manifests are disk state, plans are not).
func TestLinkUndoAfterRestart(t *testing.T) {
	requireIdentity(t)
	dataDir := t.TempDir()
	svc, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc2, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ures, err := svc2.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Removed != 2 || !ures.Undone {
		t.Errorf("restart undo = %+v, want 2 removed / undone", ures)
	}
	// Planning again on the restarted service still refuses to
	// reuse the consumed in-memory plan ID from before.
	if _, err := svc2.LinkApply(plan.ID); err == nil {
		t.Errorf("a pre-restart plan ID applied on the new service")
	}
}

// §17.6.8: a hand-edited manifest pointing outside its target
// root is skipped, removes nothing outside, and is retained.
func TestLinkUndoEscapingManifest(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	video := filepath.Join(src, "Film (2020).mkv")
	writeOrgFile(t, video, "film")
	outside := filepath.Join(base, "outside", "precious.mkv")
	writeOrgFile(t, outside, "precious")
	sd, si, _ := identityOf(t, video)
	m := &linkManifest{
		ID:         "link-test-escape",
		CreatedAt:  "2026-01-01T00:00:00Z",
		SourceRoot: src,
		TargetRoot: tgt,
		Links: []linkManifestEntry{
			{Source: video, Target: outside, SourceDev: sd, SourceIno: si, Done: true},
		},
	}
	if err := svc.writeLinkManifest(m); err != nil {
		t.Fatal(err)
	}
	ures, err := svc.LinkUndo(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Removed != 0 || len(ures.Skipped) != 1 || ures.Undone {
		t.Errorf("undo = %+v, want the escaping entry skipped and the manifest retained", ures)
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "precious" {
		t.Errorf("the outside file was disturbed: %q, %v", b, err)
	}
}

// §17.7: the operation guard, both directions, with a blocking
// link function holding Apply open.
func TestLinkGuardMutualExclusion(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)

	// Preview refuses while a scan is (marked) running.
	svc.mu.Lock()
	svc.state.Running = true
	svc.mu.Unlock()
	if _, err := svc.LinkPlan(src, tgt); err == nil || !strings.Contains(err.Error(), "scan is running") {
		t.Errorf("LinkPlan during scan: err = %v, want the scan-running refusal", err)
	}
	svc.mu.Lock()
	svc.state.Running = false
	svc.mu.Unlock()

	plan := mustSvcLinkPlan(t, svc, src, tgt)

	// Hold Apply open at the link seam.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	orig := createHardlinkFn
	createHardlinkFn = func(source, target string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return orig(source, target)
	}
	defer func() { createHardlinkFn = orig }()
	done := make(chan error, 1)
	go func() {
		res, err := svc.LinkApply(plan.ID)
		if err == nil && res.Created != 2 {
			err = errors.New("apply under guard created != 2")
		}
		done <- err
	}()
	<-entered
	// While link Apply holds the guard: scans, move-mode
	// applies, a second link Apply, and link Undo all refuse.
	if _, err := svc.StartScan(nil); err == nil || !strings.Contains(err.Error(), "organize operation is in progress") {
		t.Errorf("StartScan during link apply: err = %v", err)
	}
	if _, err := svc.OrganizeApply("whatever"); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("move apply during link apply: err = %v", err)
	}
	if _, err := svc.LinkApply(plan.ID); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("second link apply: err = %v", err)
	}
	if _, err := svc.LinkUndo("link-whatever"); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("link undo during link apply: err = %v", err)
	}
	if _, err := svc.LinkPlan(src, tgt); err == nil {
		t.Errorf("LinkPlan during link apply: expected a refusal")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("guarded apply: %v", err)
	}
	// Guard is free again.
	if err := svc.beginFileOp(); err != nil {
		t.Errorf("guard leaked after link apply: %v", err)
	} else {
		svc.endFileOp()
	}
}

// Move-mode Apply refuses while LINK Apply would hold the
// guard (the reverse direction of §17.7, without goroutines:
// hold the guard directly, as the hardening tests do).
func TestLinkUndoRefusesWhileFileOpHeld(t *testing.T) {
	svc := linkSvc(t)
	if err := svc.beginFileOp(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkUndo("link-whatever"); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("LinkUndo while guard held: err = %v", err)
	}
	if _, err := svc.LinkApply("whatever"); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("LinkApply while guard held: err = %v", err)
	}
	svc.endFileOp()
}

// The manifests list reflects created builds and their undo
// state (drives the Past link builds UI).
func TestLinkManifestsList(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	if list, err := svc.LinkManifests(); err != nil || len(list) != 0 {
		t.Fatalf("empty manifests = %v, %v", list, err)
	}
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.LinkManifests()
	if err != nil || len(list) != 1 {
		t.Fatalf("manifests = %v, %v", list, err)
	}
	sum := list[0]
	if sum.ID != res.ManifestID || sum.Links != 2 || sum.TotalLinks != 2 || sum.Status != "applied" || sum.Undone {
		t.Errorf("summary = %+v", sum)
	}
	if _, err := svc.LinkUndo(res.ManifestID); err != nil {
		t.Fatal(err)
	}
	list, err = svc.LinkManifests()
	if err != nil || len(list) != 1 || !list[0].Undone || list[0].Status != "undone" {
		t.Errorf("after undo: %+v, %v", list, err)
	}
}
