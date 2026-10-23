package core

// linkplan_test.go — tests for the v1.1.0 link-mode planner
// (LINK-LIBRARY-SPEC §§17.2–17.4). The planner is read-only, so
// every case builds a small tree in temp dirs and asserts the
// plan; identity-dependent cases skip only when the host test
// filesystem genuinely cannot report identity.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkFixture is the §17.1-shaped tree: a movie with sidecars,
// an episode, an unidentified file, an orphan subtitle, a
// single-film artwork folder, a 720p/1080p duplicate pair, and
// a target pre-occupied by a real file. It returns the source
// root, target root, and the paths assertions need.
type linkFixture struct {
	src, tgt string
	paths    map[string]string
}

func buildLinkFixture(t *testing.T) linkFixture {
	t.Helper()
	base := t.TempDir()
	fx := linkFixture{src: filepath.Join(base, "source"), tgt: filepath.Join(base, "target"), paths: map[string]string{}}
	w := func(rel, content string) string {
		p := filepath.Join(fx.src, rel)
		writeOrgFile(t, p, content)
		fx.paths[rel] = p
		return p
	}
	w("downloads/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", "matrix-video")
	w("downloads/The.Matrix.1999.1080p.BluRay.x264-GROUP.en.srt", "matrix-subs")
	w("downloads/The.Matrix.1999.1080p.BluRay.x264-GROUP.nfo", "matrix-nfo")
	w("show/My Show S01E02.mkv", "episode-video")
	w("show/My Show S01E02.en.srt", "episode-subs")
	w("downloads/random-file.mkv", "mystery")
	w("downloads/orphan.en.srt", "orphan-subs")
	w("artwork-case/Single Film (2021).mkv", "single-film")
	w("artwork-case/poster.jpg", "poster-bytes")
	w("duplicates/Film (2020) 720p.mkv", "film-720")
	w("duplicates/Film (2020) 1080p.mkv", "film-1080-longer-content")
	w("downloads/Control Film (2023).mkv", "control-film")
	if err := os.MkdirAll(filepath.Join(fx.src, "empty-folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real, different file already sits at the Control Film target.
	occupied := filepath.Join(fx.tgt, "Control Film (2023)", "Control Film (2023).mkv")
	writeOrgFile(t, occupied, "someone-elses-file")
	fx.paths["occupied-target"] = occupied
	return fx
}

func mustLinkPlan(t *testing.T, src, tgt string) *LinkPlan {
	t.Helper()
	plan, err := PlanLinkLibrary(src, tgt)
	if err != nil {
		t.Fatalf("PlanLinkLibrary(%s, %s): %v", src, tgt, err)
	}
	return plan
}

func linkEntryFor(plan *LinkPlan, source string) *LinkEntry {
	for i := range plan.LinksToCreate {
		if plan.LinksToCreate[i].Source == source {
			return &plan.LinksToCreate[i]
		}
	}
	return nil
}

func requireIdentity(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "probe.mkv")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := fileIdentityFor(info, p); !ok {
		t.Skip("host test filesystem cannot report storage identity")
	}
}

// §17.2.1 + §7.2: the full category picture on one fixture.
func TestLinkPlanCategories(t *testing.T) {
	requireIdentity(t)
	fx := buildLinkFixture(t)
	plan := mustLinkPlan(t, fx.src, fx.tgt)
	st := plan.Stats
	// links: matrix mkv+srt+nfo (3), episode mkv+srt (2),
	// single film + poster (2), dup keeper (1) = 8.
	if st.LinksToCreate != 8 {
		t.Errorf("links_to_create = %d, want 8 (stats %+v)", st.LinksToCreate, st)
	}
	if st.Movies != 3 || st.Episodes != 1 || st.Sidecars != 3 || st.Artwork != 1 {
		t.Errorf("kind counts = %d/%d/%d/%d, want 3 movies / 1 episode / 3 sidecars / 1 artwork",
			st.Movies, st.Episodes, st.Sidecars, st.Artwork)
	}
	if st.AlreadyLinked != 0 || st.Unlinkable != 0 {
		t.Errorf("already_linked/unlinkable = %d/%d, want 0/0", st.AlreadyLinked, st.Unlinkable)
	}
	if st.CollisionGroups != 1 || st.Alternates != 1 {
		t.Errorf("collisions = %d groups / %d alternates, want 1/1", st.CollisionGroups, st.Alternates)
	}
	if st.NotIdentified != 1 {
		t.Errorf("not_identified = %d, want 1 (random-file.mkv)", st.NotIdentified)
	}
	if st.ConflictTargets != 1 || st.ConflictFiles != 1 {
		t.Errorf("conflicts = %d targets / %d files, want 1/1", st.ConflictTargets, st.ConflictFiles)
	}
	if st.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (orphan.en.srt)", st.Skipped)
	}
	// Every planned target is confined to the target root, and
	// no target is the occupied Control Film path.
	for _, e := range plan.LinksToCreate {
		if !pathWithinRoot(plan.TargetRoot, e.Target) {
			t.Errorf("target %s escapes the target root", e.Target)
		}
		if e.Target == fx.paths["occupied-target"] {
			t.Errorf("the occupied target was planned as a link")
		}
	}
	// The keeper is the 1080p copy (filename ranking: no
	// resolver at this level), the 720p is the alternate.
	col := plan.Collisions[0]
	if col.KeeperSource != fx.paths["duplicates/Film (2020) 1080p.mkv"] {
		t.Errorf("keeper = %s, want the 1080p copy", col.KeeperSource)
	}
	if len(col.Alternates) != 1 || col.Alternates[0].Source != fx.paths["duplicates/Film (2020) 720p.mkv"] {
		t.Errorf("alternates = %+v, want the 720p copy", col.Alternates)
	}
	// The conflict names the blocked source.
	tc := plan.TargetConflicts[0]
	if len(tc.Sources) != 1 || tc.Sources[0] != fx.paths["downloads/Control Film (2023).mkv"] || tc.Occupant != "regular file" {
		t.Errorf("conflict = %+v", tc)
	}
	// Sidecar suffixes preserved exactly.
	sub := linkEntryFor(plan, fx.paths["downloads/The.Matrix.1999.1080p.BluRay.x264-GROUP.en.srt"])
	if sub == nil || !strings.HasSuffix(sub.Target, "The Matrix (1999).en.srt") {
		t.Errorf("matrix sidecar entry = %+v", sub)
	}
}

