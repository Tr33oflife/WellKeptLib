package core

// linkplan_141_test.go — v1.4.1: pack-level extras find
// their owner by context identity, and samples carry their
// own not-identified label.
//
// The pack fixtures use the owner's REAL file lists, taken
// from the preserved problems exports (the same paths the
// owner's fresh v1.4.0 preview still refused): 36 Mighty
// Boosh Featurettes, 23 Regular Show series extras (10 in
// "S01-02 DVD Extras" + 13 in "Shorts/…"), 1 Gunsmith Cats
// NCOP — the 60 named flips of this release, each pinned
// with its target. Companion videos (the packs' episodes
// and movies) are fixture-built in the shapes the diagnosis
// reproduced: a series registered under two folder
// identities at one pack root (Boosh), a series plus a
// movie at one pack root (Regular Show, Gunsmith Cats).

import (
	"path/filepath"
	"strings"
	"testing"
)

func buildPack141(t *testing.T, files []string) (string, string, map[string]string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	paths := map[string]string{}
	for _, rel := range files {
		p := filepath.Join(src, rel)
		writeOrgFile(t, p, "bytes-"+rel)
		paths[rel] = p
	}
	return src, tgt, paths
}

func extrasTargetDir(entries []LinkEntry, source string) string {
	for _, e := range entries {
		if e.Source == source {
			return e.Target
		}
	}
	return ""
}

// The Mighty Boosh pack: episodes loose in the pack root
// parse year-less ("The Mighty Boosh") while the season
// folders pick the pack's (2003) up ("The Mighty Boosh
// (2003)") — two work entries at the deciding folder, so
// the v1.4.0 one-work rule refused all 36 Featurettes. The
// pack root's own identity names the (2003) work.
func TestExtras141BooshPack(t *testing.T) {
	requireIdentity(t)
	root := "The.Mighty.Boosh.(2003).Season.1-3.S01-S03.(576p.DVD.x265.HEVC.10bit.AAC.2.0.Panda)"
	featurettes := []string{
		"(Un)Natural Acts.mkv",
		"Bob Fossil Makes Love to the Camera.mkv",
		"Bob Fossil's Zoo Announcements.mkv",
		"Bollo's Drums.mkv",
		"Boosh Music - Season 1.mkv",
		"Boosh Music - Season 3.mkv",
		"Boosh Music Season 2.mkv",
		"Boosh Night Links.mkv",
		"Boosh Night Live.mkv",
		"Boosh Pilot Deleted Scenes.mkv",
		"Boosh Publicity 2.mkv",
		"Boosh Publicity.mkv",
		"Boosh Q&A at the Institute of Contemporary Arts.mkv",
		"Crimps Complete Collection.mkv",
		"Dave Stewart Interview.mkv",
		"Deleted Scenes - Season 3.mkv",
		"Deleted and Extended Scenes - Season 2.mkv",
		"How to Draw Paul King.mkv",
		"In the Studio.mkv",
		"Making Boosh 3.mkv",
		"Making Sammy The Crab.mkv",
		"Mint Royale - Blue Song.mkv",
		"Noel & Julien Record Parker People Chant.mkv",
		"Outtakes - Season 1.mkv",
		"Outtakes - Season 2.mkv",
		"Outtakes - Season 3.mkv",
		"Pilot Outtakes.mkv",
		"RTS Awards London 2008.mkv",
		"Season 3 Trailer.mkv",
		"Series 1 DVD Commentary Outtakes.mkv",
		"Sweet (2000).mkv",
		"The Mighty Boosh - A History.mkv",
		"The Mighty Boosh - Inside the Zooniverse.mkv",
		"The Mighty Boosh - The Making of Series 2.mkv",
		"The Mighty Boosh A Journey Through Time and Space.mkv",
		"Two Girls Doing A Crimp.mkv",
	}
	files := []string{
		root + "/The Mighty Boosh - S03E01 - The Nightmare of Milky Joe.mkv",
		root + "/The Mighty Boosh - S03E02 - The Priest and the Beast.mkv",
		root + "/Season 1/The Mighty Boosh - S01E01 - Tundra.mkv",
		root + "/Season 1/The Mighty Boosh - S01E02 - Jungle.mkv",
		root + "/Season 2/The Mighty Boosh - S02E01 - Call of the Yeti.mkv",
	}
	for _, f := range featurettes {
		files = append(files, root+"/Featurettes/"+f)
	}
	src, tgt, paths := buildPack141(t, files)
	plan := plan140(t, src, tgt, defaultConfig())

	if len(plan.NotIdentified) != 0 {
		t.Fatalf("not_identified = %+v, want none — every pack file resolves", plan.NotIdentified)
	}
	extras := entriesOfKind(plan, "extra")
	if len(extras) != len(featurettes) {
		t.Fatalf("extras to create = %d, want %d", len(extras), len(featurettes))
	}
	wantDir := filepath.Join(tgt, "The Mighty Boosh (2003)", "extras")
	for _, f := range featurettes {
		rel := root + "/Featurettes/" + f
		got := extrasTargetDir(extras, paths[rel])
		if filepath.Dir(got) != wantDir {
			t.Errorf("featurette %s: target = %s, want under %s", f, got, wantDir)
		}
	}
	// Exact targets for the two names the diagnosis printed.
	if got := extrasTargetDir(extras, paths[root+"/Featurettes/Boosh Pilot Deleted Scenes.mkv"]); got != filepath.Join(wantDir, "Boosh Pilot Deleted Scenes.mkv") {
		t.Errorf("Boosh Pilot Deleted Scenes target = %s", got)
	}
	if got := extrasTargetDir(extras, paths[root+"/Featurettes/Outtakes - Season 1.mkv"]); got != filepath.Join(wantDir, "Outtakes Season 1.mkv") {
		t.Errorf("Outtakes - Season 1 target = %s", got)
	}
}

