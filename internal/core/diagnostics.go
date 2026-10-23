package core

// diagnostics.go — the support snapshot served by
// GET /api/diagnostics (v0.5.0).
//
// PURPOSE: when something is wrong on the user's machine, one
// downloaded JSON file replaces a round of screenshots and
// questions: which build, which platform, where the data lives,
// whether the configured ffprobe/ffmpeg actually exist, the
// tuning values in force, and what the last scan saw.
//
// HARD RULE — no secrets, ever: nothing from plex.json is copied
// here beyond "is Plex configured" and the server's display name
// (both already exposed via /api/state). The Plex token and the
// account token have NO field in this struct, so they cannot
// leak through it. A unit test plants a fake token in a fixture
// plex.json and asserts it never appears in the serialized
// diagnostics.

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// Diagnostics is the JSON shape of GET /api/diagnostics.
type Diagnostics struct {
	Version string `json:"version"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
	DataDir string `json:"data_dir"`
	// Configured tool paths + whether each currently exists on
	// disk as a file (the classic "worked yesterday" failure is a
	// moved/renamed ffmpeg).
	FFprobePath   string `json:"ffprobe_path"`
	FFprobeExists bool   `json:"ffprobe_exists"`
	FFmpegPath    string `json:"ffmpeg_path"`
	FFmpegExists  bool   `json:"ffmpeg_exists"`
	// Config summary: counts and tuning values — NOT the root
	// paths or exclusion paths themselves.
	RootsCount      int `json:"roots_count"`
	ExclusionsCount int `json:"exclusions_count"`
	// WantedLanguages (v0.9.0): the coverage wanted list in
	// force — a preference, not a secret, and the first thing a
	// "why is everything uncovered" report needs.
	WantedLanguages []string `json:"wanted_languages"`
	Tuning          struct {
		Workers                   int     `json:"workers"`
		SubtitleConfidence        float64 `json:"subtitle_confidence"`
		DurationTolerancePct      float64 `json:"duration_tolerance_pct"`
		DurationToleranceSec      float64 `json:"duration_tolerance_sec"`
		TinyFileFloorBytes        int64   `json:"tiny_file_floor_bytes"`
		ProbeTimeoutSec           int     `json:"probe_timeout_sec"`
		SubtitleSampleSec         int     `json:"subtitle_sample_sec"`
		SubtitleExtractTimeoutSec int     `json:"subtitle_extract_timeout_sec"`
		SubtitleMaxTracks         int     `json:"subtitle_max_tracks"`
		EmbeddedAudit             bool    `json:"embedded_audit"`
	} `json:"tuning"`
	// Plex appears ONLY as configured + server name (the token
	// secrecy rule above).
	PlexConfigured bool   `json:"plex_configured"`
	PlexServerName string `json:"plex_server_name,omitempty"`
	// Last scan: the headline summary + when it ran.
	LastScanSummary ScanSummary `json:"last_scan_summary"`
	LastScannedAt   string      `json:"last_scanned_at,omitempty"`
	// ProbeCacheEntries is the size of the on-disk probe cache —
	// the number that explains "why is the rescan fast/slow".
	ProbeCacheEntries int `json:"probe_cache_entries"`
	// OrganizeManifests counts the undo manifests in the data dir.
	OrganizeManifests int `json:"organize_manifests"`
	// ConfigWarning mirrors StateInfo.ConfigWarning (v0.5.0):
	// non-empty when config.json was corrupt at startup.
	ConfigWarning string `json:"config_warning,omitempty"`
	// v1.0.0 automation status — presence and outcomes only.
	// The webhook URL is NEVER included (a Discord/ntfy URL
	// embeds a credential); the schedule shows its shape, not
	// its anchor internals.
	ScheduleEnabled bool          `json:"schedule_enabled"`
	ScheduleNextRun string        `json:"schedule_next_run,omitempty"`
	WebhookEnabled  bool          `json:"webhook_enabled"`
	WebhookFlavor   string        `json:"webhook_flavor,omitempty"`
	WebhookLast     webhookStatus `json:"webhook_last"`
	// OpenSubtitles (v1.0.0): presence only — the API key and
	// login credentials follow the Plex token rule above.
	OpenSubtitlesEnabled    bool `json:"opensubtitles_enabled"`
	OpenSubtitlesConfigured bool `json:"opensubtitles_configured"`
	OpenSubtitlesHasLogin   bool `json:"opensubtitles_has_login"`
	HistoryRecords          int  `json:"history_records"`
}

// fileExistsOnDisk reports whether path names an existing
// non-directory file.
func fileExistsOnDisk(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Diagnostics builds the support snapshot from current state.
func (s *Service) Diagnostics() Diagnostics {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := Diagnostics{
		Version:           Version,
		GOOS:              runtime.GOOS,
		GOARCH:            runtime.GOARCH,
		DataDir:           s.dataDir,
		FFprobePath:       s.cfg.FFprobePath,
		FFprobeExists:     fileExistsOnDisk(s.cfg.FFprobePath),
		FFmpegPath:        s.cfg.FFmpegPath,
		FFmpegExists:      fileExistsOnDisk(s.cfg.FFmpegPath),
		RootsCount:        len(s.cfg.Roots),
		ExclusionsCount:   len(s.cfg.Exclusions),
		WantedLanguages:   append([]string{}, s.cfg.WantedLanguages...),
		PlexConfigured:    s.plex.configured(),
		PlexServerName:    s.plex.ServerName,
		LastScanSummary:   s.currentSummaryLocked(),
		LastScannedAt:     s.results.ScannedAt,
		ProbeCacheEntries: len(s.store.Cache),
		ConfigWarning:     s.configWarning,
	}
	d.Tuning.Workers = s.cfg.Workers
	d.Tuning.SubtitleConfidence = s.cfg.SubtitleConfidence
	d.Tuning.DurationTolerancePct = s.cfg.DurationTolerancePct
	d.Tuning.DurationToleranceSec = s.cfg.DurationToleranceSec
	d.Tuning.TinyFileFloorBytes = s.cfg.TinyFileFloorBytes
	d.Tuning.ProbeTimeoutSec = s.cfg.ProbeTimeoutSec
	d.Tuning.SubtitleSampleSec = s.cfg.SubtitleSampleSec
	d.Tuning.SubtitleExtractTimeoutSec = s.cfg.SubtitleExtractTimeoutSec
	d.Tuning.SubtitleMaxTracks = s.cfg.SubtitleMaxTracks
	d.Tuning.EmbeddedAudit = s.cfg.EmbeddedAudit
	// v1.0.0 automation status (see the struct comments).
	d.ScheduleEnabled = s.cfg.ScheduleEnabled
	if next, ok := s.scheduleNextRunLocked(time.Now()); ok {
		d.ScheduleNextRun = next.UTC().Format(time.RFC3339)
	}
	d.WebhookEnabled = s.cfg.WebhookEnabled
	d.WebhookFlavor = s.cfg.WebhookFlavor
	d.WebhookLast = s.webhookLast
	osView := osSecretsViewLocked(s)
	d.OpenSubtitlesEnabled = osView.Enabled
	d.OpenSubtitlesConfigured = osView.Configured
	d.OpenSubtitlesHasLogin = osView.HasLogin
	d.HistoryRecords = len(s.history)
	// Count undo manifests by filename (the manifests themselves
	// are organize_apply.go's business; a support snapshot only
	// needs the count).
	if entries, err := os.ReadDir(s.organizeManifestsDir()); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") &&
				!strings.HasSuffix(e.Name(), ".tmp") {
				d.OrganizeManifests++
			}
		}
	}
	return d
}
