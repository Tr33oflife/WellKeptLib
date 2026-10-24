package core

// service.go — the Service type: the single transport-agnostic API of
// the app core.
//
// TRANSPORT SEPARATION: every operation the HTTP API exposes
// is a method on Service, mirroring the endpoints 1:1. The HTTP layer
// (package main, server.go) is a thin wrapper: decode -> call one
// Service method -> encode. A future desktop shell could bind these same
// methods directly and skip HTTP entirely. Nothing in this package
// imports net/http, and no method takes HTTP types.
//
// State and scan progress live here (guarded by s.mu), reachable only
// through State() — never through globals in the HTTP layer.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Version is the app version string served by State / export.
const Version = "1.3.0"

// StateInfo is what GET /api/state returns: scan state plus the tool
// discovery facts the UI header needs.
type StateInfo struct {
	Version        string    `json:"version"`
	Scan           ScanState `json:"scan"`
	Roots          []string  `json:"roots"`
	FFprobeFound   string    `json:"ffprobe_found,omitempty"`
	FFmpegFound    string    `json:"ffmpeg_found,omitempty"`
	ExclusionCount int       `json:"exclusion_count"`
	// PlexConfigured / PlexURL (v0.2.0): enough for the UI to enable
	// the import + cross-check buttons and label them. The token is
	// NEVER part of state — see plex.go's token-secrecy rule.
	PlexConfigured bool   `json:"plex_configured"`
	PlexURL        string `json:"plex_url,omitempty"`
	// ConfigWarning (v0.5.0): set when config.json existed at
	// startup but could not be parsed — the unreadable file was
	// preserved as config.json.corrupt and defaults are in use.
	// Empty when the config loaded fine. Shown in Settings.
	ConfigWarning string `json:"config_warning,omitempty"`
	// EmbeddedAudit (v0.8.0): the CURRENT Settings value of the
	// embedded-subtitle audit switch, mirrored here so the UI can
	// show the Subtitles tab's "checking is off" notice from the
	// same poll that carries the scan state — the scan's own
	// outcome marker is ScanState.AuditSkipped.
	EmbeddedAudit bool `json:"embedded_audit"`
	// Schedule (v1.0.0): the in-process scheduler's public face —
	// whether it is enabled, when it will next fire (RFC3339,
	// empty when disabled or unscheduled), and the last thing it
	// did or skipped (plain language, e.g. "skipped 14:00 —
	// a scan was already running"). The settings themselves live
	// in Config; this is the live status.
	ScheduleEnabled  bool   `json:"schedule_enabled"`
	ScheduleNextRun  string `json:"schedule_next_run,omitempty"`
	ScheduleLastNote string `json:"schedule_last_note,omitempty"`
	// PlexServerName (v0.3.1): the connected server's display name
	// from the saved settings — "Connected ✓ — <name>" with no live
	// call. Empty until a sign-in or a successful test learns it.
	PlexServerName string `json:"plex_server_name,omitempty"`
}

// ExportData is the full-inventory export (/api/export.json) and the
// shape: parsed identity per file. It is a scanner by-product,
type ExportData struct {
	Version    string            `json:"version"`
	ExportedAt string            `json:"exported_at"`
	Summary    ScanSummary       `json:"summary"`
	Items      []*Item           `json:"items"`
	Series     []SeriesAgg       `json:"series"`
	Subtitles  []SubtitleFinding `json:"subtitles"`
	Duplicates []DupGroup        `json:"duplicates"`
	Health     []HealthFinding   `json:"health"`
}

