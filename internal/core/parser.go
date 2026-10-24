package core

// parser.go — release-name parsing: turn a media file path into a
// library identity (movie / episode), with release tags stripped.
//
// The parser is purpose-built (stdlib only, no guessit port). It
// handles the library's common shapes:
//
//	Movie Name (2020) 1080p BluRay x264-GROUP.mkv
//	Movie.Name.2020.720p.WEB-DL.x265.mkv
//	Show Name S01E02 ....mkv      Show.Name.1x02....mkv
//	Show/Season 01/<episode file> (folder context supplies series/season)
//
// The v0.3.5 expansion (from NAMING-PATTERNS.md, the naming-pattern
// catalog) added: extras classification BEFORE any episode/movie
// parsing (an extra must never become a movie or an episode
// identity), Specials-folder / SP## specials context, {...} tag
// stripping + {edition-…} capture, a movie year/title cleanup that
// stops eating numeric titles, one optional separator inside SxE,
// range tails, NxN parity, the completed split vocabulary, folder
// year for movies, fractional (NN.5) flag-only handling, and the
// folder-branch tokenizer/E-token repairs.
//
// The v0.4.0 round (from NAMING-RESEARCH-2.md, the TRaSH/Servarr
// round-2 survey) added: the series year harvested from the
// episode filename's pre-marker chunk, Servarr's Extend and
// Duplicate multi-episode styles, the daily/air-date harm guard,
// bracketed anime episode numbers, glued vN release versions on
// bare numbers, NCOP/NCED extras tokens, bracketed edition
// phrases, and the release-group tag guard.

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParsedName is the parser's output for one path.
type ParsedName struct {
	Kind       string
	Title      string // display title
	NormTitle  string // identity key title (lowercase, articles stripped)
	Year       int
	Season     int
	Episodes   []int
	Edition    string
	Tags       []string
	Resolution string // label from filename when probe has none
	Special    string // multi-episode | split-file | fractional
}

var (
	// reSxE: one optional separator between S and E (v0.3.5) —
	// "S01.E02", "S01 E02" and "S01-E02" are the same marker as
	// "S01E02". Group 3 is the E-chain tail ("-E02", "E02E03", …).
	reSxE = regexp.MustCompile(`(?i)S(\d{1,2})[ ._-]?E(\d{1,3})((?:-?E\d{1,3})*)`)
	// reNxN: case-insensitive since v0.3.5 ("1X02"). The \b anchors
	// and the 1–2 digit season cap stay: they are what stops
	// "1920x1080" (a resolution) from matching.
	reNxN   = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
	reYearP = regexp.MustCompile(`\((19|20)\d{2}\)`)
	reYear  = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	// reSeason: the season-word form allows a trailing suffix
	// after the number (v0.3.9) — "Season 04 - Arrival in
	// Alabasta, …" is a season folder; the digits must be
	// followed by a separator or the end of the name, and a
	// folder like "Season of the Witch" still does NOT match
	// (no digits follow "season"). The bare S## form stays
	// anchored to the whole name.
	reSeason = regexp.MustCompile(`(?i)^season[ ._-]*(\d{1,2})(?:[ ._-]|$)|^s(\d{1,2})$`)
	reEpOnly = regexp.MustCompile(`(?i)^E(\d{1,3})$`)
	// reBareE is the standalone bare-E marker (v0.3.9): a
	// separator-delimited "E33" token in a file with no other
	// marker. At least TWO digits are required — a lone "E5"
	// is too easily a title fragment to claim a file with.
	reBareE = regexp.MustCompile(`(?i)^E(\d{2,3})$`)
	// Word-form episode markers, consulted ONLY inside the
	// folder-season branch of parseName (v0.3.5): "Episode 39" and
	// "EP 07" / "ep7". They must never run on standalone movie
	// names — a film with "Episode" in its title is a movie.
	reEpWord = regexp.MustCompile(`(?i)\bepisode[ ._-]*(\d{1,3})\b`)
	reEpAbbr = regexp.MustCompile(`(?i)\bep[ ._-]?(\d{1,3})\b`)
	// reSP is the specials marker ("SP01", "SP 01"), consulted only
	// inside the folder-season branch: a hit forces Season 0.
	// v0.4.0: an optional glued release version ("SP01v2") is
	// consumed and discarded — the version is not part of identity.
	reSP = regexp.MustCompile(`(?i)\bsp[ ._-]?(\d{1,3})(?:v\d+)?\b`)
	// reDailyDate matches a full air date in the three documented
	// daily-naming shapes (YYYY-MM-DD, YYYY.MM.DD, YYYY MM DD —
	// NAMING-RESEARCH-2 §3). The month/day alternatives enforce
	// validity (month 01–12, day 01–31) inside the pattern itself.
	reDailyDate = regexp.MustCompile(`(19|20)\d{2}[-. ](0[1-9]|1[0-2])[-. ](0[1-9]|[12]\d|3[01])`)
	// reDupSxE matches one repeated full SxE marker at the start of
	// the text following an SxE match — Servarr's "Duplicate"
	// multi-episode style ("S01E01.S01E02", v0.4.0).
	reDupSxE = regexp.MustCompile(`^[._ -]?S(\d{1,2})[ ._-]?E(\d{1,3})`)
	// reExtendChain matches a glued bare-number chain at the start
	// of the text following an SxE match — Servarr's "Extend"
	// multi-episode style ("S01E01-02-03", v0.4.0).
	reExtendChain = regexp.MustCompile(`^(-\d{1,3}){2,8}`)
	// reVNNumber matches a bare number token with an optional
	// glued release version ("142", "01v2", "0103v2" — v0.4.0,
	// NAMING-RESEARCH-2 §8): group 1 is the number itself; the
	// version is discarded wherever it appears.
	reVNNumber = regexp.MustCompile(`(?i)^(\d{1,4})(?:v\d+)?$`)
	// reNCToken matches the anime creditless-OP/ED tokens NCOP /
	// NCED (optionally numbered) as delimited tokens (v0.4.0,
	// NAMING-RESEARCH-2 §9).
	reNCToken = regexp.MustCompile(`(?i)\bNC(?:OP|ED)\d{0,3}\b`)
	// reFractional matches the recap/half-episode form "12.5" in a
	// TV context. Fractional episodes are flag-only (v0.3.5): they
	// record NO episode number, so a recap can never merge into —
	// or duplicate-group with — the real episode NN.
	reFractional = regexp.MustCompile(`\b(\d{1,3})\.5\b`)
	reBrack      = regexp.MustCompile(`\[[^\]]*\]`)
	// reParen strips (...) blocks, the paren twin of reBrack:
	// quality/credit blocks like "(Dual Audio_10bit_BD1080p_x265)"
	// are release furniture, never title or number evidence.
	reParen = regexp.MustCompile(`\([^)]*\)`)
	// reBrace strips {...} blocks in cleanTokens exactly like
	// reBrack strips [...]: provider tags ({tvdb-73244}) and edition
	// braces must never leak tokens into a title.
	reBrace = regexp.MustCompile(`\{[^}]*\}`)
	// reEditionBrace captures the official {edition-…} payload
	// (Plex/TRaSH form) verbatim, e.g. {edition-Director's Cut}.
	reEditionBrace = regexp.MustCompile(`(?i)\{edition-([^}]+)\}`)
	reGroup        = regexp.MustCompile(`-([A-Za-z0-9]{2,})$`)
	// reSplit vocabulary completed in v0.3.5 (Plex/Jellyfin split
	// markers): cd, disc, disk, dvd, part, pt + a 1–2 digit number.
	reSplit = regexp.MustCompile(`(?i)\b(cd|disc|disk|dvd|part|pt)[ ._-]?(\d{1,2})\b`)
	// Tail scanners for the SxE / NxN branches (v0.3.5): the chain
	// rescan is case-insensitive (lowercase "-e02" tails used to be
	// captured by reSxE and then silently dropped), a single "-E03"
	// tail is a RANGE, and a bare "-03" tail is a guarded range.
	reTailE    = regexp.MustCompile(`(?i)E(\d{1,3})`)
	reTailOneE = regexp.MustCompile(`(?i)^-E(\d{1,3})$`)
	reBareTail = regexp.MustCompile(`^-(\d{1,3})\b`)
	reNxNTail  = regexp.MustCompile(`(?i)^-?(\d{1,2})x(\d{1,3})\b`)
)

