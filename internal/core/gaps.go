package core

// gaps.go — missing-episode derivation (v0.9.0, FEATURE-PLAN
// §1: "Missing-episode gaps").
//
// The derivation is pure read-time arithmetic over the parsed
// series/season/episode identities the scanner already produced
// — no scan-model change, no persistence: the same items always
// derive the same gaps. A "gap" is a series+season whose present
// episode numbers are not contiguous: the numbers strictly
// between the first and last present episode that no file
// accounts for. The wording everywhere is "missing between
// first and last present" — the derivation knows nothing about
// episodes before the first or after the last file, and never
// claims a series is incomplete at its edges.
//
// Guards (each one exists because the naive version cries wolf):
//
//   - Season 0 (specials) is EXCLUDED entirely: specials
//     numbering is not sequential truth (OVAs, pilots and
//     Christmas specials share a season with no order promise).
//   - A season needs at least TWO distinct present episode
//     numbers before anything can be "between" them; a single
//     episode shows no gaps.
//   - Multi-episode files contribute EVERY number they carry
//     (the parser puts them all in Item.Episodes), so a
//     S01E01-E02 file fills both slots.
//   - Split files (Item.Special == "split-file", a CD1/CD2 pair)
//     contribute NOTHING: their episode identity is a part
//     marker, not a whole episode, and counting them would both
//     fill slots falsely and invent holes around them.
//     Fractional (.5) specials carry no plain episode list from
//     the parser, so they contribute nothing by construction.
//   - Absolute-numbered series (season 1 running to episode
//     50+) are still reported — the holes are usually real — but
//     the row is flagged Absolute so the UI can say "verify
//     against your source" instead of presenting source-order
//     differences as plain holes.
//   - The listed missing numbers are capped per season
//     (gapMissingListCap); MissingCount always carries the true
//     total and Truncated says the list was cut.

import "sort"

// gapMissingListCap bounds how many missing episode numbers one
// gap row lists. The count is never capped — only the list.
const gapMissingListCap = 100

// absoluteNumberingMaxPresent is the season-1 max-present episode
// number at or above which a season is flagged as probably
// absolute-numbered (a weekly show's season 1 does not reach 50
// episodes; a long-running anime's does).
const absoluteNumberingMaxPresent = 50

// EpisodeGap is one series+season row of the gaps derivation.
type EpisodeGap struct {
	Series       string `json:"series"`
	NormTitle    string `json:"norm_title"`
	Season       int    `json:"season"`
	Present      []int  `json:"present"`
	PresentCount int    `json:"present_count"`
	Missing      []int  `json:"missing"`
	MissingCount int    `json:"missing_count"`
	Truncated    bool   `json:"truncated,omitempty"`
	Absolute     bool   `json:"absolute,omitempty"`
}

// buildEpisodeGaps derives the gap rows for a set of items.
// Series identity is the parser's NormTitle — the same key
// buildSeries aggregates by — and the display name is the first
// item's Title for that key. Excluded items are NOT filtered
// out: an excluded copy still exists on disk and still fills
// its episode slot (exclusions suppress checks, not files).
func buildEpisodeGaps(items []*Item) []EpisodeGap {
	type seasonKey struct {
		norm   string
		season int
	}
	present := map[seasonKey]map[int]bool{}
	titles := map[string]string{}
	for _, it := range items {
		if it == nil || it.Kind != "episode" || it.Season < 1 {
			continue
		}
		if it.Special == "split-file" {
			continue
		}
		if len(it.Episodes) == 0 {
			continue
		}
		k := seasonKey{it.NormTitle, it.Season}
		set := present[k]
		if set == nil {
			set = map[int]bool{}
			present[k] = set
		}
		for _, e := range it.Episodes {
			set[e] = true
		}
		if _, ok := titles[it.NormTitle]; !ok {
			titles[it.NormTitle] = it.Title
		}
	}
	gaps := []EpisodeGap{}
	for k, set := range present {
		if len(set) < 2 {
			continue // a single episode has nothing "between"
		}
		nums := make([]int, 0, len(set))
		for n := range set {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		min, max := nums[0], nums[len(nums)-1]
		missing := []int{}
		missingCount := 0
		for n := min + 1; n < max; n++ {
			if set[n] {
				continue
			}
			missingCount++
			if len(missing) < gapMissingListCap {
				missing = append(missing, n)
			}
		}
		if missingCount == 0 {
			continue
		}
		gaps = append(gaps, EpisodeGap{
			Series:       titles[k.norm],
			NormTitle:    k.norm,
			Season:       k.season,
			Present:      nums,
			PresentCount: len(nums),
			Missing:      missing,
			MissingCount: missingCount,
			Truncated:    missingCount > len(missing),
			Absolute:     k.season == 1 && max >= absoluteNumberingMaxPresent,
		})
	}
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].Series != gaps[j].Series {
			return gaps[i].Series < gaps[j].Series
		}
		return gaps[i].Season < gaps[j].Season
	})
	return gaps
}

// EpisodeGaps returns the current results' gap rows
// (GET /api/gaps). Read-only: there are no gap actions in this
// release — a gap is information, not an instruction.
func (s *Service) EpisodeGaps() []EpisodeGap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return buildEpisodeGaps(s.results.Items)
}
