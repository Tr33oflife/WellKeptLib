package core

// plex_auth.go — the optional Plex module, slice 2 (v0.3.1;
// approval-link flow corrected in v0.3.2; resources parsing
// corrected in v0.3.3): plex.tv PIN sign-in + server discovery —
// the plex.tv approval-link pattern, and now the PRIMARY way this app
// connects to Plex.
//
// WHY: the v0.2.0 flow asked the user to hunt down their
// X-Plex-Token and paste a server URL. In the user's hands that
// failed silently several ways at once (a scheme-less address was
// rejected at save; Test connection tested the saved config, not
// what was typed; the Import button stayed disabled with the
// reason only in a tooltip). Sign-in removes the whole class: the
// user clicks an approval link and approves on Plex's own hosted
// auth page, the app discovers the account's servers, and stores
// the chosen server's own accessToken — the manual URL+token form
// survives only as an "Advanced" fallback for unclaimed /
// local-only servers.
//
// FLOW: BeginSignIn (POST /api/v2/pins) -> the UI shows an
// approval link built in the standard plex.tv shape
// (https://app.plex.tv/auth#?clientID=…&code=…&context[device]
// [product]=…) and polls PollSignInSession -> once plex.tv
// returns an account authToken, FetchServers runs once and the
// session holds the server list -> ChooseSignInServer tries the
// chosen server's connections in preference order and persists
// the winner.
//
// THE CODE IS NEVER DISPLAYED (v0.3.2 correction): v0.3.1 showed
// the v2 PIN code (~25 chars) and told the user to type it into
// Plex's legacy "link" page. That page belongs to the separate
// LEGACY PIN flow and accepts only its 4-character codes — the
// v2 code cannot be approved there, so the shipped sign-in was
// unapprovable. The v2 code is never typed anywhere: it rides
// inside the approval URL, which is the only place it leaves
// this server, and it never reaches the UI as a value to copy.
//
// SECURITY (unit-tested, same discipline as plex.go's token rule):
// the ACCOUNT token is NEVER returned by any API response. By
// default it exists only in memory: inside a sign-in session
// while one runs, and — once an approval succeeds — in the
// Service's RETAINED sign-in (server switching), which is what
// lets the user change the connected server later without
// signing in again. It is written to plex.json ONLY when the
// user explicitly opts in with "remember my sign-in" at
// server-pick time (the picker's checkbox says exactly what it
// stores and where); that opt-in is the single deliberate
// exception to the memory-only rule, and both Disconnect and
// "Forget saved sign-in" wipe it. Choosing a server persists
// that SERVER's accessToken (each resource carries its own) plus
// the server's name; the session is then deleted. Sessions also
// expire after 10 minutes.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// plexTVBaseURL is the plex.tv API root. A package-level variable
// (not a constant) so tests can point it at an httptest server.
var plexTVBaseURL = "https://plex.tv"

// init lets the end-to-end verification point the plex.tv API at a
// local mock via PFM_PLEX_TV_URL. Normal runs never set it — the
// real plex.tv is the default, and the user's own live sign-in is
// the real-plex.tv test.
func init() {
	if v := strings.TrimSpace(os.Getenv("PFM_PLEX_TV_URL")); v != "" {
		plexTVBaseURL = strings.TrimRight(v, "/")
	}
}

// plexProductName is the app's product identity: it rides on
// every plex.tv / Plex-server call (X-Plex-Product) and is the
// device product name Plex's approval page shows the user.
const plexProductName = "WellKeptLib"

// plexTVHTTP is the shared client for plex.tv calls (15s timeout,
// like the Plex server client).
var plexTVHTTP = &http.Client{Timeout: 15 * time.Second}

