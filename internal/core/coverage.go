package core

// coverage.go — subtitle coverage derivation (v0.9.0,
// FEATURE-PLAN §3 + SUBTITLE-WIZARD-SPEC §2.2 Step F).
//
// For every media item the scan probed successfully, coverage
// answers one question: does this item HAVE subtitles in one of
// the user's wanted languages (Settings → Wanted languages,
// default eng)? The evidence is the probe's stream list (from
// the probe cache — items do not retain streams) plus the
// scan's external subtitle files, associated with items by the
// same filename rule Organize uses (a sidecar belongs to the
// video whose stem it extends).
//
// Classes (exactly one per covered item):
//
//   - covered: a wanted language is present as a TEXT subtitle
//     (embedded text stream or external file) — verifiable
//     evidence, the only class that counts as "has it".
//   - no-wanted: subtitles exist, but none in a wanted
//     language. Includes tracks/files whose language is
//     undeclared or tagged "und" — present, language unknown.
//   - none: no subtitle evidence of any kind.
//   - unverifiable: subtitle evidence exists ONLY as image
//     tracks (PGS/VobSub/DVB — they carry no text this app can
//     verify a language from). An image track tagged with a
//     wanted language still lands here, NOT in covered: the tag
//     is a claim, and this app does not count claims as coverage.
//
// A forced-only track does NOT count as coverage in any class
// calculation (a forced track shows foreign dialogue only — it
// is not a subtitle track for the item's own language); it is
// reported in the item's notes instead. Probe-failed items are
// EXCLUDED from coverage entirely: unknown is not uncovered,
// and counting a broken file as "no subtitles" would bury the
// health finding that actually describes it. Sample/trailer
// items (Item.IsSample) are excluded for the same reason —
// nobody needs subtitles on a trailer.
//
// The derivation is read-time over results + probe cache +
// config, computed ONCE here and consumed by the summary
// (currentSummaryLocked), the wizard overview, and the
// coverage endpoint, so the three can never disagree. Items the
// user dismissed from coverage ("Not wanted",
// subtitle_dismissals.go) are excluded from the item list and
// all counts, and reported separately.

import (
	"path/filepath"
	"sort"
	"strings"
)

// Coverage classes (the Class field of CoverageItem and the keys
// of the counts map coverageAllLocked returns).
const (
	coverageCovered      = "covered"
	coverageNoWanted     = "no-wanted"
	coverageNone         = "none"
	coverageUnverifiable = "unverifiable"
)

// CoverageItem is one media item's coverage verdict.
type CoverageItem struct {
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	Class     string   `json:"class"`     // covered | no-wanted | none | unverifiable
	Languages []string `json:"languages"` // known subtitle languages present, ISO 639-1, sorted
	Notes     []string `json:"notes,omitempty"`
}

// CoverageReport is GET /api/coverage: the wanted list in force,
// the class counts over all covered items, the dismissed count,
// and the ATTENTION list — every item that is not covered (the
// wizard's Step F worklist). Covered items are counted but not
// listed: on a healthy library they are the overwhelming
// majority and listing them helps nobody.
type CoverageReport struct {
	Wanted       []string       `json:"wanted"`
	Covered      int            `json:"covered"`
	NoWanted     int            `json:"no_wanted"`
	None         int            `json:"none"`
	Unverifiable int            `json:"unverifiable"`
	Dismissed    int            `json:"dismissed"`
	Items        []CoverageItem `json:"items"`
}

// normalizeLangKey canonicalizes a language code for comparison:
// ISO 639-2/bibliographic codes and names map to ISO 639-1 via
// langCodeNormal ("eng" and "en" are the same key); an already
// canonical or unknown code passes through lowercased; "" stays
// "". "und" canonicalizes to "und" — a real key, distinct from
// "no language declared".
func normalizeLangKey(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	if v := langCodeNormal(code); v != "" {
		return v
	}
	return code
}

