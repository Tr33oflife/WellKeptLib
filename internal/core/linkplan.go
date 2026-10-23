package core

// linkplan.go — the hardlink library planner (v1.1.0,
// LINK-LIBRARY-SPEC). Organize's second mode, "Build Jellyfin
// library (hardlinks)": the SAME parser, Jellyfin layout,
// sidecar and artwork rules as move mode, but the layout is
// built under a separate TARGET folder and Apply fills it with
// hardlinks instead of moves (see linkapply.go). The originals
// are never moved, renamed, copied over, deleted, or edited —
// the prime invariant of the mode.
//
// This file is the dry-run planner: it reads both trees and
// classifies every candidate into exactly one disposition
// (links-to-create / already-linked / collision / not-identified
// / unlinkable / target-conflict, plus the move planner's
// separate skipped outcomes). Same-filesystem feasibility is
// PROVEN HERE, in the preview, from walk-time device+inode
// identity — never deferred to Apply.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// fileIdentityForFn is the testable seam over fileIdentityFor
// (LINK-LIBRARY-SPEC §6.2), mirroring the codebase's
// injectable-function pattern: the organize/link walks and the
// link Apply/Undo read identity through it, so tests can
// simulate unknown identity or a foreign device without a
// second physical filesystem. It is not a user setting.
var fileIdentityForFn = fileIdentityFor

// linkRankResolver supplies ranking metadata for one collision
// candidate (LINK-LIBRARY-SPEC §6.4): the Service resolves
// from the current scan (exact path + size + mtime match) or
// probes the file; the package-level PlanLinkLibrary passes
// nil, which ranks by filename/size only. A nil return means
// "no metadata" — the planner falls back to a filename item
// and marks the group's ranking note accordingly.
type linkRankResolver func(source string, parsed ParsedName, size int64, modTimeUnixNano int64) *Item

// LinkEntry is one planned (or already existing) link: both
// endpoints and the identity proof Apply re-checks.
type LinkEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"` // movie | episode | sidecar | artwork
	Title  string `json:"title"`
	// OwnerSource is the video a sidecar belongs to (empty for
	// videos and folder artwork).
	OwnerSource string `json:"owner_source,omitempty"`
	SourceDev   uint64 `json:"source_dev,omitempty"`
	SourceIno   uint64 `json:"source_ino,omitempty"`
	SourceSize  int64  `json:"source_size"`
	// SourceMtimeUnix carries the source's modification time
	// at NANOSECOND precision despite the name — it is a
	// change-detection fingerprint for Apply revalidation,
	// not a display value.
	SourceMtimeUnix int64  `json:"source_mtime_unix"`
	DestinationDev  uint64 `json:"destination_dev,omitempty"`

	// DepSources (internal, never serialized): the video
	// sources whose link must exist — created by this Apply
	// or already linked — before Apply attempts this entry:
	// the owner video for a sidecar, the work's selected
	// videos for folder artwork. Empty for videos.
	DepSources []string `json:"-"`
}

// LinkSkip is one file deliberately not linked, with the reason.
type LinkSkip struct {
	Path          string   `json:"path"`
	Target        string   `json:"target,omitempty"`
	Reason        string   `json:"reason"`
	AttachedFiles []string `json:"attached_files,omitempty"`
}

// LinkAlternate is one source in a collision group that was not
// selected for the target.
type LinkAlternate struct {
	Source        string   `json:"source"`
	Reason        string   `json:"reason"`
	SamePhysical  bool     `json:"same_physical,omitempty"`
	AttachedFiles []string `json:"attached_files,omitempty"`
}

// LinkTargetConflict is one occupied target link mode will
// never touch, with the candidate sources it blocks.
type LinkTargetConflict struct {
	Target        string   `json:"target"`
	Sources       []string `json:"sources"`
	Occupant      string   `json:"occupant"` // regular file | directory | symbolic link | other | unknown identity
	Reason        string   `json:"reason"`
	AttachedFiles []string `json:"attached_files,omitempty"`
}

// LinkPlanWarning is one plan-level warning (scan-root overlap).
type LinkPlanWarning struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Roots   []string `json:"roots,omitempty"`
}

// LinkCollision is one target wanted by more than one source,
// with the keeper decision and its evidence.
type LinkCollision struct {
	Target         string          `json:"target"`
	KeeperSource   string          `json:"keeper_source,omitempty"`
	Alternates     []LinkAlternate `json:"alternates"`
	Ineligible     []LinkSkip      `json:"ineligible,omitempty"`
	DurationStatus string          `json:"duration_status,omitempty"`
	DurationNote   string          `json:"duration_note,omitempty"`
	RankingNote    string          `json:"ranking_note,omitempty"`
	Reason         string          `json:"reason"`
}

// LinkPlanStats labels its units: files unless the name says
// groups or targets.
type LinkPlanStats struct {
	LinksToCreate   int `json:"links_to_create"`
	AlreadyLinked   int `json:"already_linked"`
	CollisionGroups int `json:"collision_groups"`
	Alternates      int `json:"alternates"`
	NotIdentified   int `json:"not_identified"`
	Unlinkable      int `json:"unlinkable"`
	ConflictTargets int `json:"target_conflict_targets"`
	ConflictFiles   int `json:"target_conflict_files"`
	Skipped         int `json:"skipped"`
	// Movies / Episodes / Sidecars / Artwork count the files
	// within links-to-create only.
	Movies   int `json:"movies"`
	Episodes int `json:"episodes"`
	Sidecars int `json:"sidecars"`
	Artwork  int `json:"artwork"`
}

// LinkPlan is the full dry-run output for one source root into
// one target root.
type LinkPlan struct {
	ID              string               `json:"id"`
	SourceRoot      string               `json:"source_root"`
	TargetRoot      string               `json:"target_root"`
	LinksToCreate   []LinkEntry          `json:"links_to_create"`
	AlreadyLinked   []LinkEntry          `json:"already_linked"`
	Collisions      []LinkCollision      `json:"collisions"`
	NotIdentified   []LinkSkip           `json:"not_identified"`
	Unlinkable      []LinkSkip           `json:"unlinkable"`
	TargetConflicts []LinkTargetConflict `json:"target_conflicts"`
	Skipped         []LinkSkip           `json:"skipped"`
	Warnings        []LinkPlanWarning    `json:"warnings"`
	Stats           LinkPlanStats        `json:"stats"`
}

