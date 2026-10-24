package core

// linkapply.go — link-mode execution: LinkPlan / LinkApply /
// LinkUndo (v1.1.0, LINK-LIBRARY-SPEC §§11–13).
//
// PRIME INVARIANT: link Apply and link Undo never touch an
// original. Apply creates directories and hardlinks ONLY, in
// the target tree; Undo removes only target names whose current
// device+inode still equal the identity recorded at Apply
// time. Everything runs under the shared file-operation guard
// (beginFileOp), so link operations mutually exclude scans and
// move-mode Organize, in both directions.
//
// Unlike move mode, Apply is NOT all-or-nothing: a failed link
// leaves every created link independently recorded in a
// manifest written BEFORE the first operation, and a fresh
// preview reclassifies what exists — the workflow converges
// incrementally instead of restarting.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// linkManifest is the on-disk record of one link build, kept
// in <data dir>/link-manifests/. It is an intent-and-progress
// record: written before the first directory or link is
// created, rewritten as each lands.
type linkManifest struct {
	ID          string              `json:"id"`
	CreatedAt   string              `json:"created_at"`
	SourceRoot  string              `json:"source_root"`
	TargetRoot  string              `json:"target_root"`
	CreatedDirs []linkManifestDir   `json:"created_dirs"`
	Links       []linkManifestEntry `json:"links"`
	UndoneAt    string              `json:"undone_at,omitempty"`
}

type linkManifestDir struct {
	Path    string `json:"path"`
	Dev     uint64 `json:"dev,omitempty"`
	Ino     uint64 `json:"ino,omitempty"`
	Created bool   `json:"created"` // false = pre-existing (or not yet created)
}

type linkManifestEntry struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	SourceDev uint64 `json:"source_dev"`
	SourceIno uint64 `json:"source_ino"`
	Done      bool   `json:"done"`
	Undone    bool   `json:"undone"`
}

// LinkManifestSummary is one row of the "past link builds" list.
type LinkManifestSummary struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	SourceRoot  string `json:"source_root"`
	TargetRoot  string `json:"target_root"`
	Links       int    `json:"links"` // confirmed created (Done) at manifest time
	TotalLinks  int    `json:"total_links"`
	UndoneLinks int    `json:"undone_links"`
	CreatedDirs int    `json:"created_dirs"`
	Undone      bool   `json:"undone"`
	Status      string `json:"status"` // applied | partially undone | undone
}

// LinkOutcome is one skipped or failed item in an Apply or Undo
// report: what, where it would have gone, and why not.
type LinkOutcome struct {
	Source   string `json:"source,omitempty"`
	Target   string `json:"target,omitempty"`
	Category string `json:"category"` // collision | already-linked | target-conflict | unlinkable | source-changed | dependency | failed | manifest | invalid | skipped | directory
	Reason   string `json:"reason"`
}

// LinkApplyResult reports what one LinkApply call did.
type LinkApplyResult struct {
	ManifestID    string            `json:"manifest_id,omitempty"`
	Created       int               `json:"created"`
	AlreadyLinked int               `json:"already_linked"`
	CreatedDirs   int               `json:"created_dirs"`
	Partial       bool              `json:"partial"`
	Skipped       []LinkOutcome     `json:"skipped"`
	Failed        []LinkOutcome     `json:"failed"`
	Warnings      []LinkPlanWarning `json:"warnings"`
}

// LinkUndoResult reports what one LinkUndo call did.
type LinkUndoResult struct {
	Removed         int           `json:"removed"`
	AlreadyGone     int           `json:"already_gone"`
	Skipped         []LinkOutcome `json:"skipped"`
	DirsRemoved     int           `json:"dirs_removed"`
	DirsAlreadyGone int           `json:"dirs_already_gone"`
	DirsLeft        []LinkOutcome `json:"dirs_left"`
	Undone          bool          `json:"undone"` // the manifest was marked fully undone
}