// §17.2.2: planner reuse — link-mode relative targets equal
// move mode's relative targets for the same identified tree.
func TestLinkPlanLayoutMatchesMoveMode(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	writeOrgFile(t, filepath.Join(src, "dl", "The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv"), "v")
	writeOrgFile(t, filepath.Join(src, "dl", "The.Matrix.1999.1080p.BluRay.x264-GROUP.en.srt"), "s")
	writeOrgFile(t, filepath.Join(src, "show", "My Show S01E02.mkv"), "e")
	writeOrgFile(t, filepath.Join(src, "one", "Single Film (2021).mkv"), "f")
	writeOrgFile(t, filepath.Join(src, "one", "poster.jpg"), "p")

	move := mustPlan(t, src)
	links := mustLinkPlan(t, src, tgt)
	moveRel := map[string]bool{}
	for _, mv := range move.Moves {
		rel, err := filepath.Rel(move.Root, mv.To)
		if err != nil {
			t.Fatal(err)
		}
		moveRel[rel] = true
	}
	if len(links.LinksToCreate) != len(moveRel) {
		t.Fatalf("link entries = %d, move moves = %d", len(links.LinksToCreate), len(moveRel))
	}
	for _, e := range links.LinksToCreate {
		rel, err := filepath.Rel(links.TargetRoot, e.Target)
		if err != nil {
			t.Fatal(err)
		}
		if !moveRel[rel] {
			t.Errorf("link target %s has no matching move-mode layout entry", rel)
		}
	}
}

