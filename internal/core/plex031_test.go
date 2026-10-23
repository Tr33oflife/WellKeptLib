package core

// plex031_test.go — tests for the v0.3.1 manual-connection fixes:
// scheme-less URL saves (normalizePlexURL via SavePlexSettings)
// and test-what's-typed (TestPlexWith). httptest mocks only — no
// real Plex server is touched.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSavePlexSettingsURLNormalization: a bare "host:port" — the
// address shape a user actually types — must save as http://…,
// an https URL keeps its scheme, trailing slashes are stripped,
// and junk is still rejected (with the save failing, loudly).
func TestSavePlexSettingsURLNormalization(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	good := []struct{ in, want string }{
		{"192.168.1.50:32400", "http://192.168.1.50:32400"},
		{"127.0.0.1:32400/", "http://127.0.0.1:32400"},
		{"https://plex.example:32400/", "https://plex.example:32400"},
		{"http://127.0.0.1:32400", "http://127.0.0.1:32400"},
		{"  http://host:32400/  ", "http://host:32400"},
	}
	for _, c := range good {
		view, err := svc.SavePlexSettings(c.in, "tok", nil)
		if err != nil {
			t.Errorf("save %q: unexpected error %v", c.in, err)
			continue
		}
		if view.URL != c.want || svc.plex.URL != c.want {
			t.Errorf("save %q: URL = %q (stored %q), want %q", c.in, view.URL, svc.plex.URL, c.want)
		}
	}
	for _, bad := range []string{"ht!tp://%%", "ftp://example.com", "http://", "://"} {
		if _, err := svc.SavePlexSettings(bad, "tok", nil); err == nil {
			t.Errorf("save %q: expected an error, got none", bad)
		}
	}
}

// newTokenGatePlex is a mock Plex that answers identity ONLY for
// the one token it accepts — it proves which token a test sent.
func newTokenGatePlex(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "sekrit" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer friendlyName="Gate Plex" machineIdentifier="gate123" version="4.5.6"/>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTestPlexWithFormValues: typed values beat saved ones field by
// field — a typed URL is used with the STORED token, a typed token
// is used against the saved URL, and blanks fall back to saved.
func TestTestPlexWithFormValues(t *testing.T) {
	gate := newTokenGatePlex(t)
	other := newMockPlex(t, false) // ignores tokens; identity "Unit Test Plex"
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Nothing saved, nothing typed: not configured.
	if res := svc.TestPlexWith("", ""); res.OK || !strings.Contains(res.Error, "not configured") {
		t.Errorf("empty everything: %+v", res)
	}
	// Save the gate server with its token.
	if _, err := svc.SavePlexSettings(gate.URL, "sekrit", nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Blanks -> saved values.
	if res := svc.TestPlexWith("", ""); !res.OK || res.ServerName != "Gate Plex" {
		t.Errorf("saved fallback: %+v", res)
	}
	// A typed WRONG token wins over the stored right one.
	if res := svc.TestPlexWith("", "wrong"); res.OK || !strings.Contains(res.Error, "rejected the token") {
		t.Errorf("typed token must win: %+v", res)
	}
	// A typed URL wins; the stored token fills the blank.
	if res := svc.TestPlexWith(other.URL, ""); !res.OK || res.ServerName != "Unit Test Plex" {
		t.Errorf("typed URL + stored token: %+v", res)
	}
	// A scheme-less typed URL is normalized like a save.
	if res := svc.TestPlexWith(strings.TrimPrefix(other.URL, "http://"), ""); !res.OK {
		t.Errorf("scheme-less typed URL: %+v", res)
	}
	// A junk typed URL fails in-band with the shape named.
	if res := svc.TestPlexWith("ftp://example.com", ""); res.OK || !strings.Contains(res.Error, "Plex URL must look like") {
		t.Errorf("junk URL: %+v", res)
	}
}

// TestTestPlexPersistsServerName: a successful test of the saved
// settings teaches the settings the server's name, and both the
// settings view and State expose it (plex_server_name) so the UI
// can show "Connected ✓ — <name>" without a live call.
func TestTestPlexPersistsServerName(t *testing.T) {
	gate := newTokenGatePlex(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.SavePlexSettings(gate.URL, "sekrit", nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	if svc.GetPlexSettings().ServerName != "" {
		t.Fatal("server name should be unknown before any test")
	}
	res := svc.TestPlex()
	if !res.OK {
		t.Fatalf("TestPlex: %+v", res)
	}
	if got := svc.GetPlexSettings().ServerName; got != "Gate Plex" {
		t.Errorf("settings server_name = %q, want Gate Plex", got)
	}
	if got := svc.State().PlexServerName; got != "Gate Plex" {
		t.Errorf("state plex_server_name = %q, want Gate Plex", got)
	}
	// Changing the saved URL forgets the old server's name.
	other := newMockPlex(t, false)
	if _, err := svc.SavePlexSettings(other.URL, "sekrit", nil); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if got := svc.GetPlexSettings().ServerName; got != "" {
		t.Errorf("server_name after URL change = %q, want forgotten", got)
	}
	// And the name never drags a token along: marshal both views.
	for name, v := range map[string]any{"settings": svc.GetPlexSettings(), "state": svc.State()} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "sekrit") {
			t.Errorf("token leaked into %s: %s", name, b)
		}
	}
}