// LinkPlan computes a link plan via planLinkLibrary (live
// config + the Service's ranking resolver) and remembers it
// under its ID so Apply executes exactly what was previewed.
// Refuses while a scan or another file operation is running
// (SPEC §13).
func (s *Service) LinkPlan(sourceRoot, targetRoot string) (*LinkPlan, error) {
	s.mu.Lock()
	if s.state.Running {
		s.mu.Unlock()
		return nil, errScanRunning
	}
	if s.fileOpBusy {
		s.mu.Unlock()
		return nil, errFileOpBusy
	}
	cfg := s.cfg
	items := append([]*Item{}, s.results.Items...)
	s.mu.Unlock()
	plan, err := planLinkLibrary(sourceRoot, targetRoot, cfg, s.linkRankResolver(items, cfg))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.linkPlans == nil {
		s.linkPlans = map[string]*LinkPlan{}
	}
	s.linkPlans[plan.ID] = plan
	s.mu.Unlock()
	return plan, nil
}

// linkRankResolver builds the planner's ranking resolver from a
// snapshot of the last scan's items (SPEC §8.3): a scan result
// for the exact path whose size and mtime still match the file
// the link walk just observed wins; otherwise the file is
// probed read-only (scan results and the probe store are NOT
// updated). A probe failure returns nil — filename/size
// fallback.
func (s *Service) linkRankResolver(items []*Item, cfg *Config) linkRankResolver {
	byPath := map[string]*Item{}
	for _, it := range items {
		if it != nil {
			byPath[cleanAbs(it.Path)] = it
		}
	}
	return func(source string, parsed ParsedName, size int64, modTimeUnixNano int64) *Item {
		if it, ok := byPath[cleanAbs(source)]; ok && it.Size == size && it.ModTimeUnix == modTimeUnixNano/1e9 {
			// v1.3.1: a matched scan item that carries NO ranking
			// facts at all (size 0, no height, no bitrate, no
			// duration — the shape the v1.3.0 real-tree export
			// showed for whole packs) does NOT short-circuit:
			// it would make the keeper fall to path order.
			// Fall through to a live probe instead, so the
			// ranking gets real resolution/bitrate to compare.
			// A probe failure still returns nil below, which
			// is today's filename/size fallback.
			if it.Size != 0 || it.Height != 0 || it.Bitrate != 0 || it.Duration != 0 {
				cp := *it
				return &cp
			}
		}
		pr := probeFile(context.Background(), cfg.FFprobePath, source, cfg.ProbeTimeoutSec)
		if pr == nil || !pr.OK {
			return nil
		}
		return &Item{
			Duration: pr.Duration, Bitrate: pr.Bitrate, Height: pr.Height, HDR: pr.HDR,
			ResolutionLabel: resolutionLabel(pr.Width, pr.Height),
		}
	}
}