// §17.2.3: extras are not-identified with the planner's reason.
func TestLinkPlanExtraNotIdentified(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	writeOrgFile(t, filepath.Join(src, "Movie (2020)-trailer.mkv"), "trailer")
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.NotIdentified) != 1 || !strings.Contains(plan.NotIdentified[0].Reason, "extra content") {
		t.Errorf("not_identified = %+v, want the extra-content reason", plan.NotIdentified)
	}
}

// §17.2.4: already-linked by inode.
func TestLinkPlanAlreadyLinked(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	video := filepath.Join(src, "Film (2020).mkv")
	writeOrgFile(t, video, "film-bytes")
	target := filepath.Join(tgt, "Film (2020)", "Film (2020).mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(video, target); err != nil {
		t.Fatal(err)
	}
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.AlreadyLinked) != 1 || plan.AlreadyLinked[0].Source != video {
		t.Fatalf("already_linked = %+v, want the pre-linked film", plan.AlreadyLinked)
	}
	if len(plan.LinksToCreate) != 0 {
		t.Errorf("links_to_create = %+v, want none", plan.LinksToCreate)
	}
}

// §17.2.5/6: target conflicts — directory and symlink occupants.
func TestLinkPlanTargetConflictOccupants(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	writeOrgFile(t, filepath.Join(src, "Dir Film (2020).mkv"), "a")
	writeOrgFile(t, filepath.Join(src, "Link Film (2021).mkv"), "b")
	if err := os.MkdirAll(filepath.Join(tgt, "Dir Film (2020)", "Dir Film (2020).mkv"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(tgt, "Link Film (2021)", "Link Film (2021).mkv")
	if err := os.MkdirAll(filepath.Dir(linkTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(src, "Link Film (2021).mkv"), linkTarget); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.TargetConflicts) != 2 {
		t.Fatalf("conflicts = %+v, want 2", plan.TargetConflicts)
	}
	byOccupant := map[string]bool{}
	for _, tc := range plan.TargetConflicts {
		byOccupant[tc.Occupant] = true
	}
	if !byOccupant["directory"] || !byOccupant["symbolic link"] {
		t.Errorf("occupants = %v, want directory + symbolic link", byOccupant)
	}
	if len(plan.LinksToCreate) != 0 {
		t.Errorf("links planned against occupied targets: %+v", plan.LinksToCreate)
	}
}

// §17.2.7: unknown identity via the seam → unlinkable.
func TestLinkPlanUnknownIdentity(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	writeOrgFile(t, filepath.Join(src, "Film (2020).mkv"), "film")
	orig := fileIdentityForFn
	fileIdentityForFn = func(info os.FileInfo, path string) (uint64, uint64, uint64, bool) {
		return 0, 0, 0, false
	}
	defer func() { fileIdentityForFn = orig }()
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.Unlinkable) != 1 || !strings.Contains(plan.Unlinkable[0].Reason, "device/inode is unavailable") {
		t.Errorf("unlinkable = %+v, want the identity reason", plan.Unlinkable)
	}
	if len(plan.LinksToCreate) != 0 {
		t.Errorf("an identity-unknown file was planned as a link")
	}
}

// §17.2.8: cross-device preview classification, fabricated at
// the identity seam (this proves the CLASSIFICATION only — no
// real second filesystem is involved).
func TestLinkPlanCrossDeviceClassification(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	video := filepath.Join(src, "Film (2020).mkv")
	writeOrgFile(t, video, "film")
	// The target root exists, so the destination-device lookup
	// consults it (the fabrication below keys on its prefix).
	if err := os.MkdirAll(tgt, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := fileIdentityForFn
	fileIdentityForFn = func(info os.FileInfo, path string) (uint64, uint64, uint64, bool) {
		dev, ino, n, ok := orig(info, path)
		if ok && strings.HasPrefix(path, tgt) {
			return dev + 999, ino, n, ok // the target tree "lives" on another device
		}
		return dev, ino, n, ok
	}
	defer func() { fileIdentityForFn = orig }()
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.Unlinkable) != 1 || !strings.Contains(plan.Unlinkable[0].Reason, "cannot cross filesystems") {
		t.Errorf("unlinkable = %+v, want the cross-device reason", plan.Unlinkable)
	}
}

// The pure feasibility classifier behind §17.2.8: equal
// devices are feasible, differing devices are not.
func TestLinkFeasibilityClassifier(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	video := filepath.Join(src, "Film (2020).mkv")
	writeOrgFile(t, video, "film")
	plan, err := planLinkLibrary(src, tgt, defaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Stats.LinksToCreate != 1 {
		t.Fatalf("same-device baseline: links = %d, want 1", plan.Stats.LinksToCreate)
	}
	b := &linkPlanBuilder{plan: plan, destCache: map[string]destDeviceInfo{}}
	vp := &videoPlan{File: orgFile{Path: video, Dev: 5, Ino: 7, IdentityKnown: true}}
	target := filepath.Join(tgt, "Film (2020)", "Film (2020).mkv")
	// Fabricate the destination verdict by priming the cache.
	b.destCache[filepath.Dir(target)] = destDeviceInfo{dev: 5, known: true}
	if reason, _ := b.feasibilityReason(vp, target); reason != "" {
		t.Errorf("equal devices: reason = %q, want feasible", reason)
	}
	b.destCache[filepath.Dir(target)] = destDeviceInfo{dev: 6, known: true}
	if reason, _ := b.feasibilityReason(vp, target); !strings.Contains(reason, "cannot cross filesystems") {
		t.Errorf("differing devices: reason = %q, want cross-device", reason)
	}
}

// §17.2.9: disjoint-root validation.
func TestLinkPlanDisjointRoots(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "source")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, target string }{
		{"equal roots", src},
		{"target inside source", filepath.Join(src, "inner")},
		{"source inside target", base},
	}
	for _, c := range cases {
		if _, err := PlanLinkLibrary(src, c.target); err == nil {
			t.Errorf("%s: expected a hard error, got a plan", c.name)
		}
	}
	sibling := filepath.Join(base, "sibling-target")
	if _, err := PlanLinkLibrary(src, sibling); err != nil {
		t.Errorf("sibling roots: unexpected error %v", err)
	}
	// A target more than one missing level deep is rejected.
	deep := filepath.Join(base, "missing", "deeper", "target")
	if _, err := PlanLinkLibrary(src, deep); err == nil {
		t.Errorf("deep missing target: expected an error naming the missing parent")
	}
}

