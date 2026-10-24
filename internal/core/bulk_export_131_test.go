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
//
// v1.4.0 amendment: the leading "[Extra]" bracket marker adds
// a third named flip set — the sixteen Regular Show movie
// extras the export recorded as year-less movies. They must
// flip to "extra" with the SAME display titles the export
// recorded (the bracket is a marker, not title text). The
// release's other ten flips (eight assigned-special files,
// two year-less movies) are PLANNER-level diversions — the
// parser output for them does not change, so they are gated
// separately by TestBulk140PlannerPredicateSweep below,
// which runs the planner's own diversion predicates over
// every not-found path in this corpus.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	// The v1.4.0 "[Extra]" bracket flips: the sixteen Regular
	// Show movie extras recorded as year-less movies. Each
	// must now parse as an extra carrying the same display
	// title the export recorded for it.
	extraMarkerFlips140 := map[string]bool{
		"[Extra] Animatic Destroying The Timenado.mkv":                        true,
		"[Extra] Animatic Opening Fight Scene.mkv":                            true,
		"[Extra] Animatic Stop The Timenado.mkv":                              true,
		"[Extra] Animatic The File Battle.mkv":                                true,
		"[Extra] Animatic The Ship Is Gone.mkv":                               true,
		"[Extra] Animatic Time To Save The Universe.mkv":                      true,
		"[Extra] Concept Art Gallery.mkv":                                     true,
		"[Extra] Deleted Animatic Alternate Ending.mkv":                       true,
		"[Extra] Deleted Animatic Alternate Fight Scene.mkv":                  true,
		"[Extra] Deleted Animatic Alternate Opening.mkv":                      true,
		"[Extra] Deleted Animatic Alternate Timeline.mkv":                     true,
		"[Extra] Deleted Animatic Fight Scene.mkv":                            true,
		"[Extra] Deleted Animatic Mordecai & Rigby Meet Mordecai & Rigby.mkv": true,
		"[Extra] Deleted Animatic Muscle Man Vs. The Bathroom.mkv":            true,
		"[Extra] Movie Art Gallery.mkv":                                       true,
		"[Extra] Regular Show The Movie Trailer.mkv":                          true,
	}
	var checked, flipped, extrasFlipped, markerFlipped int
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
		if extraMarkerFlips140[base] {
			if got.Kind != "extra" || got.Title != recorded.Title || got.Year != 0 || got.Season != 0 || len(got.Episodes) != 0 {
				t.Errorf("v1.4.0 [Extra] flip %s: got kind/title/year/season/episodes = %q/%q/%d/%d/%v, want \"extra\"/%q/0/0/[]",
					base, got.Kind, got.Title, got.Year, got.Season, bulkEps(got.Episodes), recorded.Title)
			} else {
				markerFlipped++
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
	t.Logf("bulk regression: %d basenames checked, %d victims flipped, %d v1.3.6 extras flips, %d v1.4.0 [Extra] flips, %d unexplained deltas", checked, flipped, extrasFlipped, markerFlipped, len(deltas))
	if flipped != len(victims) {
		t.Errorf("victims flipped = %d, want %d (a victim basename is missing from the export or did not flip)", flipped, len(victims))
	}
	if extrasFlipped != len(extrasFlips136) {
		t.Errorf("v1.3.6 extras flips = %d, want %d (a WWDITS basename is missing from the export or did not flip)", extrasFlipped, len(extrasFlips136))
	}
	if markerFlipped != len(extraMarkerFlips140) {
		t.Errorf("v1.4.0 [Extra] flips = %d, want %d (an [Extra] basename is missing from the export or did not flip)", markerFlipped, len(extraMarkerFlips140))
	}
	if len(deltas) > 0 {
		t.Errorf("%d unexplained parse deltas (listed above) — the parser changed more than the named victims and flips", len(deltas))
	}
}

// TestBulk140PlannerPredicateSweep gates the PLANNER-level
// half of the v1.4.0 corpus promise. Rulings 1 and 2 do not
// change what the parser extracts — they divert files inside
// the link planner — so the parse regression above cannot see
// them. This sweep runs the planner's own diversion
// predicates (the same functions, in the same order, behind
// the same sample gate) over EVERY not-found path in the
// real export and pins the qualifying set to exactly the ten
// named files: eight specials that gain created Season 00
// numbers and two year-less movies. Any other qualifier is
// an unexplained delta and fails the test. Paths are handled
// relative to the export's own source root, read from the
// document at runtime.
func TestBulk140PlannerPredicateSweep(t *testing.T) {
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
		t.Fatalf("owner's real export not found: %v", err)
	}
	var doc bulkExport
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse export: %v", err)
	}
	wantSpecials := map[string]bool{
		"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE.mkv":                                      true,
		"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE.mkv":                                        true,
		"Invader.ZIM.S01-S02.(2001-2003).Complete.Series).[OTS]/Specials/Invader ZIM - S00 - An Introduction to Kevin Manthei.avi":                                                                                    true,
		"Invader.ZIM.S01-S02.(2001-2003).Complete.Series).[OTS]/Specials/Invader ZIM - S00 - The Music of ZIM.avi":                                                                                                    true,
		"South.Park.S01-S26.DVDRip.XviD.BDRip.x264-Scene/S15/South.Park.S15.Six.Days.to.Air.The.Making.of.South.Park.BDRip.x264-DEiMOS/South.Park.S15.Six.Days.to.Air.The.Making.of.South.Park.BDRip.x264-DEiMOS.mkv": true,
		"[Trix] Panty & Stocking with Garterbelt S01 - AV1 MiNi/Specials/[Trix] Panty & Stocking in Sanitarybox (OVA) (BD 720p AV1).mkv":                                                                              true,
		"Yu Yu Hakusho/OVA/[Samir755] Yu Yu Hakusho - Opening & Ending Encyclopedia (Dual Audio).mkv":                                                                                                                 true,
		"Yu Yu Hakusho/OVA/[Samir755] Yu Yu Hakusho Picture Drama.mkv":                                                                                                                                                true,
	}
	wantYearless := map[string]bool{
		"Macross (BD 720p) (NakamaSub)/SDF Macross - Do You Remember Love/SDF Macross - Do You Remember Love (BD 720p) (Live-eviL) (NakamaSub).mkv":                 true,
		"Macross Frontier + Movies [10bit BD 720p]/[Hi10] Macross Frontier Movies [BD 720p]/(Hi10)_Macross_Frontier_Itsuwari_no_Utahime_(BD_720p)_(Seto_Otaku).mkv": true,
	}
	gotSpecials := map[string]bool{}
	gotYearless := map[string]bool{}
	for _, nf := range doc.NotFound {
		path := nf.Path
		rel := strings.TrimPrefix(path, doc.SourceRoot+"/")
		name := filepath.Base(path)
		ext := filepath.Ext(name)
		f := orgFile{
			Path: path, Dir: filepath.Dir(path), Name: name,
			Stem: strings.TrimSuffix(name, ext), Ext: strings.ToLower(ext), ExtOrig: ext,
		}
		pn := parseName(path, doc.SourceRoot)
		_, _, _, _, reason := buildVideoTarget("/target", f, pn)
		if reason == "" || isSamplePath(f, doc.SourceRoot) {
			continue
		}
		if _, _, ok := specialSeriesFor(f, pn, doc.SourceRoot); ok {
			gotSpecials[rel] = true
			continue
		}
		if reason == "no year found" {
			if _, ok := yearlessMovieTitle(f, pn, doc.SourceRoot); ok {
				gotYearless[rel] = true
			}
		}
	}
	for rel := range gotSpecials {
		if !wantSpecials[rel] {
			t.Errorf("UNEXPLAINED special qualifier: %s", rel)
		}
	}
	for rel := range wantSpecials {
		if !gotSpecials[rel] {
			t.Errorf("named special did NOT qualify: %s", rel)
		}
	}
	for rel := range gotYearless {
		if !wantYearless[rel] {
			t.Errorf("UNEXPLAINED year-less qualifier: %s", rel)
		}
	}
	for rel := range wantYearless {
		if !gotYearless[rel] {
			t.Errorf("named year-less movie did NOT qualify: %s", rel)
		}
	}
	t.Logf("v1.4.0 predicate sweep: %d specials qualified (want 8), %d year-less qualified (want 2)", len(gotSpecials), len(gotYearless))
}