// Service is the app core. Construct with NewService.
type Service struct {
	dataDir string
	mu      sync.Mutex
	cfg     *Config
	// configWarning is the corrupt-config warning captured at
	// startup (v0.5.0, see loadConfig). Set once in NewService and
	// never mutated afterwards, so State()/Diagnostics can read it
	// under mu without further synchronization concerns.
	configWarning string
	plex          *plexSettings // optional Plex module settings (plex.json)
	store         *Store
	results       *ScanResults
	state         ScanState
	cancel        context.CancelFunc
	// fileOpBusy is the file-operation guard (v0.4.1): true while an
	// Organize Apply or Undo is executing. Apply/Undo previously
	// checked state.Running only at entry and set no flag, so a scan
	// could START mid-apply and two applies could interleave on the
	// same manifest/tmp paths. The guard is taken via beginFileOp /
	// endFileOp (organize_apply.go); StartScan refuses while held.
	// Guarded by mu like everything else.
	fileOpBusy bool
	// lastStoreSave is when the probe store was last persisted by a
	// THROTTLED incremental save (v0.4.1, see saveStoreThrottledLocked).
	// The unconditional saves (scan finish, result assembly) do not
	// read or update it. Guarded by mu.
	lastStoreSave time.Time
	// orgPlans holds Organize plans computed this session, keyed by
	// plan ID (v0.3.0, see organize_apply.go). Plans are deliberately
	// in-memory only: a restart means "preview again". The undo
	// manifests, by contrast, live on disk and survive restarts.
	orgPlans map[string]*OrganizePlan
	// linkPlans holds link-mode plans computed this session,
	// keyed by plan ID (v1.1.0, see linkapply.go) — a SEPARATE
	// store from orgPlans so a move plan can never be submitted
	// to a link endpoint or the reverse. In-memory only, like
	// move plans; link manifests live on disk.
	linkPlans map[string]*LinkPlan
	// signins holds in-flight plex.tv sign-in sessions (v0.3.1, see
	// plex_auth.go), keyed by an opaque session ID. Memory only by
	// design: a session carries the user's ACCOUNT token between
	// approval and server choice, and that token must never be
	// persisted anywhere.
	signins map[string]*signinSession
	// retained is the COMPLETED plex.tv sign-in (server switching,
	// see plex_auth.go): once an approval succeeds, the account
	// token is kept here for the life of the process so the user
	// can switch the connected server without signing in again.
	// Memory only UNLESS the user opted in to "remember my
	// sign-in", in which case the same token is also persisted in
	// plex.json (plexSettings.AccountToken) and rehydrated into
	// this field at startup. Guarded by mu like everything else.
	retained *retainedSignIn
	// dismissals (v0.9.0, subtitle_dismissals.go): the user's
	// "verified / intentional" marks for subtitle findings and
	// coverage items, loaded from subtitle-dismissals.json at
	// startup and saved on every change. Guarded by mu.
	dismissals []SubtitleDismissal
	// scanTrigger (v1.0.0): who asked for the scan that is
	// about to run — "manual" | "scheduled" | "cli" — set by the
	// start path under mu and consumed by runScan for the
	// history record. Guarded by mu.
	scanTrigger string
	// history (v1.0.0, history.go): the compact per-scan
	// records, loaded from scan-history.json at startup and
	// rewritten on every completed scan. Guarded by mu.
	history []ScanHistoryRecord
	// Scheduler bookkeeping (v1.0.0, scheduler.go): the last
	// scheduler action in plain language, and whether the
	// startup catch-up check has run. Guarded by mu.
	schedNote     string
	schedNoteAt   time.Time
	schedulerLive bool
	// Webhook bookkeeping (v1.0.0, webhook.go): the last
	// delivery attempt's outcome, surfaced in diagnostics.
	// Guarded by mu.
	webhookLast webhookStatus
	// OpenSubtitles (v1.0.0, opensubtitles.go): the user's own
	// credentials (opensubtitles.json, 0600, never returned by
	// any API) and the in-memory login session (JWT + the
	// user-specific base URL + the quota the API last reported).
	// Guarded by mu; network calls never hold it.
	osSecrets *osSecretsFile
	osSession *osSession
}

