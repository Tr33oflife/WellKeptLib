package core

// plex_test.go — tests for the v0.2.0 Plex library import.
// Everything runs against httptest mock servers serving recorded
// Plex XML — no real Plex server is touched, here or anywhere in the
// verification (a real server is only ever exercised by the user's
// own live test, per the project's standing rule).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mockSectionsXML is the recorded-style sections reply: two media
// libraries (one with two Locations) and one artist library that the
// import must list but never treat as a media library.
const mockSectionsXML = `<?xml version="1.0" encoding="UTF-8"?>
<MediaContainer size="3">
  <Directory key="1" title="Movies" type="movie" agent="tv.plex.agents.movie">
    <Location id="11" path="/media/Movies"/>
    <Location id="12" path="/media/MoreMovies"/>
  </Directory>
  <Directory key="2" title="TV Shows" type="show" agent="tv.plex.agents.series">
    <Location id="21" path="/media/TV"/>
  </Directory>
  <Directory key="3" title="Music" type="artist" agent="tv.plex.agents.music">
    <Location id="31" path="/media/Music"/>
  </Directory>
</MediaContainer>`

// mockMovieTitles are the three movie items the mock serves, paged
// two at a time against a totalSize of 3 to prove pagination works.
var mockMovieTitles = []string{"Alpha (2020)", "Beta (2021)", "Gamma (2022)"}