// fractionalIsEpisodeClaim reports whether a "N.5" match in the stem
// is the stem's episode claim (a recap/half-episode marker) rather
// than title text. v1.3.1: the decimal is an episode claim only
// when nothing else in the stem claims an episode — an explicit
// E/SP/Episode/EP token, or a strong (leading-zero 4-digit)
// absolute number. "Show - 12.5" has no other claim, so it stays
// fractional; "One Piece - 0438 - … Level 5.5" has the strong
// claim 0438, so the "5.5" is title text and the cascade keeps
// episode 438. (The SxE branch does not use this helper: there
// the decimal must attach directly to the episode token, which
// the ".5"-plus-non-digit test at the SxE site enforces.)
func fractionalIsEpisodeClaim(stem string) bool {
	if !reFractional.MatchString(stem) {
		return false
	}
	split := func(s string) []string {
		return strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(s))
	}
	for _, tok := range split(stem) {
		if reEpOnly.FindStringSubmatch(tok) != nil {
			return false
		}
	}
	if reSP.FindStringSubmatch(stem) != nil || reEpWord.FindStringSubmatch(stem) != nil || reEpAbbr.FindStringSubmatch(stem) != nil {
		return false
	}
	// Strong absolute tokens in the bracket/paren-stripped view
	// (the same view the bare-number cascades search).
	for _, tok := range split(reParen.ReplaceAllString(reBrack.ReplaceAllString(stem, " "), " ")) {
		if digits, ok := bareNumberParts(tok); ok {
			if _, strong := strongEpisodeNumber(digits); strong {
				return false
			}
		}
	}
	return true
}

// tagTokens are filename tokens that are release metadata, not title.
var tagTokens = map[string]bool{
	"1080p": true, "720p": true, "2160p": true, "480p": true, "576p": true, "360p": true,
	"4k": true, "uhd": true, "hdr": true, "hdr10": true, "dv": true, "dolby": true, "vision": true,
	"bluray": true, "blu": true, "ray": true, "bdrip": true, "brrip": true, "remux": true,
	"web": true, "dl": true, "webdl": true, "webrip": true, "hdtv": true, "dvdrip": true, "dvd": true,
	"hdrip": true, "cam": true, "ts": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true, "avc": true,
	"xvid": true, "divx": true, "av1": true, "vp9": true,
	"aac": true, "ac3": true, "eac3": true, "dts": true, "atmos": true, "truehd": true,
	"mp3": true, "flac": true, "opus": true, "dd": true, "ddp": true, "5": true, "7": true, "1": true, "2": true, "0": true,
	"10bit": true, "8bit": true, "proper": true, "repack": true, "internal": true, "limited": true,
	"extended": true, "theatrical": true, "unrated": true, "imax": true, "remastered": true,
	"complete": true, "pack": true,
}

// editions maps lowercase phrases to canonical edition labels. Order
// matters: multi-word phrases are checked on the raw name first.
var editions = []struct{ phrase, label string }{
	{"director's cut", "Directors Cut"}, {"directors cut", "Directors Cut"}, {"director cut", "Directors Cut"},
	{"extended cut", "Extended"}, {"extended", "Extended"},
	{"theatrical", "Theatrical"}, {"unrated", "Unrated"}, {"imax", "IMAX"},
	{"remastered", "Remastered"}, {"ultimate cut", "Ultimate Cut"}, {"final cut", "Final Cut"},
}

// detectEdition returns the canonical edition label in a raw name, or "".
func detectEdition(raw string) string {
	low := " " + strings.ToLower(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(raw)) + " "
	for _, e := range editions {
		// v0.4.0: a phrase closed by "]" or "}" also matches —
		// the bracketed Jellyfin form ("[Director's Cut]",
		// "[Extended]") previously fell through because the scan
		// only accepted a following space or "(" (the {edition-…}
		// payload case is intercepted before this scan runs).
		if strings.Contains(low, " "+e.phrase+" ") || strings.Contains(low, "("+e.phrase) ||
			strings.Contains(low, e.phrase+"]") || strings.Contains(low, e.phrase+"}") {
			return e.label
		}
	}
	return ""
}

// extraSuffixes are the Plex/Jellyfin trailing-token extras markers
// (v0.3.5): a stem ENDING in one of these hyphen-attached tokens is
// an extra, whatever the rest of the name parses as. Longest first,
// so "-deletedscene" wins over "-deleted" and "-scene". The token
// must be the exact trailing token — titles CONTAIN "trailer" and
// "scene" all the time; only the attached suffix is the convention.
var extraSuffixes = []string{
	"-behindthescenes", "-deletedscene", "-featurette", "-interview",
	"-trailer", "-deleted", "-sample", "-short", "-scene",
	"-other", "-clip", "-extra",
}

// extraFolderNames are the folder names that mark everything under
// them as extras (exact, case-insensitive).
var extraFolderNames = map[string]bool{
	"extras": true, "extra": true, "featurettes": true,
	"deleted scenes": true, "behind the scenes": true,
	"trailers": true, "interviews": true, "shorts": true,
	"bonus": true, "bonus features": true, "special features": true,
}

// extrasTokens are the words that mark extras content when they
// appear as whole TOKENS in a pack folder name or a stem (v0.3.9).
var extrasTokens = map[string]bool{
	"extra": true, "extras": true, "featurette": true,
	"featurettes": true, "trailers": true, "bonus": true,
}

// reBareSeasonToken matches a bare season token ("S07", "s01") as
// a whole token — deliberately NOT a full SxE marker token like
// "s01e05", which is one different token.
var reBareSeasonToken = regexp.MustCompile(`(?i)^s\d{1,2}$`)

// stripParenFurniture removes (...) blocks whose contents carry
// release/range furniture — a digit token or a known release-tag
// token ("(0001-1071+Movies+Specials)", "(BD 720p)", "(2000)") —
// from a folder name before title cleaning (v0.3.9). A paren
// block of plain title words survives untouched: "(US)" in
// "The Office (US)" is part of the series' name, not furniture,
// and cleanTokens handles it exactly as before.
func stripParenFurniture(s string) string {
	return reParen.ReplaceAllStringFunc(s, func(block string) string {
		inner := strings.Trim(block, "()")
		for _, tok := range looseTokens(inner) {
			if isAllDigits(tok) || tagTokens[tok] {
				return " "
			}
		}
		return block
	})
}

// looseTokens tokenizes a name chunk the pack-layout way (v0.3.9):
// bracket and paren block CONTENTS are dropped first (release
// furniture), then the rest is split on the usual separators and
// lowercased.
func looseTokens(s string) []string {
	s = reParen.ReplaceAllString(reBrack.ReplaceAllString(s, " "), " ")
	s = strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ").Replace(s)
	var out []string
	for _, tok := range strings.Fields(s) {
		out = append(out, strings.ToLower(tok))
	}
	return out
}

