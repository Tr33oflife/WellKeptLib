package core

// parser_expansion_test.go — the v0.3.5 naming-pattern expansion
// (NAMING-PATTERNS.md fold list): extras, specials, brace tags,
// movie year/title cleanup, SxE completion, NxN parity, split
// vocabulary, folder year, fractional flag-only, and the folder-
// branch repairs. Each test names the catalog item it covers.

import (
	"path/filepath"
	"strings"
	"testing"
)

// --- Item 1: extras -------------------------------------------------

// TestParserExtraBySuffix: a trailing extras token classifies the
// file BEFORE episode/movie parsing — an episode extra, whose
// basename by Plex convention starts with the episode code, must
// NOT parse as that episode.
func TestParserExtraBySuffix(t *testing.T) {
	p := mustParse(t, "/lib/Great Show/Season 01/Great Show S01E02-featurette.mkv", "/lib", "extra")
	if p.Season != 0 || len(p.Episodes) != 0 || p.Year != 0 || p.Special != "" {
		t.Fatalf("extra leaked identity fields: %+v", p)
	}
	p2 := mustParse(t, "/lib/Some Film (2020)/Some Film (2020)-trailer.mkv", "/lib", "extra")
	if p2.Title != "Some Film" {
		t.Fatalf("movie extra title = %q, want %q", p2.Title, "Some Film")
	}
	for _, suf := range []string{"-behindthescenes", "-deleted", "-deletedscene", "-interview", "-scene", "-short", "-other", "-sample", "-clip", "-extra"} {
		pp := parseName("/lib/Film (2021)"+suf+".mkv", "/lib")
		if pp.Kind != "extra" {
			t.Errorf("suffix %s: kind = %q, want extra", suf, pp.Kind)
		}
	}
	// Control: the word inside a title is not a suffix. "The
	// Trailer" does not END in a hyphen-attached token.
	p3 := mustParse(t, "/lib/The Trailer (2019).mkv", "/lib", "movie")
	if p3.Title != "The Trailer" || p3.Year != 2019 {
		t.Fatalf("control title/year = %q/%d", p3.Title, p3.Year)
	}
}

// TestParserExtraByFolder: an extras folder marks everything in it.
func TestParserExtraByFolder(t *testing.T) {
	p := mustParse(t, "/lib/Some Film (2020)/Extras/Deleted Scenes Reel.mkv", "/lib", "extra")
	if p.Title != "Deleted Scenes Reel" {
		t.Fatalf("title = %q", p.Title)
	}
	mustParse(t, "/lib/Great Show/Behind The Scenes/Making Of.mkv", "/lib", "extra")
	mustParse(t, "/lib/Great Show/Season 01/Featurettes/About.mkv", "/lib", "extra")
}

// TestDuplicatesExtrasNeverGroup: duplicates-level proof from
// parsed items — the real episode groups with its true copy; the
// episode extra is not in the group and cannot form one itself.
func TestDuplicatesExtrasNeverGroup(t *testing.T) {
	realA := parseName("/lib/Great Show/Season 01/Great Show S01E02.mkv", "/lib")
	realB := parseName("/other/Great Show/Season 01/Great Show S01E02.mkv", "/other")
	extra := parseName("/lib/Great Show/Season 01/Great Show S01E02-featurette.mkv", "/lib")
	extra2 := parseName("/other/Great Show/Season 01/Great Show S01E02-trailer.mkv", "/other")
	if extra.Kind != "extra" || extra2.Kind != "extra" {
		t.Fatalf("premise broken: extras parsed as %q / %q", extra.Kind, extra2.Kind)
	}
	mk := func(path string, p ParsedName) *Item {
		it := mkEpisodeItem(path, p, 300_000_000, "h-"+path)
		it.Special = p.Special
		return it
	}
	extraItem := mk(extraPath("/lib"), extra)
	if eligibleForDuplicates(extraItem) {
		t.Fatal("extra item must not be eligible for duplicate grouping")
	}
	items := []*Item{
		mk("/lib/Great Show/Season 01/Great Show S01E02.mkv", realA),
		mk("/other/Great Show/Season 01/Great Show S01E02.mkv", realB),
		extraItem,
		mk("/other/Great Show/Season 01/Great Show S01E02-trailer.mkv", extra2),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want exactly 1 (the real episode pair)", len(groups))
	}
	if len(groups[0].Copies) != 2 {
		t.Fatalf("group copies = %d, want 2 (extras excluded)", len(groups[0].Copies))
	}
	for _, c := range groups[0].Copies {
		if strings.Contains(c.Path, "-featurette") || strings.Contains(c.Path, "-trailer") {
			t.Fatalf("extra leaked into duplicate group: %s", c.Path)
		}
	}
}

