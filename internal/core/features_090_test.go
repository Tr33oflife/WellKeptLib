package core

// features_090_test.go — the v0.9.0 feature wave: missing-
// episode gaps, subtitle coverage, folder hygiene, the
// dismissal store (W2), relabel (W3), and the wizard overview
// (W1). The fixture-integration test at the end pins the
// standard corpus's derived values: gaps 0 rows, coverage
// 1 covered / 7 none / 0 no-wanted / 0 unverifiable (8 eligible
// items — the 3 probe-failures are excluded), orphan-sidecar 0,
// empty-folder 1, overview 1 external + 1 embedded mismatch.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- gaps ----

func gapItem(title string, season int, special string, eps ...int) *Item {
	return &Item{Kind: "episode", Title: title, NormTitle: "norm:" + title, Season: season, Episodes: eps, Special: special}
}

func TestGapDerivation(t *testing.T) {
	cases := []struct {
		name        string
		items       []*Item
		wantRows    int
		wantMissing []int // first row's missing list, when wantRows > 0
	}{
		{"multi-episode fills its whole range", []*Item{
			gapItem("Show", 1, "", 1), gapItem("Show", 1, "", 2),
			gapItem("Show", 1, "", 4), gapItem("Show", 1, "multi-episode", 5, 6),
		}, 1, []int{3}},
		{"two distinct episodes with a hole report it", []*Item{
			gapItem("Show", 1, "", 1), gapItem("Show", 1, "", 3),
		}, 1, []int{2}},
		{"contiguous season is silent", []*Item{
			gapItem("Show", 1, "", 1), gapItem("Show", 1, "", 2), gapItem("Show", 1, "", 3),
		}, 0, nil},
		{"single episode is silent", []*Item{
			gapItem("Show", 1, "", 5),
		}, 0, nil},
		{"season 0 specials are excluded", []*Item{
			gapItem("Show", 0, "", 1), gapItem("Show", 0, "", 3), gapItem("Show", 0, "", 7),
		}, 0, nil},
		{"episode with no number never groups", []*Item{
			gapItem("Show", 1, ""), gapItem("Show", 1, "", 3),
		}, 0, nil},
		{"split files contribute nothing", []*Item{
			gapItem("Show", 1, "split-file", 1), gapItem("Show", 1, "", 2), gapItem("Show", 1, "", 4),
		}, 1, []int{3}},
		{"split-only first episode leaves a single distinct number", []*Item{
			gapItem("Show", 1, "split-file", 1), gapItem("Show", 1, "", 3),
		}, 0, nil},
		{"seasons are separate rows", []*Item{
			gapItem("Show", 1, "", 1), gapItem("Show", 1, "", 3),
			gapItem("Show", 2, "", 1), gapItem("Show", 2, "", 4),
		}, 2, []int{2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gaps := buildEpisodeGaps(tc.items)
			if len(gaps) != tc.wantRows {
				t.Fatalf("rows = %d, want %d (%v)", len(gaps), tc.wantRows, gaps)
			}
			if tc.wantRows > 0 {
				got := gaps[0].Missing
				if len(got) != len(tc.wantMissing) {
					t.Fatalf("missing = %v, want %v", got, tc.wantMissing)
				}
				for i := range got {
					if got[i] != tc.wantMissing[i] {
						t.Fatalf("missing = %v, want %v", got, tc.wantMissing)
					}
				}
				if gaps[0].MissingCount != len(tc.wantMissing) {
					t.Errorf("MissingCount = %d, want %d", gaps[0].MissingCount, len(tc.wantMissing))
				}
			}
		})
	}
}