// §17.2.10: the scan-overlap helper.
func TestLinkScanOverlapWarnings(t *testing.T) {
	inside := linkScanOverlapWarnings([]string{"/media"}, "/media/links")
	if len(inside) != 1 || inside[0].Code != "scan-root-overlap" {
		t.Errorf("target inside root: warnings = %+v", inside)
	}
	contains := linkScanOverlapWarnings([]string{"/media/links/sub"}, "/media/links")
	if len(contains) != 1 {
		t.Errorf("root inside target: warnings = %+v", contains)
	}
	outside := linkScanOverlapWarnings([]string{"/media"}, "/elsewhere/links")
	if len(outside) != 0 {
		t.Errorf("sibling target: warnings = %+v, want none", outside)
	}
}

// fakeResolver returns per-path ranking metadata for collision
// tests (no ffprobe involved).
func fakeResolver(items map[string]*Item) linkRankResolver {
	return func(source string, parsed ParsedName, size int64, mtime int64) *Item {
		if it, ok := items[source]; ok {
			cp := *it
			return &cp
		}
		return nil
	}
}

func collisionTree(t *testing.T, names ...string) (string, string, map[string]string) {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	paths := map[string]string{}
	for i, n := range names {
		p := filepath.Join(src, "d"+string(rune('a'+i)), n)
		writeOrgFile(t, p, "content-"+n)
		paths[n] = p
	}
	return src, tgt, paths
}