// newMockPlex starts a mock Plex server. When failEpisodes is set,
// the shows episodes listing (type=4) answers HTTP 500 so tests can
// prove that lands in warnings rather than failing the whole fetch.
func newMockPlex(t *testing.T, failEpisodes bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/identity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer friendlyName="Unit Test Plex" machineIdentifier="deadbeef1234" version="1.2.3"/>`)
	})
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, mockSectionsXML)
	})
	mux.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		w.Header().Set("Content-Type", "application/xml")
		var b strings.Builder
		fmt.Fprintf(&b, `<?xml version="1.0"?><MediaContainer size="2" totalSize="3">`)
		// Serve at most 2 per page no matter what size was requested:
		// the client must advance by what it actually received.
		for i := start; i < len(mockMovieTitles) && i < start+2; i++ {
			fmt.Fprintf(&b, `<Video title=%q type="movie"><Media><Part file="/media/Movies/%s.mkv"/></Media></Video>`, mockMovieTitles[i], mockMovieTitles[i])
		}
		b.WriteString(`</MediaContainer>`)
		fmt.Fprint(w, b.String())
	})
	mux.HandleFunc("/library/sections/2/all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "4" {
			// A bare show listing returns seasons, not files; the
			// client must never rely on it.
			fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer totalSize="1"><Directory title="Season 01"/></MediaContainer>`)
			return
		}
		if failEpisodes {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer totalSize="1"><Video title="Pilot Episode" grandparentTitle="Test Show" type="episode"><Media><Part file="/media/TV/Test Show/Season 01/Test Show S01E01.mkv"/></Media></Video></MediaContainer>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestPlexSectionsParsing: keys/titles/types and every Location of
// the fixture sections XML come through exactly, artist included.
func TestPlexSectionsParsing(t *testing.T) {
	srv := newMockPlex(t, false)
	c := newPlexClient(srv.URL, "tok", "cid")
	secs, err := c.sections()
	if err != nil {
		t.Fatalf("sections: %v", err)
	}
	if len(secs) != 3 {
		t.Fatalf("got %d sections, want 3", len(secs))
	}
	want := []PlexSection{
		{Key: "1", Title: "Movies", Type: "movie", Locations: []string{"/media/Movies", "/media/MoreMovies"}},
		{Key: "2", Title: "TV Shows", Type: "show", Locations: []string{"/media/TV"}},
		{Key: "3", Title: "Music", Type: "artist", Locations: []string{"/media/Music"}},
	}
	for i, w := range want {
		got := secs[i]
		if got.Key != w.Key || got.Title != w.Title || got.Type != w.Type {
			t.Errorf("section %d = %+v, want key/title/type of %+v", i, got, w)
		}
		if strings.Join(got.Locations, "|") != strings.Join(w.Locations, "|") {
			t.Errorf("section %d locations = %v, want %v", i, got.Locations, w.Locations)
		}
	}
}

// TestPlexItemPathsPaginationAndEpisodes: the paged movie listing is
// fully collected (3 items across 2 pages) and episode paths carry
// the episode title + show (grandparent) title.
func TestPlexItemPathsPaginationAndEpisodes(t *testing.T) {
	srv := newMockPlex(t, false)
	c := newPlexClient(srv.URL, "tok", "cid")
	secs, err := c.sections()
	if err != nil {
		t.Fatalf("sections: %v", err)
	}
	paths, warnings := c.itemPaths(secs)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(paths) != 4 {
		t.Fatalf("got %d paths, want 4 (3 movies + 1 episode): %+v", len(paths), paths)
	}
	for i, title := range mockMovieTitles {
		p := paths[i]
		if p.Title != title || p.Type != "movie" || p.SectionKey != "1" || p.File != "/media/Movies/"+title+".mkv" {
			t.Errorf("movie path %d = %+v", i, p)
		}
	}
	ep := paths[3]
	if ep.Title != "Pilot Episode" || ep.GrandparentTitle != "Test Show" || ep.Type != "episode" || ep.SectionKey != "2" ||
		ep.File != "/media/TV/Test Show/Season 01/Test Show S01E01.mkv" {
		t.Errorf("episode path = %+v", ep)
	}
}

// TestPlexItemPathsEpisodeFailureWarns: a broken episodes endpoint
// degrades to a warning naming the section; the movies still arrive.
func TestPlexItemPathsEpisodeFailureWarns(t *testing.T) {
	srv := newMockPlex(t, true)
	c := newPlexClient(srv.URL, "tok", "cid")
	secs, _ := c.sections()
	paths, warnings := c.itemPaths(secs)
	if len(paths) != 3 {
		t.Errorf("got %d paths, want the 3 movies", len(paths))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "TV Shows") {
		t.Errorf("warnings = %v, want one naming TV Shows", warnings)
	}
}

// TestApplyMapping: longest prefix wins, matches only on a path
// boundary, Windows-style targets join with backslashes, and
// unmapped paths pass through untouched.
func TestApplyMapping(t *testing.T) {
	cases := []struct {
		name string
		path string
		maps []PathMapping
		want string
	}{
		{"longest prefix wins", "/media/movies/a.mkv",
			[]PathMapping{{From: "/media", To: "/mnt/all"}, {From: "/media/movies", To: "/mnt/films"}},
			"/mnt/films/a.mkv"},
		{"boundary: /media must not rewrite /mediabox", "/mediabox/a.mkv",
			[]PathMapping{{From: "/media", To: "/mnt"}},
			"/mediabox/a.mkv"},
		{"exact match", "/media",
			[]PathMapping{{From: "/media", To: "/mnt"}},
			"/mnt"},
		{"windows-style to joins with backslashes", "/media/Movies/A B.mkv",
			[]PathMapping{{From: "/media", To: `D:\Media`}},
			`D:\Media\Movies\A B.mkv`},
		{"windows-style from", `D:\Media\Movies\a.mkv`,
			[]PathMapping{{From: `D:\Media`, To: "/mnt/media"}},
			"/mnt/media/Movies/a.mkv"},
		{"no mappings passthrough", "/media/Movies/a.mkv", nil, "/media/Movies/a.mkv"},
		{"trailing slash on from still matches", "/media/TV/x.mkv",
			[]PathMapping{{From: "/media/", To: "/mnt"}},
			"/mnt/TV/x.mkv"},
	}
	for _, tc := range cases {
		if got := applyMapping(tc.path, tc.maps); got != tc.want {
			t.Errorf("%s: applyMapping(%q) = %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}
}

// TestPlexTestConnectionErrorShapes: a 401 must blame the token by
// name, an unreachable server must name the URL, and a non-Plex HTTP
// server must be called out as not answering like Plex.
func TestPlexTestConnectionErrorShapes(t *testing.T) {
	// 401 everywhere.
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv401.Close()
	_, err := newPlexClient(srv401.URL, "bad", "cid").testConnection()
	if err == nil || !strings.Contains(err.Error(), "rejected the token") || !strings.Contains(err.Error(), "401") {
		t.Errorf("401 error = %v, want a token rejection naming 401", err)
	}
	// Unreachable: start a server only to learn a free URL, then close it.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	_, err = newPlexClient(deadURL, "tok", "cid").testConnection()
	if err == nil || !strings.Contains(err.Error(), deadURL) {
		t.Errorf("unreachable error = %v, want it to name %s", err, deadURL)
	}
	// Wrong server entirely: plain HTML 200.
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body>just a web site</body></html>")
	}))
	defer web.Close()
	_, err = newPlexClient(web.URL, "tok", "cid").testConnection()
	if err == nil || !strings.Contains(err.Error(), "did not answer like a Plex server") {
		t.Errorf("wrong-server error = %v", err)
	}
}

// TestPlexTokenSecrecy: the stored token must appear in NO API-shaped
// output (settings view, state), while it IS what's on disk in
// plex.json (0600). Empty-token saves keep it; "__clear__" wipes it.
func TestPlexTokenSecrecy(t *testing.T) {
	srv := newMockPlex(t, false)
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	const secret = "SECRET-TOKEN-12345"
	view, err := svc.SavePlexSettings(srv.URL, secret, []PathMapping{{From: "/media", To: "/mnt"}})
	if err != nil {
		t.Fatalf("SavePlexSettings: %v", err)
	}
	if !view.Configured || view.URL != srv.URL {
		t.Fatalf("view = %+v", view)
	}
	// On disk: token present, file 0600.
	raw, err := os.ReadFile(filepath.Join(dir, "plex.json"))
	if err != nil {
		t.Fatalf("read plex.json: %v", err)
	}
	if !strings.Contains(string(raw), secret) {
		t.Error("plex.json should contain the token (it is the credential store)")
	}
	if info, _ := os.Stat(filepath.Join(dir, "plex.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("plex.json perms = %o, want 600", info.Mode().Perm())
	}
	// In API shapes: token absent, everywhere.
	settingsJSON, _ := json.Marshal(svc.GetPlexSettings())
	stateJSON, _ := json.Marshal(svc.State())
	exportJSON, _ := json.Marshal(svc.ExportInventory())
	for name, blob := range map[string][]byte{"settings": settingsJSON, "state": stateJSON, "export": exportJSON} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("token leaked into %s output: %s", name, blob)
		}
	}
	// Live test through the service against the mock identity.
	res := svc.TestPlex()
	if !res.OK || res.ServerName != "Unit Test Plex" || res.Version != "1.2.3" {
		t.Errorf("TestPlex = %+v", res)
	}
	// Empty token on save keeps the stored one.
	if _, err := svc.SavePlexSettings("", "", nil); err != nil {
		t.Fatalf("keep-save: %v", err)
	}
	if raw2, _ := os.ReadFile(filepath.Join(dir, "plex.json")); !strings.Contains(string(raw2), secret) {
		t.Error("empty-token save must keep the stored token")
	}
	// Client ID is stable across service restarts.
	svc2, _ := NewService(dir)
	if svc2.plex.ClientID == "" || svc2.plex.ClientID != svc.plex.ClientID {
		t.Errorf("client ID not stable: %q vs %q", svc.plex.ClientID, svc2.plex.ClientID)
	}
	// "__clear__" wipes URL + token + mappings (client ID survives).
	view, err = svc.SavePlexSettings("__clear__", "", nil)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if view.Configured || svc.plex.Token != "" || svc.plex.URL != "" || len(svc.plex.Mappings) != 0 {
		t.Errorf("after clear: view=%+v stored=%+v", view, svc.plex)
	}
}