// TestGapAbsoluteNumbering: a season 1 running past episode 50
// is flagged absolute, the missing COUNT stays exact, and the
// listed numbers are capped.
func TestGapAbsoluteNumbering(t *testing.T) {
	items := []*Item{gapItem("Long Runner", 1, "", 1), gapItem("Long Runner", 1, "", 105)}
	gaps := buildEpisodeGaps(items)
	if len(gaps) != 1 {
		t.Fatalf("rows = %d, want 1", len(gaps))
	}
	g := gaps[0]
	if !g.Absolute {
		t.Error("Absolute = false, want true for season 1 max present 105")
	}
	if g.MissingCount != 103 {
		t.Errorf("MissingCount = %d, want 103 (episodes 2..104)", g.MissingCount)
	}
	if len(g.Missing) != gapMissingListCap || !g.Truncated {
		t.Errorf("listed missing = %d (truncated %v), want capped at %d with Truncated set", len(g.Missing), g.Truncated, gapMissingListCap)
	}
	// A normal long season under the threshold is not flagged.
	items2 := []*Item{gapItem("Show", 1, "", 1), gapItem("Show", 1, "", 40)}
	g2 := buildEpisodeGaps(items2)
	if len(g2) != 1 || g2[0].Absolute {
		t.Errorf("season to 40: rows %d absolute %v, want 1 row not flagged", len(g2), len(g2) > 0 && g2[0].Absolute)
	}
}

// ---- coverage ----

func coverageService(items []*Item, probes map[string]*ProbeResult, subPaths []string, wanted []string) *Service {
	cfg := defaultConfig()
	if wanted != nil {
		cfg.WantedLanguages = wanted
	}
	cache := map[string]CacheEntry{}
	for p, pr := range probes {
		cache[p] = CacheEntry{Probe: pr}
	}
	return &Service{
		cfg:     cfg,
		store:   &Store{Cache: cache},
		results: &ScanResults{Items: items, SubtitleFilePaths: subPaths},
	}
}

func covItem(path, name string) *Item {
	return &Item{Path: path, Name: name, Kind: "movie", Title: name, ProbeOK: true, Size: 100}
}

func subStream(codec, lang, title string) StreamInfo {
	return StreamInfo{Type: "subtitle", Codec: codec, Language: lang, Title: title}
}

