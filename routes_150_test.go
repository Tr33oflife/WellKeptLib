package main

// routes_150_test.go — v1.5.0 route pins: the catalog
// download, the sizes projection, and the Plex delete
// slice's three endpoints exist and answer in their
// documented shapes on an unconfigured, unscanned service
// (the behavior core tests exercise with fixtures and a
// mock Plex; this file pins the HTTP surface itself).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"plexfilemanager/internal/core"
)

func TestRoutes150(t *testing.T) {
	svc, err := core.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := newServer(svc, indexHTML, "")

	// Catalog: a valid, empty document with the schema
	// header and a download filename.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/export/catalog", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status = %d", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "wellkeptlib-catalog-") {
		t.Errorf("catalog disposition = %q", cd)
	}
	var doc struct {
		Schema        string `json:"schema"`
		SchemaVersion int    `json:"schema_version"`
		AppVersion    string `json:"app_version"`
		Summary       struct {
			Shows      int `json:"shows"`
			Thresholds struct {
				MovieBytes int64 `json:"movie_bytes"`
			} `json:"large_thresholds"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("catalog body unparseable: %v", err)
	}
	if doc.Schema != "wellkeptlib.catalog" || doc.SchemaVersion != 1 || doc.AppVersion != "1.5.0" {
		t.Errorf("catalog header = %+v", doc)
	}
	if doc.Summary.Thresholds.MovieBytes != 6<<30 {
		t.Errorf("catalog threshold = %d, want 6 GiB default", doc.Summary.Thresholds.MovieBytes)
	}

	// Sizes: the projection answers with the live thresholds.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sizes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sizes status = %d", rec.Code)
	}
	var sizes struct {
		Shows      []any `json:"shows"`
		Thresholds struct {
			SeasonBytes int64 `json:"season_bytes"`
		} `json:"thresholds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sizes); err != nil {
		t.Fatalf("sizes body unparseable: %v", err)
	}
	if sizes.Thresholds.SeasonBytes != 20<<30 {
		t.Errorf("sizes season threshold = %d, want 20 GiB default", sizes.Thresholds.SeasonBytes)
	}

	// Preflight with Plex unconfigured: 200, not ready, and
	// the problem says why.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plex/preflight", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status = %d", rec.Code)
	}
	var pf struct {
		Configured bool     `json:"configured"`
		Ready      bool     `json:"ready"`
		Problems   []string `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pf); err != nil {
		t.Fatalf("preflight body unparseable: %v", err)
	}
	if pf.Configured || pf.Ready || len(pf.Problems) == 0 {
		t.Errorf("unconfigured preflight = %+v", pf)
	}

	// Matches with Plex unconfigured: a clean 502 naming it.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plex/matches", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("matches status = %d, want 502", rec.Code)
	}

	// Delete with no entries named: 400 before any service
	// work happens.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/plex/delete", strings.NewReader(`{"items":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty delete status = %d, want 400", rec.Code)
	}
}
