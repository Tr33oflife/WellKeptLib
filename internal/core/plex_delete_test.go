package core

// plex_delete_test.go — v1.5.0 Part C (delete via Plex)
// against an httptest mock ONLY. The standing rule binds:
// no test in this project ever touches a real Plex server.
//
// The mock serves the shapes §C.1/§C.4 name: a root document
// with capability attributes, the /:/prefs preference store,
// sections + item pages carrying ratingKey/media IDs (one
// two-version movie), and DELETE endpoints that record every
// call and can be told to refuse (403) or fail (400) per
// target.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// deleteMockPlex is the configurable mock for the delete
// slice tests.
type deleteMockPlex struct {
	srv *httptest.Server

	mu       sync.Mutex
	deletes  []string          // "METHOD path" per DELETE received
	failWith map[string]int    // DELETE path -> status to answer
	failBody map[string]string // DELETE path -> body to answer

	rootAllowDeletion *string // root allowMediaDeletion attr (nil = absent)
	rootOwned         *string // root owned attr (nil = absent)
	prefsDeletion     *string // /:/prefs allowMediaDeletion Setting (nil = absent)
}

func strptr(s string) *string { return &s }

func newDeleteMockPlex(t *testing.T) *deleteMockPlex {
	t.Helper()
	m := &deleteMockPlex{failWith: map[string]int{}, failBody: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		attrs := `friendlyName="Delete Test Plex" machineIdentifier="mock-machine-150" version="9.9.9"`
		if m.rootAllowDeletion != nil {
			attrs += fmt.Sprintf(` allowMediaDeletion=%q`, *m.rootAllowDeletion)
		}
		if m.rootOwned != nil {
			attrs += fmt.Sprintf(` owned=%q`, *m.rootOwned)
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><MediaContainer %s/>`, attrs)
	})
	mux.HandleFunc("/:/prefs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><MediaContainer>`)
		if m.prefsDeletion != nil {
			fmt.Fprintf(&b, `<Setting id="allowMediaDeletion" value=%q/>`, *m.prefsDeletion)
		}
		b.WriteString(`<Setting id="someOtherPref" value="1"/></MediaContainer>`)
		fmt.Fprint(w, b.String())
	})
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer size="1">
  <Directory key="1" title="Movies" type="movie"><Location id="11" path="/media/Movies"/></Directory>
</MediaContainer>`)
	})
	mux.HandleFunc("/library/sections/1/all", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		// Duo (2020): ratingKey 10, TWO media versions
		// (100 = the A file, 101 = the B file). Solo (2021):
		// ratingKey 11, one version (110).
		fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer totalSize="2">
  <Video title="Duo" type="movie" ratingKey="10">
    <Media id="100"><Part file="/media/Movies/Duo A.mkv"/></Media>
    <Media id="101"><Part file="/media/Movies/Duo B.mkv"/></Media>
  </Video>
  <Video title="Solo" type="movie" ratingKey="11">
    <Media id="110"><Part file="/media/Movies/Solo.mkv"/></Media>
  </Video>
</MediaContainer>`)
	})
	mux.HandleFunc("/library/metadata/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		m.mu.Lock()
		m.deletes = append(m.deletes, r.Method+" "+r.URL.Path)
		status, has := m.failWith[r.URL.Path]
		body := m.failBody[r.URL.Path]
		m.mu.Unlock()
		if has {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *deleteMockPlex) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.deletes...)
}

// deleteSvc150 builds a Service pointed at the mock, with
// the three mock files as its scanned library.
func deleteSvc150(t *testing.T, m *deleteMockPlex) *Service {
	t.Helper()
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.plex = &plexSettings{URL: m.srv.URL, Token: "test-token", ClientID: "test-client", Mappings: []PathMapping{}}
	mk := func(path, title string) *Item {
		return &Item{Path: path, Root: "/media", Name: filepath.Base(path), Kind: "movie",
			Title: title, NormTitle: normalizeTitle(title), Year: 2020, Size: 1000, ProbeOK: true}
	}
	svc.results.Items = []*Item{
		mk("/media/Movies/Duo A.mkv", "Duo"),
		mk("/media/Movies/Duo B.mkv", "Duo"),
		mk("/media/Movies/Solo.mkv", "Solo"),
		mk("/media/Movies/Unmatched.mkv", "Unmatched"),
	}
	return svc
}

func readPlexAudit(t *testing.T, svc *Service) []PlexDeleteAction {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(svc.dataDir, "plex-delete-actions.json"))
	if err != nil {
		t.Fatalf("audit log missing: %v", err)
	}
	var list []PlexDeleteAction
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatalf("audit log unparseable: %v", err)
	}
	return list
}

