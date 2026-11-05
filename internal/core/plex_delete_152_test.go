package core

// plex_delete_152_test.go — v1.5.2: the four remaining
// §C.4 completions from RESULTS-1.5.1's open items,
// against the same httptest mock as plex_delete_test.go.
// No real Plex server is ever contacted (standing rule).
//
//   1. The confirm payload (the matches rows) carries the
//      server and the size(s): each row names its server,
//      its own file's size, and every version's file of
//      its item with sizes.
//   2. Every audit record names its server — on all four
//      outcomes, client-side refusals included.
//   3. The audit reads back (PlexDeleteActions): newest
//      first, a "resolved" entry included, a missing log
//      an honest empty list, and a v1.5.0/v1.5.1-shaped
//      file (no server field) loading as written.
//   4. A 403 re-reads the preflight facts and names the
//      one that changed — staged by flipping the mock's
//      allowMediaDeletion between the batch preflight and
//      the DELETE's own answer — and says the checks still
//      pass when none changed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlexMatches152ServerAndSizes pins fix 1's payload:
// server + sizes ride the match rows the confirm renders
// from. Sizes are the scan items' own (1000 bytes each in
// this fixture).
func TestPlexMatches152ServerAndSizes(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	res, err := svc.PlexMatches()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]PlexFileMatch{}
	for _, row := range res.Matches {
		byPath[row.Path] = row
	}
	duo := byPath["/media/Movies/Duo A.mkv"]
	if duo.Server != "Delete Test Plex" {
		t.Errorf("Duo A server = %q, want the connected server's name", duo.Server)
	}
	if duo.Size != 1000 {
		t.Errorf("Duo A size = %d, want the scan item's 1000", duo.Size)
	}
	if len(duo.ItemFiles) != 2 ||
		duo.ItemFiles[0].Path != "/media/Movies/Duo A.mkv" || duo.ItemFiles[0].Size != 1000 ||
		duo.ItemFiles[1].Path != "/media/Movies/Duo B.mkv" || duo.ItemFiles[1].Size != 1000 {
		t.Errorf("Duo A item_files = %+v, want both Duo files at 1000 each", duo.ItemFiles)
	}
	duoB := byPath["/media/Movies/Duo B.mkv"]
	if len(duoB.ItemFiles) != 2 || duoB.Server != "Delete Test Plex" || duoB.Size != 1000 {
		t.Errorf("Duo B row = %+v, want the same item files, server and own size", duoB)
	}
	solo := byPath["/media/Movies/Solo.mkv"]
	if len(solo.ItemFiles) != 1 || solo.ItemFiles[0].Path != "/media/Movies/Solo.mkv" || solo.ItemFiles[0].Size != 1000 {
		t.Errorf("Solo item_files = %+v, want just its own file", solo.ItemFiles)
	}
}

// TestPlexDelete152AuditNamesServerAllOutcomes pins fix
// 2: deleted, resolved, refused, error — and a
// client-side refusal that never reaches the server —
// every audit record names the server the batch was
// issued against.
func TestPlexDelete152AuditNamesServerAllOutcomes(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/10/media/100"] = 400 // Duo A -> error
	m.failWith["/library/metadata/10/media/101"] = 403 // Duo B -> refused
	m.failWith["/library/metadata/11"] = 404           // Solo (item shape) -> resolved
	svc := readyDeleteSvc151(t, m)
	svc.PlexDelete([]PlexDeleteEntry{
		{Path: "/media/Movies/Duo A.mkv"},
		{Path: "/media/Movies/Duo B.mkv"},
		{Path: "/media/Movies/Solo.mkv"},
	})
	delete(m.failWith, "/library/metadata/10/media/100")
	svc.PlexDelete([]PlexDeleteEntry{
		{Path: "/media/Movies/Duo A.mkv"},     // now a plain 200 -> deleted
		{Path: "/media/Movies/Unmatched.mkv"}, // no match -> refused, no request
	})
	audit := readPlexAudit(t, svc)
	if len(audit) != 5 {
		t.Fatalf("audit = %d records, want 5: %+v", len(audit), audit)
	}
	results := map[string]bool{}
	for _, rec := range audit {
		if rec.Server != "Delete Test Plex" {
			t.Errorf("audit record for %s (%s) has server %q, want \"Delete Test Plex\"", rec.Path, rec.Result, rec.Server)
		}
		results[rec.Result] = true
	}
	for _, want := range []string{"deleted", "resolved", "refused", "error"} {
		if !results[want] {
			t.Errorf("audit results = %v, want %q among them", results, want)
		}
	}
}

