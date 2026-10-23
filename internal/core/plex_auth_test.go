package core

// plex_auth_test.go — tests for the plex.tv PIN sign-in
// (v0.3.1; approval-link flow v0.3.2). plexTVBaseURL is pointed
// at an httptest mock plex.tv for every unit test (restored
// afterwards); see verification-0.3.2 for the separate live
// smoke against the real plex.tv.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useMockTV points the plex.tv base URL at the given test server
// for the duration of one test.
func useMockTV(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := plexTVBaseURL
	plexTVBaseURL = srv.URL
	t.Cleanup(func() { plexTVBaseURL = old })
}

// mockTVState counts PIN polls so the mock can stay "pending" for
// the first poll and approve from the second on.
type mockTVState struct{ polls int }

// newMockTV builds a fake plex.tv: PIN begin/poll (PIN 99 approves
// on the second poll with the account token; PIN 55 is unknown ->
// 404), and a resources list holding one player (must be filtered
// out) and one server whose connections are deliberately listed
// relay-first so the preference sort has something to fix. The
// resources endpoint demands the account token. provides is
// served in the REAL payload's shape — a comma-separated string
// (v0.3.3; the array form this mock originally served was the
// same wrong assumption the parser had, which is how the bug
// passed verification).
func newMockTV(t *testing.T, pmsURL string) (*httptest.Server, *mockTVState) {
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
		fmt.Fprint(w, `{"id":99,"code":"ab12","authToken":"ACCOUNT-TOKEN-999"}`)
	})
	mux.HandleFunc("/api/v2/pins/55", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/v2/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "ACCOUNT-TOKEN-999" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[
			{"name":"Bedroom Player","product":"Plex for Android","provides":"player,streamer","accessToken":"player-tok","connections":[{"uri":"http://10.0.0.5:32400","local":true,"relay":false}]},
			{"name":"Test PMS","product":"Plex Media Server","provides":"server","accessToken":"SERVER-TOKEN-777","connections":[
				{"uri":"https://relay.example:32400","local":false,"relay":true},
				{"uri":"https://remote.example:32400","local":false,"relay":false},
				{"uri":%q,"local":true,"relay":false}
			]}
		]`, pmsURL)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

// newServerTokenPMS is a mock Plex Media Server that accepts ONLY
// the server access token from the resources payload — proving the
// choose flow sends the server's token, not the account token.
func newServerTokenPMS(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "SERVER-TOKEN-777" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MediaContainer friendlyName="Test PMS" machineIdentifier="pms123" version="1.40.0"/>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestBeginAndPollPIN: PIN creation parses {id, code}; polling is
// "" while pending, the token once approved, and a typed expiry
// error for an unknown PIN.
func TestBeginAndPollPIN(t *testing.T) {
	pms := newServerTokenPMS(t)
	tv, _ := newMockTV(t, pms.URL)
	useMockTV(t, tv)

	id, code, err := beginPIN("cid-test")
	if err != nil || id != 99 || code != "ab12" {
		t.Fatalf("beginPIN = (%d, %q, %v)", id, code, err)
	}
	tok, err := pollPIN("cid-test", 99)
	if err != nil || tok != "" {
		t.Fatalf("first poll = (%q, %v), want pending", tok, err)
	}
	tok, err = pollPIN("cid-test", 99)
	if err != nil || tok != "ACCOUNT-TOKEN-999" {
		t.Fatalf("second poll = (%q, %v), want the account token", tok, err)
	}
	if _, err := pollPIN("cid-test", 55); !errors.Is(err, errSignInExpired) {
		t.Fatalf("unknown PIN error = %v, want errSignInExpired", err)
	}
}

// parseAuthURL splits an approval URL into its parts and parses
// the fragment (a query string after "#?") for assertions.
func parseAuthURL(t *testing.T, raw string) (*url.URL, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("approval URL does not parse: %v (%q)", err, raw)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(u.Fragment, "?"))
	if err != nil {
		t.Fatalf("approval URL fragment is not a query: %v (%q)", err, u.Fragment)
	}
	return u, q
}

// TestSignInApprovalURL (v0.3.2): the begin result carries an
// approval URL built in the standard plex.tv shape —
// https://app.plex.tv/auth#?clientID=<id>&code=<pin code>&
// context[device][product]=WellKeptLib — and NO code
// field of its own: the code exists only inside the URL, never
// as a value the UI could display for typing.
func TestSignInApprovalURL(t *testing.T) {
	// The escaper: everything outside [A-Za-z0-9-_.~] is %XX, and
	// a space is %20 — never '+' (url.QueryEscape would emit '+',
	// which a fragment does not decode as a space).
	for in, want := range map[string]string{
		"plain-ABC_123.~": "plain-ABC_123.~",
		"a b":             "a%20b",
		"a+b":             "a%2Bb",
		"a/b?c=d&e":       "a%2Fb%3Fc%3Dd%26e",
	} {
		if got := plexAuthEscape(in); got != want {
			t.Errorf("plexAuthEscape(%q) = %q, want %q", in, got, want)
		}
	}

	// Direct construction with values that force encoding (the
	// client id carries spaces): the product segment must appear
	// literally, and no '+' may appear anywhere in the URL.
	raw := plexApprovalURL("client id 42", "PINCODE123")
	u, q := parseAuthURL(t, raw)
	if u.Scheme != "https" || u.Host != "app.plex.tv" || u.Path != "/auth" {
		t.Errorf("approval URL head = %s://%s%s, want https://app.plex.tv/auth", u.Scheme, u.Host, u.Path)
	}
	if got := q.Get("clientID"); got != "client id 42" {
		t.Errorf("clientID param = %q, want the client id", got)
	}
	if got := q.Get("code"); got != "PINCODE123" {
		t.Errorf("code param = %q, want the PIN code", got)
	}
	if got := q.Get("context[device][product]"); got != "WellKeptLib" {
		t.Errorf("context[device][product] param = %q, want the product name", got)
	}
	if !strings.Contains(raw, "context%5Bdevice%5D%5Bproduct%5D=WellKeptLib") {
		t.Errorf("approval URL lacks the expected product segment: %q", raw)
	}
	if strings.Contains(raw, "+") {
		t.Errorf("approval URL must not contain '+' (space is %%20): %q", raw)
	}

	// Through BeginSignIn against the mock plex.tv: the code
	// parameter is the mock-issued code, clientID is this
	// install's persisted id, and the marshaled result has NO
	// "code" key — only "session" and "auth_url".
	pms := newServerTokenPMS(t)
	tv, _ := newMockTV(t, pms.URL)
	useMockTV(t, tv)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	begin, err := svc.BeginSignIn()
	if err != nil {
		t.Fatalf("BeginSignIn: %v", err)
	}
	_, q = parseAuthURL(t, begin.AuthURL)
	if got := q.Get("code"); got != "ab12" {
		t.Errorf("begin auth_url code param = %q, want the mock-issued code", got)
	}
	if got, want := q.Get("clientID"), svc.plexSnapshot().ClientID; got != want {
		t.Errorf("begin auth_url clientID = %q, want %q", got, want)
	}
	var keys map[string]any
	b, _ := json.Marshal(begin)
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatalf("marshal begin result: %v", err)
	}
	if _, ok := keys["code"]; ok {
		t.Errorf("begin result must have NO \"code\" key: %s", b)
	}
	if _, ok := keys["auth_url"]; !ok {
		t.Errorf("begin result lacks \"auth_url\": %s", b)
	}
}

// TestFetchServersFilterAndOrder: only "server" resources come
// back, and their connections are reordered local-direct first,
// remote-direct next, relay last.
func TestFetchServersFilterAndOrder(t *testing.T) {
	pms := newServerTokenPMS(t)
	tv, _ := newMockTV(t, pms.URL)
	useMockTV(t, tv)

	servers, err := fetchServers("ACCOUNT-TOKEN-999", "cid-test")
	if err != nil {
		t.Fatalf("fetchServers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1 (the player must be filtered out)", len(servers))
	}
	s := servers[0]
	if s.Name != "Test PMS" || s.AccessToken != "SERVER-TOKEN-777" {
		t.Errorf("server = %+v", s)
	}
	wantOrder := []string{pms.URL, "https://remote.example:32400", "https://relay.example:32400"}
	if len(s.Connections) != len(wantOrder) {
		t.Fatalf("connections = %+v", s.Connections)
	}
	for i, want := range wantOrder {
		if s.Connections[i].URI != want {
			t.Errorf("connection %d = %q, want %q (preference order)", i, s.Connections[i].URI, want)
		}
	}
	// A bogus account token is called out as a rejection.
	if _, err := fetchServers("bogus", "cid-test"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("bogus token error = %v", err)
	}
}

// TestSignInFlowPersistsServerTokenNotAccountToken: the whole
// service flow — begin, poll (pending then ready), choose — lands
// the SERVER's accessToken in plex.json; the ACCOUNT token appears
// in no file and no API-shaped output, and the session is gone.
func TestSignInFlowPersistsServerTokenNotAccountToken(t *testing.T) {
	pms := newServerTokenPMS(t)
	tv, _ := newMockTV(t, pms.URL)
	useMockTV(t, tv)

	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	begin, err := svc.BeginSignIn()
	if err != nil {
		t.Fatalf("BeginSignIn: %v", err)
	}
	if begin.Session == "" || begin.AuthURL == "" {
		t.Fatalf("begin = %+v", begin)
	}
	// First poll: still waiting for the user.
	if res := svc.PollSignInSession(begin.Session); res.Status != "pending" {
		t.Fatalf("first poll = %+v, want pending", res)
	}
	// Second poll: approved, discovery ran, one server listed.
	res := svc.PollSignInSession(begin.Session)
	if res.Status != "ready" || len(res.Servers) != 1 || res.Servers[0].Name != "Test PMS" || res.Servers[0].Connections != 3 {
		t.Fatalf("second poll = %+v", res)
	}
	// The account token must not ride along in ANY response shape.
	for name, v := range map[string]any{"begin": begin, "poll": res} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "ACCOUNT-TOKEN-999") {
			t.Errorf("account token leaked into %s response: %s", name, b)
		}
	}
	// Choose it: the PMS only accepts the server token, so success
	// here already proves which token was sent.
	choose := svc.ChooseSignInServer(begin.Session, 0)
	if !choose.OK || choose.ServerName != "Test PMS" || choose.Version != "1.40.0" {
		t.Fatalf("choose = %+v", choose)
	}
	// On disk: server token present, account token absent.
	raw, err := os.ReadFile(filepath.Join(dir, "plex.json"))
	if err != nil {
		t.Fatalf("read plex.json: %v", err)
	}
	if !strings.Contains(string(raw), "SERVER-TOKEN-777") {
		t.Errorf("plex.json should hold the server's accessToken: %s", raw)
	}
	if strings.Contains(string(raw), "ACCOUNT-TOKEN-999") {
		t.Errorf("plex.json must NEVER hold the account token: %s", raw)
	}
	// Settings + state: configured, server named, no tokens at all.
	view := svc.GetPlexSettings()
	if !view.Configured || view.URL != pms.URL || view.ServerName != "Test PMS" {
		t.Errorf("settings view = %+v", view)
	}
	if got := svc.State().PlexServerName; got != "Test PMS" {
		t.Errorf("state plex_server_name = %q", got)
	}
	for name, v := range map[string]any{"settings": view, "state": svc.State(), "choose": choose} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), "ACCOUNT-TOKEN-999") || strings.Contains(string(b), "SERVER-TOKEN-777") {
			t.Errorf("a token leaked into %s output: %s", name, b)
		}
	}
	// The session is gone: polling reports expired, choosing fails.
	if res := svc.PollSignInSession(begin.Session); res.Status != "expired" {
		t.Errorf("poll after choose = %+v, want expired", res)
	}
	if res := svc.ChooseSignInServer(begin.Session, 0); res.OK {
		t.Errorf("choose after choose = %+v, want failure", res)
	}
}

// ---------------------------------------------------------------------------
// v0.3.3 — the provides fix. Background: the REAL plex.tv resources
// payload carries provides as a comma-separated STRING; the parser
// declared []string, so every real payload failed to unmarshal and
// discovery died after an otherwise successful approval. The unit
// mock served the array form too (same wrong assumption), so the
// suite passed a broken parser. The fixtures below mirror the
// real payload field-for-field.
// ---------------------------------------------------------------------------

// realShapeResources is a resources payload in the shape plex.tv
// actually serves (field set cross-checked against production
// payloads and the Plex API documentation): provides is
// a STRING; connections carry the full address parts. Entry 1 is
// a player (must be dropped), entry 2 a server with connections
// listed relay-first, entry 3 a "server,player" combination
// (must count as a server).
const realShapeResources = `[
	{
		"name": "Living Room TV",
		"product": "Plex for Android",
		"productVersion": "10.30.0",
		"platform": "Android",
		"platformVersion": "14",
		"device": "TV",
		"model": null,
		"vendor": null,
		"protocol": null,
		"clientIdentifier": "player-machine-id",
		"createdAt": "2024-03-01T10:00:00Z",
		"lastSeenAt": "2026-10-22T18:00:00Z",
		"provides": "player,streamer",
		"ownerId": null,
		"publicAddress": "203.0.113.7",
		"accessToken": "player-token-not-a-server",
		"owned": true,
		"home": true,
		"synced": false,
		"relay": false,
		"presence": true,
		"httpsRequired": false,
		"connections": [
			{"protocol": "https", "address": "203.0.113.7", "port": 32400, "uri": "https://203-0-113-7.example.plex.direct:32400", "local": true, "relay": false, "IPv6": false}
		]
	},
	{
		"name": "Real Shape PMS",
		"product": "Plex Media Server",
		"productVersion": "1.43.5.11029",
		"platform": "Linux",
		"platformVersion": "6.8",
		"device": "PC",
		"model": null,
		"vendor": null,
		"protocol": null,
		"clientIdentifier": "server-machine-id",
		"createdAt": "2023-06-15T08:30:00Z",
		"lastSeenAt": "2026-10-22T19:41:00Z",
		"provides": "server",
		"ownerId": null,
		"publicAddress": "198.51.100.23",
		"accessToken": "REALSHAPE-SERVER-TOKEN",
		"owned": true,
		"home": true,
		"synced": false,
		"relay": true,
		"presence": true,
		"httpsRequired": true,
		"connections": [
			{"protocol": "https", "address": "relay.example", "port": 443, "uri": "https://relay.example:443", "local": false, "relay": true, "IPv6": false},
			{"protocol": "https", "address": "198.51.100.23", "port": 32400, "uri": "https://198-51-100-23.example.plex.direct:32400", "local": false, "relay": false, "IPv6": false},
			{"protocol": "http", "address": "192.168.1.50", "port": 32400, "uri": "http://192.168.1.50:32400", "local": true, "relay": false, "IPv6": false}
		]
	},
	{
		"name": "Combo Box",
		"product": "Plex Media Server",
		"productVersion": "1.40.0",
		"platform": "Windows",
		"platformVersion": "10",
		"device": "PC",
		"model": null,
		"vendor": null,
		"protocol": null,
		"clientIdentifier": "combo-machine-id",
		"createdAt": "2024-01-20T12:00:00Z",
		"lastSeenAt": "2026-10-22T19:00:00Z",
		"provides": "server,player",
		"ownerId": 12345678,
		"publicAddress": "192.0.2.9",
		"accessToken": "COMBO-SERVER-TOKEN",
		"owned": false,
		"home": false,
		"synced": false,
		"relay": false,
		"presence": true,
		"httpsRequired": true,
		"connections": [
			{"protocol": "https", "address": "192.0.2.9", "port": 32400, "uri": "https://192-0-2-9.example.plex.direct:32400", "local": true, "relay": false, "IPv6": false}
		]
	}
]`

// resourcesFixtureServer serves one fixed resources payload (no
// token gate — these tests are about the payload SHAPE).
func resourcesFixtureServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestStringListShapes: the tolerant decoder — string form (the
// real one), comma-joined multi-value string, array form (must
// keep working), null/empty, and a number (must error, naming the
// type problem).
func TestStringListShapes(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{`"server"`, []string{"server"}, false},
		{`"player,streamer"`, []string{"player", "streamer"}, false},
		{`"server, player"`, []string{"server", "player"}, false},
		{`""`, []string{}, false},
		{`["server"]`, []string{"server"}, false},
		{`["client","player"]`, []string{"client", "player"}, false},
		{`null`, nil, false},
		{`42`, nil, true},
		{`[1,2]`, nil, true},
	}
	for _, c := range cases {
		var s stringList
		err := json.Unmarshal([]byte(c.in), &s)
		if c.wantErr {
			if err == nil {
				t.Errorf("stringList(%s): want error, got %v", c.in, []string(s))
			}
			continue
		}
		if err != nil {
			t.Errorf("stringList(%s): %v", c.in, err)
			continue
		}
		if len(s) != len(c.want) {
			t.Errorf("stringList(%s) = %v, want %v", c.in, []string(s), c.want)
			continue
		}
		for i := range c.want {
			if s[i] != c.want[i] {
				t.Errorf("stringList(%s) = %v, want %v", c.in, []string(s), c.want)
				break
			}
		}
	}
}

// TestFetchServersRealShapePayload: discovery against the REAL
// payload shape — servers kept (including the combined
// "server,player" entry), the player dropped, the server's own
// accessToken mapped, and its connections reordered into
// preference order (local-direct, remote-direct, relay).
func TestFetchServersRealShapePayload(t *testing.T) {
	useMockTV(t, resourcesFixtureServer(t, realShapeResources))

	servers, err := fetchServers("any-account-token", "cid-test")
	if err != nil {
		t.Fatalf("fetchServers on the real payload shape: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d servers, want 2 (player dropped, combo kept): %+v", len(servers), servers)
	}
	s := servers[0]
	if s.Name != "Real Shape PMS" || s.AccessToken != "REALSHAPE-SERVER-TOKEN" {
		t.Errorf("server[0] = %+v", s)
	}
	wantOrder := []string{
		"http://192.168.1.50:32400",
		"https://198-51-100-23.example.plex.direct:32400",
		"https://relay.example:443",
	}
	if len(s.Connections) != len(wantOrder) {
		t.Fatalf("server[0] connections = %+v", s.Connections)
	}
	for i, want := range wantOrder {
		if s.Connections[i].URI != want {
			t.Errorf("connection %d = %q, want %q (preference order)", i, s.Connections[i].URI, want)
		}
	}
	if servers[1].Name != "Combo Box" || servers[1].AccessToken != "COMBO-SERVER-TOKEN" {
		t.Errorf("server[1] = %+v (a \"server,player\" string must count as a server)", servers[1])
	}
}

// TestFetchServersMalformedError: a payload that still doesn't
// parse produces the diagnostic error — it names what could not
// be read and carries the stdlib detail (field + types), and it
// must NOT contain any of the payload's bytes (the payload holds
// access tokens).
func TestFetchServersMalformedError(t *testing.T) {
	const secret = "MUST-NOT-LEAK-TOKEN"
	payload := `[{"name":"Broken","provides":42,"accessToken":"` + secret + `"}]`
	useMockTV(t, resourcesFixtureServer(t, payload))

	_, err := fetchServers("any-account-token", "cid-test")
	if err == nil {
		t.Fatal("fetchServers on a malformed payload: want an error")
	}
	if !strings.Contains(err.Error(), "plex.tv's server list could not be read") {
		t.Errorf("error = %q, want the could-not-be-read wording", err)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("error = %q, want the stdlib unmarshal detail", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked payload bytes (an access token): %q", err)
	}
}

// TestSignInSessionExpiry: sessions older than the TTL are purged
// lazily — polling one reports "expired", and beginning a new
// sign-in sweeps old ones out of the map.
func TestSignInSessionExpiry(t *testing.T) {
	pms := newServerTokenPMS(t)
	tv, _ := newMockTV(t, pms.URL)
	useMockTV(t, tv)

	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.signins["stale"] = &signinSession{pinID: 99, createdAt: time.Now().Add(-signInSessionTTL - time.Minute)}
	if res := svc.PollSignInSession("stale"); res.Status != "expired" {
		t.Errorf("stale poll = %+v, want expired", res)
	}
	if _, ok := svc.signins["stale"]; ok {
		t.Error("stale session should have been purged by the poll")
	}
	// Unknown sessions report expired too (no oracle about which).
	if res := svc.PollSignInSession("never-existed"); res.Status != "expired" {
		t.Errorf("unknown poll = %+v, want expired", res)
	}
	// Begin sweeps expired sessions as well.
	svc.signins["stale-2"] = &signinSession{pinID: 99, createdAt: time.Now().Add(-signInSessionTTL - time.Minute)}
	if _, err := svc.BeginSignIn(); err != nil {
		t.Fatalf("BeginSignIn: %v", err)
	}
	if _, ok := svc.signins["stale-2"]; ok {
		t.Error("BeginSignIn should purge expired sessions")
	}
}
