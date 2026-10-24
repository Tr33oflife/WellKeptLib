package main

// link_problems_route_test.go — the v1.3.0 problems download
// at the HTTP surface: a planned link plan exports as an
// attachment with the schema fields, an unknown plan id is a
// clean 404 JSON error, and the export does not consume the
// plan (a second download answers identically).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plexfilemanager/internal/core"
)

func TestLinkProblemsRoute(t *testing.T) {
	svc, err := core.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := newServer(svc, indexHTML, "")

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
	// One collision (720p/1080p), one not-identified, one
	// target conflict — the §17.1 problem trio in miniature.
	write(filepath.Join(src, "dups", "Film (2020) 720p.mkv"), "film-720")
	write(filepath.Join(src, "dups", "Film (2020) 1080p.mkv"), "film-1080-longer")
	write(filepath.Join(src, "random-file.mkv"), "mystery")
	write(filepath.Join(src, "Control Film (2023).mkv"), "control-film")
	write(filepath.Join(tgt, "Control Film (2023)", "Control Film (2023).mkv"), "someone-elses-file")

	planBody, _ := json.Marshal(map[string]string{"source_root": src, "target_root": tgt})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/organize/links/plan", strings.NewReader(string(planBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: status %d, body %s", rec.Code, rec.Body.String())
	}
	var plan struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || plan.ID == "" {
		t.Fatalf("plan response: %v (%s)", err, rec.Body.String())
	}

	get := func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/organize/links/problems?plan_id="+id, nil))
		return rec
	}
	rec = get(plan.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("problems: status %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment; filename=wellkeptlib-link-problems-") || !strings.HasSuffix(cd, ".json") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	var doc core.LinkDiagnostics
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("problems body does not parse: %v", err)
	}
	if doc.Schema != core.LinkDiagnosticsSchema || doc.SchemaVersion != 1 || doc.AppVersion != core.Version {
		t.Errorf("doc identity = %q v%d app %q", doc.Schema, doc.SchemaVersion, doc.AppVersion)
	}
	if doc.Counts.CollisionGroups != 1 || doc.Counts.NotIdentified != 1 || doc.Counts.ConflictTargets != 1 {
		t.Errorf("doc counts = %+v, want 1 collision / 1 not-identified / 1 target-conflict", doc.Counts)
	}
	kinds := map[string]int{}
	for _, c := range doc.Conflicts {
		kinds[c.Kind]++
	}
	if kinds["collision"] != 1 || kinds["target_conflict"] != 1 || len(doc.NotFound) != 1 {
		t.Errorf("doc sections = kinds %v, not_found %d", kinds, len(doc.NotFound))
	}
	// Not consumed: a second download answers 200 too.
	if rec := get(plan.ID); rec.Code != http.StatusOK {
		t.Errorf("second download: status %d — the export must not consume the plan", rec.Code)
	}
	// Unknown plan: a clean 404 JSON error, not a download.
	rec = get("plan-20000101-000000-deadbeef")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plan: status %d, want 404", rec.Code)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || !strings.Contains(e.Error, "unknown link plan") {
		t.Errorf("unknown plan body = %s", rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("unknown plan carried a download header: %q", cd)
	}
}
