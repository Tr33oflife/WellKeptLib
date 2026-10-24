package core

// linkjobs_test.go — v1.3.5: link Apply / Undo as background
// jobs. The job path must be the SAME build the synchronous
// engine does (equivalence), must report a running state and
// refuse concurrent work while it runs (the incident's blind
// lock), must round-trip an undo, must keep the v1.3.3
// zero-byte guarantee, and must finish — not hang — when
// entries fail in-band.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitLinkJob polls a job to a terminal state.
func waitLinkJob(t *testing.T, svc *Service, id string) *LinkJobStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := svc.LinkJob(id)
		if err != nil {
			t.Fatalf("LinkJob(%s): %v", id, err)
		}
		if st.State == "done" || st.State == "failed" {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("link job %s did not finish within 15s", id)
	return nil
}

// Before anything runs there is no job: state "none", no ID —
// and asking for a specific (unknown) job while a record
// exists is an error, while no record at all is just "none".
func TestLinkJobNoneBeforeAnyJob(t *testing.T) {
	svc := linkSvc(t)
	st, err := svc.LinkJob("")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "none" || st.ID != "" {
		t.Errorf("status = %+v, want state none with no id", st)
	}
	st, err = svc.LinkJob("linkjob-whatever-1")
	if err != nil || st.State != "none" {
		t.Errorf("unknown id with no record = %+v, %v; want state none, no error", st, err)
	}
}

