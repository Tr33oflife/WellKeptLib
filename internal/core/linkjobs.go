package core

// linkjobs.go — link Apply / Undo as background jobs (v1.3.5).
//
// THE INCIDENT (the owner's live report on v1.3.3): building
// a ~9,000-link plan through the synchronous endpoint
// outlasted the nginx proxy timeout — the browser got
// HTTP 504 while the apply kept running server-side behind
// the file-operation guard, invisible: no progress, no
// result, and a reload showed nothing but the guard's
// refusal when he retried the preview.
//
// The fix is the scan idiom (StartScan -> goroutine ->
// ScanState) applied to link operations. POST starts the SAME
// engine the synchronous Service methods run
// (linkApplyLocked / linkUndoLocked) in a goroutine and
// returns a job handle at once; the client polls
// GET /api/organize/links/job for live progress and, at the
// end, the final summary — the exact LinkApplyResult /
// LinkUndoResult the synchronous endpoints return, attached
// to the terminal job status.
//
// Guarantees carried over, unchanged:
//   - The file-operation guard is held for the job's whole
//     life: the start methods take it synchronously (so a
//     second start while a job — or a scan, or any other file
//     operation — runs refuses with the SAME errors the
//     synchronous calls return, and leaves no job record),
//     and the job's goroutine releases it when the engine
//     finishes.
//   - Exactly one link job at a time: a new start is
//     impossible while one runs, because the guard is.
//   - Job state is in-memory only, like scan state: a server
//     restart mid-job leaves no phantom "running" job. The
//     partial build stays safe — created links are on disk
//     and recorded in their manifest, and a fresh preview
//     classifies them as already-linked.

import (
	"fmt"
	"time"
)

// LinkJobStatus is the public snapshot of one link Apply or
// Undo job: the start endpoints' 202 body and the status
// endpoint's payload. While running it carries live progress
// (phase, processed/total for the phase, the outcome counts
// so far, the entry being processed); at a terminal state it
// carries Error (job-level failure) and the SAME final
// summary the synchronous endpoints return (ApplyResult or
// UndoResult), so completion renders from one shape whether
// the UI watched the job or reattached after a reload.
type LinkJobStatus struct {
	ID            string           `json:"id"`
	Kind          string           `json:"kind"`            // apply | undo
	State         string           `json:"state"`           // running | done | failed
	Phase         string           `json:"phase,omitempty"` // checking | folders | building | removing
	Total         int              `json:"total"`
	Processed     int              `json:"processed"`
	Created       int              `json:"created"`
	AlreadyLinked int              `json:"already_linked"`
	Removed       int              `json:"removed"`
	AlreadyGone   int              `json:"already_gone"`
	Skipped       int              `json:"skipped"`
	Failed        int              `json:"failed"`
	Current       string           `json:"current,omitempty"`
	StartedAt     string           `json:"started_at,omitempty"`
	FinishedAt    string           `json:"finished_at,omitempty"`
	Error         string           `json:"error,omitempty"`
	PlanID        string           `json:"plan_id,omitempty"`
	ManifestID    string           `json:"manifest_id,omitempty"`
	ApplyResult   *LinkApplyResult `json:"apply_result,omitempty"`
	UndoResult    *LinkUndoResult  `json:"undo_result,omitempty"`
}

// linkProgSnap is one progress report from a running link
// engine: the phase and entry in flight, how much of the
// phase is done, and the outcome counts so far — read by the
// engine from its own result struct, so the job record only
// ever copies values the final summary will agree with.
type linkProgSnap struct {
	Phase         string
	Current       string
	Processed     int
	Total         int
	Created       int
	AlreadyLinked int
	Removed       int
	AlreadyGone   int
	Skipped       int
	Failed        int
}

// linkProgressFunc receives progress snapshots from a link
// engine while it runs (nil = the synchronous form: no
// reporting).
type linkProgressFunc func(linkProgSnap)

