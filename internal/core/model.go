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
	Type     string `json:"type"` // empty|tiny|probe-failure|no-video-stream|no-duration|suspect-truncated|incomplete|empty-folder|subtitle-empty|subtitle-unparseable|walk-warning
	Evidence string `json:"evidence"`
	Size     int64  `json:"size,omitempty"`
}

// DupCopy is one file inside a duplicate group (an Item plus flags).
type DupCopy struct {
	Item
	ExactDuplicate bool `json:"exact_duplicate,omitempty"`
	IsKeeper       bool `json:"is_keeper,omitempty"`
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
	Key         string      `json:"key"`
	BaseKey     string      `json:"base_key"`
	Kind        string      `json:"kind"`
	Title       string      `json:"title"`
	Year        int         `json:"year,omitempty"`
	Season      int         `json:"season,omitempty"`
	Episodes    []int       `json:"episodes,omitempty"`
	Edition     string      `json:"edition,omitempty"`
	Status      string      `json:"status"` // confirmed | probable | demoted
	Note        string      `json:"note,omitempty"`
	Copies      []DupCopy   `json:"copies"`
	WastedBytes int64       `json:"wasted_bytes"`
	KeeperPath  string      `json:"keeper_path"`
	KeeperWhy   string      `json:"keeper_why"`
	HasExact    bool        `json:"has_exact,omitempty"`
	CrossLinks  []CrossLink `json:"cross_links,omitempty"`
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
	SubtitleFindings  int   `json:"subtitle_findings"`
	HealthFindings    int   `json:"health_findings"`
	ExcludedDupCopies int   `json:"excluded_duplicate_copies"`
	ExclusionCount    int   `json:"exclusion_count"` // configured exclusions (Maintainerr pattern)
	ScannedRootCount  int   `json:"scanned_roots"`
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
	AuditPending bool   `json:"audit_pending"`
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
