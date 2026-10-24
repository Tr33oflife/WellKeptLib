package core

// linkplan_140_real_test.go — v1.4.0 end-to-end on the REAL
// names from the owner's first link-problems export: the ten
// files Rulings 1 and 2 name, their two sample siblings, and
// the sixteen "[Extra]" movie extras with their owning movie
// present. The corpus gate (bulk_export_131_test.go) proves
// NOTHING ELSE in the real tree changes classification; this
// test proves the named files land on their exact targets.
// All paths here are relative — materialized under a temp
// source root, so no real library path appears in the test.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// real140 holds one materialized tree: every path below is
// the export-relative path of a real file.
var real140Files = []string{
	// Ruling 1 — Creepshow specials (+ their samples).
	"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE.mkv",
	"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE/Sample/creepshow.s02.a.creepshow.animated.special.bdrip.x264-bordure.sample.mkv",
	"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE.mkv",
	"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE/Sample/creepshow.s02.a.creepshow.holiday.special.bdrip.x264-bordure.sample.mkv",
	// Ruling 1 — Invader ZIM S00 pieces.
	"Invader.ZIM.S01-S02.(2001-2003).Complete.Series).[OTS]/Specials/Invader ZIM - S00 - An Introduction to Kevin Manthei.avi",
	"Invader.ZIM.S01-S02.(2001-2003).Complete.Series).[OTS]/Specials/Invader ZIM - S00 - The Music of ZIM.avi",
	// Ruling 1 — the South Park making-of.
	"South.Park.S01-S26.DVDRip.XviD.BDRip.x264-Scene/S15/South.Park.S15.Six.Days.to.Air.The.Making.of.South.Park.BDRip.x264-DEiMOS/South.Park.S15.Six.Days.to.Air.The.Making.of.South.Park.BDRip.x264-DEiMOS.mkv",
	// Ruling 1 — the Panty & Stocking OVA in Specials/.
	"[Trix] Panty & Stocking with Garterbelt S01 - AV1 MiNi/Specials/[Trix] Panty & Stocking in Sanitarybox (OVA) (BD 720p AV1).mkv",
	// Ruling 1 — the Yu Yu Hakusho OVA-folder files.
	"Yu Yu Hakusho/OVA/[Samir755] Yu Yu Hakusho - Opening & Ending Encyclopedia (Dual Audio).mkv",
	"Yu Yu Hakusho/OVA/[Samir755] Yu Yu Hakusho Picture Drama.mkv",
	// Ruling 2 — the two year-less movies.
	"Macross (BD 720p) (NakamaSub)/SDF Macross - Do You Remember Love/SDF Macross - Do You Remember Love (BD 720p) (Live-eviL) (NakamaSub).mkv",
	"Macross Frontier + Movies [10bit BD 720p]/[Hi10] Macross Frontier Movies [BD 720p]/(Hi10)_Macross_Frontier_Itsuwari_no_Utahime_(BD_720p)_(Seto_Otaku).mkv",
	// The owning movie of the sixteen [Extra] files (it
	// linked normally in the real tree, so it is not in the
	// export; its path is the standard movie layout inside
	// the folder the export does record).
	"The Regular Show S01-S08 + Movie + Shorts [1080p BluRay x265 HEVC 10bit]/Regular Show The Movie (2015)/Regular Show The Movie (2015).mkv",
}

var real140Extras = []string{
	"[Extra] Animatic Destroying The Timenado.mkv",
	"[Extra] Animatic Opening Fight Scene.mkv",
	"[Extra] Animatic Stop The Timenado.mkv",
	"[Extra] Animatic The File Battle.mkv",
	"[Extra] Animatic The Ship Is Gone.mkv",
	"[Extra] Animatic Time To Save The Universe.mkv",
	"[Extra] Concept Art Gallery.mkv",
	"[Extra] Deleted Animatic Alternate Ending.mkv",
	"[Extra] Deleted Animatic Alternate Fight Scene.mkv",
	"[Extra] Deleted Animatic Alternate Opening.mkv",
	"[Extra] Deleted Animatic Alternate Timeline.mkv",
	"[Extra] Deleted Animatic Fight Scene.mkv",
	"[Extra] Deleted Animatic Mordecai & Rigby Meet Mordecai & Rigby.mkv",
	"[Extra] Deleted Animatic Muscle Man Vs. The Bathroom.mkv",
	"[Extra] Movie Art Gallery.mkv",
	"[Extra] Regular Show The Movie Trailer.mkv",
}

