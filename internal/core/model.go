package core

// model.go — shared data types for the Plex File Media Manager.
//
// Everything the scanner produces, the API serves, and the JSON store
// persists is defined here so a future worker can find the data model
// in one place. All types are plain JSON-serializable structs: the scan
// cache / last-scan store is a JSON file (see SPEC Decisions §1 — no
// third-party SQLite driver, stdlib only).

// Exclusion is one persistent "leave this alone" entry (Maintainerr
// pattern, SPEC §Reference). Scope is "all" or one check name:
// "subtitles", "duplicates", "health". A folder path excludes
// everything under it for that scope.
type Exclusion struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

// Config is the persisted user configuration (data dir / config.json).
type Config struct {
	Roots                []string    `json:"roots"`
	FFprobePath          string      `json:"ffprobe_path"`
	FFmpegPath           string      `json:"ffmpeg_path"`
	SubtitleConfidence   float64     `json:"subtitle_confidence"`    // flag threshold, default 0.75
	DurationTolerancePct float64     `json:"duration_tolerance_pct"` // duplicate confirm, default 3
	DurationToleranceSec float64     `json:"duration_tolerance_sec"` // duplicate confirm, default 120
	TinyFileFloorBytes   int64       `json:"tiny_file_floor_bytes"`  // health floor, default 1 MiB
	Workers              int         `json:"workers"`                // probe workers, default 4 (v0.3.1; was 2)
	Exclusions           []Exclusion `json:"exclusions"`
	// v0.5.0 tuning fields: previously hard-coded literals in the
	// probe / subtitle-extraction paths, now user-tunable in
	// Settings. Defaults are exactly the old literals, so a config
	// written by an older build (fields absent) behaves
	// byte-for-byte as before.
	ProbeTimeoutSec           int `json:"probe_timeout_sec"`            // ffprobe per-file timeout, default 60
	SubtitleSampleSec         int `json:"subtitle_sample_sec"`          // ffmpeg -t sample per subtitle track, default 120
	SubtitleExtractTimeoutSec int `json:"subtitle_extract_timeout_sec"` // ffmpeg extraction timeout, default 45
	SubtitleMaxTracks         int `json:"subtitle_max_tracks"`          // embedded-audit text-track cap per file, default 6
	// EmbeddedAudit (v0.8.0): the master switch for the embedded-
	// subtitle audit phase — the one knob the v0.5.0 tuning set was
	// missing. Default TRUE (the historical behavior). When false,
	// a scan creates no audit jobs and never invokes ffmpeg
	// extraction: results publish external-subtitle findings only,
	// and previously cached embedded findings are not republished.
	EmbeddedAudit bool `json:"embedded_audit"` // embedded-subtitle audit on/off, default true
	// WantedLanguages (v0.9.0): the subtitle languages the user
	// wants every item to have — the coverage derivation's wanted
	// set (coverage.go). Stored as entered (normalized to
	// lowercase, deduplicated by language); comparisons always go
	// through the ISO 639-1 canonical form, so "eng" and "en" are
	// the same want. Default ["eng"]. A preference, NOT tuning:
	// ResetTuning deliberately leaves it alone.
	WantedLanguages []string `json:"wanted_languages"`
	// v1.0.0 automation settings. These are PREFERENCES, not
	// tuning: ResetTuning deliberately leaves all of them alone.
	//
	// Scheduled scans (scheduler.go): ScheduleEnabled is a bool
	// whose default is FALSE, but it still needs the presence-
	// aware load merge (v0.8.0 pattern) — a config written before
	// v1.0.0 must not gain a schedule, and a saved value must be
	// honored exactly. The schedule is EITHER an interval in
	// hours (ScheduleIntervalHours > 0) or a daily local time
	// (ScheduleDailyTime "HH:MM"); the interval wins when both
	// are set. Scheduled scans run in-process while the app
	// runs — no cron, no OS timer (FEATURE-PLAN §5).
	ScheduleEnabled       bool   `json:"schedule_enabled"`
	ScheduleIntervalHours int    `json:"schedule_interval_hours,omitempty"`
	ScheduleDailyTime     string `json:"schedule_daily_time,omitempty"`
	// Scan-finished webhook (webhook.go): WebhookEnabled follows
	// the same bool merge rules. WebhookFlavor is "generic"
	// (the default JSON payload), "discord", or "ntfy".
	WebhookEnabled bool   `json:"webhook_enabled"`
	WebhookURL     string `json:"webhook_url,omitempty"`
	WebhookFlavor  string `json:"webhook_flavor,omitempty"`
	// ScheduleLastRunUnix (v1.0.0): the scheduler's anchor — the
	// unix time of its last action (fire OR recorded skip),
	// persisted with the config so interval math survives
	// restarts. Written by the scheduler itself; user edits to
	// it are meaningless but harmless.
	ScheduleLastRunUnix int64 `json:"schedule_last_run_unix,omitempty"`
	// Theme (v1.2.0): the UI theme id (themes.go) — a preference
	// like WantedLanguages, NOT tuning: ResetTuning deliberately
	// leaves it alone. The palettes live in the page's CSS; the
	// config only carries the chosen id so the choice follows
	// the user across browsers. Absent/unknown = "midnight",
	// the original palette.
	Theme string `json:"theme,omitempty"`
	// KofiUsername (v1.3.2): the Ko-fi account the floating
	// Donate button points at — a preference like Theme, NOT
	// tuning: ResetTuning deliberately leaves it alone. A BLANK
	// value disables the button entirely: the page must not
	// even attempt the Ko-fi script load. The field is a plain
	// string (no omitempty): a saved blank must survive a
	// round trip as blank, distinct from a missing key (which
	// loadConfig maps to the default — see the presence probe
	// there). PUBLIC EDITION EXCEPTION (owner-decided
	// 2026-10-23): the default is carried as-is, so the
	// button works out of the box for public users donating
	// to the maintainer. A blank value still disables it.
	KofiUsername string `json:"kofi_username"`
	// KofiEnabled (v1.3.2, owner-added mid-build): the
	// "Show Ko-fi Donate button" toggle — a preference like
	// KofiUsername, NOT tuning: ResetTuning deliberately
	// leaves it alone. Default ON (the online-conditional
	// behavior ships out of the box); OFF means the page
	// never attempts the Ko-fi script load at all, regardless
	// of connectivity or username. Like EmbeddedAudit, it
	// needs the presence-aware load merge: a bool whose
	// default is TRUE cannot use the "non-zero = present"
	// rule, or an older config without the key would read
	// as OFF.
	KofiEnabled bool `json:"kofi_enabled"`
	// v1.4.0 link-library preferences — all three are
	// PREFERENCES, not tuning: ResetTuning deliberately
	// leaves them alone. All three default ON and need the
	// presence-aware load merge (the EmbeddedAudit pattern)
	// for the same reason KofiEnabled does.
	//
	// LinkExtras: extras (trailers, featurettes, deleted
	// scenes, commentaries, NCOP/NCED — everything the parser
	// classifies "extra") get hardlinks into the built
	// library's per-work extras/ folder. Link mode only:
	// extras still never MOVE. OFF reproduces the pre-1.4.0
	// plan exactly (extras stay "extra content — left in
	// place"). Samples never link either way.
	LinkExtras bool `json:"link_extras"`
	// AssignSpecialNumbers: a TV file with an identified
	// series but no episode number that signals special-ness
	// (an S00 marker, the word "Special", a Specials/OVA
	// folder, a making-of) is given the next Season 00 number
	// for its series, in sorted source-path order. The number
	// is CREATED, never found in the name — plan entries
	// carry it flagged (number_assigned) and the UI badges
	// it, so an invented number is never silent. OFF leaves
	// such files unidentified, as before.
	AssignSpecialNumbers bool `json:"assign_special_numbers"`
	// AllowYearlessMovies: a movie whose name carries no
	// year still links — as Title/Title.ext with no (Year) —
	// when it sits in a movie context (its own folder, or a
	// folder naming Movies). Plan entries are flagged
	// (no_year) and badged. OFF restores the pre-1.4.0 law:
	// a movie with no year is not identified.
	AllowYearlessMovies bool `json:"allow_yearless_movies"`
}