// LinkApply executes a previously previewed link plan. See the
// file header for the contract; per-item outcomes are in-band.
func (s *Service) LinkApply(planID string) (*LinkApplyResult, error) {
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	defer s.endFileOp()
	s.mu.Lock()
	plan, ok := s.linkPlans[planID]
	if ok {
		delete(s.linkPlans, planID) // consumed at the start (SPEC §11.1)
	}
	cfgRoots := append([]string{}, s.cfg.Roots...)
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown link plan %q — preview the plan again", planID)
	}
	res := &LinkApplyResult{
		Skipped: []LinkOutcome{}, Failed: []LinkOutcome{},
		Warnings: linkScanOverlapWarnings(cfgRoots, plan.TargetRoot),
	}
	if res.Warnings == nil {
		res.Warnings = []LinkPlanWarning{}
	}
	partial := false

	// Root-level revalidation (SPEC §11.1): an in-band result
	// with zero created links and NO manifest.
	if err := checkLinkRoots(plan.SourceRoot, plan.TargetRoot); err != nil {
		for _, e := range plan.LinksToCreate {
			res.Failed = append(res.Failed, LinkOutcome{Source: e.Source, Target: e.Target, Category: "invalid", Reason: err.Error()})
		}
		partial = len(res.Failed) > 0
		appendLinkCollisionOutcomes(plan, res)
		res.Partial = partial
		return res, nil
	}

	// Per-entry revalidation (SPEC §11.2) against the live
	// tree. Entries that fail are reported, never manifested.
	satisfied := map[string]bool{} // sources whose link exists (dependency gate)
	for _, e := range plan.AlreadyLinked {
		satisfied[e.Source] = true
	}
	var work []LinkEntry
	for _, e := range plan.LinksToCreate {
		out := revalidateLinkEntry(plan, e)
		if out == nil {
			work = append(work, e)
			continue
		}
		switch out.Category {
		case "already-linked":
			res.AlreadyLinked++
			satisfied[e.Source] = true
		case "failed":
			res.Failed = append(res.Failed, *out)
			partial = true
		default:
			res.Skipped = append(res.Skipped, *out)
			partial = true
		}
	}
	appendLinkCollisionOutcomes(plan, res)
	if len(work) == 0 {
		res.Partial = partial
		return res, nil
	}

	// Manifest FIRST (SPEC §11.3): every directory Apply may
	// need (shallow first, target root included when missing)
	// and every worklist link, written before anything exists.
	dirSet := map[string]bool{}
	for _, e := range work {
		for d := filepath.Dir(e.Target); ; d = filepath.Dir(d) {
			dirSet[d] = true
			if d == plan.TargetRoot {
				break
			}
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if pathDepth(dirs[i]) != pathDepth(dirs[j]) {
			return pathDepth(dirs[i]) < pathDepth(dirs[j])
		}
		return dirs[i] < dirs[j]
	})
	m := &linkManifest{
		ID:          s.newLinkManifestID(),
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		SourceRoot:  plan.SourceRoot,
		TargetRoot:  plan.TargetRoot,
		CreatedDirs: []linkManifestDir{},
		Links:       []linkManifestEntry{},
	}
	for _, d := range dirs {
		m.CreatedDirs = append(m.CreatedDirs, linkManifestDir{Path: d})
	}
	workIndex := map[string]int{} // target -> manifest link index
	for i, e := range work {
		m.Links = append(m.Links, linkManifestEntry{Source: e.Source, Target: e.Target, SourceDev: e.SourceDev, SourceIno: e.SourceIno})
		workIndex[e.Target] = i
	}
	if err := s.writeLinkManifest(m); err != nil {
		return nil, fmt.Errorf("cannot write the undo manifest, nothing was created: %w", err)
	}
	res.ManifestID = m.ID

	// Directories, one level at a time, shallow first (§11.4).
	blockedDirs := map[string]bool{}
	manifestBroken := false
	for i := range m.CreatedDirs {
		rec := &m.CreatedDirs[i]
		if manifestBroken || blockedDirs[filepath.Dir(rec.Path)] && rec.Path != plan.TargetRoot {
			blockedDirs[rec.Path] = true
			continue
		}
		info, err := os.Lstat(rec.Path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				blockedDirs[rec.Path] = true
				continue
			}
			rec.Dev, rec.Ino, _, _ = fileIdentityForFn(info, rec.Path)
			continue // pre-existing: used, never recorded as created
		}
		if err := os.Mkdir(rec.Path, 0o755); err != nil {
			if os.IsExist(err) {
				if fi, lerr := os.Lstat(rec.Path); lerr == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
					rec.Dev, rec.Ino, _, _ = fileIdentityForFn(fi, rec.Path)
					continue
				}
			}
			blockedDirs[rec.Path] = true
			continue
		}
		rec.Created = true
		if fi, lerr := os.Lstat(rec.Path); lerr == nil {
			rec.Dev, rec.Ino, _, _ = fileIdentityForFn(fi, rec.Path)
		}
		if werr := s.writeLinkManifest(m); werr != nil {
			manifestBroken = true
		}
	}
	entryDirBlocked := func(e LinkEntry) bool {
		for d := filepath.Dir(e.Target); ; d = filepath.Dir(d) {
			if blockedDirs[d] {
				return true
			}
			if d == plan.TargetRoot {
				return false
			}
		}
	}

	// Links (§11.5): videos, then sidecars, then artwork;
	// within a kind, by target path.
	ordered := append([]LinkEntry{}, work...)
	kindRank := func(k string) int {
		switch k {
		case "movie", "episode":
			return 0
		case "sidecar":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if kindRank(ordered[i].Kind) != kindRank(ordered[j].Kind) {
			return kindRank(ordered[i].Kind) < kindRank(ordered[j].Kind)
		}
		return ordered[i].Target < ordered[j].Target
	})
	stopped := manifestBroken
	for _, e := range ordered {
		out := LinkOutcome{Source: e.Source, Target: e.Target}
		if stopped {
			out.Category, out.Reason = "manifest", "creation stopped after the undo manifest could not be updated — preview again to build the rest"
			res.Skipped = append(res.Skipped, out)
			partial = true
			continue
		}
		if entryDirBlocked(e) {
			out.Category, out.Reason = "target-conflict", "a folder in the target path could not be created or is not a real folder"
			res.Failed = append(res.Failed, out)
			partial = true
			continue
		}
		if len(e.DepSources) > 0 {
			depOK := false
			for _, d := range e.DepSources {
				if satisfied[d] {
					depOK = true
					break
				}
			}
			if !depOK {
				out.Category, out.Reason = "dependency", "its video was not linked, so this file was not linked either"
				res.Skipped = append(res.Skipped, out)
				partial = true
				continue
			}
		}
		err := createHardlinkFn(e.Source, e.Target)
		if err == nil {
			// Confirm by identity before counting it (§11.5).
			occ := inspectTargetOccupant(e.Target)
			if occ.state == "regular" && occ.known && occ.dev == e.SourceDev && occ.ino == e.SourceIno {
				m.Links[workIndex[e.Target]].Done = true
				res.Created++
				satisfied[e.Source] = true
				if werr := s.writeLinkManifest(m); werr != nil {
					out.Category, out.Reason = "manifest", "the link was created, but the undo manifest could not be updated: "+werr.Error()
					res.Failed = append(res.Failed, out)
					partial = true
					stopped = true
				}
				continue
			}
			out.Category, out.Reason = "failed", "the target changed during apply and was left alone"
			res.Failed = append(res.Failed, out)
			partial = true
			continue
		}
		switch classifyLinkError(err) {
		case linkErrCrossDevice:
			out.Category, out.Reason = "unlinkable", "the link attempt reported a cross-device error — the target folder is not on the source's filesystem"
			res.Skipped = append(res.Skipped, out)
			partial = true
		case linkErrExists:
			// Race (§10.4): reclassify by the target's identity.
			occ := inspectTargetOccupant(e.Target)
			if occ.state == "regular" && occ.known && occ.dev == e.SourceDev && occ.ino == e.SourceIno {
				res.AlreadyLinked++
				satisfied[e.Source] = true
			} else {
				out.Category, out.Reason = "target-conflict", occ.conflictReason()
				res.Skipped = append(res.Skipped, out)
				partial = true
			}
		case linkErrUnsupported:
			out.Category, out.Reason = "unlinkable", "the destination filesystem does not support hardlinks: "+err.Error()
			res.Skipped = append(res.Skipped, out)
			partial = true
		default:
			out.Category, out.Reason = "failed", err.Error()
			res.Failed = append(res.Failed, out)
			partial = true
		}
	}
	for _, rec := range m.CreatedDirs {
		if rec.Created {
			res.CreatedDirs++
		}
	}
	res.Partial = partial
	return res, nil
}

