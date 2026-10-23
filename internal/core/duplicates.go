package core

// duplicates.go — duplicate grouping by parsed identity.
//
// Identity, not bytes: a movie is title+year(+edition), an episode is
// series+season+episode(+edition). Duration confirms or demotes a
// group; sampled hashes flag exact duplicates inside a group.
// Exclusions (scope "duplicates") remove copies before grouping, so a
// group reduced below 2 copies disappears (SPEC §Reference exclusions).

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// dupStats carries bookkeeping the scan summary needs.
type dupStats struct {
	ExcludedCopies int // copies dropped by exclusions that belonged to a would-be group
}

// identityKey is the full group key including the edition.
func identityKey(it *Item) string {
	if it.Kind == "episode" {
		eps := make([]string, len(it.Episodes))
		for i, e := range it.Episodes {
			eps[i] = fmt.Sprint(e)
		}
		return fmt.Sprintf("episode|%s|s%02d|e%s|%s", it.NormTitle, it.Season, strings.Join(eps, "-"), it.Edition)
	}
	return movieBaseKey(it) + "|" + it.Edition
}

// baseKey is the identity without the edition (for cross-links).
func baseKey(it *Item) string {
	if it.Kind == "episode" {
		eps := make([]string, len(it.Episodes))
		for i, e := range it.Episodes {
			eps[i] = fmt.Sprint(e)
		}
		return fmt.Sprintf("episode|%s|s%02d|e%s", it.NormTitle, it.Season, strings.Join(eps, "-"))
	}
	return movieBaseKey(it)
}

// movieBaseKey is a movie's identity without the edition. For a
// YEARESS movie (v0.3.7) the key also carries the filename's
// standalone-number signature: a yearless title-only identity
// is exactly the shape that collapsed the Vanitas pack (twelve
// different episodes, all parsed as the same yearless "movie")
// into one false group, and the v0.3.5 episode guard cannot
// see movie-kind items. Two yearless items whose stems disagree
// on their bare numbers are different works, whatever their
// shared title says. Items WITH a year keep the exact
// pre-v0.3.7 key — the year already separates them.
func movieBaseKey(it *Item) string {
	k := fmt.Sprintf("movie|%s|%d", it.NormTitle, it.Year)
	if it.Year == 0 {
		k += "|n" + numberSignature(it)
	}
	return k
}

