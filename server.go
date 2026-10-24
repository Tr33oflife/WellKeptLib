package main

// server.go — the HTTP transport layer. RULE: handlers
// here are thin wrappers only — decode input, call exactly one
// core.Service method, encode the result. No business logic lives in
// this file; anything that looks like logic belongs in internal/core.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"plexfilemanager/internal/core"
)

// server wraps the core service and the embedded UI.
type server struct {
	svc  *core.Service
	page []byte
	base string // reverse-proxy path prefix, "" or "/something"
	mux  *http.ServeMux
}

// newServer builds the HTTP handler with all routes registered under
// the base prefix.
func newServer(svc *core.Service, page []byte, base string) http.Handler {
	s := &server{svc: svc, page: page, base: base}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	// Favicon / app icons (v1.2.0): browsers ask for /favicon.ico
	// unprompted — before this release the mux had no such route
	// and it 404'd. The set is embedded at build time (see
	// main.go); the mark carries its own background, so these
	// are served exactly as shipped on every theme.
	mux.HandleFunc("GET /favicon.ico", iconHandler(iconICO, "image/x-icon"))
	mux.HandleFunc("GET /favicon.svg", iconHandler(iconSVG, "image/svg+xml"))
	mux.HandleFunc("GET /favicon-32x32.png", iconHandler(iconPNG32, "image/png"))
	mux.HandleFunc("GET /apple-touch-icon.png", iconHandler(iconAppleTouch, "image/png"))
	mux.HandleFunc("GET /android-chrome-192x192.png", iconHandler(iconPNG192, "image/png"))
	mux.HandleFunc("GET /android-chrome-512x512.png", iconHandler(iconPNG512, "image/png"))
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/scan", s.handleScanStart)
	mux.HandleFunc("POST /api/scan/cancel", s.handleScanCancel)
	mux.HandleFunc("GET /api/library", s.handleLibrary)
	mux.HandleFunc("GET /api/series", s.handleSeries)
	mux.HandleFunc("GET /api/subtitles", s.handleSubtitles)
	mux.HandleFunc("GET /api/duplicates", s.handleDuplicates)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	// Health actions — reveal a finding's file in the local file
	// manager, and the guarded per-file delete. Both validate the
	// path against the current findings inside core (see
	// health_actions.go for why the chain is that strict).
	mux.HandleFunc("POST /api/health/reveal", s.handleHealthReveal)
	mux.HandleFunc("POST /api/health/delete", s.handleHealthDelete)
	// Duplicates actions (v0.3.6) — the same reveal/delete pair
	// for duplicate copies; validation lives in core (current
	// group membership + last-copy protection — see
	// duplicates_actions.go).
	mux.HandleFunc("POST /api/duplicates/reveal", s.handleDuplicatesReveal)
	mux.HandleFunc("POST /api/duplicates/delete", s.handleDuplicatesDelete)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("POST /api/config", s.handleConfigSave)
	// Settings pass (v0.5.0) — reset ONLY the tuning knobs to
	// defaults (roots/exclusions/tool paths survive), and the
	// secret-free diagnostics snapshot for support.
	mux.HandleFunc("POST /api/config/reset-tuning", s.handleConfigResetTuning)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("GET /api/exclusions", s.handleExclusionsList)
	mux.HandleFunc("POST /api/exclusions", s.handleExclusionsPost)
	mux.HandleFunc("GET /api/export.json", s.handleExportJSON)
	mux.HandleFunc("GET /api/export.csv", s.handleExportCSV)
	// Plex module (v0.2.0) — settings, test, sections import, missing check.
	mux.HandleFunc("GET /api/plex/settings", s.handlePlexSettingsGet)
	mux.HandleFunc("POST /api/plex/settings", s.handlePlexSettingsSave)
	mux.HandleFunc("POST /api/plex/test", s.handlePlexTest)
	mux.HandleFunc("GET /api/plex/sections", s.handlePlexSections)
	mux.HandleFunc("POST /api/plex/missing", s.handlePlexMissing)
	// Plex sign-in (v0.3.1) — plex.tv PIN flow: begin -> poll -> choose.
	mux.HandleFunc("POST /api/plex/signin/begin", s.handlePlexSignInBegin)
	mux.HandleFunc("GET /api/plex/signin/poll", s.handlePlexSignInPoll)
	mux.HandleFunc("POST /api/plex/signin/choose", s.handlePlexSignInChoose)
	// Plex server switching — change the connected server from the
	// retained/remembered account sign-in, no fresh sign-in.
	mux.HandleFunc("GET /api/plex/servers", s.handlePlexServers)
	mux.HandleFunc("POST /api/plex/switch", s.handlePlexSwitch)
	mux.HandleFunc("POST /api/plex/forget-signin", s.handlePlexForgetSignIn)
	// Organize (v0.3.0) — the app's only file-moving feature:
	// preview a plan, apply it, undo from the on-disk manifest.
	mux.HandleFunc("POST /api/organize/plan", s.handleOrganizePlan)
	mux.HandleFunc("POST /api/organize/apply", s.handleOrganizeApply)
	mux.HandleFunc("POST /api/organize/undo", s.handleOrganizeUndo)
	mux.HandleFunc("GET /api/organize/manifests", s.handleOrganizeManifests)
	// v1.1.0 — the hardlink library mode: parallel endpoints,
	// never overloaded move semantics (LINK-LIBRARY-SPEC §15.1).
	mux.HandleFunc("POST /api/organize/links/plan", s.handleLinkPlan)
	mux.HandleFunc("POST /api/organize/links/apply", s.handleLinkApply)
	mux.HandleFunc("POST /api/organize/links/undo", s.handleLinkUndo)
	// v1.3.5 — link Apply/Undo run as background jobs (a big
	// build outlasted the proxy timeout as one synchronous
	// request): the POSTs above answer 202 + a job handle, and
	// this status endpoint carries progress + the final
	// summary.
	mux.HandleFunc("GET /api/organize/links/job", s.handleLinkJob)
	mux.HandleFunc("GET /api/organize/links/manifests", s.handleLinkManifests)
	// v1.3.0 — the link plan's problems as ONE downloadable
	// JSON document (every conflict, not-identified and
	// unlinkable entry with the planner's evidence).
	mux.HandleFunc("GET /api/organize/links/problems", s.handleLinkProblems)
	// v0.9.0 — gaps, coverage, and the subtitle wizard (W1–W3):
	// the Step 0 overview, the dismissal store, and the external
	// fixes (relabel via manifest + guarded delete). No
	// OpenSubtitles endpoints exist in this release.
	mux.HandleFunc("GET /api/gaps", s.handleGaps)
	mux.HandleFunc("GET /api/coverage", s.handleCoverage)
	mux.HandleFunc("GET /api/subtitles/overview", s.handleSubtitlesOverview)
	mux.HandleFunc("GET /api/subtitles/dismissals", s.handleSubtitlesDismissals)
	mux.HandleFunc("POST /api/subtitles/dismiss", s.handleSubtitlesDismiss)
	mux.HandleFunc("POST /api/subtitles/undismiss", s.handleSubtitlesUndismiss)
	mux.HandleFunc("POST /api/subtitles/delete", s.handleSubtitlesDelete)
	mux.HandleFunc("POST /api/subtitles/relabel/preview", s.handleRelabelPreview)
	mux.HandleFunc("POST /api/subtitles/relabel/apply", s.handleRelabelApply)
	mux.HandleFunc("POST /api/subtitles/relabel/undo", s.handleRelabelUndo)
	mux.HandleFunc("GET /api/subtitles/manifests", s.handleRelabelManifests)
	// v1.0.0 — report card + exports, scan history, and the
	// OpenSubtitles wizard slices (W4 search / W5 download).
	// Automation settings (schedule, webhook) ride the existing
	// GET/POST /api/config channel with the Config fields.
	mux.HandleFunc("GET /api/report", s.handleReport)
	mux.HandleFunc("GET /api/report.csv", s.handleReportCSV)
	mux.HandleFunc("GET /api/report.html", s.handleReportHTML)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/opensubtitles/settings", s.handleOSSettingsGet)
	mux.HandleFunc("POST /api/opensubtitles/settings", s.handleOSSettingsSave)
	mux.HandleFunc("POST /api/opensubtitles/search", s.handleOSSearch)
	mux.HandleFunc("POST /api/opensubtitles/download", s.handleOSDownload)
	mux.HandleFunc("GET /api/opensubtitles/actions", s.handleOSActions)
	s.mux = mux
	return s
}

