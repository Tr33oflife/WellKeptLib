package core

// features_150_test.go — v1.5.0 Parts A+B (catalog export,
// size roll-ups, large thresholds) over in-memory fixtures
// with generalized paths. No real library is touched.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const gib = int64(1) << 30

// fixture150Items is the shared Part A/B fixture: one show
// with a small season and one season over the 20 GiB line,
// a large and a small movie, an extra in the large movie's
// folder, one multi-episode file, and two unidentified
// shapes (no identity at all, and an episode with no
// number).
func fixture150Items() []*Item {
	mk := func(path, kind, title string, year, season int, eps []int, size int64) *Item {
		return &Item{
			Path: path, Root: "/home/user/media", Name: filepath.Base(path),
			Kind: kind, Title: title, NormTitle: normalizeTitle(title),
			Year: year, Season: season, Episodes: eps, Size: size,
			Duration: 1800, ResolutionLabel: "1080p", Codec: "h264",
			Bitrate: 8000000, ProbeOK: true, HasVideo: true,
		}
	}
	return []*Item{
		mk("/home/user/media/TV/Big Show/Season 01/Big Show - S01E01.mkv", "episode", "Big Show", 2020, 1, []int{1}, 4*gib),
		mk("/home/user/media/TV/Big Show/Season 01/Big Show - S01E02-E03.mkv", "episode", "Big Show", 2020, 1, []int{2, 3}, 1*gib),
		mk("/home/user/media/TV/Big Show/Season 02/Big Show - S02E01.mkv", "episode", "Big Show", 2020, 2, []int{1}, 21*gib),
		mk("/home/user/media/TV/Alpha Show/Season 01/Alpha Show - S01E01.mkv", "episode", "Alpha Show", 2019, 1, []int{1}, 2*gib),
		mk("/home/user/media/Movies/Huge Film (2020)/Huge Film (2020).mkv", "movie", "Huge Film", 2020, 0, nil, 7*gib),
		mk("/home/user/media/Movies/Small Film (2021)/Small Film (2021).mkv", "movie", "Small Film", 2021, 0, nil, 1*gib),
		mk("/home/user/media/Movies/Huge Film (2020)/Huge Film (2020)-trailer.mkv", "extra", "Huge Film", 0, 0, nil, 100<<20),
		mk("/home/user/media/TV/Big Show/Season 01/Big Show - S01EXX.mkv", "episode", "Big Show", 2020, 1, nil, 500<<20),
		mk("/home/user/media/Downloads/random.bin.mkv", "unknown", "", 0, 0, nil, 300<<20),
	}
}

func fixture150Cfg() *Config {
	c := defaultConfig()
	c.Roots = []string{"/home/user/media"}
	return c
}

// TestCatalog150ShapeAndRollups: the document's structure,
// totals discipline (extras bytes OUT of show totals),
// counts, and deterministic sort.
func TestCatalog150ShapeAndRollups(t *testing.T) {
	items := fixture150Items()
	doc := BuildCatalog(items, []string{"/home/user/media"}, fixture150Cfg(), "1.5.0", time.Unix(1700000000, 0))
	if doc.Schema != "wellkeptlib.catalog" || doc.SchemaVersion != 1 || doc.AppVersion != "1.5.0" {
		t.Fatalf("schema header wrong: %+v", doc)
	}
	// Shows sort by title: Alpha Show before Big Show.
	if len(doc.Shows) != 2 || doc.Shows[0].Title != "Alpha Show" || doc.Shows[1].Title != "Big Show" {
		t.Fatalf("shows = %+v", doc.Shows)
	}
	big := doc.Shows[1]
	// Show total = episode files only: 4+1+21 GiB. The
	// unnumbered S01EXX file and the extra are NOT in it.
	if big.TotalBytes != 26*gib {
		t.Errorf("Big Show total = %d, want %d (episode files only)", big.TotalBytes, 26*gib)
	}
	if big.EpisodeFiles != 3 {
		t.Errorf("Big Show episode_files = %d, want 3", big.EpisodeFiles)
	}
	if len(big.Seasons) != 2 || big.Seasons[0].Season != 1 || big.Seasons[1].Season != 2 {
		t.Fatalf("Big Show seasons not ascending: %+v", big.Seasons)
	}
	if big.Seasons[0].TotalBytes != 5*gib || big.Seasons[1].TotalBytes != 21*gib {
		t.Errorf("season totals = %d/%d, want 5/21 GiB", big.Seasons[0].TotalBytes, big.Seasons[1].TotalBytes)
	}
	// The multi-episode file appears ONCE with its full list.
	s1 := big.Seasons[0]
	if len(s1.Episodes) != 2 || len(s1.Episodes[1].Episodes) != 2 || s1.Episodes[1].Episodes[0] != 2 || s1.Episodes[1].Episodes[1] != 3 {
		t.Errorf("season 1 episodes wrong: %+v", s1.Episodes)
	}
	// Movies sort by title; the large one flags.
	if len(doc.Movies) != 2 || doc.Movies[0].Title != "Huge Film" || !doc.Movies[0].Large || doc.Movies[1].Large {
		t.Errorf("movies wrong: %+v", doc.Movies)
	}
	// The extra carries its owner (exactly one work shares
	// its folder) and stays out of the show totals above.
	if len(doc.Extras) != 1 || doc.Extras[0].OwnerTitle != "Huge Film" {
		t.Errorf("extras wrong: %+v", doc.Extras)
	}
	// Unidentified: both shapes, with the planner's reasons.
	if len(doc.Unidentified) != 2 {
		t.Fatalf("unidentified = %+v", doc.Unidentified)
	}
	reasons := map[string]bool{}
	for _, u := range doc.Unidentified {
		reasons[u.Reason] = true
	}
	if !reasons["no episode number found"] || !reasons["couldn't identify this file"] {
		t.Errorf("unidentified reasons = %v", reasons)
	}
	// Summary: entries counted, thresholds recorded, total
	// bytes = EVERY scanned file.
	sum := doc.Summary
	if sum.Shows != 2 || sum.Seasons != 3 || sum.Episodes != 4 || sum.Movies != 2 || sum.Extras != 1 || sum.Unidentified != 2 {
		t.Errorf("summary counts = %+v", sum)
	}
	var wantTotal int64
	for _, it := range items {
		wantTotal += it.Size
	}
	if sum.TotalBytes != wantTotal {
		t.Errorf("summary total_bytes = %d, want %d", sum.TotalBytes, wantTotal)
	}
	if sum.Thresholds.MovieBytes != 6*gib || sum.Thresholds.SeasonBytes != 20*gib || sum.Thresholds.EpisodeBytes != 3*gib {
		t.Errorf("summary thresholds = %+v", sum.Thresholds)
	}
	// The document must round-trip through JSON (it IS the
	// export format).
	if _, err := json.Marshal(doc); err != nil {
		t.Fatalf("catalog does not marshal: %v", err)
	}
}

