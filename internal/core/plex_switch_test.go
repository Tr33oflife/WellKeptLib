package core

// plex_switch_test.go — tests for server switching (retained /
// remembered sign-in): choosing with the remember opt-in off and
// on, listing + switching servers without a fresh sign-in,
// forgetting the saved sign-in, and Disconnect wiping it.
// Everything runs against a mock plex.tv + token-gated mock
// PMSes — each gate accepts exactly one server token, so a
// successful connect/switch proves WHICH token was sent.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three secrets in play in this file's mocks.
const (
	switchAccountToken = "ACCOUNT-TOKEN-999"
	switchServerAToken = "SERVER-A-TOKEN-111"
	switchServerBToken = "SERVER-B-TOKEN-222"
)

// newGatedPMS is a mock Plex Media Server that answers identity
// ONLY for the one token it accepts — proving which token a
// connect/switch sent. name/version identify it in results.
func newGatedPMS(t *testing.T, token, name, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?><MediaContainer friendlyName=%q machineIdentifier=%q version=%q/>`, name, "mid-"+name, version)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newMockTVTwoServers is a fake plex.tv like newMockTV, but its
// resources list carries TWO servers, each with a single local
// connection to its own token-gated PMS — the switch tests need a
// real second target. PIN behaviour matches newMockTV: PIN 99
// approves on the second poll with the account token, and the
// resources endpoint demands that token.
func newMockTVTwoServers(t *testing.T, pmsA, pmsB string) *httptest.Server {
	t.Helper()
	st := &mockTVState{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/pins", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":99,"code":"ab12"}`)
	})
	mux.HandleFunc("/api/v2/pins/99", func(w http.ResponseWriter, r *http.Request) {
		st.polls++
		w.Header().Set("Content-Type", "application/json")
		if st.polls < 2 {
			fmt.Fprint(w, `{"id":99,"code":"ab12","authToken":null}`)
			return
		}
		fmt.Fprint(w, `{"id":99,"code":"ab12","authToken":"`+switchAccountToken+`"}`)
	})
	mux.HandleFunc("/api/v2/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != switchAccountToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[
			{"name":"Server A","product":"Plex Media Server","provides":"server","accessToken":%q,"connections":[{"uri":%q,"local":true,"relay":false}]},
			{"name":"Server B","product":"Plex Media Server","provides":"server","accessToken":%q,"connections":[{"uri":%q,"local":true,"relay":false}]}
		]`, switchServerAToken, pmsA, switchServerBToken, pmsB)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// scanForTokens asserts a marshaled API-shaped value carries NONE
// of the three secrets in play — the token-secrecy rule, applied
// to every output the switch flow produces.
func scanForTokens(t *testing.T, what string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	for _, tok := range []string{switchAccountToken, switchServerAToken, switchServerBToken} {
		if strings.Contains(string(b), tok) {
			t.Errorf("%s leaked a token: %s", what, b)
		}
	}
}

// signInTwoServers drives the service-level sign-in flow against
// the two-server mock: begin, poll to ready (both servers
// listed), and returns the session ID for choosing.
func signInTwoServers(t *testing.T, svc *Service) string {
	t.Helper()
	begin, err := svc.BeginSignIn()
	if err != nil {
		t.Fatalf("BeginSignIn: %v", err)
	}
	scanForTokens(t, "begin result", begin)
	if res := svc.PollSignInSession(begin.Session); res.Status != "pending" {
		t.Fatalf("first poll = %+v, want pending", res)
	}
	res := svc.PollSignInSession(begin.Session)
	if res.Status != "ready" || len(res.Servers) != 2 || res.Servers[0].Name != "Server A" || res.Servers[1].Name != "Server B" {
		t.Fatalf("second poll = %+v, want ready with Server A + Server B", res)
	}
	scanForTokens(t, "poll result", res)
	return begin.Session
}

// TestSwitchServerWithoutRemembering: sign in (remember OFF),
// choose Server A, list the servers, switch to Server B — all
// without a second sign-in. plex.json never gains the account
// token (retention is memory-only); forgetting then removes
// switching while the connection to B stays live.
func TestSwitchServerWithoutRemembering(t *testing.T) {
	pmsA := newGatedPMS(t, switchServerAToken, "Server A", "1.0.A")
	pmsB := newGatedPMS(t, switchServerBToken, "Server B", "1.0.B")
	tv := newMockTVTwoServers(t, pmsA.URL, pmsB.URL)
	useMockTV(t, tv)

	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	session := signInTwoServers(t, svc)

	// The approval ALONE retained the sign-in: switching is
	// available in memory, nothing is persisted.
	if v := svc.GetPlexSettings(); !v.CanSwitch || v.SignInRemembered {
		t.Errorf("after approval: view = %+v, want can_switch && !signin_remembered", v)
	}

	// Choose A with remember OFF.
	choose := svc.ChooseSignInServerRemember(session, 0, false)
	if !choose.OK || choose.ServerName != "Server A" || choose.Version != "1.0.A" {
		t.Fatalf("choose A = %+v", choose)
	}
	scanForTokens(t, "choose result", choose)
	raw, err := os.ReadFile(filepath.Join(dir, "plex.json"))
	if err != nil {
		t.Fatalf("read plex.json: %v", err)
	}
	if strings.Contains(string(raw), switchAccountToken) || strings.Contains(string(raw), "account_token") {
		t.Errorf("remember=false must persist NO account token: %s", raw)
	}
	if !strings.Contains(string(raw), switchServerAToken) {
		t.Errorf("plex.json should hold server A's token: %s", raw)
	}
	view := svc.GetPlexSettings()
	if !view.Configured || view.ServerName != "Server A" || view.URL != pmsA.URL || !view.CanSwitch || view.SignInRemembered {
		t.Errorf("settings after choose A = %+v", view)
	}
	scanForTokens(t, "settings view", view)

	// List the servers fresh: two, in order, available, not
	// remembered.
	list := svc.PlexServers()
	if !list.Available || list.Remembered || len(list.Servers) != 2 || list.Servers[0].Name != "Server A" || list.Servers[1].Name != "Server B" {
		t.Fatalf("PlexServers = %+v", list)
	}
	scanForTokens(t, "servers list", list)

	// Switch to B (remember omitted -> nil: the persisted sign-in
	// is left untouched).
	sw := svc.SwitchPlexServer(1, nil)
	if !sw.OK || sw.ServerName != "Server B" || sw.Version != "1.0.B" {
		t.Fatalf("switch to B = %+v", sw)
	}
	scanForTokens(t, "switch result", sw)
	view = svc.GetPlexSettings()
	if view.ServerName != "Server B" || view.URL != pmsB.URL {
		t.Errorf("settings after switch = %+v, want connected to Server B", view)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "plex.json"))
	if !strings.Contains(string(raw), switchServerBToken) || strings.Contains(string(raw), switchAccountToken) {
		t.Errorf("plex.json after switch must hold B's token and no account token: %s", raw)
	}
	// The new connection actually works — B's gate proves the
	// token it was handed.
	if res := svc.TestPlex(); !res.OK || res.ServerName != "Server B" {
		t.Errorf("TestPlex after switch = %+v", res)
	}

	// Forget: switching dies, the connection to B does NOT.
	fview, err := svc.ForgetSignIn()
	if err != nil {
		t.Fatalf("ForgetSignIn: %v", err)
	}
	if fview.CanSwitch || fview.SignInRemembered || !fview.Configured || fview.ServerName != "Server B" {
		t.Errorf("settings after forget = %+v", fview)
	}
	scanForTokens(t, "forget view", fview)
	if list := svc.PlexServers(); list.Available || list.Error == "" {
		t.Errorf("PlexServers after forget = %+v, want unavailable with a reason", list)
	}
	if res := svc.SwitchPlexServer(0, nil); res.OK {
		t.Errorf("switch after forget = %+v, want failure", res)
	}
	if res := svc.TestPlex(); !res.OK || res.ServerName != "Server B" {
		t.Errorf("TestPlex after forget = %+v, want still connected to B", res)
	}
}

// TestRememberedSignInSurvivesRestartAndSwitches: choose with
// remember ON persists the account token in plex.json (the one
// deliberate exception to the memory-only rule); a fresh Service
// on the same data dir rehydrates it (can_switch), and switches
// with no new sign-in — including the refetch path (no cached
// list) and the picker's tri-state remember checkbox clearing /
// re-setting the persistence in the switch's own save.
func TestRememberedSignInSurvivesRestartAndSwitches(t *testing.T) {
	pmsA := newGatedPMS(t, switchServerAToken, "Server A", "1.0.A")
	pmsB := newGatedPMS(t, switchServerBToken, "Server B", "1.0.B")
	tv := newMockTVTwoServers(t, pmsA.URL, pmsB.URL)
	useMockTV(t, tv)

	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	session := signInTwoServers(t, svc)
	choose := svc.ChooseSignInServerRemember(session, 0, true)
	if !choose.OK || choose.ServerName != "Server A" {
		t.Fatalf("choose A (remember) = %+v", choose)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plex.json"))
	if err != nil {
		t.Fatalf("read plex.json: %v", err)
	}
	if !strings.Contains(string(raw), switchAccountToken) || !strings.Contains(string(raw), `"account_token"`) {
		t.Errorf("remember=true must persist the account token: %s", raw)
	}
	if v := svc.GetPlexSettings(); !v.CanSwitch || !v.SignInRemembered {
		t.Errorf("view = %+v, want can_switch && signin_remembered", v)
	}

	// Restart: a new Service on the same dir rehydrates the
	// remembered sign-in — connected to A, switching available.
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (restart): %v", err)
	}
	v2 := svc2.GetPlexSettings()
	if !v2.Configured || v2.ServerName != "Server A" || !v2.CanSwitch || !v2.SignInRemembered {
		t.Errorf("view after restart = %+v", v2)
	}
	scanForTokens(t, "settings view after restart", v2)

	// Switch to B with NO list fetched first: SwitchPlexServer
	// must re-run discovery itself. remember=nil leaves the
	// persisted sign-in untouched.
	sw := svc2.SwitchPlexServer(1, nil)
	if !sw.OK || sw.ServerName != "Server B" {
		t.Fatalf("switch to B after restart = %+v", sw)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "plex.json"))
	if !strings.Contains(string(raw), switchAccountToken) || !strings.Contains(string(raw), switchServerBToken) {
		t.Errorf("plex.json after switch must hold the account token AND B's token: %s", raw)
	}
	list := svc2.PlexServers()
	if !list.Available || !list.Remembered || len(list.Servers) != 2 {
		t.Fatalf("PlexServers after restart = %+v", list)
	}
	scanForTokens(t, "servers list after restart", list)

	// Switch back to A with the checkbox UNCHECKED (remember =
	// &false): the connection moves and the persistence is
	// cleared in the same save; in-memory switching still works
	// for the rest of this run.
	no := false
	sw = svc2.SwitchPlexServer(0, &no)
	if !sw.OK || sw.ServerName != "Server A" {
		t.Fatalf("switch back to A = %+v", sw)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "plex.json"))
	if strings.Contains(string(raw), switchAccountToken) || strings.Contains(string(raw), "account_token") {
		t.Errorf("unchecking remember at switch must clear the persisted token: %s", raw)
	}
	if v := svc2.GetPlexSettings(); !v.CanSwitch || v.SignInRemembered || v.ServerName != "Server A" {
		t.Errorf("view after uncheck-switch = %+v, want can_switch (memory) && !remembered, on A", v)
	}

	// Switch to B with the checkbox CHECKED (remember = &true):
	// the retained token is persisted again, in the same save.
	yes := true
	sw = svc2.SwitchPlexServer(1, &yes)
	if !sw.OK || sw.ServerName != "Server B" {
		t.Fatalf("switch to B (re-remember) = %+v", sw)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "plex.json"))
	if !strings.Contains(string(raw), switchAccountToken) {
		t.Errorf("checking remember at switch must persist the account token: %s", raw)
	}
	if v := svc2.GetPlexSettings(); !v.SignInRemembered || v.ServerName != "Server B" {
		t.Errorf("view after recheck-switch = %+v", v)
	}
}

// TestDisconnectWipesRememberedSignIn: the settings save with
// "__clear__" — what both Disconnect buttons send — wipes the
// persisted account token AND the retained in-memory sign-in,
// alongside the URL / server token / mappings it always cleared.
func TestDisconnectWipesRememberedSignIn(t *testing.T) {
	pmsA := newGatedPMS(t, switchServerAToken, "Server A", "1.0.A")
	pmsB := newGatedPMS(t, switchServerBToken, "Server B", "1.0.B")
	tv := newMockTVTwoServers(t, pmsA.URL, pmsB.URL)
	useMockTV(t, tv)

	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	session := signInTwoServers(t, svc)
	if choose := svc.ChooseSignInServerRemember(session, 0, true); !choose.OK {
		t.Fatalf("choose A (remember) = %+v", choose)
	}
	// Precondition via a rehydrated instance (the state a later
	// run would disconnect from).
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (restart): %v", err)
	}
	if v := svc2.GetPlexSettings(); !v.CanSwitch || !v.SignInRemembered || !v.Configured {
		t.Fatalf("precondition: view = %+v, want connected + remembered", v)
	}
	view, err := svc2.SavePlexSettings("__clear__", "", nil)
	if err != nil {
		t.Fatalf("disconnect save: %v", err)
	}
	if view.Configured || view.CanSwitch || view.SignInRemembered {
		t.Errorf("view after disconnect = %+v, want fully cleared", view)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plex.json"))
	if err != nil {
		t.Fatalf("read plex.json: %v", err)
	}
	for _, tok := range []string{switchAccountToken, "account_token", switchServerAToken} {
		if strings.Contains(string(raw), tok) {
			t.Errorf("disconnect must wipe %q from plex.json: %s", tok, raw)
		}
	}
	if list := svc2.PlexServers(); list.Available {
		t.Errorf("PlexServers after disconnect = %+v, want unavailable", list)
	}
	// And a fresh Service on the dir stays signed out.
	svc3, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService (after disconnect): %v", err)
	}
	if v := svc3.GetPlexSettings(); v.CanSwitch || v.SignInRemembered || v.Configured {
		t.Errorf("view after disconnect + restart = %+v, want all false", v)
	}
}

// TestSwitchingUnavailableWithoutSignIn: with no sign-in at all,
// the servers list answers unavailable with the reason, a switch
// fails in-band, both settings booleans are false, and forgetting
// nothing is a harmless no-op. (The remember=false persistence
// contract itself is also covered by the ORIGINAL suite:
// TestSignInFlowPersistsServerTokenNotAccountToken drives the
// original ChooseSignInServer signature, which defaults to
// remember=false.)
func TestSwitchingUnavailableWithoutSignIn(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if v := svc.GetPlexSettings(); v.CanSwitch || v.SignInRemembered {
		t.Errorf("fresh view = %+v, want both booleans false", v)
	}
	list := svc.PlexServers()
	if list.Available || !strings.Contains(list.Error, "sign-in") {
		t.Errorf("PlexServers = %+v, want unavailable with the sign-in reason", list)
	}
	scanForTokens(t, "unavailable list", list)
	if res := svc.SwitchPlexServer(0, nil); res.OK || !strings.Contains(res.Error, "sign-in") {
		t.Errorf("switch = %+v, want an in-band sign-in failure", res)
	}
	if _, err := svc.ForgetSignIn(); err != nil {
		t.Errorf("ForgetSignIn on empty state: %v", err)
	}
	// An out-of-range switch against a live sign-in is an in-band
	// failure too, not a panic or a wrong connection.
	pmsA := newGatedPMS(t, switchServerAToken, "Server A", "1.0.A")
	pmsB := newGatedPMS(t, switchServerBToken, "Server B", "1.0.B")
	tv := newMockTVTwoServers(t, pmsA.URL, pmsB.URL)
	useMockTV(t, tv)
	session := signInTwoServers(t, svc)
	if choose := svc.ChooseSignInServer(session, 0); !choose.OK {
		t.Fatalf("choose via original signature = %+v", choose)
	}
	if res := svc.SwitchPlexServer(7, nil); res.OK || !strings.Contains(res.Error, "isn't in the list") {
		t.Errorf("out-of-range switch = %+v", res)
	}
	if v := svc.GetPlexSettings(); v.ServerName != "Server A" {
		t.Errorf("after failed switch, still on A: view = %+v", v)
	}
}
