package core

// duplicates_test.go — grouping logic, edition separation, exact-dup
// flags, and the exclusion behavior (Maintainerr pattern):
//  - an excluded path produces no finding for its scope, but still
//    produces findings for other scopes;
//  - a duplicate group reduced below 2 copies disappears.

import "testing"

// mkItem builds a minimal movie Item for grouping tests.
func mkItem(path, title string, year int, edition string, size int64, height int, hash string) *Item {
	return &Item{
		Path: path, Kind: "movie", Title: title, NormTitle: normalizeTitle(title),
		Year: year, Edition: edition, Size: size, Height: height,
		ResolutionLabel: resolutionLabel(0, height), Duration: 3600, Bitrate: 8_000_000,
		SampleHash: hash, ProbeOK: true, HasVideo: true,
	}
}

// testCfg is the grouping config used across these tests.
func testCfg() *Config {
	return &Config{DurationTolerancePct: 3, DurationToleranceSec: 120, TinyFileFloorBytes: 1024 * 1024, SubtitleConfidence: 0.75, Workers: 2}
}

// TestDuplicatesBasic: two encodes of one movie form one confirmed group.
func TestDuplicatesBasic(t *testing.T) {
	items := []*Item{
		mkItem("/lib/a.mkv", "Same Film", 2020, "", 4_000_000_000, 1080, "h1"),
		mkItem("/lib/b.mp4", "Same Film", 2020, "", 1_500_000_000, 720, "h2"),
		mkItem("/lib/c.mkv", "Other Film", 2021, "", 2_000_000_000, 1080, "h3"),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if len(g.Copies) != 2 || g.Status != "confirmed" {
		t.Fatalf("copies=%d status=%q", len(g.Copies), g.Status)
	}
	if g.KeeperPath != "/lib/a.mkv" {
		t.Fatalf("keeper = %q (want the 1080p copy)", g.KeeperPath)
	}
	if g.WastedBytes != 1_500_000_000 {
		t.Fatalf("wasted = %d", g.WastedBytes)
	}
}

// TestDuplicatesEditionsSeparate: editions do not merge, they cross-link.
func TestDuplicatesEditionsSeparate(t *testing.T) {
	items := []*Item{
		mkItem("/lib/a.mkv", "Same Film", 2020, "", 4_000_000_000, 1080, "h1"),
		mkItem("/lib/b.mp4", "Same Film", 2020, "", 1_500_000_000, 720, "h2"),
		mkItem("/lib/c.mkv", "Same Film", 2020, "Extended", 5_000_000_000, 1080, "h3"),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (Extended singleton is not a group)", len(groups))
	}
	g := groups[0]
	if g.Edition != "" || len(g.Copies) != 2 {
		t.Fatalf("merged editions! group edition=%q copies=%d", g.Edition, len(g.Copies))
	}
	if len(g.CrossLinks) != 1 || g.CrossLinks[0].Edition != "Extended" {
		t.Fatalf("cross links = %+v", g.CrossLinks)
	}
}

// TestDuplicatesExact: same size + same sampled hash => exact flag.
func TestDuplicatesExact(t *testing.T) {
	items := []*Item{
		mkItem("/lib/a.mkv", "Same Film", 2020, "", 2_000_000_000, 1080, "samehash"),
		mkItem("/lib/b.mkv", "Same Film", 2020, "", 2_000_000_000, 1080, "samehash"),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 || !groups[0].HasExact {
		t.Fatalf("expected one group with HasExact, got %+v", groups)
	}
}

// TestExclusionScopeIsolation: an exclusion for one scope suppresses
// that scope's findings for the path but leaves other scopes working.
func TestExclusionScopeIsolation(t *testing.T) {
	ex := []Exclusion{{Path: "/lib/x.en.srt", Scope: "subtitles"}}
	if !isExcluded(ex, "/lib/x.en.srt", "subtitles") {
		t.Fatal("subtitles scope should be excluded")
	}
	if isExcluded(ex, "/lib/x.en.srt", "health") {
		t.Fatal("health scope must NOT be excluded by a subtitles exclusion")
	}
	if isExcluded(ex, "/lib/x.en.srt", "duplicates") {
		t.Fatal("duplicates scope must NOT be excluded")
	}
	// Folder exclusion + "all" scope.
	ex2 := []Exclusion{{Path: "/lib/folder", Scope: "all"}}
	if !isExcluded(ex2, "/lib/folder/movie.mkv", "health") {
		t.Fatal("folder + all scope should exclude children for every scope")
	}
	if isExcluded(ex2, "/lib/other/movie.mkv", "health") {
		t.Fatal("unrelated path must not be excluded")
	}
	// Findings-level check: subtitle finding filtered, health kept.
	subs := []SubtitleFinding{{Path: "/lib/x.en.srt", Status: "mismatch"}}
	kept := 0
	for _, f := range subs {
		if !isExcluded(ex, f.Path, "subtitles") {
			kept++
		}
	}
	if kept != 0 {
		t.Fatal("excluded subtitle finding was not suppressed")
	}
	health := []HealthFinding{{Path: "/lib/x.en.srt", Type: "subtitle-empty"}}
	keptH := 0
	for _, f := range health {
		if !isExcluded(ex, f.Path, "health") {
			keptH++
		}
	}
	if keptH != 1 {
		t.Fatal("health finding for the same path must survive a subtitles-only exclusion")
	}
}

// TestExclusionRemovesDuplicateGroup: excluding one of two copies
// removes the group entirely.
func TestExclusionRemovesDuplicateGroup(t *testing.T) {
	items := []*Item{
		mkItem("/lib/a.mkv", "Same Film", 2020, "", 4_000_000_000, 1080, "h1"),
		mkItem("/lib/b.mp4", "Same Film", 2020, "", 1_500_000_000, 720, "h2"),
	}
	ex := []Exclusion{{Path: "/lib/b.mp4", Scope: "duplicates"}}
	groups, stats := buildDuplicates(items, testCfg(), ex)
	if len(groups) != 0 {
		t.Fatalf("groups = %d, want 0 after exclusion", len(groups))
	}
	if stats.ExcludedCopies != 1 {
		t.Fatalf("excluded copies = %d, want 1", stats.ExcludedCopies)
	}
	// Same exclusion under a different scope must NOT dissolve the group.
	exOther := []Exclusion{{Path: "/lib/b.mp4", Scope: "health"}}
	groups2, _ := buildDuplicates(items, testCfg(), exOther)
	if len(groups2) != 1 {
		t.Fatalf("health-scoped exclusion dissolved a duplicate group: %d groups", len(groups2))
	}
}

// mkEpisodeItem builds an episode Item from a parse result, the way
// the scanner does, for the v0.3.5 grouping tests.
func mkEpisodeItem(path string, p ParsedName, size int64, hash string) *Item {
	return &Item{
		Path: path, Kind: p.Kind, Title: p.Title, NormTitle: p.NormTitle,
		Season: p.Season, Episodes: p.Episodes, Edition: p.Edition,
		Size: size, Height: 1080, ResolutionLabel: "1080p",
		Duration: 1444, Bitrate: 2_000_000, SampleHash: hash,
		ProbeOK: true, HasVideo: true,
	}
}

// TestDuplicatesNumberlessEpisodesNeverGroup is the v0.3.5 guard:
// episodes whose numbers no pattern can find (folder context only)
// must produce ZERO duplicate groups, however similar their sizes
// and durations are — an unidentified episode can produce no group,
// never a wrong one. Two of the items even share size AND sampled
// hash, proving exact-duplicate flagging cannot fire either (it
// only runs inside groups, and no group may form).
func TestDuplicatesNumberlessEpisodesNeverGroup(t *testing.T) {
	words := []string{"One", "Two", "Three", "Four", "Five", "Six"}
	var items []*Item
	for i, w := range words {
		path := "/lib/Eureka Seven/S01/Eureka Seven - Part " + w + ".mkv"
		p := parseName(path, "/lib")
		if p.Kind != "episode" || len(p.Episodes) != 0 {
			t.Fatalf("premise broken: %q parsed as %+v — the new patterns must NOT catch it", path, p)
		}
		size := int64(330_000_000 + i*2_000_000)
		hash := "h" + w
		if i < 2 {
			size, hash = 330_000_000, "identical"
		}
		items = append(items, mkEpisodeItem(path, p, size, hash))
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 0 {
		t.Fatalf("groups = %d, want 0 for numberless episodes (first group: %+v)", len(groups), groups[0])
	}
}

// TestDuplicatesParsedEpisodePositive: two copies of the same
// word-form episode (the v0.3.5 screenshot shape, two different
// roots) DO form exactly one confirmed group.
func TestDuplicatesParsedEpisodePositive(t *testing.T) {
	pathA := "/a/TV Shows/Eureka Seven/S01/[bonkai77] Eureka Seven - Episode 39 [BD 1080p Dual Audio x265 10bit].mkv"
	pathB := "/b/Eureka Seven/S01/Eureka Seven - Episode 39.mkv"
	pA := parseName(pathA, "/a")
	pB := parseName(pathB, "/b")
	for _, p := range []ParsedName{pA, pB} {
		if p.Kind != "episode" || len(p.Episodes) != 1 || p.Episodes[0] != 39 {
			t.Fatalf("premise broken: parsed %+v", p)
		}
	}
	items := []*Item{
		mkEpisodeItem(pathA, pA, 356_000_000, "hA"),
		mkEpisodeItem(pathB, pB, 340_000_000, "hB"),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want exactly 1", len(groups))
	}
	g := groups[0]
	if len(g.Copies) != 2 || g.Status != "confirmed" || len(g.Episodes) != 1 || g.Episodes[0] != 39 {
		t.Fatalf("group = copies %d, status %q, episodes %v", len(g.Copies), g.Status, g.Episodes)
	}
}

// TestDuplicatesDurationDemotion: wildly different durations demote.
func TestDuplicatesDurationDemotion(t *testing.T) {
	a := mkItem("/lib/a.mkv", "Same Film", 2020, "", 4_000_000_000, 1080, "h1")
	b := mkItem("/lib/b.mp4", "Same Film", 2020, "", 1_500_000_000, 720, "h2")
	b.Duration = 1800 // half the runtime: not the same cut
	groups, _ := buildDuplicates([]*Item{a, b}, testCfg(), nil)
	if len(groups) != 1 || groups[0].Status != "demoted" {
		t.Fatalf("status = %+v", groups)
	}
}