// ServeHTTP strips the base prefix (Ultra nginx proxy) and delegates.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.base != "" {
		if r.URL.Path == s.base {
			http.Redirect(w, r, s.base+"/", http.StatusFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, s.base+"/") {
			r2 := *r
			u := *r.URL
			u.Path = strings.TrimPrefix(r.URL.Path, s.base)
			r2.URL = &u
			s.mux.ServeHTTP(w, &r2)
			return
		}
		http.NotFound(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

// writeJSON encodes one service result as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr encodes a simple JSON error.
func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// iconHandler serves one embedded icon asset with its content
// type and a day of cache (the mark changes only with releases).
func iconHandler(b []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(b)
	}
}

// handleIndex serves the embedded single-page UI.
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.page)
}

// handleState -> Service.State
func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.State())
}

// handleScanStart -> Service.StartScan
func (s *server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Roots []string `json:"roots"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	st, err := s.svc.StartScan(body.Roots)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleScanCancel -> Service.CancelScan
func (s *server) handleScanCancel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.CancelScan())
}

// handleLibrary -> Service.Library
func (s *server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": s.svc.Library(q.Get("sort"), q.Get("order")),
	})
}

// handleSeries -> Service.Series
func (s *server) handleSeries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"series": s.svc.Series()})
}

// handleSubtitles -> Service.Subtitles
func (s *server) handleSubtitles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"findings": s.svc.Subtitles()})
}

// handleDuplicates -> Service.Duplicates
func (s *server) handleDuplicates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"groups": s.svc.Duplicates()})
}

// handleHealth -> Service.Health
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"findings": s.svc.Health()})
}

// actionPathBody decodes the shared {path} body of the health
// and duplicates action endpoints. ok=false means the error was
// already written.
func actionPathBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return "", false
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return "", false
	}
	return body.Path, true
}

// handleHealthReveal -> Service.RevealHealthPath.
// Body: {path}. The answer is in-band about what happened:
// {mode:"revealed"} when a file-manager open was launched on
// this host, else {mode:"path", path} for the UI's copy box.
// Validation failures (not a current finding) are 400.
func (s *server) handleHealthReveal(w http.ResponseWriter, r *http.Request) {
	path, ok := actionPathBody(w, r)
	if !ok {
		return
	}
	res, err := s.svc.RevealHealthPath(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleHealthDelete -> Service.DeleteHealthFile.
// Body: {path}. Every guard failure (not a current finding,
// outside the scan roots, a folder, a scan running) is a 400
// and nothing is deleted. Success answers {ok, path, summary}.
func (s *server) handleHealthDelete(w http.ResponseWriter, r *http.Request) {
	path, ok := actionPathBody(w, r)
	if !ok {
		return
	}
	res, err := s.svc.DeleteHealthFile(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDuplicatesReveal -> Service.RevealDuplicatePath.
// Body: {path}. Same in-band answer as the health reveal:
// {mode:"revealed"} when a file-manager open was launched on
// this host, else {mode:"path", path}. Validation failures
// (not a copy in a current duplicate group) are 400.
func (s *server) handleDuplicatesReveal(w http.ResponseWriter, r *http.Request) {
	path, ok := actionPathBody(w, r)
	if !ok {
		return
	}
	res, err := s.svc.RevealDuplicatePath(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleDuplicatesDelete -> Service.DeleteDuplicateCopy.
// Body: {path}. Every guard failure (not a current group
// copy, the group's last remaining copy, outside the scan
// roots, a folder, a scan running) is a 400 and nothing is
// deleted. Success answers {ok, path, summary}.
func (s *server) handleDuplicatesDelete(w http.ResponseWriter, r *http.Request) {
	path, ok := actionPathBody(w, r)
	if !ok {
		return
	}
	res, err := s.svc.DeleteDuplicateCopy(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleConfigGet -> Service.GetConfig
func (s *server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.GetConfig())
}

// handleConfigSave -> Service.SaveConfig
func (s *server) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var cfg core.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// v1.3.2: Ko-fi presence is a wire-format fact, not a
	// value — the current page always sends both keys (blank
	// username = deliberately disabled, enabled=false =
	// deliberately off), but a stale pre-1.3.2 tab has
	// neither field, and its decoded "" / false must not
	// wipe the stored handle or switch the button off. An
	// absent key therefore keeps the current value; a
	// present key (even blank / false) is used exactly as
	// sent.
	var probe struct {
		KofiUsername *string `json:"kofi_username"`
		KofiEnabled  *bool   `json:"kofi_enabled"`
		// v1.4.0: the three link-library switches ride the
		// same stale-page protection — a pre-1.4.0 tab's save
		// carries none of the keys, and its decoded falses
		// must not silently switch the new behaviors off.
		LinkExtras           *bool `json:"link_extras"`
		AssignSpecialNumbers *bool `json:"assign_special_numbers"`
		AllowYearlessMovies  *bool `json:"allow_yearless_movies"`
	}
	if err := json.Unmarshal(body, &probe); err == nil {
		current := s.svc.GetConfig()
		if probe.KofiUsername == nil {
			cfg.KofiUsername = current.KofiUsername
		}
		if probe.KofiEnabled == nil {
			cfg.KofiEnabled = current.KofiEnabled
		}
		if probe.LinkExtras == nil {
			cfg.LinkExtras = current.LinkExtras
		}
		if probe.AssignSpecialNumbers == nil {
			cfg.AssignSpecialNumbers = current.AssignSpecialNumbers
		}
		if probe.AllowYearlessMovies == nil {
			cfg.AllowYearlessMovies = current.AllowYearlessMovies
		}
	}
	if err := s.svc.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.svc.GetConfig())
}

// handleConfigResetTuning -> Service.ResetTuning: resets only the
// tuning knobs to defaults, persists, and answers the full config
// (same shape as a config save).
func (s *server) handleConfigResetTuning(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ResetTuning()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// handleDiagnostics -> Service.Diagnostics: the support snapshot.
// Contains NO secrets by construction (see core/diagnostics.go).
func (s *server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Diagnostics())
}

// handleExclusionsList -> Service.ListExclusions
func (s *server) handleExclusionsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"exclusions": s.svc.ListExclusions()})
}

// handleExclusionsPost -> Service.AddExclusion / RemoveExclusion.
// Body: {path, scope, action: "add"|"remove"} (action defaults to add;
// remove:true is also accepted for convenience).
func (s *server) handleExclusionsPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		core.Exclusion
		Action string `json:"action"`
		Remove bool   `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var list []core.Exclusion
	var err error
	if body.Action == "remove" || body.Remove {
		list, err = s.svc.RemoveExclusion(body.Exclusion)
	} else {
		list, err = s.svc.AddExclusion(body.Exclusion)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exclusions": list})
}

// handlePlexSettingsGet -> Service.GetPlexSettings (never the token)
func (s *server) handlePlexSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.GetPlexSettings())
}