// targetKey is the platform grouping key for a target path:
// cleaned absolute; case-folded on Windows, where two names
// differing only by case are the same file (SPEC §5.4).
func targetKey(p string) string {
	k := cleanAbs(p)
	if runtime.GOOS == "windows" {
		k = strings.ToLower(k)
	}
	return k
}

// checkLinkRoots validates a cleaned source/target root pair
// (LINK-LIBRARY-SPEC §5.3): the roots must be disjoint — a hard
// error, never a warning — and the target, when it exists, must
// be a real directory (never a symbolic link); when it does not
// exist, its parent must be a real directory, so Apply creates
// exactly one new, recorded level.
func checkLinkRoots(absSource, absTarget string) error {
	if absTarget == absSource {
		return fmt.Errorf("the target folder must be different from the source folder")
	}
	if pathWithinRoot(absSource, absTarget) {
		return fmt.Errorf("the target folder is inside the source folder — pick a target outside it")
	}
	if pathWithinRoot(absTarget, absSource) {
		return fmt.Errorf("the source folder is inside the target folder — pick a different target")
	}
	info, err := os.Lstat(absTarget)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("the target folder is a symbolic link — pick a real folder")
		}
		if !info.IsDir() {
			return fmt.Errorf("the target path exists and is not a folder")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("cannot check the target folder: %w", err)
	}
	parent := filepath.Dir(absTarget)
	pinfo, perr := os.Lstat(parent)
	if perr != nil {
		return fmt.Errorf("the target folder's parent %s does not exist — create it first or pick another target", parent)
	}
	if pinfo.Mode()&os.ModeSymlink != 0 || !pinfo.IsDir() {
		return fmt.Errorf("the target folder's parent %s is not a real folder", parent)
	}
	return nil
}

// linkRootsOverlap reports whether the link plan's two roots
// overlap at all (used by Undo manifest validation).
func linkRootsOverlap(a, b string) bool {
	return cleanAbs(a) == cleanAbs(b) || pathWithinRoot(a, b) || pathWithinRoot(b, a)
}

// targetOccupant is the Lstat-level truth about one target path.
type targetOccupant struct {
	state string // absent | symlink | dir | other | regular
	dev   uint64
	ino   uint64
	known bool // identity readable (regular files only)
}

// inspectTargetOccupant Lstats the target (never follows a
// symbolic link) and, for a regular file, reads its identity.
func inspectTargetOccupant(path string) targetOccupant {
	info, err := os.Lstat(path)
	if err != nil {
		return targetOccupant{state: "absent"}
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return targetOccupant{state: "symlink"}
	case mode.IsDir():
		return targetOccupant{state: "dir"}
	case !mode.IsRegular():
		return targetOccupant{state: "other"}
	}
	dev, ino, _, ok := fileIdentityForFn(info, path)
	return targetOccupant{state: "regular", dev: dev, ino: ino, known: ok}
}

// label is the user-facing occupant word for conflicts.
func (o targetOccupant) label() string {
	switch o.state {
	case "symlink":
		return "symbolic link"
	case "dir":
		return "directory"
	case "other":
		return "other"
	case "regular":
		if o.known {
			return "regular file"
		}
		return "unknown identity"
	}
	return "other"
}

// conflictReason is the never-overwrite explanation per occupant.
func (o targetOccupant) conflictReason() string {
	switch o.state {
	case "symlink":
		return "a symbolic link already sits at the target path — a symbolic link is not a hardlink, and link mode never replaces one"
	case "dir":
		return "a folder already sits at the target path — it is never removed to make room for a link"
	case "other":
		return "something that is not a regular file already sits at the target path — it is never removed"
	case "regular":
		if o.known {
			return "a different file already sits at the target path — it is never overwritten or replaced"
		}
		return "a file already sits at the target path and cannot be proven to be a hardlink to the source — it is never overwritten or replaced"
	}
	return "the target path is occupied — it is never overwritten or replaced"
}

// destDeviceInfo is the destination-filesystem verdict for one
// target: the device of its nearest existing ancestor directory,
// and whether a path component blocks directory creation.
type destDeviceInfo struct {
	dev          uint64
	known        bool
	blocked      string // non-empty: why no link can be created under this parent
	blockedLabel string // occupant word for the blocked conflict
}

// PlanLinkLibrary computes the link-mode dry-run plan with the
// default config and filename/size ranking only. The Service
// entry point (Service.LinkPlan) passes the live config and its
// ranking resolver.
func PlanLinkLibrary(sourceRoot, targetRoot string) (*LinkPlan, error) {
	return planLinkLibrary(sourceRoot, targetRoot, defaultConfig(), nil)
}

// linkVideo is the planner's per-video working record: the
// shared videoPlan plus where its outcome record lives (list +
// index), so sidecars and artwork withheld by that outcome can
// be attached to it after every list has stopped growing
// (indices are resolved before the final sort).
type linkVideo struct {
	vp      *videoPlan
	outKind string // "" (selected) | notidentified | unlinkable | conflict | alternate | ineligible
	outA    int    // index into the plan list (or the collision list)
	outB    int    // index inside the collision's alternates / ineligible
}

// pendingAttach is one withheld file to record on its owner
// video's outcome once classification is complete.
type pendingAttach struct {
	vp   *videoPlan
	path string
}

// linkCandidate is one non-video file (sidecar / artwork) whose
// owner video was selected, awaiting its own target
// classification.
type linkCandidate struct {
	file        orgFile
	target      string
	kind        string // sidecar | artwork
	title       string
	ownerSource string
	depSources  []string
}

type linkPlanBuilder struct {
	plan      *LinkPlan
	cfg       *Config
	resolve   linkRankResolver
	lv        map[*videoPlan]*linkVideo
	destCache map[string]destDeviceInfo
	pending   []pendingAttach
}