// TestCatalog150LargeFlagsFollowThresholds: flags are
// computed at export time — a threshold of 0 disables its
// flag, and a changed threshold re-flags with no rescan.
func TestCatalog150LargeFlagsFollowThresholds(t *testing.T) {
	cfg := fixture150Cfg()
	cfg.LargeEpisodeBytes = 0 // disabled
	cfg.LargeMovieBytes = 8 * gib
	doc := BuildCatalog(fixture150Items(), cfg.Roots, cfg, "1.5.0", time.Unix(1700000000, 0))
	for _, sh := range doc.Shows {
		for _, sn := range sh.Seasons {
			for _, ep := range sn.Episodes {
				if ep.Large {
					t.Errorf("episode %s flagged large with the episode flag disabled", ep.Path)
				}
			}
		}
	}
	for _, m := range doc.Movies {
		if m.Large {
			t.Errorf("movie %s flagged large under an 8 GiB threshold (7 GiB file)", m.Path)
		}
	}
}

// TestSizes150Rollups: the Sizes projection — per-level
// sorting, largest/average math, the large-season count on
// the show row (counts, not nested badges).
func TestSizes150Rollups(t *testing.T) {
	rep := buildSizes(fixture150Items(), fixture150Cfg())
	if len(rep.Shows) != 2 {
		t.Fatalf("shows = %d", len(rep.Shows))
	}
	// Size-descending: Big Show (26 GiB) before Alpha Show.
	big := rep.Shows[0]
	if big.Title != "Big Show" || big.TotalBytes != 26*gib || big.SeasonCount != 2 || big.EpisodeFiles != 3 {
		t.Fatalf("Big Show roll-up wrong: %+v", big)
	}
	if big.LargestBytes != 21*gib || !strings.HasSuffix(big.LargestPath, "S02E01.mkv") {
		t.Errorf("largest episode wrong: %d %s", big.LargestBytes, big.LargestPath)
	}
	if big.AvgBytes != 26*gib/3 {
		t.Errorf("avg = %d, want %d", big.AvgBytes, 26*gib/3)
	}
	if big.LargeSeasons != 1 {
		t.Errorf("large_seasons = %d, want 1", big.LargeSeasons)
	}
	// Seasons sort by size descending inside the show:
	// season 2 (21 GiB, large) before season 1 (5 GiB).
	if big.Seasons[0].Season != 2 || !big.Seasons[0].Large || big.Seasons[1].Season != 1 || big.Seasons[1].Large {
		t.Errorf("season order/flags wrong: %+v", big.Seasons)
	}
	// Episodes inside a season sort by size descending.
	s1 := big.Seasons[1]
	if len(s1.Episodes) != 2 || s1.Episodes[0].SizeBytes != 4*gib || !s1.Episodes[0].Large {
		t.Errorf("season 1 episodes wrong: %+v", s1.Episodes)
	}
	// Movies: size descending, large flag on the 7 GiB one.
	if len(rep.Movies) != 2 || rep.Movies[0].Title != "Huge Film" || !rep.Movies[0].Large || rep.Movies[1].Large {
		t.Errorf("movies wrong: %+v", rep.Movies)
	}
	if rep.Thresholds.MovieBytes != 6*gib {
		t.Errorf("report thresholds = %+v", rep.Thresholds)
	}
	// A disabled season threshold un-flags the season.
	cfg := fixture150Cfg()
	cfg.LargeSeasonBytes = 0
	rep2 := buildSizes(fixture150Items(), cfg)
	if rep2.Shows[0].LargeSeasons != 0 || rep2.Shows[0].Seasons[0].Large {
		t.Errorf("season flag survived a 0 threshold: %+v", rep2.Shows[0])
	}
}