// StreamInfo is the flattened per-stream view of an ffprobe stream that
// the rest of the app actually uses.
type StreamInfo struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Type     string `json:"type"` // video | audio | subtitle | other
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Bitrate  int64  `json:"bitrate,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	HDR      bool   `json:"hdr,omitempty"`
	Channels int    `json:"channels,omitempty"`
}

// ProbeResult is the cached outcome of one ffprobe run on one file.
type ProbeResult struct {
	OK         bool         `json:"ok"`
	Error      string       `json:"error,omitempty"`
	Duration   float64      `json:"duration"` // seconds, 0 = unknown
	Size       int64        `json:"size"`     // container-reported size
	Bitrate    int64        `json:"bitrate"`  // container bitrate
	HasVideo   bool         `json:"has_video"`
	VideoCodec string       `json:"video_codec,omitempty"`
	Width      int          `json:"width,omitempty"`
	Height     int          `json:"height,omitempty"`
	HDR        bool         `json:"hdr,omitempty"`
	AudioDesc  string       `json:"audio_desc,omitempty"` // e.g. "aac 5.1"
	Streams    []StreamInfo `json:"streams,omitempty"`
}

// Item is one scanned media file in the library inventory.
type Item struct {
	Path            string   `json:"path"`
	Root            string   `json:"root"`
	Name            string   `json:"name"`
	Ext             string   `json:"ext"`
	Size            int64    `json:"size"`
	ModTimeUnix     int64    `json:"mtime_unix"`
	Kind            string   `json:"kind"` // movie | episode | unknown
	Title           string   `json:"title"`
	NormTitle       string   `json:"norm_title"`
	Year            int      `json:"year,omitempty"`
	Season          int      `json:"season,omitempty"`
	Episodes        []int    `json:"episodes,omitempty"`
	Edition         string   `json:"edition,omitempty"`
	ReleaseTags     []string `json:"release_tags,omitempty"`
	ResolutionLabel string   `json:"resolution"`
	Width           int      `json:"width,omitempty"`
	Height          int      `json:"height,omitempty"`
	Codec           string   `json:"codec,omitempty"`
	Bitrate         int64    `json:"bitrate,omitempty"`
	Duration        float64  `json:"duration"`
	HDR             bool     `json:"hdr,omitempty"`
	AudioDesc       string   `json:"audio_desc,omitempty"`
	ProbeOK         bool     `json:"probe_ok"`
	ProbeError      string   `json:"probe_error,omitempty"`
	HasVideo        bool     `json:"has_video"`
	SampleHash      string   `json:"sample_hash,omitempty"`
	// Dev / Ino / Nlink (v1.0.0, FEATURE-PLAN §2): the file's
	// storage identity, captured at WALK time from the walk's
	// own os.FileInfo (no extra IO). Two items are the same
	// physical file iff Dev and Ino are both non-zero and equal
	// (hardlinks). Zero means "identity unknown on this
	// platform/filesystem" — duplicates math then falls back to
	// the pre-v1.0.0 per-file arithmetic and says so. Nlink is
	// the link count at walk time (0 = unknown).
	Dev             uint64   `json:"dev,omitempty"`
	Ino             uint64   `json:"ino,omitempty"`
	Nlink           uint64   `json:"nlink,omitempty"`
	IsSample        bool     `json:"is_sample,omitempty"` // sample/trailer/extras pattern
	Special         string   `json:"special,omitempty"`   // "multi-episode" | "split-file"
	Excluded        bool     `json:"excluded,omitempty"`
	ExclusionScopes []string `json:"exclusion_scopes,omitempty"`
}

// SubtitleFinding is one subtitle-audit finding (external or embedded).
// Status: "mismatch" | "missing-language" | "uncertain" | "cannot-verify".
type SubtitleFinding struct {
	Path       string   `json:"path"`   // subtitle file (external) or media file (embedded)
	Source     string   `json:"source"` // external | embedded
	StreamIdx  int      `json:"stream_index,omitempty"`
	Declared   string   `json:"declared"` // ISO 639-1, "und", or "" when absent
	Detected   string   `json:"detected"`
	Confidence float64  `json:"confidence"`
	Words      int      `json:"words"`
	Status     string   `json:"status"`
	Sample     []string `json:"sample,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// HealthFinding is one empty/corrupt finding with its probe evidence.
