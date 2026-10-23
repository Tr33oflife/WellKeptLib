package core

// parser_039_test.go — v0.3.9 parser fixes, every case reproduced
// from the user's REAL Organize preview output on his SSHFS-mounted
// library (the One Piece pack collisions, the FLCL "title is empty"
// rows, the Adventure Time extras rows, the Digimon "no year found"
// row). Paths below are the preview's relative shapes verbatim,
// hung under a fake root the way the tests in this package do.

import (
	"path/filepath"
	"testing"
)

// The One Piece pack folder name, verbatim from the preview.
const onePiecePack = "[Anime Time] One Piece (0001-1071+Movies+Specials) [BD+CR] [Dual Audio] [1080p][HEVC 10bit x265][AAC][Multi Sub]"

func onePieceName(num, tail string) string {
	return "[Anime Time] One Piece - " + num + " - " + tail + ".mkv"
}

// --- Fix 1: leading-zero absolute episode numbers -------------------

// TestParserStrongAbsoluteOnePiece: the range-folder copies from
// the preview. The old series-folder absolute rule took the LAST
// weak token — the "8" from "8 o'Clock" — and planned episode 8
// with the title "One Piece 0103 Spiders Café at"; "0151" landed
// on the "100" from "100 Million Man". The strong leading-zero
// candidate ("0103") is the episode, and the title is the text
// before it.
func TestParserStrongAbsoluteOnePiece(t *testing.T) {
	root := "/TV"
	dir := filepath.Join(root, onePiecePack, "Episode 001-206 Uncropped (1080p Upscale)")

	p := parseName(filepath.Join(dir, onePieceName("0103", "Spiders Café at 8 o'Clock! The Enemy Leaders Gather!")), root)
	if p.Kind != "episode" || p.Title != "One Piece" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 103 {
		t.Fatalf("0103 parsed as %+v, want episode \"One Piece\" S1 [103]", p)
	}
	p = parseName(filepath.Join(dir, onePieceName("0151", "100 Million Man! World's Greatest Power and Pirate Black Beard!")), root)
	if p.Kind != "episode" || p.Title != "One Piece" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 151 {
		t.Fatalf("0151 parsed as %+v, want episode \"One Piece\" S1 [151]", p)
	}
}

// TestParserStrongAbsoluteFolderSeason: the same strong priority
// inside a season folder (fix 1's second half) — the file under a
// "Season 04" folder keeps season 4, and the strong token still
// beats the "8" later in the episode title.
func TestParserStrongAbsoluteFolderSeason(t *testing.T) {
	root := "/TV"
	dir := filepath.Join(root, "Show Pack", "Season 04 - Arrival in Alabasta, Fierce Fighting in Alabasta")
	p := parseName(filepath.Join(dir, "Show - 0103 - Spiders Café at 8 o'Clock!.mkv"), root)
	if p.Kind != "episode" || p.Season != 4 || len(p.Episodes) != 1 || p.Episodes[0] != 103 {
		t.Fatalf("parsed as %+v, want episode S4 [103]", p)
	}
}

// TestStrongEpisodeNumber: the strong shape itself — exactly four
// digits, leading zero, value 1–999. Years and resolutions can
// never look like this.
func TestStrongEpisodeNumber(t *testing.T) {
	for tok, want := range map[string]int{"0103": 103, "0151": 151, "0007": 7, "0999": 999} {
		if n, ok := strongEpisodeNumber(tok); !ok || n != want {
			t.Errorf("strongEpisodeNumber(%q) = %d, %v; want %d, true", tok, n, ok, want)
		}
	}
	for _, tok := range []string{"0000", "103", "01030", "1999", "1080", "abcd", " 103"} {
		if _, ok := strongEpisodeNumber(tok); ok {
			t.Errorf("strongEpisodeNumber(%q) = true, want false", tok)
		}
	}
}

// --- Fix 2: "Season NN - <arc name>" folders ------------------------