func extraPath(root string) string {
	return root + "/Great Show/Season 01/Great Show S01E02-featurette.mkv"
}

// TestPlanExtraLeftInPlace: Organize lists an extra as unidentified
// with its own reason — identified, but never moved.
func TestPlanExtraLeftInPlace(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Some Film (2020)-trailer.mkv")
	writeOrgFile(t, src, "trailer")
	plan := mustPlan(t, root)
	wantUnidentified(t, plan, src, "extra content — left in place")
}

// --- Item 2: specials ------------------------------------------------

// TestParserSpecialsFolder: a "Specials" folder is season-0 TV
// context; SP## and bare E-tokens both land at Season 0, and a
// tokenless file stays numberless.
func TestParserSpecialsFolder(t *testing.T) {
	p := mustParse(t, "/lib/Great Show/Specials/Great Show SP01.mkv", "/lib", "episode")
	if p.Season != 0 || len(p.Episodes) != 1 || p.Episodes[0] != 1 || p.Title != "Great Show" {
		t.Fatalf("SP01 in Specials: %+v", p)
	}
	p2 := mustParse(t, "/lib/Great Show/Specials/E05.mkv", "/lib", "episode")
	if p2.Season != 0 || len(p2.Episodes) != 1 || p2.Episodes[0] != 5 {
		t.Fatalf("E05 in Specials: %+v", p2)
	}
	p3 := mustParse(t, "/lib/Great Show/Specials/random.mkv", "/lib", "episode")
	if len(p3.Episodes) != 0 {
		t.Fatalf("tokenless specials file gained episodes: %+v", p3)
	}
}

// TestParserSeasonZeroFolders: "Season 00" and "S00" folders now
// activate the folder branch (the old >0 gate skipped them).
func TestParserSeasonZeroFolders(t *testing.T) {
	for _, folder := range []string{"Season 00", "S00"} {
		p := mustParse(t, "/lib/Great Show/"+folder+"/Great Show E03.mkv", "/lib", "episode")
		if p.Season != 0 || len(p.Episodes) != 1 || p.Episodes[0] != 3 {
			t.Errorf("%s folder: season/episodes = %d/%v, want 0/[3]", folder, p.Season, p.Episodes)
		}
	}
}

// TestParserSPInSeasonFolder: an SP## file inside a regular season
// folder is still a special — Season is forced to 0, not the
// folder's season.
func TestParserSPInSeasonFolder(t *testing.T) {
	p := mustParse(t, "/lib/Great Show/Season 02/Great Show SP01.mkv", "/lib", "episode")
	if p.Season != 0 || len(p.Episodes) != 1 || p.Episodes[0] != 1 {
		t.Fatalf("SP01 in Season 02: season/episodes = %d/%v, want 0/[1]", p.Season, p.Episodes)
	}
}

// --- Item 3: brace tags ----------------------------------------------

// TestParserEditionBrace: the official {edition-…} form yields its
// payload verbatim (the phrase scan could never see it).
func TestParserEditionBrace(t *testing.T) {
	p := mustParse(t, "/lib/Some Film (2019) {edition-Director's Cut}.mkv", "/lib", "movie")
	if p.Edition != "Director's Cut" {
		t.Fatalf("edition = %q, want %q", p.Edition, "Director's Cut")
	}
	if p.Title != "Some Film" || p.Year != 2019 {
		t.Fatalf("title/year = %q/%d", p.Title, p.Year)
	}
	p2 := mustParse(t, "/lib/Some Film (2019) {edition-Unrated}.mkv", "/lib", "movie")
	if p2.Edition != "Unrated" {
		t.Fatalf("edition = %q, want Unrated", p2.Edition)
	}
	p3 := mustParse(t, "/lib/Some Film (2019) {tmdb-12345}.mkv", "/lib", "movie")
	if p3.Title != "Some Film" || p3.Edition != "" {
		t.Fatalf("provider brace leaked: title=%q edition=%q", p3.Title, p3.Edition)
	}
}

