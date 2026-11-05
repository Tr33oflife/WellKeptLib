package main

// routes_152_test.go — v1.5.2 route pin: the Plex delete
// audit's read path exists and answers its documented
// shape on a service that has never deleted — an empty
// list, not an error (the History view's empty state
// renders from exactly this).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plexfilemanager/internal/core"
)

func TestRoutes152DeleteActionsEmpty(t *testing.T) {
	svc, err := core.NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := newServer(svc, indexHTML, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plex/delete-actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete-actions status = %d", rec.Code)
	}
	var doc struct {
		Actions []core.PlexDeleteAction `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("delete-actions body unparseable: %v", err)
	}
	if doc.Actions == nil || len(doc.Actions) != 0 {
		t.Errorf("actions = %v, want an empty (non-null) list", doc.Actions)
	}
}
