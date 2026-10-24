package core

// opensubtitles.go — the OpenSubtitles client + wizard slices
// W4–W5 (v1.0.0, SUBTITLE-WIZARD-SPEC Part 1).
//
// Bring-your-own-key by design: the user pastes THEIR
// OpenSubtitles API key (and, for downloads, their account
// username/password — the documented flow is login-first) into
// Settings; both live only in opensubtitles.json (mode 0600,
// the plex.json pattern) and are NEVER returned by any API
// response — the settings view carries {enabled, configured,
// has_login} and nothing else.
//
// Endpoint realities (spec §1.1, verified against the official
// docs 2026-10-23 — citations in the spec):
//   - Every call carries "Api-Key" + a descriptive "User-Agent".
//   - Search (GET /subtitles) needs the key only.
//   - Download is login-first: POST /login → JWT + the USER's
//     own base_url; POST {base_url}/download {file_id} → a
//     short-lived link + the account's remaining quota. Quota
//     numbers come from API RESPONSES ONLY (the docs conflict
//     with themselves — spec §1.2) and are displayed verbatim.
//   - Search by the documented content hash (spec §1.3):
//     filesize + the little-endian uint64 sums of the first and
//     last 64 KiB, as 16 lowercase hex digits.
//
// VERIFICATION BOUNDARY: no live API key exists in the build
// environment, so everything here is verified against an
// httptest mock implementing the spec's documented request /
// response shapes. Response JSON is parsed flexibly (unknown
// fields ignored, missing fields default) — the shapes NOT
// pinned by the spec are listed in verification-1.0.0's
// RESULTS under "to verify live". Nothing in this file claims
// the live API was exercised.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// osAPIBase is the public API base (a var so tests can point
// the client at a mock server).
var osAPIBase = "https://api.opensubtitles.com/api/v1"

// osUserAgent identifies the app on every API call (the API
// requires a descriptive User-Agent).
const osUserAgent = "WellKeptLib v1.3.3"

// osHTTPClient bounds every OpenSubtitles call.
var osHTTPClient = &http.Client{Timeout: 30 * time.Second}