// TestPlexDelete152HistoryReadback pins fix 3's read
// path: newest first with a resolved entry in the
// vocabulary; a missing log reads as an empty list.
func TestPlexDelete152HistoryReadback(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/11"] = 404 // Solo -> resolved
	svc := readyDeleteSvc151(t, m)
	if got := svc.PlexDeleteActions(); len(got) != 0 {
		t.Fatalf("PlexDeleteActions before any delete = %+v, want empty", got)
	}
	svc.PlexDelete([]PlexDeleteEntry{
		{Path: "/media/Movies/Duo A.mkv"}, // deleted
		{Path: "/media/Movies/Solo.mkv"},  // resolved (404)
	})
	got := svc.PlexDeleteActions()
	if len(got) != 2 {
		t.Fatalf("PlexDeleteActions = %d entries, want 2", len(got))
	}
	if got[0].Result != "resolved" || got[0].Path != "/media/Movies/Solo.mkv" {
		t.Errorf("newest entry = %+v, want the resolved Solo delete first", got[0])
	}
	if got[1].Result != "deleted" || got[1].Server != "Delete Test Plex" {
		t.Errorf("oldest entry = %+v, want the deleted Duo A with its server", got[1])
	}
}

// TestPlexDelete152HistoryToleratesLegacyAudit: an audit
// file written before v1.5.2 (no server field anywhere)
// loads exactly as written, server empty.
func TestPlexDelete152HistoryToleratesLegacyAudit(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy := `[{"time":"2026-11-05T10:00:00Z","path":"/media/Movies/Old.mkv","title":"Old","scope":"item","rating_key":"7","result":"deleted","status":200}]`
	if err := os.WriteFile(filepath.Join(svc.dataDir, "plex-delete-actions.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got := svc.PlexDeleteActions()
	if len(got) != 1 || got[0].Result != "deleted" || got[0].Server != "" || got[0].RatingKey != "7" {
		t.Errorf("legacy readback = %+v, want the one record, server empty", got)
	}
}

// TestPlexDelete152ForbiddenNamesDeletionTurnedOff pins
// fix 4's main case: the batch preflight passes, the
// DELETE is refused 403, and the server fact flipped in
// between (the mock turns allowMediaDeletion off as the
// DELETE lands) — the outcome and the audit name the
// deletion fact, not both candidate causes.
func TestPlexDelete152ForbiddenNamesDeletionTurnedOff(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/10/media/100"] = 403
	m.onDelete = func(path string) {
		if path != "/library/metadata/10/media/100" {
			return
		}
		m.mu.Lock()
		m.rootAllowDeletion = strptr("0")
		m.mu.Unlock()
	}
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Duo A.mkv"}})
	if len(res.Outcomes) != 1 || res.Outcomes[0].OK || res.Outcomes[0].Status != 403 {
		t.Fatalf("outcomes = %+v, want one refused 403", res.Outcomes)
	}
	detail := res.Outcomes[0].Detail
	if !strings.Contains(detail, "Allow media deletion") || !strings.Contains(detail, "now turned off") {
		t.Errorf("403 detail = %q, want it to name the deletion setting as now turned off", detail)
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 1 || audit[0].Result != "refused" || audit[0].Detail != detail {
		t.Errorf("audit = %+v, want the refused record carrying the same named fact", audit)
	}
}

// TestPlexDelete152ForbiddenFactsUnchanged pins the
// other half of fix 4: the DELETE is refused and every
// readable preflight fact still holds — the detail says
// the checks still pass instead of naming a cause.
func TestPlexDelete152ForbiddenFactsUnchanged(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/10/media/100"] = 403
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Duo A.mkv"}})
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != 403 {
		t.Fatalf("outcomes = %+v, want one 403", res.Outcomes)
	}
	detail := res.Outcomes[0].Detail
	if !strings.Contains(detail, "Every check still passes") {
		t.Errorf("403 detail = %q, want the still-passes wording", detail)
	}
	if strings.Contains(detail, "now turned off") || strings.Contains(detail, "no longer owns") {
		t.Errorf("403 detail = %q, must not name a fact that did not change", detail)
	}
}

// TestChangedPreflightFact152 pins the phrasing helper's
// truth table directly: gate order, one fact named, and
// the all-pass sentence when nothing readable changed.
func TestChangedPreflightFact152(t *testing.T) {
	ready := PlexPreflight{Configured: true, Reachable: true, OwnedKnown: true, Owned: true, DeletionKnown: true, DeletionEnabled: true, Ready: true}
	cases := []struct {
		name string
		pf   PlexPreflight
		want string
	}{
		{"not configured", PlexPreflight{}, "no longer configured"},
		{"unreachable", PlexPreflight{Configured: true}, "no longer answers"},
		{"not owned", PlexPreflight{Configured: true, Reachable: true, OwnedKnown: true}, "no longer owns"},
		{"owned unknown", PlexPreflight{Configured: true, Reachable: true}, "can no longer confirm the signed-in account owns"},
		{"deletion off", PlexPreflight{Configured: true, Reachable: true, OwnedKnown: true, Owned: true, DeletionKnown: true}, "now turned off"},
		{"deletion unknown", PlexPreflight{Configured: true, Reachable: true, OwnedKnown: true, Owned: true}, "can no longer confirm"},
		{"all pass", ready, "Every check still passes"},
	}
	for _, c := range cases {
		if got := changedPreflightFact(c.pf); !strings.Contains(got, c.want) {
			t.Errorf("%s: changedPreflightFact = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}