// sidecarStemOf returns a file's name without its final
// extension ("Film (2020).en.srt" -> "Film (2020).en").
func sidecarStemOf(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sidecarMatchesStems reports whether a sidecar file belongs to
// one of the given media stems in its folder, by the Organize
// association rule: the sidecar's stem is the media stem itself,
// or the media stem followed by a separator ('.', '-', '_',
// ' ') — the separator check is what stops "Film2.srt"
// attaching to "Film.mkv".
func sidecarMatchesStems(sidecarPath string, mediaStems map[string]bool) bool {
	stem := sidecarStemOf(sidecarPath)
	if mediaStems[stem] {
		return true
	}
	for ms := range mediaStems {
		if len(stem) > len(ms) && strings.HasPrefix(stem, ms) {
			switch stem[len(ms)] {
			case '.', '-', '_', ' ':
				return true
			}
		}
	}
	return false
}

// coverageAllLocked derives coverage for every eligible item in
// the current results. Returns the per-item verdicts (ALL
// classes, in results order), the class counts (dismissed items
// excluded), and how many items a coverage dismissal suppressed.
// Caller holds s.mu.
func (s *Service) coverageAllLocked() (items []CoverageItem, counts map[string]int, dismissed int) {
	counts = map[string]int{
		coverageCovered: 0, coverageNoWanted: 0,
		coverageNone: 0, coverageUnverifiable: 0,
	}
	items = []CoverageItem{}
	if s.results == nil {
		return items, counts, 0
	}
	wanted := s.cfg.wantedLangKeys()

	// External sidecar evidence, grouped by folder: media stem
	// set per folder (for association) and the declared language
	// of each sidecar file in it ("" = undeclared).
	stemsByDir := map[string]map[string]bool{}
	for _, it := range s.results.Items {
		if it == nil {
			continue
		}
		dir := filepath.Dir(it.Path)
		if stemsByDir[dir] == nil {
			stemsByDir[dir] = map[string]bool{}
		}
		stemsByDir[dir][strings.TrimSuffix(it.Name, filepath.Ext(it.Name))] = true
	}
	type sidecarEv struct {
		path string
		lang string // canonical key, "" when undeclared
	}
	sidecarsByDir := map[string][]sidecarEv{}
	for _, p := range s.results.SubtitleFilePaths {
		dir := filepath.Dir(p)
		sidecarsByDir[dir] = append(sidecarsByDir[dir], sidecarEv{path: p, lang: normalizeLangKey(declaredLangFromFilename(p))})
	}

	for _, it := range s.results.Items {
		if it == nil || it.IsSample || !it.ProbeOK {
			continue
		}
		entry, ok := s.store.Cache[it.Path]
		if !ok || entry.Probe == nil {
			continue // no probe evidence: unknown, not uncovered
		}
		textLangs := map[string]bool{}
		imageLangs := map[string]bool{}
		textUnknown, imageUnknown, forcedSeen := false, false, false
		for _, st := range entry.Probe.Streams {
			if st.Type != "subtitle" {
				continue
			}
			if strings.Contains(strings.ToLower(st.Title), "forced") {
				// A forced-only track is not coverage (see the
				// header); it is noted, never counted.
				forcedSeen = true
				continue
			}
			key := normalizeLangKey(st.Language)
			known := key != "" && key != "und"
			if imageSubtitleCodecs[st.Codec] {
				if known {
					imageLangs[key] = true
				} else {
					imageUnknown = true
				}
				continue
			}
			// Text (or unknown-codec) stream: language evidence.
			if known {
				textLangs[key] = true
			} else {
				textUnknown = true
			}
		}
		// External sidecars associated with this item by name.
		dir := filepath.Dir(it.Path)
		stem := strings.TrimSuffix(it.Name, filepath.Ext(it.Name))
		for _, sc := range sidecarsByDir[dir] {
			if !sidecarMatchesStems(sc.path, map[string]bool{stem: true}) {
				continue
			}
			if sc.lang != "" && sc.lang != "und" {
				textLangs[sc.lang] = true
			} else {
				textUnknown = true
			}
		}

		ci := CoverageItem{Path: it.Path, Title: it.Title, Kind: it.Kind}
		switch {
		case anyKeyIn(textLangs, wanted):
			ci.Class = coverageCovered
		case len(textLangs) > 0 || textUnknown:
			ci.Class = coverageNoWanted
		case len(imageLangs) > 0 || imageUnknown:
			ci.Class = coverageUnverifiable
		default:
			ci.Class = coverageNone
		}
		langs := map[string]bool{}
		for k := range textLangs {
			langs[k] = true
		}
		for k := range imageLangs {
			langs[k] = true
		}
		for k := range langs {
			ci.Languages = append(ci.Languages, k)
		}
		sort.Strings(ci.Languages)
		if ci.Languages == nil {
			ci.Languages = []string{}
		}
		if textUnknown && len(textLangs) == 0 && ci.Class == coverageNoWanted {
			ci.Notes = append(ci.Notes, "has subtitle track(s) with no declared language")
		}
		if forcedSeen && ci.Class != coverageCovered {
			ci.Notes = append(ci.Notes, "a forced-only track is present (not counted as coverage)")
		}
		if anyKeyIn(imageLangs, wanted) && ci.Class != coverageCovered {
			ci.Notes = append(ci.Notes, "wanted language present only as an image track — cannot be verified")
		}
		// Coverage dismissal ("Not wanted"): the item leaves the
		// list and the counts, and is reported in the dismissed
		// total instead.
		if s.coverageDismissedLocked(it.Path) {
			dismissed++
			continue
		}
		counts[ci.Class]++
		items = append(items, ci)
	}
	return items, counts, dismissed
}

// anyKeyIn reports whether any key of set is in wanted.
func anyKeyIn(set map[string]bool, wanted map[string]bool) bool {
	for k := range set {
		if wanted[k] {
			return true
		}
	}
	return false
}

// Coverage returns the coverage report for the current results
// (GET /api/coverage): counts over all eligible items, and the
// attention list (every item that is not covered).
func (s *Service) Coverage() CoverageReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, counts, dismissed := s.coverageAllLocked()
	attention := []CoverageItem{}
	for _, ci := range all {
		if ci.Class != coverageCovered {
			attention = append(attention, ci)
		}
	}
	wanted := append([]string{}, s.cfg.WantedLanguages...)
	if len(wanted) == 0 {
		wanted = []string{"eng"}
	}
	return CoverageReport{
		Wanted:       wanted,
		Covered:      counts[coverageCovered],
		NoWanted:     counts[coverageNoWanted],
		None:         counts[coverageNone],
		Unverifiable: counts[coverageUnverifiable],
		Dismissed:    dismissed,
		Items:        attention,
	}
}
