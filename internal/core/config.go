package core

// config.go — persisted configuration + exclusion matching.
//
// Config lives as JSON in the data dir so the Ultra install needs no
// database and no environment setup. Exclusion matching lives here too
// because every findings endpoint consults it.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Tuning defaults + clamp bounds for the numeric knobs (v0.5.0).
// The four subtitle/probe values were literals in probe.go /
// scanner.go before v0.5.0; the defaults here ARE those literals,
// so upgrading changes no behavior until the user edits them.
const (
	defaultProbeTimeoutSec           = 60
	defaultSubtitleSampleSec         = 120
	defaultSubtitleExtractTimeoutSec = 45
	// defaultSubtitleMaxTracks mirrors maxTextSubtitleSamplesPerFile
	// in scanner.go (kept as the named default the audit falls back
	// to if a Config ever reaches it unclamped).
	defaultSubtitleMaxTracks = 6
)

// clampTuningInt bounds v into [lo, hi]; a non-positive v (field
// absent / cleared) yields def. Used by loadConfig and SaveConfig
// alike, the way Workers has always been clamped in both.
func clampTuningInt(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// defaultConfig returns a Config with the SPEC defaults filled in and
// ffprobe/ffmpeg auto-detected from PATH.
func defaultConfig() *Config {
	c := &Config{
		SubtitleConfidence:   0.75,
		DurationTolerancePct: 3,
		DurationToleranceSec: 120,
		TinyFileFloorBytes:   1024 * 1024,
		// v0.3.1: default probe workers 2 -> 4. Probing is IO-bound
		// subprocess work, and 2 workers made first scans of real
		// libraries crawl (live-test report). Only the DEFAULT
		// changed — a saved config keeps its own value.
		Workers:                   4,
		ProbeTimeoutSec:           defaultProbeTimeoutSec,
		SubtitleSampleSec:         defaultSubtitleSampleSec,
		SubtitleExtractTimeoutSec: defaultSubtitleExtractTimeoutSec,
		SubtitleMaxTracks:         defaultSubtitleMaxTracks,
		// v0.8.0: the embedded-audit master switch defaults ON —
		// that is the behavior every build before this one had.
		EmbeddedAudit: true,
		// v0.9.0: coverage wants English subtitles by default.
		WantedLanguages: []string{"eng"},
		// v1.0.0: webhook flavor defaults to the generic JSON
		// payload; the schedule and the webhook both default OFF.
		WebhookFlavor: "generic",
		// v1.2.0: the UI theme defaults to the original palette.
		Theme: DefaultTheme,
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		c.FFprobePath = p
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		c.FFmpegPath = p
	}
	return c
}

// loadConfig reads config.json from the data dir, merging over defaults
// so a config written by an older build still gets sane values.
//
// v0.5.0 corrupt-config resilience: if config.json EXISTS but does
// not parse, the old behavior silently fell back to defaults and
// the file was overwritten on the next save — the user's settings
// were gone with no trace. Now the unreadable file is preserved by
// renaming it to config.json.corrupt (or -2, -3… when earlier
// preserves exist), defaults are used, and the second return value
// carries a user-facing warning the Service surfaces in State()
// (StateInfo.ConfigWarning) and in the diagnostics export. A
// missing file is NOT corruption: first run, defaults, no warning.
func loadConfig(dataDir string) (*Config, string) {
	c := defaultConfig()
	path := filepath.Join(dataDir, "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return c, ""
	}
	var saved Config
	if uerr := json.Unmarshal(b, &saved); uerr != nil {
		preserved := preserveCorruptConfig(dataDir)
		warn := "config.json could not be read (" + uerr.Error() + ") — your previous settings were preserved as " + preserved + " and the app started with default settings."
		return c, warn
	}
	// Only override defaults with values actually present/non-zero,
	// except slices which are taken as saved.
	if saved.SubtitleConfidence > 0 {
		c.SubtitleConfidence = saved.SubtitleConfidence
	}
	if saved.DurationTolerancePct > 0 {
		c.DurationTolerancePct = saved.DurationTolerancePct
	}
	if saved.DurationToleranceSec > 0 {
		c.DurationToleranceSec = saved.DurationToleranceSec
	}
	if saved.TinyFileFloorBytes > 0 {
		c.TinyFileFloorBytes = saved.TinyFileFloorBytes
	}
	if saved.Workers > 0 {
		c.Workers = saved.Workers
	}
	if saved.ProbeTimeoutSec > 0 {
		c.ProbeTimeoutSec = saved.ProbeTimeoutSec
	}
	if saved.SubtitleSampleSec > 0 {
		c.SubtitleSampleSec = saved.SubtitleSampleSec
	}
	if saved.SubtitleExtractTimeoutSec > 0 {
		c.SubtitleExtractTimeoutSec = saved.SubtitleExtractTimeoutSec
	}
	if saved.SubtitleMaxTracks > 0 {
		c.SubtitleMaxTracks = saved.SubtitleMaxTracks
	}
	// v0.8.0: EmbeddedAudit is a bool whose default is TRUE, so the
	// numeric fields' "non-zero = present" merge above cannot
	// express it: a config written before the field existed has no
	// key at all (and must stay ON), while a deliberately saved
	// OFF must be honored. Presence is therefore probed from the
	// raw JSON: only an explicit "embedded_audit" key overrides
	// the default.
	var auditProbe struct {
		EmbeddedAudit *bool `json:"embedded_audit"`
	}
	if err := json.Unmarshal(b, &auditProbe); err == nil && auditProbe.EmbeddedAudit != nil {
		c.EmbeddedAudit = *auditProbe.EmbeddedAudit
	}
	// v1.0.0: the two automation switches get the same presence-
	// aware treatment. Their default is FALSE, so the numeric
	// rule would coincidentally work — but the presence probe
	// keeps the semantics explicit (absent = default, present =
	// honored) and immune to a future default flip.
	var v100Probe struct {
		ScheduleEnabled *bool `json:"schedule_enabled"`
		WebhookEnabled  *bool `json:"webhook_enabled"`
	}
	if err := json.Unmarshal(b, &v100Probe); err == nil {
		if v100Probe.ScheduleEnabled != nil {
			c.ScheduleEnabled = *v100Probe.ScheduleEnabled
		}
		if v100Probe.WebhookEnabled != nil {
			c.WebhookEnabled = *v100Probe.WebhookEnabled
		}
	}
	if saved.ScheduleIntervalHours > 0 {
		c.ScheduleIntervalHours = saved.ScheduleIntervalHours
	}
	if saved.ScheduleDailyTime != "" {
		c.ScheduleDailyTime = saved.ScheduleDailyTime
	}
	if saved.WebhookURL != "" {
		c.WebhookURL = saved.WebhookURL
	}
	if saved.WebhookFlavor != "" {
		c.WebhookFlavor = saved.WebhookFlavor
	}
	if saved.ScheduleLastRunUnix > 0 {
		c.ScheduleLastRunUnix = saved.ScheduleLastRunUnix
	}
	c.Roots = saved.Roots
	c.Exclusions = saved.Exclusions
	// v0.9.0: wanted languages are a slice, taken as saved (like
	// Roots/Exclusions) and normalized; an older config without
	// the key keeps the default, and a saved-but-empty list falls
	// back to the default rather than wanting nothing.
	if saved.WantedLanguages != nil {
		c.WantedLanguages = normalizeWantedLanguages(saved.WantedLanguages)
		if len(c.WantedLanguages) == 0 {
			c.WantedLanguages = []string{"eng"}
		}
	}
	// v1.2.0: the theme is a plain stored string, normalized on
	// the way in — an older config has no key (default), and an
	// unknown id (e.g. written by a newer build) falls back to
	// the default rather than erroring or sticking the UI with
	// a palette it does not have.
	c.Theme = normalizeTheme(saved.Theme)
	if saved.FFprobePath != "" {
		c.FFprobePath = saved.FFprobePath
	}
	if saved.FFmpegPath != "" {
		c.FFmpegPath = saved.FFmpegPath
	}
	if c.Workers < 1 {
		c.Workers = 1
	}
	if c.Workers > 16 {
		c.Workers = 16
	}
	c.ProbeTimeoutSec = clampTuningInt(c.ProbeTimeoutSec, 5, 600, defaultProbeTimeoutSec)
	c.SubtitleSampleSec = clampTuningInt(c.SubtitleSampleSec, 10, 900, defaultSubtitleSampleSec)
	c.SubtitleExtractTimeoutSec = clampTuningInt(c.SubtitleExtractTimeoutSec, 5, 600, defaultSubtitleExtractTimeoutSec)
	c.SubtitleMaxTracks = clampTuningInt(c.SubtitleMaxTracks, 1, 20, defaultSubtitleMaxTracks)
	return c, ""
}

// preserveCorruptConfig renames dataDir/config.json to the first
// free config.json.corrupt / config.json.corrupt-N name and returns
// the name used. If the rename itself fails, it returns the plain
// "config.json.corrupt" name anyway (the warning text still tells
// the user where their settings were meant to go, and the defaults
// fallback proceeds regardless — a failed preserve must never stop
// the app from starting).
func preserveCorruptConfig(dataDir string) string {
	src := filepath.Join(dataDir, "config.json")
	name := "config.json.corrupt"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dataDir, name)); os.IsNotExist(err) {
			break
		}
		name = "config.json.corrupt-" + strconv.Itoa(i)
	}
	_ = os.Rename(src, filepath.Join(dataDir, name))
	return name
}

