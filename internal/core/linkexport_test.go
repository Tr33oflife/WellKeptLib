package core

// linkexport_test.go — tests for the v1.3.0 link-plan
// diagnostics export: the schema pin, completeness (every
// problem, no caps), the unresolved-collision shape, the
// target-conflict occupant facts, the unlinkable device
// facts, the excluded-sections rule, and the Service surface
// (unknown plan, non-consuming).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func mustDiagnostics(t *testing.T, plan *LinkPlan) (*LinkDiagnostics, []byte) {
	t.Helper()
	doc := BuildLinkPlanDiagnostics(plan, Version, time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	return doc, b
}

func rawDoc(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("export does not parse as JSON: %v", err)
	}
	return m
}

// The schema pin: exact top-level key set, identity values,
// and struct key order (schema first, sections in order).
func TestLinkDiagnosticsExportShape(t *testing.T) {
	requireIdentity(t)
	fx := buildLinkFixture(t)
	plan := mustLinkPlan(t, fx.src, fx.tgt)
	doc, b := mustDiagnostics(t, plan)
	m := rawDoc(t, b)

	wantKeys := []string{"schema", "schema_version", "app_version", "exported_at", "plan_id",
		"source_root", "destination_root", "counts", "conflicts", "not_found", "unlinkable", "notes"}
	var gotKeys []string
	for k := range m {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	sort.Strings(wantKeys)
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("top-level keys = %v, want exactly %v", gotKeys, wantKeys)
	}
	if m["schema"] != "wellkeptlib.linkplan.diagnostics" || m["schema_version"] != float64(1) {
		t.Errorf("schema = %v v%v", m["schema"], m["schema_version"])
	}
	if m["app_version"] != Version || m["plan_id"] != plan.ID {
		t.Errorf("app_version/plan_id = %v/%v", m["app_version"], m["plan_id"])
	}
	if m["source_root"] != plan.SourceRoot || m["destination_root"] != plan.TargetRoot {
		t.Errorf("roots = %v -> %v", m["source_root"], m["destination_root"])
	}
	if _, err := time.Parse(time.RFC3339, m["exported_at"].(string)); err != nil {
		t.Errorf("exported_at %q is not RFC 3339: %v", m["exported_at"], err)
	}
	// Key order follows the struct: schema < counts < conflicts.
	s := string(b)
	iSchema, iCounts, iConflicts := strings.Index(s, `"schema"`), strings.Index(s, `"counts"`), strings.Index(s, `"conflicts"`)
	if !(0 <= iSchema && iSchema < iCounts && iCounts < iConflicts) {
		t.Errorf("key order off: schema@%d counts@%d conflicts@%d", iSchema, iCounts, iConflicts)
	}
	// Counts are the plan's own stats, verbatim.
	if doc.Counts != plan.Stats {
		t.Errorf("counts = %+v, want the plan stats %+v", doc.Counts, plan.Stats)
	}
	if doc.Counts.LinksToCreate != 8 || doc.Counts.Skipped != 1 ||
		doc.Counts.CollisionGroups != 1 || doc.Counts.ConflictTargets != 1 || doc.Counts.NotIdentified != 1 {
		t.Errorf("counts = %+v", doc.Counts)
	}
}

// The §17.1 fixture's collision, fully evidenced: keeper with
// its parsed + probe facts, the alternate with its reason.
func TestLinkDiagnosticsCollisionEvidence(t *testing.T) {
	requireIdentity(t)
	fx := buildLinkFixture(t)
	plan := mustLinkPlan(t, fx.src, fx.tgt)
	doc, _ := mustDiagnostics(t, plan)
	var col *LinkProblemConflict
	for i := range doc.Conflicts {
		if doc.Conflicts[i].Kind == "collision" {
			col = &doc.Conflicts[i]
		}
	}
	if col == nil {
		t.Fatalf("no collision entry in %+v", doc.Conflicts)
	}
	if col.Status != "resolved" || col.Keeper == nil || *col.Keeper != fx.paths["duplicates/Film (2020) 1080p.mkv"] {
		t.Fatalf("collision keeper = %+v", col)
	}
	if col.KeeperReason == "" {
		t.Errorf("resolved collision has no keeper reason")
	}
	if len(col.Sources) != 2 {
		t.Fatalf("collision sources = %d, want keeper + 1 alternate", len(col.Sources))
	}
	k := col.Sources[0]
	if k.Role != "keeper" || k.Source != *col.Keeper {
		t.Errorf("first source = %+v, want the keeper first", k)
	}
	if k.Parsed == nil || k.Parsed.Title != "Film" || k.Parsed.Year != 2020 {
		t.Errorf("keeper parsed = %+v, want Film (2020)", k.Parsed)
	}
	if k.Probe == nil || k.Probe.Size != int64(len("film-1080-longer-content")) || k.Probe.Resolution != "1080p" {
		t.Errorf("keeper probe = %+v, want size 25 + 1080p label", k.Probe)
	}
	a := col.Sources[1]
	if a.Role != "alternate" || a.Source != fx.paths["duplicates/Film (2020) 720p.mkv"] || a.Reason == "" {
		t.Errorf("alternate = %+v", a)
	}
	if a.Parsed == nil || a.Probe == nil || a.Probe.Resolution != "720p" {
		t.Errorf("alternate facts = parsed %+v probe %+v", a.Parsed, a.Probe)
	}
}