// §17.3.1: the keeper ranking follows the ACTUAL comparator —
// resolution first (a 2160p low-bitrate copy beats a 1080p
// high-bitrate one), then HDR, then bitrate, then size.
func TestLinkCollisionKeeperRanking(t *testing.T) {
	requireIdentity(t)
	src, tgt, paths := collisionTree(t, "Film.2020.2160p.mkv", "Film.2020.1080p.mkv")
	res := fakeResolver(map[string]*Item{
		paths["Film.2020.2160p.mkv"]: {Height: 2160, Bitrate: 5_000_000, Duration: 3600},
		paths["Film.2020.1080p.mkv"]: {Height: 1080, Bitrate: 40_000_000, Duration: 3600},
	})
	plan, err := planLinkLibrary(src, tgt, defaultConfig(), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v", plan.Collisions)
	}
	col := plan.Collisions[0]
	if col.KeeperSource != paths["Film.2020.2160p.mkv"] {
		t.Errorf("keeper = %s, want the 2160p copy (resolution outranks bitrate)", col.KeeperSource)
	}
	if col.DurationStatus != "confirmed" {
		t.Errorf("duration status = %q, want confirmed", col.DurationStatus)
	}
}

// §17.3.1 (continued): HDR, then bitrate, break ties in order.
func TestLinkCollisionKeeperTieBreaks(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	mk := func(sub string, hdr bool, bitrate int64) (string, *Item) {
		p := filepath.Join(base, sub, "Film.2020.1080p.mkv")
		writeOrgFile(t, p, "x")
		return p, &Item{Height: 1080, HDR: hdr, Bitrate: bitrate, Duration: 3600}
	}
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	pa, ia := mk(filepath.Join("source", "a"), false, 30_000_000)
	pb, ib := mk(filepath.Join("source", "b"), true, 10_000_000)
	pc, ic := mk(filepath.Join("source", "c"), true, 20_000_000)
	_ = src
	res := fakeResolver(map[string]*Item{pa: ia, pb: ib, pc: ic})
	plan, err := planLinkLibrary(filepath.Join(base, "source"), tgt, defaultConfig(), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v", plan.Collisions)
	}
	// HDR first (b, c beat a despite lower bitrates), then
	// bitrate (c beats b).
	if plan.Collisions[0].KeeperSource != pc {
		t.Errorf("keeper = %s, want %s (HDR, then bitrate)", plan.Collisions[0].KeeperSource, pc)
	}
}

// §17.3.2: an exact comparator tie keeps the first cleaned
// path, regardless of creation order.
func TestLinkCollisionExactTiePathOrder(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	// Create the LATER path first.
	pb := filepath.Join(src, "b-folder", "Film.2020.1080p.mkv")
	pa := filepath.Join(src, "a-folder", "Film.2020.1080p.mkv")
	writeOrgFile(t, pb, "identical-size")
	writeOrgFile(t, pa, "identical-size")
	meta := &Item{Height: 1080, Bitrate: 10_000_000, Duration: 3600}
	res := fakeResolver(map[string]*Item{pa: meta, pb: meta})
	plan, err := planLinkLibrary(src, tgt, defaultConfig(), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collisions) != 1 || plan.Collisions[0].KeeperSource != pa {
		t.Errorf("keeper = %+v, want the a-folder path %s", plan.Collisions, pa)
	}
}

// §17.3.3: a duration-demoted group is UNRESOLVED — no keeper,
// no link, and the reason carries the duration note.
func TestLinkCollisionDurationDemotedUnresolved(t *testing.T) {
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
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v", plan.Collisions)
	}
	col := plan.Collisions[0]
	if col.KeeperSource != "" {
		t.Errorf("demoted group got a keeper: %s", col.KeeperSource)
	}
	if col.DurationStatus != "demoted" || !strings.Contains(col.Reason, col.DurationNote) || !strings.Contains(col.Reason, "may not be the same cut") {
		t.Errorf("demoted collision = %+v, want the duration note inside the reason", col)
	}
	if len(plan.LinksToCreate) != 0 {
		t.Errorf("demoted group planned links: %+v", plan.LinksToCreate)
	}
}

