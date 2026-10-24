package core

// bulk_export_131_test.go — v1.3.1 bulk regression against the
// owner's first real link-problems export (2026-10-24, app
// v1.3.0; copy kept at verification-1.3.1/real-export-2026-10-24.json).
// The v1.3.1 parser runs over EVERY basename in that export —
// all conflict sources (identified at v1.3.0) plus all
// not-found paths — and the outcome must match the outcome
// recorded in the export, EXCEPT the two fractional-misfire
// victims, which must flip to their corrected identities.
// Any other delta fails the test: parser entry points are
// shared with scans and duplicates, so this corpus is the
// blast-radius proof for the fractional narrowing.
//
// v1.3.6 amendment: the pack-extras adjacency narrowing adds
// a second named flip set — the twelve What We Do in the
// Shadows season-6 episodes the export recorded as "extra"
// because their folder is "S06 w-extra ep11" (season 6 WITH
// an extra episode, misread as a pack extras folder). They
// must flip to their episode identities; every other outcome
// in the corpus — including the 282 other entries the export
// recorded as extras — must still match the export exactly.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type bulkParsed struct {
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	NormTitle string `json:"norm_title"`
	Year      int    `json:"year"`
	Season    int    `json:"season"`
	Episodes  []int  `json:"episodes"`
	Special   string `json:"special"`
}

type bulkExport struct {
	SourceRoot string `json:"source_root"`
	Conflicts  []struct {
		Sources []struct {
			Source string      `json:"source"`
			Parsed *bulkParsed `json:"parsed"`
		} `json:"sources"`
	} `json:"conflicts"`
	NotFound []struct {
		Path   string      `json:"path"`
		Parsed *bulkParsed `json:"parsed"`
	} `json:"not_found"`
}

func bulkEps(e []int) []int {
	if e == nil {
		return []int{}
	}
	return e
}