// The target-conflict occupant facts: a real file, its size,
// and its identity as the planner read them.
func TestLinkDiagnosticsTargetConflictOccupant(t *testing.T) {
	requireIdentity(t)
	fx := buildLinkFixture(t)
	plan := mustLinkPlan(t, fx.src, fx.tgt)
	doc, _ := mustDiagnostics(t, plan)
	var tc *LinkProblemConflict
	for i := range doc.Conflicts {
		if doc.Conflicts[i].Kind == "target_conflict" {
			tc = &doc.Conflicts[i]
		}
	}
	if tc == nil {
		t.Fatalf("no target_conflict entry in %+v", doc.Conflicts)
	}
	if len(tc.BlockedSources) != 1 || tc.BlockedSources[0] != fx.paths["downloads/Control Film (2023).mkv"] {
		t.Errorf("blocked sources = %v", tc.BlockedSources)
	}
	if tc.Reason == "" {
		t.Errorf("target conflict has no reason")
	}
	occ := tc.Occupant
	if occ == nil || occ.Kind != "regular file" {
		t.Fatalf("occupant = %+v, want a regular file", occ)
	}
	if occ.Size == nil || *occ.Size != int64(len("someone-elses-file")) {
		t.Errorf("occupant size = %v, want %d", occ.Size, len("someone-elses-file"))
	}
	info, err := os.Stat(fx.paths["occupied-target"])
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, _, ok := fileIdentityFor(info, fx.paths["occupied-target"])
	if !ok || !occ.IdentityKnown || occ.Dev == nil || *occ.Dev != dev || occ.Ino == nil || *occ.Ino != ino {
		t.Errorf("occupant identity = %+v, want dev %d ino %d", occ, dev, ino)
	}
}

// Not-found entries carry the planner's exact reason plus
// everything the parser extracted (here: almost nothing —
// that absence is the signal), the extension and the size.
func TestLinkDiagnosticsNotFound(t *testing.T) {
	requireIdentity(t)
	fx := buildLinkFixture(t)
	plan := mustLinkPlan(t, fx.src, fx.tgt)
	doc, _ := mustDiagnostics(t, plan)
	if len(doc.NotFound) != 1 {
		t.Fatalf("not_found = %+v, want the one mystery file", doc.NotFound)
	}
	nf := doc.NotFound[0]
	if nf.Path != fx.paths["downloads/random-file.mkv"] {
		t.Errorf("not_found path = %s", nf.Path)
	}
	if nf.Reason != plan.NotIdentified[0].Reason || nf.Reason == "" {
		t.Errorf("not_found reason = %q, want the planner's %q", nf.Reason, plan.NotIdentified[0].Reason)
	}
	if nf.Ext != ".mkv" || nf.Size != int64(len("mystery")) {
		t.Errorf("not_found ext/size = %q/%d", nf.Ext, nf.Size)
	}
	if nf.Parsed == nil {
		t.Fatalf("not_found has no parsed block")
	}
	if nf.Parsed.Year != 0 || len(nf.Parsed.Episodes) != 0 {
		t.Errorf("parsed = %+v, want the empty extraction for a mystery name", nf.Parsed)
	}
}