func planLinkLibrary(sourceRoot, targetRoot string, cfg *Config, resolveRank linkRankResolver) (*LinkPlan, error) {
	valid, problems := validateRoots([]string{sourceRoot})
	if len(valid) == 0 {
		if len(problems) > 0 {
			return nil, fmt.Errorf("cannot plan a link library — %s", strings.Join(problems, "; "))
		}
		return nil, fmt.Errorf("cannot plan a link library — no source folder given")
	}
	absSource := cleanAbs(valid[0])
	rawTarget := cleanRootInput(targetRoot)
	if rawTarget == "" {
		return nil, fmt.Errorf("cannot plan a link library — no target folder given")
	}
	absTarget := cleanAbs(rawTarget)
	if err := checkLinkRoots(absSource, absTarget); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = defaultConfig()
	}
	plan := &LinkPlan{
		ID:              newPlanID(),
		SourceRoot:      absSource,
		TargetRoot:      absTarget,
		LinksToCreate:   []LinkEntry{},
		AlreadyLinked:   []LinkEntry{},
		Collisions:      []LinkCollision{},
		NotIdentified:   []LinkSkip{},
		Unlinkable:      []LinkSkip{},
		TargetConflicts: []LinkTargetConflict{},
		Skipped:         []LinkSkip{},
		Warnings:        []LinkPlanWarning{},
	}
	b := &linkPlanBuilder{
		plan: plan, cfg: cfg, resolve: resolveRank,
		lv: map[*videoPlan]*linkVideo{}, destCache: map[string]destDeviceInfo{},
	}

	filesByDir, symlinked, err := collectOrgFiles(absSource)
	if err != nil {
		return nil, fmt.Errorf("cannot read the folder %s: %w", absSource, err)
	}
	for _, sf := range symlinked {
		plan.Skipped = append(plan.Skipped, LinkSkip{Path: sf.Path, Reason: "symbolic link — left alone"})
	}

	// ---- Pass 1: videos. Parse identity against the SOURCE
	// root (folder context belongs to the source name), build
	// targets under the TARGET root. ----
	videosByDir := map[string][]*videoPlan{}
	var videos []*videoPlan
	for _, files := range filesByDir {
		for _, f := range files {
			if !mediaExts[f.Ext] {
				continue
			}
			vp := &videoPlan{File: f, Parsed: parseName(f.Path, absSource)}
			lv := &linkVideo{vp: vp}
			b.lv[vp] = lv
			target, targetDir, targetStem, workDir, reason := buildVideoTarget(absTarget, f, vp.Parsed)
			if reason == "" && !pathWithinRoot(absTarget, target) {
				// Defensive confinement (SPEC §5.4): sanitization
				// should make this unreachable.
				reason = "planned target would escape the target folder — left in place"
			}
			if reason != "" {
				vp.Status = "unidentified"
				plan.NotIdentified = append(plan.NotIdentified, LinkSkip{Path: f.Path, Reason: reason})
				lv.outKind, lv.outA = "notidentified", len(plan.NotIdentified)-1
			} else {
				vp.Target, vp.TargetDir, vp.TargetStem, vp.WorkDir = target, targetDir, targetStem, workDir
				vp.Status = "candidate"
			}
			videos = append(videos, vp)
			videosByDir[f.Dir] = append(videosByDir[f.Dir], vp)
		}
	}

	// ---- Pass 2: resolve each video target group. ----
	groups := map[string][]*videoPlan{}
	var groupKeys []string
	for _, vp := range videos {
		if vp.Status != "candidate" {
			continue
		}
		k := targetKey(vp.Target)
		if _, ok := groups[k]; !ok {
			groupKeys = append(groupKeys, k)
		}
		groups[k] = append(groups[k], vp)
	}
	sort.Strings(groupKeys)
	for _, k := range groupKeys {
		b.resolveVideoGroup(groups[k][0].Target, groups[k])
	}

	// ---- Pass 3: sidecars + folder artwork for selected
	// videos only; everything else is attached to its owner's
	// outcome or skipped in the move planner's shapes. ----
	var cands []*linkCandidate
	for dir, files := range filesByDir {
		for _, f := range files {
			if mediaExts[f.Ext] {
				continue
			}
			cands = b.planLinkNonVideo(f, videosByDir[dir], cands)
		}
	}

	// ---- Pass 4: classify the non-video candidates against
	// the live target tree and the video reservations. ----
	b.classifyCandidates(cands)

	// ---- Attach withheld files to their owners' outcomes
	// (all lists have stopped growing; indices are final). ----
	for _, pa := range b.pending {
		b.applyAttach(pa)
	}

	// ---- Deterministic ordering + stats + warnings. ----
	sort.Slice(plan.LinksToCreate, func(i, j int) bool { return plan.LinksToCreate[i].Source < plan.LinksToCreate[j].Source })
	sort.Slice(plan.AlreadyLinked, func(i, j int) bool { return plan.AlreadyLinked[i].Source < plan.AlreadyLinked[j].Source })
	sort.Slice(plan.Collisions, func(i, j int) bool { return plan.Collisions[i].Target < plan.Collisions[j].Target })
	sort.Slice(plan.TargetConflicts, func(i, j int) bool { return plan.TargetConflicts[i].Target < plan.TargetConflicts[j].Target })
	sort.Slice(plan.NotIdentified, func(i, j int) bool { return plan.NotIdentified[i].Path < plan.NotIdentified[j].Path })
	sort.Slice(plan.Unlinkable, func(i, j int) bool { return plan.Unlinkable[i].Path < plan.Unlinkable[j].Path })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].Path < plan.Skipped[j].Path })

	st := &plan.Stats
	st.LinksToCreate = len(plan.LinksToCreate)
	st.AlreadyLinked = len(plan.AlreadyLinked)
	st.CollisionGroups = len(plan.Collisions)
	for _, c := range plan.Collisions {
		st.Alternates += len(c.Alternates)
	}
	st.NotIdentified = len(plan.NotIdentified)
	st.Unlinkable = len(plan.Unlinkable)
	st.ConflictTargets = len(plan.TargetConflicts)
	for _, tc := range plan.TargetConflicts {
		st.ConflictFiles += len(tc.Sources)
	}
	st.Skipped = len(plan.Skipped)
	for _, e := range plan.LinksToCreate {
		switch e.Kind {
		case "movie":
			st.Movies++
		case "episode":
			st.Episodes++
		case "sidecar":
			st.Sidecars++
		case "artwork":
			st.Artwork++
		}
	}
	plan.Warnings = append(plan.Warnings, linkScanOverlapWarnings(cfg.Roots, absTarget)...)
	return plan, nil
}