// TestParserBraceFolderPollution: a {tvdb-…} tag on the series
// folder used to leak "{tvdb" and "73244}" tokens into the series
// title; brace stripping removes them.
func TestParserBraceFolderPollution(t *testing.T) {
	p := mustParse(t, "/lib/The Office (US) (2005) {tvdb-73244}/Season 01/The Office E05.mkv", "/lib", "episode")
	if p.Title != "The Office US" {
		t.Fatalf("series title = %q, want %q", p.Title, "The Office US")
	}
	if strings.Contains(p.Title, "{") || strings.Contains(p.Title, "73244") {
		t.Fatalf("brace pollution survived: %q", p.Title)
	}
	if len(p.Episodes) != 1 || p.Episodes[0] != 5 {
		t.Fatalf("episodes = %v", p.Episodes)
	}
}

// --- Item 4: movie year/title cleanup ---------------------------------

// TestParserNumericTitles: the catalog's worked examples. Only the
// CHOSEN year occurrence is cut from the title; other year-like
// tokens are title text.
func TestParserNumericTitles(t *testing.T) {
	cases := []struct {
		path      string
		wantTitle string
		wantYear  int
	}{
		{"/lib/1917 (2019).mkv", "1917", 2019},
		{"/lib/2012 (2009).mkv", "2012", 2009},
		{"/lib/2001 A Space Odyssey (1968).mkv", "2001 A Space Odyssey", 1968},
		{"/lib/Blade Runner 2049 (2017).mkv", "Blade Runner 2049", 2017},
		// Unpunctuated: 2049 is not a plausible release year, so
		// no year is chosen and the number stays in the title.
		{"/lib/Blade Runner 2049.mkv", "Blade Runner 2049", 0},
		// Bare-year fallback takes the LAST plausible bare year;
		// the earlier year token stays in the title.
		{"/lib/Movie 1999 2020.mkv", "Movie 1999", 2020},
	}
	for _, c := range cases {
		p := mustParse(t, c.path, "/lib", "movie")
		if p.Title != c.wantTitle || p.Year != c.wantYear {
			t.Errorf("parseName(%q): title/year = %q/%d, want %q/%d", c.path, p.Title, p.Year, c.wantTitle, c.wantYear)
		}
	}
}

// TestParserTitleDigitSurvival: a lone digit with word neighbours
// is title text ("Fast 5"); a digit PAIR is the split audio spec
// and is dropped with its tag.
func TestParserTitleDigitSurvival(t *testing.T) {
	p := mustParse(t, "/lib/Fast 5 (2011).mkv", "/lib", "movie")
	if p.Title != "Fast 5" || p.Year != 2011 {
		t.Fatalf("Fast 5: title/year = %q/%d", p.Title, p.Year)
	}
	p2 := mustParse(t, "/lib/Movie DD 5.1 (2020).mkv", "/lib", "movie")
	if p2.Title != "Movie" {
		t.Fatalf("DD 5.1 title = %q, want %q (the 5.1 pair is audio, not title)", p2.Title, "Movie")
	}
	// Series-name cleaning keeps the digit too ("Channel 5").
	p3 := mustParse(t, "/lib/Channel 5/Season 01/E05.mkv", "/lib", "episode")
	if p3.Title != "Channel 5" {
		t.Fatalf("series title = %q, want %q", p3.Title, "Channel 5")
	}
}

// --- Item 5: SxE completion -------------------------------------------

// TestParserSxESeparators: one optional separator between S and E.
func TestParserSxESeparators(t *testing.T) {
	for _, name := range []string{"Show S01.E02.mkv", "Show S01 E02.mkv", "Show S01-E02.mkv", "Show S01E02.mkv"} {
		p := mustParse(t, "/lib/"+name, "/lib", "episode")
		if p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 2 {
			t.Errorf("%s: season/episodes = %d/%v, want 1/[2]", name, p.Season, p.Episodes)
		}
	}
}

// TestParserSxERangeTails: single tails are RANGES (all episodes
// first..last), scanned case-insensitively; chained tails
// enumerate; bare tails are guarded ranges.
func TestParserSxERangeTails(t *testing.T) {
	cases := []struct {
		path string
		want []int
	}{
		{"/lib/Show S01E01-E03.mkv", []int{1, 2, 3}},
		{"/lib/Show S01E01-e03.mkv", []int{1, 2, 3}}, // lowercase tail
		{"/lib/Show S01E01-03.mkv", []int{1, 2, 3}},  // bare range tail
		{"/lib/Show S01E01E02.mkv", []int{1, 2}},     // chain
		{"/lib/Show S01E01-E02.mkv", []int{1, 2}},    // two-episode range
		{"/lib/Show S01E01-720p.mkv", []int{1}},      // resolution tail is not a range
	}
	for _, c := range cases {
		p := mustParse(t, c.path, "/lib", "episode")
		if len(p.Episodes) != len(c.want) {
			t.Errorf("%s: episodes = %v, want %v", c.path, p.Episodes, c.want)
			continue
		}
		for i, e := range c.want {
			if p.Episodes[i] != e {
				t.Errorf("%s: episodes = %v, want %v", c.path, p.Episodes, c.want)
				break
			}
		}
	}
}