// TestPlexItemIDs150Retained: the extended cross-check fetch
// keeps ratingKey + mediaId per file (§C.3).
func TestPlexItemIDs150Retained(t *testing.T) {
	m := newDeleteMockPlex(t)
	client := newPlexClient(m.srv.URL, "test-token", "test-client")
	secs, err := client.sections()
	if err != nil {
		t.Fatal(err)
	}
	items, _ := client.itemPaths(secs)
	byFile := map[string]PlexItemPath{}
	for _, it := range items {
		byFile[it.File] = it
	}
	if got := byFile["/media/Movies/Duo A.mkv"]; got.RatingKey != "10" || got.MediaID != "100" {
		t.Errorf("Duo A ids = %q/%q, want 10/100", got.RatingKey, got.MediaID)
	}
	if got := byFile["/media/Movies/Duo B.mkv"]; got.RatingKey != "10" || got.MediaID != "101" {
		t.Errorf("Duo B ids = %q/%q, want 10/101", got.RatingKey, got.MediaID)
	}
	if got := byFile["/media/Movies/Solo.mkv"]; got.RatingKey != "11" || got.MediaID != "110" {
		t.Errorf("Solo ids = %q/%q, want 11/110", got.RatingKey, got.MediaID)
	}
}

// TestPlexPreflight150RootAttrs: ownership and deletion
// both stated by the server's root document -> ready, with
// the sources named.
func TestPlexPreflight150RootAttrs(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	pf := svc.PlexPreflight()
	if !pf.Ready || !pf.Owned || pf.OwnedSource != "server" || !pf.DeletionEnabled || pf.DeletionSource != "server" {
		t.Fatalf("preflight = %+v", pf)
	}
	if len(pf.Problems) != 0 {
		t.Errorf("problems = %v", pf.Problems)
	}
}

// TestPlexPreflight150PrefsAndAccount: the root document
// stays silent, so deletion comes from /:/prefs and
// ownership from the plex.tv account resources (matched by
// machine identifier).
func TestPlexPreflight150PrefsAndAccount(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.prefsDeletion = strptr("1")
	// plex.tv mock: one server resource whose
	// clientIdentifier is the mock server's machine ID.
	tv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/resources") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"name":"Delete Test Plex","accessToken":"srv-tok","provides":"server","owned":true,"clientIdentifier":"mock-machine-150","connections":[{"uri":"`+m.srv.URL+`","local":true,"relay":false}]}]`)
	}))
	t.Cleanup(tv.Close)
	old := plexTVBaseURL
	plexTVBaseURL = tv.URL
	t.Cleanup(func() { plexTVBaseURL = old })

	svc := deleteSvc150(t, m)
	svc.plex.AccountToken = "account-tok"
	pf := svc.PlexPreflight()
	if !pf.Ready {
		t.Fatalf("preflight not ready: %+v", pf)
	}
	if pf.OwnedSource != "plex.tv account" || pf.DeletionSource != "server preferences" {
		t.Errorf("sources = %q/%q", pf.OwnedSource, pf.DeletionSource)
	}
}

// TestPlexPreflight150Refusals: not-owner and deletion-off
// each fail the preflight with their own plain problem, and
// an account that cannot be checked fails it too (never
// guessed).
func TestPlexPreflight150Refusals(t *testing.T) {
	// Not the owner.
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("0")
	m.rootAllowDeletion = strptr("1")
	pf := deleteSvc150(t, m).PlexPreflight()
	if pf.Ready || pf.Owned || !pf.OwnedKnown {
		t.Fatalf("not-owner preflight = %+v", pf)
	}
	if !strings.Contains(strings.Join(pf.Problems, " "), "does not own") {
		t.Errorf("problems = %v", pf.Problems)
	}
	// Deletion off.
	m2 := newDeleteMockPlex(t)
	m2.rootOwned = strptr("1")
	m2.rootAllowDeletion = strptr("0")
	pf2 := deleteSvc150(t, m2).PlexPreflight()
	if pf2.Ready || pf2.DeletionEnabled || !pf2.DeletionKnown {
		t.Fatalf("deletion-off preflight = %+v", pf2)
	}
	if !strings.Contains(strings.Join(pf2.Problems, " "), "turned off") {
		t.Errorf("problems = %v", pf2.Problems)
	}
	// Ownership unconfirmable: root silent, no account.
	m3 := newDeleteMockPlex(t)
	m3.prefsDeletion = strptr("1")
	pf3 := deleteSvc150(t, m3).PlexPreflight()
	if pf3.Ready || pf3.OwnedKnown {
		t.Fatalf("unknown-owner preflight = %+v", pf3)
	}
	if !strings.Contains(strings.Join(pf3.Problems, " "), "could not confirm") {
		t.Errorf("problems = %v", pf3.Problems)
	}
}