// numberSignature is the sequence of standalone number tokens
// in an item's filename stem, in order: the digits that survive
// the same cleaning the title gets (bracket/paren blocks
// removed first, known release-tag tokens stripped by the
// existing tag machinery — resolutions and codecs never
// count, and the audio-digit rule applies, so "DD 5.1" digits
// vanish while a title's own "2" in "Part 2" survives). Two
// encodes of one film differ only in their tags, so their
// signatures are EQUAL; two different episodes of one pack
// differ in the episode number, so theirs never are.
func numberSignature(it *Item) string {
	name := it.Name
	if name == "" {
		name = filepath.Base(it.Path)
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	// Paren blocks are removed outright (cleanTokensMode would
	// tokenize their contents — "(Part 2)" must not leak its
	// digit); bracket/brace blocks are stripped inside
	// cleanTokensMode itself.
	stripped := reParen.ReplaceAllString(stem, " ")
	var tags []string
	toks := cleanTokensMode(stripped, &tags, false)
	digits := []string{}
	for _, tok := range toks {
		if isAllDigits(tok) {
			digits = append(digits, tok)
		}
	}
	return strings.Join(digits, ",")
}

// eligibleForDuplicates applies the standing exclusions of the feature
// itself: samples/trailers/extras, multi-episode and split files, and
// zero-byte files never form plain duplicate groups.
func eligibleForDuplicates(it *Item) bool {
	if it.Kind != "movie" && it.Kind != "episode" {
		return false
	}
	// GROUPER SAFETY GUARD (v0.3.5): an episode whose episode
	// number(s) could not be parsed must NEVER enter episode
	// duplicate grouping. Its identity key (series + season +
	// episodes) would be identical for every episode of the
	// season, so a whole season pack collapsed into one false
	// "duplicate" group on the user's live library (fixed in
	// v0.3.5 together with the new parser patterns). The rule:
	// an unidentified episode can produce no group, never a
	// wrong one. The item stays a normal library item; this also
	// keeps it out of the exact-duplicate flagging below, which
	// only runs inside groups formed here — there is no other
	// grouping path that uses the episode key.
	if it.Kind == "episode" && len(it.Episodes) == 0 {
		return false
	}
	if it.IsSample || it.Special != "" || it.Size == 0 {
		return false
	}
	return it.NormTitle != ""
}

// resHeight gives the keeper-ranking height for a copy: probed height,
// falling back to the filename resolution label.
func resHeight(it *Item) int {
	if it.Height > 0 {
		return it.Height
	}
	switch it.ResolutionLabel {
	case "2160p":
		return 2160
	case "1080p":
		return 1080
	case "720p":
		return 720
	case "576p":
		return 576
	case "480p":
		return 480
	}
	return 0
}

// betterKeeper reports whether a beats b as keeper: highest
// resolution, then HDR, then bitrate, then size.
func betterKeeper(a, b *Item) bool {
	if resHeight(a) != resHeight(b) {
		return resHeight(a) > resHeight(b)
	}
	if a.HDR != b.HDR {
		return a.HDR
	}
	if a.Bitrate != b.Bitrate {
		return a.Bitrate > b.Bitrate
	}
	return a.Size > b.Size
}

// buildDuplicates groups items into duplicate groups. exclusions may
// be nil (unit tests / raw analysis).
func buildDuplicates(items []*Item, cfg *Config, exclusions []Exclusion) ([]DupGroup, dupStats) {
	var stats dupStats
	// Raw groups first, so we can count excluded copies that belonged
	// to a group before exclusions were applied.
	rawGroups := map[string][]*Item{}
	for _, it := range items {
		if !eligibleForDuplicates(it) {
			continue
		}
		k := identityKey(it)
		rawGroups[k] = append(rawGroups[k], it)
	}
	isEx := func(it *Item) bool {
		return len(exclusions) > 0 && isExcluded(exclusions, it.Path, "duplicates")
	}
	for _, copies := range rawGroups {
		if len(copies) < 2 {
			continue
		}
		for _, it := range copies {
			if isEx(it) {
				stats.ExcludedCopies++
			}
		}
	}
	// Filtered grouping.
	groups := map[string][]*Item{}
	allKeys := map[string][]*Item{} // includes singleton edition variants, for cross-links
	for _, it := range items {
		if !eligibleForDuplicates(it) || isEx(it) {
			continue
		}
		k := identityKey(it)
		allKeys[k] = append(allKeys[k], it)
	}
	for k, copies := range allKeys {
		if len(copies) >= 2 {
			groups[k] = copies
		}
	}
	out := []DupGroup{}
	for key, copies := range groups {
		g := DupGroup{Key: key, BaseKey: baseKey(copies[0])}
		first := copies[0]
		g.Kind, g.Title, g.Year = first.Kind, first.Title, first.Year
		g.Season, g.Episodes, g.Edition = first.Season, first.Episodes, first.Edition
		// Keeper.
		keeper := copies[0]
		for _, c := range copies[1:] {
			if betterKeeper(c, keeper) {
				keeper = c
			}
		}
		g.KeeperPath = keeper.Path
		why := []string{fmt.Sprintf("highest resolution (%s)", keeper.ResolutionLabel)}
		if keeper.HDR {
			why = append(why, "HDR")
		}
		highestBitrate := true
		for _, c := range copies {
			if c != keeper && c.Bitrate > keeper.Bitrate {
				highestBitrate = false
			}
		}
		if highestBitrate && keeper.Bitrate > 0 {
			why = append(why, fmt.Sprintf("highest bitrate (%.1f Mbps)", float64(keeper.Bitrate)/1e6))
		}
		g.KeeperWhy = strings.Join(why, ", ")
		// Copies + wasted.
		var total int64
		for _, c := range copies {
			total += c.Size
			g.Copies = append(g.Copies, DupCopy{Item: *c, IsKeeper: c.Path == keeper.Path})
		}
		sort.Slice(g.Copies, func(i, j int) bool { return g.Copies[i].Size > g.Copies[j].Size })
		g.WastedBytes = total - keeper.Size
		// v1.0.0 hardlink math: WastedBytes above is the APPARENT
		// figure (every extra entry counted at full size).
		// ReclaimableBytes is what deleting down to one physical
		// copy would actually free: copies are grouped by storage
		// identity (device+inode); each distinct physical file
		// beyond the keeper's counts ONCE at its size, and entries
		// sharing the keeper's identity count ZERO — deleting a
		// hardlink of the keeper removes a name, not data. A copy
		// whose identity is unknown is treated as its own physical
		// file (the pre-v1.0.0 arithmetic — the honest fallback,
		// labelled via HardlinkKnown=false).
		physSize := map[string]int64{}
		physKeyOf := make([]string, len(g.Copies))
		keeperKey := ""
		allKnown := true
		for i := range g.Copies {
			c := &g.Copies[i]
			key := ""
			if c.Dev != 0 && c.Ino != 0 {
				c.IdentityKnown = true
				key = fmt.Sprintf("%d:%d", c.Dev, c.Ino)
			} else {
				allKnown = false
				key = "path:" + c.Path // unknown = its own physical file
			}
			physKeyOf[i] = key
			if _, seen := physSize[key]; !seen {
				physSize[key] = c.Size
			}
			if c.IsKeeper {
				keeperKey = key
			}
		}
		g.FileEntries = len(g.Copies)
		g.PhysicalCopies = len(physSize)
		g.HardlinkKnown = allKnown
		hardlinksOfKeeper := 0
		for i := range g.Copies {
			if !g.Copies[i].IsKeeper && physKeyOf[i] == keeperKey {
				g.Copies[i].HardlinkOfKeeper = true
				hardlinksOfKeeper++
			}
			if physKeyOf[i] != keeperKey {
				// Count each distinct physical file once: only the
				// first entry carrying a key contributes its size.
				first := true
				for j := 0; j < i; j++ {
					if physKeyOf[j] == physKeyOf[i] {
						first = false
						break
					}
				}
				if first {
					g.ReclaimableBytes += physSize[physKeyOf[i]]
				}
			}
		}
		if hardlinksOfKeeper > 0 {
			g.KeeperWhy += fmt.Sprintf("; hardlinked to %d other %s — deleting those frees 0 B", hardlinksOfKeeper, pluralCopies(hardlinksOfKeeper))
		}
		// Exact duplicates: same size AND same sampled hash.
		byHash := map[string][]int{}
		for i := range g.Copies {
			c := &g.Copies[i]
			if c.SampleHash == "" {
				continue
			}
			hk := fmt.Sprintf("%d|%s", c.Size, c.SampleHash)
			byHash[hk] = append(byHash[hk], i)
		}
		for _, idxs := range byHash {
			if len(idxs) >= 2 {
				g.HasExact = true
				for _, i := range idxs {
					g.Copies[i].ExactDuplicate = true
				}
			}
		}
		// Duration classification.
		g.Status, g.Note = classifyDurations(copies, cfg)
		// Cross-links to other editions of the same base identity.
		for otherKey, otherCopies := range allKeys {
			if otherKey == key || baseKey(otherCopies[0]) != g.BaseKey {
				continue
			}
			paths := []string{}
			for _, c := range otherCopies {
				paths = append(paths, c.Path)
			}
			ed := otherCopies[0].Edition
			if ed == "" {
				ed = "(default edition)"
			}
			g.CrossLinks = append(g.CrossLinks, CrossLink{Edition: ed, Key: otherKey, Count: len(otherCopies), Paths: paths})
		}
		sort.Slice(g.CrossLinks, func(i, j int) bool { return g.CrossLinks[i].Edition < g.CrossLinks[j].Edition })
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WastedBytes > out[j].WastedBytes })
	return out, stats
}

