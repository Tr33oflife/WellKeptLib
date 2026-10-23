package core

// organize_test.go — tests for the v0.3.0 Organize planner.
// The planner is pure (reads the tree, writes nothing), so every
// case builds a small tree in a temp dir and asserts the plan.
// Coverage mirrors the agreed feature: Jellyfin targets for movies
// and episodes (specials, multi-episode, folder context), sidecar
// and artwork attachment, unidentified files left in place with
// reasons, collisions that exclude BOTH sides, banned-character
// sanitizing, and the confinement property (no target ever escapes
// the chosen root).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOrgFile creates one fixture file (and its parent folders).
func writeOrgFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustPlan runs the planner and fails the test on error.
func mustPlan(t *testing.T, root string) *OrganizePlan {
	t.Helper()
	plan, err := PlanOrganize(root)
	if err != nil {
		t.Fatalf("PlanOrganize(%s): %v", root, err)
	}
	return plan
}

// moveFor returns the planned move for one source path, or nil.
func moveFor(plan *OrganizePlan, from string) *OrganizeMove {
	for i := range plan.Moves {
		if plan.Moves[i].From == from {
			return &plan.Moves[i]
		}
	}
	return nil
}

// assertConfined checks the feature's core safety property for every
// move in a plan: both endpoints inside the plan root.
func assertConfined(t *testing.T, plan *OrganizePlan) {
	t.Helper()
	for _, mv := range plan.Moves {
		if !pathWithinRoot(plan.Root, mv.From) {
			t.Errorf("source %s escapes root %s", mv.From, plan.Root)
		}
		if !pathWithinRoot(plan.Root, mv.To) {
			t.Errorf("target %s escapes root %s", mv.To, plan.Root)
		}
	}
}

// wantMove asserts one planned move's exact target and kind.
func wantMove(t *testing.T, plan *OrganizePlan, from, to, kind string) {
	t.Helper()
	mv := moveFor(plan, from)
	if mv == nil {
		t.Fatalf("no move planned for %s; moves=%v unidentified=%v collisions=%v", from, plan.Moves, plan.Unidentified, plan.Collisions)
	}
	if mv.To != to {
		t.Errorf("move for %s: target = %s, want %s", from, mv.To, to)
	}
	if mv.Kind != kind {
		t.Errorf("move for %s: kind = %s, want %s", from, mv.Kind, kind)
	}
}

// wantUnidentified asserts a file was left alone with an exact reason.
func wantUnidentified(t *testing.T, plan *OrganizePlan, path, reason string) {
	t.Helper()
	for _, u := range plan.Unidentified {
		if u.Path == path {
			if u.Reason != reason {
				t.Errorf("unidentified %s: reason = %q, want %q", path, u.Reason, reason)
			}
			return
		}
	}
	t.Errorf("%s not in unidentified list %v", path, plan.Unidentified)
}

func TestPlanPlainMovie(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "The Matrix (1999).mkv")
	writeOrgFile(t, src, "matrix")
	plan := mustPlan(t, root)
	wantMove(t, plan, src, filepath.Join(root, "The Matrix (1999)", "The Matrix (1999).mkv"), "movie")
	if plan.Stats.Movies != 1 || plan.Stats.TotalMoves != 1 {
		t.Errorf("stats = %+v, want 1 movie / 1 move", plan.Stats)
	}
	assertConfined(t, plan)
}

func TestPlanDottedReleaseMovie(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv")
	writeOrgFile(t, src, "matrix")
	plan := mustPlan(t, root)
	// Release tags stripped, year taken from the name.
	wantMove(t, plan, src, filepath.Join(root, "The Matrix (1999)", "The Matrix (1999).mkv"), "movie")
	assertConfined(t, plan)
}

func TestPlanMovieFolderFallback(t *testing.T) {
	// Generic filename ("1080p.mkv" parses to no title at all): the
	// parser falls back to the folder name, which carries the year.
	root := t.TempDir()
	src := filepath.Join(root, "Generic Film (2005)", "1080p.mkv")
	writeOrgFile(t, src, "film")
	plan := mustPlan(t, root)
	wantMove(t, plan, src, filepath.Join(root, "Generic Film (2005)", "Generic Film (2005).mkv"), "movie")
	assertConfined(t, plan)
}

func TestPlanMovieNoYearUnidentified(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Some Random Movie.mkv")
	writeOrgFile(t, src, "film")
	plan := mustPlan(t, root)
	wantUnidentified(t, plan, src, "no year found")
	if len(plan.Moves) != 0 {
		t.Errorf("moves = %v, want none", plan.Moves)
	}
}

