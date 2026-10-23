package core

// parser_absolute_test.go — v0.3.7 Fix 1: series-folder absolute
// numbering. The live bug: a season pack sitting DIRECTLY in its
// series folder (no season folder, no SxE/NxN marker), episode
// number carried as a bare "_-_NN_" segment:
//
//	Vanitas no Karte Part 2_[DB]Vanitas no Karte Part 2_-_11_(Dual Audio_10bit_BD1080p_x265).mkv
//
// fell to the movie fallback, whose title cut at the first tag
// token ("2") produced the SAME yearless title-only identity for
// all twelve episodes — and the grouper (identity = title +
// year) collapsed twelve different episodes into one confirmed
// "duplicate" group with live delete buttons on it. The new
// parse step runs after the folder-season branch and before the
// movie fallback: a file in a subfolder, with no plausible year
// in play, whose stem carries a standalone bare-number token
// after the title text, is an absolutely-numbered episode of
// the series named by that title text.

import (
	"fmt"
	"testing"
)

// vanitasName is the exact filename shape from the live report.
func vanitasName(ep int) string {
	return fmt.Sprintf("Vanitas no Karte Part 2_[DB]Vanitas no Karte Part 2_-_%02d_(Dual Audio_10bit_BD1080p_x265).mkv", ep)
}

func vanitasPath(ep int) string {
	return "/TV Shows/Vanitas no Karte/" + vanitasName(ep)
}

// TestParserSeriesFolderAbsoluteVanitas: the exact live case —
// episode 11 parses as episode 11 of "Vanitas no Karte Part 2"
// (the fuller filename-derived title, NOT the folder's shorter
// "Vanitas no Karte"), season 1 by the absolute convention.
func TestParserSeriesFolderAbsoluteVanitas(t *testing.T) {
	p := parseName(vanitasPath(11), "/TV Shows")
	if p.Kind != "episode" {
		t.Fatalf("kind = %q, want episode (parsed %+v)", p.Kind, p)
	}
	if p.Title != "Vanitas no Karte Part 2" {
		t.Errorf("title = %q, want %q", p.Title, "Vanitas no Karte Part 2")
	}
	if p.Season != 1 {
		t.Errorf("season = %d, want 1", p.Season)
	}
	if len(p.Episodes) != 1 || p.Episodes[0] != 11 {
		t.Errorf("episodes = %v, want [11]", p.Episodes)
	}
}

// TestParserSeriesFolderAbsolutePackDistinct: all twelve
// episodes of the pack parse to twelve DISTINCT identities and
// form no duplicate group at all.
func TestParserSeriesFolderAbsolutePackDistinct(t *testing.T) {
	seen := map[string]bool{}
	var items []*Item
	for ep := 1; ep <= 12; ep++ {
		path := vanitasPath(ep)
		p := parseName(path, "/TV Shows")
		if p.Kind != "episode" || len(p.Episodes) != 1 || p.Episodes[0] != ep {
			t.Fatalf("ep %d parsed as %+v", ep, p)
		}
		ident := fmt.Sprintf("%s|%d|%d", p.NormTitle, p.Season, p.Episodes[0])
		if seen[ident] {
			t.Fatalf("episode %d identity %q already seen", ep, ident)
		}
		seen[ident] = true
		items = append(items, mkEpisodeItem(path, p, int64(300_000_000+ep*7_000_000), fmt.Sprintf("hv%d", ep)))
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 0 {
		t.Fatalf("groups = %d, want 0 (first group: %+v)", len(groups), groups[0])
	}
}

// TestParserSeriesFolderAbsoluteRequiredCases: the two other
// shapes the fix must cover — a leading group bracket, and the
// glued "_-_" separator with a bracketed quality tail.
func TestParserSeriesFolderAbsoluteRequiredCases(t *testing.T) {
	p := mustParse(t, "/lib/Series Name/[DB] Series Name - 07 (1080p).mkv", "/lib", "episode")
	if p.Title != "Series Name" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 7 {
		t.Fatalf("parsed %+v, want Series Name S01 [7]", p)
	}
	p = mustParse(t, "/lib/Series Name/Series Name_-_03_[1080p].mkv", "/lib", "episode")
	if p.Title != "Series Name" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 3 {
		t.Fatalf("parsed %+v, want Series Name S01 [3]", p)
	}
}

// TestParserSeriesFolderAbsoluteNegatives: the branch must not
// fire where a movie's business is concerned.
//   - A year — bare or in the trailing (Year) position — vetoes
//     it: "Movie Title - 2 (2020)" stays a movie, year 2020.
//   - A file directly in a scan root never takes the branch.
//   - An ordinary dotted scene movie in a subfolder has no
//     standalone number segment after a title and is untouched.
func TestParserSeriesFolderAbsoluteNegatives(t *testing.T) {
	p := mustParse(t, "/lib/Films/Movie Title - 2 (2020).mkv", "/lib", "movie")
	if p.Year != 2020 {
		t.Fatalf("parsed %+v, want a movie with year 2020 (the title's digit survives per the title-digit rule — what matters is it did NOT become episode 2)", p)
	}
	p = parseName("/TV Shows/"+vanitasName(11), "/TV Shows")
	if p.Kind == "episode" {
		t.Fatalf("root-level file took the series-folder branch: %+v", p)
	}
	p = mustParse(t, "/lib/Films/Film.Name.1080p.WEB-DL.x264.mkv", "/lib", "movie")
	if p.Title != "Film Name" || p.Year != 0 {
		t.Fatalf("parsed %+v, want movie %q year 0", p, "Film Name")
	}
}