// handlePlexSettingsSave -> Service.SavePlexSettings.
// Body: {url, token, mappings:[{from,to}]} — empty token keeps the
// stored one; "__clear__" clears the Plex settings (see core docs).
func (s *server) handlePlexSettingsSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL      string             `json:"url"`
		Token    string             `json:"token"`
		Mappings []core.PathMapping `json:"mappings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.svc.SavePlexSettings(body.URL, body.Token, body.Mappings)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handlePlexTest -> Service.TestPlexWith (result is in-band:
// ok/error). The body is OPTIONAL: {url, token} carries the values
// currently typed in the Settings form so Test checks what's typed,
// not only what was saved (v0.3.1); an empty/absent body tests the
// saved settings.
func (s *server) handlePlexTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // an absent body is fine
	}
	writeJSON(w, http.StatusOK, s.svc.TestPlexWith(body.URL, body.Token))
}

// handlePlexSignInBegin -> Service.BeginSignIn: starts a plex.tv
// PIN sign-in and returns {session, auth_url} — the approval link
// for Plex's hosted auth page. The PIN code never leaves the
// server (it exists only inside that URL).
func (s *server) handlePlexSignInBegin(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.BeginSignIn()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePlexSignInPoll -> Service.PollSignInSession?session=...
// (status is in-band: pending | ready | expired | error).
func (s *server) handlePlexSignInPoll(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.PollSignInSession(r.URL.Query().Get("session")))
}

// handlePlexSignInChoose -> Service.ChooseSignInServerRemember.
// Body: {session, index, remember}. remember is the picker's
// "remember my sign-in" opt-in (absent = false). The result has
// TestPlex's in-band shape.
func (s *server) handlePlexSignInChoose(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session  string `json:"session"`
		Index    int    `json:"index"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.svc.ChooseSignInServerRemember(body.Session, body.Index, body.Remember))
}