// extraByPackFolder is the v0.3.9 folder-token extras rule: a dir
// part whose tokens carry an extras word AND a season token (or
// the word "season", or a known release-tag token) is a PACK
// extras folder — e.g. "Show.S01.EXTRAS.BDRip.x264-GROUP" or
// "Show.S07.Extras.DVDRip.x264-W4F". The second condition is the
// guard: a plain series folder named "Extras (2005)" carries
// neither, so a series that happens to be CALLED Extras is safe.
func extraByPackFolder(dirParts []string) bool {
	for _, part := range dirParts {
		toks := looseTokens(part)
		hasExtras, hasContext := false, false
		for _, tok := range toks {
			if extrasTokens[tok] {
				hasExtras = true
			}
			if tok == "season" || reBareSeasonToken.MatchString(tok) || tagTokens[tok] {
				hasContext = true
			}
		}
		if hasExtras && hasContext {
			return true
		}
	}
	return false
}

// extraByStemTokens is the v0.3.9 stem-token extras rule: the
// stem's own tokens carry "extra"/"extras" AND a bare season
// token ("adventure.time.s07.extra-animatics-…"). A real marker
// token like "s01e05" is a single different token and never
// trips this rule.
func extraByStemTokens(stem string) bool {
	hasExtras, hasSeason := false, false
	for _, tok := range looseTokens(stem) {
		if tok == "extra" || tok == "extras" {
			hasExtras = true
		}
		if reBareSeasonToken.MatchString(tok) {
			hasSeason = true
		}
	}
	return hasExtras && hasSeason
}

// classifyExtra reports whether a file is a Plex/Jellyfin "extra"
// (trailer, featurette, deleted scene, …) and, if so, its display
// title: the cleaned stem with the extras suffix removed. Extras
// are classified BEFORE any episode/movie parsing (v0.3.5): a movie
// extra must not become a second "movie", and an episode extra —
// whose basename by Plex convention STARTS with the episode code —
// must not parse AS that episode and fake a duplicate identity.
func classifyExtra(stem string, dirParts []string) (title string, isExtra bool) {
	titleStem := stem
	low := strings.ToLower(stem)
	matched := false
	for _, suf := range extraSuffixes {
		if strings.HasSuffix(low, suf) {
			titleStem = stem[:len(stem)-len(suf)]
			matched = true
			break
		}
	}
	if !matched {
		for _, part := range dirParts {
			if extraFolderNames[strings.ToLower(strings.TrimSpace(part))] {
				matched = true
				break
			}
		}
	}
	if !matched {
		// v0.3.9: pack-layout extras (an EXTRAS folder inside a
		// season pack, "s07.extra-…" stems) carry neither an
		// extras SUFFIX nor an exactly-named extras folder.
		matched = extraByPackFolder(dirParts) || extraByStemTokens(stem)
	}
	if !matched && reNCToken.MatchString(stem) {
		// v0.4.0 (NAMING-RESEARCH-2 §9): NCOP/NCED tokens mark
		// anime creditless opening/ending clips — definitionally
		// extras, and the tokens never occur inside real titles.
		// Only NCOP/NCED: OP/ED/OVA/OAD/Menu stay unclassified
		// (ambiguous or numbering-inventing, per the memo). The
		// token is removed from the display title.
		matched = true
		titleStem = reNCToken.ReplaceAllString(stem, " ")
	}
	if !matched {
		return "", false
	}
	var tags []string
	return strings.Join(cleanTokens(titleStem, &tags), " "), true
}

// cleanTokens splits a name chunk into words, dropping release tags,
// years and pure punctuation, preserving display casing. Years are
// dropped (episode series names, folder titles); the MOVIE title
// path uses cleanTokensMode with dropYears=false — see there.
func cleanTokens(chunk string, tags *[]string) []string {
	return cleanTokensMode(chunk, tags, true)
}

// cleanTokensMode is cleanTokens with control over year dropping.
// With dropYears=false the caller has already cut the chunk at the
// ONE chosen year occurrence (by byte index), so every other
// year-like token in the chunk belongs to the title and survives:
// "1917 (2019)" → "1917", "2001 A Space Odyssey (1968)" → the full
// title, "Blade Runner 2049 (2017)" → "Blade Runner 2049".
//
// Two token rules apply in both modes:
//   - {...} blocks are stripped exactly like [...] blocks (provider
//     and edition tags must not pollute titles).
//   - A lone-digit token from the audio set {0,1,2,5,7} (the "DD
//     5.1" split) is dropped ONLY when an adjacent token is also a
//     lone digit — i.e. it really is one half of a digit pair. A
//     lone digit with word neighbours is part of the title and
//     survives: "Fast 5" stays "Fast 5".
func cleanTokensMode(chunk string, tags *[]string, dropYears bool) []string {
	chunk = reBrack.ReplaceAllString(chunk, " ")
	chunk = reBrace.ReplaceAllString(chunk, " ")
	chunk = strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ").Replace(chunk)
	fields := strings.Fields(chunk)
	var out []string
	for i, tok := range fields {
		low := strings.ToLower(tok)
		if dropYears && reYear.MatchString(tok) {
			continue
		}
		if tagTokens[low] {
			if isAudioDigit(low) && !adjacentLoneDigit(fields, i) {
				out = append(out, tok) // a title digit, not an audio token
				continue
			}
			*tags = appendUnique(*tags, low)
			continue
		}
		out = append(out, tok)
	}
	return out
}

// isAudioDigit reports whether a lowercase token is one of the lone
// digits the tag set carries for split audio specs ("DD 5.1").
// Digits outside this set (3, 4, 6, 8, 9, and every multi-digit
// token) were never treated as tags and always survive.
func isAudioDigit(low string) bool {
	switch low {
	case "0", "1", "2", "5", "7":
		return true
	}
	return false
}

// adjacentLoneDigit reports whether the token at index i in fields
// has a single-digit neighbour on either side — the shape of a
// split digit pair like "5 1" (from "5.1").
func adjacentLoneDigit(fields []string, i int) bool {
	isLone := func(s string) bool { return len(s) == 1 && s[0] >= '0' && s[0] <= '9' }
	if i > 0 && isLone(fields[i-1]) {
		return true
	}
	if i+1 < len(fields) && isLone(fields[i+1]) {
		return true
	}
	return false
}

// isAllDigits reports whether s is non-empty and all ASCII digits
// (strconv.Atoi alone would also accept a leading + or -).
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// appendUnique appends s to list when not already present.
func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// appendUniqueInt appends n to list when not already present.
func appendUniqueInt(list []int, n int) []int {
	for _, x := range list {
		if x == n {
			return list
		}
	}
	return append(list, n)
}

// expandRange returns the episodes first+1 … last for a range tail
// ("S01E01-E03" covers 1, 2, 3), or nil when the tail is not a sane
// forward range (last ≤ first, or a span over 100 — no real episode
// range is that wide; a huge span means the number was something
// else).
func expandRange(first, last int) []int {
	if last <= first || last-first > 100 {
		return nil
	}
	out := make([]int, 0, last-first)
	for e := first + 1; e <= last; e++ {
		out = append(out, e)
	}
	return out
}

// bareTailOK guards a bare "-NN" range tail ("S01E01-03", "1x01-03"):
// the value must move FORWARD from the first episode and must not be
// a resolution value (480/720/1080/2160 — the four-digit ones cannot
// match the 1–3 digit tail anyway, but the guard names them all).
func bareTailOK(first, n int) bool {
	if n <= first {
		return false
	}
	switch n {
	case 480, 720, 1080, 2160:
		return false
	}
	return true
}

