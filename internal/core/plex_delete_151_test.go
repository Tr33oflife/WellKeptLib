package core

// plex_delete_151_test.go — v1.5.1: the two §C.4
// reconciliations against the RESTORED spec text, against
// the same httptest mock as plex_delete_test.go. No real
// Plex server is ever contacted (standing rule).
//
//   1. 404 = already gone, treat as resolved: the attempt
//      records "resolved" (distinct from "deleted"), the
//      row is marked exactly as a success marks it, and
//      the outcome renders resolved — on BOTH DELETE
//      shapes (item and media).
//   2. A single-version item's delete IS the item delete:
//      DELETE /library/metadata/{ratingKey}, including
//      under the default scope and under an explicitly
//      named "version" scope. Multi-version items keep
//      the per-version default.

import (
	"strings"
	"testing"
)

// deleteMarks151 re-runs the match fetch and returns the
// deleted-pending-rescan marks by path.
func deleteMarks151(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	res, err := svc.PlexMatches()
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]bool{}
	for _, row := range res.Matches {
		marks[row.Path] = row.DeletedPendingRescan
	}
	return marks
}

func readyDeleteSvc151(t *testing.T, m *deleteMockPlex) *Service {
	t.Helper()
	m.rootOwned = strptr("1")
	m.rootAllowDeletion = strptr("1")
	svc := deleteSvc150(t, m)
	if _, err := svc.PlexMatches(); err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestPlexDelete151SingleVersionDefaultIsItemDelete: the
// default delete of a one-version item sends the ITEM
// delete verbatim — never the media path — and the
// outcome/audit record the scope the action became.
func TestPlexDelete151SingleVersionDefaultIsItemDelete(t *testing.T) {
	m := newDeleteMockPlex(t)
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Solo.mkv"}})
	if len(res.Outcomes) != 1 || !res.Outcomes[0].OK {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	if res.Outcomes[0].Scope != "item" {
		t.Errorf("outcome scope = %q, want \"item\" (the scope the action became)", res.Outcomes[0].Scope)
	}
	calls := m.calls()
	if len(calls) != 1 || calls[0] != "DELETE /library/metadata/11" {
		t.Fatalf("server calls = %v, want exactly the item DELETE", calls)
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 1 || audit[0].Result != "deleted" || audit[0].Scope != "item" || audit[0].RatingKey != "11" {
		t.Errorf("audit = %+v", audit)
	}
	if marks := deleteMarks151(t, svc); !marks["/media/Movies/Solo.mkv"] {
		t.Errorf("Solo not marked deleted-pending-rescan: %v", marks)
	}
}

// TestPlexDelete151SingleVersionExplicitVersionScope: an
// explicitly named "version" scope on a single-version
// item still resolves to the item delete — the distinction
// does not exist for one version (§C.4).
func TestPlexDelete151SingleVersionExplicitVersionScope(t *testing.T) {
	m := newDeleteMockPlex(t)
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Solo.mkv", Scope: "version"}})
	if len(res.Outcomes) != 1 || !res.Outcomes[0].OK {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	calls := m.calls()
	if len(calls) != 1 || calls[0] != "DELETE /library/metadata/11" {
		t.Fatalf("server calls = %v, want exactly the item DELETE", calls)
	}
}

// TestPlexDelete151NotFoundMediaShapeResolves: a 404 on
// the per-version DELETE resolves the attempt — OK, the
// "already gone" wording, a "resolved" audit record, and
// the row marked exactly as a 200 marks it. The other
// version of the same item is NOT marked.
func TestPlexDelete151NotFoundMediaShapeResolves(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/10/media/100"] = 404
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Duo A.mkv"}})
	if len(res.Outcomes) != 1 || !res.Outcomes[0].OK {
		t.Fatalf("outcomes = %+v (a 404 must resolve, not fail)", res.Outcomes)
	}
	out := res.Outcomes[0]
	if out.Status != 404 || !strings.Contains(out.Detail, "already gone") {
		t.Errorf("outcome = %+v, want status 404 + already-gone wording", out)
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 1 || audit[0].Result != "resolved" || audit[0].Status != 404 {
		t.Errorf("audit = %+v, want one \"resolved\" record at 404", audit)
	}
	marks := deleteMarks151(t, svc)
	if !marks["/media/Movies/Duo A.mkv"] || marks["/media/Movies/Duo B.mkv"] {
		t.Errorf("marks = %v, want Duo A marked only", marks)
	}
}

// TestPlexDelete151NotFoundItemShapeResolves: a 404 on
// the whole-item DELETE resolves the same way, and the
// item marking covers every version's file.
func TestPlexDelete151NotFoundItemShapeResolves(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/10"] = 404
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{{Path: "/media/Movies/Duo B.mkv", Scope: "item"}})
	if len(res.Outcomes) != 1 || !res.Outcomes[0].OK || res.Outcomes[0].Status != 404 {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 1 || audit[0].Result != "resolved" || audit[0].Scope != "item" {
		t.Errorf("audit = %+v", audit)
	}
	marks := deleteMarks151(t, svc)
	if !marks["/media/Movies/Duo A.mkv"] || !marks["/media/Movies/Duo B.mkv"] {
		t.Errorf("marks = %v, want both Duo files marked after a resolved item delete", marks)
	}
}

// TestPlexDelete151MixedBatchThreeOutcomes: one batch, a
// 200, a 404 and a 403 — three distinct outcomes in order
// (deleted / resolved / refused), the multi-version
// default still sending the per-version DELETE, and the
// marks following each outcome.
func TestPlexDelete151MixedBatchThreeOutcomes(t *testing.T) {
	m := newDeleteMockPlex(t)
	m.failWith["/library/metadata/10/media/100"] = 404
	m.failWith["/library/metadata/10/media/101"] = 403
	m.failBody["/library/metadata/10/media/101"] = "Deletion is not allowed on this server"
	svc := readyDeleteSvc151(t, m)
	res := svc.PlexDelete([]PlexDeleteEntry{
		{Path: "/media/Movies/Solo.mkv"},  // 200, item shape (single version)
		{Path: "/media/Movies/Duo A.mkv"}, // 404, media shape -> resolved
		{Path: "/media/Movies/Duo B.mkv"}, // 403, media shape -> refused
	})
	if len(res.Outcomes) != 3 {
		t.Fatalf("outcomes = %+v", res.Outcomes)
	}
	if !res.Outcomes[0].OK || res.Outcomes[0].Status != 200 {
		t.Errorf("outcome[0] = %+v, want OK at 200", res.Outcomes[0])
	}
	if !res.Outcomes[1].OK || res.Outcomes[1].Status != 404 {
		t.Errorf("outcome[1] = %+v, want OK (resolved) at 404", res.Outcomes[1])
	}
	if res.Outcomes[2].OK || res.Outcomes[2].Status != 403 {
		t.Errorf("outcome[2] = %+v, want refused at 403", res.Outcomes[2])
	}
	wantCalls := []string{
		"DELETE /library/metadata/11",
		"DELETE /library/metadata/10/media/100",
		"DELETE /library/metadata/10/media/101",
	}
	calls := m.calls()
	if len(calls) != len(wantCalls) {
		t.Fatalf("server calls = %v", calls)
	}
	for i, want := range wantCalls {
		if calls[i] != want {
			t.Errorf("call[%d] = %q, want %q", i, calls[i], want)
		}
	}
	audit := readPlexAudit(t, svc)
	if len(audit) != 3 || audit[0].Result != "deleted" || audit[1].Result != "resolved" || audit[2].Result != "refused" {
		t.Errorf("audit results = %+v, want deleted/resolved/refused", audit)
	}
	marks := deleteMarks151(t, svc)
	if !marks["/media/Movies/Solo.mkv"] || !marks["/media/Movies/Duo A.mkv"] || marks["/media/Movies/Duo B.mkv"] {
		t.Errorf("marks = %v, want Solo + Duo A marked, Duo B not", marks)
	}
}