// applyAttach records one withheld file on its owner video's
// outcome record.
func (b *linkPlanBuilder) applyAttach(pa pendingAttach) {
	lv := b.lv[pa.vp]
	if lv == nil {
		return
	}
	plan := b.plan
	switch lv.outKind {
	case "notidentified":
		rec := &plan.NotIdentified[lv.outA]
		rec.AttachedFiles = append(rec.AttachedFiles, pa.path)
	case "unlinkable":
		rec := &plan.Unlinkable[lv.outA]
		rec.AttachedFiles = append(rec.AttachedFiles, pa.path)
	case "conflict":
		rec := &plan.TargetConflicts[lv.outA]
		rec.AttachedFiles = append(rec.AttachedFiles, pa.path)
	case "alternate":
		rec := &plan.Collisions[lv.outA].Alternates[lv.outB]
		rec.AttachedFiles = append(rec.AttachedFiles, pa.path)
	case "ineligible":
		rec := &plan.Collisions[lv.outA].Ineligible[lv.outB]
		rec.AttachedFiles = append(rec.AttachedFiles, pa.path)
	}
}

// destinationDevice returns the feasibility verdict for one
// target (cached per parent folder): the device of the nearest
// existing ancestor directory, and whether an existing path
// component below the target root blocks creation (a symbolic
// link or a non-directory where a folder is needed — those are
// target-conflicts per SPEC §7.5, not ordinary links).
func (b *linkPlanBuilder) destinationDevice(target string) destDeviceInfo {
	parent := filepath.Dir(target)
	if info, ok := b.destCache[parent]; ok {
		return info
	}
	info := b.computeDestinationDevice(target)
	b.destCache[parent] = info
	return info
}

func (b *linkPlanBuilder) computeDestinationDevice(target string) destDeviceInfo {
	return destinationDeviceFor(b.plan.TargetRoot, target)
}

// destinationDeviceFor is the uncached computation behind the
// planner's destinationDevice; link Apply's revalidation calls
// it directly.
func destinationDeviceFor(root, target string) destDeviceInfo {
	if rel, rerr := filepath.Rel(root, filepath.Dir(target)); rerr == nil && rel != "." {
		cur := root
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			cur = filepath.Join(cur, part)
			fi, err := os.Lstat(cur)
			if err != nil {
				break // missing: deeper components cannot exist
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return destDeviceInfo{blocked: "a folder inside the target folder is a symbolic link (" + cur + ") — links are never created through one", blockedLabel: "symbolic link"}
			}
			if !fi.IsDir() {
				return destDeviceInfo{blocked: "a file blocks the target folder path at " + cur + " — it is never removed", blockedLabel: "other"}
			}
		}
	}
	for d := filepath.Dir(target); ; d = filepath.Dir(d) {
		fi, err := os.Lstat(d)
		if err == nil {
			dev, _, _, ok := fileIdentityForFn(fi, d)
			return destDeviceInfo{dev: dev, known: ok}
		}
		if filepath.Dir(d) == d {
			return destDeviceInfo{}
		}
	}
}

// feasibilityReason returns "" when a hardlink to vp's file can
// provably be created at target, or the unlinkable reason.
// Identity is the gate: without a known source device+inode,
// neither the same-filesystem proof nor a safe undo record
// exists (SPEC §7.5).
func (b *linkPlanBuilder) feasibilityReason(vp *videoPlan, target string) (string, destDeviceInfo) {
	di := b.destinationDevice(target)
	if di.blocked != "" {
		return "", di // the caller turns blocked into a target-conflict
	}
	if !vp.File.IdentityKnown {
		return "the source's device/inode is unavailable, so a same-filesystem link and a safe undo record cannot be proven", di
	}
	if !di.known {
		return "the target folder's filesystem cannot be determined, so a same-filesystem link cannot be proven", di
	}
	if vp.File.Dev != di.dev {
		return fmt.Sprintf("the source is on device %d but the target folder is on device %d — hardlinks cannot cross filesystems", vp.File.Dev, di.dev), di
	}
	return "", di
}

// makeEntry builds the LinkEntry for a selected video.
func (b *linkPlanBuilder) makeEntry(vp *videoPlan, destDev uint64) LinkEntry {
	return LinkEntry{
		Source: vp.File.Path, Target: vp.Target, Kind: vp.Parsed.Kind, Title: vp.Parsed.Title,
		SourceDev: vp.File.Dev, SourceIno: vp.File.Ino, SourceSize: vp.File.Size,
		SourceMtimeUnix: vp.File.ModTimeUnixNano, DestinationDev: destDev,
	}
}

// addVideoConflict records one occupied video target and marks
// every candidate in the group as conflicted.
func (b *linkPlanBuilder) addVideoConflict(target string, group []*videoPlan, occ targetOccupant, blockedReason string) {
	plan := b.plan
	sources := make([]string, 0, len(group))
	for _, vp := range group {
		sources = append(sources, vp.File.Path)
	}
	sort.Strings(sources)
	reason := occ.conflictReason()
	occupant := occ.label()
	if blockedReason != "" {
		reason = blockedReason
		occupant = b.destinationDevice(target).blockedLabel
	}
	idx := len(plan.TargetConflicts)
	plan.TargetConflicts = append(plan.TargetConflicts, LinkTargetConflict{
		Target: target, Sources: sources, Occupant: occupant, Reason: reason,
	})
	for _, vp := range group {
		vp.Status = "conflict"
		lv := b.lv[vp]
		lv.outKind, lv.outA = "conflict", idx
	}
}

// resolveVideoGroup classifies every video wanting one target:
// existing occupant first (SPEC §7.4), then collision
// resolution (§8).
func (b *linkPlanBuilder) resolveVideoGroup(target string, group []*videoPlan) {
	occ := inspectTargetOccupant(target)
	switch occ.state {
	case "symlink", "dir", "other":
		b.addVideoConflict(target, group, occ, "")
		return
	case "regular":
		if occ.known {
			for _, vp := range group {
				if vp.File.IdentityKnown && vp.File.Dev == occ.dev && vp.File.Ino == occ.ino {
					b.resolveOccupiedGroup(target, group, vp, occ)
					return
				}
			}
		}
		b.addVideoConflict(target, group, occ, "")
		return
	}
	// Absent: a blocked destination chain is a conflict, not a
	// creatable link (SPEC §7.5).
	if di := b.destinationDevice(target); di.blocked != "" {
		b.addVideoConflict(target, group, targetOccupant{state: "other"}, di.blocked)
		return
	}
	b.resolveAbsentVideoGroup(target, group)
}