func TestPlanUnknownFileUnidentified(t *testing.T) {
	// "---.mkv" cleans to no title and no folder context: the parser
	// calls it unknown, and the planner leaves it with a plain reason.
	root := t.TempDir()
	src := filepath.Join(root, "---.mkv")
	writeOrgFile(t, src, "junk")
	plan := mustPlan(t, root)
	wantUnidentified(t, plan, src, "couldn't identify this file")
}

func TestPlanEpisodeSxE(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "My Show S01E02.mkv")
	writeOrgFile(t, src, "ep")
	plan := mustPlan(t, root)
	wantMove(t, plan, src, filepath.Join(root, "My Show", "Season 01", "My Show - S01E02.mkv"), "episode")
	assertConfined(t, plan)
}

func TestPlanEpisodeNxN(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "My Show 1x02.mkv")
	writeOrgFile(t, src, "ep")
	plan := mustPlan(t, root)
	wantMove(t, plan, src, filepath.Join(root, "My Show", "Season 01", "My Show - S01E02.mkv"), "episode")
	assertConfined(t, plan)
}

func TestPlanEpisodeFolderContext(t *testing.T) {
	// Bare "E05" filename; the folder supplies series + season.
	root := t.TempDir()
	src := filepath.Join(root, "My Show", "Season 02", "E05.mkv")
	writeOrgFile(t, src, "ep")
	plan := mustPlan(t, root)
	wantMove(t, plan, src, filepath.Join(root, "My Show", "Season 02", "My Show - S02E05.mkv"), "episode")
	assertConfined(t, plan)
}

func TestPlanEpisodeNoNumberUnidentified(t *testing.T) {
	// TV folder context but no episode number anywhere.
	root := t.TempDir()
	src := filepath.Join(root, "My Show", "Season 01", "random.mkv")
	writeOrgFile(t, src, "ep")
	plan := mustPlan(t, root)
	wantUnidentified(t, plan, src, "no episode number found")
}

func TestPlanEpisodeSpecialSeason00(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "My Show S00E03.mkv")
	writeOrgFile(t, src, "ep")
	plan := mustPlan(t, root)
	wantMove(t, plan, src, filepath.Join(root, "My Show", "Season 00", "My Show - S00E03.mkv"), "episode")
	assertConfined(t, plan)
}

func TestPlanMultiEpisodeForms(t *testing.T) {
	// Both spellings (S01E01E02 and S01E01-E02) canonicalize to the
	// S01E01-E02 target. Separate roots: in one tree the two files
	// would (correctly) collide with each other.
	for _, name := range []string{"My Show S01E01E02.mkv", "My Show S01E01-E02.mkv"} {
		root := t.TempDir()
		src := filepath.Join(root, name)
		writeOrgFile(t, src, "ep")
		plan := mustPlan(t, root)
		wantMove(t, plan, src, filepath.Join(root, "My Show", "Season 01", "My Show - S01E01-E02.mkv"), "episode")
		assertConfined(t, plan)
	}
}

func TestPlanEpisodeTitleDerivedAndOmitted(t *testing.T) {
	root := t.TempDir()
	withTitle := filepath.Join(root, "Show A S01E02 The Big Day 1080p.mkv")
	tagsOnly := filepath.Join(root, "Show B S01E02 1080p WEB-DL.mkv")
	writeOrgFile(t, withTitle, "ep")
	writeOrgFile(t, tagsOnly, "ep")
	plan := mustPlan(t, root)
	// Words after the marker survive as the episode title…
	wantMove(t, plan, withTitle, filepath.Join(root, "Show A", "Season 01", "Show A - S01E02 - The Big Day.mkv"), "episode")
	// …but when only release tags follow, no title segment appears.
	wantMove(t, plan, tagsOnly, filepath.Join(root, "Show B", "Season 01", "Show B - S01E02.mkv"), "episode")
	assertConfined(t, plan)
}

func TestPlanBasenameSidecars(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "Film (2010).mkv")
	sub := filepath.Join(root, "Film (2010).en.srt")
	nfo := filepath.Join(root, "Film (2010).nfo")
	writeOrgFile(t, video, "film")
	writeOrgFile(t, sub, "subs")
	writeOrgFile(t, nfo, "nfo")
	plan := mustPlan(t, root)
	dir := filepath.Join(root, "Film (2010)")
	wantMove(t, plan, video, filepath.Join(dir, "Film (2010).mkv"), "movie")
	// The suffix after the old basename survives exactly (.en.srt).
	wantMove(t, plan, sub, filepath.Join(dir, "Film (2010).en.srt"), "sidecar")
	wantMove(t, plan, nfo, filepath.Join(dir, "Film (2010).nfo"), "sidecar")
	if plan.Stats.Sidecars != 2 {
		t.Errorf("sidecar stats = %d, want 2", plan.Stats.Sidecars)
	}
	assertConfined(t, plan)
}