// TestParserSeasonArcFolder: the preview's third One Piece copy
// sat in "Season 04 - Arrival in Alabasta, Fierce Fighting in
// Alabasta", which the anchored reSeason could not see at all;
// it parsed (wrongly) as a season-1 absolute episode and
// collided with the range-folder copies. Now the folder gives
// season 4, the series title comes from the pack folder with its
// paren furniture stripped ("(0001-1071+Movies+Specials)" must
// not leak into the title), and the number is still 103.
func TestParserSeasonArcFolder(t *testing.T) {
	root := "/TV"
	dir := filepath.Join(root, onePiecePack, "Season 04 - Arrival in Alabasta, Fierce Fighting in Alabasta")
	p := parseName(filepath.Join(dir, onePieceName("0103", "Spiders Café at 8 o'Clock! The Enemy Leaders Gather!")), root)
	if p.Kind != "episode" || p.Title != "One Piece" || p.Season != 4 || len(p.Episodes) != 1 || p.Episodes[0] != 103 {
		t.Fatalf("Season 04 copy parsed as %+v, want episode \"One Piece\" S4 [103]", p)
	}
	dir6 := filepath.Join(root, onePiecePack, "Season 06 - Sky Island ~ Skypiea, The Golden Bell")
	p = parseName(filepath.Join(dir6, onePieceName("0151", "100 Million Man! World's Greatest Power and Pirate Black Beard!")), root)
	if p.Kind != "episode" || p.Title != "One Piece" || p.Season != 6 || len(p.Episodes) != 1 || p.Episodes[0] != 151 {
		t.Fatalf("Season 06 copy parsed as %+v, want episode \"One Piece\" S6 [151]", p)
	}
}

// TestSeasonRegexSuffixGuard: the widened season pattern still
// refuses a folder with no digits after "season" — "Season of
// the Witch" is a title, not a season context.
func TestSeasonRegexSuffixGuard(t *testing.T) {
	if m := reSeason.FindStringSubmatch("Season of the Witch"); m != nil {
		t.Fatalf("reSeason matched %q: %v", "Season of the Witch", m)
	}
	m := reSeason.FindStringSubmatch("Season 04 - Arrival in Alabasta, Fierce Fighting in Alabasta")
	if m == nil || m[1] != "04" {
		t.Fatalf("reSeason on arc folder = %v, want group 1 = \"04\"", m)
	}
	// A movie inside a "Season of the Witch" folder stays a movie.
	p := mustParse(t, "/lib/Season of the Witch/Movie Title (2020).mkv", "/lib", "movie")
	if p.Year != 2020 {
		t.Fatalf("parsed %+v, want movie year 2020", p)
	}
}

// --- Fix 3: episode title fallback to the parent folder -------------

// TestParserFLCLFolderTitle: the preview's six "title is empty
// after removing banned characters" rows. "[Prof] S01E01 - ….mkv"
// carries no series name at all; the series lives in the folder
// name "FLCL (2000) […] [Prof]". The episode fixup now derives
// the title (and the lone folder year) from that folder.
func TestParserFLCLFolderTitle(t *testing.T) {
	root := "/TV"
	dir := filepath.Join(root, "FLCL (2000) [1080p x265 HEVC 10bit BluRay Dual Audio AAC] [Prof]")
	names := []string{
		"S01E01 - Fooly Cooly", "S01E02 - Firestarter", "S01E03 - Marquis de Carabas",
		"S01E04 - Full Swing", "S01E05 - Brittle Bulletw", "S01E06 - FLCLimax",
	}
	for i, n := range names {
		p := parseName(filepath.Join(dir, "[Prof] "+n+".mkv"), root)
		if p.Kind != "episode" || p.Title != "FLCL" || p.Year != 2000 || p.Season != 1 ||
			len(p.Episodes) != 1 || p.Episodes[0] != i+1 {
			t.Fatalf("%s parsed as %+v, want episode \"FLCL\" 2000 S1 [%d]", n, p, i+1)
		}
	}
}

// --- Fix 4: extras detection for pack layouts -----------------------