// appendLinkCollisionOutcomes reports the plan's collision
// alternates and ineligible copies in the Apply report
// (SPEC §11.7): alternates are skipped items that were never
// attempted, labeled as collisions — completeness without
// implying failure. They do not make a build partial.
func appendLinkCollisionOutcomes(plan *LinkPlan, res *LinkApplyResult) {
	for _, col := range plan.Collisions {
		for _, alt := range col.Alternates {
			res.Skipped = append(res.Skipped, LinkOutcome{
				Source: alt.Source, Target: col.Target, Category: "collision", Reason: alt.Reason,
			})
		}
		for _, inel := range col.Ineligible {
			res.Skipped = append(res.Skipped, LinkOutcome{
				Source: inel.Path, Target: col.Target, Category: "unlinkable", Reason: inel.Reason,
			})
		}
	}
}

// revalidateLinkEntry rechecks one planned link against the
// live tree (SPEC §11.2). It returns nil when the entry is safe
// to create, or the outcome to report. Category "already-linked"
// is the benign race: the target already holds the source's
// hardlink.
func revalidateLinkEntry(plan *LinkPlan, e LinkEntry) *LinkOutcome {
	out := func(cat, reason string) *LinkOutcome {
		return &LinkOutcome{Source: e.Source, Target: e.Target, Category: cat, Reason: reason}
	}
	if !pathWithinRoot(plan.SourceRoot, e.Source) {
		return out("source-changed", "the source is no longer inside the source folder — preview again")
	}
	if !pathWithinRoot(plan.TargetRoot, e.Target) {
		return out("source-changed", "the target is no longer inside the target folder — preview again")
	}
	info, err := os.Lstat(e.Source)
	if err != nil {
		return out("source-changed", "the source no longer exists — preview again")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return out("source-changed", "the source is no longer a regular file — preview again")
	}
	dev, ino, _, ok := fileIdentityForFn(info, e.Source)
	if !ok || dev != e.SourceDev || ino != e.SourceIno {
		return out("source-changed", "the source changed since the preview — preview again")
	}
	if info.Size() != e.SourceSize || info.ModTime().UnixNano() != e.SourceMtimeUnix {
		return out("source-changed", "the source changed since the preview — preview again")
	}
	occ := inspectTargetOccupant(e.Target)
	switch occ.state {
	case "regular":
		if occ.known && occ.dev == e.SourceDev && occ.ino == e.SourceIno {
			return out("already-linked", "the target already holds a hardlink to the source")
		}
		return out("target-conflict", occ.conflictReason())
	case "symlink", "dir", "other":
		return out("target-conflict", occ.conflictReason())
	}
	di := destinationDeviceFor(plan.TargetRoot, e.Target)
	if di.blocked != "" {
		return out("target-conflict", di.blocked)
	}
	if !di.known || di.dev != e.SourceDev {
		return out("unlinkable", "the target folder is no longer on the source's filesystem — preview again")
	}
	return nil
}