// NewService loads config, the probe cache, and the last scan results
// from the data dir (creating it when needed).
func NewService(dataDir string) (*Service, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	cfg, cfgWarning := loadConfig(dataDir)
	s := &Service{dataDir: dataDir, cfg: cfg, configWarning: cfgWarning, signins: map[string]*signinSession{}}
	// v0.2.0: load the optional Plex module's settings (plex.json).
	// The file tools never depend on this — an unconfigured Plex
	// simply leaves every /api/plex/* feature disabled in the UI.
	s.plex = loadPlexSettings(dataDir)
	// Server switching: a REMEMBERED account token (persisted only
	// on the user's explicit opt-in — see plex_auth.go) rehydrates
	// the retained sign-in, so switching servers works across
	// restarts exactly as it does within one run.
	if s.plex.AccountToken != "" {
		s.retained = &retainedSignIn{accountToken: s.plex.AccountToken}
	}
	// v0.1.1: if ffprobe/ffmpeg were not found on PATH and were not
	// set in Settings, look next to the executable (and in a bin\
	// subfolder beside it) — on Windows the natural setup is dropping
	// ffprobe.exe / ffmpeg.exe into the app folder, no PATH editing.
	fillToolPathsBesideExe(s.cfg)
	s.store = &Store{Cache: map[string]CacheEntry{}}
	if b, err := os.ReadFile(filepath.Join(dataDir, "cache.json")); err == nil {
		_ = json.Unmarshal(b, s.store)
		if s.store.Cache == nil {
			s.store.Cache = map[string]CacheEntry{}
		}
	}
	s.results = &ScanResults{}
	if b, err := os.ReadFile(filepath.Join(dataDir, "scan.json")); err == nil {
		_ = json.Unmarshal(b, s.results)
	}
	// v0.9.0: load the subtitle dismissal store (best-effort — a
	// missing or unreadable file means no marks, never a startup
	// failure).
	s.dismissals = loadSubtitleDismissals(dataDir)
	// v1.0.0: scan history + the OpenSubtitles secrets file,
	// both best-effort loads (missing = empty/unconfigured).
	s.history = loadScanHistory(dataDir)
	s.osSecrets = loadOSSecrets(dataDir)
	if s.results.Items == nil {
		s.results.Items = []*Item{}
	}
	s.state.Summary = s.results.Summary
	s.state.ScannedAt = s.results.ScannedAt
	return s, nil
}

// State returns the current scan state (poll target for the UI) plus
// tool-discovery facts.
func (s *Service) State() StateInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := StateInfo{
		Version:        Version,
		Scan:           s.state,
		Roots:          append([]string{}, s.cfg.Roots...),
		FFprobeFound:   s.cfg.FFprobePath,
		FFmpegFound:    s.cfg.FFmpegPath,
		ExclusionCount: len(s.cfg.Exclusions),
		PlexConfigured: s.plex.configured(),
		PlexURL:        s.plex.URL,
		PlexServerName: s.plex.ServerName,
		ConfigWarning:  s.configWarning,
		EmbeddedAudit:  s.cfg.EmbeddedAudit,
	}
	// v1.0.0: scheduler status (computed fresh — the next run
	// depends on the clock, not on stored state).
	info.ScheduleEnabled = s.cfg.ScheduleEnabled
	info.ScheduleLastNote = s.schedNote
	if next, ok := s.scheduleNextRunLocked(time.Now()); ok {
		info.ScheduleNextRun = next.UTC().Format(time.RFC3339)
	}
	info.Scan.Summary = s.currentSummaryLocked()
	return info
}

// currentSummaryLocked recomputes headline counts from current results
// and current exclusions (exclusions filter dynamically, so counts
// must not be frozen at scan time). Caller holds s.mu.
func (s *Service) currentSummaryLocked() ScanSummary {
	sum := s.results.Summary
	sum.SubtitleFindings = len(s.filteredSubtitlesLocked())
	sum.HealthFindings = len(s.filteredHealthLocked())
	groups, stats := buildDuplicates(s.results.Items, s.cfg, s.cfg.Exclusions)
	sum.DuplicateGroups = len(groups)
	sum.DuplicateWasted = 0
	sum.DuplicateReclaimable = 0
	for _, g := range groups {
		sum.DuplicateWasted += g.WastedBytes
		sum.DuplicateReclaimable += g.ReclaimableBytes
	}
	sum.ExcludedDupCopies = stats.ExcludedCopies
	sum.ExclusionCount = len(s.cfg.Exclusions)
	// v0.9.0 derivations ride the same recompute, so the header
	// chips, the persisted summary, and diagnostics never disagree:
	// episode gaps from the parsed identities (gaps.go) and the
	// wanted-language coverage classes (coverage.go, dismissed
	// items excluded there).
	gaps := buildEpisodeGaps(s.results.Items)
	sum.GapCount = len(gaps)
	sum.GapEpisodes = 0
	for _, g := range gaps {
		sum.GapEpisodes += g.MissingCount
	}
	_, covCounts, _ := s.coverageAllLocked()
	sum.CoverageCovered = covCounts[coverageCovered]
	sum.CoverageNoWanted = covCounts[coverageNoWanted]
	sum.CoverageNoSubtitles = covCounts[coverageNone]
	sum.CoverageUnverifiable = covCounts[coverageUnverifiable]
	return sum
}