func bulkSame(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestBulkExport131ParserRegression(t *testing.T) {
	var raw []byte
	var err error
	for _, cand := range []string{
		"../../../verification-1.3.1/real-export-2026-10-24.json",
		"../../../verification-1.3.0/real-export-2026-10-24.json",
	} {
		raw, err = os.ReadFile(cand)
		if err == nil {
			break
		}
	}
	if err != nil {
		// PUBLIC-TREE NOTE: the corpus above is the owner's real
		// link-problems export, which is NOT shipped in the public
		// repo (it carries real library paths). In a standalone
		// clone this test therefore skips; it runs in full where
		// the verification copies exist. The two fractional-misfire
		// victims are also pinned verbatim in parser_131_test.go,
		// and the twelve v1.3.6 extras flips in
		// parser_extras_136_test.go, so a clone still covers
		// every named fix.
		t.Skipf("real export corpus not present in this tree (tried verification-1.3.1 and verification-1.3.0 copies): %v", err)
	}
	var doc bulkExport
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if doc.SourceRoot == "" {
		t.Fatal("export has no source_root")
	}
	// The two fractional-misfire victims and their corrected
	// identities (kind/title/season/episodes/special).
	victims := map[string]bulkParsed{
		"The.Simpsons.S22E21.500.Keys.1080p.WEBRip.DD5.1.x264.mkv": {
			Kind: "episode", Title: "The Simpsons", Season: 22, Episodes: []int{21},
		},
		"[Anime Time] One Piece - 0438 - A Paradise in Hell! Impel Down - Level 5.5!.mkv": {
			Kind: "episode", Title: "One Piece", Season: 13, Episodes: []int{438},
		},
	}
	// The v1.3.6 extras-adjacency flips: the twelve WWDITS S06
	// episodes recorded as "extra" in the export, with the
	// episode identities they must parse to now. S06E11B (the
	// alternate-ending version) parses as plain episode 11 —
	// the B marker is not a distinct episode identity.
	extrasFlips136 := map[string]bulkParsed{
		"What.We.Do.in.the.Shadows.S06E01.The.Return.of.Jerry.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":              {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{1}},
		"What.We.Do.in.the.Shadows.S06E02.Headhunting.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                      {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{2}},
		"What.We.Do.in.the.Shadows.S06E03.Sleep.Hypnosis.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                   {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{3}},
		"What.We.Do.in.the.Shadows.S06E04.The.Railroad.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                     {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{4}},
		"What.We.Do.in.the.Shadows.S06E05.Nandors.Army.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                     {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{5}},
		"What.We.Do.in.the.Shadows.S06E06.Laszlos.Father.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                   {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{6}},
		"What.We.Do.in.the.Shadows.S06E07.March.Madness.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                    {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{7}},
		"What.We.Do.in.the.Shadows.S06E08.P.I.Undercover.New.York.1080p.AMZN.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":     {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{8}},
		"What.We.Do.in.the.Shadows.S06E09.Come.Out.And.Play.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{9}},
		"What.We.Do.in.the.Shadows.S06E10.The.Promotion.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                    {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{10}},
		"What.We.Do.in.the.Shadows.S06E11.The.Finale.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv":                       {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{11}},
		"What.We.Do.in.the.Shadows.S06E11B.The.Finale.and.Alternate.Ending.1080p.WEBRip.10Bit.DDP5.1.HEVC-d3g.mkv": {Kind: "episode", Title: "What We Do in the Shadows", Season: 6, Episodes: []int{11}},
	}
	var checked, flipped, extrasFlipped int
	var deltas []string
	check := func(path string, recorded *bulkParsed) {
		t.Helper()
		if recorded == nil {
			t.Errorf("%s: export has no parsed block", path)
			return
		}
		checked++
		got := parseName(path, doc.SourceRoot)
		base := filepath.Base(path)
		if want, isVictim := victims[base]; isVictim {
			if got.Kind != want.Kind || got.Title != want.Title || got.Season != want.Season || !bulkSame(bulkEps(got.Episodes), want.Episodes) || got.Special != "" {
				t.Errorf("victim %s: got kind/title/season/episodes/special = %q/%q/%d/%v/%q, want %q/%q/%d/%v/\"\"",
					base, got.Kind, got.Title, got.Season, bulkEps(got.Episodes), got.Special,
					want.Kind, want.Title, want.Season, want.Episodes)
			} else {
				flipped++
			}
			return
		}
		if want, isFlip := extrasFlips136[base]; isFlip {
			if got.Kind != want.Kind || got.Title != want.Title || got.Season != want.Season || !bulkSame(bulkEps(got.Episodes), want.Episodes) || got.Special != "" {
				t.Errorf("v1.3.6 extras flip %s: got kind/title/season/episodes/special = %q/%q/%d/%v/%q, want %q/%q/%d/%v/\"\"",
					base, got.Kind, got.Title, got.Season, bulkEps(got.Episodes), got.Special,
					want.Kind, want.Title, want.Season, want.Episodes)
			} else {
				extrasFlipped++
			}
			return
		}
		if got.Kind != recorded.Kind || got.Title != recorded.Title || got.NormTitle != recorded.NormTitle ||
			got.Year != recorded.Year || got.Season != recorded.Season ||
			!bulkSame(bulkEps(got.Episodes), bulkEps(recorded.Episodes)) || got.Special != recorded.Special {
			deltas = append(deltas, path)
			t.Errorf("DELTA %s:\n got      kind/title/norm/year/season/episodes/special = %q/%q/%q/%d/%d/%v/%q\n recorded kind/title/norm/year/season/episodes/special = %q/%q/%q/%d/%d/%v/%q",
				path,
				got.Kind, got.Title, got.NormTitle, got.Year, got.Season, bulkEps(got.Episodes), got.Special,
				recorded.Kind, recorded.Title, recorded.NormTitle, recorded.Year, recorded.Season, bulkEps(recorded.Episodes), recorded.Special)
		}
	}
	for _, c := range doc.Conflicts {
		for _, s := range c.Sources {
			check(s.Source, s.Parsed)
		}
	}
	for _, nf := range doc.NotFound {
		check(nf.Path, nf.Parsed)
	}
	t.Logf("bulk regression: %d basenames checked, %d victims flipped, %d v1.3.6 extras flips, %d unexplained deltas", checked, flipped, extrasFlipped, len(deltas))
	if flipped != len(victims) {
		t.Errorf("victims flipped = %d, want %d (a victim basename is missing from the export or did not flip)", flipped, len(victims))
	}
	if extrasFlipped != len(extrasFlips136) {
		t.Errorf("v1.3.6 extras flips = %d, want %d (a WWDITS basename is missing from the export or did not flip)", extrasFlipped, len(extrasFlips136))
	}
	if len(deltas) > 0 {
		t.Errorf("%d unexplained parse deltas (listed above) — the parser changed more than the named victims and flips", len(deltas))
	}
}