// resolveOccupiedGroup handles a target that already holds a
// hardlink to one of the candidate sources: that source is the
// de facto selection and is NEVER replaced to upgrade the
// keeper (SPEC §7.4).
func (b *linkPlanBuilder) resolveOccupiedGroup(target string, group []*videoPlan, linked *videoPlan, occ targetOccupant) {
	plan := b.plan
	linked.Status = "linked"
	plan.AlreadyLinked = append(plan.AlreadyLinked, b.makeEntry(linked, occ.dev))
	if len(group) == 1 {
		return
	}
	colIdx := len(plan.Collisions)
	col := LinkCollision{
		Target:       target,
		KeeperSource: linked.File.Path,
		Reason:       "the target already holds a hardlink to one copy — it is never replaced to upgrade the keeper",
	}
	for _, vp := range sortedVideosByPath(group) {
		if vp == linked {
			continue
		}
		reason := "the target is already linked to another copy of this work"
		same := vp.File.IdentityKnown && linked.File.IdentityKnown &&
			vp.File.Dev == linked.File.Dev && vp.File.Ino == linked.File.Ino
		if same {
			reason = "same physical file as the linked copy — no second link needed"
		}
		col.Alternates = append(col.Alternates, LinkAlternate{Source: vp.File.Path, Reason: reason, SamePhysical: same})
		vp.Status = "alternate"
		lv := b.lv[vp]
		lv.outKind, lv.outA, lv.outB = "alternate", colIdx, len(col.Alternates)-1
	}
	plan.Collisions = append(plan.Collisions, col)
}

// linkPartitionKey is the collision partition key (SPEC §8.1):
// the duplicate identityKey over the parsed identity, plus the
// parser's Special value (split-file / multi-episode /
// fractional never share a partition with plain copies).
func linkPartitionKey(vp *videoPlan) string {
	it := &Item{
		Name: vp.File.Name, Kind: vp.Parsed.Kind, NormTitle: vp.Parsed.NormTitle,
		Year: vp.Parsed.Year, Season: vp.Parsed.Season, Episodes: vp.Parsed.Episodes,
		Edition: vp.Parsed.Edition,
	}
	return identityKey(it) + "\x00" + vp.Parsed.Special
}

// dedupePhysical collapses same-physical source paths (equal
// known device+inode) to their lexicographically first cleaned
// path; the rest are returned as extras of their representative.
// Candidates with unknown identity are each their own physical
// copy.
func dedupePhysical(group []*videoPlan) (reps []*videoPlan, extras map[*videoPlan][]*videoPlan) {
	extras = map[*videoPlan][]*videoPlan{}
	seen := map[string]*videoPlan{}
	for _, vp := range sortedVideosByPath(group) {
		if vp.File.IdentityKnown {
			k := fmt.Sprintf("%d:%d", vp.File.Dev, vp.File.Ino)
			if rep, ok := seen[k]; ok {
				extras[rep] = append(extras[rep], vp)
				continue
			}
			seen[k] = vp
		}
		reps = append(reps, vp)
	}
	return reps, extras
}

func sortedVideosByPath(group []*videoPlan) []*videoPlan {
	out := append([]*videoPlan{}, group...)
	sort.Slice(out, func(i, j int) bool { return out[i].File.Path < out[j].File.Path })
	return out
}