// StartScan begins an asynchronous scan of the given roots (or the
// configured roots when empty). It errors when a scan is running.
//
// v0.1.1: roots are cleaned and validated up front (see roots.go).
// Every rejected root is named — in the returned error when NO root
// is usable, or in ScanState.RootProblems when the scan proceeds
// with the usable subset. A bad root is never silently scanned as
// an empty library again.
func (s *Service) StartScan(roots []string) (ScanState, error) {
	return s.startScan(roots, "manual")
}

// StartScanTriggered is StartScan with the trigger recorded for
// the scan-history record (v1.0.0): "scheduled" from the
// in-process scheduler, "cli" from the one-shot CLI mode. The
// scan itself is identical — same roots validation, same
// guards, same pipeline.
func (s *Service) StartScanTriggered(roots []string, trigger string) (ScanState, error) {
	return s.startScan(roots, trigger)
}

func (s *Service) startScan(roots []string, trigger string) (ScanState, error) {
	s.mu.Lock()
	if s.state.Running {
		st := s.state
		s.mu.Unlock()
		return st, fmt.Errorf("a scan is already running")
	}
	// v0.4.1: no scan may start while an Organize apply/undo holds
	// the file-operation guard — the scan's results would go stale
	// mid-move and the SPEC rule is no scan/action overlap, both ways.
	if s.fileOpBusy {
		st := s.state
		s.mu.Unlock()
		return st, fmt.Errorf("an organize operation is in progress — wait for it to finish")
	}
	candidates := roots
	if len(candidates) == 0 {
		candidates = s.cfg.Roots
	}
	valid, problems := validateRoots(candidates)
	if len(valid) == 0 {
		s.mu.Unlock()
		if len(problems) > 0 {
			return s.state, fmt.Errorf("no usable scan roots — %s", strings.Join(problems, "; "))
		}
		return s.state, fmt.Errorf("no scan roots configured")
	}
	s.cfg.Roots = valid
	_ = s.saveConfigLocked()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.scanTrigger = trigger
	s.state = ScanState{Running: true, Phase: "walking", StartedAt: time.Now().UTC().Format(time.RFC3339), RootProblems: problems}
	st := s.state
	s.mu.Unlock()
	go s.runScan(ctx, valid)
	return st, nil
}

// CancelScan cancels the running scan, if any.
func (s *Service) CancelScan() ScanState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	return s.state
}

// Library returns inventory items with exclusion marks applied and
// sorting applied (sortBy: size|title|year|duration; order: asc|desc).
func (s *Service) Library(sortBy, order string) []*Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Item, 0, len(s.results.Items))
	for _, it := range s.results.Items {
		cp := *it
		if scopes := exclusionScopes(s.cfg.Exclusions, it.Path); len(scopes) > 0 {
			cp.Excluded = true
			cp.ExclusionScopes = scopes
		}
		out = append(out, &cp)
	}
	key := func(it *Item) (f float64, str string) {
		switch sortBy {
		case "title":
			return 0, it.NormTitle
		case "year":
			return float64(it.Year), ""
		case "duration":
			return it.Duration, ""
		default: // size
			return float64(it.Size), ""
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, si := key(out[i])
		fj, sj := key(out[j])
		less := fi < fj
		if si != "" || sj != "" {
			less = si < sj
		}
		if order == "asc" {
			return less
		}
		return !less && (fi != fj || si != sj)
	})
	return out
}

// Series returns the per-series size aggregation.
func (s *Service) Series() []SeriesAgg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return buildSeries(s.results.Items)
}

