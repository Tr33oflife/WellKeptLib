package core

// linkplan_140_test.go — v1.4.0: extras link into the built
// library, specials gain CREATED Season 00 numbers, and
// year-less movies link on folder context. The corpus gate
// for the release lives in bulk_export_131_test.go (843 real
// basenames: exactly the 16 "[Extra]" parse flips there, and
// the predicate sweep pinning the 8 + 2 planner diversions);
// these tests pin the mechanics on fixture trees: exact
// targets, owner resolution, the sample gate, toggle-off
// parity, and the apply → already-linked → undo round trip
// through the v1.3.5 job engine.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func plan140(t *testing.T, src, tgt string, cfg *Config) *LinkPlan {
	t.Helper()
	plan, err := planLinkLibrary(src, tgt, cfg, nil)
	if err != nil {
		t.Fatalf("planLinkLibrary(%s, %s): %v", src, tgt, err)
	}
	return plan
}

func entriesOfKind(plan *LinkPlan, kind string) []LinkEntry {
	var out []LinkEntry
	for _, e := range plan.LinksToCreate {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func notIdentifiedFor(plan *LinkPlan, source string) *LinkSkip {
	for i := range plan.NotIdentified {
		if plan.NotIdentified[i].Path == source {
			return &plan.NotIdentified[i]
		}
	}
	return nil
}

func alreadyEntryFor(plan *LinkPlan, source string) *LinkEntry {
	for i := range plan.AlreadyLinked {
		if plan.AlreadyLinked[i].Source == source {
			return &plan.AlreadyLinked[i]
		}
	}
	return nil
}

// The brief's extras fixture: a season EXTRAS pack, a
// season-level "DVD Extras" folder, a series-level "DVD
// Extras" folder, a movie folder with extras (one
// "[Extra]"-prefixed), Sample material in TV and movie
// contexts, an extras folder whose show has no identifiable
// episodes, and two extras cleaning to the same target name.
func buildExtrasFixture140(t *testing.T) (string, string, map[string]string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	paths := map[string]string{}
	w := func(rel string) string {
		p := filepath.Join(src, rel)
		writeOrgFile(t, p, "bytes-"+rel)
		paths[rel] = p
		return p
	}
	w("Fixture Show (2020)/Season 01/Fixture Show - S01E01 - Pilot.mkv")
	w("Fixture Show (2020)/Season 01/Fixture Show - S01E02 - Second.mkv")
	w("Fixture Show (2020)/Season 01/EXTRAS/Fixture Show - S01E01 - Commentary.mkv")
	w("Fixture Show (2020)/Season 01/EXTRAS/Fixture Show - Gag Reel.mkv")
	w("Fixture Show (2020)/Season 02/Fixture Show - S02E01 - Return.mkv")
	w("Fixture Show (2020)/Season 02/Season 2 - DVD Extras/Fixture Show - Deleted Scenes.mkv")
	w("Fixture Show (2020)/Season 02/EXTRAS/Fixture Show - Gag Reel.mkv")
	w("Fixture Show (2020)/DVD Extras/Fixture Show - Behind the Scenes.mkv")
	w("Fixture Show (2020)/Sample/Fixture Show - Trailer-sample.mkv")
	w("Fixture Show (2020)/DVD Extras/Fixture Show - Gag Reel-sample.mkv")
	w("Solo Film (2021)/Solo Film (2021).mkv")
	w("Solo Film (2021)/Solo Film (2021)-trailer.mkv")
	w("Solo Film (2021)/[Extra] Solo Film Deleted Scene.mkv")
	w("Solo Film (2021)/Sample/Solo Film (2021)-featurette.mkv")
	w("Mystery Show/DVD Extras/Mystery Show - Trailer.mkv")
	return src, tgt, paths
}

func TestLinkExtras140FixtureTree(t *testing.T) {
	requireIdentity(t)
	src, tgt, paths := buildExtrasFixture140(t)
	plan := plan140(t, src, tgt, defaultConfig())

	wantExtras := map[string]string{ // source rel -> target rel
		"Fixture Show (2020)/Season 01/EXTRAS/Fixture Show - S01E01 - Commentary.mkv":           "Fixture Show (2020)/extras/Fixture Show S01E01 Commentary.mkv",
		"Fixture Show (2020)/Season 02/Season 2 - DVD Extras/Fixture Show - Deleted Scenes.mkv": "Fixture Show (2020)/extras/Fixture Show Deleted Scenes.mkv",
		"Fixture Show (2020)/DVD Extras/Fixture Show - Behind the Scenes.mkv":                   "Fixture Show (2020)/extras/Fixture Show Behind the Scenes.mkv",
		"Solo Film (2021)/Solo Film (2021)-trailer.mkv":                                         "Solo Film (2021)/extras/Solo Film.mkv",
		"Solo Film (2021)/[Extra] Solo Film Deleted Scene.mkv":                                  "Solo Film (2021)/extras/Solo Film Deleted Scene.mkv",
	}
	extras := entriesOfKind(plan, "extra")
	if len(extras) != len(wantExtras) {
		t.Fatalf("extras to create = %d, want %d (all entries: %+v)", len(extras), len(wantExtras), plan.LinksToCreate)
	}
	if plan.Stats.Extras != len(wantExtras) {
		t.Errorf("stats.extras = %d, want %d", plan.Stats.Extras, len(wantExtras))
	}
	// Series-folder identity: the show's extras land under the
	// SAME series folder its episodes use.
	ep := linkEntryFor(plan, paths["Fixture Show (2020)/Season 01/Fixture Show - S01E01 - Pilot.mkv"])
	if ep == nil {
		t.Fatal("the pilot episode has no link entry")
	}
	seriesFolder := filepath.Dir(filepath.Dir(ep.Target))
	for rel, wantRel := range wantExtras {
		e := linkEntryFor(plan, paths[rel])
		if e == nil {
			t.Errorf("extra %s: no link entry", rel)
			continue
		}
		if want := filepath.Join(tgt, wantRel); e.Target != want {
			t.Errorf("extra %s: target = %s, want %s", rel, e.Target, want)
		}
	}
	for _, e := range extras {
		if strings.HasPrefix(e.Target, filepath.Join(seriesFolder, "extras")) {
			continue
		}
		if strings.HasPrefix(e.Target, filepath.Join(tgt, "Solo Film (2021)", "extras")) {
			continue
		}
		t.Errorf("extra target %s is not inside its work's extras folder", e.Target)
	}

	// Samples never link: all three stay not-identified.
	// v1.4.1 deliberate label split: they carry the sample
	// reason, not the extra-content reason (classification
	// and outcome unchanged — see RESULTS-1.4.1).
	for _, rel := range []string{
		"Fixture Show (2020)/Sample/Fixture Show - Trailer-sample.mkv",
		"Fixture Show (2020)/DVD Extras/Fixture Show - Gag Reel-sample.mkv",
		"Solo Film (2021)/Sample/Solo Film (2021)-featurette.mkv",
	} {
		ni := notIdentifiedFor(plan, paths[rel])
		if ni == nil || ni.Reason != "sample — left in place" {
			t.Errorf("sample %s: not_identified = %+v, want the sample reason", rel, ni)
		}
	}
	// The ownerless extra stays not-identified too.
	ni := notIdentifiedFor(plan, paths["Mystery Show/DVD Extras/Mystery Show - Trailer.mkv"])
	if ni == nil || ni.Reason != "extra content — left in place" {
		t.Errorf("ownerless extra: not_identified = %+v", ni)
	}
	if len(plan.NotIdentified) != 4 {
		t.Errorf("not_identified = %d, want 4 (3 samples + 1 ownerless extra)", len(plan.NotIdentified))
	}
	// The two Gag Reels clean to ONE target name: a collision
	// in which NEITHER links — never a keeper contest, never
	// an overwrite.
	gagTarget := filepath.Join(tgt, "Fixture Show (2020)", "extras", "Fixture Show Gag Reel.mkv")
	for _, e := range plan.LinksToCreate {
		if e.Target == gagTarget {
			t.Errorf("a Gag Reel was planned to %s — same-name extras must collide, not link", gagTarget)
		}
	}
	found := false
	for _, c := range plan.Collisions {
		if c.Target != gagTarget {
			continue
		}
		found = true
		if c.KeeperSource != "" {
			t.Errorf("gag collision has a keeper %q — extras never hold a keeper contest", c.KeeperSource)
		}
		if len(c.Alternates) != 2 {
			t.Errorf("gag collision alternates = %d, want both Gag Reels", len(c.Alternates))
		}
	}
	if !found {
		t.Errorf("no collision recorded for the shared extras target %s", gagTarget)
	}
}

// Toggle OFF reproduces the pre-1.4.0 plan outcomes: every
// extras file is not-identified, no extras entries exist,
// and the rest of the plan is unchanged. (v1.4.1: sample
// files carry the sample reason in both switch states —
// the label split is orthogonal to the toggle; outcomes
// are what parity pins.)
func TestLinkExtras140ToggleOffParity(t *testing.T) {
	requireIdentity(t)
	src, tgt, paths := buildExtrasFixture140(t)
	on := plan140(t, src, tgt, defaultConfig())
	cfg := defaultConfig()
	cfg.LinkExtras = false
	off := plan140(t, src, tgt, cfg)

	if got := entriesOfKind(off, "extra"); len(got) != 0 {
		t.Errorf("toggle off: %d extras entries, want 0", len(got))
	}
	if off.Stats.Extras != 0 {
		t.Errorf("toggle off: stats.extras = %d, want 0", off.Stats.Extras)
	}
	extraFiles := []string{
		"Fixture Show (2020)/Season 01/EXTRAS/Fixture Show - S01E01 - Commentary.mkv",
		"Fixture Show (2020)/Season 01/EXTRAS/Fixture Show - Gag Reel.mkv",
		"Fixture Show (2020)/Season 02/Season 2 - DVD Extras/Fixture Show - Deleted Scenes.mkv",
		"Fixture Show (2020)/Season 02/EXTRAS/Fixture Show - Gag Reel.mkv",
		"Fixture Show (2020)/DVD Extras/Fixture Show - Behind the Scenes.mkv",
		"Fixture Show (2020)/Sample/Fixture Show - Trailer-sample.mkv",
		"Fixture Show (2020)/DVD Extras/Fixture Show - Gag Reel-sample.mkv",
		"Solo Film (2021)/Solo Film (2021)-trailer.mkv",
		"Solo Film (2021)/[Extra] Solo Film Deleted Scene.mkv",
		"Solo Film (2021)/Sample/Solo Film (2021)-featurette.mkv",
		"Mystery Show/DVD Extras/Mystery Show - Trailer.mkv",
	}
	sampleFiles := map[string]bool{
		"Fixture Show (2020)/Sample/Fixture Show - Trailer-sample.mkv":      true,
		"Fixture Show (2020)/DVD Extras/Fixture Show - Gag Reel-sample.mkv": true,
		"Solo Film (2021)/Sample/Solo Film (2021)-featurette.mkv":           true,
	}
	for _, rel := range extraFiles {
		want := "extra content — left in place"
		if sampleFiles[rel] {
			want = "sample — left in place"
		}
		ni := notIdentifiedFor(off, paths[rel])
		if ni == nil || ni.Reason != want {
			t.Errorf("toggle off: %s not_identified = %+v, want reason %q", rel, ni, want)
		}
	}
	// The non-extras plan is identical: same videos, same
	// counts, once the extras outcomes are removed.
	if len(off.LinksToCreate) != len(on.LinksToCreate)-len(entriesOfKind(on, "extra")) {
		t.Errorf("toggle off links = %d, on links = %d (extras %d)",
			len(off.LinksToCreate), len(on.LinksToCreate), len(entriesOfKind(on, "extra")))
	}
	if off.Stats.Movies != on.Stats.Movies || off.Stats.Episodes != on.Stats.Episodes {
		t.Errorf("toggle off changed the video counts: %+v vs %+v", off.Stats, on.Stats)
	}
	// The Gag Reel collision exists only because extras link.
	for _, c := range off.Collisions {
		if strings.Contains(c.Target, "extras") {
			t.Errorf("toggle off: unexpected extras collision %+v", c)
		}
	}
}

// The apply → already-linked → undo round trip over the
// Service job engine: extras links are ordinary manifest
// entries (kind recorded), created and removed by the same
// rules as every other link, and the source tree is
// byte-identical afterwards (the prime invariant).
func TestLinkExtras140JobRoundTrip(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	w := func(rel string) string {
		p := filepath.Join(src, rel)
		writeOrgFile(t, p, "bytes-"+rel)
		return p
	}
	ep := w("Round Show (2022)/Season 01/Round Show - S01E01.mkv")
	ex := w("Round Show (2022)/Season 01/EXTRAS/Round Show - S01E01 - Commentary.mkv")
	mv := w("Round Film (2022)/Round Film (2022).mkv")
	tr := w("Round Film (2022)/Round Film (2022)-trailer.mkv")
	_ = ep
	_ = mv

	svc := linkSvc(t)
	before := snapshotSourceTree(t, src)
	plan := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan.LinksToCreate) != 4 || len(entriesOfKind(plan, "extra")) != 2 {
		t.Fatalf("plan = %d links (%d extras), want 4 (2 extras)", len(plan.LinksToCreate), len(entriesOfKind(plan, "extra")))
	}
	job, err := svc.StartLinkApplyJob(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := waitLinkJob(t, svc, job.ID)
	if st.State != "done" || st.ApplyResult == nil || st.ApplyResult.Created != 4 {
		t.Fatalf("apply job = %+v, want done with 4 created", st)
	}
	// The manifest records the extras' kind.
	m, err := svc.loadLinkManifest(st.ApplyResult.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	kindBySource := map[string]string{}
	for _, l := range m.Links {
		kindBySource[l.Source] = l.Kind
		if !l.Done {
			t.Errorf("manifest link %s not done", l.Target)
		}
	}
	if kindBySource[ex] != "extra" || kindBySource[tr] != "extra" {
		t.Errorf("manifest kinds = %v, want both extras recorded as \"extra\"", kindBySource)
	}
	// The links are real hardlinks (same identity as sources).
	for _, p := range []string{ex, tr} {
		e := linkEntryFor(plan, p)
		info, err := os.Stat(e.Target)
		if err != nil {
			t.Fatalf("extra target %s missing after apply: %v", e.Target, err)
		}
		sinfo, _ := os.Stat(p)
		d1, i1, _, ok1 := fileIdentityFor(info, e.Target)
		d2, i2, _, ok2 := fileIdentityFor(sinfo, p)
		if !ok1 || !ok2 || d1 != d2 || i1 != i2 {
			t.Errorf("extra target %s is not a hardlink of %s", e.Target, p)
		}
	}
	// A fresh preview recognizes the extras as already linked.
	plan2 := mustSvcLinkPlan(t, svc, src, tgt)
	if len(plan2.LinksToCreate) != 0 || len(plan2.AlreadyLinked) != 4 {
		t.Fatalf("second plan = %d to create / %d already, want 0/4", len(plan2.LinksToCreate), len(plan2.AlreadyLinked))
	}
	if e := alreadyEntryFor(plan2, ex); e == nil || e.Kind != "extra" {
		t.Errorf("commentary extra not recognized as already linked: %+v", plan2.AlreadyLinked)
	}
	// Undo as a job removes the links and restores the source
	// tree exactly.
	ujob, err := svc.StartLinkUndoJob(st.ApplyResult.ManifestID)
	if err != nil {
		t.Fatal(err)
	}
	ust := waitLinkJob(t, svc, ujob.ID)
	if ust.State != "done" || ust.UndoResult == nil || ust.UndoResult.Removed != 4 {
		t.Fatalf("undo job = %+v, want done with 4 removed", ust)
	}
	after := snapshotSourceTree(t, src)
	if len(after) != len(before) {
		t.Fatalf("source tree changed size: %d entries before, %d after undo", len(before), len(after))
	}
	for rel, e := range before {
		g, ok := after[rel]
		if !ok || g != e {
			t.Errorf("source entry %s changed across apply+undo: %+v -> %+v", rel, e, g)
		}
	}
}

// Ruling 1 mechanics: three unnumbered specials of one series
// are numbered in sorted source-path order across the whole
// plan, flagged as assigned, exported as assigned — and the
// toggle restores today's not-identified outcome.
func TestSpecials140Numbering(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	w := func(rel string) string {
		p := filepath.Join(src, rel)
		writeOrgFile(t, p, "bytes-"+rel)
		return p
	}
	real := w("Numbered Show (2018)/Season 01/Numbered Show - S01E01 - Start.mkv")
	making := w("Numbered Show (2018)/Season 01/Numbered Show - The Making Of.mkv")
	holiday := w("Numbered Show (2018)/Specials/Numbered Show - Holiday Special.mkv")
	bonus := w("Numbered Show (2018)/Specials/Numbered Show - S00 - Bonus.mkv")
	sample := w("Numbered Show (2018)/Specials/Sample/Numbered Show - Reunion Special.sample.mkv")

	plan := plan140(t, src, tgt, defaultConfig())
	// Sorted source-path order across the series: the
	// Season 01 file first ("Season" < "Specials"), then the
	// two Specials files by name.
	want := map[string]string{
		making:  "Numbered Show (2018)/Season 00/Numbered Show - S00E01.mkv",
		holiday: "Numbered Show (2018)/Season 00/Numbered Show - S00E02.mkv",
		bonus:   "Numbered Show (2018)/Season 00/Numbered Show - S00E03.mkv",
	}
	for source, wantRel := range want {
		e := linkEntryFor(plan, source)
		if e == nil {
			t.Errorf("special %s: no link entry", source)
			continue
		}
		if e.Target != filepath.Join(tgt, wantRel) {
			t.Errorf("special %s: target = %s, want %s", source, e.Target, filepath.Join(tgt, wantRel))
		}
		if !e.NumberAssigned {
			t.Errorf("special %s: number_assigned not set — an invented number must never be silent", source)
		}
		if e.Kind != "episode" {
			t.Errorf("special %s: kind = %q, want episode", source, e.Kind)
		}
	}
	if e := linkEntryFor(plan, real); e == nil || e.NumberAssigned {
		t.Errorf("the real episode must link unflagged: %+v", e)
	}
	// The sample special is gated out by the sample gate even
	// though it signals special-ness.
	if ni := notIdentifiedFor(plan, sample); ni == nil || ni.Reason != "no episode number found" {
		t.Errorf("sample special not_identified = %+v, want the no-episode-number reason", ni)
	}
	// The export notes the assigned entries too.
	doc := BuildLinkPlanDiagnostics(plan, "test", time.Now())
	if len(doc.NumberAssigned) != 3 {
		t.Errorf("export number_assigned = %d entries, want the 3 specials", len(doc.NumberAssigned))
	}
	notesJoined := strings.Join(doc.Notes, "\n")
	if !strings.Contains(notesJoined, "CREATED") || !strings.Contains(notesJoined, "can shift") {
		t.Errorf("export notes do not carry the renumbering honesty note: %v", doc.Notes)
	}

	// Toggle OFF: all three specials are not-identified,
	// exactly as before v1.4.0.
	cfg := defaultConfig()
	cfg.AssignSpecialNumbers = false
	off := plan140(t, src, tgt, cfg)
	for _, source := range []string{making, holiday, bonus} {
		if ni := notIdentifiedFor(off, source); ni == nil || ni.Reason != "no episode number found" {
			t.Errorf("toggle off: %s not_identified = %+v", source, ni)
		}
		if e := linkEntryFor(off, source); e != nil {
			t.Errorf("toggle off: %s still links to %s", source, e.Target)
		}
	}
}

// Ruling 2 mechanics: a year-less movie in its own folder
// links Title/Title.ext flagged no_year; a series-tree stray
// beside it stays unidentified; a folder-year movie is the
// control (links as before, unflagged); the toggle restores
// the movie-requires-year law.
func TestYearless140(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	w := func(rel string) string {
		p := filepath.Join(src, rel)
		writeOrgFile(t, p, "bytes-"+rel)
		return p
	}
	lone := w("Lone Film/Lone Film.mkv")
	ep := w("Some Show (2020)/Season 01/Some Show - S01E01.mkv")
	stray := w("Some Show (2020)/Season 01/Ending.mkv")
	dated := w("Dated Film (2019)/Dated Film.mkv")
	_ = ep

	plan := plan140(t, src, tgt, defaultConfig())
	e := linkEntryFor(plan, lone)
	if e == nil {
		t.Fatalf("the year-less standalone movie did not link (plan: %+v)", plan.NotIdentified)
	}
	if want := filepath.Join(tgt, "Lone Film", "Lone Film.mkv"); e.Target != want {
		t.Errorf("year-less target = %s, want %s", e.Target, want)
	}
	if !e.NoYear {
		t.Errorf("year-less entry is not flagged no_year — a year-less acceptance must never be silent")
	}
	if e.Kind != "movie" {
		t.Errorf("year-less kind = %q, want movie", e.Kind)
	}
	doc := BuildLinkPlanDiagnostics(plan, "test", time.Now())
	if len(doc.YearlessMovies) != 1 || doc.YearlessMovies[0].Source != lone {
		t.Errorf("export yearless_movies = %+v, want the lone film", doc.YearlessMovies)
	}
	// The series-tree stray stays unidentified.
	if ni := notIdentifiedFor(plan, stray); ni == nil {
		t.Errorf("the Ending stray was identified — series-tree strays must stay out (entries: %+v)", plan.LinksToCreate)
	}
	if le := linkEntryFor(plan, stray); le != nil {
		t.Errorf("the Ending stray links to %s — it must not", le.Target)
	}
	// Control: the folder-year movie links with its year, unflagged.
	d := linkEntryFor(plan, dated)
	if d == nil || d.NoYear {
		t.Errorf("folder-year control = %+v, want a normal unflagged movie link", d)
	} else if want := filepath.Join(tgt, "Dated Film (2019)", "Dated Film (2019).mkv"); d.Target != want {
		t.Errorf("folder-year target = %s, want %s", d.Target, want)
	}

	// Toggle OFF restores the law exactly.
	cfg := defaultConfig()
	cfg.AllowYearlessMovies = false
	off := plan140(t, src, tgt, cfg)
	if ni := notIdentifiedFor(off, lone); ni == nil || ni.Reason != "no year found" {
		t.Errorf("toggle off: lone film not_identified = %+v, want the no-year reason", ni)
	}
	if linkEntryFor(off, dated) == nil {
		t.Errorf("toggle off: the folder-year movie must still link")
	}
}