func TestPlanSidecarStaysWithUnidentifiedVideo(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "Mystery Film.mkv") // no year -> unidentified
	sub := filepath.Join(root, "Mystery Film.en.srt")
	writeOrgFile(t, video, "film")
	writeOrgFile(t, sub, "subs")
	plan := mustPlan(t, root)
	wantUnidentified(t, plan, video, "no year found")
	// The subtitle stays with its video: not moved, not "skipped"
	// (the video's own listing already explains the situation).
	if mv := moveFor(plan, sub); mv != nil {
		t.Errorf("subtitle of an unidentified video was planned to move: %+v", mv)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", plan.Skipped)
	}
}

func TestPlanFolderArtworkSingleWork(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "pack", "Heat (1995).mkv")
	poster := filepath.Join(root, "pack", "poster.jpg")
	writeOrgFile(t, video, "film")
	writeOrgFile(t, poster, "img")
	plan := mustPlan(t, root)
	wantMove(t, plan, poster, filepath.Join(root, "Heat (1995)", "poster.jpg"), "artwork")
	if plan.Stats.Artwork != 1 {
		t.Errorf("artwork stats = %d, want 1", plan.Stats.Artwork)
	}
	assertConfined(t, plan)
}

func TestPlanFolderArtworkMixedWorksSkipped(t *testing.T) {
	root := t.TempDir()
	writeOrgFile(t, filepath.Join(root, "mix", "Alpha (2001).mkv"), "a")
	writeOrgFile(t, filepath.Join(root, "mix", "Beta (2002).mkv"), "b")
	poster := filepath.Join(root, "mix", "poster.jpg")
	writeOrgFile(t, poster, "img")
	plan := mustPlan(t, root)
	if mv := moveFor(plan, poster); mv != nil {
		t.Fatalf("artwork in a two-work folder was planned to move: %+v", mv)
	}
	found := false
	for _, sk := range plan.Skipped {
		if sk.Path == poster && strings.Contains(sk.Reason, "more than one work") {
			found = true
		}
	}
	if !found {
		t.Errorf("poster not in skipped with a multi-work reason: %v", plan.Skipped)
	}
	if len(plan.Moves) != 2 {
		t.Errorf("moves = %v, want the two movies", plan.Moves)
	}
}

func TestPlanOrphanSubtitleSkipped(t *testing.T) {
	root := t.TempDir()
	writeOrgFile(t, filepath.Join(root, "Film (2010).mkv"), "film")
	orphan := filepath.Join(root, "Stray Subs.srt")
	writeOrgFile(t, orphan, "subs")
	plan := mustPlan(t, root)
	found := false
	for _, sk := range plan.Skipped {
		if sk.Path == orphan && sk.Reason == "sidecar with no matching video" {
			found = true
		}
	}
	if !found {
		t.Errorf("orphan subtitle not listed as skipped: %v", plan.Skipped)
	}
}

func TestPlanAlreadyOrganized(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Film (2010)", "Film (2010).mkv")
	writeOrgFile(t, src, "film")
	plan := mustPlan(t, root)
	if len(plan.Moves) != 0 {
		t.Errorf("moves = %v, want none", plan.Moves)
	}
	if len(plan.AlreadyOrganized) != 1 || plan.AlreadyOrganized[0] != src {
		t.Errorf("already_organized = %v, want [%s]", plan.AlreadyOrganized, src)
	}
	if plan.Stats.AlreadyOrganized != 1 {
		t.Errorf("stats = %+v, want already_organized 1", plan.Stats)
	}
}

func TestPlanCollisionTwoSourcesOneTarget(t *testing.T) {
	// Two different releases of one movie canonicalize to one name:
	// BOTH are collisions and NEITHER moves (no winner-picking).
	root := t.TempDir()
	a := filepath.Join(root, "Film.2010.1080p.mkv")
	b := filepath.Join(root, "Film.2010.720p.mkv")
	writeOrgFile(t, a, "a")
	writeOrgFile(t, b, "b")
	plan := mustPlan(t, root)
	if len(plan.Moves) != 0 {
		t.Errorf("moves = %v, want none", plan.Moves)
	}
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %v, want exactly 1", plan.Collisions)
	}
	c := plan.Collisions[0]
	if c.Target != filepath.Join(root, "Film (2010)", "Film (2010).mkv") {
		t.Errorf("collision target = %s", c.Target)
	}
	if len(c.Sources) != 2 {
		t.Errorf("collision sources = %v, want both files", c.Sources)
	}
}

