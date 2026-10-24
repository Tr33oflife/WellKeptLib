package core

// linkplan_131_test.go — v1.3.1 collision-ranking facts
// (NAMING-ROUND-3-ANALYSIS.md Defect 1): the keeper ranking
// must never decide on empty facts. The v1.3.0 real-tree
// export showed whole packs ranking on 0 bytes because the
// walk stat itself came back zero for them and the resolver
// short-circuited to fact-empty scan items.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLinkPlan131UnprobedCopiesRankByRealSize is the
// acceptance test: two unprobed copies of one episode, same
// resolution label, different stat sizes — the LARGER file is
// keeper, and the diagnostics export's keeper_reason cites
// real bytes (never "0 bytes").
func TestLinkPlan131UnprobedCopiesRankByRealSize(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	tgt := filepath.Join(base, "dst")
	writeOrgFile(t, filepath.Join(src, "PackA", "Show.S01E01.1080p.mkv"), strings.Repeat("a", 100000))
	writeOrgFile(t, filepath.Join(src, "PackB", "Show.S01E01.1080p.mkv"), strings.Repeat("b", 50000))
	writeOrgFile(t, filepath.Join(src, "PackC", "Other.S01E01.mkv"), strings.Repeat("c", 30000))
	writeOrgFile(t, filepath.Join(src, "PackD", "Other.S01E01.mkv"), strings.Repeat("d", 80000))
	plan, err := planLinkLibrary(src, tgt, nil, nil)
	if err != nil {
		t.Fatalf("planLinkLibrary: %v", err)
	}
	if len(plan.Collisions) != 2 {
		t.Fatalf("collisions = %d, want 2", len(plan.Collisions))
	}
	doc := BuildLinkPlanDiagnostics(plan, "1.3.1", time.Now())
	if len(doc.Conflicts) != 2 {
		t.Fatalf("export conflicts = %d, want 2", len(doc.Conflicts))
	}
	// A bare "0 bytes" size fact (word-boundary match — "80000
	// bytes" must NOT trip this) means the ranking decided on
	// empty facts.
	zeroBytes := regexp.MustCompile(`\b0 bytes`)
	for _, c := range doc.Conflicts {
		if c.Status != "resolved" {
			t.Fatalf("target %s: status %q, want resolved", c.Target, c.Status)
		}
		if zeroBytes.MatchString(c.KeeperReason) {
			t.Fatalf("target %s: keeper_reason cites 0 bytes: %q", c.Target, c.KeeperReason)
		}
	}
	// The 1080p group keeps the 100000-byte copy; the untagged
	// group keeps the 80000-byte copy.
	for _, c := range doc.Conflicts {
		var want int
		switch {
		case strings.Contains(c.Target, "Show"):
			want = 100000
		case strings.Contains(c.Target, "Other"):
			want = 80000
		default:
			t.Fatalf("unexpected target %s", c.Target)
		}
		if !strings.Contains(c.KeeperReason, strconv.Itoa(want)+" bytes") {
			t.Fatalf("target %s: keeper_reason %q does not cite %d bytes", c.Target, c.KeeperReason, want)
		}
	}
}