// TestParserExtrasPackLayouts: the preview's Adventure Time rows
// — extras living in pack folders ("….S01.EXTRAS.BDRip.x264-…"
// inside a season pack, "….S07.Extras.DVDRip.…") with "extra-…"
// stems. Neither the suffix rule nor the exact-folder rule could
// see them, so they fell through as unidentified episodes.
func TestParserExtrasPackLayouts(t *testing.T) {
	root := "/TV"
	pack := filepath.Join(root, "Adventure.Time.With.Finn.And.Jake.S01-S10.DVDRip.XviD.BDRip.x264-Scene")

	p := parseName(filepath.Join(pack, "S01",
		"Adventure.Time.With.Finn.And.Jake.S01.EXTRAS.BDRip.x264-DEiMOS",
		"Adventure.Time.Behind.The.Scenes.Featurette.BDRip.x264-DEiMOS.mkv"), root)
	if p.Kind != "extra" {
		t.Fatalf("S01 EXTRAS file parsed as %+v, want kind extra", p)
	}
	p = parseName(filepath.Join(pack, "S07",
		"Adventure.Time.S07.Extras.DVDRip.x264-W4F",
		"adventure.time.s07.extra-animatics-football.dvdrip.x264-w4f.mkv"), root)
	if p.Kind != "extra" {
		t.Fatalf("S07 Extras file parsed as %+v, want kind extra", p)
	}
}

// TestExtrasPackGuards: the new extras rules are guarded — a
// normal episode under a plain season folder stays an episode,
// and a series folder that is merely NAMED "Extras (2005)" does
// not trip the folder-token rule (no season/tag token in it).
func TestExtrasPackGuards(t *testing.T) {
	p := mustParse(t, "/lib/Series/Season 01/Series - S01E05.mkv", "/lib", "episode")
	if len(p.Episodes) != 1 || p.Episodes[0] != 5 {
		t.Fatalf("parsed %+v, want episode [5]", p)
	}
	if _, isExtra := classifyExtra("Series - 05", []string{"Extras (2005)"}); isExtra {
		t.Fatal("classifyExtra fired for a file in a plain \"Extras (2005)\" series folder")
	}
	p = parseName("/lib/Extras (2005)/[Group] Series - 05.mkv", "/lib")
	if p.Kind != "episode" || len(p.Episodes) != 1 || p.Episodes[0] != 5 {
		t.Fatalf("file in \"Extras (2005)\" folder parsed as %+v, want episode [5] via absolute numbering", p)
	}
}

// --- Fix 6: standalone bare-E episode marker ------------------------

// TestParserBareEMarker: the preview's Digimon row —
// "Digimon.Beatbreak.E33.1080p.…" directly in the root parsed
// as a yearless movie ("no year found"). The bounded bare-E
// branch claims it as episode 33 of season 1.
func TestParserBareEMarker(t *testing.T) {
	p := parseName("/lib/Digimon.Beatbreak.E33.1080p.WEBRip.10bit.AAC.x265-Xiangliu.mkv", "/lib")
	if p.Kind != "episode" || p.Title != "Digimon Beatbreak" || p.Season != 1 ||
		len(p.Episodes) != 1 || p.Episodes[0] != 33 {
		t.Fatalf("Digimon parsed as %+v, want episode \"Digimon Beatbreak\" S1 [33]", p)
	}
}

// TestParserBareEGuards: the branch is bounded — a one-digit
// "E5" never fires, and any year token in the stem vetoes it
// (the file is a movie's business).
func TestParserBareEGuards(t *testing.T) {
	p := parseName("/lib/Show.E5.1080p.WEBRip.mkv", "/lib")
	if p.Kind == "episode" {
		t.Fatalf("E5 took the bare-E branch: %+v", p)
	}
	p = mustParse(t, "/lib/Show.E33.2020.1080p.mkv", "/lib", "movie")
	if p.Year != 2020 {
		t.Fatalf("year-veto case parsed as %+v, want movie year 2020", p)
	}
}