// filteredSubtitlesLocked drops findings excluded for "subtitles".
// Caller holds s.mu.
func (s *Service) filteredSubtitlesLocked() []SubtitleFinding {
	out := []SubtitleFinding{}
	for _, f := range s.results.Subtitles {
		// v0.8.0: while the embedded audit is OFF in Settings,
		// embedded findings are not published at all — not even
		// ones an earlier audit-on scan left in the results — so
		// the Subtitles view (and the summary count and the
		// export, which share this filter) honestly shows
		// external findings only, never stale embedded results.
		if !s.cfg.EmbeddedAudit && f.Source == "embedded" {
			continue
		}
		if isExcluded(s.cfg.Exclusions, f.Path, "subtitles") {
			continue
		}
		// v0.9.0: a finding the user marked verified/intentional
		// (subtitle_dismissals.go) is suppressed exactly like an
		// excluded one — from this list, and therefore from the
		// summary count, the wizard counts, and the export, which
		// all share this filter. The dismissal only holds while
		// the file's fingerprint (size + mtime) still matches the
		// mark; a changed file resurfaces its finding.
		if s.findingDismissedLocked(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Subtitles returns subtitle-audit findings, exclusions applied.
func (s *Service) Subtitles() []SubtitleFinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filteredSubtitlesLocked()
}

// filteredHealthLocked drops findings excluded for "health".
// Caller holds s.mu.
func (s *Service) filteredHealthLocked() []HealthFinding {
	out := []HealthFinding{}
	for _, f := range s.results.Health {
		if isExcluded(s.cfg.Exclusions, f.Path, "health") {
			continue
		}
		out = append(out, f)
	}
	return out
}

// Health returns health findings, exclusions applied.
func (s *Service) Health() []HealthFinding {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filteredHealthLocked()
}

// Duplicates returns duplicate groups with exclusions applied: an
// excluded copy is removed first, and a group left with <2 copies
// disappears.
func (s *Service) Duplicates() []DupGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups, _ := buildDuplicates(s.results.Items, s.cfg, s.cfg.Exclusions)
	if groups == nil {
		groups = []DupGroup{}
	}
	return groups
}

// GetConfig returns a copy of the current config.
func (s *Service) GetConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.cfg
}

// SaveConfig replaces the editable config fields and persists them.
// Exclusions are preserved when the incoming list is nil (they are
// managed through their own methods).
func (s *Service) SaveConfig(in Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Exclusions == nil {
		in.Exclusions = s.cfg.Exclusions
	}
	// WantedLanguages (v0.9.0) follows the Exclusions pattern: a
	// nil list means "not part of this save" and the current list
	// is kept; a submitted list is normalized, and an empty result
	// falls back to the default rather than wanting nothing.
	if in.WantedLanguages == nil {
		in.WantedLanguages = s.cfg.WantedLanguages
	} else {
		in.WantedLanguages = normalizeWantedLanguages(in.WantedLanguages)
		if len(in.WantedLanguages) == 0 {
			in.WantedLanguages = []string{"eng"}
		}
	}
	if in.Workers < 1 {
		in.Workers = 4 // v0.3.1: matches the new default (was 2)
	}
	if in.Workers > 16 {
		in.Workers = 16
	}
	if in.SubtitleConfidence <= 0 || in.SubtitleConfidence > 1 {
		in.SubtitleConfidence = 0.75
	}
	if in.TinyFileFloorBytes < 0 {
		in.TinyFileFloorBytes = 0
	}
	// EmbeddedAudit (v0.8.0) is a plain bool: no clamp applies and
	// the saved value is taken exactly as submitted — the Settings
	// form always sends it, and other saves start from the current
	// config (Object.assign over GetConfig's copy in the page).
	// v0.5.0 tuning fields: clamped the same way Workers is above
	// (non-positive = unset -> default; out-of-range -> bound).
	in.ProbeTimeoutSec = clampTuningInt(in.ProbeTimeoutSec, 5, 600, defaultProbeTimeoutSec)
	in.SubtitleSampleSec = clampTuningInt(in.SubtitleSampleSec, 10, 900, defaultSubtitleSampleSec)
	in.SubtitleExtractTimeoutSec = clampTuningInt(in.SubtitleExtractTimeoutSec, 5, 600, defaultSubtitleExtractTimeoutSec)
	in.SubtitleMaxTracks = clampTuningInt(in.SubtitleMaxTracks, 1, 20, defaultSubtitleMaxTracks)
	// v1.0.0 automation preferences (NOT tuning — ResetTuning
	// never touches them): the schedule interval is clamped to
	// a sane range (0 = unset), a daily time must parse as
	// HH:MM (an unparseable value is a save error, not a silent
	// clear — the user typed it and should hear about it), and
	// the webhook flavor falls back to generic on anything
	// unrecognized.
	if in.ScheduleIntervalHours < 0 {
		in.ScheduleIntervalHours = 0
	}
	if in.ScheduleIntervalHours > 24*7 {
		in.ScheduleIntervalHours = 24 * 7
	}
	if in.ScheduleDailyTime != "" {
		if _, _, err := parseDailyTime(in.ScheduleDailyTime); err != nil {
			return err
		}
	}
	switch in.WebhookFlavor {
	case "generic", "discord", "ntfy":
	default:
		in.WebhookFlavor = "generic"
	}
	in.WebhookURL = strings.TrimSpace(in.WebhookURL)
	// Theme (v1.2.0) is a preference, normalized like the load
	// path: a submitted id the page ships is kept, an unknown
	// one falls back to the default without failing the save.
	// An EMPTY value means "not part of this save" (a page from
	// an older build has no theme field) and keeps the current
	// choice rather than resetting it.
	if in.Theme == "" {
		in.Theme = s.cfg.Theme
	} else {
		in.Theme = normalizeTheme(in.Theme)
	}
	// Roots saved through Settings get the same input hygiene as
	// roots sent to StartScan (quotes/tilde/env expansion, v0.1.1).
	cleaned := make([]string, 0, len(in.Roots))
	for _, r := range in.Roots {
		if p := cleanRootInput(r); p != "" {
			cleaned = append(cleaned, p)
		}
	}
	in.Roots = cleaned
	cp := in
	s.cfg = &cp
	return s.saveConfigLocked()
}