// TestParserSplitDoesNotOverwriteMulti: a split marker on a
// multi-episode file flags nothing over "multi-episode".
func TestParserSplitDoesNotOverwriteMulti(t *testing.T) {
	p := mustParse(t, "/lib/Show S01E01-E02 CD1.mkv", "/lib", "episode")
	if p.Special != "multi-episode" {
		t.Fatalf("special = %q, want multi-episode", p.Special)
	}
	if len(p.Episodes) != 2 {
		t.Fatalf("episodes = %v", p.Episodes)
	}
}

// --- Item 6: NxN parity ------------------------------------------------

// TestParserNxNParity: uppercase X, chained continuations, and
// bare ranges behave like SxE; dimension strings never match.
func TestParserNxNParity(t *testing.T) {
	p := mustParse(t, "/lib/Show.Name.1X02.HDTV.x264.mkv", "/lib", "episode")
	if p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 2 {
		t.Fatalf("1X02: %+v", p)
	}
	p2 := mustParse(t, "/lib/Show 1x01-02.mkv", "/lib", "episode")
	if len(p2.Episodes) != 2 || p2.Episodes[0] != 1 || p2.Episodes[1] != 2 {
		t.Fatalf("1x01-02: episodes = %v, want [1 2]", p2.Episodes)
	}
	p3 := mustParse(t, "/lib/Show 1x01-1x02.mkv", "/lib", "episode")
	if len(p3.Episodes) != 2 || p3.Episodes[1] != 2 {
		t.Fatalf("1x01-1x02: episodes = %v, want [1 2]", p3.Episodes)
	}
	p4 := parseName("/lib/Show 1920x1080.mkv", "/lib")
	if p4.Kind == "episode" || len(p4.Episodes) != 0 {
		t.Fatalf("1920x1080 parsed as an episode: %+v", p4)
	}
}

// --- Item 7: split vocabulary ------------------------------------------

// TestParserSplitVocabulary: the completed marker set (pt, disk,
// dvd, …) flags split-file AND comes out of the dotted title, so
// the parts share one identity.
func TestParserSplitVocabulary(t *testing.T) {
	p1 := mustParse(t, "/lib/Movie.CD1.1998.1080p.mkv", "/lib", "movie")
	if p1.Title != "Movie" || p1.Year != 1998 || p1.Special != "split-file" {
		t.Fatalf("CD1: title/year/special = %q/%d/%q", p1.Title, p1.Year, p1.Special)
	}
	p2 := mustParse(t, "/lib/Movie.CD2.1998.1080p.mkv", "/lib", "movie")
	if p2.NormTitle != p1.NormTitle || p2.Year != p1.Year {
		t.Fatalf("CD1/CD2 identities differ: %q/%d vs %q/%d", p1.NormTitle, p1.Year, p2.NormTitle, p2.Year)
	}
	a := mustParse(t, "/lib/Film pt1 (2020).mkv", "/lib", "movie")
	b := mustParse(t, "/lib/Film pt2 (2020).mkv", "/lib", "movie")
	if a.Special != "split-file" || b.Special != "split-file" {
		t.Fatalf("pt1/pt2 specials = %q/%q, want split-file", a.Special, b.Special)
	}
	if a.Title != "Film" || b.Title != "Film" || a.NormTitle != b.NormTitle {
		t.Fatalf("pt1/pt2 titles = %q/%q, want a shared %q", a.Title, b.Title, "Film")
	}
	for _, name := range []string{"Movie Disk 1 (2020).mkv", "Movie DVD1 (2020).mkv", "Movie Disc 2 (2020).mkv"} {
		pp := mustParse(t, "/lib/"+name, "/lib", "movie")
		if pp.Special != "split-file" || pp.Title != "Movie" {
			t.Errorf("%s: special/title = %q/%q, want split-file/Movie", name, pp.Special, pp.Title)
		}
	}
}