// TestLargeThresholds150Config: the §B.2 persistence rules —
// defaults on a fresh config, presence-aware load merge
// (an explicit 0 disables and is honored), SaveConfig
// normalization, and Reset tuning leaving them alone.
func TestLargeThresholds150Config(t *testing.T) {
	dir := t.TempDir()
	// Fresh: no config file -> the owner's defaults.
	c, warn := loadConfig(dir)
	if warn != "" {
		t.Fatalf("fresh load warned: %s", warn)
	}
	if c.LargeMovieBytes != 6*gib || c.LargeSeasonBytes != 20*gib || c.LargeEpisodeBytes != 3*gib {
		t.Fatalf("defaults = %d/%d/%d", c.LargeMovieBytes, c.LargeSeasonBytes, c.LargeEpisodeBytes)
	}
	// Explicit 0 for one + explicit value for another;
	// the third key absent -> default.
	body := `{"large_movie_bytes": 0, "large_episode_bytes": 12345}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = loadConfig(dir)
	if c.LargeMovieBytes != 0 {
		t.Errorf("explicit 0 not honored: movie = %d", c.LargeMovieBytes)
	}
	if c.LargeEpisodeBytes != 12345 {
		t.Errorf("explicit value not honored: episode = %d", c.LargeEpisodeBytes)
	}
	if c.LargeSeasonBytes != 20*gib {
		t.Errorf("absent key lost its default: season = %d", c.LargeSeasonBytes)
	}
	// Negative reads as 0.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"large_season_bytes": -5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = loadConfig(dir)
	if c.LargeSeasonBytes != 0 {
		t.Errorf("negative threshold = %d, want 0", c.LargeSeasonBytes)
	}
	// SaveConfig: negative normalizes to 0, values persist,
	// and Reset tuning leaves all three alone.
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := svc.GetConfig()
	in.LargeMovieBytes = -1
	in.LargeSeasonBytes = 42 * gib
	in.LargeEpisodeBytes = 0
	if err := svc.SaveConfig(in); err != nil {
		t.Fatal(err)
	}
	got := svc.GetConfig()
	if got.LargeMovieBytes != 0 || got.LargeSeasonBytes != 42*gib || got.LargeEpisodeBytes != 0 {
		t.Fatalf("saved thresholds = %d/%d/%d", got.LargeMovieBytes, got.LargeSeasonBytes, got.LargeEpisodeBytes)
	}
	out, err := svc.ResetTuning()
	if err != nil {
		t.Fatal(err)
	}
	if out.LargeMovieBytes != 0 || out.LargeSeasonBytes != 42*gib || out.LargeEpisodeBytes != 0 {
		t.Errorf("Reset tuning touched the thresholds: %d/%d/%d", out.LargeMovieBytes, out.LargeSeasonBytes, out.LargeEpisodeBytes)
	}
}

// TestExports150LargeField: the additive `large` field in
// the flat inventory JSON and the CSV column.
func TestExports150LargeField(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.results.Items = fixture150Items()
	exp := svc.ExportInventory()
	largeByPath := map[string]bool{}
	for _, it := range exp.Items {
		largeByPath[it.Path] = it.Large
	}
	if !largeByPath["/home/user/media/Movies/Huge Film (2020)/Huge Film (2020).mkv"] {
		t.Error("inventory JSON: 7 GiB movie not flagged large")
	}
	if largeByPath["/home/user/media/Movies/Small Film (2021)/Small Film (2021).mkv"] {
		t.Error("inventory JSON: 1 GiB movie flagged large")
	}
	// The STORED items must not carry the flag (computed at
	// view/export time, never stored).
	for _, it := range svc.results.Items {
		if it.Large {
			t.Errorf("stored item %s carries a large flag", it.Path)
		}
	}
	csvBytes, err := svc.ExportCSV()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(csvBytes)), "\n")
	if !strings.HasSuffix(lines[0], ",large") {
		t.Errorf("CSV header = %q", lines[0])
	}
	found := false
	for _, ln := range lines[1:] {
		if strings.Contains(ln, "Huge Film (2020).mkv") {
			found = true
			if !strings.HasSuffix(ln, ",true") {
				t.Errorf("CSV row for the large movie = %q", ln)
			}
		}
	}
	if !found {
		t.Error("CSV has no row for the large movie")
	}
	// The Library view computes the same flags.
	lib := svc.Library("size", "desc")
	for _, it := range lib {
		if strings.HasSuffix(it.Path, "S02E01.mkv") && !it.Large {
			t.Error("Library: 21 GiB episode not flagged large")
		}
		if strings.HasSuffix(it.Path, "S01E02-E03.mkv") && it.Large {
			t.Error("Library: 1 GiB episode flagged large")
		}
	}
}
