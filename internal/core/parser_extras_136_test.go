package core

// parser_extras_136_test.go — v1.3.6: the pack-extras folder
// rule (extraByPackFolder) narrowed to ADJACENCY. Before the
// narrowing, an extras word anywhere in a folder part plus a
// season/tag token anywhere in the same part classified every
// file in the folder as an extra. The owner's real tree has a
// folder named "S06 w-extra ep11" — season 6 WITH an extra
// episode — whose twelve real episodes were classified
// "extra content — left in place" and got no links. Now the
// extras word must sit immediately beside a context token
// (bare season, "season", or a release-tag token), the way it
// does in every genuine release-pack extras folder.
//
// The corpus side of this change (843 real basenames, exactly
// these twelve flips, zero other deltas) is pinned by
// TestBulkExport131ParserRegression in bulk_export_131_test.go;
// this file pins the shapes individually.

import (
	"path/filepath"
	"strings"
	"testing"
)

const extras136Root = "/home/user/downloads/rtorrent/TV"

// extras136DirParts derives dirParts from a path exactly the
// way parseName does (relative dir under the root, split).
func extras136DirParts(path string) []string {
	rel, err := filepath.Rel(extras136Root, filepath.Dir(path))
	if err != nil || rel == "." {
		return nil
	}
	return strings.Split(rel, string(filepath.Separator))
}

