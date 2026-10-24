package core

// parser_131_test.go — v1.3.1 fractional-episode narrowing
// (NAMING-ROUND-3-ANALYSIS.md Defect 2): a decimal is a
// fractional-episode marker only when it is attached to the
// episode token itself. The two real-tree victims below are
// verbatim basenames from the owner's first link-problems
// export; the true fractional shapes they narrow are pinned
// alongside so the narrowing cannot silently eat them.

import "testing"

func TestParser131Simpsons500KeysNotFractional(t *testing.T) {
	p := mustParse(t, "/lib/Simpsons/S20-S25/The.Simpsons.S22E21.500.Keys.1080p.WEBRip.DD5.1.x264.mkv", "/lib", "episode")
	if p.Season != 22 || len(p.Episodes) != 1 || p.Episodes[0] != 21 || p.Special != "" {
		t.Fatalf("500 Keys: season/episodes/special = %d/%v/%q, want 22/[21]/\"\"", p.Season, p.Episodes, p.Special)
	}
	if p.Title != "The Simpsons" {
		t.Fatalf("500 Keys: title = %q, want %q", p.Title, "The Simpsons")
	}
}

func TestParser131OnePieceLevel55NotFractional(t *testing.T) {
	p := mustParse(t, "/lib/[Anime Time] One Piece (0001-1071+Movies+Specials) [BD+CR] [Dual Audio] [1080p][HEVC 10bit x265][AAC][Multi Sub]/Season 13 - Impel Down/[Anime Time] One Piece - 0438 - A Paradise in Hell! Impel Down - Level 5.5!.mkv", "/lib", "episode")
	if p.Season != 13 || len(p.Episodes) != 1 || p.Episodes[0] != 438 || p.Special != "" {
		t.Fatalf("Level 5.5: season/episodes/special = %d/%v/%q, want 13/[438]/\"\"", p.Season, p.Episodes, p.Special)
	}
	if p.Title != "One Piece" {
		t.Fatalf("Level 5.5: title = %q, want %q", p.Title, "One Piece")
	}
}

func TestParser131TrueFractionalsStillFlagged(t *testing.T) {
	// SxE: ".5" followed by a tag separator is still fractional.
	p := mustParse(t, "/lib/Show.S01E12.5.1080p.WEBRip.mkv", "/lib", "episode")
	if p.Season != 1 || len(p.Episodes) != 0 || p.Special != "fractional" {
		t.Fatalf("S01E12.5.1080p: season/episodes/special = %d/%v/%q, want 1/[]/fractional", p.Season, p.Episodes, p.Special)
	}
	// A ".50" tail is not a ".5" fraction: episode 12 stands.
	p2 := mustParse(t, "/lib/Show.S01E12.50.mkv", "/lib", "episode")
	if p2.Season != 1 || len(p2.Episodes) != 1 || p2.Episodes[0] != 12 || p2.Special != "" {
		t.Fatalf("S01E12.50: season/episodes/special = %d/%v/%q, want 1/[12]/\"\"", p2.Season, p2.Episodes, p2.Special)
	}
	// Folder context: a bare "12.5" with no competing claim is
	// still fractional (pinned in TestParserFractional too).
	p3 := mustParse(t, "/lib/Great Show/Season 01/Great Show - 12.5.mkv", "/lib", "episode")
	if len(p3.Episodes) != 0 || p3.Special != "fractional" {
		t.Fatalf("folder 12.5: episodes/special = %v/%q, want []/fractional", p3.Episodes, p3.Special)
	}
	// Series-folder absolute: a strong number beats a title
	// decimal ("Level 5.5" is title text, 0438 is the episode).
	p4 := mustParse(t, "/lib/One Piece/One Piece - 0438 - A Paradise in Hell! Impel Down - Level 5.5!.mkv", "/lib", "episode")
	if p4.Season != 1 || len(p4.Episodes) != 1 || p4.Episodes[0] != 438 || p4.Special != "" {
		t.Fatalf("absolute 0438 + Level 5.5: season/episodes/special = %d/%v/%q, want 1/[438]/\"\"", p4.Season, p4.Episodes, p4.Special)
	}
}
