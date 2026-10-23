package main

// main_test.go — the -scan-once exit-code table (FEATURE-PLAN
// §7), pinned at the decision function: main() itself maps a
// data-dir lock refusal and a no-roots config to exit 2 before
// a scan exists (see runScanOnce), which the shipped-binary
// smoke exercises end to end.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"plexfilemanager/internal/core"
)

func TestExitCodeTable(t *testing.T) {
	clean := core.ScanSummary{}
	findings := core.ScanSummary{DuplicateReclaimable: 5}
	healthOnly := core.ScanSummary{HealthFindings: 2}
	apparentOnly := core.ScanSummary{DuplicateWasted: 9} // apparent waste does NOT gate
	cases := []struct {
		name string
		err  string
		sum  core.ScanSummary
		fail bool
		want int
	}{
		{"completed clean", "", clean, false, 0},
		{"completed clean, gate on", "", clean, true, 0},
		{"completed with findings, gate off", "", findings, false, 0},
		{"completed with reclaimable, gate on", "", findings, true, 3},
		{"completed with health findings, gate on", "", healthOnly, true, 3},
		{"apparent waste alone does not gate", "", apparentOnly, true, 0},
		{"scan failed", "boom", clean, false, 1},
		{"scan failed beats the gate", "boom", findings, true, 1},
	}
	for _, c := range cases {
		if got := exitCodeForScan(c.err, c.sum, c.fail); got != c.want {
			t.Errorf("%s: exitCodeForScan = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestIconRoutesServeTheEmbeddedSet pins the v1.2.0 favicon
// routes: before this release /favicon.ico had no route and
// 404'd. Each asset must answer 200 with its content type and
// exactly the embedded bytes (the mark ships inside the binary
// like the page does).
func TestIconRoutesServeTheEmbeddedSet(t *testing.T) {
	svc, err := core.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := newServer(svc, indexHTML, "")
	cases := []struct {
		path        string
		contentType string
		want        []byte
	}{
		{"/favicon.ico", "image/x-icon", iconICO},
		{"/favicon.svg", "image/svg+xml", iconSVG},
		{"/favicon-32x32.png", "image/png", iconPNG32},
		{"/apple-touch-icon.png", "image/png", iconAppleTouch},
		{"/android-chrome-192x192.png", "image/png", iconPNG192},
		{"/android-chrome-512x512.png", "image/png", iconPNG512},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", c.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != c.contentType {
			t.Errorf("GET %s: Content-Type %q, want %q", c.path, ct, c.contentType)
		}
		if !bytes.Equal(rec.Body.Bytes(), c.want) {
			t.Errorf("GET %s: body is not the embedded asset (%d vs %d bytes)",
				c.path, rec.Body.Len(), len(c.want))
		}
		if len(c.want) < 100 {
			t.Errorf("GET %s: embedded asset suspiciously small (%d bytes)", c.path, len(c.want))
		}
	}
}