// --- Item 8: folder year for movies -------------------------------------

// TestParserFolderYearForMovies: a yearless filename takes the
// parent folder's year when the folder carries exactly one; a
// filename year is never overridden.
func TestParserFolderYearForMovies(t *testing.T) {
	p := mustParse(t, "/lib/Avatar (2009)/Avatar.mkv", "/lib", "movie")
	if p.Title != "Avatar" || p.Year != 2009 {
		t.Fatalf("Avatar: title/year = %q/%d, want Avatar/2009", p.Title, p.Year)
	}
	p2 := mustParse(t, "/lib/Some Film (1999)/Some Film (2020).mkv", "/lib", "movie")
	if p2.Year != 2020 {
		t.Fatalf("filename year overridden: %d, want 2020", p2.Year)
	}
	p3 := mustParse(t, "/lib/Retro 1999 2001/movie film.mkv", "/lib", "movie")
	if p3.Year != 0 {
		t.Fatalf("two-year folder supplied a year: %d, want 0", p3.Year)
	}
}

// --- Item 9: fractional flag-only ---------------------------------------

// TestParserFractional: NN.5 records NO episode number, in both the
// SxE branch and the folder branch, and is flagged "fractional".
func TestParserFractional(t *testing.T) {
	p := mustParse(t, "/lib/Show S01E12.5.mkv", "/lib", "episode")
	if p.Season != 1 || len(p.Episodes) != 0 || p.Special != "fractional" {
		t.Fatalf("S01E12.5: season/episodes/special = %d/%v/%q", p.Season, p.Episodes, p.Special)
	}
	p2 := mustParse(t, "/lib/Great Show/Season 01/Great Show - 12.5.mkv", "/lib", "episode")
	if len(p2.Episodes) != 0 || p2.Special != "fractional" {
		t.Fatalf("folder 12.5: episodes/special = %v/%q", p2.Episodes, p2.Special)
	}
}

// TestDuplicatesFractionalNotGrouped: a fractional recap never
// joins the real episode's duplicate group.
func TestDuplicatesFractionalNotGrouped(t *testing.T) {
	real1 := parseName("/a/Show/Season 01/Show S01E12.mkv", "/a")
	real2 := parseName("/b/Show/Season 01/Show S01E12.mkv", "/b")
	frac := parseName("/a/Show/Season 01/Show S01E12.5.mkv", "/a")
	mk := func(path string, p ParsedName) *Item {
		it := mkEpisodeItem(path, p, 300_000_000, "h-"+path)
		it.Special = p.Special
		return it
	}
	items := []*Item{
		mk("/a/Show/Season 01/Show S01E12.mkv", real1),
		mk("/b/Show/Season 01/Show S01E12.mkv", real2),
		mk("/a/Show/Season 01/Show S01E12.5.mkv", frac),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 || len(groups[0].Copies) != 2 {
		t.Fatalf("groups = %v, want one group of the two real copies only", groups)
	}
}

// --- Item 10: folder-branch repairs --------------------------------------

// TestParserFolderBranchRepairs: the tokenizer splits on "-"
// (glued "Show-E02"), and ALL bare E-tokens are collected
// (deduped), not just the last.
func TestParserFolderBranchRepairs(t *testing.T) {
	p := mustParse(t, "/lib/Great Show/Season 01/Show-E02.mkv", "/lib", "episode")
	if len(p.Episodes) != 1 || p.Episodes[0] != 2 {
		t.Fatalf("glued Show-E02: episodes = %v, want [2]", p.Episodes)
	}
	p2 := mustParse(t, "/lib/Great Show/Season 01/Great Show E02 E03.mkv", "/lib", "episode")
	if len(p2.Episodes) != 2 || p2.Episodes[0] != 2 || p2.Episodes[1] != 3 {
		t.Fatalf("two E-tokens: episodes = %v, want [2 3]", p2.Episodes)
	}
	if p2.Special != "multi-episode" {
		t.Fatalf("two E-tokens special = %q, want multi-episode", p2.Special)
	}
	p3 := mustParse(t, "/lib/Great Show/Season 01/Great Show E02 E02.mkv", "/lib", "episode")
	if len(p3.Episodes) != 1 || p3.Episodes[0] != 2 {
		t.Fatalf("repeated E-token: episodes = %v, want [2] (deduped)", p3.Episodes)
	}
}