// A duration-demoted group exports as UNRESOLVED: keeper is
// present in the JSON and null, the reason carries the
// duration note, and both sources show the durations that
// demoted them.
func TestLinkDiagnosticsUnresolvedCollision(t *testing.T) {
	requireIdentity(t)
	src, tgt, paths := collisionTree(t, "Film.2020.1080p.a.mkv", "Film.2020.1080p.b.mkv")
	res := fakeResolver(map[string]*Item{
		paths["Film.2020.1080p.a.mkv"]: {Height: 1080, Bitrate: 10_000_000, Duration: 3600},
		paths["Film.2020.1080p.b.mkv"]: {Height: 1080, Bitrate: 10_000_000, Duration: 5400},
	})
	plan, err := planLinkLibrary(src, tgt, defaultConfig(), res)
	if err != nil {
		t.Fatal(err)
	}
	doc, b := mustDiagnostics(t, plan)
	if len(doc.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", doc.Conflicts)
	}
	col := doc.Conflicts[0]
	if col.Status != "unresolved" || col.Keeper != nil {
		t.Errorf("collision = status %q keeper %v, want unresolved + nil keeper", col.Status, col.Keeper)
	}
	if !strings.Contains(col.Reason, "may not be the same cut") || col.DurationStatus != "demoted" {
		t.Errorf("unresolved reason/status = %q/%q", col.Reason, col.DurationStatus)
	}
	// In the raw JSON the keeper key is PRESENT and null.
	m := rawDoc(t, b)
	rawCol := m["conflicts"].([]any)[0].(map[string]any)
	if v, present := rawCol["keeper"]; !present || v != nil {
		t.Errorf("raw keeper = %v (present %v), want an explicit null", v, present)
	}
	durs := map[float64]bool{}
	for _, s := range col.Sources {
		if s.Role != "alternate" || s.Probe == nil {
			t.Fatalf("unresolved source = %+v", s)
		}
		durs[s.Probe.Duration] = true
	}
	if !durs[3600] || !durs[5400] {
		t.Errorf("source durations = %v, want both 3600 and 5400 (the demoting evidence)", durs)
	}
}

// Unlinkable entries carry the would-be target and the device
// facts the feasibility proof used (fabricated cross-device
// at the identity seam, as in the planner tests).
func TestLinkDiagnosticsUnlinkableFacts(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	video := filepath.Join(src, "Film (2020).mkv")
	writeOrgFile(t, video, "film")
	if err := os.MkdirAll(tgt, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := fileIdentityForFn
	fileIdentityForFn = func(info os.FileInfo, path string) (uint64, uint64, uint64, bool) {
		dev, ino, n, ok := orig(info, path)
		if ok && strings.HasPrefix(path, tgt) {
			return dev + 999, ino, n, ok
		}
		return dev, ino, n, ok
	}
	defer func() { fileIdentityForFn = orig }()
	plan := mustLinkPlan(t, src, tgt)
	doc, _ := mustDiagnostics(t, plan)
	if len(doc.Unlinkable) != 1 {
		t.Fatalf("unlinkable = %+v", doc.Unlinkable)
	}
	u := doc.Unlinkable[0]
	if u.Path != video || !strings.HasSuffix(u.Target, filepath.Join("Film (2020)", "Film (2020).mkv")) {
		t.Errorf("unlinkable path/target = %s -> %s", u.Path, u.Target)
	}
	if !strings.Contains(u.Reason, "cannot cross filesystems") {
		t.Errorf("unlinkable reason = %q", u.Reason)
	}
	if !u.IdentityKnown || u.SourceDev == nil || u.SourceIno == nil {
		t.Errorf("unlinkable source identity = %+v", u)
	}
	if !u.DestinationKnown || u.DestinationDev == nil || *u.DestinationDev == *u.SourceDev {
		t.Errorf("unlinkable destination = %+v, want a known device differing from the source's", u)
	}
	if u.Parsed == nil || u.Parsed.Title != "Film" || u.Parsed.Year != 2020 {
		t.Errorf("unlinkable parsed = %+v (identified, just unlinkable)", u.Parsed)
	}
}

// Completeness: problem lists far beyond any UI page size are
// exported whole — 300 not-identified files and 40 collision
// groups, every one present, in the struct AND the JSON.
func TestLinkDiagnosticsCompletenessNoCap(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	letters := "abcdefghijklmnopqrstuvwxyz"
	suffix := func(i int) string {
		return string([]byte{letters[(i/26)%26], letters[i%26], letters[(i/676)%26]})
	}
	var mystery []string
	for i := 0; i < 300; i++ {
		p := filepath.Join(src, "mysteries", "mystery-"+suffix(i)+".mkv")
		writeOrgFile(t, p, "mystery")
		mystery = append(mystery, p)
	}
	for i := 0; i < 40; i++ {
		// A distinct title per pair: identical titles would all
		// land on ONE target and form a single giant group.
		dir := filepath.Join(src, "pair-"+suffix(i))
		writeOrgFile(t, filepath.Join(dir, "Dup Film "+suffix(i)+" (2022) 720p.mkv"), "short")
		writeOrgFile(t, filepath.Join(dir, "Dup Film "+suffix(i)+" (2022) 1080p.mkv"), "longer-content")
	}
	plan := mustLinkPlan(t, src, tgt)
	if plan.Stats.NotIdentified != 300 || plan.Stats.CollisionGroups != 40 {
		t.Fatalf("fixture plan = %d not-identified / %d collisions, want 300/40", plan.Stats.NotIdentified, plan.Stats.CollisionGroups)
	}
	doc, b := mustDiagnostics(t, plan)
	if len(doc.NotFound) != 300 {
		t.Errorf("exported not_found = %d, want all 300", len(doc.NotFound))
	}
	found := map[string]bool{}
	for _, nf := range doc.NotFound {
		found[nf.Path] = true
	}
	for _, p := range mystery {
		if !found[p] {
			t.Errorf("export is missing not-found file %s", p)
		}
	}
	collisions := 0
	for _, c := range doc.Conflicts {
		if c.Kind == "collision" {
			collisions++
			if c.Keeper == nil || len(c.Sources) != 2 {
				t.Errorf("collision %s incomplete: %+v", c.Target, c)
			}
		}
	}
	if collisions != 40 {
		t.Errorf("exported collisions = %d, want all 40", collisions)
	}
	if n := strings.Count(string(b), `"role": "alternate"`); n != 40 {
		t.Errorf("raw JSON carries %d alternates, want 40", n)
	}
	var round LinkDiagnostics
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("export does not round-trip: %v", err)
	}
	if round.Counts.NotIdentified != 300 || len(round.NotFound) != 300 || len(round.Conflicts) != 40 {
		t.Errorf("round trip = %d/%d/%d", round.Counts.NotIdentified, len(round.NotFound), len(round.Conflicts))
	}
}