// osSecretsFile is the 0600 secrets document (plex.json's
// sibling). APIKey/Username/Password NEVER leave the process.
type osSecretsFile struct {
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"api_key,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// osSecretsPath is the secrets file's location.
func (s *Service) osSecretsPath() string {
	return filepath.Join(s.dataDir, "opensubtitles.json")
}

// loadOSSecrets reads the secrets file (best-effort: missing =
// unconfigured, never a startup failure).
func loadOSSecrets(dataDir string) *osSecretsFile {
	sec := &osSecretsFile{}
	b, err := os.ReadFile(filepath.Join(dataDir, "opensubtitles.json"))
	if err == nil {
		_ = json.Unmarshal(b, sec)
	}
	return sec
}

// saveOSSecretsLocked writes the secrets file at 0600.
func (s *Service) saveOSSecretsLocked() error {
	b, err := json.MarshalIndent(s.osSecrets, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.osSecretsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.osSecretsPath())
}

// OSSecretsView is the ONLY shape any API returns about the
// OpenSubtitles settings: presence, never values.
type OSSecretsView struct {
	Enabled    bool `json:"enabled"`
	Configured bool `json:"configured"` // an API key is stored
	HasLogin   bool `json:"has_login"`  // username + password stored
}

// OSSecretsInput updates the secrets file. Empty credential
// fields mean "keep the stored value" (the UI never receives
// the stored values, so it cannot echo them back); Clear drops
// the stored credentials entirely.
type OSSecretsInput struct {
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"api_key"`
	Username string `json:"username"`
	Password string `json:"password"`
	Clear    bool   `json:"clear"`
}

// OSSecretsView returns the presence-only view.
func (s *Service) OSSecretsState() OSSecretsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return osSecretsViewLocked(s)
}

func osSecretsViewLocked(s *Service) OSSecretsView {
	sec := s.osSecrets
	if sec == nil {
		sec = &osSecretsFile{}
	}
	return OSSecretsView{
		Enabled:    sec.Enabled,
		Configured: sec.APIKey != "",
		HasLogin:   sec.Username != "" && sec.Password != "",
	}
}

// SaveOSSecrets applies a settings update (the stored session
// is dropped whenever credentials change, so the next download
// re-logs-in with the new ones).
func (s *Service) SaveOSSecrets(in OSSecretsInput) (OSSecretsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.osSecrets == nil {
		s.osSecrets = &osSecretsFile{}
	}
	sec := s.osSecrets
	sec.Enabled = in.Enabled
	if in.Clear {
		sec.APIKey, sec.Username, sec.Password = "", "", ""
	} else {
		if strings.TrimSpace(in.APIKey) != "" {
			sec.APIKey = strings.TrimSpace(in.APIKey)
		}
		if strings.TrimSpace(in.Username) != "" {
			sec.Username = strings.TrimSpace(in.Username)
		}
		if in.Password != "" {
			sec.Password = in.Password
		}
	}
	s.osSession = nil
	if err := s.saveOSSecretsLocked(); err != nil {
		return OSSecretsView{}, err
	}
	return osSecretsViewLocked(s), nil
}

// osSession is the in-memory login session: the JWT, the
// user-specific download base URL, and the quota the API
// reported (spec §1.4 — numbers from responses, never docs).
type osSession struct {
	Token     string
	BaseURL   string
	Username  string
	Allowed   int    // downloads allowed per the account (login user payload / download response)
	Remaining int    // remaining at last report; -1 = never reported
	ResetTime string // verbatim from the API ("" = never reported)
	Level     string
	VIP       bool
}

// OSQuota is the picker's quota readout — the API's own numbers,
// verbatim, with an honest "not reported yet" state.
type OSQuota struct {
	LoggedIn  bool   `json:"logged_in"`
	Username  string `json:"username,omitempty"`
	Level     string `json:"level,omitempty"`
	VIP       bool   `json:"vip,omitempty"`
	Allowed   int    `json:"allowed_downloads"`
	Remaining int    `json:"remaining"` // -1 when the API has not reported yet
	ResetTime string `json:"reset_time,omitempty"`
}

// osCredentials snapshots the secrets + enabled state under the
// lock for use by network calls (which never hold s.mu).
func (s *Service) osCredentials() (*osSecretsFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.osSecrets == nil || !s.osSecrets.Enabled {
		return nil, errors.New("OpenSubtitles is off — enable it and add your API key in Settings")
	}
	if s.osSecrets.APIKey == "" {
		return nil, errors.New("no OpenSubtitles API key saved — add yours in Settings (the app ships without one, by design)")
	}
	cp := *s.osSecrets
	return &cp, nil
}

// osDo performs one API call with the standard headers. base is
// osAPIBase or the session's user base URL; token (when set) is
// sent as a Bearer header. The response body is returned for the
// caller to decode; a non-2xx becomes an error carrying the
// API's own message where it provided one.
func osDo(sec *osSecretsFile, method, reqURL, token string, payload any) ([]byte, int, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Api-Key", sec.APIKey)
	req.Header.Set("User-Agent", osUserAgent)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := osHTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("OpenSubtitles request failed: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := osErrorMessage(b)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return b, resp.StatusCode, fmt.Errorf("OpenSubtitles: %s", msg)
	}
	return b, resp.StatusCode, nil
}

// osErrorMessage extracts the API's error text ({"message"} or
// {"errors":[...]}) where present.
func osErrorMessage(b []byte) string {
	var v struct {
		Message string   `json:"message"`
		Errors  []string `json:"errors"`
	}
	if json.Unmarshal(b, &v) == nil {
		if v.Message != "" {
			return v.Message
		}
		if len(v.Errors) > 0 {
			return strings.Join(v.Errors, "; ")
		}
	}
	return ""
}

// osLogin performs the login-first flow (spec §1.1): POST /login
// with the account credentials → JWT + the user's own base_url
// + the user payload (quota fields as reported). The session is
// cached in memory; a cached session for the same username is
// reused.
func (s *Service) osLogin(sec *osSecretsFile) (*osSession, error) {
	if sec.Username == "" || sec.Password == "" {
		return nil, errors.New("downloads need your OpenSubtitles username + password as well as the API key (Settings → OpenSubtitles) — search works with the key alone")
	}
	s.mu.Lock()
	if s.osSession != nil && s.osSession.Username == sec.Username && s.osSession.Token != "" {
		sess := s.osSession
		s.mu.Unlock()
		return sess, nil
	}
	s.mu.Unlock()
	b, _, err := osDo(sec, http.MethodPost, osAPIBase+"/login", "", map[string]string{
		"username": sec.Username,
		"password": sec.Password,
	})
	if err != nil {
		return nil, err
	}
	var lr struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
		User    struct {
			Username           string `json:"username"`
			Level              string `json:"level"`
			VIP                bool   `json:"vip"`
			DownloadsCount     int    `json:"downloads_count"`
			RemainingDownloads int    `json:"remaining_downloads"`
		} `json:"user"`
	}
	if err := json.Unmarshal(b, &lr); err != nil {
		return nil, fmt.Errorf("OpenSubtitles login response did not parse: %w", err)
	}
	if lr.Token == "" {
		return nil, errors.New("OpenSubtitles login returned no token")
	}
	sess := &osSession{
		Token: lr.Token, BaseURL: lr.BaseURL, Username: sec.Username,
		Remaining: -1, Level: lr.User.Level, VIP: lr.User.VIP,
		Allowed: lr.User.DownloadsCount,
	}
	if lr.BaseURL == "" {
		sess.BaseURL = osAPIBase
	}
	s.mu.Lock()
	s.osSession = sess
	s.mu.Unlock()
	return sess, nil
}

// Quota returns the last quota the API reported (search shows
// it read-only; a download attempt logs in and refreshes it).
func (s *Service) Quota() OSQuota {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := OSQuota{Remaining: -1}
	if s.osSession != nil {
		q.LoggedIn = true
		q.Username = s.osSession.Username
		q.Level = s.osSession.Level
		q.VIP = s.osSession.VIP
		q.Allowed = s.osSession.Allowed
		q.Remaining = s.osSession.Remaining
		q.ResetTime = s.osSession.ResetTime
	}
	return q
}

// openSubtitlesHash computes the documented content hash
// (spec §1.3): the file size, plus the sum of every little-
// endian uint64 in the first 64 KiB, plus the sum over the last
// 64 KiB — formatted as exactly 16 lowercase hex digits. A file
// smaller than 64 KiB has both reads covering the whole file (so
// its words sum twice); only COMPLETE 8-byte words are summed.
func openSubtitlesHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	sumChunk := func(off int64) (uint64, error) {
		buf := make([]byte, 65536)
		n, err := f.ReadAt(buf, off)
		if err != nil && err != io.EOF {
			return 0, err
		}
		buf = buf[:n]
		var sum uint64
		for i := 0; i+8 <= len(buf); i += 8 {
			sum += binary.LittleEndian.Uint64(buf[i : i+8])
		}
		return sum, nil
	}
	var hash uint64 = uint64(size)
	head, err := sumChunk(0)
	if err != nil {
		return "", err
	}
	hash += head
	tailOff := size - 65536
	if tailOff < 0 {
		tailOff = 0
	}
	tail, err := sumChunk(tailOff)
	if err != nil {
		return "", err
	}
	hash += tail
	return fmt.Sprintf("%016x", hash), nil
}

// OSSearchRequest is the picker's search ask: hash, or query, or
// both (the wizard does hash first, query as the fallback).
type OSSearchRequest struct {
	Path string `json:"path"`
	Mode string `json:"mode"` // "hash" | "query"
	// Language is a 639-1 code (the findings' language), sent as
	// the search's languages filter.
	Language string `json:"language"`
}

// OSResult is one picker row — a single downloadable subtitle
// FILE (a result entry can carry several files; each becomes a
// row). Badges come straight from the entry's flags.
type OSResult struct {
	FileID            int     `json:"file_id"`
	Release           string  `json:"release"`
	Language          string  `json:"language"`
	DownloadCount     int     `json:"download_count"`
	Rating            float64 `json:"rating"`
	HearingImpaired   bool    `json:"hearing_impaired"`
	Forced            bool    `json:"forced"`
	AITranslated      bool    `json:"ai_translated"`
	MachineTranslated bool    `json:"machine_translated"`
	FromTrusted       bool    `json:"from_trusted"`
	HashMatch         bool    `json:"hash_match"`
}

// OSSearchResponse is the picker payload: ranked results, the
// honest mode note, and the quota readout.
type OSSearchResponse struct {
	Mode    string     `json:"mode"`
	Note    string     `json:"note,omitempty"`
	Results []OSResult `json:"results"`
	Quota   OSQuota    `json:"quota"`
}

// osSearchWire is the documented search response shape
// (GET /subtitles → {"total_pages", "total_count", "page",
// "data"|"results": [{...,"attributes": {...}}]}). The list key
// and inner field spellings are among the shapes the spec does
// not pin byte-for-byte — parsed tolerantly (see the file
// comment's verification boundary).
type osSearchWire struct {
	TotalCount int             `json:"total_count"`
	Data       []osSearchEntry `json:"data"`
	Results    []osSearchEntry `json:"results"`
}

type osSearchEntry struct {
	Attributes struct {
		Language          string  `json:"language"`
		Release           string  `json:"release"`
		DownloadCount     int     `json:"download_count"`
		Ratings           float64 `json:"ratings"`
		HearingImpaired   bool    `json:"hearing_impaired"`
		ForeignPartsOnly  bool    `json:"foreign_parts_only"`
		AITranslated      bool    `json:"ai_translated"`
		MachineTranslated bool    `json:"machine_translated"`
		FromTrusted       bool    `json:"from_trusted"`
		MoviehashMatch    bool    `json:"moviehash_match"`
		Files             []struct {
			FileID   int    `json:"file_id"`
			FileName string `json:"file_name"`
		} `json:"files"`
	} `json:"attributes"`
}

// SearchSubtitles runs a picker search for the finding's media
// file (spec W4). Hash mode computes the content hash and asks
// for moviehash matches; query mode asks by the parsed title /
// year / season / episode. Hash-match entries always rank
// first; the user picks — nothing is ever auto-selected.
func (s *Service) SearchSubtitles(q OSSearchRequest) (OSSearchResponse, error) {
	sec, err := s.osCredentials()
	if err != nil {
		return OSSearchResponse{}, err
	}
	s.mu.Lock()
	item := s.itemLocked(q.Path)
	s.mu.Unlock()
	if item == nil {
		return OSSearchResponse{}, fmt.Errorf("no media in the current results at %s", q.Path)
	}
	params := url.Values{}
	// Findings carry 639-1 codes already; langCodeNormal also
	// folds any 639-2 the caller passes down to 639-1, which is
	// what the API's languages filter speaks (spec §1.5).
	if two := langCodeNormal(q.Language); two != "" {
		params.Set("languages", two)
	}
	mode := q.Mode
	switch mode {
	case "hash":
		hash, err := openSubtitlesHash(item.Path)
		if err != nil {
			return OSSearchResponse{}, fmt.Errorf("hashing %s: %w", item.Name, err)
		}
		params.Set("moviehash", hash)
	case "query":
		if item.Title == "" {
			return OSSearchResponse{}, errors.New("this file's name has no parseable title to search by — the hash search is the route for it")
		}
		params.Set("query", item.Title)
		if item.Year > 0 {
			params.Set("year", strconv.Itoa(item.Year))
		}
		if item.Kind == "episode" {
			params.Set("type", "episode")
			if len(item.Episodes) > 0 {
				params.Set("season_number", strconv.Itoa(item.Season))
				params.Set("episode_number", strconv.Itoa(item.Episodes[0]))
			}
		} else {
			params.Set("type", "movie")
		}
	default:
		return OSSearchResponse{}, fmt.Errorf("unknown search mode %q (want hash or query)", mode)
	}
	b, _, err := osDo(sec, http.MethodGet, osAPIBase+"/subtitles?"+params.Encode(), "", nil)
	if err != nil {
		return OSSearchResponse{}, err
	}
	var wire osSearchWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return OSSearchResponse{}, fmt.Errorf("OpenSubtitles search response did not parse: %w", err)
	}
	entries := wire.Data
	if len(entries) == 0 {
		entries = wire.Results
	}
	out := OSSearchResponse{Mode: mode, Results: []OSResult{}, Quota: s.Quota()}
	for _, e := range entries {
		a := e.Attributes
		for _, f := range a.Files {
			if f.FileID == 0 {
				continue
			}
			out.Results = append(out.Results, OSResult{
				FileID: f.FileID, Release: a.Release, Language: a.Language,
				DownloadCount: a.DownloadCount, Rating: a.Ratings,
				HearingImpaired: a.HearingImpaired, Forced: a.ForeignPartsOnly,
				AITranslated: a.AITranslated, MachineTranslated: a.MachineTranslated,
				FromTrusted: a.FromTrusted, HashMatch: a.MoviehashMatch,
			})
		}
	}
	// Ranking: hash matches first, then trusted, then download
	// count (spec W4 — the user still makes the final call).
	sort.SliceStable(out.Results, func(i, j int) bool {
		a, b := out.Results[i], out.Results[j]
		if a.HashMatch != b.HashMatch {
			return a.HashMatch
		}
		if a.FromTrusted != b.FromTrusted {
			return a.FromTrusted
		}
		return a.DownloadCount > b.DownloadCount
	})
	if len(out.Results) == 0 {
		out.Note = "no results for this search — nothing was downloaded or changed"
	}
	return out, nil
}

// OSDownloadRequest is the picker's explicit pick.
type OSDownloadRequest struct {
	Path     string `json:"path"` // the media file to sidecar
	FileID   int    `json:"file_id"`
	Language string `json:"language"` // 639-1 code for the file name
	Release  string `json:"release,omitempty"`
	Forced   bool   `json:"forced"`
	SDH      bool   `json:"sdh"`
}

// OSDownloadResult reports exactly what happened — including
// an honest detector mismatch (the file is KEPT; the user's
// call — spec W5).
type OSDownloadResult struct {
	SavedPath        string `json:"saved_path"`
	DetectedLanguage string `json:"detected_language,omitempty"`
	Verified         bool   `json:"verified"`
	Note             string `json:"note,omitempty"`
	Remaining        int    `json:"remaining"`
	ResetTime        string `json:"reset_time,omitempty"`
	QuotaReported    bool   `json:"quota_reported"`
}

// osDownloadWire is the documented download-link response
// (POST {base}/download → {"link", "file_name", "requests",
// "remaining", "reset_time", ...} — spec §1.1; exact sibling
// fields are in RESULTS' "to verify live" list).
type osDownloadWire struct {
	Link      string `json:"link"`
	FileName  string `json:"file_name"`
	Requests  int    `json:"requests"`
	Remaining int    `json:"remaining"`
	ResetTime string `json:"reset_time"`
}

// subtitleAction is one line of the download log
// (subtitle-actions.json — spec W5: path, release, language,
// outcome).
type subtitleAction struct {
	At       string `json:"at"`
	Media    string `json:"media"`
	Saved    string `json:"saved"`
	FileID   int    `json:"file_id"`
	Release  string `json:"release,omitempty"`
	Language string `json:"language"`
	Detected string `json:"detected,omitempty"`
	Verified bool   `json:"verified"`
	Note     string `json:"note,omitempty"`
}

// DownloadSubtitle runs the full W5 flow for one explicit pick:
// login (cached) → download-link request (quota captured
// verbatim) → fetch → detector verify → place as a NEVER-
// overwriting sidecar → log the action → attach the new file
// to the current results so the wizard's coverage view sees it
// without a rescan.
func (s *Service) DownloadSubtitle(req OSDownloadRequest) (OSDownloadResult, error) {
	sec, err := s.osCredentials()
	if err != nil {
		return OSDownloadResult{}, err
	}
	if req.FileID <= 0 {
		return OSDownloadResult{}, errors.New("no subtitle file selected")
	}
	if err := s.beginFileOp(); err != nil {
		return OSDownloadResult{}, err
	}
	defer s.endFileOp()

	s.mu.Lock()
	item := s.itemLocked(req.Path)
	roots := append([]string{}, s.cfg.Roots...)
	threshold := s.cfg.SubtitleConfidence
	s.mu.Unlock()
	if item == nil {
		return OSDownloadResult{}, fmt.Errorf("no media in the current results at %s", req.Path)
	}
	cleanItem := cleanAbs(item.Path)
	rootOK := false
	for _, r := range roots {
		if pathWithinRoot(r, cleanItem) {
			rootOK = true
			break
		}
	}
	if !rootOK {
		return OSDownloadResult{}, fmt.Errorf("refusing: %s is outside every scan root", item.Path)
	}

	// 1. Login first (the documented download flow).
	sess, err := s.osLogin(sec)
	if err != nil {
		return OSDownloadResult{}, err
	}
	// 2. The download-link request — the quota figures in this
	// response are the ones displayed, verbatim.
	b, _, err := osDo(sec, http.MethodPost, strings.TrimRight(sess.BaseURL, "/")+"/download", sess.Token,
		map[string]any{"file_id": req.FileID})
	if err != nil {
		return OSDownloadResult{}, err
	}
	var dw osDownloadWire
	if err := json.Unmarshal(b, &dw); err != nil {
		return OSDownloadResult{}, fmt.Errorf("OpenSubtitles download response did not parse: %w", err)
	}
	if dw.Link == "" {
		return OSDownloadResult{}, errors.New("OpenSubtitles returned no download link")
	}
	s.mu.Lock()
	if s.osSession != nil {
		s.osSession.Remaining = dw.Remaining
		s.osSession.ResetTime = dw.ResetTime
	}
	s.mu.Unlock()
	res := OSDownloadResult{Remaining: dw.Remaining, ResetTime: dw.ResetTime, QuotaReported: true}

	// 3. Fetch the subtitle bytes (the link is pre-signed; no
	// API headers go to the file host beyond a User-Agent).
	freq, err := http.NewRequest(http.MethodGet, dw.Link, nil)
	if err != nil {
		return OSDownloadResult{}, err
	}
	freq.Header.Set("User-Agent", osUserAgent)
	fresp, err := osHTTPClient.Do(freq)
	if err != nil {
		return OSDownloadResult{}, fmt.Errorf("downloading the subtitle file failed: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(fresp.Body, 8<<20))
	fresp.Body.Close()
	if err != nil {
		return OSDownloadResult{}, fmt.Errorf("reading the subtitle download failed: %w", err)
	}
	if fresp.StatusCode < 200 || fresp.StatusCode > 299 {
		return OSDownloadResult{}, fmt.Errorf("the subtitle download returned HTTP %d", fresp.StatusCode)
	}

	// 4. Verify BEFORE placing: run the app's own detector on
	// the downloaded text (the same parse + DetectLanguage the
	// audit applies to everyone else's files, same 64 KiB cap).
	// A mismatch never rejects the file — it is reported,
	// plainly (spec W5).
	detected := ""
	{
		sample := data
		if len(sample) > 65536 {
			sample = sample[:65536]
		}
		lines, _ := parseSubtitleText(sample)
		det := DetectLanguage(strings.Join(lines, " "))
		if det.Words >= 10 && det.Code != "" {
			detected = det.Code
		}
	}
	lang2 := langCodeNormal(req.Language)
	if lang2 == "" {
		lang2 = lowerASCII(strings.TrimSpace(req.Language))
	}
	verified := detected != "" && langCodeNormal(detected) == langCodeNormal(lang2)
	note := ""
	switch {
	case detected == "":
		note = "the downloaded text was too short or ambiguous for the language detector — kept unverified, review it before trusting it"
	case !verified:
		note = fmt.Sprintf("detector reads this file as %s, not the %s it was listed as — kept anyway; wrong-language downloads happen, the call is yours", detected, lang2)
	default:
		note = fmt.Sprintf("verified: the detector reads the downloaded file as %s", detected)
	}

	// 5. Place it — name.<lang>[.forced|.sdh].srt next to the
	// media, never overwriting: .1, .2, … on collision, the
	// v0.9.0 discipline.
	ext := ".srt"
	if fn := dw.FileName; fn != "" {
		if e := strings.ToLower(filepath.Ext(fn)); e != "" {
			ext = e
		}
	}
	stem := strings.TrimSuffix(item.Name, item.Ext)
	if item.Ext == "" {
		stem = item.Name
	}
	suffix := ""
	if req.Forced {
		suffix = ".forced"
	} else if req.SDH {
		suffix = ".sdh"
	}
	base := filepath.Join(filepath.Dir(item.Path), stem+"."+lang2+suffix+ext)
	finalPath := base
	for n := 1; ; n++ {
		if _, err := os.Stat(finalPath); os.IsNotExist(err) {
			break
		}
		finalPath = fmt.Sprintf("%s.%d%s", strings.TrimSuffix(base, ext), n, ext)
	}
	tmpPath := finalPath + ".pfm-download"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return OSDownloadResult{}, err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return OSDownloadResult{}, err
	}

	// 6. Log the action + attach to the live results.
	action := subtitleAction{
		At: time.Now().UTC().Format(time.RFC3339), Media: item.Path, Saved: finalPath,
		FileID: req.FileID, Release: req.Release, Language: lang2, Detected: detected,
		Verified: verified, Note: note,
	}
	s.mu.Lock()
	s.appendSubtitleActionLocked(action)
	if !containsString(s.results.SubtitleFilePaths, finalPath) {
		s.results.SubtitleFilePaths = append(s.results.SubtitleFilePaths, finalPath)
	}
	// The new sidecar gets the same external audit any scanned
	// file gets, attached to the live results so coverage and
	// the wizard reflect the placement without a rescan. The
	// audit finding's MediaPath points at the media it serves.
	if f, ok := externalFindingFor(finalPath, threshold); ok {
		s.results.Subtitles = replaceFinding(s.results.Subtitles, f)
	}
	s.saveResultsLocked()
	s.mu.Unlock()

	res.SavedPath = finalPath
	res.DetectedLanguage = detected
	res.Verified = verified
	res.Note = note
	return res, nil
}

// containsString is the membership helper for the attach step.
func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// externalFindingFor builds the audit finding for a freshly
// placed external sidecar, via the scanner's own external
// audit (auditExternalSubtitle) so the placed file is judged by
// exactly the rules every other file is. A nil finding there
// means a clean pass — nothing to attach.
func externalFindingFor(path string, threshold float64) (SubtitleFinding, bool) {
	f, _, _, err := auditExternalSubtitle(path, threshold)
	if err != nil || f == nil {
		return SubtitleFinding{}, false
	}
	return *f, true
}

// itemLocked finds a media item by path (cleanAbs comparison,
// the parser's path discipline). Caller holds s.mu.
func (s *Service) itemLocked(path string) *Item {
	want := cleanAbs(path)
	for _, it := range s.results.Items {
		if it != nil && cleanAbs(it.Path) == want {
			return it
		}
	}
	return nil
}

// replaceFinding swaps (or appends) a finding by its identity
// key (path + source + stream index).
func replaceFinding(findings []SubtitleFinding, f SubtitleFinding) []SubtitleFinding {
	key := func(x SubtitleFinding) string {
		return x.Path + "\x00" + x.Source + "\x00" + strconv.Itoa(x.StreamIdx)
	}
	for i, x := range findings {
		if key(x) == key(f) {
			findings[i] = f
			return findings
		}
	}
	return append(findings, f)
}

// appendSubtitleActionLocked appends one line to the download
// log (subtitle-actions.json — a JSON array, rewritten whole;
// the file is small by nature). Caller holds s.mu.
func (s *Service) appendSubtitleActionLocked(a subtitleAction) {
	path := filepath.Join(s.dataDir, "subtitle-actions.json")
	var actions []subtitleAction
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &actions)
	}
	actions = append(actions, a)
	if b, err := json.MarshalIndent(actions, "", "  "); err == nil {
		tmp := path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, path)
		}
	}
}

// SubtitleActions returns the download log (newest content last,
// as stored) for the wizard's history of what IT placed.
func (s *Service) SubtitleActions() []subtitleAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dataDir, "subtitle-actions.json")
	var actions []subtitleAction
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &actions)
	}
	if actions == nil {
		actions = []subtitleAction{}
	}
	return actions
}