// handlePlexServers -> Service.PlexServers: the account's servers
// for the switcher, fetched fresh from the retained/remembered
// sign-in. Availability is in-band ({available, remembered,
// servers|error}); no token ever appears in the answer.
func (s *server) handlePlexServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.PlexServers())
}

// handlePlexSwitch -> Service.SwitchPlexServer.
// Body: {index, remember?} — remember is optional and tri-state
// (absent leaves the persisted sign-in untouched). The result
// has TestPlex's in-band shape.
func (s *server) handlePlexSwitch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index    int   `json:"index"`
		Remember *bool `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.svc.SwitchPlexServer(body.Index, body.Remember))
}

// handlePlexForgetSignIn -> Service.ForgetSignIn: clears ONLY the
// saved/retained account sign-in (the connected server stays
// connected) and answers the settings view.
func (s *server) handlePlexForgetSignIn(w http.ResponseWriter, r *http.Request) {
	view, err := s.svc.ForgetSignIn()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handlePlexSections -> Service.PlexSections
func (s *server) handlePlexSections(w http.ResponseWriter, r *http.Request) {
	sections, err := s.svc.PlexSections()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sections": sections})
}

// handlePlexMissing -> Service.PlexMissingOnDisk
func (s *server) handlePlexMissing(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.PlexMissingOnDisk()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleOrganizePlan -> Service.OrganizePlan.
// Body: {root}. Returns the full dry-run plan (nothing has moved).
func (s *server) handleOrganizePlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	plan, err := s.svc.OrganizePlan(body.Root)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleOrganizeApply -> Service.OrganizeApply.
// Body: {plan_id}. Re-validation and mid-apply failures come back
// in-band in the result (applied count + failed list); only hard
// errors (unknown plan, scan running) are HTTP errors.
func (s *server) handleOrganizeApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.OrganizeApply(body.PlanID)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleOrganizeUndo -> Service.OrganizeUndo.
// Body: {manifest_id}. Per-file restore failures come back in-band.
func (s *server) handleOrganizeUndo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ManifestID string `json:"manifest_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.OrganizeUndo(body.ManifestID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleLinkPlan -> Service.LinkPlan (v1.1.0).
// Body: {source_root, target_root}. Returns the full dry-run
// link plan (nothing has been created).
func (s *server) handleLinkPlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceRoot string `json:"source_root"`
		TargetRoot string `json:"target_root"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	plan, err := s.svc.LinkPlan(body.SourceRoot, body.TargetRoot)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleLinkApply -> Service.StartLinkApplyJob (v1.1.0;
// v1.3.5: a background job). Body: {plan_id}. The apply runs
// server-side and the answer is 202 + the job handle — poll
// GET /api/organize/links/job for progress and the final
// summary (the LinkApplyResult the synchronous endpoint used
// to return, attached to the terminal job status). Per-item
// skips and failures stay in-band in that result; hard start
// errors (unknown plan, scan running, another file operation)
// stay HTTP errors with the synchronous endpoint's statuses.
func (s *server) handleLinkApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.svc.StartLinkApplyJob(body.PlanID)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// handleLinkUndo -> Service.StartLinkUndoJob (v1.1.0;
// v1.3.5: a background job, same shape as apply).
// Body: {manifest_id}. Per-entry skips come back in-band in
// the finished job's undo_result.
func (s *server) handleLinkUndo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ManifestID string `json:"manifest_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.svc.StartLinkUndoJob(body.ManifestID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// handleLinkJob -> Service.LinkJob (v1.3.5): the latest link
// job's status — live progress while running, the final
// summary when done or the job-level error when failed.
// ?id= pins one job; an id that matches no recorded job is
// a 404. After a server restart there is no record at all
// and the answer is state "none" — the build's manifest is
// the durable record, and a fresh preview shows what
// actually landed as already-linked.
func (s *server) handleLinkJob(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.LinkJob(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleLinkProblems -> Service.LinkPlanDiagnostics (v1.3.0):
// the current link plan's problems as a JSON download. Plans
// live only in memory, so a stale or unknown plan id is a 404
// with the honest error — never a downloaded error page.
func (s *server) handleLinkProblems(w http.ResponseWriter, r *http.Request) {
	doc, filename, err := s.svc.LinkPlanDiagnostics(r.URL.Query().Get("plan_id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	_, _ = w.Write(append(b, '\n'))
}

// handleLinkManifests -> Service.LinkManifests (v1.1.0).
func (s *server) handleLinkManifests(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.LinkManifests()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifests": list})
}

// handleOrganizeManifests -> Service.OrganizeManifests
func (s *server) handleOrganizeManifests(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.OrganizeManifests()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifests": list})
}

// handleGaps -> Service.EpisodeGaps (v0.9.0): missing-episode
// rows derived from the current results. Read-only.
func (s *server) handleGaps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"gaps": s.svc.EpisodeGaps()})
}

// handleCoverage -> Service.Coverage (v0.9.0): wanted-language
// coverage counts + the not-covered attention list.
func (s *server) handleCoverage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Coverage())
}