// normalizeTitle builds the identity form of a title: lowercase,
// punctuation removed, leading article stripped, spaces collapsed.
func normalizeTitle(t string) string {
	t = strings.ToLower(t)
	var b strings.Builder
	for _, r := range t {
		if isLetter(r) || (r >= '0' && r <= '9') {
			b.WriteRune(lowerRune(r))
		} else {
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	if len(fields) > 1 {
		switch fields[0] {
		case "the", "a", "an":
			fields = fields[1:]
		}
	}
	return strings.Join(fields, " ")
}

// resolutionFromName returns a resolution label carried in the name.
func resolutionFromName(raw string) string {
	low := strings.ToLower(raw)
	for _, r := range []string{"2160p", "1080p", "720p", "576p", "480p"} {
		if strings.Contains(low, r) {
			return r
		}
	}
	if strings.Contains(low, "4k") || strings.Contains(low, "uhd") {
		return "2160p"
	}
	return ""
}

// parseName parses one media path. root is the scan root the path came
// from (folder context is taken relative to it).
func parseName(path, root string) ParsedName {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	// Folder context relative to the root.
	relDir := ""
	if root != "" {
		if rel, err := filepath.Rel(root, filepath.Dir(path)); err == nil && rel != "." {
			relDir = rel
		}
	}
	var dirParts []string
	if relDir != "" {
		dirParts = strings.Split(relDir, string(filepath.Separator))
	}
	// Folder-season detection (v0.3.5 restructure): hasFolderSeason
	// records that a season context EXISTS even when the season
	// number is 0 — "Season 00" / "S00" folders and a "Specials"
	// folder are season-0 (specials) context, and the old
	// folderSeason > 0 gate meant they never activated TV parsing.
	folderSeason := 0
	hasFolderSeason := false
	folderYear := 0
	var folderSeries string
	for i, part := range dirParts {
		name := strings.TrimSpace(part)
		season, isSeason := 0, false
		if m := reSeason.FindStringSubmatch(name); m != nil {
			n := m[1]
			if n == "" {
				n = m[2]
			}
			season, _ = strconv.Atoi(n)
			isSeason = true
		} else if strings.EqualFold(name, "specials") {
			season, isSeason = 0, true
		}
		if !isSeason {
			continue
		}
		hasFolderSeason = true
		folderSeason = season
		if i > 0 {
			// The series folder is the one above the season
			// folder — unless that folder is itself season-like
			// (nested "Season 01/Specials"), in which case look
			// one level higher rather than naming the series
			// after a season folder.
			cand := dirParts[i-1]
			if reSeason.MatchString(strings.TrimSpace(cand)) || strings.EqualFold(strings.TrimSpace(cand), "specials") {
				cand = ""
				if i > 1 {
					cand = dirParts[i-2]
				}
			}
			if cand != "" {
				var tags []string
				// v0.3.9: paren furniture is stripped for
				// the title — pack folders carry release/
				// range blocks there ("(0001-1071+Movies+
				// Specials)", "(BD 720p)") that cleanTokens
				// would otherwise leak in as title words.
				// Plain-word paren blocks ("(US)") survive
				// (see stripParenFurniture). The folder
				// YEAR below still reads the raw folder
				// name, exactly as before.
				t := cleanTokens(stripParenFurniture(cand), &tags)
				folderSeries = strings.Join(t, " ")
				// Folder may carry the year: "Show Name (2019)".
				if ym := reYear.FindString(cand); ym != "" {
					folderYear, _ = strconv.Atoi(ym)
				}
			}
		}
	}

	// EXTRAS FIRST (v0.3.5): before any episode/movie parsing, a
	// file that is an extra by suffix or by folder is an extra —
	// Kind "extra", cleaned title, every other field zero. It can
	// never form a duplicate identity (the grouper only admits
	// movie/episode kinds) and Organize leaves it in place.
	if title, isExtra := classifyExtra(stem, dirParts); isExtra {
		return ParsedName{Kind: "extra", Title: title, NormTitle: normalizeTitle(title)}
	}

	p := ParsedName{Resolution: resolutionFromName(stem)}
	p.Year = folderYear
	// Edition: the {edition-…} brace payload wins verbatim when
	// present (the official Plex/TRaSH form — the phrase scan in
	// detectEdition cannot see it: the phrase is followed by "}",
	// never by the space or "(" it looks for); else the phrase scan.
	if m := reEditionBrace.FindStringSubmatch(stem); m != nil {
		p.Edition = strings.TrimSpace(m[1])
	} else {
		p.Edition = detectEdition(stem)
	}
	// Scan the whole stem for release tags (they usually sit after the
	// year/episode marker, outside the title chunk parsed below).
	for _, tok := range strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ").Replace(stem)) {
		if low := strings.ToLower(tok); tagTokens[low] {
			p.Tags = appendUnique(p.Tags, low)
		}
	}

	work := stem
	seasonLocked := false // an SP## hit pins Season 0 against the folder fixup below
	// DAILY HARM GUARD (v0.4.0): no SxE/NxN marker matched, but
	// the stem carries a full valid air date in episode position
	// — park the file as unknown BEFORE the folder-season branch
	// can invent an episode number from the date's tokens and
	// before the movie fallback can adopt the date's year (see
	// dailyGuardTitle). This is not daily support.
	if reSxE.FindStringIndex(work) == nil && reNxN.FindStringIndex(work) == nil {
		if title, ok := dailyGuardTitle(work); ok {
			p.Kind = "unknown"
			p.Title = title
			p.NormTitle = normalizeTitle(title)
			collectGroupTag(stem, &p)
			return p
		}
	}
	// Episode patterns first.
	if loc := reSxE.FindStringSubmatchIndex(work); loc != nil {
		p.Kind = "episode"
		p.Season, _ = strconv.Atoi(work[loc[2]:loc[3]])
		ep, _ := strconv.Atoi(work[loc[4]:loc[5]])
		p.Episodes = []int{ep}
		// Fractional (v0.3.5, narrowed v1.3.1): episode digits
		// immediately followed by ".5" AND a non-digit (or the
		// end of the stem) — "S01E12.5" is a recap/half
		// episode. Flag-only: NO episode number is recorded,
		// so it can never merge into episode 12 or group with
		// it. "S22E21.500.Keys" is NOT fractional: the ".5"
		// there opens the title token "500", so episode 21
		// stands and the title is "500 Keys".
		if rest := work[loc[5]:]; strings.HasPrefix(rest, ".5") && (len(rest) == 2 || rest[2] < '0' || rest[2] > '9') {
			p.Episodes = nil
			p.Special = "fractional"
		} else if tail := work[loc[6]:loc[7]]; tail != "" {
			if tm := reTailOneE.FindStringSubmatch(tail); tm != nil {
				// Single "-E<last>" tail: a RANGE, expanded to
				// every episode first..last (falling back to
				// just the endpoint when the span is not a sane
				// forward range).
				last, _ := strconv.Atoi(tm[1])
				if rng := expandRange(ep, last); rng != nil {
					p.Episodes = append(p.Episodes, rng...)
				} else {
					p.Episodes = append(p.Episodes, last)
				}
			} else {
				// Chained tail ("E02E03", "-E02-E03"): enumerate.
				for _, em := range reTailE.FindAllStringSubmatch(tail, -1) {
					e2, _ := strconv.Atoi(em[1])
					p.Episodes = append(p.Episodes, e2)
				}
			}
		} else {
			rest := work[loc[1]:]
			// v0.4.0 "Duplicate" style ("S01E01.S01E02"):
			// repeated full markers with the SAME season
			// append their episodes. A different season stops
			// the scan — it starts some other name segment.
			for {
				m := reDupSxE.FindStringSubmatchIndex(rest)
				if m == nil {
					break
				}
				s2, _ := strconv.Atoi(rest[m[2]:m[3]])
				if s2 != p.Season {
					break
				}
				e2, _ := strconv.Atoi(rest[m[4]:m[5]])
				p.Episodes = appendUniqueInt(p.Episodes, e2)
				rest = rest[m[1]:]
			}
			if len(p.Episodes) == 1 {
				// v0.4.0 "Extend" style ("S01E01-02-03"): a
				// glued bare-number chain ENUMERATES; a single
				// bare tail stays the guarded RANGE it has
				// been since v0.3.5 ("S01E01-03" → 1, 2, 3).
				if chain := extendChain(rest, ep); chain != nil {
					p.Episodes = append(p.Episodes, chain...)
				} else if bm := reBareTail.FindStringSubmatch(rest); bm != nil {
					last, _ := strconv.Atoi(bm[1])
					if bareTailOK(ep, last) {
						p.Episodes = append(p.Episodes, expandRange(ep, last)...)
					}
				}
			}
		}
		var tags []string
		titleToks := cleanTokens(work[:loc[0]], &tags)
		p.Tags = append(p.Tags, tags...)
		p.Title = strings.Join(titleToks, " ")
		// v0.4.0 (research §1): the series year rides in the
		// pre-marker chunk of TRaSH/Servarr standard names. A
		// folder year (already in p.Year) always wins — the
		// filename year only fills a gap, never overrides.
		if p.Year == 0 {
			if y := episodeYearFromChunk(work[:loc[0]]); y > 0 {
				p.Year = y
			}
		}
	} else if loc := reNxN.FindStringSubmatchIndex(work); loc != nil {
		p.Kind = "episode"
		p.Season, _ = strconv.Atoi(work[loc[2]:loc[3]])
		ep, _ := strconv.Atoi(work[loc[4]:loc[5]])
		p.Episodes = []int{ep}
		// Continuation parity with SxE (v0.3.5): chained same-season
		// markers ("1x01-1x02") append their episodes; a trailing
		// bare "-<last>" is a guarded range ("1x01-03" → 1, 2, 3).
		rest := work[loc[1]:]
		for {
			tm := reNxNTail.FindStringSubmatch(rest)
			if tm == nil {
				break
			}
			s2, _ := strconv.Atoi(tm[1])
			if s2 != p.Season {
				break
			}
			e2, _ := strconv.Atoi(tm[2])
			p.Episodes = append(p.Episodes, e2)
			ti := reNxNTail.FindStringIndex(rest)
			rest = rest[ti[1]:]
		}
		if len(p.Episodes) == 1 {
			if bm := reBareTail.FindStringSubmatch(rest); bm != nil {
				last, _ := strconv.Atoi(bm[1])
				if bareTailOK(ep, last) {
					p.Episodes = append(p.Episodes, expandRange(ep, last)...)
				}
			}
		}
		var tags []string
		p.Title = strings.Join(cleanTokens(work[:loc[0]], &tags), " ")
		p.Tags = append(p.Tags, tags...)
		// v0.4.0 (research §1): same series-year harvest as the
		// SxE branch — pre-marker chunk only, folder year wins.
		if p.Year == 0 {
			if y := episodeYearFromChunk(work[:loc[0]]); y > 0 {
				p.Year = y
			}
		}
	} else if hasFolderSeason {
		// Folder says TV: find the episode number in the stem.
		// Extraction precedence (v0.3.5) — the first form that
		// yields a number wins:
		//   (a) bare E-tokens ("E05") — the original pattern, now
		//       collecting ALL matches (deduped), not just the last;
		//   (b) the SP form ("SP01") — a special: Season forced to 0;
		//   (c) the word form "Episode 39";
		//   (d) the abbreviation "EP 07" / "ep7";
		//   (e) the first bare-number token ("39", "- 12 -") that
		//       is not a year and not a resolution value.
		// Before v0.3.5 only (a) existed, so a season pack named
		// "... - Episode 39 ..." parsed with NO episode number at
		// all — and the duplicates grouper (identity = series +
		// season + episodes) then collapsed the whole season
		// into one false duplicate group. These forms are only
		// tried inside this folder-season branch: a standalone
		// movie filename keeps its existing behavior even when
		// its title contains "Episode" or a bare number. A file
		// in a specials context still needs one of these tokens —
		// a tokenless file stays unidentified, as before.
		p.Kind = "episode"
		p.Season = folderSeason
		if fractionalIsEpisodeClaim(work) {
			// Fractional in folder context ("Show - 12.5"):
			// flag-only, no episode number (see reFractional).
			// Checked BEFORE the cascade: the tokenizer would
			// otherwise split "12.5" into "12" and hand episode
			// 12 to the bare-number rule. v1.3.1: only when
			// the decimal IS the episode claim — a stem with
			// a stronger claim ("One Piece - 0438 - …
			// Level 5.5") falls through to the cascade, which
			// keeps episode 438.
			p.Special = "fractional"
		} else {
			// The tokenizer also splits on "-" (v0.3.5): glued
			// forms like "Show-E02" tokenize to an E-token.
			toks := strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(work))
			for _, tok := range toks {
				if m := reEpOnly.FindStringSubmatch(tok); m != nil {
					ep, _ := strconv.Atoi(m[1])
					p.Episodes = appendUniqueInt(p.Episodes, ep)
				}
			}
			if len(p.Episodes) == 0 {
				if m := reSP.FindStringSubmatch(work); m != nil {
					ep, _ := strconv.Atoi(m[1])
					p.Episodes = []int{ep}
					p.Season = 0
					seasonLocked = true
				}
			}
			if len(p.Episodes) == 0 {
				if m := reEpWord.FindStringSubmatch(work); m != nil {
					ep, _ := strconv.Atoi(m[1])
					p.Episodes = []int{ep}
				}
			}
			if len(p.Episodes) == 0 {
				if m := reEpAbbr.FindStringSubmatch(work); m != nil {
					ep, _ := strconv.Atoi(m[1])
					p.Episodes = []int{ep}
				}
			}
			if len(p.Episodes) == 0 {
				// v0.3.9 STRONG candidates first: a 4-digit
				// leading-zero token ("0103") is an absolute
				// episode number wherever it sits — it wins
				// over every weak 1–3 digit token (the "8"
				// in "8 o'Clock" is exactly the trap the
				// weak rule fell into on the One Piece pack).
				// v0.4.0: tokens are read through
				// bareNumberParts, so a glued release
				// version ("0103v2", "01v2") yields its
				// number here too.
				for _, tok := range toks {
					digits, ok := bareNumberParts(tok)
					if !ok {
						continue
					}
					if n, strong := strongEpisodeNumber(digits); strong {
						p.Episodes = []int{n}
						break
					}
				}
			}
			if len(p.Episodes) == 0 {
				for _, tok := range toks {
					digits, ok := bareNumberParts(tok)
					if !ok || len(digits) > 3 {
						continue
					}
					n, _ := strconv.Atoi(digits)
					if n >= 1900 && n <= 2099 {
						continue // a year, not an episode
					}
					if digits == "480" || digits == "720" || digits == "1080" || digits == "2160" {
						continue // a resolution value, not an episode
					}
					p.Episodes = []int{n}
					break
				}
			}
		}
		if folderSeries != "" {
			p.Title = folderSeries
		}
	}

	// STANDALONE BARE-E MARKER (v0.3.9): no season folder, no
	// SxE/NxN marker — but the stem carries a separator-delimited
	// "E33" token ("Digimon.Beatbreak.E33.1080p.…"). Bounded
	// hard: at least two digits (reBareE), non-empty cleaned
	// title text BEFORE the marker, and NO year token anywhere
	// in the stem — a year in play means this is a movie's
	// business, the same veto spirit as seriesFolderAbsolute.
	// Tried before the absolute-numbering branch: an explicit
	// E-marker outranks a guessed bare number.
	if p.Kind == "" {
		toks := strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ").Replace(reBrack.ReplaceAllString(stem, " ")))
		yearSeen := false
		for _, tok := range toks {
			if isYearToken(tok) {
				yearSeen = true
				break
			}
		}
		if !yearSeen {
			for i, tok := range toks {
				m := reBareE.FindStringSubmatch(tok)
				if m == nil {
					continue
				}
				var tags []string
				cleaned := cleanTokens(strings.Join(toks[:i], " "), &tags)
				if len(cleaned) == 0 {
					break
				}
				ep, _ := strconv.Atoi(m[1])
				p.Kind = "episode"
				p.Season = 1
				p.Episodes = []int{ep}
				p.Title = strings.Join(cleaned, " ")
				p.Tags = append(p.Tags, tags...)
				break
			}
		}
	}

	// SERIES-FOLDER ABSOLUTE NUMBERING (v0.3.7): no season
	// folder, no SxE/NxN marker — but the file sits in a series
	// folder and its stem carries a bare episode number after
	// the title text ("Series_-_11_..."). Without this step the
	// file fell to the movie fallback and the whole pack shared
	// one yearless title-only identity (see seriesFolderAbsolute
	// for the conditions). Runs only when nothing above claimed
	// the file.
	if p.Kind == "" {
		if title, ep, ok := seriesFolderAbsolute(stem, dirParts); ok {
			p.Kind = "episode"
			p.Title = title
			p.Season = 1
			p.Episodes = []int{ep}
			// v0.4.0 (research §1): a paren year that survived
			// seriesFolderAbsolute's vetoes sits BEFORE the
			// episode candidate — the series' own year tag
			// ("Series x Title (2011) - 142"). A folder year
			// (already in p.Year) wins; this only fills a gap.
			if p.Year == 0 {
				if m := reYearP.FindStringSubmatch(stem); m != nil {
					p.Year, _ = strconv.Atoi(strings.Trim(m[0], "()"))
				}
			}
			if reSplit.MatchString(stem) && p.Special == "" {
				p.Special = "split-file"
			}
			p.NormTitle = normalizeTitle(p.Title)
			collectGroupTag(stem, &p)
			return p
		}
	}

	if p.Kind == "episode" {
		if p.Title == "" && folderSeries != "" {
			p.Title = folderSeries
		}
		if p.Title == "" && len(dirParts) > 0 {
			// v0.3.9: no season folder, so no folderSeries —
			// but the file's own folder may still BE the
			// series folder ("FLCL (2000) […] [Prof]" holding
			// "[Prof] S01E01 - ….mkv", whose filename title
			// cleans to empty). Derive the title from the
			// LAST dir part with bracket contents and paren
			// furniture stripped before cleaning; a lone
			// year token in that folder is the series'
			// year when the parse has none.
			folder := dirParts[len(dirParts)-1]
			var ftags []string
			ft := cleanTokens(stripParenFurniture(reBrack.ReplaceAllString(folder, " ")), &ftags)
			if len(ft) > 0 {
				p.Title = strings.Join(ft, " ")
				p.Tags = append(p.Tags, ftags...)
			}
			if p.Year == 0 {
				if ys := reYear.FindAllString(folder, -1); len(ys) == 1 {
					p.Year, _ = strconv.Atoi(ys[0])
				}
			}
		}
		if p.Season == 0 && folderSeason > 0 && !seasonLocked {
			p.Season = folderSeason
		}
		if len(p.Episodes) > 1 && p.Special == "" {
			p.Special = "multi-episode"
		}
		// A split marker never overwrites an existing Special
		// (v0.3.5): "multi-episode" and "fractional" outrank it.
		if reSplit.MatchString(stem) && p.Special == "" {
			p.Special = "split-file"
		}
		p.NormTitle = normalizeTitle(p.Title)
		collectGroupTag(stem, &p)
		return p
	}

	// Movie (or unknown).
	// Split marker (v0.3.5): with the completed vocabulary, a split
	// match in the stem is also REMOVED from the title chunk, so
	// "Movie.CD1.1998" is the film "Movie" (part 1) and its CD1/CD2
	// — or pt1/pt2 — copies share one title and identity.
	workStem := stem
	if loc := reSplit.FindStringIndex(workStem); loc != nil {
		p.Special = "split-file"
		workStem = workStem[:loc[0]] + " " + workStem[loc[1]:]
	}
	// Year choice: the first PARENTHESIZED year wins; the bare-year
	// fallback takes the LAST PLAUSIBLE bare year. The title is cut
	// at the chosen match's byte index — never by searching for the
	// digit string (the same digits can appear inside the title).
	yearStr := ""
	yearCut := -1
	if loc := reYearP.FindStringIndex(workStem); loc != nil {
		yearStr = strings.Trim(workStem[loc[0]:loc[1]], "()")
		yearCut = loc[0]
	} else {
		// Plausibility bound for bare years: next year. A bare
		// far-future number ("Blade Runner 2049") is part of a
		// title far more often than it is a release year; with no
		// plausible bare year the film simply has Year 0 here
		// (the folder-year rule below may still supply one).
		maxYear := time.Now().Year() + 1
		for _, loc := range reYear.FindAllStringIndex(workStem, -1) {
			if y, err := strconv.Atoi(workStem[loc[0]:loc[1]]); err == nil && y <= maxYear {
				yearStr = workStem[loc[0]:loc[1]]
				yearCut = loc[0]
			}
		}
	}
	if yearStr != "" {
		p.Year, _ = strconv.Atoi(yearStr)
	}
	// Title = text before the chosen year, else before the first
	// tag token.
	chunk := workStem
	if yearCut >= 0 {
		chunk = workStem[:yearCut]
	} else {
		// Cut at the first dotted/spaced release tag.
		low := strings.ToLower(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(workStem))
		fields := strings.Fields(low)
		origFields := strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(workStem))
		cut := len(fields)
		for i, f := range fields {
			if tagTokens[f] {
				cut = i
				break
			}
		}
		if cut < len(origFields) {
			chunk = strings.Join(origFields[:cut], " ")
		}
	}
	var tags []string
	// dropYears=false: the chunk was cut at the ONE chosen year;
	// any other year-like token in it is title text ("1917",
	// "2001", "2049") and must survive cleaning.
	titleToks := cleanTokensMode(chunk, &tags, false)
	p.Tags = append(p.Tags, tags...)
	p.Title = strings.Join(titleToks, " ")

	// Filename may be generic ("movie.mkv"): fall back to the folder.
	if (p.Title == "" || len(p.Title) <= 2) && len(dirParts) > 0 {
		folder := dirParts[len(dirParts)-1]
		var ftags []string
		ft := cleanTokens(folder, &ftags)
		if ym := reYear.FindString(folder); ym != "" && p.Year == 0 {
			p.Year, _ = strconv.Atoi(ym)
		}
		if len(ft) > 0 {
			p.Title = strings.Join(ft, " ")
			p.Tags = append(p.Tags, ftags...)
		}
	}
	// Folder year for movies (v0.3.5): the filename yielded no year
	// — take the immediate parent folder's year when its name
	// contains EXACTLY ONE year token (paren or bare). It never
	// overrides a filename year, and the folder's TITLE is never
	// imported by this rule ("Avatar (2009)/Avatar.mkv" → 2009).
	if p.Year == 0 && len(dirParts) > 0 {
		parent := dirParts[len(dirParts)-1]
		if ys := reYear.FindAllString(parent, -1); len(ys) == 1 {
			p.Year, _ = strconv.Atoi(ys[0])
		}
	}
	if p.Title != "" {
		p.Kind = "movie"
	} else {
		p.Kind = "unknown"
		p.Title = stem
	}
	p.NormTitle = normalizeTitle(p.Title)
	collectGroupTag(stem, &p)
	return p
}