// normalizeWantedLanguages cleans a wanted-languages list for
// storage (v0.9.0): entries are trimmed and lowercased, empties
// dropped, and duplicates removed BY LANGUAGE — "spa" and "es"
// are the same want, so only the first spelling is kept. The
// stored form is otherwise what the user typed ("eng" stays
// "eng"); every comparison canonicalizes through
// normalizeLangKey, so the spelling never changes meaning.
func normalizeWantedLanguages(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		key := normalizeLangKey(c)
		if key == "" {
			key = c
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// wantedLangKeys returns the canonical (ISO 639-1) key set of the
// config's wanted languages — the set coverage derivation tests
// subtitle languages against. An empty configured list (which the
// load/save paths already prevent) still wants English rather
// than nothing.
func (c *Config) wantedLangKeys() map[string]bool {
	out := map[string]bool{}
	for _, code := range c.WantedLanguages {
		if k := normalizeLangKey(code); k != "" {
			out[k] = true
		}
	}
	if len(out) == 0 {
		out["en"] = true
	}
	return out
}

// saveConfig writes the config back to the data dir atomically enough
// for a single-user app (write temp + rename).
func (s *Service) saveConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveConfigLocked()
}

// saveConfigLocked is saveConfig for callers already holding s.mu.
func (s *Service) saveConfigLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dataDir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dataDir, "config.json"))
}