// tvDo performs one plex.tv API call with the app's identity
// headers and returns the body plus the HTTP status. The status is
// returned separately (not folded into the error) because callers
// interpret specific codes — a 404 while polling a PIN means the
// code expired. Errors are written for the user; the token is a
// header only and never appears in them.
func tvDo(clientID, token, method, path string) ([]byte, int, error) {
	req, err := http.NewRequest(method, plexTVBaseURL+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("bad plex.tv request: %v", err)
	}
	req.Header.Set("X-Plex-Product", plexProductName)
	req.Header.Set("X-Plex-Version", Version)
	req.Header.Set("X-Plex-Client-Identifier", clientID)
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
	resp, err := plexTVHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading plex.tv's answer failed: %v", err)
	}
	return body, resp.StatusCode, nil
}

// errSignInExpired is the typed error for a PIN plex.tv no longer
// knows (expired or already consumed): the UI restarts the flow.
var errSignInExpired = errors.New("that sign-in expired — start again")

// pinJSON mirrors the plex.tv PIN payloads: POST /api/v2/pins
// answers {id, code}; GET /api/v2/pins/{id} adds authToken once
// the user has approved on Plex's page (null until then). Field
// types audited in v0.3.3 against the real payload (captured in
// verification-0.3.2): id is a JSON number, code a string,
// authToken null or a string, expiresIn a number of seconds.
type pinJSON struct {
	ID        int     `json:"id"`
	Code      string  `json:"code"`
	AuthToken *string `json:"authToken"`
}

// stringList is a list of short strings that plex.tv encodes in
// EITHER of two shapes depending on the field: one comma-
// separated JSON string ("server", "player,streamer") or a JSON
// array of strings (["server"]). The resources payload's
// provides field is the STRING form in production — production
// clients split it on commas — while the array form is what this app
// originally (wrongly) assumed, and what some tooling emits.
// v0.3.3: UnmarshalJSON accepts both, so the payload parses no
// matter which shape plex.tv serves, and a future shape flip
// cannot make discovery fail wholesale again.
type stringList []string

// UnmarshalJSON implements json.Unmarshaler for stringList: a
// JSON string is split on commas (parts trimmed, empties
// dropped); a JSON array is taken as-is; null means "no values".
func (s *stringList) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*s = nil
		return nil
	}
	if trimmed[0] == '"' {
		var str string
		if err := json.Unmarshal(trimmed, &str); err != nil {
			return err
		}
		out := stringList{}
		for _, part := range strings.Split(str, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		*s = out
		return nil
	}
	var arr []string
	if err := json.Unmarshal(trimmed, &arr); err != nil {
		return err
	}
	*s = stringList(arr)
	return nil
}

// beginPIN starts a PIN sign-in on plex.tv and returns the PIN id
// (for polling) and the code plex.tv issued. The code is a
// server-side secret: it is embedded in the approval URL and is
// never displayed or typed anywhere (see the file header).
func beginPIN(clientID string) (int, string, error) {
	body, status, err := tvDo(clientID, "", http.MethodPost, "/api/v2/pins?strong=true")
	if err != nil {
		return 0, "", fmt.Errorf("could not reach plex.tv to start sign-in — %v", err)
	}
	if status < 200 || status >= 300 {
		return 0, "", fmt.Errorf("plex.tv answered HTTP %d when starting sign-in", status)
	}
	var pin pinJSON
	if err := json.Unmarshal(body, &pin); err != nil || pin.ID == 0 || pin.Code == "" {
		return 0, "", fmt.Errorf("plex.tv gave an unreadable sign-in reply")
	}
	return pin.ID, pin.Code, nil
}

// plexAuthEscape percent-encodes s for the approval URL's
// fragment, matching the encodeURIComponent behaviour the
// approval link requires: every byte outside [A-Za-z0-9-_.~]
// becomes %XX, so a space is %20 — never '+' (url.QueryEscape
// would emit '+', which a URL fragment does not decode as a
// space, and the product name is full of spaces).
func plexAuthEscape(s string) string {
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0x0f])
		}
	}
	return b.String()
}