func TestCoverageStates(t *testing.T) {
	items := []*Item{
		covItem("/lib/Covered.mkv", "Covered.mkv"),
		covItem("/lib/SpanishOnly.mkv", "SpanishOnly.mkv"),
		covItem("/lib/Undeclared.mkv", "Undeclared.mkv"),
		covItem("/lib/ImageWanted.mkv", "ImageWanted.mkv"),
		covItem("/lib/Bare.mkv", "Bare.mkv"),
		covItem("/lib/ForcedOnly.mkv", "ForcedOnly.mkv"),
		covItem("/lib/Sidecar.mkv", "Sidecar.mkv"),
		covItem("/lib/Broken.mkv", "Broken.mkv"),
	}
	items[7].ProbeOK = false // probe-failed: excluded entirely
	probes := map[string]*ProbeResult{
		"/lib/Covered.mkv":     {Streams: []StreamInfo{subStream("subrip", "eng", "")}},
		"/lib/SpanishOnly.mkv": {Streams: []StreamInfo{subStream("subrip", "spa", "")}},
		"/lib/Undeclared.mkv":  {Streams: []StreamInfo{subStream("subrip", "und", "")}},
		"/lib/ImageWanted.mkv": {Streams: []StreamInfo{subStream("hdmv_pgs_subtitle", "eng", "")}},
		"/lib/Bare.mkv":        {Streams: []StreamInfo{{Type: "video", Codec: "h264"}}},
		"/lib/ForcedOnly.mkv":  {Streams: []StreamInfo{subStream("subrip", "eng", "English (Forced)")}},
		"/lib/Sidecar.mkv":     {Streams: []StreamInfo{{Type: "video", Codec: "h264"}}},
		"/lib/Broken.mkv":      {Streams: []StreamInfo{subStream("subrip", "eng", "")}},
	}
	svc := coverageService(items, probes, []string{"/lib/Sidecar.es.srt"}, nil)
	rep := svc.Coverage()
	if rep.Covered != 1 || rep.NoWanted != 3 || rep.None != 2 || rep.Unverifiable != 1 {
		t.Fatalf("counts = covered %d, no-wanted %d, none %d, unverifiable %d; want 1/3/2/1",
			rep.Covered, rep.NoWanted, rep.None, rep.Unverifiable)
	}
	byPath := map[string]CoverageItem{}
	for _, ci := range rep.Items {
		byPath[ci.Path] = ci
	}
	// The attention list holds everything not covered (6 items);
	// the probe-failed item appears nowhere.
	if len(rep.Items) != 6 {
		t.Errorf("attention list = %d items, want 6", len(rep.Items))
	}
	if _, ok := byPath["/lib/Broken.mkv"]; ok {
		t.Error("probe-failed item appears in coverage — unknown must not read as uncovered")
	}
	if ci := byPath["/lib/ImageWanted.mkv"]; ci.Class != coverageUnverifiable ||
		len(ci.Notes) == 0 || !strings.Contains(ci.Notes[len(ci.Notes)-1], "image track") {
		t.Errorf("image-wanted item = %+v, want unverifiable with the image-track note", ci)
	}
	if ci := byPath["/lib/ForcedOnly.mkv"]; ci.Class != coverageNone ||
		len(ci.Notes) == 0 || !strings.Contains(ci.Notes[0], "forced") {
		t.Errorf("forced-only item = %+v, want class none with the forced note (a forced track is not coverage)", ci)
	}
	if ci := byPath["/lib/Undeclared.mkv"]; ci.Class != coverageNoWanted {
		t.Errorf("und-tagged item class = %q, want no-wanted (present, language unknown)", ci.Class)
	}
	if ci := byPath["/lib/Sidecar.mkv"]; ci.Class != coverageNoWanted || len(ci.Languages) != 1 || ci.Languages[0] != "es" {
		t.Errorf("sidecar item = %+v, want no-wanted with languages [es]", ci)
	}

	// Wanted-list configurability: want Spanish instead and the
	// Spanish-only + sidecar items become covered, the English
	// one drops to no-wanted, the PGS-English item stays
	// unverifiable (its wanted language changed, not its nature).
	svc2 := coverageService(items, probes, []string{"/lib/Sidecar.es.srt"}, []string{"spa"})
	rep2 := svc2.Coverage()
	if rep2.Covered != 2 || rep2.NoWanted != 2 || rep2.None != 2 || rep2.Unverifiable != 1 {
		t.Errorf("wanted=spa counts = %d/%d/%d/%d, want 2/2/2/1",
			rep2.Covered, rep2.NoWanted, rep2.None, rep2.Unverifiable)
	}
	if len(rep2.Wanted) != 1 || rep2.Wanted[0] != "spa" {
		t.Errorf("report wanted = %v, want [spa]", rep2.Wanted)
	}
}