// handleSubtitlesOverview -> Service.SubtitleOverview (v0.9.0):
// the wizard's Step 0 counts — per problem class over the
// current findings, plus coverage/gap counts and audit state.
func (s *server) handleSubtitlesOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.SubtitleOverview())
}

// handleSubtitlesDismissals -> Service.Dismissals (v0.9.0): the
// verified/intentional marks, for the Settings manager.
func (s *server) handleSubtitlesDismissals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"dismissals": s.svc.Dismissals()})
}

// handleSubtitlesDismiss -> Service.DismissFinding /
// DismissCoverage (v0.9.0). Body: {path, source, stream_index,
// note}. source "coverage" marks a coverage item "not wanted";
// anything else must name a current subtitle finding exactly.
func (s *server) handleSubtitlesDismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Source    string `json:"source"`
		StreamIdx int    `json:"stream_index"`
		Note      string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var d core.SubtitleDismissal
	var err error
	if body.Source == "coverage" {
		d, err = s.svc.DismissCoverage(body.Path, body.Note)
	} else {
		d, err = s.svc.DismissFinding(body.Path, body.Source, body.StreamIdx, body.Note)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissal": d})
}

// handleSubtitlesUndismiss -> Service.Undismiss (v0.9.0).
// Body: {path, source, stream_index, class} — the full mark key,
// exactly as the dismissals list reports it.
func (s *server) handleSubtitlesUndismiss(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Source    string `json:"source"`
		StreamIdx int    `json:"stream_index"`
		Class     string `json:"class"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.svc.Undismiss(body.Path, body.Source, body.StreamIdx, body.Class); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSubtitlesDelete -> Service.DeleteSubtitleFinding
// (v0.9.0). Body: {path}. Guard failures (not a current external
// finding, embedded path, outside the scan roots, a scan
// running) are a 400 and nothing is deleted.
func (s *server) handleSubtitlesDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.DeleteSubtitleFinding(body.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRelabelPreview -> Service.RelabelPreview (v0.9.0).
// Body: {paths}. Plans only — nothing moves.
func (s *server) handleRelabelPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.svc.RelabelPreview(body.Paths)})
}

// handleRelabelApply -> Service.RelabelApply (v0.9.0).
// Body: {paths}. Plans are recomputed server-side; per-file
// skips come back in-band (applied + failed); only guard errors
// (scan running, another file operation, manifest unwritable)
// are HTTP errors — 409, like Organize apply.
func (s *server) handleRelabelApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.RelabelApply(body.Paths)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRelabelUndo -> Service.RelabelUndo (v0.9.0).
// Body: {manifest_id}. Per-file restore failures come back
// in-band; hard errors (unknown manifest, already undone,
// guards) are 400, like Organize undo.
func (s *server) handleRelabelUndo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ManifestID string `json:"manifest_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.RelabelUndo(body.ManifestID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRelabelManifests -> Service.SubtitleManifests (v0.9.0)
func (s *server) handleRelabelManifests(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.SubtitleManifests()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manifests": list})
}

// handleExportJSON -> Service.ExportInventory
func (s *server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.ExportInventory())
}