// The excluded sections stay excluded: healthy and skipped
// files appear as counts only — their paths are nowhere in
// the document — and the notes field says so in the document
// itself.
func TestLinkDiagnosticsExcludedSections(t *testing.T) {
	requireIdentity(t)
	fx := buildLinkFixture(t)
	plan := mustLinkPlan(t, fx.src, fx.tgt)
	doc, b := mustDiagnostics(t, plan)
	s := string(b)
	for _, absent := range []string{"The.Matrix.1999", "Single Film", "poster.jpg", "orphan.en.srt", "My Show"} {
		if strings.Contains(s, absent) {
			t.Errorf("export contains %q — healthy/skipped files must be counts only", absent)
		}
	}
	if doc.Counts.LinksToCreate != 8 || doc.Counts.Skipped != 1 {
		t.Errorf("counts = %+v, want the excluded sections still counted", doc.Counts)
	}
	notes := strings.Join(doc.Notes, " ")
	if !strings.Contains(notes, "counts only") {
		t.Errorf("notes do not disclose the counts-only choice: %q", notes)
	}
}

// The Service surface: unknown plan errors, the filename
// carries the plan timestamp, and exporting does NOT consume
// the plan (Apply still owns that).
func TestServiceLinkPlanDiagnostics(t *testing.T) {
	requireIdentity(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, _, err := svc.LinkPlanDiagnostics("plan-nope"); err == nil ||
		!strings.Contains(err.Error(), "unknown link plan") {
		t.Errorf("unknown plan error = %v", err)
	}
	fx := buildLinkFixture(t)
	plan, err := svc.LinkPlan(fx.src, fx.tgt)
	if err != nil {
		t.Fatalf("LinkPlan: %v", err)
	}
	doc, filename, err := svc.LinkPlanDiagnostics(plan.ID)
	if err != nil {
		t.Fatalf("LinkPlanDiagnostics: %v", err)
	}
	if doc.PlanID != plan.ID || doc.AppVersion != Version || doc.Schema != LinkDiagnosticsSchema {
		t.Errorf("doc identity = %q/%q/%q", doc.PlanID, doc.AppVersion, doc.Schema)
	}
	if !strings.HasPrefix(filename, "wellkeptlib-link-problems-") || !strings.HasSuffix(filename, ".json") ||
		!strings.Contains(filename, strings.TrimPrefix(plan.ID, "plan-")) {
		t.Errorf("filename = %q", filename)
	}
	if _, _, err := svc.LinkPlanDiagnostics(plan.ID); err != nil {
		t.Errorf("second export failed — the export must not consume the plan: %v", err)
	}
}
