package core

// plex.go — the optional Plex module, slice 1 (v0.2.0): library import.
//
// WHAT THIS IS: a read-only Plex Media Server client plus the service
// methods behind it. It fetches the library sections Plex knows (with
// the folder Locations each library was configured with) so the user
// can import them as scan roots instead of typing paths, and it fetches
// every item's on-disk file path so the app can cross-check "Plex
// thinks this exists" against the actual disk.
//
// DISCIPLINE (route-2 module rules from the SPEC):
//   - OPTIONAL: every file tool works with Plex unconfigured. Nothing
//     in the scanner imports or depends on this file.
//   - READ-ONLY against Plex: this client only ever issues GETs.
//   - TOKEN SECRECY: the token lives only in plex.json (0600) and in
//     the X-Plex-Token header. No service method returns it — the
//     views below physically have no field for it (unit-tested).
//   - TRANSPORT SEPARATION: the HTTP client is a core type of its own;
//     Service methods take/return plain structs, never net/http types.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Public types (service/API shapes)
// ---------------------------------------------------------------------------

// PlexSection is one Plex library section as returned by
// GET /library/sections: its key, display title, library type
// ("movie", "show", "artist", "photo", ...) and the folder Locations
// the library was configured with on the SERVER (unmapped).
type PlexSection struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Type      string   `json:"type"`
	Locations []string `json:"locations"`
}

// PlexItemPath is one media file Plex knows about: the item that owns
// it (movie title, or episode title + show title) and the file path
// from the item's Media > Part element — the SERVER's view of the
// path, unmapped.
type PlexItemPath struct {
	Title            string `json:"title"`
	Type             string `json:"type"` // "movie" | "episode"
	SectionKey       string `json:"section_key"`
	File             string `json:"file"`
	GrandparentTitle string `json:"grandparent_title,omitempty"` // show title for episodes
}

// PathMapping is one server-prefix -> local-prefix rewrite pair
// (the *arr-style remote path mapping). Applied to imported section
// locations and to item file paths when the app and the Plex server
// live on different machines.
type PathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PlexSettingsView is the ONLY shape of the Plex settings any API
// response may carry: configured flag, URL, mappings. There is
// deliberately no token field — see the token-secrecy rule above.
type PlexSettingsView struct {
	Configured bool   `json:"configured"`
	URL        string `json:"url"`
	// ServerName (v0.3.1) is the connected server's own name,
	// learned at sign-in (or by a successful Test connection), so
	// the UI can show "Connected ✓ — <name>" without a live call.
	ServerName string        `json:"server_name,omitempty"`
	Mappings   []PathMapping `json:"mappings"`
	// CanSwitch (server switching): an account sign-in is
	// available RIGHT NOW (retained in memory, or remembered on
	// disk), so the UI may offer "Change server…" — switching
	// needs the account token to re-run discovery.
	CanSwitch bool `json:"can_switch"`
	// SignInRemembered (server switching): the account token is
	// persisted in plex.json because the user opted in with
	// "remember my sign-in" — the UI shows the matching
	// "Forget saved sign-in" affordance. The token itself still
	// never appears in any view.
	SignInRemembered bool `json:"signin_remembered"`
}