// plexApprovalURL builds the link the user clicks to approve the
// sign-in on Plex's hosted auth page: the fragment
// is a query string carrying clientID, the PIN code, and the
// device product, with the context key pre-encoded. The code
// exists only inside this URL; the UI renders the URL as a
// button and never shows the code itself.
func plexApprovalURL(clientID, pinCode string) string {
	return "https://app.plex.tv/auth#?clientID=" + plexAuthEscape(clientID) +
		"&code=" + plexAuthEscape(pinCode) +
		"&context%5Bdevice%5D%5Bproduct%5D=" + plexAuthEscape(plexProductName)
}

// pollPIN checks one PIN: it returns the account authToken once
// the user has approved, "" while still pending, and
// errSignInExpired when plex.tv has forgotten the PIN.
func pollPIN(clientID string, pinID int) (string, error) {
	body, status, err := tvDo(clientID, "", http.MethodGet, fmt.Sprintf("/api/v2/pins/%d", pinID))
	if err != nil {
		return "", fmt.Errorf("could not reach plex.tv to check the sign-in — %v", err)
	}
	if status == http.StatusNotFound {
		return "", errSignInExpired
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("plex.tv answered HTTP %d when checking the sign-in", status)
	}
	var pin pinJSON
	if err := json.Unmarshal(body, &pin); err != nil {
		return "", fmt.Errorf("plex.tv gave an unreadable sign-in reply")
	}
	if pin.AuthToken != nil && *pin.AuthToken != "" {
		return *pin.AuthToken, nil
	}
	return "", nil
}

// plexConnection is one way to reach a server, from the plex.tv
// resources payload: a ready-to-use URI plus the flags the
// preference order sorts by.
type plexConnection struct {
	URI   string `json:"uri"`
	Local bool   `json:"local"`
	Relay bool   `json:"relay"`
}

// plexServer is one server resource: display name, ITS OWN access
// token (distinct from the account token — this is the one that
// gets persisted), and its connections in preference order.
type plexServer struct {
	Name        string
	AccessToken string
	Connections []plexConnection
}

// resourceJSON mirrors one entry of GET /api/v2/resources.
// Provides is a stringList (v0.3.3): the real payload carries it
// as a comma-separated STRING ("server"), not the array this
// struct originally declared — with []string the whole payload
// failed to parse and discovery died with "not readable JSON"
// on the user's live test. Only the fields the flow consumes
// are declared; the rest of the real entry (product, platform,
// clientIdentifier, timestamps, flags, connection address
// parts) is ignored by the decoder.
type resourceJSON struct {
	Name        string           `json:"name"`
	AccessToken string           `json:"accessToken"`
	Provides    stringList       `json:"provides"`
	Connections []plexConnection `json:"connections"`
}

// connectionRank orders connection attempts (v0.3.1 brief): a
// direct local connection first (same LAN — fastest, no plex.tv
// relay in the path), then a direct remote one, then the relay.
func connectionRank(c plexConnection) int {
	switch {
	case c.Relay:
		return 2
	case c.Local:
		return 0
	default:
		return 1
	}
}