// LinkUndo reverses one link build from its on-disk manifest
// (SPEC §12). A manifest entry is removed ONLY when the
// target's current device+inode still equal the identity
// recorded at Apply time; anything else is skipped, reported,
// and left exactly where it is — and the manifest stays active
// for retry. Created directories are removed deepest first,
// only when empty.
func (s *Service) LinkUndo(manifestID string) (*LinkUndoResult, error) {
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	defer s.endFileOp()
	m, err := s.loadLinkManifest(manifestID)
	if err != nil {
		return nil, err
	}
	if m.UndoneAt != "" {
		return nil, fmt.Errorf("link build %s was already undone at %s", m.ID, m.UndoneAt)
	}
	res := &LinkUndoResult{Skipped: []LinkOutcome{}, DirsLeft: []LinkOutcome{}}
	unresolved := false
	skip := func(e *linkManifestEntry, reason string) {
		res.Skipped = append(res.Skipped, LinkOutcome{Source: e.Source, Target: e.Target, Category: "skipped", Reason: reason})
		unresolved = true
	}
	for i := len(m.Links) - 1; i >= 0; i-- {
		e := &m.Links[i]
		if e.Undone {
			continue
		}
		// The manifest is data, not authority (v0.4.1
		// confinement, applied to links): recorded paths must
		// sit inside the manifest's own roots, the roots must
		// be disjoint, and the recorded identity must be real.
		if !pathWithinRoot(m.SourceRoot, e.Source) || !pathWithinRoot(m.TargetRoot, e.Target) ||
			linkRootsOverlap(m.SourceRoot, m.TargetRoot) || e.SourceDev == 0 || e.SourceIno == 0 {
			skip(e, "the recorded paths fail validation — left in place")
			continue
		}
		info, err := os.Lstat(e.Target)
		if os.IsNotExist(err) {
			e.Undone = true
			res.AlreadyGone++
			_ = s.writeLinkManifest(m)
			continue
		}
		if err != nil {
			skip(e, "cannot check the target: "+err.Error())
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			skip(e, "the target is no longer a regular file — left in place")
			continue
		}
		dev, ino, _, ok := fileIdentityForFn(info, e.Target)
		if !ok {
			skip(e, "the target's identity cannot be proven — left in place")
			continue
		}
		if dev != e.SourceDev || ino != e.SourceIno {
			skip(e, "the target was replaced and no longer points to the recorded file — left in place")
			continue
		}
		if err := os.Remove(e.Target); err != nil {
			skip(e, err.Error())
			continue
		}
		e.Undone = true
		res.Removed++
		_ = s.writeLinkManifest(m)
	}
	// Created directories, deepest first, only when empty
	// (os.Remove enforces the empty part). A directory left
	// behind never blocks marking the build undone (§12.3).
	dirs := append([]linkManifestDir{}, m.CreatedDirs...)
	sort.Slice(dirs, func(i, j int) bool {
		if pathDepth(dirs[i].Path) != pathDepth(dirs[j].Path) {
			return pathDepth(dirs[i].Path) > pathDepth(dirs[j].Path)
		}
		return dirs[i].Path > dirs[j].Path
	})
	for _, rec := range dirs {
		if !rec.Created {
			continue
		}
		out := LinkOutcome{Target: rec.Path, Category: "directory"}
		if rec.Path != m.TargetRoot && !pathWithinRoot(m.TargetRoot, rec.Path) {
			out.Reason = "the recorded folder is outside the target folder — left in place"
			res.DirsLeft = append(res.DirsLeft, out)
			continue
		}
		info, err := os.Lstat(rec.Path)
		if os.IsNotExist(err) {
			res.DirsAlreadyGone++
			continue
		}
		if err != nil {
			out.Reason = "cannot check the folder: " + err.Error()
			res.DirsLeft = append(res.DirsLeft, out)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			out.Reason = "no longer a real folder — left in place"
			res.DirsLeft = append(res.DirsLeft, out)
			continue
		}
		if rec.Dev != 0 || rec.Ino != 0 {
			if dev, ino, _, ok := fileIdentityForFn(info, rec.Path); ok && (dev != rec.Dev || ino != rec.Ino) {
				out.Reason = "the folder was replaced — left in place"
				res.DirsLeft = append(res.DirsLeft, out)
				continue
			}
		}
		if err := os.Remove(rec.Path); err != nil {
			out.Reason = "not empty or not removable — left in place"
			res.DirsLeft = append(res.DirsLeft, out)
			continue
		}
		res.DirsRemoved++
	}
	if !unresolved {
		m.UndoneAt = time.Now().UTC().Format(time.RFC3339)
		res.Undone = true
	}
	if err := s.writeLinkManifest(m); err != nil {
		return res, fmt.Errorf("links were removed, but the manifest could not be updated: %w", err)
	}
	return res, nil
}