func extras136Stem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// The twelve real episodes from the folder "S06 w-extra ep11"
// must parse as the episodes they are — none of them an extra.
func TestExtras136WEFolderEpisodesParse(t *testing.T) {
	folder := extras136Root + "/What We Do in the Shadows S01-S06 web 10bit hevc-d3g/S06 w-extra ep11/"
	cases := []struct {
		base string
		ep   int
	}{
		{"What.We.Do.in.the.Shadows.S06E01.The.Return.of.Jerry.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 1},
		{"What.We.Do.in.the.Shadows.S06E02.Headhunting.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 2},
		{"What.We.Do.in.the.Shadows.S06E03.Sleep.Hypnosis.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 3},
		{"What.We.Do.in.the.Shadows.S06E04.The.Railroad.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 4},
		{"What.We.Do.in.the.Shadows.S06E05.Nandors.Army.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 5},
		{"What.We.Do.in.the.Shadows.S06E06.Laszlos.Father.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 6},
		{"What.We.Do.in.the.Shadows.S06E07.March.Madness.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 7},
		{"What.We.Do.in.the.Shadows.S06E08.P.I.Undercover.New.York.1080p.AMZN.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 8},
		{"What.We.Do.in.the.Shadows.S06E09.Come.Out.And.Play.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 9},
		{"What.We.Do.in.the.Shadows.S06E10.The.Promotion.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 10},
		{"What.We.Do.in.the.Shadows.S06E11.The.Finale.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 11},
		// The B-suffixed alternate-ending version parses as
		// plain episode 11 — the B is not a distinct episode
		// identity (so in a link plan it meets S06E11 as a
		// collision for the same target, keeper-ranked).
		{"What.We.Do.in.the.Shadows.S06E11B.The.Finale.and.Alternate.Ending.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv", 11},
	}
	for _, c := range cases {
		path := folder + c.base
		if _, isExtra := classifyExtra(extras136Stem(path), extras136DirParts(path)); isExtra {
			t.Errorf("%s: classifyExtra = true, want false (\"w-extra\" folder is not a pack extras folder)", c.base)
		}
		got := parseName(path, extras136Root)
		if got.Kind != "episode" || got.Title != "What We Do in the Shadows" || got.Season != 6 ||
			len(got.Episodes) != 1 || got.Episodes[0] != c.ep || got.Special != "" {
			t.Errorf("%s: parse = kind/title/season/episodes/special %q/%q/%d/%v/%q, want episode/\"What We Do in the Shadows\"/6/[%d]/\"\"",
				c.base, got.Kind, got.Title, got.Season, got.Episodes, got.Special, c.ep)
		}
	}
}

// Every genuine extras shape from the owner's tree must STILL
// classify as an extra, each asserted individually with the
// rule that fires for it named.
func TestExtras136GenuineExtrasStayExtra(t *testing.T) {
	pack := extras136Root + "/Adventure.Time.With.Finn.And.Jake.S01-S10.DVDRip.XviD.BDRip.x264-Scene"
	earl := extras136Root + "/My.Name.Is.Earl.S01-S04.DVDRip.XviD-SCENE"
	cases := []struct {
		name      string
		path      string
		packRule  bool // extraByPackFolder must fire
		stemRule  bool // extraByStemTokens must fire
		ruleNames string
	}{
		{
			name:      "Adventure Time S01 EXTRAS pack folder (extras glued to S01/BDRip)",
			path:      pack + "/S01/Adventure.Time.With.Finn.And.Jake.S01.EXTRAS.BDRip.x264-DEiMOS/Adventure.Time.Animatic.For.S01E01.Slumber.Party.Panic.BDRip.x264-DEiMOS.mkv",
			packRule:  true,
			ruleNames: "extraByPackFolder",
		},
		{
			name:      "Adventure Time S03 EXTRAS pack folder",
			path:      pack + "/S03/Adventure.Time.With.Finn.And.Jake.S03.EXTRAS.BDRip.x264-DEiMOS/Adventure.Time.With.Finn.And.Jake.S03E01.Audio.Commentary.mp4",
			packRule:  true,
			ruleNames: "extraByPackFolder",
		},
		{
			name:      "Adventure Time S04 EXTRAS pack folder",
			path:      pack + "/S04/Adventure.Time.With.Finn.And.Jake.S04.EXTRAS.BDRip.x264-DEiMOS/Adventure.Time.With.Finn.And.Jake.S04E01.Audio.Commentary.mp4",
			packRule:  true,
			ruleNames: "extraByPackFolder",
		},
		{
			name:      "Adventure Time S07 Extras folder + s07.extra stem",
			path:      pack + "/S07/Adventure.Time.S07.Extras.DVDRip.x264-W4F/adventure.time.s07.extra-animatics-football.dvdrip.x264-w4f.mkv",
			packRule:  true,
			stemRule:  true,
			ruleNames: "extraByPackFolder + extraByStemTokens",
		},
		{
			name:      "My Name Is Earl S01 Extras pack folder + stem tokens",
			path:      earl + "/S01/My.Name.Is.Earl.S01.Extras.DVDRip.XviD-FUtV/Sample/sample-my.name.is.earl.s01.extras.dvdrip.xvid-futv.avi",
			packRule:  true,
			stemRule:  true,
			ruleNames: "extraByPackFolder + extraByStemTokens",
		},
		{
			name:      "My Name Is Earl S03 EXTRAS pack folder",
			path:      earl + "/S03/My.Name.is.Earl.S03.EXTRAS.DVDRip.XviD-NODLABS/Sample/mnie.s03.extras.dvdrip.sample.xvid-nodlabs.avi",
			packRule:  true,
			stemRule:  true,
			ruleNames: "extraByPackFolder + extraByStemTokens",
		},
		{
			// "extras" is the final token; its only neighbour
			// is "dvd" — a release-tag token — so adjacency
			// holds and the pack rule keeps firing.
			name:      "DS9 Season 3 DVD Extras folder (extras adjacent to the 'dvd' tag token)",
			path:      extras136Root + "/Star Trek Deep Space Nine/Season 3/Star Trek Deep Space Nine - Season 3 - DVD Extras/Crew Dossier Odo.mkv",
			packRule:  true,
			ruleNames: "extraByPackFolder (adjacency via tag token \"dvd\")",
		},
		{
			name:      "DS9 Season 4 DVD Extras folder",
			path:      extras136Root + "/Star Trek Deep Space Nine/Season 4/Star Trek Deep Space Nine - Season 4 - DVD Extras/Crew Dossier Worf.mkv",
			packRule:  true,
			ruleNames: "extraByPackFolder (adjacency via tag token \"dvd\")",
		},
		{
			// The folder's own label is "0 Extras" and the app
			// follows it (the owner's stated position): the
			// extras word is adjacent to "0", a tag token (the
			// DD 5.1 audio-digit set), so the outcome is
			// unchanged by the narrowing.
			name:      "Simpsons Treehouse pair in the folder named '0 Extras'",
			path:      extras136Root + "/The Simpsons- Treehouse of Horror S01-S36 COMPLETE Fixed+Updated jZQ/0 Extras/S31E08 Thanksgiving of Horror IMDB 7.4.mkv",
			packRule:  true,
			ruleNames: "extraByPackFolder (adjacency via tag token \"0\")",
		},
		{
			name:      "Simpsons Treehouse pair, second file",
			path:      extras136Root + "/The Simpsons- Treehouse of Horror S01-S36 COMPLETE Fixed+Updated jZQ/0 Extras/S34E05 Not It IMDB 7.6.mkv",
			packRule:  true,
			ruleNames: "extraByPackFolder (adjacency via tag token \"0\")",
		},
	}
	for _, c := range cases {
		path := c.path
		dirParts := extras136DirParts(path)
		if got := extraByPackFolder(dirParts); got != c.packRule {
			t.Errorf("%s: extraByPackFolder = %v, want %v (rules: %s)", c.name, got, c.packRule, c.ruleNames)
		}
		if got := extraByStemTokens(extras136Stem(path)); got != c.stemRule {
			t.Errorf("%s: extraByStemTokens = %v, want %v (rules: %s)", c.name, got, c.stemRule, c.ruleNames)
		}
		if _, isExtra := classifyExtra(extras136Stem(path), dirParts); !isExtra {
			t.Errorf("%s: classifyExtra = false, want true (rules: %s)", c.name, c.ruleNames)
		}
		if got := parseName(path, extras136Root); got.Kind != "extra" {
			t.Errorf("%s: parseName kind = %q, want \"extra\"", c.name, got.Kind)
		}
	}
}

// An extras token whose neighbours are all non-context never
// fires the pack rule — even when a season token exists
// elsewhere in the same folder part (the pre-1.3.6 rule fired
// on exactly that combination).
func TestExtras136NonAdjacentExtrasTokenNeverFires(t *testing.T) {
	noFire := []string{
		"S06 w-extra ep11",            // the defect shape: neighbours "w" and "ep11"
		"Show Extras Collection S01",  // extras separated from S01 by a title word
		"Show Bonus Disc S01",         // bonus separated from S01 by "disc"
		"Some Show w-extras included", // plural, same natural-language shape
	}
	for _, part := range noFire {
		if extraByPackFolder([]string{part}) {
			t.Errorf("extraByPackFolder(%q) = true, want false (extras token has no context neighbour)", part)
		}
	}
	fire := []string{
		"Show.S01.EXTRAS.BDRip.x264-GRP",                    // glued between season and tag tokens
		"Star Trek Deep Space Nine - Season 4 - DVD Extras", // final token, "dvd" tag neighbour
		"0 Extras",        // "0" tag-token neighbour
		"Extras Season 1", // "season" word neighbour
		"Show S01 Extras", // bare-season neighbour
	}
	for _, part := range fire {
		if !extraByPackFolder([]string{part}) {
			t.Errorf("extraByPackFolder(%q) = false, want true (extras token has a context neighbour)", part)
		}
	}
	// Parse level: an episode file inside a "w-extra" folder
	// for a different show/season parses as its episode.
	path := extras136Root + "/Some Show/S02 w-extra ep03/Some.Show.S02E03.1080p.WEB-GRP.mkv"
	got := parseName(path, extras136Root)
	if got.Kind != "episode" || got.Season != 2 || len(got.Episodes) != 1 || got.Episodes[0] != 3 {
		t.Errorf("w-extra folder episode: parse = kind/season/episodes %q/%d/%v, want episode/2/[3]", got.Kind, got.Season, got.Episodes)
	}
}