// fetchServers lists the account's SERVERS from plex.tv (resources
// whose provides list contains "server" — players/clients are
// dropped), each with its connections sorted into attempt order.
func fetchServers(accountToken, clientID string) ([]plexServer, error) {
	body, status, err := tvDo(clientID, accountToken, http.MethodGet, "/api/v2/resources?includeHttps=1&includeRelay=1")
	if err != nil {
		return nil, fmt.Errorf("could not reach plex.tv to list your servers — %v", err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("plex.tv rejected the account sign-in (%d) — start the sign-in again", status)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("plex.tv answered HTTP %d when listing your servers", status)
	}
	var raw []resourceJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		// The stdlib error names the field and the types involved
		// (safe to show); the BODY is never included — it carries
		// per-server access tokens.
		return nil, fmt.Errorf("plex.tv's server list could not be read (%v)", err)
	}
	out := []plexServer{}
	for _, r := range raw {
		provides := false
		for _, p := range r.Provides {
			if p == "server" {
				provides = true
			}
		}
		if !provides {
			continue
		}
		conns := append([]plexConnection{}, r.Connections...)
		sort.SliceStable(conns, func(i, j int) bool { return connectionRank(conns[i]) < connectionRank(conns[j]) })
		out = append(out, plexServer{Name: r.Name, AccessToken: r.AccessToken, Connections: conns})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Sign-in sessions + Service methods (mirrored 1:1 by /api/plex/signin/*)
// ---------------------------------------------------------------------------

// signInSessionTTL bounds how long a session (and the account
// token inside it) may live, matching how long plex.tv keeps a PIN
// usable. Expired sessions are purged lazily on any sign-in call.
const signInSessionTTL = 10 * time.Minute

// signinSession is one in-flight sign-in. accountToken is set once
// the user approves on Plex's page; servers/fetched once
// discovery ran. MEMORY ONLY — see the file header's security
// contract.
type signinSession struct {
	pinID        int
	createdAt    time.Time
	accountToken string
	servers      []plexServer
	fetched      bool
}

// retainedSignIn is the COMPLETED plex.tv sign-in kept at Service
// level (server switching) — see Service.retained. Lifecycle: it
// is created/refreshed by PollSignInSession the moment an
// approval's discovery succeeds (or rehydrated at startup from a
// remembered token); unlike sessions it has no TTL and survives
// server choice, so switching works any time the app is running;
// ForgetSignIn and Disconnect drop it. servers/fetched cache the
// last list fetched FOR SWITCHING, so a switch connects from the
// list the user was just shown instead of re-running discovery.
// MEMORY ONLY unless the user opted in to remembering — then
// accountToken is also persisted in plex.json (0600).
type retainedSignIn struct {
	accountToken string
	servers      []plexServer
	fetched      bool
}

// SignInBeginResult hands the UI an opaque session key plus the
// approval URL to open on Plex's hosted auth page. The PIN code
// is deliberately NOT a field: it exists only inside the
// session's server-side state and inside the approval URL —
// never as a value the UI could display or the user could be
// asked to type (see the file header).
type SignInBeginResult struct {
	Session string `json:"session"`
	AuthURL string `json:"auth_url"`
}

// SignInServerView is one discovered server as the UI lists it:
// the session-local index to choose by, its name, and how many
// connections it offers. No URIs and no tokens — choosing is by
// index, and the connection details stay server-side.
type SignInServerView struct {
	Index       int    `json:"index"`
	Name        string `json:"name"`
	Connections int    `json:"connections"`
}

// SignInPollResult is the poll answer. Status is "pending" (keep
// waiting), "ready" (servers listed), "expired" (session or code
// gone — restart), or "error" (something else failed — Error says
// what). The account token is never part of it.
type SignInPollResult struct {
	Status  string             `json:"status"`
	Servers []SignInServerView `json:"servers,omitempty"`
	Error   string             `json:"error,omitempty"`
}

// purgeSignInsLocked drops expired sessions. Caller holds s.mu.
func (s *Service) purgeSignInsLocked(now time.Time) {
	for id, sess := range s.signins {
		if now.Sub(sess.createdAt) > signInSessionTTL {
			delete(s.signins, id)
		}
	}
}

// serverViews renders a session's servers for the poll response.
func serverViews(servers []plexServer) []SignInServerView {
	out := make([]SignInServerView, 0, len(servers))
	for i, srv := range servers {
		out = append(out, SignInServerView{Index: i, Name: srv.Name, Connections: len(srv.Connections)})
	}
	return out
}

// BeginSignIn starts a plex.tv PIN sign-in: it mints the PIN,
// stores a fresh session (purging expired ones), and returns the
// session key + the approval URL for Plex's hosted auth page.
// The PIN code stays server-side — it is embedded in the URL
// and nowhere else.
func (s *Service) BeginSignIn() (SignInBeginResult, error) {
	p := s.plexSnapshot()
	pinID, code, err := beginPIN(p.ClientID)
	if err != nil {
		return SignInBeginResult{}, err
	}
	session := newClientID() // an opaque random key, same helper as the client ID
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signins == nil {
		s.signins = map[string]*signinSession{}
	}
	s.purgeSignInsLocked(time.Now())
	s.signins[session] = &signinSession{pinID: pinID, createdAt: time.Now()}
	return SignInBeginResult{Session: session, AuthURL: plexApprovalURL(p.ClientID, code)}, nil
}

// PollSignInSession advances one sign-in session: while approval
// is outstanding it returns "pending"; on approval it runs server
// discovery ONCE, keeps the result (and the account token) in the
// session, and returns "ready" with the server list. An expired
// PIN or session reports "expired" and the session is dropped.
func (s *Service) PollSignInSession(sessionID string) SignInPollResult {
	p := s.plexSnapshot()
	s.mu.Lock()
	s.purgeSignInsLocked(time.Now())
	sess, ok := s.signins[sessionID]
	if ok && sess.fetched {
		views := serverViews(sess.servers)
		s.mu.Unlock()
		return SignInPollResult{Status: "ready", Servers: views}
	}
	s.mu.Unlock()
	if !ok {
		return SignInPollResult{Status: "expired", Error: "that sign-in session expired — start again"}
	}
	token, err := pollPIN(p.ClientID, sess.pinID)
	if errors.Is(err, errSignInExpired) {
		s.mu.Lock()
		delete(s.signins, sessionID)
		s.mu.Unlock()
		return SignInPollResult{Status: "expired", Error: err.Error()}
	}
	if err != nil {
		return SignInPollResult{Status: "error", Error: err.Error()}
	}
	if token == "" {
		return SignInPollResult{Status: "pending"}
	}
	servers, err := fetchServers(token, p.ClientID)
	if err != nil {
		return SignInPollResult{Status: "error", Error: err.Error()}
	}
	s.mu.Lock()
	sess.accountToken = token // memory only — deleted with the session
	sess.servers = servers
	sess.fetched = true
	// Server switching: the approval also RETAINS the sign-in at
	// Service level, past the session's death — the account token
	// stays in memory (never persisted unless the user opts in at
	// pick time) so the connected server can be changed later
	// without signing in again.
	if s.retained == nil {
		s.retained = &retainedSignIn{}
	}
	s.retained.accountToken = token
	s.mu.Unlock()
	return SignInPollResult{Status: "ready", Servers: serverViews(servers)}
}

// ChooseSignInServer connects the app to one discovered server
// WITHOUT persisting the sign-in (remember=false). The original
// signature, kept for existing callers; the picker calls the
// Remember variant with the checkbox's state.
func (s *Service) ChooseSignInServer(sessionID string, index int) PlexTestResult {
	return s.ChooseSignInServerRemember(sessionID, index, false)
}

// ChooseSignInServerRemember connects the app to one discovered
// server: the chosen server's connections are tried in preference
// order (via connectToDiscoveredServer) and the winner is
// persisted. remember is the picker's "remember my sign-in"
// checkbox: when true, the session's ACCOUNT token is persisted
// to plex.json in the SAME save that stores the connection, so
// switching servers keeps working across restarts; when false,
// any previously remembered account token is cleared in that save
// — the checkbox's state is authoritative for the persisted
// token. Either way the token is also retained in memory (see
// PollSignInSession), so switching works for this run regardless.
// On success the session is deleted (its work is done); on total
// connection failure it is KEPT so the user can pick another
// server or retry. The result has TestPlex's shape.
func (s *Service) ChooseSignInServerRemember(sessionID string, index int, remember bool) PlexTestResult {
	p := s.plexSnapshot()
	s.mu.Lock()
	s.purgeSignInsLocked(time.Now())
	sess, ok := s.signins[sessionID]
	if !ok || !sess.fetched {
		s.mu.Unlock()
		return PlexTestResult{OK: false, Error: "that sign-in session expired — start again"}
	}
	if index < 0 || index >= len(sess.servers) {
		s.mu.Unlock()
		return PlexTestResult{OK: false, Error: "that server isn't in this sign-in session — start again"}
	}
	srv := sess.servers[index]
	accountToken := sess.accountToken
	s.mu.Unlock()

	persisted := ""
	if remember {
		persisted = accountToken
	}
	return s.connectToDiscoveredServer(srv, p.ClientID, func() {
		// Runs under s.mu inside the helper, as part of the same
		// atomic save: fold the remember choice in and retire the
		// session (the original flow deleted it at persist time,
		// even if the save itself then failed).
		s.plex.AccountToken = persisted
		delete(s.signins, sessionID)
	})
}

// connectToDiscoveredServer is the shared connection core behind
// ChooseSignInServerRemember (fresh sign-in) and SwitchPlexServer
// (retained sign-in): it tries srv's connections in preference
// order with the SERVER's accessToken, and the first that answers
// like a Plex server wins. The winner is persisted to plex.json
// (URL = winning URI, token = the server's accessToken, server
// name for display; existing path mappings and the client ID are
// kept). mutate, when non-nil, runs under s.mu just before the
// save so a caller can fold related state (the remember choice,
// session cleanup) into the SAME atomic write; a nil mutate
// leaves everything else exactly as stored — which is how a
// plain switch preserves the remember choice untouched.
func (s *Service) connectToDiscoveredServer(srv plexServer, clientID string, mutate func()) PlexTestResult {
	var lastErr error
	for _, conn := range srv.Connections {
		res, err := newPlexClient(conn.URI, srv.AccessToken, clientID).testConnection()
		if err != nil {
			lastErr = err
			continue
		}
		s.mu.Lock()
		s.plex.URL = strings.TrimRight(conn.URI, "/")
		s.plex.Token = srv.AccessToken // the SERVER's token — never the account token
		s.plex.ServerName = srv.Name
		if mutate != nil {
			mutate()
		}
		saveErr := savePlexSettingsFile(s.dataDir, s.plex)
		s.mu.Unlock()
		if saveErr != nil {
			return PlexTestResult{OK: false, Error: "connected to " + srv.Name + " but saving the connection failed: " + saveErr.Error()}
		}
		name := res.ServerName
		if name == "" {
			name = srv.Name
		}
		return PlexTestResult{OK: true, ServerName: name, Version: res.Version}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("the server reported no connections to try")
	}
	return PlexTestResult{OK: false, Error: fmt.Sprintf("could not reach %q on any of its %d connection(s): %v", srv.Name, len(srv.Connections), lastErr)}
}

// ---------------------------------------------------------------------------
// Server switching (mirrored 1:1 by /api/plex/servers, /api/plex/switch,
// /api/plex/forget-signin)
// ---------------------------------------------------------------------------

// PlexServersResult is the answer of GET /api/plex/servers:
// whether switching is available right now, whether the sign-in
// is also remembered on disk, and the account's servers as views
// (names + connection counts only — no tokens; choosing is by
// index, exactly like the sign-in picker).
type PlexServersResult struct {
	Available  bool               `json:"available"`
	Remembered bool               `json:"remembered"`
	Servers    []SignInServerView `json:"servers,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// signInSnapshot reads the switching state under one lock: the
// account token available for discovery (the retained in-memory
// one — a remembered token was rehydrated into it at startup, so
// there is exactly one place to look), the client ID discovery
// must use, and whether the token is ALSO persisted (remembered).
func (s *Service) signInSnapshot() (token, clientID string, remembered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retained != nil {
		token = s.retained.accountToken
	}
	return token, s.plex.ClientID, s.plex.AccountToken != ""
}

// cacheSwitchServersLocked stores a freshly fetched server list
// in the retained sign-in for SwitchPlexServer. Caller holds
// s.mu; the retained sign-in (and its token) already exists.
func (s *Service) cacheSwitchServersLocked(servers []plexServer) {
	if s.retained == nil {
		s.retained = &retainedSignIn{}
	}
	s.retained.servers = servers
	s.retained.fetched = true
}

// PlexServers lists the account's servers for the switcher. It
// runs discovery FRESH with the retained (or remembered) account
// token and caches the list, so a following switch connects to
// exactly the server the user was shown. Without an available
// sign-in it answers Available=false with the reason — the UI
// then points at the full sign-in instead of dead-ending.
func (s *Service) PlexServers() PlexServersResult {
	token, clientID, remembered := s.signInSnapshot()
	if token == "" {
		return PlexServersResult{Available: false, Error: "switching servers needs your Plex account sign-in — use \"Sign in with Plex\" first (the sign-in stays available while the app runs; tick \"remember my sign-in\" when picking a server to keep it across restarts)"}
	}
	servers, err := fetchServers(token, clientID)
	if err != nil {
		return PlexServersResult{Available: false, Remembered: remembered, Error: err.Error()}
	}
	s.mu.Lock()
	s.cacheSwitchServersLocked(servers)
	s.mu.Unlock()
	return PlexServersResult{Available: true, Remembered: remembered, Servers: serverViews(servers)}
}

// SwitchPlexServer connects the app to another of the account's
// servers WITHOUT a fresh sign-in, using the retained/remembered
// account token. The target comes from the freshest fetched list
// (the PlexServers cache; discovery re-runs here when nothing is
// cached yet). The answer has TestPlex's shape, like choosing.
//
// remember is the switch picker's checkbox, tri-state:
//   - nil: the persisted sign-in is left exactly as it was;
//   - true: the account token is persisted in the same save that
//     stores the new connection (opt in without re-signing-in);
//   - false: any persisted account token is cleared in that save
//     (the in-memory retained sign-in is unaffected either way —
//     switching keeps working until the app closes).
func (s *Service) SwitchPlexServer(index int, remember *bool) PlexTestResult {
	token, clientID, _ := s.signInSnapshot()
	if token == "" {
		return PlexTestResult{OK: false, Error: "switching servers needs your Plex account sign-in — sign in with Plex first"}
	}
	s.mu.Lock()
	var servers []plexServer
	if s.retained != nil && s.retained.fetched {
		servers = s.retained.servers
	}
	s.mu.Unlock()
	if servers == nil {
		fresh, err := fetchServers(token, clientID)
		if err != nil {
			return PlexTestResult{OK: false, Error: err.Error()}
		}
		servers = fresh
		s.mu.Lock()
		s.cacheSwitchServersLocked(servers)
		s.mu.Unlock()
	}
	if index < 0 || index >= len(servers) {
		return PlexTestResult{OK: false, Error: "that server isn't in the list — reopen the server list and try again"}
	}
	var mutate func()
	if remember != nil {
		mutate = func() {
			// Runs under s.mu inside the helper, as part of its save.
			if *remember {
				s.plex.AccountToken = token
			} else {
				s.plex.AccountToken = ""
			}
		}
	}
	return s.connectToDiscoveredServer(servers[index], clientID, mutate)
}

// ForgetSignIn clears ONLY the account sign-in: the persisted
// (remembered) token and the retained in-memory one, cache
// included. The connected server — URL, server token, name,
// mappings — stays connected: forgetting a sign-in is not
// disconnecting. Answers the settings view.
func (s *Service) ForgetSignIn() (PlexSettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retained = nil
	if s.plex.AccountToken != "" {
		s.plex.AccountToken = ""
		if err := savePlexSettingsFile(s.dataDir, s.plex); err != nil {
			return PlexSettingsView{}, err
		}
	}
	return s.settingsViewLocked(), nil
}