// §17.3.4: different editions landing on one Jellyfin name are
// unresolved — no invented suffix.
func TestLinkCollisionDifferentEditions(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	writeOrgFile(t, filepath.Join(src, "a", "Movie (2021).mkv"), "theatrical")
	writeOrgFile(t, filepath.Join(src, "b", "Movie (2021) Extended.mkv"), "extended")
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v, want the edition collision", plan.Collisions)
	}
	col := plan.Collisions[0]
	if col.KeeperSource != "" || !strings.Contains(col.Reason, "editions") {
		t.Errorf("edition collision = %+v", col)
	}
	if len(plan.LinksToCreate) != 0 {
		t.Errorf("edition collision planned links: %+v", plan.LinksToCreate)
	}
}

// §17.3.5: split-file parts are never keeper copies of each
// other.
func TestLinkCollisionSplitFiles(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	writeOrgFile(t, filepath.Join(src, "Movie (2021) CD1.mkv"), "part1")
	writeOrgFile(t, filepath.Join(src, "Movie (2021) CD2.mkv"), "part2")
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v, want the split-file group", plan.Collisions)
	}
	col := plan.Collisions[0]
	if col.KeeperSource != "" || !strings.Contains(col.Reason, "split-file") {
		t.Errorf("split collision = %+v", col)
	}
	if len(plan.LinksToCreate) != 0 {
		t.Errorf("split files planned links: %+v", plan.LinksToCreate)
	}
}