// resolveAbsentVideoGroup resolves a group whose target is
// absent: identity partition, split-file guard, physical
// dedupe, the duration eligibility gate, feasibility, and the
// keeper fold (SPEC §8).
func (b *linkPlanBuilder) resolveAbsentVideoGroup(target string, group []*videoPlan) {
	plan := b.plan
	// 1. Partition by parsed identity/edition.
	partitions := map[string][]*videoPlan{}
	var partKeys []string
	for _, vp := range group {
		k := linkPartitionKey(vp)
		if _, ok := partitions[k]; !ok {
			partKeys = append(partKeys, k)
		}
		partitions[k] = append(partitions[k], vp)
	}
	if len(partKeys) > 1 {
		b.addUnresolvedCollision(target, group,
			"different parsed identities or editions land on the same Jellyfin name — v1.1.0 does not invent a distinguishing name, so none of them is linked",
			"not linked — this collision could not be resolved automatically", "", "")
		return
	}
	// 2. Split-file guard: parts are segments of one work,
	// never interchangeable copies.
	if len(group) > 1 {
		for _, vp := range group {
			if vp.Parsed.Special == "split-file" {
				b.addUnresolvedCollision(target, group,
					"split-file parts are segments of one work, not copies of each other — none of them is linked",
					"not linked — split-file part", "", "")
				return
			}
		}
	}
	// 3. Physical dedupe.
	reps, extras := dedupePhysical(group)
	if len(reps) == 1 {
		rep := reps[0]
		if reason, di := b.feasibilityReason(rep, target); reason != "" {
			b.addUnlinkableGroup(group, target, reason)
			return
		} else {
			rep.Status = "create"
			plan.LinksToCreate = append(plan.LinksToCreate, b.makeEntry(rep, di.dev))
		}
		if len(group) > 1 {
			colIdx := len(plan.Collisions)
			col := LinkCollision{
				Target:       target,
				KeeperSource: rep.File.Path,
				Reason:       "several source paths name the same physical file — only one link is needed",
			}
			for _, ex := range extras[rep] {
				col.Alternates = append(col.Alternates, LinkAlternate{
					Source: ex.File.Path, SamePhysical: true,
					Reason: "same physical file as the keeper — no second link needed",
				})
				ex.Status = "alternate"
				lv := b.lv[ex]
				lv.outKind, lv.outA, lv.outB = "alternate", colIdx, len(col.Alternates)-1
			}
			plan.Collisions = append(plan.Collisions, col)
		}
		return
	}
	// 4. Ranking metadata — collision groups only (SPEC §8.3).
	items := map[*videoPlan]*Item{}
	anyFallback := false
	for _, rep := range reps {
		it, fb := b.rankingItem(rep)
		items[rep] = it
		anyFallback = anyFallback || fb
	}
	// 5. Duration eligibility gate (SPEC §8.2): demoted means
	// NO automatic keeper, ever.
	itemList := make([]*Item, 0, len(reps))
	for _, rep := range reps {
		itemList = append(itemList, items[rep])
	}
	status, note := classifyDurations(itemList, b.cfg)
	if status == "demoted" {
		b.addUnresolvedCollision(target, group,
			"no keeper was chosen: "+note,
			"not linked — the duration check could not confirm these copies are the same cut",
			status, note)
		return
	}
	// 6. Feasibility split: infeasible copies are ineligible,
	// never silently treated as alternates.
	var feasible []*videoPlan
	ineligible := map[*videoPlan]string{}
	for _, rep := range reps {
		if reason, _ := b.feasibilityReason(rep, target); reason != "" {
			ineligible[rep] = reason
		} else {
			feasible = append(feasible, rep)
		}
	}
	if len(feasible) == 0 {
		for _, rep := range reps {
			b.addUnlinkable(rep, target, ineligible[rep])
			for _, ex := range extras[rep] {
				b.addUnlinkable(ex, target, ineligible[rep])
			}
		}
		return
	}
	// 7. Keeper fold: candidates in ascending cleaned-path
	// order, replaced only when strictly better — an exact
	// comparator tie keeps the first path (SPEC §8.4).
	keeper := feasible[0]
	for _, cand := range feasible[1:] {
		if betterKeeper(items[cand], items[keeper]) {
			keeper = cand
		}
	}
	_, keeperDest := b.feasibilityReason(keeper, target)
	keeper.Status = "create"
	plan.LinksToCreate = append(plan.LinksToCreate, b.makeEntry(keeper, keeperDest.dev))

	colIdx := len(plan.Collisions)
	col := LinkCollision{
		Target:         target,
		KeeperSource:   keeper.File.Path,
		DurationStatus: status,
		DurationNote:   note,
		RankingNote:    rankingNote(items[keeper], anyFallback),
	}
	addAlt := func(vp *videoPlan, reason string, same bool) {
		col.Alternates = append(col.Alternates, LinkAlternate{Source: vp.File.Path, Reason: reason, SamePhysical: same})
		vp.Status = "alternate"
		lv := b.lv[vp]
		lv.outKind, lv.outA, lv.outB = "alternate", colIdx, len(col.Alternates)-1
	}
	for _, rep := range feasible {
		if rep == keeper {
			continue
		}
		addAlt(rep, "another copy of the same work — the keeper above was linked instead", false)
		for _, ex := range extras[rep] {
			addAlt(ex, "same physical file as an unlinked copy", true)
		}
	}
	for _, ex := range extras[keeper] {
		addAlt(ex, "same physical file as the keeper — no second link needed", true)
	}
	for _, rep := range reps {
		reason, bad := ineligible[rep]
		if !bad {
			continue
		}
		col.Ineligible = append(col.Ineligible, LinkSkip{Path: rep.File.Path, Target: target, Reason: reason})
		rep.Status = "ineligible"
		lv := b.lv[rep]
		lv.outKind, lv.outA, lv.outB = "ineligible", colIdx, len(col.Ineligible)-1
		for _, ex := range extras[rep] {
			col.Ineligible = append(col.Ineligible, LinkSkip{Path: ex.File.Path, Target: target, Reason: reason})
			ex.Status = "ineligible"
			lvx := b.lv[ex]
			lvx.outKind, lvx.outA, lvx.outB = "ineligible", colIdx, len(col.Ineligible)-1
		}
	}
	plan.Collisions = append(plan.Collisions, col)
}

// addUnresolvedCollision records a collision group with no
// keeper: every source becomes an alternate with the given
// per-source reason.
func (b *linkPlanBuilder) addUnresolvedCollision(target string, group []*videoPlan, reason, altReason, durStatus, durNote string) {
	plan := b.plan
	colIdx := len(plan.Collisions)
	col := LinkCollision{Target: target, Reason: reason, DurationStatus: durStatus, DurationNote: durNote}
	for _, vp := range sortedVideosByPath(group) {
		col.Alternates = append(col.Alternates, LinkAlternate{Source: vp.File.Path, Reason: altReason})
		vp.Status = "alternate"
		lv := b.lv[vp]
		lv.outKind, lv.outA, lv.outB = "alternate", colIdx, len(col.Alternates)-1
	}
	plan.Collisions = append(plan.Collisions, col)
}

// addUnlinkable records one source as unlinkable and indexes
// its outcome for attachments.
func (b *linkPlanBuilder) addUnlinkable(vp *videoPlan, target, reason string) {
	plan := b.plan
	plan.Unlinkable = append(plan.Unlinkable, LinkSkip{Path: vp.File.Path, Target: target, Reason: reason})
	vp.Status = "unlinkable"
	lv := b.lv[vp]
	lv.outKind, lv.outA = "unlinkable", len(plan.Unlinkable)-1
}

// addUnlinkableGroup marks every path of a single-physical-copy
// group unlinkable with the representative's reason.
func (b *linkPlanBuilder) addUnlinkableGroup(group []*videoPlan, target, reason string) {
	for _, vp := range sortedVideosByPath(group) {
		b.addUnlinkable(vp, target, reason)
	}
}

// rankingItem resolves the ranking Item for one collision
// representative (SPEC §8.3): resolver metadata when available,
// filename/size fallback otherwise. Parsed identity fields and
// the walk-observed size always win; the resolution label falls
// back to the parsed filename label when no probed height
// exists.
func (b *linkPlanBuilder) rankingItem(vp *videoPlan) (*Item, bool) {
	var it *Item
	if b.resolve != nil {
		it = b.resolve(vp.File.Path, vp.Parsed, vp.File.Size, vp.File.ModTimeUnixNano)
	}
	if it == nil {
		return &Item{
			Path: vp.File.Path, Name: vp.File.Name, Kind: vp.Parsed.Kind,
			Title: vp.Parsed.Title, NormTitle: vp.Parsed.NormTitle, Year: vp.Parsed.Year,
			Season: vp.Parsed.Season, Episodes: vp.Parsed.Episodes, Edition: vp.Parsed.Edition,
			ResolutionLabel: vp.Parsed.Resolution, Size: vp.File.Size,
		}, true
	}
	cp := *it
	cp.Path, cp.Name = vp.File.Path, vp.File.Name
	cp.Kind, cp.Title, cp.NormTitle = vp.Parsed.Kind, vp.Parsed.Title, vp.Parsed.NormTitle
	cp.Year, cp.Season, cp.Episodes, cp.Edition = vp.Parsed.Year, vp.Parsed.Season, vp.Parsed.Episodes, vp.Parsed.Edition
	cp.Size = vp.File.Size
	if cp.Height == 0 {
		cp.ResolutionLabel = vp.Parsed.Resolution
	}
	return &cp, false
}