type HealthFinding struct {
	Path     string `json:"path"`
	Type     string `json:"type"` // empty|tiny|probe-failure|no-video-stream|no-duration|suspect-truncated|incomplete|empty-folder|subtitle-empty|subtitle-unparseable|walk-warning|orphan-sidecar
	Evidence string `json:"evidence"`
	Size     int64  `json:"size,omitempty"`
}

// DupCopy is one file inside a duplicate group (an Item plus flags).
type DupCopy struct {
	Item
	ExactDuplicate bool `json:"exact_duplicate,omitempty"`
	IsKeeper       bool `json:"is_keeper,omitempty"`
	// HardlinkOfKeeper (v1.0.0): this copy is the SAME physical
	// file as the group's keeper (same device+inode, both
	// known) — deleting it frees zero bytes. Meaningful only
	// when the group's HardlinkKnown is true.
	HardlinkOfKeeper bool `json:"hardlink_of_keeper,omitempty"`
	// IdentityKnown (v1.0.0): this copy's device+inode were
	// captured (both non-zero). Copies without identity are
	// counted as separate physical files (the honest fallback).
	IdentityKnown bool `json:"identity_known,omitempty"`
}

// CrossLink points from one edition sub-group to another edition of the
// same base identity (SPEC: editions never merge, they cross-link).
type CrossLink struct {
	Edition string   `json:"edition"`
	Key     string   `json:"key"`
	Count   int      `json:"count"`
	Paths   []string `json:"paths"`
}

