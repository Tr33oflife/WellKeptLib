package core

// organize_039_test.go — v0.3.9 planner-side checks, built from
// the same real preview shapes as parser_039_test.go: the One
// Piece pack (fix 1 + 2 collision reshaping), the FLCL folder
// titles (fix 3), the release-group episode "titles" (fix 5) and
// the Digimon bare-E file (fix 6).

import (
	"path/filepath"
	"testing"
)

// TestOrganizeOnePiecePack039: the preview's headline collision,
// rebuilt as a fixture tree. Three copies of episode 0103: two in
// range folders ("Episode 001-206 Uncropped" at 1080p and 480p)
// and one in an arc-named season folder. After the fixes the two
// range copies plan to the same season-1 target and collide WITH
// EACH OTHER; the arc-folder copy has a season-4 identity of its
// own and plans cleanly; nothing is unidentified.
func TestOrganizeOnePiecePack039(t *testing.T) {
	root := t.TempDir()
	name := "[Anime Time] One Piece - 0103 - Spiders Café at 8 o'Clock! The Enemy Leaders Gather!.mkv"
	packDir := filepath.Join(root, onePiecePack)
	src1080 := filepath.Join(packDir, "Episode 001-206 Uncropped (1080p Upscale)", name)
	src480 := filepath.Join(packDir, "Episode 001-206 Uncropped (480p)", name)
	srcS04 := filepath.Join(packDir, "Season 04 - Arrival in Alabasta, Fierce Fighting in Alabasta", name)
	for _, src := range []string{src1080, src480, srcS04} {
		writeOrgFile(t, src, "x")
	}
	plan := mustPlan(t, root)
	assertConfined(t, plan)

	if len(plan.Unidentified) != 0 {
		t.Fatalf("unidentified = %v, want none", plan.Unidentified)
	}
	wantS04 := filepath.Join(root, "One Piece", "Season 04", "One Piece - S04E103.mkv")
	wantMove(t, plan, srcS04, wantS04, "episode")

	wantTarget := filepath.Join(root, "One Piece", "Season 01", "One Piece - S01E103.mkv")
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v, want exactly the range-copy pair", plan.Collisions)
	}
	col := plan.Collisions[0]
	if col.Target != wantTarget || len(col.Sources) != 2 {
		t.Fatalf("collision = %+v, want target %s with 2 sources", col, wantTarget)
	}
	if moveFor(plan, src1080) != nil || moveFor(plan, src480) != nil {
		t.Fatalf("a range copy was planned to move despite the collision: %v", plan.Moves)
	}
}

// TestOrganizeFLCL039: the six FLCL files plan into the folder
// title's series folder with their episode titles intact.
func TestOrganizeFLCL039(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "FLCL (2000) [1080p x265 HEVC 10bit BluRay Dual Audio AAC] [Prof]")
	names := []string{
		"S01E01 - Fooly Cooly", "S01E02 - Firestarter", "S01E03 - Marquis de Carabas",
		"S01E04 - Full Swing", "S01E05 - Brittle Bulletw", "S01E06 - FLCLimax",
	}
	for _, n := range names {
		writeOrgFile(t, filepath.Join(dir, "[Prof] "+n+".mkv"), "x")
	}
	plan := mustPlan(t, root)
	assertConfined(t, plan)
	if len(plan.Unidentified) != 0 {
		t.Fatalf("unidentified = %v, want none", plan.Unidentified)
	}
	wantMove(t, plan,
		filepath.Join(dir, "[Prof] S01E01 - Fooly Cooly.mkv"),
		filepath.Join(root, "FLCL (2000)", "Season 01", "FLCL - S01E01 - Fooly Cooly.mkv"),
		"episode")
	wantMove(t, plan,
		filepath.Join(dir, "[Prof] S01E06 - FLCLimax.mkv"),
		filepath.Join(root, "FLCL (2000)", "Season 01", "FLCL - S01E06 - FLCLimax.mkv"),
		"episode")
}

// TestOrganizeGroupIsNotEpisodeTitle039: the preview planned the
// episode titles "SYNCOPY" and "ELiTE" — release groups glued to
// the quality tail. After fix 5 both targets are title-free.
// (v0.4.0 update: when this test was written the parser reported
// no year for the Scrubs scene name and the series folder was
// yearless. NAMING-RESEARCH-2 §1 fix 1 now harvests the series
// year from the pre-marker chunk — "Scrubs.2026.S02E05…" is the
// same dotted TitleYear shape as the memo's acceptance case —
// so the Scrubs target folder carries (2026). The Simpsons name
// has no year and its target is unchanged.)
func TestOrganizeGroupIsNotEpisodeTitle039(t *testing.T) {
	root := t.TempDir()
	scrubs := filepath.Join(root, "Scrubs.2026.S02E05.1080p.HDTV.x264-SYNCOPY",
		"Scrubs.2026.S02E05.1080p.HDTV.x264-SYNCOPY.mkv")
	simpsons := filepath.Join(root, "Simpsons", "S37", "The.Simpsons.S37E02.1080p.x265-ELiTE.mkv")
	writeOrgFile(t, scrubs, "x")
	writeOrgFile(t, simpsons, "x")
	plan := mustPlan(t, root)
	assertConfined(t, plan)
	wantMove(t, plan, scrubs,
		filepath.Join(root, "Scrubs (2026)", "Season 02", "Scrubs - S02E05.mkv"), "episode")
	wantMove(t, plan, simpsons,
		filepath.Join(root, "The Simpsons", "Season 37", "The Simpsons - S37E02.mkv"), "episode")
}

// TestEpisodeTitleSpacedTitleUnaffected039: the group strip must
// never touch a spaced human episode title.
func TestEpisodeTitleSpacedTitleUnaffected039(t *testing.T) {
	if got := episodeTitleFromName("Scrubs S02E05 - My Title"); got != "My Title" {
		t.Fatalf("episodeTitleFromName = %q, want \"My Title\"", got)
	}
	if got := episodeTitleFromName("Scrubs.2026.S02E05.1080p.HDTV.x264-SYNCOPY"); got != "" {
		t.Fatalf("episodeTitleFromName = %q, want \"\" (group stripped)", got)
	}
}

// TestOrganizeDigimon039: the bare-E file plans as an episode.
func TestOrganizeDigimon039(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Digimon.Beatbreak.E33.1080p.WEBRip.10bit.AAC.x265-Xiangliu.mkv")
	writeOrgFile(t, src, "x")
	plan := mustPlan(t, root)
	assertConfined(t, plan)
	wantMove(t, plan, src,
		filepath.Join(root, "Digimon Beatbreak", "Season 01", "Digimon Beatbreak - S01E33.mkv"),
		"episode")
}