const real140ExtrasDir = "The Regular Show S01-S08 + Movie + Shorts [1080p BluRay x265 HEVC 10bit]/Regular Show The Movie (2015)/DVD Extras:Special Features"

func TestLink140RealNames(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	paths := map[string]string{}
	w := func(rel string) {
		p := filepath.Join(src, rel)
		writeOrgFile(t, p, "bytes-"+rel)
		paths[rel] = p
	}
	for _, rel := range real140Files {
		w(rel)
	}
	for _, name := range real140Extras {
		w(real140ExtrasDir + "/" + name)
	}

	plan := plan140(t, src, tgt, defaultConfig())

	// The eight specials: exact targets, all flagged. Series
	// titles are the parser's own folder-derived identities —
	// the same folders the shows' episodes use.
	wantSpecials := map[string]string{
		"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE.mkv":                                      "Creepshow S01 S04 Scene/Season 00/Creepshow S01 S04 Scene - S00E01.mkv",
		"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE.mkv":                                        "Creepshow S01 S04 Scene/Season 00/Creepshow S01 S04 Scene - S00E02.mkv",
		"Invader.ZIM.S01-S02.(2001-2003).Complete.Series).[OTS]/Specials/Invader ZIM - S00 - An Introduction to Kevin Manthei.avi":                                                                                    "Invader ZIM S01 S02 Series (2001)/Season 00/Invader ZIM S01 S02 Series - S00E01.avi",
		"Invader.ZIM.S01-S02.(2001-2003).Complete.Series).[OTS]/Specials/Invader ZIM - S00 - The Music of ZIM.avi":                                                                                                    "Invader ZIM S01 S02 Series (2001)/Season 00/Invader ZIM S01 S02 Series - S00E02.avi",
		"South.Park.S01-S26.DVDRip.XviD.BDRip.x264-Scene/S15/South.Park.S15.Six.Days.to.Air.The.Making.of.South.Park.BDRip.x264-DEiMOS/South.Park.S15.Six.Days.to.Air.The.Making.of.South.Park.BDRip.x264-DEiMOS.mkv": "South Park S01 S26 Scene/Season 00/South Park S01 S26 Scene - S00E01.mkv",
		"[Trix] Panty & Stocking with Garterbelt S01 - AV1 MiNi/Specials/[Trix] Panty & Stocking in Sanitarybox (OVA) (BD 720p AV1).mkv":                                                                              "Panty & Stocking with Garterbelt S01 MiNi/Season 00/Panty & Stocking with Garterbelt S01 MiNi - S00E01.mkv",
		"Yu Yu Hakusho/OVA/[Samir755] Yu Yu Hakusho - Opening & Ending Encyclopedia (Dual Audio).mkv":                                                                                                                 "Yu Yu Hakusho/Season 00/Yu Yu Hakusho - S00E01.mkv",
		"Yu Yu Hakusho/OVA/[Samir755] Yu Yu Hakusho Picture Drama.mkv":                                                                                                                                                "Yu Yu Hakusho/Season 00/Yu Yu Hakusho - S00E02.mkv",
	}
	for rel, wantRel := range wantSpecials {
		e := linkEntryFor(plan, paths[rel])
		if e == nil {
			t.Errorf("special %s: no link entry (not_identified: %+v)", rel, plan.NotIdentified)
			continue
		}
		if e.Target != filepath.Join(tgt, wantRel) {
			t.Errorf("special %s:\n target = %s\n want   %s", rel, e.Target, filepath.Join(tgt, wantRel))
		}
		if !e.NumberAssigned || e.Kind != "episode" {
			t.Errorf("special %s: kind/assigned = %q/%v, want episode/true", rel, e.Kind, e.NumberAssigned)
		}
	}
	// The Creepshow samples stay not-identified (sample gate).
	for _, rel := range []string{
		"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Animated.Special.BDRip.x264-BORDURE/Sample/creepshow.s02.a.creepshow.animated.special.bdrip.x264-bordure.sample.mkv",
		"Creepshow.S01-S04.BDRip.x264-Scene/S02/Creepshow.S02.A.Creepshow.Holiday.Special.BDRip.x264-BORDURE/Sample/creepshow.s02.a.creepshow.holiday.special.bdrip.x264-bordure.sample.mkv",
	} {
		if ni := notIdentifiedFor(plan, paths[rel]); ni == nil {
			t.Errorf("sample %s was identified — the sample gate must hold it", rel)
		}
	}
	// The two year-less movies: exact targets, flagged.
	wantYearless := map[string]string{
		"Macross (BD 720p) (NakamaSub)/SDF Macross - Do You Remember Love/SDF Macross - Do You Remember Love (BD 720p) (Live-eviL) (NakamaSub).mkv":                 "SDF Macross Do You Remember Love/SDF Macross Do You Remember Love.mkv",
		"Macross Frontier + Movies [10bit BD 720p]/[Hi10] Macross Frontier Movies [BD 720p]/(Hi10)_Macross_Frontier_Itsuwari_no_Utahime_(BD_720p)_(Seto_Otaku).mkv": "Macross Frontier Itsuwari no Utahime/Macross Frontier Itsuwari no Utahime.mkv",
	}
	for rel, wantRel := range wantYearless {
		e := linkEntryFor(plan, paths[rel])
		if e == nil {
			t.Errorf("year-less movie %s: no link entry", rel)
			continue
		}
		if e.Target != filepath.Join(tgt, wantRel) {
			t.Errorf("year-less movie %s:\n target = %s\n want   %s", rel, e.Target, filepath.Join(tgt, wantRel))
		}
		if !e.NoYear || e.Kind != "movie" {
			t.Errorf("year-less movie %s: kind/no_year = %q/%v, want movie/true", rel, e.Kind, e.NoYear)
		}
	}
	// The sixteen [Extra] files: all link as extras under
	// the movie's own extras folder.
	extras := entriesOfKind(plan, "extra")
	if len(extras) != 16 {
		t.Fatalf("extras linked = %d, want 16 (not_identified: %+v)", len(extras), plan.NotIdentified)
	}
	extrasPrefix := filepath.Join(tgt, "Regular Show The Movie (2015)", "extras") + string(os.PathSeparator)
	for _, e := range extras {
		if !strings.HasPrefix(e.Target, extrasPrefix) {
			t.Errorf("extra target %s is not under the movie's extras folder", e.Target)
		}
	}
	spot := map[string]string{
		"[Extra] Animatic Destroying The Timenado.mkv":                        "Animatic Destroying The Timenado.mkv",
		"[Extra] Deleted Animatic Mordecai & Rigby Meet Mordecai & Rigby.mkv": "Deleted Animatic Mordecai & Rigby Meet Mordecai & Rigby.mkv",
		"[Extra] Regular Show The Movie Trailer.mkv":                          "Regular Show The Movie Trailer.mkv",
	}
	for name, wantFile := range spot {
		e := linkEntryFor(plan, paths[real140ExtrasDir+"/"+name])
		if e == nil || e.Target != filepath.Join(tgt, "Regular Show The Movie (2015)", "extras", wantFile) {
			t.Errorf("extra %s: entry = %+v, want target .../extras/%s", name, e, wantFile)
		}
	}
	// The owning movie itself links normally.
	movie := "The Regular Show S01-S08 + Movie + Shorts [1080p BluRay x265 HEVC 10bit]/Regular Show The Movie (2015)/Regular Show The Movie (2015).mkv"
	if e := linkEntryFor(plan, paths[movie]); e == nil || e.Kind != "movie" || e.NoYear {
		t.Errorf("the owning movie entry = %+v, want a normal movie link", e)
	}
	// Nothing in this tree is left unidentified except the
	// two samples.
	if len(plan.NotIdentified) != 2 {
		t.Errorf("not_identified = %d, want exactly the 2 samples: %+v", len(plan.NotIdentified), plan.NotIdentified)
	}
	if plan.Stats.Extras != 16 {
		t.Errorf("stats.extras = %d, want 16", plan.Stats.Extras)
	}
}