// cleanAbs normalizes a path for exclusion comparison.
func cleanAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

// isExcluded reports whether path is excluded for the given scope.
// An entry applies when its scope is "all" or equals the scope, and the
// path is the entry path itself or lives under it (folder exclusion).
func isExcluded(exclusions []Exclusion, path, scope string) bool {
	cp := cleanAbs(path)
	for _, e := range exclusions {
		if e.Scope != "all" && e.Scope != scope {
			continue
		}
		ep := cleanAbs(e.Path)
		if cp == ep || strings.HasPrefix(cp, ep+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// exclusionScopes returns every scope under which path is excluded
// (used to mark Library items as excluded without hiding them).
func exclusionScopes(exclusions []Exclusion, path string) []string {
	var scopes []string
	cp := cleanAbs(path)
	for _, e := range exclusions {
		ep := cleanAbs(e.Path)
		if cp == ep || strings.HasPrefix(cp, ep+string(os.PathSeparator)) {
			scopes = append(scopes, e.Scope)
		}
	}
	return scopes
}

// addExclusion appends an exclusion if not already present.
func addExclusion(list []Exclusion, e Exclusion) []Exclusion {
	e.Path = cleanAbs(e.Path)
	for _, x := range list {
		if x.Path == e.Path && x.Scope == e.Scope {
			return list
		}
	}
	return append(list, e)
}

// removeExclusion drops the exact path+scope entry.
func removeExclusion(list []Exclusion, e Exclusion) []Exclusion {
	ep := cleanAbs(e.Path)
	out := list[:0]
	for _, x := range list {
		if x.Path == ep && x.Scope == e.Scope {
			continue
		}
		out = append(out, x)
	}
	return out
}

// validScope guards the exclusion API against nonsense scopes.
func validScope(s string) bool {
	switch s {
	case "all", "subtitles", "duplicates", "health":
		return true
	}
	return false
}