// TestPlexMatches150: matched files carry their IDs and
// version counts; the unmatched scanned file is counted,
// not matched.
func TestPlexMatches150(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	res, err := svc.PlexMatches()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Preflight.Ready {
		t.Fatalf("preflight in matches = %+v", res.Preflight)
	}
	if res.ScannedFiles != 4 || res.MatchedFiles != 3 || res.UnmatchedFiles != 1 {
		t.Fatalf("counts = %d/%d/%d", res.ScannedFiles, res.MatchedFiles, res.UnmatchedFiles)
	}
	byPath := map[string]PlexFileMatch{}
	for _, row := range res.Matches {
		byPath[row.Path] = row
	}
	if row := byPath["/media/Movies/Duo A.mkv"]; row.RatingKey != "10" || row.MediaID != "100" || row.Versions != 2 {
		t.Errorf("Duo A match = %+v", row)
	}
	if row := byPath["/media/Movies/Solo.mkv"]; row.RatingKey != "11" || row.Versions != 1 {
		t.Errorf("Solo match = %+v", row)
	}
	if _, ok := byPath["/media/Movies/Unmatched.mkv"]; ok {
		t.Error("unmatched file got a match row")
	}
}

// TestPlexDelete150VersionAndItem: the two DELETE shapes —
// per-version by default, whole-item only when named — and
// the deleted-pending-rescan marks that follow.
func TestPlexDelete150VersionAndItem(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	if _, err := svc.PlexMatches(); err != nil {
		t.Fatal(err)
	}
	// Default scope = this file's version only.
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Duo A.mkv"}})
	if len(res.Outcomes) != 1 || !res.Outcomes[0].OK || res.Outcomes[0].Scope != "version" {
		t.Fatalf("version delete outcomes = %+v", res.Outcomes)
	}
	calls := m.calls()
	if len(calls) != 1 || calls[0] != "DELETE /library/metadata/10/media/100" {
		t.Fatalf("server calls = %v", calls)
	}
	// Only Duo A is marked; Duo B (the other version) is not.
	matches, err := svc.PlexMatches()
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]bool{}
	for _, row := range matches.Matches {
		marks[row.Path] = row.DeletedPendingRescan
	}
	if !marks["/media/Movies/Duo A.mkv"] || marks["/media/Movies/Duo B.mkv"] {
		t.Errorf("marks after version delete = %v", marks)
	}
	// Item scope on Duo B deletes the whole item and marks
	// every version's file.
	res = svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Duo B.mkv", Scope: "item"}})
	if len(res.Outcomes) != 1 || !res.Outcomes[0].OK {
		t.Fatalf("item delete outcomes = %+v", res.Outcomes)
	}
	calls = m.calls()
	if len(calls) != 2 || calls[1] != "DELETE /library/metadata/10" {
		t.Fatalf("server calls = %v", calls)
	}
	matches, err = svc.PlexMatches()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range matches.Matches {
		if strings.HasPrefix(row.Path, "/media/Movies/Duo") && !row.DeletedPendingRescan {
			t.Errorf("%s not marked after item delete", row.Path)
		}
	}
	// Audit: both attempts recorded as deleted, with IDs.
	audit := readPlexAudit(t, svc)
	if len(audit) != 2 {
		t.Fatalf("audit entries = %d", len(audit))
	}
	if audit[0].Result != "deleted" || audit[0].Scope != "version" || audit[0].RatingKey != "10" || audit[0].MediaID != "100" {
		t.Errorf("audit[0] = %+v", audit[0])
	}
	if audit[1].Result != "deleted" || audit[1].Scope != "item" || audit[1].RatingKey != "10" {
		t.Errorf("audit[1] = %+v", audit[1])
	}
}