// TestPlexSectionsServiceMappingAndValidation: through the service,
// section locations are mapped AND validated — an existing mapped
// folder is valid, a missing one names its problem (v0.1.1 rules).
func TestPlexSectionsServiceMappingAndValidation(t *testing.T) {
	srv := newMockPlex(t, false)
	dir := t.TempDir()
	local := filepath.Join(dir, "localmedia")
	if err := os.MkdirAll(filepath.Join(local, "Movies"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Map the server's /media tree onto the local tree: /media/Movies
	// exists locally, /media/MoreMovies and /media/TV do not.
	if _, err := svc.SavePlexSettings(srv.URL, "tok", []PathMapping{{From: "/media", To: local}}); err != nil {
		t.Fatalf("SavePlexSettings: %v", err)
	}
	views, err := svc.PlexSections()
	if err != nil {
		t.Fatalf("PlexSections: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("got %d section views, want 3", len(views))
	}
	movies := views[0]
	if len(movies.Locations) != 2 {
		t.Fatalf("movies locations = %+v", movies.Locations)
	}
	if !movies.Locations[0].Valid || movies.Locations[0].Path != filepath.Join(local, "Movies") {
		t.Errorf("location 0 = %+v, want valid %s", movies.Locations[0], filepath.Join(local, "Movies"))
	}
	if movies.Locations[1].Valid || !strings.Contains(movies.Locations[1].Problem, "root not found") {
		t.Errorf("location 1 = %+v, want invalid with root-not-found problem", movies.Locations[1])
	}
	if movies.Locations[0].ServerPath != "/media/Movies" {
		t.Errorf("server_path = %q", movies.Locations[0].ServerPath)
	}
}