func TestPlanCollisionTargetExists(t *testing.T) {
	// An organized copy already sits at the target; a second release
	// wanting the same name collides with it instead of overwriting.
	root := t.TempDir()
	existing := filepath.Join(root, "Film (2010)", "Film (2010).mkv")
	other := filepath.Join(root, "Film.2010.WEB-DL.mkv")
	writeOrgFile(t, existing, "kept")
	writeOrgFile(t, other, "other")
	plan := mustPlan(t, root)
	if len(plan.AlreadyOrganized) != 1 || plan.AlreadyOrganized[0] != existing {
		t.Errorf("already_organized = %v, want the existing file", plan.AlreadyOrganized)
	}
	if len(plan.Collisions) != 1 || len(plan.Collisions[0].Sources) != 1 || plan.Collisions[0].Sources[0] != other {
		t.Errorf("collisions = %v, want the loose release colliding", plan.Collisions)
	}
	if len(plan.Moves) != 0 {
		t.Errorf("moves = %v, want none", plan.Moves)
	}
}

func TestPlanBannedCharactersStripped(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Mission: Impossible (1996).mkv")
	writeOrgFile(t, src, "film")
	plan := mustPlan(t, root)
	want := filepath.Join(root, "Mission Impossible (1996)", "Mission Impossible (1996).mkv")
	wantMove(t, plan, src, want, "movie")
	if strings.Contains(want, ":") && filepath.Separator == '/' {
		// The ":" check is about the generated names, not the root.
		rel, _ := filepath.Rel(root, want)
		if strings.Contains(rel, ":") {
			t.Errorf("generated path still contains a colon: %s", rel)
		}
	}
	assertConfined(t, plan)
}

func TestPlanTitleSanitizesToEmpty(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "??? (2001).mkv")
	writeOrgFile(t, src, "film")
	plan := mustPlan(t, root)
	wantUnidentified(t, plan, src, "title is empty after removing banned characters")
	if len(plan.Moves) != 0 {
		t.Errorf("moves = %v, want none", plan.Moves)
	}
}

func TestPlanConfinementNastyNames(t *testing.T) {
	root := t.TempDir()
	// A backslash in the (Linux-legal) filename must be stripped by
	// sanitizing, never become a path separator in the target.
	bs := filepath.Join(root, `Back\Slash (2001).mkv`)
	writeOrgFile(t, bs, "film")
	// Dot-only titles cannot survive cleaning; they are unidentified,
	// never a ".." segment in a generated path.
	dots := filepath.Join(root, ".. (2001).mkv")
	writeOrgFile(t, dots, "film")
	plan := mustPlan(t, root)
	wantMove(t, plan, bs, filepath.Join(root, "BackSlash (2001)", "BackSlash (2001).mkv"), "movie")
	foundDots := false
	for _, u := range plan.Unidentified {
		if u.Path == dots {
			foundDots = true
		}
	}
	if !foundDots {
		t.Errorf("dot-title file not unidentified: %v", plan.Unidentified)
	}
	assertConfined(t, plan)
	// Segment-level check on top of the prefix property: no generated
	// segment may be "." or "..".
	for _, mv := range plan.Moves {
		rel, err := filepath.Rel(plan.Root, mv.To)
		if err != nil {
			t.Fatal(err)
		}
		for _, seg := range strings.Split(rel, string(filepath.Separator)) {
			if seg == "." || seg == ".." {
				t.Errorf("target %s contains segment %q", mv.To, seg)
			}
		}
	}
}

func TestPlanBadRoots(t *testing.T) {
	if _, err := PlanOrganize(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("nonexistent root: want an error")
	} else if !strings.Contains(err.Error(), "root not found") {
		t.Errorf("nonexistent root error = %q, want it to name the problem", err)
	}
	file := filepath.Join(t.TempDir(), "afile.mkv")
	writeOrgFile(t, file, "x")
	if _, err := PlanOrganize(file); err == nil {
		t.Error("file as root: want an error")
	} else if !strings.Contains(err.Error(), "root is not a folder") {
		t.Errorf("file-as-root error = %q, want it to name the problem", err)
	}
}