// pluralCopies is the tiny plural helper for keeper notes.
func pluralCopies(n int) string {
	if n == 1 {
		return "copy"
	}
	return "copies"
}

// classifyDurations implements the confirm / probable / demoted logic:
// all durations present and within tolerance => confirmed; a duration
// missing => probable; durations beyond tolerance => demoted w/ note.
func classifyDurations(copies []*Item, cfg *Config) (string, string) {
	var minD, maxD float64
	missing := 0
	for i, c := range copies {
		if c.Duration <= 0 {
			missing++
			continue
		}
		if i == 0 || c.Duration < minD || minD == 0 {
			if minD == 0 || c.Duration < minD {
				minD = c.Duration
			}
		}
		if c.Duration > maxD {
			maxD = c.Duration
		}
	}
	// Recompute min correctly (the loop above is careful but let it be simple).
	minD = 0
	for _, c := range copies {
		if c.Duration <= 0 {
			continue
		}
		if minD == 0 || c.Duration < minD {
			minD = c.Duration
		}
	}
	if missing > 0 {
		return "probable", fmt.Sprintf("duration missing for %d of %d copies; identity match only", missing, len(copies))
	}
	diff := maxD - minD
	tol := maxD * cfg.DurationTolerancePct / 100
	if cfg.DurationToleranceSec > tol {
		tol = cfg.DurationToleranceSec
	}
	if diff <= tol {
		return "confirmed", fmt.Sprintf("durations within tolerance (Δ %.1fs, tolerance %.1fs)", diff, tol)
	}
	return "demoted", fmt.Sprintf("durations differ by %.1fs, beyond tolerance %.1fs — may not be the same cut", diff, tol)
}