// newLinkJobLocked registers a fresh running job record on
// the Service and returns the record. Caller holds mu and
// fills in the kind-specific fields before copying a
// snapshot for return.
func (s *Service) newLinkJobLocked(kind string) *LinkJobStatus {
	s.linkJobSeq++
	job := &LinkJobStatus{
		ID:        fmt.Sprintf("linkjob-%s-%d", time.Now().UTC().Format("20060102-150405"), s.linkJobSeq),
		Kind:      kind,
		State:     "running",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.linkJob = job
	return job
}

// jobProgress returns the progress sink for one job: each
// engine snapshot is copied into the job record under mu.
// Reports arriving after the job finished (or for a
// superseded id) are dropped — a record can only be replaced
// once its job is done and the guard is free, so this is
// belt and braces.
func (s *Service) jobProgress(id string) linkProgressFunc {
	return func(snap linkProgSnap) {
		s.mu.Lock()
		defer s.mu.Unlock()
		j := s.linkJob
		if j == nil || j.ID != id || j.State != "running" {
			return
		}
		j.Phase = snap.Phase
		j.Current = snap.Current
		j.Processed = snap.Processed
		j.Total = snap.Total
		j.Created = snap.Created
		j.AlreadyLinked = snap.AlreadyLinked
		j.Removed = snap.Removed
		j.AlreadyGone = snap.AlreadyGone
		j.Skipped = snap.Skipped
		j.Failed = snap.Failed
	}
}

// finishLinkJob records a job's terminal state: the engine's
// result is attached verbatim and the counts are recomputed
// from it, so the terminal snapshot is exact even if the last
// progress report predates the final entries. A job-level
// error marks the job failed (a result may still be attached
// — link Undo returns one alongside its manifest error).
// Caller does not hold mu.
func (s *Service) finishLinkJob(id string, applyRes *LinkApplyResult, undoRes *LinkUndoResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.linkJob
	if j == nil || j.ID != id {
		return
	}
	j.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	j.Current = ""
	if applyRes != nil {
		j.ApplyResult = applyRes
		j.ManifestID = applyRes.ManifestID
		j.Created = applyRes.Created
		j.AlreadyLinked = applyRes.AlreadyLinked
		j.Skipped = len(applyRes.Skipped)
		j.Failed = len(applyRes.Failed)
	}
	if undoRes != nil {
		j.UndoResult = undoRes
		j.Removed = undoRes.Removed
		j.AlreadyGone = undoRes.AlreadyGone
		j.Skipped = len(undoRes.Skipped)
		j.Failed = 0
	}
	if err != nil {
		j.State = "failed"
		j.Error = err.Error()
		return
	}
	j.State = "done"
	j.Processed = j.Total
}

// StartLinkApplyJob starts a link Apply of a previewed plan
// as a background job and returns its handle at once. The
// refusal surface is the synchronous call's: a scan running,
// another file operation running (including another link
// job), or an unknown plan id all error BEFORE anything
// starts, and none of them leaves a job record behind.
func (s *Service) StartLinkApplyJob(planID string) (*LinkJobStatus, error) {
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	plan, ok := s.linkPlans[planID]
	if !ok {
		s.mu.Unlock()
		s.endFileOp()
		return nil, fmt.Errorf("unknown link plan %q — preview the plan again", planID)
	}
	rec := s.newLinkJobLocked("apply")
	rec.PlanID = planID
	rec.Phase = "checking"
	rec.Total = len(plan.LinksToCreate)
	snap := *rec
	id := rec.ID
	s.mu.Unlock()
	go func() {
		defer s.endFileOp()
		res, err := s.linkApplyLocked(planID, s.jobProgress(id))
		s.finishLinkJob(id, res, nil, err)
	}()
	return &snap, nil
}

// StartLinkUndoJob starts a link Undo of one manifest as a
// background job. Like the apply start, it refuses up front
// with the synchronous call's exact errors — a missing or
// already-undone manifest is a start error, never a job that
// fails a moment later.
func (s *Service) StartLinkUndoJob(manifestID string) (*LinkJobStatus, error) {
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	// Peek the manifest under the guard: the engine will load
	// it again, but the refusal belongs to the start.
	m, err := s.loadLinkManifest(manifestID)
	if err != nil {
		s.endFileOp()
		return nil, err
	}
	if m.UndoneAt != "" {
		s.endFileOp()
		return nil, fmt.Errorf("link build %s was already undone at %s", m.ID, m.UndoneAt)
	}
	total := 0
	for i := range m.Links {
		if !m.Links[i].Undone {
			total++
		}
	}
	s.mu.Lock()
	rec := s.newLinkJobLocked("undo")
	rec.ManifestID = manifestID
	rec.Phase = "removing"
	rec.Total = total
	snap := *rec
	id := rec.ID
	s.mu.Unlock()
	go func() {
		defer s.endFileOp()
		res, err := s.linkUndoLocked(manifestID, s.jobProgress(id))
		s.finishLinkJob(id, nil, res, err)
	}()
	return &snap, nil
}

// LinkJob returns the latest link job's snapshot: the running
// job, or the most recent finished one — the record is kept
// after completion so a page that reloads can still render
// the summary (reattach). With an id it answers only for
// that job; an id that does not match the recorded job
// errors (the status endpoint maps it to 404). When no job
// has run — including after a server restart, which forgets
// jobs by design — it returns a status with State "none"
// and no ID.
func (s *Service) LinkJob(id string) (*LinkJobStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.linkJob == nil {
		return &LinkJobStatus{State: "none"}, nil
	}
	if id != "" && id != s.linkJob.ID {
		return nil, fmt.Errorf("unknown link job %q", id)
	}
	cp := *s.linkJob
	return &cp, nil
}