// handleExportCSV -> Service.ExportCSV
func (s *server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.ExportCSV()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=library.csv")
	_, _ = w.Write(b)
}

// --- v1.0.0 handlers: report, history, OpenSubtitles. Thin by
// the standing rule: exactly one Service call each. ---

// handleReport -> Service.Report: the report card JSON.
func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Report())
}

// handleReportCSV -> Service.ReportCSV: the sectioned CSV
// export as a download.
func (s *server) handleReportCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.ReportCSV()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=wellkeptlib-report.csv")
	_, _ = w.Write(b)
}

// handleReportHTML -> Service.ReportHTML: the standalone,
// self-contained HTML report as a download.
func (s *server) handleReportHTML(w http.ResponseWriter, r *http.Request) {
	b, err := s.svc.ReportHTML()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=wellkeptlib-report.html")
	_, _ = w.Write(b)
}

// handleHistory -> Service.History: scan records newest-first,
// each with its delta against the previous record.
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"history": s.svc.History()})
}

// handleOSSettingsGet -> Service.OSSecretsState: presence only
// (enabled / configured / has_login) — never the values.
func (s *server) handleOSSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.OSSecretsState())
}

// handleOSSettingsSave -> Service.SaveOSSecrets.
func (s *server) handleOSSettingsSave(w http.ResponseWriter, r *http.Request) {
	var in core.OSSecretsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.svc.SaveOSSecrets(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleOSSearch -> Service.SearchSubtitles (wizard W4).
func (s *server) handleOSSearch(w http.ResponseWriter, r *http.Request) {
	var in core.OSSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp, err := s.svc.SearchSubtitles(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleOSDownload -> Service.DownloadSubtitle (wizard W5).
func (s *server) handleOSDownload(w http.ResponseWriter, r *http.Request) {
	var in core.OSDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.svc.DownloadSubtitle(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleOSActions -> Service.SubtitleActions: what the wizard
// itself downloaded, for the picker's history strip.
func (s *server) handleOSActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"actions": s.svc.SubtitleActions()})
}