// DupGroup is one duplicate group (one edition of one identity).
type DupGroup struct {
	Key         string    `json:"key"`
	BaseKey     string    `json:"base_key"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Year        int       `json:"year,omitempty"`
	Season      int       `json:"season,omitempty"`
	Episodes    []int     `json:"episodes,omitempty"`
	Edition     string    `json:"edition,omitempty"`
	Status      string    `json:"status"` // confirmed | probable | demoted
	Note        string    `json:"note,omitempty"`
	Copies      []DupCopy `json:"copies"`
	WastedBytes int64     `json:"wasted_bytes"`
	// ReclaimableBytes (v1.0.0): what deleting every non-keeper
	// copy would ACTUALLY free — each distinct physical file
	// (device+inode) beyond the keeper's counts once, hardlinks
	// of the keeper count zero. Equals WastedBytes whenever all
	// copies are distinct physical files (the pre-v1.0.0 case,
	// including unknown identity — the labelled fallback).
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
	// PhysicalCopies / FileEntries (v1.0.0): distinct physical
	// files vs directory entries in this group. HardlinkKnown
	// is true only when EVERY copy's identity was captured;
	// when false the reclaimable figure is the fallback
	// arithmetic and must be labelled "at least / estimated".
	PhysicalCopies int         `json:"physical_copies"`
	FileEntries    int         `json:"file_entries"`
	HardlinkKnown  bool        `json:"hardlink_known"`
	KeeperPath     string      `json:"keeper_path"`
	KeeperWhy      string      `json:"keeper_why"`
	HasExact       bool        `json:"has_exact,omitempty"`
	CrossLinks     []CrossLink `json:"cross_links,omitempty"`
}

// SeriesAgg is the per-series aggregation for the Library view.
type SeriesAgg struct {
	Series       string        `json:"series"`
	TotalBytes   int64         `json:"total_bytes"`
	EpisodeFiles int           `json:"episode_files"`
	Seasons      map[int]int64 `json:"seasons"` // season -> bytes
	AvgBytes     int64         `json:"avg_episode_bytes"`
	LargestBytes int64         `json:"largest_episode_bytes"`
}

// ScanSummary is the headline counts of the last scan.
type ScanSummary struct {
	MediaCount        int   `json:"media_count"`
	SubtitleFileCount int   `json:"subtitle_file_count"`
	TotalBytes        int64 `json:"total_bytes"`
	DuplicateGroups   int   `json:"duplicate_groups"`
	DuplicateWasted   int64 `json:"duplicate_wasted_bytes"`
	// DuplicateReclaimable (v1.0.0): the summary-level honest
	// figure — the sum of every group's ReclaimableBytes (see
	// DupGroup). DuplicateWasted stays the apparent figure
	// (total minus one copy per group) so older consumers keep
	// their meaning; the two are equal unless hardlinks exist.
	DuplicateReclaimable int64 `json:"duplicate_reclaimable_bytes"`
	SubtitleFindings     int   `json:"subtitle_findings"`
	HealthFindings       int   `json:"health_findings"`
	ExcludedDupCopies    int   `json:"excluded_duplicate_copies"`
	ExclusionCount       int   `json:"exclusion_count"` // configured exclusions (Maintainerr pattern)
	ScannedRootCount     int   `json:"scanned_roots"`
	// v0.9.0 derivations — computed in currentSummaryLocked from
	// the same results, so every surface (state, diagnostics,
	// exports) agrees. Gaps: series+season rows with missing
	// episode numbers, and the total number of missing episodes
	// across them (see gaps.go). Coverage: per-item
	// wanted-language classes (see coverage.go); items the user
	// dismissed from coverage are excluded from all four counts.
	GapCount             int `json:"gap_count"`
	GapEpisodes          int `json:"gap_episodes"`
	CoverageCovered      int `json:"coverage_covered"`
	CoverageNoWanted     int `json:"coverage_no_wanted"`
	CoverageNoSubtitles  int `json:"coverage_no_subtitles"`
	CoverageUnverifiable int `json:"coverage_unverifiable"`
}

// ScanResults is everything one scan produced (persisted to scan.json).
type ScanResults struct {
	Items        []*Item           `json:"items"`
	Subtitles    []SubtitleFinding `json:"subtitles"`
	Health       []HealthFinding   `json:"health"`
	Summary      ScanSummary       `json:"summary"`
	ScannedAt    string            `json:"scanned_at,omitempty"`
	FFprobeFound string            `json:"ffprobe_found,omitempty"`
	FFmpegFound  string            `json:"ffmpeg_found,omitempty"`
	// SubtitleFilePaths (v0.9.0) persists the scanned external
	// subtitle file paths so the coverage derivation (read-time,
	// over items + the probe cache) can associate sidecars with
	// their items after a restart. Additive: scan.json files
	// written by older builds simply carry no sidecar evidence
	// until the next scan.
	SubtitleFilePaths []string `json:"subtitle_file_paths,omitempty"`
}

// ScanState is the live progress view served by /api/state.
type ScanState struct {
	Running bool   `json:"running"`
	Phase   string `json:"phase,omitempty"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	Current string `json:"current,omitempty"`
	// CurrentSince (v0.3.1) is when Current last changed (RFC3339
	// UTC): the UI shows "on this file for Ns" so a slow file reads
	// as slow, not as a frozen scan.
	CurrentSince string `json:"current_since,omitempty"`
	// AuditPending (v0.3.4) is true between the scan's first
	// assembly — library / duplicates / health / EXTERNAL subtitle
	// findings already published — and the embedded-subtitle
	// audit's second assembly merging its findings in. State-only
	// by design: the persisted results JSON does not carry it, and
	// a cancelled audit leaves it set so the UI can keep warning
	// that embedded findings are missing from the shown results.
	AuditPending bool `json:"audit_pending"`
	// AuditSkipped (v0.8.0) is true when the most recent scan ran
	// with the embedded-subtitle audit OFF in Settings: no audit
	// phase ran at all, AuditPending is false, and the published
	// subtitle findings are external-only BY CHOICE — distinct
	// from both "audit still running" (AuditPending) and "audit
	// done" (both false). Set by every result assembly, so it
	// always describes the scan that produced the current results.
	AuditSkipped bool   `json:"audit_skipped"`
	StartedAt    string `json:"started_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	// RootProblems lists every root rejected by validation at scan
	// start (v0.1.1) — kept on the state so the UI can keep showing
	// them after the scan finishes, not just in a transient alert.
	RootProblems []string    `json:"root_problems,omitempty"`
	Summary      ScanSummary `json:"summary"`
	ScannedAt    string      `json:"scanned_at,omitempty"`
}

// CacheEntry wraps a ProbeResult with the file fingerprint it belongs
// to; a cache hit requires path + size + mtime to all match.
type CacheEntry struct {
	Size      int64        `json:"size"`
	MtimeNano int64        `json:"mtime_nano"`
	Probe     *ProbeResult `json:"probe"`
	// Dev / Ino / Nlink (v1.0.0): the file identity captured at
	// walk time, persisted additively with the probe (older
	// cache files simply carry zeros = unknown until the entry
	// is refreshed). The scan's authoritative identity is the
	// fresh walk's, never the cache's — see scanner.go.
	Dev   uint64 `json:"dev,omitempty"`
	Ino   uint64 `json:"ino,omitempty"`
	Nlink uint64 `json:"nlink,omitempty"`
	// EmbeddedFindings + AuditDone (v0.3.4) cache the embedded-
	// subtitle audit alongside the probe, under the SAME
	// fingerprint: extracting subtitle text with ffmpeg is the
	// scan's expensive step, so an unchanged file must never be
	// extracted twice. AuditDone separates "audited, and the
	// answer was zero findings" from "not audited yet" — entries
	// written before v0.3.4 have it false and simply re-audit once.
	EmbeddedFindings []SubtitleFinding `json:"embedded_findings,omitempty"`
	AuditDone        bool              `json:"audit_done,omitempty"`
}

// Store is the on-disk JSON cache (cache.json in the data dir).
type Store struct {
	Cache map[string]CacheEntry `json:"cache"` // key = absolute path
}