// ResetTuning resets ONLY the tuning knobs — Workers,
// SubtitleConfidence, the duration tolerances, the tiny-file
// floor, the four v0.5.0 probe/subtitle values, and (v0.8.0)
// the EmbeddedAudit switch, which resets to ON — back to
// their defaults, persists, and returns the resulting config.
// Roots, Exclusions, and the ffprobe/ffmpeg paths are the user's
// setup, not tuning, and are deliberately left untouched
// (v0.5.0, POST /api/config/reset-tuning).
func (s *Service) ResetTuning() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := defaultConfig()
	s.cfg.Workers = d.Workers
	s.cfg.SubtitleConfidence = d.SubtitleConfidence
	s.cfg.DurationTolerancePct = d.DurationTolerancePct
	s.cfg.DurationToleranceSec = d.DurationToleranceSec
	s.cfg.TinyFileFloorBytes = d.TinyFileFloorBytes
	s.cfg.ProbeTimeoutSec = d.ProbeTimeoutSec
	s.cfg.SubtitleSampleSec = d.SubtitleSampleSec
	s.cfg.SubtitleExtractTimeoutSec = d.SubtitleExtractTimeoutSec
	s.cfg.SubtitleMaxTracks = d.SubtitleMaxTracks
	s.cfg.EmbeddedAudit = d.EmbeddedAudit
	if err := s.saveConfigLocked(); err != nil {
		return Config{}, err
	}
	return *s.cfg, nil
}

// ListExclusions returns the persistent exclusion list.
func (s *Service) ListExclusions() []Exclusion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Exclusions == nil {
		return []Exclusion{}
	}
	return append([]Exclusion{}, s.cfg.Exclusions...)
}

// AddExclusion adds one persistent exclusion (path + scope).
func (s *Service) AddExclusion(e Exclusion) ([]Exclusion, error) {
	if strings.TrimSpace(e.Path) == "" {
		return nil, fmt.Errorf("path is required")
	}
	if !validScope(e.Scope) {
		return nil, fmt.Errorf("invalid scope %q (want all|subtitles|duplicates|health)", e.Scope)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Exclusions = addExclusion(s.cfg.Exclusions, e)
	if err := s.saveConfigLocked(); err != nil {
		return nil, err
	}
	return append([]Exclusion{}, s.cfg.Exclusions...), nil
}

// RemoveExclusion removes one exclusion by exact path+scope.
func (s *Service) RemoveExclusion(e Exclusion) ([]Exclusion, error) {
	if !validScope(e.Scope) {
		return nil, fmt.Errorf("invalid scope %q", e.Scope)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Exclusions = removeExclusion(s.cfg.Exclusions, e)
	if s.cfg.Exclusions == nil {
		s.cfg.Exclusions = []Exclusion{}
	}
	if err := s.saveConfigLocked(); err != nil {
		return nil, err
	}
	return append([]Exclusion{}, s.cfg.Exclusions...), nil
}

// ExportInventory returns the full inventory export (items with parsed
// identity + all findings, exclusions applied the same way the
// endpoints apply them).
func (s *Service) ExportInventory() ExportData {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]*Item, 0, len(s.results.Items))
	for _, it := range s.results.Items {
		cp := *it
		if scopes := exclusionScopes(s.cfg.Exclusions, it.Path); len(scopes) > 0 {
			cp.Excluded = true
			cp.ExclusionScopes = scopes
		}
		items = append(items, &cp)
	}
	groups, _ := buildDuplicates(s.results.Items, s.cfg, s.cfg.Exclusions)
	if groups == nil {
		groups = []DupGroup{}
	}
	return ExportData{
		Version:    Version,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Summary:    s.currentSummaryLocked(),
		Items:      items,
		Series:     buildSeries(s.results.Items),
		Subtitles:  s.filteredSubtitlesLocked(),
		Duplicates: groups,
		Health:     s.filteredHealthLocked(),
	}
}

