package core

// linkapply_zerosize_test.go — v1.3.3: a plan-recorded source
// size of 0 is "unknown", not a measurement. On the owner's
// real tree, whole packs report size 0 to every stat the app
// makes while device+inode from the same stat read fine, so a
// later stat reporting the real size must not refuse the
// entry's hardlink at Apply time — the owner's rule: "if a
// file has 0 bites a hardlin should still be made." Identity
// (dev+ino) and mtime still guard the entry exactly, and a
// recorded size > 0 keeps the exact size check.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// zeroSizeTree builds a one-episode tree whose file starts
// with the given content; returns source root, target root,
// and the episode path.
func zeroSizeTree(t *testing.T, content string) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	ep := filepath.Join(src, "show", "My Show S01E01.mkv")
	writeOrgFile(t, ep, content)
	return src, tgt, ep
}

func mustLinkEntryFor(t *testing.T, plan *LinkPlan, source string) LinkEntry {
	t.Helper()
	e := linkEntryFor(plan, source)
	if e == nil {
		t.Fatalf("plan has no link entry for %s (stats %+v)", source, plan.Stats)
	}
	return *e
}

func restoreRecordedMtime(t *testing.T, path string, unixNano int64) {
	t.Helper()
	mt := time.Unix(0, unixNano)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func skippedSourceChanged(res *LinkApplyResult, source string) bool {
	for _, sk := range res.Skipped {
		if sk.Source == source && sk.Category == "source-changed" {
			return true
		}
	}
	return false
}

// v1.3.3 discriminator: the plan recorded size 0 (the file was
// genuinely 0 bytes at preview); by Apply time the file has
// real bytes, but it is the SAME file (identity unchanged) with
// the SAME mtime the plan recorded. The size difference alone
// must not refuse the link. (Fails on v1.3.2, where the apply
// revalidation treats any size difference as source-changed.)
func TestLinkApplyZeroRecordedSizeGrownStillLinks(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, ep := zeroSizeTree(t, "")
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	entry := mustLinkEntryFor(t, plan, ep)
	if entry.SourceSize != 0 {
		t.Fatalf("recorded SourceSize = %d, want 0 for the 0-byte source", entry.SourceSize)
	}
	if err := os.WriteFile(ep, []byte("now-it-has-real-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreRecordedMtime(t, ep, entry.SourceMtimeUnix)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if skippedSourceChanged(res, ep) {
		t.Fatalf("the grown recorded-0 source was refused as source-changed (skipped %+v)", res.Skipped)
	}
	if res.Created != 1 {
		t.Fatalf("created = %d, want 1 (result %+v)", res.Created, res)
	}
	sd, si, _ := identityOf(t, ep)
	td, ti, _ := identityOf(t, entry.Target)
	if sd != td || si != ti {
		t.Errorf("target %s identity (%d,%d) != source (%d,%d)", entry.Target, td, ti, sd, si)
	}
}

// The guard still guards: a recorded size > 0 with a changed
// size (mtime restored, identity unchanged) is still refused
// as source-changed.
func TestLinkApplyNonzeroRecordedSizeChangedStillRefused(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, ep := zeroSizeTree(t, "original-video-bytes")
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	entry := mustLinkEntryFor(t, plan, ep)
	if entry.SourceSize == 0 {
		t.Fatalf("recorded SourceSize = 0, want > 0 for the non-empty source")
	}
	if err := os.WriteFile(ep, []byte("a-different-and-longer-video-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreRecordedMtime(t, ep, entry.SourceMtimeUnix)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 {
		t.Errorf("created = %d, want 0 (recorded size > 0 and the size changed)", res.Created)
	}
	if !skippedSourceChanged(res, ep) {
		t.Errorf("skipped = %+v, want the episode as source-changed", res.Skipped)
	}
	if _, err := os.Lstat(entry.Target); !os.IsNotExist(err) {
		t.Errorf("the refused entry's target exists")
	}
}

// End to end: a genuinely 0-byte identified episode is
// selected by the plan, Apply creates a real hardlink to it
// (same inode, link count 2), and Undo removes the link and
// leaves the 0-byte source in place.
func TestLinkApplyZeroByteEpisodeEndToEnd(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, ep := zeroSizeTree(t, "")
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	entry := mustLinkEntryFor(t, plan, ep)
	if entry.SourceSize != 0 {
		t.Fatalf("recorded SourceSize = %d, want 0", entry.SourceSize)
	}
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.ManifestID == "" {
		t.Fatalf("apply = %+v, want 1 created with a manifest", res)
	}
	sd, si, nlink := identityOf(t, ep)
	td, ti, _ := identityOf(t, entry.Target)
	if sd != td || si != ti {
		t.Errorf("target %s identity (%d,%d) != source (%d,%d)", entry.Target, td, ti, sd, si)
	}
	if nlink != 2 {
		t.Errorf("source nlink = %d, want 2 after the link", nlink)
	}
	ures, err := svc.LinkUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if ures.Removed != 1 || !ures.Undone {
		t.Fatalf("undo = %+v, want 1 removed and the manifest undone", ures)
	}
	if _, err := os.Lstat(entry.Target); !os.IsNotExist(err) {
		t.Errorf("the created link survived Undo")
	}
	info, err := os.Stat(ep)
	if err != nil {
		t.Fatalf("source missing after Undo: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("source size = %d after Undo, want 0", info.Size())
	}
}

// The mtime term is untouched by the v1.3.3 fix: a recorded-0
// entry whose mtime changed is still refused as
// source-changed.
func TestLinkApplyZeroRecordedSizeMtimeChangedRefused(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, ep := zeroSizeTree(t, "")
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	entry := mustLinkEntryFor(t, plan, ep)
	if entry.SourceSize != 0 {
		t.Fatalf("recorded SourceSize = %d, want 0", entry.SourceSize)
	}
	later := time.Unix(0, entry.SourceMtimeUnix).Add(2 * time.Hour)
	if err := os.Chtimes(ep, later, later); err != nil {
		t.Fatal(err)
	}
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 {
		t.Errorf("created = %d, want 0 (mtime changed since the preview)", res.Created)
	}
	if !skippedSourceChanged(res, ep) {
		t.Errorf("skipped = %+v, want the episode as source-changed", res.Skipped)
	}
	if _, err := os.Lstat(entry.Target); !os.IsNotExist(err) {
		t.Errorf("the refused entry's target exists")
	}
}