// rankingNote describes how the keeper won, in the comparator's
// own terms. When any candidate ranked on filename/size alone,
// the note says so with the spec's exact phrase (SPEC §8.3).
func rankingNote(keeper *Item, anyFallback bool) string {
	facts := []string{}
	if h := resHeight(keeper); h > 0 {
		facts = append(facts, fmt.Sprintf("%dp", h))
	}
	if keeper.HDR {
		facts = append(facts, "HDR")
	}
	if keeper.Bitrate > 0 {
		facts = append(facts, fmt.Sprintf("%.1f Mbps", float64(keeper.Bitrate)/1e6))
	}
	facts = append(facts, fmt.Sprintf("%d bytes", keeper.Size))
	note := "keeper chosen by the duplicate ranking (resolution, then HDR, then bitrate, then size): " + strings.Join(facts, " · ")
	if anyFallback {
		note = "filename/size ranking — probe data unavailable for at least one copy. " + note
	}
	return note
}

// planLinkNonVideo classifies one non-video file for link mode,
// mirroring planNonVideo's shapes: a basename sidecar of a
// selected video becomes a candidate; a sidecar whose owner was
// not selected is attached to the owner's outcome; folder
// artwork follows a single selected work; orphans are skipped.
func (b *linkPlanBuilder) planLinkNonVideo(f orgFile, dirVideos []*videoPlan, cands []*linkCandidate) []*linkCandidate {
	plan := b.plan
	matches := sidecarVideoMatches(f.Name, dirVideos)
	if len(matches) > 0 {
		var sel []*videoPlan
		for _, vp := range matches {
			if vp.Status == "create" || vp.Status == "linked" {
				sel = append(sel, vp)
			}
		}
		switch {
		case len(sel) == 1:
			owner := sel[0]
			suffix := f.Name[len(owner.File.Stem):] // preserved exactly, as in move mode
			return append(cands, &linkCandidate{
				file: f, target: filepath.Join(owner.TargetDir, owner.TargetStem+suffix),
				kind: "sidecar", title: owner.Parsed.Title,
				ownerSource: owner.File.Path, depSources: []string{owner.File.Path},
			})
		case len(sel) > 1:
			plan.Skipped = append(plan.Skipped, LinkSkip{
				Path: f.Path, Reason: "matches more than one video in its folder — left in place",
			})
		default:
			// Its video is staying unlinked, so the sidecar is
			// reported on that video's outcome — creating it
			// anyway would manufacture an orphan in the target.
			b.pending = append(b.pending, pendingAttach{vp: matches[0], path: f.Path})
		}
		return cands
	}

	if isFolderArtworkFile(f) {
		works := map[string][]*videoPlan{}
		for _, vp := range dirVideos {
			if vp.Status == "create" || vp.Status == "linked" {
				works[vp.WorkDir] = append(works[vp.WorkDir], vp)
			}
		}
		switch {
		case len(works) == 1:
			var workDir string
			var deps []string
			var title string
			for wd, vps := range works {
				workDir = wd
				title = vps[0].Parsed.Title
				for _, vp := range vps {
					deps = append(deps, vp.File.Path)
				}
			}
			sort.Strings(deps)
			return append(cands, &linkCandidate{
				file: f, target: filepath.Join(workDir, f.Name),
				kind: "artwork", title: title, depSources: deps,
			})
		case len(works) > 1:
			plan.Skipped = append(plan.Skipped, LinkSkip{
				Path: f.Path, Reason: "folder holds more than one work — artwork left in place",
			})
		case len(dirVideos) == 0:
			plan.Skipped = append(plan.Skipped, LinkSkip{
				Path: f.Path, Reason: "sidecar with no matching video",
			})
		default:
			// Videos are here but none was selected; the artwork
			// is withheld with the first video's outcome.
			b.pending = append(b.pending, pendingAttach{vp: dirVideos[0], path: f.Path})
		}
		return cands
	}

	if subtitleExts[f.Ext] || f.Ext == ".nfo" {
		plan.Skipped = append(plan.Skipped, LinkSkip{
			Path: f.Path, Reason: "sidecar with no matching video",
		})
	}
	return cands
}

// classifyCandidates runs the non-video candidates through the
// reservation sweep and their own target classification
// (SPEC §7.3 steps 10–11, §9.4): a candidate can never take a
// video target; two distinct non-video files never share a
// target; same-physical paths dedupe to one representative.
func (b *linkPlanBuilder) classifyCandidates(cands []*linkCandidate) {
	plan := b.plan
	reserved := map[string]string{}
	for _, e := range plan.LinksToCreate {
		reserved[targetKey(e.Target)] = e.Source
	}
	for _, e := range plan.AlreadyLinked {
		reserved[targetKey(e.Target)] = e.Source
	}
	groups := map[string][]*linkCandidate{}
	var keys []string
	for _, c := range cands {
		k := targetKey(c.target)
		if src, taken := reserved[k]; taken {
			plan.Collisions = append(plan.Collisions, LinkCollision{
				Target: c.target,
				Reason: "this name is already taken by a video in this plan — the file is not linked",
				Alternates: []LinkAlternate{{
					Source: c.file.Path,
					Reason: "not linked — a video target (" + src + ") takes this name",
				}},
			})
			continue
		}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], c)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.classifyCandidateGroup(groups[k])
	}
}