// The Regular Show pack: the pack root holds the series AND
// "Regular Show The Movie (2015)" — two works at the
// deciding folder for every root-level extras folder, so
// v1.4.0 refused all 23 series extras while the movie's own
// 16 extras (decided inside the movie folder) linked. The
// fix must flip the 23 to the series and leave the movie's
// 16 targets exactly as v1.4.0 built them.
func TestExtras141RegularShowPack(t *testing.T) {
	requireIdentity(t)
	root := "The Regular Show S01-S08 + Movie + Shorts [1080p BluRay x265 HEVC 10bit]"
	dvdExtras := []string{
		"2010 Comic-Con Teaser Trailer.mkv",
		"Animatic For 'The Power'.mkv",
		"Animatic For The Unaired Pilot.mkv",
		"Behind The Voices.mkv",
		"JG Pitches 'The Power'.mkv",
		"Party Tonight Music Video.mkv",
		"Sam Sings Mystery Karaoke.mkv",
		"Secret Prints.mkv",
		"The Unaired Regular Show Pilot.mkv",
		"nterview With Jg Quintel.mkv",
	}
	shorts := []string{
		"College Shorts (2005-06)/'The Naive Man from Lolliland' Student Short.mkv",
		"College Shorts (2005-06)/2 In The AM PM.mkv",
		"DVD Shorts (2011)/Mordecai and Rigby Ringtoneers.mkv",
		"Season 6 Shorts/Fun Run (Short).mp4",
		"Season 6 Shorts/Ooohh! (Short).mp4",
		"Season 6 Shorts/Regular Show (Short).mp4",
		"Season 6 Shorts/USA! USA! (Short).mp4",
		"Season 7 Shorts/1973 Tetherball Championship Trophy (Short).mp4",
		"Season 7 Shorts/Coming Soon (Short).mp4",
		"Season 7 Shorts/Ninja Shoes (Short).mp4",
		"Season 7 Shorts/Pizza Pouch Drop (Short).mp4",
		"Season 7 Shorts/Sick Day (Short).mp4",
		"Season 8 Shorts/2001 A Nap Odyssey (Short).mp4",
	}
	movieExtras := []string{
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
	files := []string{
		root + "/Season 01/Regular Show - S01E01 - The Power.mkv",
		root + "/Season 01/Regular Show - S01E02 - Just Set Up the Chairs.mkv",
		root + "/Season 02/Regular Show - S02E01 - Ello Gov'nor.mkv",
		root + "/Regular Show The Movie (2015)/Regular Show The Movie (2015).mkv",
	}
	for _, f := range dvdExtras {
		files = append(files, root+"/S01-02 DVD Extras/"+f)
	}
	for _, f := range shorts {
		files = append(files, root+"/Shorts/"+f)
	}
	for _, f := range movieExtras {
		files = append(files, root+"/Regular Show The Movie (2015)/DVD Extras:Special Features/"+f)
	}
	src, tgt, paths := buildPack141(t, files)
	plan := plan140(t, src, tgt, defaultConfig())

	if len(plan.NotIdentified) != 0 {
		t.Fatalf("not_identified = %+v, want none — every pack file resolves", plan.NotIdentified)
	}
	extras := entriesOfKind(plan, "extra")
	if len(extras) != len(dvdExtras)+len(shorts)+len(movieExtras) {
		t.Fatalf("extras to create = %d, want %d", len(extras), len(dvdExtras)+len(shorts)+len(movieExtras))
	}
	seriesDir := filepath.Join(tgt, "Regular Show", "extras")
	for _, f := range dvdExtras {
		got := extrasTargetDir(extras, paths[root+"/S01-02 DVD Extras/"+f])
		if filepath.Dir(got) != seriesDir {
			t.Errorf("series extra %s: target = %s, want under %s", f, got, seriesDir)
		}
	}
	for _, f := range shorts {
		got := extrasTargetDir(extras, paths[root+"/Shorts/"+f])
		if filepath.Dir(got) != seriesDir {
			t.Errorf("short %s: target = %s, want under %s", f, got, seriesDir)
		}
	}
	movieDir := filepath.Join(tgt, "Regular Show The Movie (2015)", "extras")
	for _, f := range movieExtras {
		got := extrasTargetDir(extras, paths[root+"/Regular Show The Movie (2015)/DVD Extras:Special Features/"+f])
		if filepath.Dir(got) != movieDir {
			t.Errorf("movie extra %s: target = %s, want under %s (its v1.4.0 home)", f, got, movieDir)
		}
	}
	// Exact targets, one per group.
	if got := extrasTargetDir(extras, paths[root+"/S01-02 DVD Extras/The Unaired Regular Show Pilot.mkv"]); got != filepath.Join(seriesDir, "The Unaired Regular Show Pilot.mkv") {
		t.Errorf("Unaired Pilot target = %s", got)
	}
	if got := extrasTargetDir(extras, paths[root+"/Regular Show The Movie (2015)/DVD Extras:Special Features/[Extra] Animatic Destroying The Timenado.mkv"]); got != filepath.Join(movieDir, "Animatic Destroying The Timenado.mkv") {
		t.Errorf("movie animatic target = %s", got)
	}
	if got := extrasTargetDir(extras, paths[root+"/Regular Show The Movie (2015)/DVD Extras:Special Features/[Extra] Regular Show The Movie Trailer.mkv"]); got != filepath.Join(movieDir, "Regular Show The Movie Trailer.mkv") {
		t.Errorf("movie trailer target = %s", got)
	}
}

// Gunsmith Cats: the pack root holds the series' OVA
// episodes directly, the NCOP in the root, and one more
// target-built work — two works at the only deciding
// folder, so v1.4.0 refused the NCOP even though its chain
// names the series. (With ONLY the episodes present the
// old rule linked it; the refusal itself proves a second
// work entry shares the live root.)
func TestExtras141GunsmithPack(t *testing.T) {
	requireIdentity(t)
	root := "Gunsmith Cats (BDRip 1440x1080p x265 HEVC FLAC, AC3x4 2.0x4+5.1)(Dual Audio)[sxales]"
	ncop := root + "/Gunsmith Cats - NCOP (BDRip 1440x1080p x265 HEVC AC3 2.0)[sxales].mkv"
	files := []string{
		root + "/Gunsmith Cats - 01 (BDRip 1440x1080p x265 HEVC AC3 2.0)[sxales].mkv",
		root + "/Gunsmith Cats - 02 (BDRip 1440x1080p x265 HEVC AC3 2.0)[sxales].mkv",
		root + "/Gunsmith Cats - 03 (BDRip 1440x1080p x265 HEVC AC3 2.0)[sxales].mkv",
		root + "/Bundled Movie (1995).mkv",
		ncop,
	}
	src, tgt, paths := buildPack141(t, files)
	plan := plan140(t, src, tgt, defaultConfig())

	if len(plan.NotIdentified) != 0 {
		t.Fatalf("not_identified = %+v, want none — every pack file resolves", plan.NotIdentified)
	}
	extras := entriesOfKind(plan, "extra")
	if len(extras) != 1 {
		t.Fatalf("extras to create = %d, want 1 (the NCOP)", len(extras))
	}
	want := filepath.Join(tgt, "Gunsmith Cats", "extras", "Gunsmith Cats 1440x1080p.mkv")
	if got := extrasTargetDir(extras, paths[ncop]); got != want {
		t.Errorf("NCOP target = %s, want %s", got, want)
	}
	// The second work is undisturbed.
	mv := linkEntryFor(plan, paths[root+"/Bundled Movie (1995).mkv"])
	if mv == nil || mv.Target != filepath.Join(tgt, "Bundled Movie (1995)", "Bundled Movie (1995).mkv") {
		t.Errorf("bundled movie entry = %+v", mv)
	}
}

// The refusal stays real: a pack root genuinely mixing two
// different SERIES, with extras no folder identity can
// attribute, still refuses — reported, never guessed.
func TestExtras141TwoSeriesRefusal(t *testing.T) {
	requireIdentity(t)
	files := []string{
		"Two Shows Pack/Alpha Show/Season 01/Alpha Show - S01E01.mkv",
		"Two Shows Pack/Beta Show/Season 01/Beta Show - S01E01.mkv",
		"Two Shows Pack/Extras/Alpha Show - Deleted Scene.mkv",
		"Two Shows Pack/Beta Show-trailer.mkv",
	}
	src, tgt, paths := buildPack141(t, files)
	plan := plan140(t, src, tgt, defaultConfig())

	if got := entriesOfKind(plan, "extra"); len(got) != 0 {
		t.Fatalf("extras to create = %d, want 0 — ambiguous pack extras refuse", len(got))
	}
	for _, rel := range []string{
		"Two Shows Pack/Extras/Alpha Show - Deleted Scene.mkv",
		"Two Shows Pack/Beta Show-trailer.mkv",
	} {
		ni := notIdentifiedFor(plan, paths[rel])
		if ni == nil || ni.Reason != "extra content — left in place" {
			t.Errorf("%s: not_identified = %+v, want the extra-content refusal", rel, ni)
		}
	}
	// Both series still link normally.
	if len(plan.LinksToCreate) != 2 {
		t.Errorf("links to create = %d, want the 2 episodes", len(plan.LinksToCreate))
	}
}

// Re-plan stability: after a pack build is applied, a fresh
// plan over the same tree must resolve the same owners —
// the evidence set includes the already-linked videos, so
// the pack-level extras come back already-linked instead of
// flipping to refused.
func TestExtras141ReplanKeepsOwners(t *testing.T) {
	requireIdentity(t)
	root := "Mini Show S01-S02 + Movie [1080p BluRay x265 HEVC 10bit]"
	files := []string{
		root + "/Season 01/Mini Show - S01E01.mkv",
		root + "/Mini Show The Movie (2020)/Mini Show The Movie (2020).mkv",
		root + "/Mini Show The Movie (2020)/Mini Show The Movie (2020)-trailer.mkv",
		root + "/DVD Extras/Mini Show - Deleted Scenes.mkv",
	}
	src, tgt, paths := buildPack141(t, files)

	svc := linkSvc(t)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan.LinksToCreate) != 4 || len(entriesOfKind(plan, "extra")) != 2 {
		t.Fatalf("first plan = %d links (%d extras), want 4 (2)", len(plan.LinksToCreate), len(entriesOfKind(plan, "extra")))
	}
	seriesExtra := paths[root+"/DVD Extras/Mini Show - Deleted Scenes.mkv"]
	first := linkEntryFor(plan, seriesExtra)
	if first == nil || !strings.HasPrefix(first.Target, filepath.Join(tgt, "Mini Show", "extras")+string(filepath.Separator)) {
		t.Fatalf("series extra first-plan target = %+v, want the series extras folder", first)
	}
	job, err := svc.StartLinkApplyJob(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := waitLinkJob(t, svc, job.ID)
	if st.State != "done" || st.ApplyResult == nil || st.ApplyResult.Created != 4 {
		t.Fatalf("apply job = %+v, want done with 4 created", st)
	}
	plan2 := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan2.LinksToCreate) != 0 || len(plan2.AlreadyLinked) != 4 {
		t.Fatalf("second plan = %d to create / %d already, want 0/4", len(plan2.LinksToCreate), len(plan2.AlreadyLinked))
	}
	if len(plan2.NotIdentified) != 0 {
		t.Fatalf("second plan not_identified = %+v, want none — owners must survive a re-plan", plan2.NotIdentified)
	}
	again := alreadyEntryFor(plan2, seriesExtra)
	if again == nil || again.Kind != "extra" || again.Target != first.Target {
		t.Errorf("series extra after re-plan = %+v, want already-linked extra at %s", again, first.Target)
	}
}