// The parser addition on its own: a leading "[Extra]"/
// "[Extras]" bracket classifies; group brackets and
// non-leading brackets do not.
func TestClassifyExtraBracket140(t *testing.T) {
	root := "/home/user/media"
	cases := []struct {
		rel       string
		wantKind  string
		wantTitle string // extra cases only
	}{
		{"Show/Movie (2015)/[Extra] Deleted Scene.mkv", "extra", "Deleted Scene"},
		{"Show/Movie (2015)/[Extras] Trailer.mkv", "extra", "Trailer"},
		{"Show/Movie (2015)/[Extra] Animatic - The Fight.mkv", "extra", "Animatic The Fight"},
		{"Show/[Group] Show - S01E01 - Pilot.mkv", "episode", ""},
		{"Show/Show - S01E01 [Extra].mkv", "episode", ""},
	}
	for _, c := range cases {
		pn := parseName(root+"/"+c.rel, root)
		if pn.Kind != c.wantKind {
			t.Errorf("%s: kind = %q, want %q", c.rel, pn.Kind, c.wantKind)
			continue
		}
		if c.wantTitle != "" && pn.Title != c.wantTitle {
			t.Errorf("%s: title = %q, want %q", c.rel, pn.Title, c.wantTitle)
		}
	}
	// A bracket that merely STARTS with the letters is not
	// the marker: "[ExtraStuff] …" must not classify.
	pn := parseName(root+"/Show/Movie (2015)/[ExtraStuff] Deleted Scene.mkv", root)
	if pn.Kind == "extra" {
		t.Errorf("[ExtraStuff] classified as extra — only the exact word is the marker")
	}
}