// classifyCandidateGroup classifies one same-target group of
// non-video candidates.
func (b *linkPlanBuilder) classifyCandidateGroup(group []*linkCandidate) {
	plan := b.plan
	target := group[0].target
	// Same-physical dedupe (SPEC §9.4).
	seen := map[string]*linkCandidate{}
	var reps []*linkCandidate
	extras := map[*linkCandidate][]*linkCandidate{}
	sorted := append([]*linkCandidate{}, group...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].file.Path < sorted[j].file.Path })
	for _, c := range sorted {
		if c.file.IdentityKnown {
			k := fmt.Sprintf("%d:%d", c.file.Dev, c.file.Ino)
			if rep, ok := seen[k]; ok {
				extras[rep] = append(extras[rep], c)
				continue
			}
			seen[k] = c
		}
		reps = append(reps, c)
	}
	if len(reps) > 1 {
		col := LinkCollision{
			Target: target,
			Reason: "more than one file would land on this name — none of them is linked",
		}
		for _, c := range sorted {
			col.Alternates = append(col.Alternates, LinkAlternate{
				Source: c.file.Path, Reason: "not linked — the target is shared with another file",
			})
		}
		plan.Collisions = append(plan.Collisions, col)
		return
	}
	rep := reps[0]
	ex := extras[rep]
	entry := LinkEntry{
		Source: rep.file.Path, Target: rep.target, Kind: rep.kind, Title: rep.title,
		OwnerSource: rep.ownerSource,
		SourceDev:   rep.file.Dev, SourceIno: rep.file.Ino, SourceSize: rep.file.Size,
		SourceMtimeUnix: rep.file.ModTimeUnixNano, DepSources: rep.depSources,
	}
	occ := inspectTargetOccupant(target)
	switch occ.state {
	case "symlink", "dir", "other":
		b.addCandidateConflict(rep, ex, occ, "")
		return
	case "regular":
		if occ.known && rep.file.IdentityKnown && rep.file.Dev == occ.dev && rep.file.Ino == occ.ino {
			entry.DestinationDev = occ.dev
			plan.AlreadyLinked = append(plan.AlreadyLinked, entry)
			b.addSamePhysicalAlternates(rep, ex, "same physical file as the linked copy — no second link needed")
			return
		}
		b.addCandidateConflict(rep, ex, occ, "")
		return
	}
	// Absent.
	di := b.destinationDevice(target)
	if di.blocked != "" {
		b.addCandidateConflict(rep, ex, targetOccupant{state: "other"}, di.blocked)
		return
	}
	if !rep.file.IdentityKnown {
		b.addCandidateUnlinkable(rep, ex, target, "the source's device/inode is unavailable, so a same-filesystem link and a safe undo record cannot be proven")
		return
	}
	if !di.known {
		b.addCandidateUnlinkable(rep, ex, target, "the target folder's filesystem cannot be determined, so a same-filesystem link cannot be proven")
		return
	}
	if rep.file.Dev != di.dev {
		b.addCandidateUnlinkable(rep, ex, target, fmt.Sprintf("the source is on device %d but the target folder is on device %d — hardlinks cannot cross filesystems", rep.file.Dev, di.dev))
		return
	}
	entry.DestinationDev = di.dev
	plan.LinksToCreate = append(plan.LinksToCreate, entry)
	b.addSamePhysicalAlternates(rep, ex, "same physical file as the linked copy — no second link needed")
}

// addSamePhysicalAlternates reports extra source paths that are
// the same physical file as a selected non-video representative.
func (b *linkPlanBuilder) addSamePhysicalAlternates(rep *linkCandidate, ex []*linkCandidate, reason string) {
	if len(ex) == 0 {
		return
	}
	col := LinkCollision{
		Target:       rep.target,
		KeeperSource: rep.file.Path,
		Reason:       "several source paths name the same physical file — only one link is needed",
	}
	for _, c := range ex {
		col.Alternates = append(col.Alternates, LinkAlternate{Source: c.file.Path, Reason: reason, SamePhysical: true})
	}
	b.plan.Collisions = append(b.plan.Collisions, col)
}

// addCandidateConflict records a target-conflict for a
// non-video representative and every same-physical extra path
// it blocks.
func (b *linkPlanBuilder) addCandidateConflict(rep *linkCandidate, ex []*linkCandidate, occ targetOccupant, blockedReason string) {
	plan := b.plan
	sources := []string{rep.file.Path}
	for _, c := range ex {
		sources = append(sources, c.file.Path)
	}
	sort.Strings(sources)
	reason := occ.conflictReason()
	occupant := occ.label()
	if blockedReason != "" {
		reason = blockedReason
		occupant = b.destinationDevice(rep.target).blockedLabel
	}
	plan.TargetConflicts = append(plan.TargetConflicts, LinkTargetConflict{
		Target: rep.target, Sources: sources, Occupant: occupant, Reason: reason,
	})
}

// addCandidateUnlinkable records a non-video representative and
// its same-physical extras as unlinkable.
func (b *linkPlanBuilder) addCandidateUnlinkable(rep *linkCandidate, ex []*linkCandidate, target, reason string) {
	plan := b.plan
	plan.Unlinkable = append(plan.Unlinkable, LinkSkip{Path: rep.file.Path, Target: target, Reason: reason})
	for _, c := range ex {
		plan.Unlinkable = append(plan.Unlinkable, LinkSkip{Path: c.file.Path, Target: target, Reason: reason})
	}
}

// linkScanOverlapWarnings computes the scan-root overlap
// warning (SPEC §14.2): the target equals or lies inside a
// configured scan root, or a scan root lies inside the target.
// The copy carries the §14.3 recommendation and the honest
// limits of an exclusion as mitigation.
func linkScanOverlapWarnings(roots []string, targetRoot string) []LinkPlanWarning {
	var overlap []string
	seen := map[string]bool{}
	for _, r := range roots {
		cr := cleanAbs(r)
		if cr == "" || seen[cr] {
			continue
		}
		seen[cr] = true
		if pathWithinRoot(cr, targetRoot) || pathWithinRoot(targetRoot, cr) {
			overlap = append(overlap, cr)
		}
	}
	if len(overlap) == 0 {
		return nil
	}
	return []LinkPlanWarning{{
		Code:  "scan-root-overlap",
		Roots: overlap,
		Message: "the target folder overlaps a scan root (" + strings.Join(overlap, ", ") + "): scans will list every linked file a second time. " +
			"Recommended: put the target folder outside every scan root. If you keep it there, adding the target folder as an \"all\"-scope exclusion in Settings " +
			"hides its duplicate, subtitle and health findings — but scans still walk the folder and the Library still lists its files",
	}}
}
