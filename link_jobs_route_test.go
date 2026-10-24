package main

// link_jobs_route_test.go — v1.3.5: link Apply / Undo as
// background jobs at the HTTP surface. The POSTs answer 202
// with a job handle (never the final summary — a big build
// outlasted the proxy timeout waiting for one), the status
// endpoint polls to the terminal state carrying the SAME
// summary the synchronous endpoints used to return, an id
// that matches no recorded job is a clean 404, and "no job
// yet" is an honest state "none".

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"plexfilemanager/internal/core"
)

func TestLinkJobsRoute(t *testing.T) {
	svc, err := core.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := newServer(svc, indexHTML, "")

	post := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b))))
		return rec
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	// No job yet: state "none", not an error.
	rec := get("/api/organize/links/job")
	var st core.LinkJobStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil || st.State != "none" {
		t.Fatalf("job before any run: code %d body %s", rec.Code, rec.Body.String())
	}

	// A small real tree: one movie + its subtitle.
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "downloads", "The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv"), "matrix-video-content")
	write(filepath.Join(src, "downloads", "The.Matrix.1999.1080p.BluRay.x264-GROUP.en.srt"), "matrix-subtitle-content")

	rec = post("/api/organize/links/plan", map[string]string{"source_root": src, "target_root": tgt})
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: status %d, body %s", rec.Code, rec.Body.String())
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan response: %v (%s)", err, rec.Body.String())
	}

	// Apply now answers 202 + a job handle.
	rec = post("/api/organize/links/apply", map[string]string{"plan_id": plan.ID})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("apply: status %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var job core.LinkJobStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || job.ID == "" || job.Kind != "apply" {
		t.Fatalf("apply handle: %v (%s)", err, rec.Body.String())
	}

	waitJob := func(id string) core.LinkJobStatus {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			rec := get("/api/organize/links/job?id=" + id)
			if rec.Code != http.StatusOK {
				t.Fatalf("job poll: status %d, body %s", rec.Code, rec.Body.String())
			}
			var st core.LinkJobStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
				t.Fatalf("job poll body: %v", err)
			}
			if st.State == "done" || st.State == "failed" {
				return st
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("job %s did not finish", id)
		return core.LinkJobStatus{}
	}

	st = waitJob(job.ID)
	if st.State != "done" || st.ApplyResult == nil || st.ApplyResult.Created != 2 || st.ApplyResult.ManifestID == "" {
		t.Fatalf("apply job terminal = %+v, want done with 2 created + a manifest", st)
	}
	mid := st.ApplyResult.ManifestID

	// An id that matches no recorded job is a clean 404.
	rec = get("/api/organize/links/job?id=linkjob-bogus-1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown job id: status %d, want 404", rec.Code)
	}

	// Undo is a job too, and the latest-job read follows it.
	rec = post("/api/organize/links/undo", map[string]string{"manifest_id": mid})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("undo: status %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var ujob core.LinkJobStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &ujob); err != nil || ujob.Kind != "undo" {
		t.Fatalf("undo handle: %v (%s)", err, rec.Body.String())
	}
	ust := waitJob(ujob.ID)
	if ust.State != "done" || ust.UndoResult == nil || ust.UndoResult.Removed != 2 || !ust.UndoResult.Undone {
		t.Fatalf("undo job terminal = %+v, want done with 2 removed", ust)
	}
	rec = get("/api/organize/links/job")
	var latest core.LinkJobStatus
	if json.Unmarshal(rec.Body.Bytes(), &latest) != nil || latest.ID != ujob.ID {
		t.Fatalf("latest job = %s, want the undo job %s", latest.ID, ujob.ID)
	}
	if _, err := os.Stat(tgt); !os.IsNotExist(err) {
		t.Errorf("target tree still exists after the undo job (err %v)", err)
	}

	// Start errors keep the synchronous surface: unknown plan
	// is a 409, unknown manifest a 400, and neither leaves a
	// job behind (the latest record is still the undo job).
	rec = post("/api/organize/links/apply", map[string]string{"plan_id": "plan-nope"})
	if rec.Code != http.StatusConflict {
		t.Errorf("apply unknown plan: status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	rec = post("/api/organize/links/undo", map[string]string{"manifest_id": "link-nope"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("undo unknown manifest: status %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	rec = get("/api/organize/links/job")
	latest = core.LinkJobStatus{}
	if json.Unmarshal(rec.Body.Bytes(), &latest) != nil || latest.ID != ujob.ID {
		t.Errorf("latest job after refused starts = %s, want still %s", latest.ID, ujob.ID)
	}
}