// TestWantedLanguagesSettings: the field's persistence contract —
// default eng, normalization on save (case folded, duplicates
// removed BY LANGUAGE), nil keeps, empty falls back to the
// default, and ResetTuning does NOT touch it (a preference, not
// tuning).
func TestWantedLanguagesSettings(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.GetConfig().WantedLanguages; len(got) != 1 || got[0] != "eng" {
		t.Fatalf("default wanted = %v, want [eng]", got)
	}
	cfg := svc.GetConfig()
	cfg.WantedLanguages = []string{"EN", "spa", "es", " spa "}
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetConfig().WantedLanguages; len(got) != 2 || got[0] != "en" || got[1] != "spa" {
		t.Fatalf("saved wanted = %v, want [en spa] (deduped by language)", got)
	}
	svc2, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc2.GetConfig().WantedLanguages; len(got) != 2 || got[1] != "spa" {
		t.Fatalf("reloaded wanted = %v, want [en spa]", got)
	}
	// nil in a save keeps the current list.
	cfg2 := svc2.GetConfig()
	cfg2.WantedLanguages = nil
	cfg2.Workers = 7
	if err := svc2.SaveConfig(cfg2); err != nil {
		t.Fatal(err)
	}
	if got := svc2.GetConfig().WantedLanguages; len(got) != 2 {
		t.Fatalf("after nil save wanted = %v, want the kept [en spa]", got)
	}
	// Reset tuning leaves the preference alone.
	if _, err := svc2.ResetTuning(); err != nil {
		t.Fatal(err)
	}
	if got := svc2.GetConfig().WantedLanguages; len(got) != 2 || got[1] != "spa" {
		t.Fatalf("after ResetTuning wanted = %v, want [en spa] untouched", got)
	}
	// An explicitly emptied list falls back to the default.
	cfg3 := svc2.GetConfig()
	cfg3.WantedLanguages = []string{}
	if err := svc2.SaveConfig(cfg3); err != nil {
		t.Fatal(err)
	}
	if got := svc2.GetConfig().WantedLanguages; len(got) != 1 || got[0] != "eng" {
		t.Fatalf("after empty save wanted = %v, want [eng]", got)
	}
}

// ---- hygiene ----

func TestHygieneOrphanSidecars(t *testing.T) {
	dir := "/media/Films"
	item := &Item{Path: dir + "/Film (2020).mkv", Name: "Film (2020).mkv", Kind: "movie",
		Size: 5 << 20, ProbeOK: true, HasVideo: true, Duration: 100, Bitrate: 400000}
	subs := []subFileInfo{
		{Path: dir + "/Film (2020).en.srt", Size: 100, Cues: 3, Lines: 3},    // matched: no finding
		{Path: dir + "/Other Film.en.srt", Size: 100, Cues: 3, Lines: 3},     // orphan
		{Path: dir + "/Empty One.en.srt", Size: 10, Cues: 0, Lines: 0},       // subtitle-empty, NOT double-reported
		{Path: "/media/Staging/Lonely.en.srt", Size: 100, Cues: 3, Lines: 3}, // no media in folder: no orphan
	}
	sidecars := []fileRef{
		{Path: dir + "/poster.jpg", Size: 10},            // folder artwork: never an orphan
		{Path: dir + "/Film (2020)-thumb.jpg", Size: 10}, // matched to the video
		{Path: dir + "/tvshow.nfo", Size: 10},            // folder-level nfo
		{Path: dir + "/Random Notes.nfo", Size: 10},      // orphan
		{Path: "/media/Staging/orphan.nfo", Size: 10},    // no media in folder: no orphan
	}
	findings := buildHealth([]*Item{item}, subs, nil, nil, sidecars, defaultConfig())
	orphans := map[string]bool{}
	for _, f := range findings {
		if f.Type == "orphan-sidecar" {
			orphans[f.Path] = true
		}
	}
	want := map[string]bool{dir + "/Other Film.en.srt": true, dir + "/Random Notes.nfo": true}
	if len(orphans) != len(want) {
		t.Fatalf("orphans = %v, want exactly %v", orphans, want)
	}
	for p := range want {
		if !orphans[p] {
			t.Errorf("missing orphan finding for %s", p)
		}
	}
}

func TestTopMostEmptyFolders(t *testing.T) {
	in := []string{"/r/Series", "/r/Series/Season 01", "/r/Series/Season 01/Extras", "/r/Other", "/r/Solo"}
	got := topMostEmptyFolders(in)
	want := []string{"/r/Series", "/r/Other", "/r/Solo"}
	if len(got) != len(want) {
		t.Fatalf("topMost = %v, want %v (nested chains counted once, at the top)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("topMost = %v, want %v", got, want)
		}
	}
}

// ---- dismissals ----