// The three v1.4.0 switches: presence-aware load (absent =
// ON, explicit OFF honored) and preference status (a tuning
// reset leaves them alone).
func TestConfig140Toggles(t *testing.T) {
	d := defaultConfig()
	if !d.LinkExtras || !d.AssignSpecialNumbers || !d.AllowYearlessMovies {
		t.Fatalf("defaults = %v/%v/%v, want all ON", d.LinkExtras, d.AssignSpecialNumbers, d.AllowYearlessMovies)
	}
	dir := t.TempDir()
	writeCfg := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(`{"roots":[]}`)
	c, _ := loadConfig(dir)
	if !c.LinkExtras || !c.AssignSpecialNumbers || !c.AllowYearlessMovies {
		t.Errorf("keyless config = %v/%v/%v, want all ON (pre-1.4.0 configs must not lose the new behaviors)", c.LinkExtras, c.AssignSpecialNumbers, c.AllowYearlessMovies)
	}
	writeCfg(`{"link_extras":false}`)
	c, _ = loadConfig(dir)
	if c.LinkExtras || !c.AssignSpecialNumbers || !c.AllowYearlessMovies {
		t.Errorf("one explicit OFF = %v/%v/%v, want false/true/true", c.LinkExtras, c.AssignSpecialNumbers, c.AllowYearlessMovies)
	}
	writeCfg(`{"link_extras":false,"assign_special_numbers":false,"allow_yearless_movies":false}`)
	c, _ = loadConfig(dir)
	if c.LinkExtras || c.AssignSpecialNumbers || c.AllowYearlessMovies {
		t.Errorf("all explicit OFF = %v/%v/%v, want all false", c.LinkExtras, c.AssignSpecialNumbers, c.AllowYearlessMovies)
	}
	// Preferences survive a tuning reset.
	svc := linkSvc(t)
	cfg := svc.GetConfig()
	cfg.LinkExtras = false
	cfg.AssignSpecialNumbers = false
	cfg.AllowYearlessMovies = false
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetTuning(); err != nil {
		t.Fatal(err)
	}
	got := svc.GetConfig()
	if got.LinkExtras || got.AssignSpecialNumbers || got.AllowYearlessMovies {
		t.Errorf("after ResetTuning = %v/%v/%v, want all still OFF (preferences, not tuning)", got.LinkExtras, got.AssignSpecialNumbers, got.AllowYearlessMovies)
	}
}