// LinkManifests lists every link-build manifest in the data
// dir, newest first. Fully undone builds stay listed (marked
// undone) as history, mirroring move-mode manifests.
func (s *Service) LinkManifests() ([]LinkManifestSummary, error) {
	dir := s.linkManifestsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []LinkManifestSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []LinkManifestSummary{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m linkManifest
		if json.Unmarshal(b, &m) != nil || m.ID == "" {
			continue
		}
		sum := LinkManifestSummary{
			ID: m.ID, CreatedAt: m.CreatedAt, SourceRoot: m.SourceRoot, TargetRoot: m.TargetRoot,
			TotalLinks: len(m.Links), Undone: m.UndoneAt != "",
		}
		for _, l := range m.Links {
			if l.Done {
				sum.Links++
			}
			if l.Undone {
				sum.UndoneLinks++
			}
		}
		for _, d := range m.CreatedDirs {
			if d.Created {
				sum.CreatedDirs++
			}
		}
		switch {
		case sum.Undone:
			sum.Status = "undone"
		case sum.UndoneLinks > 0:
			sum.Status = "partially undone"
		default:
			sum.Status = "applied"
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// linkManifestsDir is where link-build manifests live in the
// data dir, beside organize-manifests/ and subtitle-manifests/.
func (s *Service) linkManifestsDir() string {
	return filepath.Join(s.dataDir, "link-manifests")
}

// newLinkManifestID mints a manifest ID ("link-" + timestamp),
// checking the disk so two applies in the same second never
// share a manifest.
func (s *Service) newLinkManifestID() string {
	base := "link-" + time.Now().UTC().Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(s.linkManifestsDir(), id+".json")); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// writeLinkManifest persists a link manifest (temp file +
// rename, so a crash never leaves a half-written undo record).
func (s *Service) writeLinkManifest(m *linkManifest) error {
	dir := s.linkManifestsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(dir, m.ID+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// loadLinkManifest reads one link manifest from disk by ID. The
// ID is validated to be a bare file name — it is never allowed
// to become a path outside the manifests folder.
func (s *Service) loadLinkManifest(id string) (*linkManifest, error) {
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid manifest id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(s.linkManifestsDir(), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("manifest %q not found", id)
	}
	var m linkManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %q is unreadable: %w", id, err)
	}
	return &m, nil
}