// TestLinkPlan131RankingItemMergesProbeFirst covers the
// merge direction: a probe/scan size already on the item
// stands even when the walk stat is zero, and a zero probe
// size is filled from the walk stat (with a fresh stat as the
// last resort when both are zero).
func TestLinkPlan131RankingItemMergesProbeFirst(t *testing.T) {
	real := filepath.Join(t.TempDir(), "Show.S01E01.mkv")
	writeOrgFile(t, real, strings.Repeat("x", 42000))
	// Probe item with a real size, walk stat zero: probe wins.
	b := &linkPlanBuilder{resolve: func(string, ParsedName, int64, int64) *Item {
		return &Item{Height: 1080, Bitrate: 4000000, Size: 12345, ResolutionLabel: "1080p"}
	}}
	vp := &videoPlan{File: orgFile{Path: real, Name: "Show.S01E01.mkv", Size: 0}, Parsed: ParsedName{Kind: "episode", Title: "Show", Season: 1, Episodes: []int{1}, Resolution: "720p"}}
	it, fb := b.rankingItem(vp)
	if fb {
		t.Fatal("resolver item must not be the fallback")
	}
	if it.Size != 12345 {
		t.Fatalf("probe size: got %d, want 12345 (probe wins over a zero walk stat)", it.Size)
	}
	if it.ResolutionLabel != "1080p" {
		t.Fatalf("probe resolution: got %q, want 1080p (probed height keeps its label)", it.ResolutionLabel)
	}
	// Probe item with a zero size, walk stat zero, real file on
	// disk: the fresh stat rescues the size.
	b2 := &linkPlanBuilder{resolve: func(string, ParsedName, int64, int64) *Item {
		return &Item{Height: 720, ResolutionLabel: "720p"}
	}}
	it2, _ := b2.rankingItem(vp)
	if it2.Size != 42000 {
		t.Fatalf("rescued size: got %d, want 42000 (fresh stat of the real file)", it2.Size)
	}
	// Probe item with no height: the filename label is carried.
	b3 := &linkPlanBuilder{resolve: func(string, ParsedName, int64, int64) *Item {
		return &Item{}
	}}
	it3, _ := b3.rankingItem(vp)
	if it3.ResolutionLabel != "720p" {
		t.Fatalf("filename label: got %q, want 720p", it3.ResolutionLabel)
	}
	if it3.Size != 42000 {
		t.Fatalf("walk/fresh size: got %d, want 42000", it3.Size)
	}
}

// TestLinkPlan131ResolverFactEmptyFallsThroughToProbe:
// a matched scan item carrying no ranking facts at all does
// not short-circuit the resolver — with no ffprobe available
// it falls through to a nil probe (the filename/size
// fallback), NOT to the zero item. With a stub ffprobe it
// returns real probe facts instead.
func TestLinkPlan131ResolverFactEmptyFallsThrough(t *testing.T) {
	svc := linkSvc(t)
	real := filepath.Join(t.TempDir(), "Show.S01E01.mkv")
	writeOrgFile(t, real, strings.Repeat("x", 42000))
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	empty := &Item{Path: real, Size: 0, ModTimeUnix: info.ModTime().Unix()}
	cfg := &Config{FFprobePath: ""}
	got := svc.linkRankResolver([]*Item{empty}, cfg)(real, ParsedName{}, 0, info.ModTime().UnixNano())
	if got != nil {
		t.Fatalf("fact-empty matched item with no ffprobe: resolver = %+v, want nil (fall through to fallback)", got)
	}
	// A matched item WITH facts still short-circuits.
	full := &Item{Path: real, Size: 42000, ModTimeUnix: info.ModTime().Unix(), Height: 1080, Bitrate: 4000000}
	got2 := svc.linkRankResolver([]*Item{full}, cfg)(real, ParsedName{}, 42000, info.ModTime().UnixNano())
	if got2 == nil || got2.Height != 1080 {
		t.Fatalf("fact-carrying matched item: resolver = %+v, want the item", got2)
	}
	// With a stub ffprobe, the fact-empty match is probed.
	stub := filepath.Join(t.TempDir(), "ffprobe")
	stubScript := "#!/bin/sh\ncat <<'EOF'\n{\"format\": {\"duration\": \"1304.8\", \"size\": \"42000\", \"bit_rate\": \"2919534\"}, \"streams\": [{\"index\": 0, \"codec_name\": \"h264\", \"codec_type\": \"video\", \"width\": 1280, \"height\": 720, \"bit_rate\": \"2919534\"}]}\nEOF\n"
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg2 := &Config{FFprobePath: stub, ProbeTimeoutSec: 10}
	got3 := svc.linkRankResolver([]*Item{empty}, cfg2)(real, ParsedName{}, 0, info.ModTime().UnixNano())
	if got3 == nil {
		t.Fatal("fact-empty matched item with stub ffprobe: resolver = nil, want probe facts")
	}
	if got3.Height != 720 || got3.Bitrate != 2919534 {
		t.Fatalf("probed facts: height/bitrate = %d/%d, want 720/2919534", got3.Height, got3.Bitrate)
	}
}