func dismissalService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	subPath := filepath.Join(root, "Film.en.srt")
	if err := os.WriteFile(subPath, []byte("1\n00:00:01,000 --> 00:00:02,000\nHola mundo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(root, "Film.mkv")
	if err := os.WriteFile(mediaPath, []byte("not really a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.Roots = []string{root}
	svc.results.Subtitles = []SubtitleFinding{
		{Path: subPath, Source: "external", StreamIdx: -1, Declared: "en", Detected: "es", Status: "mismatch"},
		{Path: mediaPath, Source: "embedded", StreamIdx: 2, Declared: "en", Detected: "fr", Status: "mismatch"},
	}
	return svc, root
}

func TestDismissalLifecycle(t *testing.T) {
	svc, _ := dismissalService(t)
	if n := len(svc.Subtitles()); n != 2 {
		t.Fatalf("findings = %d, want 2 before any dismissal", n)
	}
	subPath := svc.results.Subtitles[0].Path
	if _, err := svc.DismissFinding(subPath, "external", -1, "checked by hand"); err != nil {
		t.Fatal(err)
	}
	if n := len(svc.Subtitles()); n != 1 {
		t.Fatalf("findings after dismiss = %d, want 1 (the embedded one)", n)
	}
	// Counts coherence: the summary and the wizard overview drop
	// with the list, and the overview reports the mark.
	if got := svc.State().Scan.Summary.SubtitleFindings; got != 1 {
		t.Errorf("summary subtitle_findings = %d, want 1", got)
	}
	if ov := svc.SubtitleOverview(); ov.Total != 1 || ov.Dismissed != 1 || ov.ExternalMismatch != 0 || ov.EmbeddedMismatch != 1 {
		t.Errorf("overview = %+v, want total 1, dismissed 1, external 0, embedded 1", ov)
	}
	// Persistence across a restart (marks reload; the finding
	// list is re-seeded the way a rescan would republish it).
	svc2, err := NewService(svc.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(svc2.Dismissals()); n != 1 {
		t.Fatalf("dismissals after restart = %d, want 1", n)
	}
	svc2.results.Subtitles = svc.results.Subtitles
	if n := len(svc2.Subtitles()); n != 1 {
		t.Fatalf("findings after restart = %d, want 1 (the mark survived)", n)
	}
	// Fingerprint invalidation: change the file and the finding
	// resurfaces — the mark was about the old file.
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(subPath, later, later); err != nil {
		t.Fatal(err)
	}
	if n := len(svc2.Subtitles()); n != 2 {
		t.Fatalf("findings after file change = %d, want 2 (stale mark no longer suppresses)", n)
	}
	// Undismiss removes the mark outright.
	if err := svc2.Undismiss(subPath, "external", -1, "mismatch"); err != nil {
		t.Fatal(err)
	}
	if n := len(svc2.Dismissals()); n != 0 {
		t.Errorf("dismissals after undismiss = %d, want 0", n)
	}
	if err := svc2.Undismiss(subPath, "external", -1, "mismatch"); err == nil {
		t.Error("second undismiss of the same mark: want an error")
	}
	// Dismissing something the app is not showing is an error.
	if _, err := svc.DismissFinding(filepath.Join(t.TempDir(), "nope.srt"), "external", -1, ""); err == nil {
		t.Error("dismiss of a non-finding: want an error")
	}
}

func TestCoverageDismissal(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "Bare.mkv")
	if err := os.WriteFile(mediaPath, []byte("video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.results.Items = []*Item{{Path: mediaPath, Name: "Bare.mkv", Kind: "movie", Title: "Bare", ProbeOK: true}}
	svc.store.Cache[mediaPath] = CacheEntry{Probe: &ProbeResult{Streams: []StreamInfo{{Type: "video", Codec: "h264"}}}}
	if rep := svc.Coverage(); rep.None != 1 || rep.Dismissed != 0 {
		t.Fatalf("coverage before = none %d dismissed %d, want 1/0", rep.None, rep.Dismissed)
	}
	if _, err := svc.DismissCoverage(mediaPath, "no subs wanted"); err != nil {
		t.Fatal(err)
	}
	if rep := svc.Coverage(); rep.None != 0 || rep.Dismissed != 1 || len(rep.Items) != 0 {
		t.Fatalf("coverage after dismiss = %+v, want none 0, dismissed 1, empty attention list", rep)
	}
	// The summary agrees (dismissed items leave every count).
	if sum := svc.State().Scan.Summary; sum.CoverageNoSubtitles != 0 {
		t.Errorf("summary coverage_no_subtitles = %d, want 0 after dismissal", sum.CoverageNoSubtitles)
	}
	// A changed file resurfaces in coverage too.
	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(mediaPath, later, later); err != nil {
		t.Fatal(err)
	}
	if rep := svc.Coverage(); rep.None != 1 || rep.Dismissed != 0 {
		t.Errorf("coverage after file change = none %d dismissed %d, want 1/0 (mark went stale)", rep.None, rep.Dismissed)
	}
}

// ---- relabel ----

func TestPlanRelabelName(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		detected string
		want     string
		wantErr  bool
	}{
		{"replace the language token", "/x/Spanish Mislabeled (2021).en.srt", "es", "Spanish Mislabeled (2021).es.srt", false},
		{"flags after the language survive", "/x/Film.en.forced.srt", "es", "Film.es.forced.srt", false},
		{"639-2 token replaced by the detected 639-1", "/x/Film.eng.srt", "es", "Film.es.srt", false},
		{"und token replaced", "/x/Film.und.srt", "es", "Film.es.srt", false},
		{"missing language is inserted", "/x/Film.srt", "es", "Film.es.srt", false},
		{"inserted before trailing flags", "/x/Film.forced.srt", "es", "Film.es.forced.srt", false},
		{"already labelled is an error", "/x/Film.es.srt", "es", "", true},
		{"no detected language is an error", "/x/Film.en.srt", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planRelabelName(SubtitleFinding{Path: tc.path, Source: "external", Detected: tc.detected})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got name %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("name = %q, want %q", got, tc.want)
			}
		})
	}
	// Embedded findings can never be planned.
	if _, err := planRelabelName(SubtitleFinding{Path: "/x/Film.mkv", Source: "embedded", Detected: "es"}); err == nil {
		t.Error("embedded finding: want an explicit refusal")
	}
}

func relabelService(t *testing.T, withCollision bool) (*Service, string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	content := []byte("1\n00:00:01,000 --> 00:00:02,000\nHola mundo, esto es una prueba\n")
	from := filepath.Join(root, "Spanish Mislabeled (2021).en.srt")
	if err := os.WriteFile(from, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if withCollision {
		if err := os.WriteFile(filepath.Join(root, "Spanish Mislabeled (2021).es.srt"), []byte("other file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.Roots = []string{root}
	svc.results.Subtitles = []SubtitleFinding{
		{Path: from, Source: "external", StreamIdx: -1, Declared: "en", Detected: "es", Status: "mismatch"},
	}
	svc.results.SubtitleFilePaths = []string{from}
	return svc, root, from, content
}

func TestRelabelApplyUndoRoundTrip(t *testing.T) {
	svc, root, from, content := relabelService(t, false)
	to := filepath.Join(root, "Spanish Mislabeled (2021).es.srt")

	preview := svc.RelabelPreview([]string{from})
	if len(preview) != 1 || preview[0].To != to || preview[0].Collision || preview[0].Error != "" {
		t.Fatalf("preview = %+v, want one clean entry to %s", preview, to)
	}
	res, err := svc.RelabelApply([]string{from})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || len(res.Failed) != 0 || res.ManifestID == "" {
		t.Fatalf("apply = %+v, want 1 applied with a manifest", res)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Error("old name still exists after apply")
	}
	got, err := os.ReadFile(to)
	if err != nil || string(got) != string(content) {
		t.Fatalf("renamed file content = %q, %v; want byte-identical", got, err)
	}
	// The finding leaves the published results at once, and the
	// sidecar path follows the rename.
	if n := len(svc.Subtitles()); n != 0 {
		t.Errorf("findings after apply = %d, want 0", n)
	}
	if len(svc.results.SubtitleFilePaths) != 1 || svc.results.SubtitleFilePaths[0] != to {
		t.Errorf("subtitle paths after apply = %v, want [%s]", svc.results.SubtitleFilePaths, to)
	}
	manifests, err := svc.SubtitleManifests()
	if err != nil || len(manifests) != 1 || manifests[0].Undone {
		t.Fatalf("manifests = %+v, %v; want one open manifest", manifests, err)
	}
	undo, err := svc.RelabelUndo(res.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Restored != 1 || len(undo.Failed) != 0 {
		t.Fatalf("undo = %+v, want 1 restored", undo)
	}
	back, err := os.ReadFile(from)
	if err != nil || string(back) != string(content) {
		t.Fatalf("restored file = %q, %v; want the original name with identical bytes", back, err)
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Error("renamed file still exists after undo")
	}
	manifests, _ = svc.SubtitleManifests()
	if len(manifests) != 1 || !manifests[0].Undone {
		t.Errorf("manifests after undo = %+v, want the manifest marked undone", manifests)
	}
	// A second undo of the same manifest is an error, not a no-op.
	if _, err := svc.RelabelUndo(res.ManifestID); err == nil {
		t.Error("second undo: want an error")
	}
}

func TestRelabelCollisionAndGuards(t *testing.T) {
	svc, root, from, _ := relabelService(t, true)
	preview := svc.RelabelPreview([]string{from})
	if len(preview) != 1 || !preview[0].Collision {
		t.Fatalf("preview = %+v, want the collision flagged", preview)
	}
	res, err := svc.RelabelApply([]string{from})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || res.ManifestID != "" {
		t.Fatalf("apply = %+v, want 0 applied, 1 failed, no manifest", res)
	}
	if _, err := os.Stat(from); err != nil {
		t.Error("source file vanished on a refused apply")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "Spanish Mislabeled (2021).es.srt")); string(b) != "other file" {
		t.Error("the colliding target was overwritten — never allowed")
	}
	// A path with no current finding is refused in-band.
	res2, err := svc.RelabelApply([]string{filepath.Join(root, "Nope.en.srt")})
	if err != nil || len(res2.Failed) != 1 || res2.Failed[0].Error == "" {
		t.Errorf("non-finding apply = %+v, %v; want one in-band refusal", res2, err)
	}
	// And the whole operation refuses while a scan runs.
	svc.state.Running = true
	if _, err := svc.RelabelApply([]string{from}); err == nil {
		t.Error("apply during a scan: want errScanRunning")
	}
	svc.state.Running = false
}

// ---- subtitle delete ----

func TestDeleteSubtitleFindingGuards(t *testing.T) {
	svc, root := dismissalService(t)
	subPath := svc.results.Subtitles[0].Path
	mediaPath := svc.results.Subtitles[1].Path

	// Embedded path: refused explicitly, media file untouched.
	if _, err := svc.DeleteSubtitleFinding(mediaPath); err == nil || !strings.Contains(err.Error(), "embedded") {
		t.Errorf("embedded delete error = %v, want the explicit embedded refusal", err)
	}
	if _, err := os.Stat(mediaPath); err != nil {
		t.Error("media file was deleted through the embedded finding — never allowed")
	}
	// Not a finding at all.
	if _, err := svc.DeleteSubtitleFinding(filepath.Join(root, "ghost.srt")); err == nil {
		t.Error("ghost path delete: want an error")
	}
	// Outside every configured root: seed a finding in another
	// folder; the confinement guard must refuse.
	other := t.TempDir()
	outside := filepath.Join(other, "Out.en.srt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc.results.Subtitles = append(svc.results.Subtitles,
		SubtitleFinding{Path: outside, Source: "external", StreamIdx: -1, Declared: "en", Detected: "es", Status: "mismatch"})
	if _, err := svc.DeleteSubtitleFinding(outside); err == nil || !strings.Contains(err.Error(), "scan root") {
		t.Errorf("outside-root delete error = %v, want the root refusal", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("outside-root file was deleted")
	}
	// Happy path: the external finding deletes, is audit-logged,
	// and leaves the results (including the sidecar path list).
	res, err := svc.DeleteSubtitleFinding(subPath)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatal("delete result not OK")
	}
	if _, err := os.Stat(subPath); !os.IsNotExist(err) {
		t.Error("subtitle file still exists after delete")
	}
	for _, f := range svc.Subtitles() {
		if f.Path == subPath {
			t.Error("finding for the deleted file is still published")
		}
	}
	logs, err := readDeletedActions(svc.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range logs {
		if a.Path == subPath && a.FindingKind == "subtitle:mismatch" {
			found = true
		}
	}
	if !found {
		t.Errorf("deleted-actions log = %+v, want a subtitle:mismatch entry for %s", logs, subPath)
	}
}

// readDeletedActions loads the deletion audit log for assertions.
func readDeletedActions(dataDir string) ([]DeletedAction, error) {
	var list []DeletedAction
	b, err := os.ReadFile(filepath.Join(dataDir, "deleted-actions.json"))
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(b, &list)
	return list, err
}

// ---- fixture integration: the pinned v0.9.0 derivations ----

// TestFixtureV090Derivations pins the standard corpus's derived
// values (see the file header for the arithmetic): headline
// counts untouched at 11/2/2/6, gaps empty, coverage 1/0/7/0,
// hygiene adds nothing on this corpus.
func TestFixtureV090Derivations(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	sum := svc.State().Scan.Summary
	if sum.MediaCount != 11 || sum.DuplicateGroups != 2 || sum.SubtitleFindings != 2 || sum.HealthFindings != 6 {
		t.Fatalf("headline = %d/%d/%d/%d, want the untouched 11/2/2/6",
			sum.MediaCount, sum.DuplicateGroups, sum.SubtitleFindings, sum.HealthFindings)
	}
	if sum.GapCount != 0 || sum.GapEpisodes != 0 {
		t.Errorf("summary gaps = %d rows / %d episodes, want 0/0 (Test Show S01 holds E01+E02)", sum.GapCount, sum.GapEpisodes)
	}
	if sum.CoverageCovered != 1 || sum.CoverageNoWanted != 0 || sum.CoverageNoSubtitles != 7 || sum.CoverageUnverifiable != 0 {
		t.Errorf("summary coverage = %d/%d/%d/%d, want 1/0/7/0 (Embedded Case covered; 3 probe-failures excluded)",
			sum.CoverageCovered, sum.CoverageNoWanted, sum.CoverageNoSubtitles, sum.CoverageUnverifiable)
	}
	orphans, emptyFolders := 0, 0
	for _, f := range svc.Health() {
		switch f.Type {
		case "orphan-sidecar":
			orphans++
		case "empty-folder":
			emptyFolders++
		}
	}
	if orphans != 0 {
		t.Errorf("orphan-sidecar findings = %d, want 0 (Subtitle Cases has no media; the empty subtitle is not double-reported)", orphans)
	}
	if emptyFolders != 1 {
		t.Errorf("empty-folder findings = %d, want 1 (Empty Movie (2023))", emptyFolders)
	}
	ov := svc.SubtitleOverview()
	if ov.ExternalMismatch != 1 || ov.EmbeddedMismatch != 1 || ov.Total != 2 || ov.Dismissed != 0 {
		t.Errorf("overview = %+v, want external 1, embedded 1, total 2, dismissed 0", ov)
	}
	if len(svc.results.SubtitleFilePaths) != 4 {
		t.Errorf("persisted subtitle file paths = %d, want 4", len(svc.results.SubtitleFilePaths))
	}
}