// The v1.4.1 label split, fixture-pinned in both planners:
// a Sample-folder file and a *-sample file report
// "sample — left in place"; a genuine extra still links
// (link mode) / reports the extra-content reason (move
// mode). Classification and outcomes are unchanged.
func TestSampleLabel141(t *testing.T) {
	requireIdentity(t)
	files := []string{
		"Label Show (2021)/Season 01/Label Show - S01E01.mkv",
		"Label Show (2021)/Sample/Label Show-trailer.mkv",
		"Label Show (2021)/Extras/Label Show - Gag Reel-sample.mkv",
		"Label Show (2021)/Extras/Label Show - Deleted Scenes.mkv",
	}
	src, tgt, paths := buildPack141(t, files)
	plan := plan140(t, src, tgt, defaultConfig())

	for _, rel := range []string{
		"Label Show (2021)/Sample/Label Show-trailer.mkv",
		"Label Show (2021)/Extras/Label Show - Gag Reel-sample.mkv",
	} {
		ni := notIdentifiedFor(plan, paths[rel])
		if ni == nil || ni.Reason != "sample — left in place" {
			t.Errorf("link plan: %s not_identified = %+v, want the sample reason", rel, ni)
		}
	}
	extras := entriesOfKind(plan, "extra")
	if len(extras) != 1 {
		t.Fatalf("extras to create = %d, want 1 (the genuine extra only)", len(extras))
	}
	want := filepath.Join(tgt, "Label Show (2021)", "extras", "Label Show Deleted Scenes.mkv")
	if got := extrasTargetDir(extras, paths["Label Show (2021)/Extras/Label Show - Deleted Scenes.mkv"]); got != want {
		t.Errorf("genuine extra target = %s, want %s", got, want)
	}

	// Move mode: same labels on the unidentified list.
	mplan := mustPlan(t, src)
	for _, rel := range []string{
		"Label Show (2021)/Sample/Label Show-trailer.mkv",
		"Label Show (2021)/Extras/Label Show - Gag Reel-sample.mkv",
	} {
		wantUnidentified(t, mplan, paths[rel], "sample — left in place")
	}
	wantUnidentified(t, mplan, paths["Label Show (2021)/Extras/Label Show - Deleted Scenes.mkv"], "extra content — left in place")
}