// TestPlexDelete150UnmatchedRefused: a file with no Plex
// match gets NO delete — no server call, a stated refusal,
// an audit entry — and no fallback of any kind happens.
func TestPlexDelete150UnmatchedRefused(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	if _, err := svc.PlexMatches(); err != nil {
		t.Fatal(err)
	}
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Unmatched.mkv"}})
	if len(res.Outcomes) != 1 || res.Outcomes[0].OK {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	if !strings.Contains(res.Outcomes[0].Detail, "no Plex match") {
		t.Errorf("detail = %q", res.Outcomes[0].Detail)
	}
	if calls := m.calls(); len(calls) != 0 {
		t.Errorf("server received DELETEs for an unmatched file: %v", calls)
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 1 || audit[0].Result != "refused" {
		t.Errorf("audit = %+v", audit)
	}
	// The file itself is untouched on disk terms — nothing
	// in this slice ever calls os.Remove (guarded by the
	// design; asserted here via the results still listing it).
	if len(svc.results.Items) != 4 {
		t.Errorf("results items = %d after refused delete", len(svc.results.Items))
	}
}

// TestPlexDelete150ServerRefusalsSurfaced: a 403 and a 400
// come back with status + the server's own words, audited
// as refused/error respectively — never swallowed, never
// retried as a file delete.
func TestPlexDelete150ServerRefusalsSurfaced(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	m.failWith["/library/metadata/10/media/100"] = 403
	m.failBody["/library/metadata/10/media/100"] = "Deletion is not allowed on this server"
	m.failWith["/library/metadata/11/media/110"] = 400
	m.failBody["/library/metadata/11/media/110"] = "Bad request: file is locked"
	svc := deleteSvc150(t, m)
	if _, err := svc.PlexMatches(); err != nil {
		t.Fatal(err)
	}
	res := svc.PlexDelete([]PlexDeleteEntry{
		{Path: "/media/Movies/Duo A.mkv"},
		{Path: "/media/Movies/Solo.mkv"},
	})
	if len(res.Outcomes) != 2 || res.Outcomes[0].OK || res.Outcomes[1].OK {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	if res.Outcomes[0].Status != 403 || !strings.Contains(res.Outcomes[0].Detail, "403") || !strings.Contains(res.Outcomes[0].Detail, "Deletion is not allowed") {
		t.Errorf("403 outcome = %+v", res.Outcomes[0])
	}
	if res.Outcomes[1].Status != 400 || !strings.Contains(res.Outcomes[1].Detail, "file is locked") {
		t.Errorf("400 outcome = %+v", res.Outcomes[1])
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 2 || audit[0].Result != "refused" || audit[0].Status != 403 || audit[1].Result != "error" || audit[1].Status != 400 {
		t.Errorf("audit = %+v", audit)
	}
	// Neither file is marked deleted.
	matches, err := svc.PlexMatches()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range matches.Matches {
		if row.DeletedPendingRescan {
			t.Errorf("%s marked deleted after a refused delete", row.Path)
		}
	}
}

// TestPlexDelete150PreflightGate: when the preflight fails,
// every entry is refused before any DELETE is issued — and
// still audited.
func TestPlexDelete150PreflightGate(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("0") // not the owner
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	if _, err := svc.PlexMatches(); err != nil {
		t.Fatal(err)
	}
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Solo.mkv"}})
	if len(res.Outcomes) != 1 || res.Outcomes[0].OK || !strings.Contains(res.Outcomes[0].Detail, "not ready") {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	if calls := m.calls(); len(calls) != 0 {
		t.Errorf("DELETEs issued behind a failed preflight: %v", calls)
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 1 || audit[0].Result != "refused" {
		t.Errorf("audit = %+v", audit)
	}
}

// TestPlexDelete150BatchMixed: a batch runs every entry in
// order and reports each outcome — one refusal does not
// stop or swallow the rest (§5: batch allowed).
func TestPlexDelete150BatchMixed(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	if _, err := svc.PlexMatches(); err != nil {
		t.Fatal(err)
	}
	res := svc.PlexDelete([]PlexDeleteEntry{
		{Path: "/media/Movies/Solo.mkv"},
		{Path: "/media/Movies/Unmatched.mkv"},
		{Path: "/media/Movies/Duo A.mkv", Scope: "bogus"},
	})
	if len(res.Outcomes) != 3 {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	if !res.Outcomes[0].OK || res.Outcomes[1].OK || res.Outcomes[2].OK {
		t.Errorf("outcome OKs = %v/%v/%v", res.Outcomes[0].OK, res.Outcomes[1].OK, res.Outcomes[2].OK)
	}
	if !strings.Contains(res.Outcomes[2].Detail, "unknown delete scope") {
		t.Errorf("scope outcome = %+v", res.Outcomes[2])
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 3 {
		t.Errorf("audit entries = %d, want 3 (every attempt)", len(audit))
	}
}