// The equivalence pin: an apply run as a job produces the
// same result, the same manifest (modulo its ID/timestamps),
// and the same target tree as the synchronous apply of the
// same plan on an identical tree.
func TestLinkApplyJobMatchesSyncApply(t *testing.T) {
	requireIdentity(t)
	svcSync := linkSvc(t)
	svcJob := linkSvc(t)
	srcS, tgtS, _, _ := simpleLinkTree(t)
	srcJ, tgtJ, _, _ := simpleLinkTree(t)

	planS := mustSvcLinkPlan(t, svcSync, srcS, tgtS)
	resS, err := svcSync.LinkApply(planS.ID)
	if err != nil {
		t.Fatal(err)
	}
	planJ := mustSvcLinkPlan(t, svcJob, srcJ, tgtJ)
	job, err := svcJob.StartLinkApplyJob(planJ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != "apply" || job.State != "running" || job.Total != 2 || job.PlanID != planJ.ID {
		t.Fatalf("job handle = %+v, want a running apply job over 2 entries", job)
	}
	st := waitLinkJob(t, svcJob, job.ID)
	if st.State != "done" || st.Error != "" {
		t.Fatalf("job terminal = %+v, want done with no job error", st)
	}
	resJ := st.ApplyResult
	if resJ == nil {
		t.Fatal("terminal job carries no apply result")
	}
	if resJ.Created != resS.Created || resJ.AlreadyLinked != resS.AlreadyLinked ||
		resJ.CreatedDirs != resS.CreatedDirs || resJ.Partial != resS.Partial ||
		len(resJ.Skipped) != len(resS.Skipped) || len(resJ.Failed) != len(resS.Failed) {
		t.Errorf("job result %+v != sync result %+v", resJ, resS)
	}
	if st.Created != resJ.Created || st.Processed != st.Total || st.FinishedAt == "" {
		t.Errorf("terminal status = %+v, want counts from the result and processed == total", st)
	}
	// Manifests agree in effect: same links (by root-relative
	// target) all done, same created dirs.
	mS, err := svcSync.loadLinkManifest(resS.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	mJ, err := svcJob.loadLinkManifest(resJ.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	relLinks := func(m *linkManifest, root string) map[string]bool {
		out := map[string]bool{}
		for _, l := range m.Links {
			rel, rerr := filepath.Rel(root, l.Target)
			if rerr != nil {
				t.Fatal(rerr)
			}
			out[rel] = l.Done
		}
		return out
	}
	ls, lj := relLinks(mS, tgtS), relLinks(mJ, tgtJ)
	if len(ls) != len(lj) {
		t.Fatalf("manifest links: sync %v, job %v", ls, lj)
	}
	for rel, done := range ls {
		if lj[rel] != done {
			t.Errorf("manifest link %s: sync done=%v, job done=%v", rel, done, lj[rel])
		}
	}
	relDirs := func(m *linkManifest, root string) map[string]bool {
		out := map[string]bool{}
		for _, d := range m.CreatedDirs {
			rel, rerr := filepath.Rel(root, d.Path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			out[rel] = d.Created
		}
		return out
	}
	ds, dj := relDirs(mS, tgtS), relDirs(mJ, tgtJ)
	if len(ds) != len(dj) {
		t.Fatalf("manifest dirs: sync %v, job %v", ds, dj)
	}
	for rel, created := range ds {
		if dj[rel] != created {
			t.Errorf("manifest dir %s: sync created=%v, job created=%v", rel, created, dj[rel])
		}
	}
	// Target trees have the same shape, and both sources carry
	// their extra link.
	snapS := snapshotSourceTree(t, tgtS)
	snapJ := snapshotSourceTree(t, tgtJ)
	if len(snapS) != len(snapJ) {
		t.Fatalf("target trees differ in size: sync %d entries, job %d", len(snapS), len(snapJ))
	}
	for rel, e := range snapS {
		g, ok := snapJ[rel]
		if !ok || g.Hash != e.Hash || g.IsDir != e.IsDir {
			t.Errorf("target entry %s differs between sync and job builds", rel)
		}
	}
}

// While a job runs: the status endpoint shows it running,
// and every concurrent organize/scan start refuses with the
// existing errors — the file-operation guard is the job's
// for its whole life.
func TestLinkApplyJobRunningRefusalsAndProgress(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)

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

	job, err := svc.StartLinkApplyJob(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-entered // the engine is inside the link seam: the job is live

	st, err := svc.LinkJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" || st.Total != 2 || st.Current == "" {
		t.Errorf("running status = %+v, want running over 2 entries with a current entry", st)
	}
	if _, err := svc.StartLinkApplyJob(plan.ID); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("second apply job: err = %v, want the file-op refusal", err)
	}
	if _, err := svc.StartLinkUndoJob("link-whatever"); err == nil || !strings.Contains(err.Error(), "another organize operation is in progress") {
		t.Errorf("undo job during apply job: err = %v, want the file-op refusal", err)
	}
	if _, err := svc.LinkPlan(src, tgt); err == nil {
		t.Errorf("LinkPlan during a link job: expected a refusal")
	}
	if _, err := svc.StartScan(nil); err == nil || !strings.Contains(err.Error(), "organize operation is in progress") {
		t.Errorf("StartScan during a link job: err = %v", err)
	}
	// The refused starts left the running job's record alone.
	st, err = svc.LinkJob("")
	if err != nil || st.ID != job.ID || st.State != "running" {
		t.Errorf("latest job after refusals = %+v, %v", st, err)
	}
	close(release)
	st = waitLinkJob(t, svc, job.ID)
	if st.State != "done" || st.ApplyResult == nil || st.ApplyResult.Created != 2 {
		t.Fatalf("terminal = %+v, want done with 2 created", st)
	}
	// The finished record stays readable (reattach), by id and
	// as latest; a wrong id errors.
	if _, err := svc.LinkJob("linkjob-bogus-99"); err == nil {
		t.Errorf("LinkJob with a wrong id: expected an error")
	}
	latest, err := svc.LinkJob("")
	if err != nil || latest.ID != job.ID || latest.State != "done" {
		t.Errorf("latest finished job = %+v, %v", latest, err)
	}
	// Guard is free again.
	if err := svc.beginFileOp(); err != nil {
		t.Errorf("guard leaked after job: %v", err)
	} else {
		svc.endFileOp()
	}
}

// Undo as a job: apply (job) -> undo (job) -> the source tree
// is exactly what it was, link counts restored.
func TestLinkUndoJobRoundTrip(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, video, _ := simpleLinkTree(t)
	before := snapshotSourceTree(t, src)

	plan := mustSvcLinkPlan(t, svc, src, tgt)
	job, err := svc.StartLinkApplyJob(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := waitLinkJob(t, svc, job.ID)
	if st.State != "done" || st.ApplyResult == nil || st.ApplyResult.Created != 2 {
		t.Fatalf("apply job = %+v", st)
	}
	mid := st.ApplyResult.ManifestID
	if _, _, n := identityOf(t, video); n != 2 {
		t.Fatalf("video nlink after apply job = %d, want 2", n)
	}

	ujob, err := svc.StartLinkUndoJob(mid)
	if err != nil {
		t.Fatal(err)
	}
	if ujob.Kind != "undo" || ujob.ManifestID != mid || ujob.Total != 2 {
		t.Fatalf("undo handle = %+v, want an undo job over 2 entries", ujob)
	}
	ust := waitLinkJob(t, svc, ujob.ID)
	if ust.State != "done" || ust.UndoResult == nil {
		t.Fatalf("undo job = %+v", ust)
	}
	if ust.UndoResult.Removed != 2 || !ust.UndoResult.Undone || ust.Removed != 2 {
		t.Errorf("undo result = %+v (status removed %d), want 2 removed, undone", ust.UndoResult, ust.Removed)
	}
	if _, err := os.Stat(tgt); !os.IsNotExist(err) {
		t.Errorf("target tree still exists after undo job (err %v)", err)
	}
	assertSnapshotsEqual(t, before, snapshotSourceTree(t, src), "source after apply+undo jobs")
	if _, _, n := identityOf(t, video); n != 1 {
		t.Errorf("video nlink after undo job = %d, want 1", n)
	}
}

// The v1.3.3 guarantee through the job path: a source recorded
// as 0 bytes that has since gained real bytes (same file, same
// mtime) still gets its hardlink.
func TestLinkJobZeroByteStillLinks(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, ep := zeroSizeTree(t, "")
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	entry := mustLinkEntryFor(t, plan, ep)
	if entry.SourceSize != 0 {
		t.Fatalf("recorded SourceSize = %d, want 0", entry.SourceSize)
	}
	if err := os.WriteFile(ep, []byte("now-it-has-real-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreRecordedMtime(t, ep, entry.SourceMtimeUnix)
	job, err := svc.StartLinkApplyJob(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := waitLinkJob(t, svc, job.ID)
	if st.State != "done" || st.ApplyResult == nil {
		t.Fatalf("job = %+v", st)
	}
	if skippedSourceChanged(st.ApplyResult, ep) {
		t.Fatalf("the grown recorded-0 source was refused as source-changed (skipped %+v)", st.ApplyResult.Skipped)
	}
	if st.ApplyResult.Created != 1 {
		t.Fatalf("created = %d, want 1 (result %+v)", st.ApplyResult.Created, st.ApplyResult)
	}
	sd, si, _ := identityOf(t, ep)
	td, ti, _ := identityOf(t, entry.Target)
	if sd != td || si != ti {
		t.Errorf("target identity (%d,%d) != source (%d,%d)", td, ti, sd, si)
	}
}

// A job whose entries fail in-band completes as a job: done,
// with the failure counted — never a hang, never a job-level
// failure for per-entry problems.
func TestLinkJobFailingEntriesComplete(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	orig := createHardlinkFn
	createHardlinkFn = func(source, target string) error {
		if strings.HasSuffix(target, ".srt") {
			return errors.New("injected link failure")
		}
		return orig(source, target)
	}
	defer func() { createHardlinkFn = orig }()
	job, err := svc.StartLinkApplyJob(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := waitLinkJob(t, svc, job.ID)
	if st.State != "done" {
		t.Fatalf("job state = %q (error %q), want done — entry failures are in-band", st.State, st.Error)
	}
	if st.Failed != 1 || st.Created != 1 || st.Processed != st.Total {
		t.Errorf("terminal counts = %+v, want 1 failed / 1 created / processed == total", st)
	}
	if st.ApplyResult == nil || len(st.ApplyResult.Failed) != 1 {
		t.Errorf("apply result = %+v, want exactly the one failed entry", st.ApplyResult)
	}
}

// Start refusals are exact and leave nothing behind: no job
// record, guard free.
func TestLinkJobStartRefusalsLeaveNoJob(t *testing.T) {
	requireIdentity(t)
	svc := linkSvc(t)
	if _, err := svc.StartLinkApplyJob("plan-nope"); err == nil || !strings.Contains(err.Error(), "unknown link plan") {
		t.Errorf("unknown plan start: err = %v", err)
	}
	if _, err := svc.StartLinkUndoJob("link-nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown manifest start: err = %v", err)
	}
	if st, err := svc.LinkJob(""); err != nil || st.State != "none" {
		t.Errorf("after refused starts: status = %+v, %v — want state none", st, err)
	}
	if err := svc.beginFileOp(); err != nil {
		t.Errorf("guard held after refused starts: %v", err)
	} else {
		svc.endFileOp()
	}

	// An already-undone build refuses at start, with the
	// synchronous call's error — not as a failed job.
	src, tgt, _, _ := simpleLinkTree(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	res, err := svc.LinkApply(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LinkUndo(res.ManifestID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartLinkUndoJob(res.ManifestID); err == nil || !strings.Contains(err.Error(), "already undone") {
		t.Errorf("already-undone start: err = %v", err)
	}
	if st, _ := svc.LinkJob(""); st.State != "none" {
		t.Errorf("after already-undone refusal: status = %+v, want state none", st)
	}
}