// PlexTestResult is the outcome of a Test connection call: ok plus
// the server's own name/version, or a human-readable error.
type PlexTestResult struct {
	OK         bool   `json:"ok"`
	ServerName string `json:"server_name,omitempty"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
}

// PlexLocationView is one section Location after path mapping, with
// its v0.1.1 root-validation verdict: a location that doesn't exist
// on THIS machine is reported by name (Valid=false + Problem), the
// same way a typed scan root is.
type PlexLocationView struct {
	Path       string `json:"path"`        // mapped, local-side path
	ServerPath string `json:"server_path"` // as Plex reported it
	Valid      bool   `json:"valid"`
	Problem    string `json:"problem,omitempty"`
}

// PlexSectionView is a section with its locations mapped + validated,
// the shape the import panel renders.
type PlexSectionView struct {
	Key       string             `json:"key"`
	Title     string             `json:"title"`
	Type      string             `json:"type"`
	Locations []PlexLocationView `json:"locations"`
}

// PlexMissingEntry is one Plex item whose file was NOT found on disk
// (after mapping): title, section title, mapped local path, and the
// original server path for diagnosis.
type PlexMissingEntry struct {
	Title      string `json:"title"`
	Section    string `json:"section"`
	File       string `json:"file"`        // mapped, local-side
	ServerFile string `json:"server_file"` // as Plex reported it
}

// PlexMissingResult is the cross-check outcome: how many unique files
// were stat'ed, which are missing, and non-fatal warnings (e.g. a
// section whose episode list could not be fetched).
type PlexMissingResult struct {
	Checked  int                `json:"checked"`
	Missing  []PlexMissingEntry `json:"missing"`
	Warnings []string           `json:"warnings"`
}

// ---------------------------------------------------------------------------
// Persisted settings (plex.json in the data dir — separate from config.json)
// ---------------------------------------------------------------------------

// plexSettings is the on-disk shape of plex.json. The token is stored
// ONLY here; the file is created 0600 because the token is a password
// equivalent (the install docs say to treat it that way).
type plexSettings struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	ClientID string `json:"client_id"`
	// ServerName (v0.3.1) is the display name of the connected
	// server (from plex.tv discovery, or a successful connection
	// test). Not a secret; kept so state/settings can show it.
	ServerName string        `json:"server_name,omitempty"`
	Mappings   []PathMapping `json:"mappings"`
	// AccountToken (server switching) is the plex.tv ACCOUNT
	// token. It is persisted ONLY when the user explicitly opts
	// in with "remember my sign-in" at server-pick time, so they
	// can switch servers later without signing in again — that
	// opt-in is the single, deliberate exception to the sign-in
	// flow's memory-only rule. omitempty keeps the key out of
	// plex.json entirely for everyone who never opted in, and
	// the token is never returned by any API response (the views
	// physically have no field for it, same as Token).
	AccountToken string `json:"account_token,omitempty"`
}

// configured reports whether a usable URL + token pair is stored.
func (p *plexSettings) configured() bool {
	return p != nil && strings.TrimSpace(p.URL) != "" && strings.TrimSpace(p.Token) != ""
}

// newClientID mints the stable per-install X-Plex-Client-Identifier
// (16 random bytes, hex). Generated once and persisted with the
// settings; Plex uses it to tell app instances apart.
func newClientID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is vanishingly rare; fall back to a
		// time-derived value rather than failing service startup.
		return fmt.Sprintf("pfm-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// loadPlexSettings reads plex.json from the data dir, creating the
// default (unconfigured, with a fresh client ID) and persisting it
// 0600 when the file is missing or lacks a client ID — the client ID
// must be stable across runs, so it is written down immediately.
func loadPlexSettings(dataDir string) *plexSettings {
	p := &plexSettings{Mappings: []PathMapping{}}
	path := filepath.Join(dataDir, "plex.json")
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, p)
		if p.Mappings == nil {
			p.Mappings = []PathMapping{}
		}
	}
	if p.ClientID == "" {
		p.ClientID = newClientID()
		_ = savePlexSettingsFile(dataDir, p)
	}
	return p
}

// savePlexSettingsFile writes plex.json via temp-file + rename, with
// 0600 permissions (token inside — see the type comment).
func savePlexSettingsFile(dataDir string, p *plexSettings) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dataDir, "plex.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, "plex.json"))
}

// ---------------------------------------------------------------------------
// Path mapping
// ---------------------------------------------------------------------------

// applyMapping rewrites a server-side path through the mapping table.
// Rules (unit-tested):
//   - the LONGEST matching `from` prefix wins;
//   - a prefix only matches on a path boundary: the path equals
//     `from`, or the next character after the prefix is / or \
//     (so /media never rewrites /mediabox);
//   - the remainder is joined onto `to` using `to`'s separator style
//     (a Windows-style `to` like D:\Media joins with backslashes even
//     when the app runs on Linux, because the string is describing a
//     Windows path);
//   - with no matching mapping the path passes through cleaned.
func applyMapping(p string, mappings []PathMapping) string {
	best := -1
	bestLen := -1
	var bestFrom string
	for i, m := range mappings {
		from := strings.TrimRight(strings.TrimSpace(m.From), "/\\")
		if from == "" || strings.TrimSpace(m.To) == "" {
			continue
		}
		if pathPrefixOnBoundary(p, from) && len(from) > bestLen {
			best, bestLen, bestFrom = i, len(from), from
		}
	}
	if best < 0 {
		return filepath.Clean(p)
	}
	to := strings.TrimRight(strings.TrimSpace(mappings[best].To), "/\\")
	rest := strings.TrimLeft(p[len(bestFrom):], "/\\")
	if rest == "" {
		return to
	}
	// Pick the separator from the `to` side's style; split the
	// remainder on either separator so server (POSIX) remainders join
	// correctly onto Windows-style targets and vice versa.
	sep := string(filepath.Separator)
	if strings.Contains(to, "\\") && !strings.Contains(to, "/") {
		sep = "\\"
	}
	parts := strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return to
	}
	joined := to + sep + strings.Join(parts, sep)
	if sep == "\\" {
		return joined // filepath.Clean would not understand a Windows path on Linux
	}
	return filepath.Clean(joined)
}

// pathPrefixOnBoundary reports whether path starts with prefix AND
// the match ends on a path boundary (end of string, or the next
// character is a separator of either flavour).
func pathPrefixOnBoundary(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	next := path[len(prefix)]
	return next == '/' || next == '\\'
}

// ---------------------------------------------------------------------------
// Plex HTTP client (stdlib only; Plex answers XML by preference)
// ---------------------------------------------------------------------------

// plexClient is a minimal read-only Plex Media Server client. It is a
// core type of its own (the transport-separation rule bans net/http
// TYPES from Service signatures, not an HTTP client living in core).
type plexClient struct {
	baseURL  string // no trailing slash
	token    string
	clientID string
	http     *http.Client
}

// newPlexClient builds a client for one server. baseURL must already
// be validated/normalized (see SavePlexSettings).
func newPlexClient(baseURL, token, clientID string) *plexClient {
	return &plexClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		clientID: clientID,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// get issues one authenticated GET and returns the body. Errors are
// written for the user, not the log: unreachable names the URL, an
// auth failure says "Plex rejected the token (401)", anything else
// names the status. The token itself is never included in an error.
func (c *plexClient) get(path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("bad Plex request URL %q: %v", u, err)
	}
	req.Header.Set("X-Plex-Token", c.token)
	req.Header.Set("X-Plex-Product", plexProductName)
	req.Header.Set("X-Plex-Version", Version)
	req.Header.Set("X-Plex-Client-Identifier", c.clientID)
	req.Header.Set("Accept", "application/xml")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Plex at %s — %v", c.baseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the answer from Plex at %s failed: %v", c.baseURL, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("Plex rejected the token (%d)", resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("Plex at %s answered HTTP %d", c.baseURL, resp.StatusCode)
	}
	return body, nil
}

// plexIdentityXML mirrors the MediaContainer attributes /identity
// (and /) answer with: friendlyName, machineIdentifier, version.
type plexIdentityXML struct {
	XMLName           xml.Name `xml:"MediaContainer"`
	FriendlyName      string   `xml:"friendlyName,attr"`
	MachineIdentifier string   `xml:"machineIdentifier,attr"`
	Version           string   `xml:"version,attr"`
}

// testConnection verifies the server answers like a Plex server and
// the token is accepted. It tries GET /identity first (the cheap
// canonical probe) and falls back to GET / on servers that don't
// support it. An auth rejection is returned immediately — retrying
// the root with the same token would only say the same thing slower.
func (c *plexClient) testConnection() (*PlexTestResult, error) {
	body, err := c.get("/identity", nil)
	if err != nil {
		if strings.Contains(err.Error(), "rejected the token") {
			return nil, err
		}
		body, err = c.get("/", nil)
		if err != nil {
			return nil, err
		}
	}
	var id plexIdentityXML
	if xerr := xml.Unmarshal(body, &id); xerr != nil || (id.MachineIdentifier == "" && id.FriendlyName == "" && id.Version == "") {
		return nil, fmt.Errorf("that URL did not answer like a Plex server (%s)", c.baseURL)
	}
	name := id.FriendlyName
	if name == "" {
		name = id.MachineIdentifier
	}
	return &PlexTestResult{OK: true, ServerName: name, Version: id.Version}, nil
}

// plexSectionsXML mirrors GET /library/sections: MediaContainer >
// Directory (key/title/type attributes) > Location (path attribute).
type plexSectionsXML struct {
	XMLName xml.Name `xml:"MediaContainer"`
	Dirs    []struct {
		Key       string `xml:"key,attr"`
		Title     string `xml:"title,attr"`
		Type      string `xml:"type,attr"`
		Locations []struct {
			Path string `xml:"path,attr"`
		} `xml:"Location"`
	} `xml:"Directory"`
}

// sections fetches and parses the server's library sections. ALL
// sections are returned (music/photo included) — choosing which ones
// imports apply to is the service/UI layer's call.
func (c *plexClient) sections() ([]PlexSection, error) {
	body, err := c.get("/library/sections", nil)
	if err != nil {
		return nil, err
	}
	var raw plexSectionsXML
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("that URL did not answer like a Plex server (%s): sections reply was not parseable XML", c.baseURL)
	}
	out := make([]PlexSection, 0, len(raw.Dirs))
	for _, d := range raw.Dirs {
		sec := PlexSection{Key: d.Key, Title: d.Title, Type: d.Type, Locations: []string{}}
		for _, loc := range d.Locations {
			if loc.Path != "" {
				sec.Locations = append(sec.Locations, loc.Path)
			}
		}
		out = append(out, sec)
	}
	return out, nil
}

// plexItemsXML mirrors one page of GET /library/sections/{key}/all:
// MediaContainer(totalSize) > Video(title, grandparentTitle) >
// Media > Part(file).
type plexItemsXML struct {
	XMLName   xml.Name `xml:"MediaContainer"`
	TotalSize int      `xml:"totalSize,attr"`
	Videos    []struct {
		Title            string `xml:"title,attr"`
		GrandparentTitle string `xml:"grandparentTitle,attr"`
		Type             string `xml:"type,attr"`
		Media            []struct {
			Parts []struct {
				File string `xml:"file,attr"`
			} `xml:"Part"`
		} `xml:"Media"`
	} `xml:"Video"`
}

// plexPageSize is the X-Plex-Container page size for item listings:
// big enough that real libraries page a handful of times, small
// enough that a page stays cheap on a seedbox connection.
const plexPageSize = 500

// fetchSectionItems fetches EVERY item file path of one section,
// paginating with X-Plex-Container-Start/Size until totalSize is
// exhausted. Shows are fetched with type=4 (episodes — a bare /all
// would return shows/seasons, not the files). Each returned path is
// one Part file; multi-part items yield several paths.
func (c *plexClient) fetchSectionItems(sec PlexSection) ([]PlexItemPath, error) {
	itemType := "movie"
	if sec.Type == "show" {
		itemType = "episode"
	}
	out := []PlexItemPath{}
	start := 0
	for {
		q := url.Values{}
		q.Set("X-Plex-Container-Start", fmt.Sprint(start))
		q.Set("X-Plex-Container-Size", fmt.Sprint(plexPageSize))
		if sec.Type == "show" {
			q.Set("type", "4")
		}
		body, err := c.get("/library/sections/"+url.PathEscape(sec.Key)+"/all", q)
		if err != nil {
			return nil, err
		}
		var page plexItemsXML
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("Plex item list for section %q was not parseable XML", sec.Title)
		}
		for _, v := range page.Videos {
			typ := v.Type
			if typ == "" {
				typ = itemType
			}
			for _, m := range v.Media {
				for _, part := range m.Parts {
					if part.File == "" {
						continue
					}
					out = append(out, PlexItemPath{
						Title:            v.Title,
						Type:             typ,
						SectionKey:       sec.Key,
						File:             part.File,
						GrandparentTitle: v.GrandparentTitle,
					})
				}
			}
		}
		start += len(page.Videos)
		if len(page.Videos) == 0 || page.TotalSize == 0 || start >= page.TotalSize {
			break
		}
	}
	return out, nil
}

// itemPaths fetches item paths for every movie/show section. A
// section that fails (e.g. a server that can't serve the episodes
// listing) is recorded as a WARNING and skipped — one bad section
// must not sink the cross-check for the whole library.
func (c *plexClient) itemPaths(sections []PlexSection) ([]PlexItemPath, []string) {
	out := []PlexItemPath{}
	warnings := []string{}
	for _, sec := range sections {
		if sec.Type != "movie" && sec.Type != "show" {
			continue
		}
		paths, err := c.fetchSectionItems(sec)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("section %q: %v", sec.Title, err))
			continue
		}
		out = append(out, paths...)
	}
	return out, warnings
}

// ---------------------------------------------------------------------------
// Service methods (mirrored 1:1 by the /api/plex/* endpoints)
// ---------------------------------------------------------------------------

// plexSnapshot returns a copy of the stored Plex settings. Callers
// that do network work snapshot first and release the lock — a slow
// Plex server must never block scan-state polling behind s.mu.
func (s *Service) plexSnapshot() plexSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.plex
}

// plexClientLocked-free constructor from a settings snapshot.
func (p plexSettings) client() *plexClient {
	return newPlexClient(p.URL, p.Token, p.ClientID)
}

// settingsViewLocked builds the token-free settings view from the
// current state. Caller holds s.mu (the switch booleans read the
// retained sign-in, which lives under the same lock).
func (s *Service) settingsViewLocked() PlexSettingsView {
	return PlexSettingsView{
		Configured:       s.plex.configured(),
		URL:              s.plex.URL,
		ServerName:       s.plex.ServerName,
		Mappings:         append([]PathMapping{}, s.plex.Mappings...),
		CanSwitch:        s.retained != nil && s.retained.accountToken != "",
		SignInRemembered: s.plex.AccountToken != "",
	}
}

// GetPlexSettings returns the token-free settings view.
func (s *Service) GetPlexSettings() PlexSettingsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsViewLocked()
}

// normalizePlexURL cleans one user-typed Plex server address
// (v0.3.1). A bare "host:port" with no scheme gets "http://"
// prepended: before this, SavePlexSettings rejected it outright,
// the save quietly failed, Plex stayed unconfigured, and every Plex
// button in the UI stayed dead with the reason buried in small red
// text in Settings (the user's "button does literally nothing"
// live report). A value that still doesn't parse, has no host, or
// uses a scheme other than http/https is rejected. Trailing
// slashes are stripped.
func normalizePlexURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("Plex URL must look like http://host:32400 (got %q)", raw)
	}
	return strings.TrimRight(v, "/"), nil
}

// SavePlexSettings validates and persists the Plex settings.
// Token semantics (so the UI never has to echo a saved secret back):
//   - an EMPTY submitted token keeps the stored one;
//   - the literal "__clear__" (in either field) clears URL, token
//     and mappings (the client ID is kept — it is not a secret);
//     it ALSO wipes the account sign-in (the persisted remembered
//     token and the retained in-memory one) — Disconnect means
//     fully disconnected, switching state included;
//   - an empty submitted URL likewise keeps the stored URL;
//   - mappings replace the stored ones whenever a non-nil list is
//     submitted (an empty list clears them).
//
// A submitted URL is normalized first (see normalizePlexURL — a
// scheme is optional since v0.3.1; http/https + a host required).
func (s *Service) SavePlexSettings(rawURL, token string, mappings []PathMapping) (PlexSettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rawURL == "__clear__" || token == "__clear__" {
		s.plex.URL, s.plex.Token, s.plex.ServerName, s.plex.Mappings = "", "", "", []PathMapping{}
		s.plex.AccountToken = ""
		s.retained = nil
		if err := savePlexSettingsFile(s.dataDir, s.plex); err != nil {
			return PlexSettingsView{}, err
		}
		return s.settingsViewLocked(), nil
	}
	if strings.TrimSpace(rawURL) != "" {
		norm, err := normalizePlexURL(rawURL)
		if err != nil {
			return PlexSettingsView{}, err
		}
		if norm != s.plex.URL {
			// The address changed: any server name learned for the
			// old one no longer applies (a test or a sign-in
			// re-learns it).
			s.plex.ServerName = ""
		}
		s.plex.URL = norm
	}
	if token != "" {
		s.plex.Token = token
	}
	if mappings != nil {
		cleaned := make([]PathMapping, 0, len(mappings))
		for _, m := range mappings {
			m.From = strings.TrimSpace(m.From)
			m.To = strings.TrimSpace(m.To)
			if m.From != "" && m.To != "" {
				cleaned = append(cleaned, m)
			}
		}
		s.plex.Mappings = cleaned
	}
	if err := savePlexSettingsFile(s.dataDir, s.plex); err != nil {
		return PlexSettingsView{}, err
	}
	return s.settingsViewLocked(), nil
}

// TestPlexWith probes a server using the values handed to it first
// and the saved settings as the fallback (v0.3.1 — "test what's
// typed"): a non-empty rawURL (normalized like a save would) wins
// over the saved URL; a non-empty token wins over the saved token.
// Before this, Test connection only ever tested the SAVED settings,
// so typing an address and hitting Test — without Save — tested an
// empty config and failed for reasons invisible to the user. The
// outcome is reported in-band: {ok, server_name, version, error} —
// a failed test is information for the Settings page, not an
// exception. Nothing is persisted by this method.
func (s *Service) TestPlexWith(rawURL, token string) PlexTestResult {
	p := s.plexSnapshot()
	useURL := p.URL
	if strings.TrimSpace(rawURL) != "" {
		norm, err := normalizePlexURL(rawURL)
		if err != nil {
			return PlexTestResult{OK: false, Error: err.Error()}
		}
		useURL = norm
	}
	useToken := p.Token
	if token != "" {
		useToken = token
	}
	if strings.TrimSpace(useURL) == "" || strings.TrimSpace(useToken) == "" {
		return PlexTestResult{OK: false, Error: "Plex is not configured — set the server URL and token in Settings first"}
	}
	res, err := newPlexClient(useURL, useToken, p.ClientID).testConnection()
	if err != nil {
		return PlexTestResult{OK: false, Error: err.Error()}
	}
	return *res
}

// TestPlex probes the SAVED settings (identity, with the root
// fallback). On success it also remembers the server's own name in
// the settings, so the UI can show "Connected ✓ — <name>" from
// state without a live call on every poll.
func (s *Service) TestPlex() PlexTestResult {
	res := s.TestPlexWith("", "")
	if res.OK && res.ServerName != "" {
		s.mu.Lock()
		if s.plex.ServerName != res.ServerName {
			s.plex.ServerName = res.ServerName
			_ = savePlexSettingsFile(s.dataDir, s.plex)
		}
		s.mu.Unlock()
	}
	return res
}

// PlexSections fetches the server's sections live and returns them
// with each Location mapped to this machine's paths and validated
// with the EXISTING v0.1.1 root validation — an unmapped or missing
// location is named as a problem here, before it can become a
// silently-empty scan.
func (s *Service) PlexSections() ([]PlexSectionView, error) {
	p := s.plexSnapshot()
	if !p.configured() {
		return nil, fmt.Errorf("Plex is not configured — set the server URL and token in Settings first")
	}
	sections, err := p.client().sections()
	if err != nil {
		return nil, err
	}
	out := make([]PlexSectionView, 0, len(sections))
	for _, sec := range sections {
		view := PlexSectionView{Key: sec.Key, Title: sec.Title, Type: sec.Type, Locations: []PlexLocationView{}}
		for _, loc := range sec.Locations {
			mapped := applyMapping(loc, p.Mappings)
			lv := PlexLocationView{Path: mapped, ServerPath: loc}
			if valid, problems := validateRoots([]string{mapped}); len(valid) == 1 {
				lv.Valid = true
				lv.Path = valid[0] // the cleaned form validation settled on
			} else if len(problems) > 0 {
				lv.Problem = problems[0]
			}
			view.Locations = append(view.Locations, lv)
		}
		out = append(out, view)
	}
	return out, nil
}

// PlexMissingOnDisk is the Plex-vs-disk cross-check: fetch every
// item file path Plex knows (movie + show sections), map each to
// this machine, deduplicate, and os.Stat it. Files that aren't there
// come back as missing entries with both the mapped and the
// original server path. Stat work runs on a small worker pool — the
// list can be tens of thousands of paths on a big library.
func (s *Service) PlexMissingOnDisk() (PlexMissingResult, error) {
	p := s.plexSnapshot()
	if !p.configured() {
		return PlexMissingResult{}, fmt.Errorf("Plex is not configured — set the server URL and token in Settings first")
	}
	client := p.client()
	sections, err := client.sections()
	if err != nil {
		return PlexMissingResult{}, err
	}
	titles := map[string]string{}
	for _, sec := range sections {
		titles[sec.Key] = sec.Title
	}
	paths, warnings := client.itemPaths(sections)
	if warnings == nil {
		warnings = []string{}
	}
	// Deduplicate identical server files (a multi-version item can
	// repeat a part; the same file listed twice is one check).
	type job struct {
		item   PlexItemPath
		mapped string
	}
	seen := map[string]bool{}
	jobs := []job{}
	for _, it := range paths {
		mapped := applyMapping(it.File, p.Mappings)
		if seen[mapped] {
			continue
		}
		seen[mapped] = true
		jobs = append(jobs, job{item: it, mapped: mapped})
	}
	// Stat pool: 8 workers, order restored afterwards by sorting.
	missing := []PlexMissingEntry{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, err := os.Stat(j.mapped)
			if err == nil && !info.IsDir() {
				return // present, as expected
			}
			mu.Lock()
			missing = append(missing, PlexMissingEntry{
				Title:      displayTitle(j.item),
				Section:    titles[j.item.SectionKey],
				File:       j.mapped,
				ServerFile: j.item.File,
			})
			mu.Unlock()
		}(j)
	}
	wg.Wait()
	sort.Slice(missing, func(i, k int) bool { return missing[i].File < missing[k].File })
	return PlexMissingResult{Checked: len(jobs), Missing: missing, Warnings: warnings}, nil
}

// displayTitle renders an item path's owner for result lists:
// "Show — Episode" for episodes, the bare title otherwise.
func displayTitle(it PlexItemPath) string {
	if it.GrandparentTitle != "" && it.Title != "" {
		return it.GrandparentTitle + " — " + it.Title
	}
	return it.Title
}