// strongEpisodeNumber reports whether tok is a STRONG absolute
// episode candidate (v0.3.9): exactly four digits with a leading
// zero and value 1–999 ("0103", "0151", "0007"). A year can
// never look like this (years don't start with 0) and neither
// can a resolution — which is what makes it strong: in both the
// series-folder absolute branch and the folder-season branch
// it outranks every weak 1–3 digit candidate regardless of
// position (the weak rule picked the "8" out of "8 o'Clock"
// and lost the real "0103" entirely).
func strongEpisodeNumber(tok string) (int, bool) {
	if len(tok) != 4 || tok[0] != '0' || !isAllDigits(tok) {
		return 0, false
	}
	n, _ := strconv.Atoi(tok)
	if n < 1 || n > 999 {
		return 0, false
	}
	return n, true
}

// bareNumberParts splits a bare-number token into its digit part,
// accepting (and discarding) a glued release version (v0.4.0,
// NAMING-RESEARCH-2 §8): "142" → "142", "01v2" → "01",
// "0103v2" → "0103". A v2 supersedes a v1 OF THE SAME EPISODE, so
// dropping the version is identity-correct for duplicates. Any
// token that is not digits plus an optional vN is refused.
func bareNumberParts(tok string) (string, bool) {
	m := reVNNumber.FindStringSubmatch(tok)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// episodeYearFromChunk extracts the series year from an episode
// filename's PRE-MARKER title chunk (v0.4.0, NAMING-RESEARCH-2
// §1): TRaSH/Servarr standard names carry it there —
// "The Series Title! (2010) - S01E01 …" or dotted
// "The.Series.Title.2010.S01E01…". A parenthesised year wins;
// otherwise the LAST plausible bare year (same plausibility bound
// as the movie branch: not beyond next calendar year). The scan
// never looks past the episode marker, so years inside episode
// titles are never harvested.
func episodeYearFromChunk(chunk string) int {
	if loc := reYearP.FindStringIndex(chunk); loc != nil {
		y, _ := strconv.Atoi(strings.Trim(chunk[loc[0]:loc[1]], "()"))
		return y
	}
	maxYear := time.Now().Year() + 1
	year := 0
	for _, loc := range reYear.FindAllStringIndex(chunk, -1) {
		if y, err := strconv.Atoi(chunk[loc[0]:loc[1]]); err == nil && y <= maxYear {
			year = y
		}
	}
	return year
}

// dailyGuardTitle is the daily/air-date HARM GUARD (v0.4.0,
// NAMING-RESEARCH-2 §3) — NOT daily support: the data model has
// no air-date field, so a date-named episode cannot be identified.
// What it must never do is what it did before the guard: acquire
// a MOVIE identity keyed on the date's year (two different daily
// episodes in one year shared one identity — a false duplicate
// group), or, inside a season folder, an invented episode number
// harvested from the date's day token. On a hit it returns the
// cleaned series title from the text BEFORE the date; the caller
// parks the file as kind "unknown" — honestly unidentified, and
// the grouper never touches it. Only full VALID dates count
// (reDailyDate enforces month 01–12 / day 01–31 shapes); a stem
// whose numbers merely contain a year is untouched.
func dailyGuardTitle(stem string) (string, bool) {
	for _, loc := range reDailyDate.FindAllStringIndex(stem, -1) {
		// The date must stand alone: digits glued to either
		// side mean this is a longer number, not a date.
		if loc[0] > 0 && stem[loc[0]-1] >= '0' && stem[loc[0]-1] <= '9' {
			continue
		}
		if loc[1] < len(stem) && stem[loc[1]] >= '0' && stem[loc[1]] <= '9' {
			continue
		}
		var tags []string
		cleaned := cleanTokens(stem[:loc[0]], &tags)
		if len(cleaned) == 0 {
			continue
		}
		return strings.Join(cleaned, " "), true
	}
	return "", false
}

// extendChain parses Servarr's "Extend" multi-episode chain at
// the start of rest ("-02-03" after "S01E01", v0.4.0): a glued
// run of 2–8 bare numbers, each strictly greater than the
// previous (prev starts at the marker's own episode). It returns
// the episodes to APPEND, or nil when the shape is not a valid
// chain — a single "-NN" is a range tail, not a chain, and the
// caller falls back to the range rule for it.
func extendChain(rest string, first int) []int {
	m := reExtendChain.FindString(rest)
	if m == "" {
		return nil
	}
	// A digit directly after the chain means the last group was
	// truncated mid-number ("-02-0345…") — not a chain.
	if len(m) < len(rest) && rest[len(m)] >= '0' && rest[len(m)] <= '9' {
		return nil
	}
	var out []int
	prev := first
	for _, part := range strings.Split(strings.TrimPrefix(m, "-"), "-") {
		n, _ := strconv.Atoi(part)
		if n <= prev {
			return nil
		}
		out = append(out, n)
		prev = n
	}
	return out
}

// isBareNumberCandidate reports whether tok can be an absolute
// episode number: all digits, length 1–3, and not a resolution
// value (480/720 are the only ones short enough to reach the
// check; the list names all four for clarity). A year can never
// be a candidate — it is four digits.
func isBareNumberCandidate(tok string) bool {
	if len(tok) == 0 || len(tok) > 3 || !isAllDigits(tok) {
		return false
	}
	switch tok {
	case "480", "720", "1080", "2160":
		return false
	}
	return true
}

// isYearToken reports whether tok is a plausible year token
// (1900–2099, the reYear shape) standing alone.
func isYearToken(tok string) bool {
	return len(tok) == 4 && isAllDigits(tok) && reYear.MatchString(tok)
}

// bracketedEpisode captures the bracketed anime episode number
// (v0.4.0, NAMING-RESEARCH-2 §6): "[Group] Series [12]". The
// bracket blocks are stripped before every other number search,
// so the number was invisible — worse, the file became a yearless
// MOVIE titled "Series", and "[12]"/"[13]" files shared one
// identity (a live false-duplicate path). A bracket qualifies
// when its whole content is a bare number (1–3 digits, or the
// strong leading-zero 4-digit form, optionally with a glued vN —
// fix §8), never a year and never a resolution value; the LAST
// qualifying bracket wins. An 8-hex CRC bracket is never shaped
// like this, and "[1080p]" is not pure digits, so release
// furniture stays furniture. The title is the cleaned text
// outside the brackets/parens. A bare year token standing
// outside any block vetoes, exactly as in the bare-number rule
// below; a PAREN year is the series' own year tag and is picked
// up by the caller, not a veto.
func bracketedEpisode(stem string, split func(string) []string) (title string, ep int, ok bool) {
	blocks := reBrack.FindAllStringIndex(stem, -1)
	cand := -1
	var candDigits string
	for i, b := range blocks {
		content := strings.TrimSpace(stem[b[0]+1 : b[1]-1])
		digits, isNum := bareNumberParts(content)
		if !isNum {
			continue
		}
		if _, strong := strongEpisodeNumber(digits); strong {
			cand, candDigits = i, digits
			continue
		}
		if isBareNumberCandidate(digits) {
			cand, candDigits = i, digits
		}
	}
	if cand < 0 {
		return "", 0, false
	}
	// Title/evidence view: bracket AND paren contents removed.
	toks := split(reParen.ReplaceAllString(reBrack.ReplaceAllString(stem, " "), " "))
	for _, tok := range toks {
		if isYearToken(tok) {
			return "", 0, false
		}
	}
	var tags []string
	cleaned := cleanTokens(strings.Join(toks, " "), &tags)
	if len(cleaned) == 0 {
		return "", 0, false
	}
	ep, _ = strconv.Atoi(candDigits)
	return strings.Join(cleaned, " "), ep, true
}

// seriesFolderAbsolute is the series-folder absolute-numbering
// parse (v0.3.7). Anime-style packs often sit DIRECTLY in the
// series folder — no season folder, no SxE/NxN marker — with
// the episode number as a bare segment:
//
//	Vanitas no Karte Part 2_[DB]Vanitas no Karte Part 2_-_11_(Dual Audio_10bit_BD1080p_x265).mkv
//
// ALL of these conditions must hold:
//
//	(a) the file has an immediate parent folder below the scan
//	    root (dirParts non-empty) — a file directly in a root
//	    never takes this branch;
//	(b) no plausible year is in play as a MOVIE year: a bare
//	    year token (outside any block) vetoes outright, and a
//	    year inside a (…)-block vetoes when it sits AFTER the
//	    episode candidate — the canonical trailing movie
//	    position, "Movie Title - 2 (2020)". A year before the
//	    candidate is the series' own year tag ("Series (2011)
//	    - 142", the Sonarr anime shape) and does not veto;
//	    years inside […] blocks are group furniture and never
//	    veto;
//	(c) on the stem with […] and (…) blocks removed, there is
//	    a standalone bare-number token (isBareNumberCandidate)
//	    AFTER the title text; the LAST such token is the
//	    episode number — titles repeat after group brackets
//	    and carry their own numbers ("Part 2" appears twice
//	    before the real "11"). EXCEPTION (v0.3.9): a strong
//	    leading-zero 4-digit token ("0103") is the episode —
//	    the FIRST strong token wins over every weak one,
//	    because a weak token later in the name is usually
//	    episode-TITLE text ("… - 0103 - … at 8 o'Clock!");
//	(d) the remaining text before that number, cleaned, is
//	    non-empty — it becomes the series title. The
//	    filename-derived title wins over the folder's name
//	    when they differ: it is the fuller one (folder
//	    "Vanitas no Karte", title "Vanitas no Karte Part 2").
//
// A fractional stem is refused up front: "Series - 12.5" is a
// recap marker, and the tokenizer would split it into "12"
// and "5" and invent episode 5. v1.3.1: only when the decimal
// IS the episode claim (see fractionalIsEpisodeClaim) — a
// stem with a stronger claim keeps its episode number.
func seriesFolderAbsolute(stem string, dirParts []string) (title string, ep int, ok bool) {
	if len(dirParts) == 0 {
		return "", 0, false
	}
	if fractionalIsEpisodeClaim(stem) {
		return "", 0, false
	}
	split := func(s string) []string {
		return strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(s))
	}
	// v0.4.0: a bracketed episode number ("[Group] Series [12]")
	// is captured BEFORE the bare-token search — it is the more
	// explicit claim, and the bare search cannot see inside
	// brackets at all (see bracketedEpisode).
	if title, ep, ok := bracketedEpisode(stem, split); ok {
		return title, ep, true
	}
	// (c) the search view: bracket AND paren blocks removed.
	toks := split(reParen.ReplaceAllString(reBrack.ReplaceAllString(stem, " "), " "))
	// v0.3.9: a STRONG candidate (leading-zero 4-digit, see
	// strongEpisodeNumber) wins outright — the FIRST strong
	// token is the episode. Only when none exists does the
	// original last-weak-candidate rule run, untouched.
	// v0.4.0: both scans read tokens through bareNumberParts, so
	// a glued release version ("01v2") yields its number instead
	// of killing the parse (research §8).
	cand := -1
	for i, tok := range toks {
		if digits, isNum := bareNumberParts(tok); isNum {
			if _, strong := strongEpisodeNumber(digits); strong {
				cand = i
				break
			}
		}
	}
	if cand < 0 {
		for i, tok := range toks {
			if digits, isNum := bareNumberParts(tok); isNum && isBareNumberCandidate(digits) {
				cand = i
			}
		}
	}
	if cand <= 0 {
		return "", 0, false
	}
	// (b) bare year anywhere in the search view vetoes: block
	// contents are already gone there, so a surviving year
	// token was standing bare in the stem.
	for _, tok := range toks {
		if isYearToken(tok) {
			return "", 0, false
		}
	}
	// (b) paren-year in trailing position: judge on the view
	// with only […] removed, where paren contents still
	// tokenize. Find the candidate's last occurrence there,
	// then refuse any year token after it.
	viewA := split(strings.NewReplacer("(", " ", ")", " ").Replace(reBrack.ReplaceAllString(stem, " ")))
	candA := -1
	for i, tok := range viewA {
		if tok != toks[cand] {
			continue
		}
		if digits, isNum := bareNumberParts(tok); isNum {
			if _, strong := strongEpisodeNumber(digits); strong || isBareNumberCandidate(digits) {
				candA = i
			}
		}
	}
	for i, tok := range viewA {
		if isYearToken(tok) && (candA < 0 || i > candA) {
			return "", 0, false
		}
	}
	// (d) the title text before the number. A trailing
	// word-form label is the number's own label, not title
	// text ("Series - Episode 39" is the series "Series").
	titleToks := toks[:cand]
	if n := len(titleToks); n > 0 &&
		(strings.EqualFold(titleToks[n-1], "episode") || strings.EqualFold(titleToks[n-1], "ep")) {
		titleToks = titleToks[:n-1]
	}
	var tags []string
	cleaned := cleanTokens(strings.Join(titleToks, " "), &tags)
	// The release-group convention repeats the whole title
	// after the [group] bracket ("Title_[DB]Title_-_11_"):
	// when the cleaned text is one phrase repeated exactly
	// twice, collapse it to the single phrase.
	if len(cleaned) >= 2 && len(cleaned)%2 == 0 {
		half := len(cleaned) / 2
		same := true
		for i := 0; i < half; i++ {
			if !strings.EqualFold(cleaned[i], cleaned[i+half]) {
				same = false
				break
			}
		}
		if same {
			cleaned = cleaned[:half]
		}
	}
	if len(cleaned) == 0 {
		return "", 0, false
	}
	candDigits, _ := bareNumberParts(toks[cand])
	ep, _ = strconv.Atoi(candDigits)
	return strings.Join(cleaned, " "), ep, true
}

// collectGroupTag records a trailing -GROUP release tag, when the name
// also carries known release tokens (so a plain hyphenated title word
// is not mistaken for a group).
func collectGroupTag(stem string, p *ParsedName) {
	if m := reGroup.FindStringSubmatch(stem); m != nil && len(p.Tags) > 0 {
		// v0.4.0 guard (NAMING-RESEARCH-2 §12): a trailing token
		// that is itself a known tag/quality token is not a
		// group — "…Remux-2160p" minted "group:2160p" and
		// "…Blu-ray" minted "group:ray".
		if tagTokens[strings.ToLower(m[1])] {
			return
		}
		p.Tags = appendUnique(p.Tags, "group:"+strings.ToLower(m[1]))
	}
}

// resolutionLabel turns a pixel height/width into a Plex-style label.
func resolutionLabel(width, height int) string {
	h := height
	if h == 0 && width > 0 {
		h = width * 9 / 16
	}
	switch {
	case h >= 2000:
		return "2160p"
	case h >= 1000:
		return "1080p"
	case h >= 700:
		return "720p"
	case h >= 500:
		return "576p"
	case h >= 300:
		return "480p"
	case h > 0:
		return strconv.Itoa(h) + "p"
	}
	return ""
}