// §17.3.6: two source paths to ONE physical file — only the
// representative is selected; the other is a same-physical
// alternate.
func TestLinkCollisionSamePhysical(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	pa := filepath.Join(src, "a", "Film (2020).mkv")
	pb := filepath.Join(src, "b", "Film (2020).mkv")
	writeOrgFile(t, pa, "one-physical-file")
	if err := os.MkdirAll(filepath.Dir(pb), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(pa, pb); err != nil {
		t.Fatal(err)
	}
	plan := mustLinkPlan(t, src, tgt)
	if len(plan.LinksToCreate) != 1 || plan.LinksToCreate[0].Source != pa {
		t.Fatalf("links = %+v, want only the a-path representative", plan.LinksToCreate)
	}
	if len(plan.Collisions) != 1 || len(plan.Collisions[0].Alternates) != 1 {
		t.Fatalf("collisions = %+v", plan.Collisions)
	}
	alt := plan.Collisions[0].Alternates[0]
	if alt.Source != pb || !alt.SamePhysical {
		t.Errorf("alternate = %+v, want the b-path marked same-physical", alt)
	}
}

// §17.3.7: when the best copy is cross-device (fabricated at
// the identity seam) a feasible lower-ranked copy is selected,
// and the best copy is INELIGIBLE — not an ordinary alternate.
func TestLinkCollisionFeasibleBeatsIneligible(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	best := filepath.Join(src, "a", "Film.2020.2160p.mkv")
	feasible := filepath.Join(src, "b", "Film.2020.1080p.mkv")
	writeOrgFile(t, best, "best")
	writeOrgFile(t, feasible, "feasible")
	orig := fileIdentityForFn
	fileIdentityForFn = func(info os.FileInfo, path string) (uint64, uint64, uint64, bool) {
		dev, ino, n, ok := orig(info, path)
		if ok && path == best {
			return dev + 555, ino, n, ok // this one source "lives" elsewhere
		}
		return dev, ino, n, ok
	}
	defer func() { fileIdentityForFn = orig }()
	res := fakeResolver(map[string]*Item{
		best:     {Height: 2160, Bitrate: 50_000_000, Duration: 3600},
		feasible: {Height: 1080, Bitrate: 10_000_000, Duration: 3600},
	})
	plan, err := planLinkLibrary(src, tgt, defaultConfig(), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Collisions) != 1 {
		t.Fatalf("collisions = %+v", plan.Collisions)
	}
	col := plan.Collisions[0]
	if col.KeeperSource != feasible {
		t.Errorf("keeper = %s, want the feasible copy %s", col.KeeperSource, feasible)
	}
	if len(col.Ineligible) != 1 || col.Ineligible[0].Path != best || !strings.Contains(col.Ineligible[0].Reason, "device") {
		t.Errorf("ineligible = %+v, want the cross-device best copy", col.Ineligible)
	}
	for _, alt := range col.Alternates {
		if alt.Source == best {
			t.Errorf("the ineligible copy was also listed as an ordinary alternate")
		}
	}
}

// §17.3.8: collision sidecars follow the keeper only; the
// alternate's subtitle is attached to the alternate, never
// planned against the keeper's target.
func TestLinkCollisionSidecarsFollowKeeper(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	keeper := filepath.Join(src, "a", "Film.2020.1080p.mkv")
	keeperSub := filepath.Join(src, "a", "Film.2020.1080p.en.srt")
	alt := filepath.Join(src, "b", "Film.2020.720p.mkv")
	altSub := filepath.Join(src, "b", "Film.2020.720p.en.srt")
	writeOrgFile(t, keeper, "keeper-video")
	writeOrgFile(t, keeperSub, "keeper-subs")
	writeOrgFile(t, alt, "alt-video")
	writeOrgFile(t, altSub, "alt-subs")
	plan := mustLinkPlan(t, src, tgt)
	sub := linkEntryFor(plan, keeperSub)
	if sub == nil {
		t.Fatalf("keeper subtitle not planned; links = %+v", plan.LinksToCreate)
	}
	if linkEntryFor(plan, altSub) != nil {
		t.Errorf("the alternate's subtitle was planned as a link")
	}
	if len(plan.Collisions) != 1 || len(plan.Collisions[0].Alternates) != 1 {
		t.Fatalf("collisions = %+v", plan.Collisions)
	}
	attached := plan.Collisions[0].Alternates[0].AttachedFiles
	if len(attached) != 1 || attached[0] != altSub {
		t.Errorf("alternate attached files = %v, want [%s]", attached, altSub)
	}
}

// §17.4: artwork in a multi-work folder is skipped; a sidecar
// whose video is unidentified is attached, not linked; an
// empty source folder produces no plan entry.
func TestLinkPlanSidecarArtworkShapes(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src := filepath.Join(base, "source")
	tgt := filepath.Join(base, "target")
	// Two works in one folder + folder artwork => skipped.
	writeOrgFile(t, filepath.Join(src, "mixed", "Film A (2020).mkv"), "a")
	writeOrgFile(t, filepath.Join(src, "mixed", "Film B (2021).mkv"), "b")
	writeOrgFile(t, filepath.Join(src, "mixed", "poster.jpg"), "poster")
	// Unidentified video + its subtitle => subtitle attached.
	mystery := filepath.Join(src, "loose", "random-file.mkv")
	mysterySub := filepath.Join(src, "loose", "random-file.en.srt")
	writeOrgFile(t, mystery, "mystery")
	writeOrgFile(t, mysterySub, "subs")
	if err := os.MkdirAll(filepath.Join(src, "empty-folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := mustLinkPlan(t, src, tgt)
	foundPosterSkip := false
	for _, sk := range plan.Skipped {
		if strings.HasSuffix(sk.Path, "poster.jpg") && strings.Contains(sk.Reason, "more than one work") {
			foundPosterSkip = true
		}
	}
	if !foundPosterSkip {
		t.Errorf("multi-work artwork not skipped: %+v", plan.Skipped)
	}
	if len(plan.NotIdentified) != 1 {
		t.Fatalf("not_identified = %+v", plan.NotIdentified)
	}
	attached := plan.NotIdentified[0].AttachedFiles
	if len(attached) != 1 || attached[0] != mysterySub {
		t.Errorf("unidentified attached = %v, want [%s]", attached, mysterySub)
	}
	for _, e := range plan.LinksToCreate {
		if strings.Contains(e.Target, "empty") {
			t.Errorf("empty source folder produced a link: %+v", e)
		}
	}
}