// ExportCSV renders the items inventory as CSV bytes.
func (s *Service) ExportCSV() ([]byte, error) {
	items := s.Library("size", "desc")
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"path", "title", "kind", "year", "season", "episodes", "edition", "size_bytes", "duration_sec", "resolution", "codec", "bitrate", "hdr", "probe_ok"})
	for _, it := range items {
		eps := ""
		for i, e := range it.Episodes {
			if i > 0 {
				eps += ","
			}
			eps += fmt.Sprint(e)
		}
		_ = w.Write([]string{
			it.Path, it.Title, it.Kind, fmt.Sprint(it.Year), fmt.Sprint(it.Season), eps, it.Edition,
			fmt.Sprint(it.Size), fmt.Sprintf("%.1f", it.Duration), it.ResolutionLabel, it.Codec,
			fmt.Sprint(it.Bitrate), fmt.Sprint(it.HDR), fmt.Sprint(it.ProbeOK),
		})
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// saveStoreLocked persists the probe cache. Caller holds s.mu.
func (s *Service) saveStoreLocked() {
	if b, err := json.Marshal(s.store); err == nil {
		tmp := filepath.Join(s.dataDir, "cache.json.tmp")
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(s.dataDir, "cache.json"))
		}
	}
}

// incrementalStoreSaveInterval throttles the scan phases'
// incremental probe-cache saves (v0.4.1). The phases still ATTEMPT
// a save every 100 completed files, but the attempt writes at most
// once per interval: on a 10k-file library the unthrottled version
// meant 100+ full-store JSON marshals + writes of a growing cache,
// each taken under s.mu, stalling every state poll behind it. The
// unconditional saves (runScan's finish on completion/cancel/error,
// and result assembly) are NOT throttled — durability at scan end
// is exactly the same as before.
const incrementalStoreSaveInterval = 5 * time.Second

// storeSaveDue is the pure throttle decision, split out so it can
// be unit-tested without a clock seam: a save is due when none has
// happened yet, or the interval has fully elapsed since the last one.
func storeSaveDue(last, now time.Time) bool {
	return last.IsZero() || !now.Before(last.Add(incrementalStoreSaveInterval))
}

// saveStoreThrottledLocked persists the probe cache if the throttle
// interval has elapsed, and records the save time when it writes.
// Caller holds s.mu.
func (s *Service) saveStoreThrottledLocked() {
	now := time.Now()
	if !storeSaveDue(s.lastStoreSave, now) {
		return
	}
	s.lastStoreSave = now
	s.saveStoreLocked()
}

// saveResultsLocked persists the last scan results. Caller holds s.mu.
func (s *Service) saveResultsLocked() {
	if b, err := json.Marshal(s.results); err == nil {
		tmp := filepath.Join(s.dataDir, "scan.json.tmp")
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, filepath.Join(s.dataDir, "scan.json"))
		}
	}
}

// buildSeries aggregates episode items by series title.
func buildSeries(items []*Item) []SeriesAgg {
	byKey := map[string]*SeriesAgg{}
	for _, it := range items {
		if it.Kind != "episode" {
			continue
		}
		agg, ok := byKey[it.NormTitle]
		if !ok {
			agg = &SeriesAgg{Series: it.Title, Seasons: map[int]int64{}}
			byKey[it.NormTitle] = agg
		}
		agg.TotalBytes += it.Size
		agg.EpisodeFiles++
		agg.Seasons[it.Season] += it.Size
		if it.Size > agg.LargestBytes {
			agg.LargestBytes = it.Size
		}
	}
	out := []SeriesAgg{}
	for _, agg := range byKey {
		if agg.EpisodeFiles > 0 {
			agg.AvgBytes = agg.TotalBytes / int64(agg.EpisodeFiles)
		}
		out = append(out, *agg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalBytes > out[j].TotalBytes })
	return out
}
