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
	Kind   string `json:"kind"` // movie | episode | sidecar | artwork | extra
	Title  string `json:"title"`
	// NumberAssigned (v1.4.0): this episode's Season 00
	// number was CREATED by the planner (the file named no
	// episode number) — flagged here, badged in the UI and
	// listed in the diagnostics export, so an invented
	// number is never silent. Only ever true for episodes.
	NumberAssigned bool `json:"number_assigned,omitempty"`
	// NoYear (v1.4.0): this movie linked without a year in
	// its name (Title/Title.ext, no "(Year)"), accepted on
	// its folder context. Flagged and badged like
	// NumberAssigned. Only ever true for movies.
	NoYear bool `json:"no_year,omitempty"`
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

// LinkParsedFact is what the release-name parser extracted
// from one file name (v1.3.0): the parser's own output, carried
// on outcome records so the diagnostics export can show what
// WAS found — a zero year, an empty title or an empty episode
// list means the parser extracted nothing there.
type LinkParsedFact struct {
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	NormTitle string `json:"norm_title,omitempty"`
	Year      int    `json:"year"`
	Season    int    `json:"season"`
	Episodes  []int  `json:"episodes"`
	Edition   string `json:"edition,omitempty"`
	Special   string `json:"special,omitempty"`
}

// LinkProbeFact is the ranking evidence for one collision
// source (v1.3.0): the probe/scan facts the duplicate ranking
// consumed. Where no probe ran for the file (an occupied
// target's copies, same-physical extras), only the filename
// resolution label and the size are set — exactly the fallback
// ranking's inputs. Zero/absent means unknown.
type LinkProbeFact struct {
	Resolution string  `json:"resolution,omitempty"`
	Height     int     `json:"height,omitempty"`
	HDR        bool    `json:"hdr"`
	Bitrate    int64   `json:"bitrate,omitempty"`
	Size       int64   `json:"size"`
	Duration   float64 `json:"duration_seconds,omitempty"`
}

func linkParsedFact(pn ParsedName) *LinkParsedFact {
	eps := pn.Episodes
	if eps == nil {
		eps = []int{}
	}
	return &LinkParsedFact{
		Kind: pn.Kind, Title: pn.Title, NormTitle: pn.NormTitle,
		Year: pn.Year, Season: pn.Season, Episodes: eps,
		Edition: pn.Edition, Special: pn.Special,
	}
}

func linkProbeFactFromItem(it *Item) *LinkProbeFact {
	if it == nil {
		return nil
	}
	return &LinkProbeFact{
		Resolution: it.ResolutionLabel, Height: it.Height, HDR: it.HDR,
		Bitrate: it.Bitrate, Size: it.Size, Duration: it.Duration,
	}
}

// linkProbeFactFromFile is the filename-level fact set for a
// source no ranking probe covered.
func linkProbeFactFromFile(f orgFile, resolution string) *LinkProbeFact {
	return &LinkProbeFact{Resolution: resolution, Size: f.Size}
}

// LinkSkip is one file deliberately not linked, with the reason.
type LinkSkip struct {
	Path          string   `json:"path"`
	Target        string   `json:"target,omitempty"`
	Reason        string   `json:"reason"`
	AttachedFiles []string `json:"attached_files,omitempty"`
	// Diagnostic facts (v1.3.0, additive — populated where the
	// planner has them, consumed by the link-problems export;
	// no computed outcome reads them): what the parser
	// extracted, the ranking evidence, the file's extension
	// and size, and the device facts the feasibility proof
	// used.
	Parsed           *LinkParsedFact `json:"parsed,omitempty"`
	Probe            *LinkProbeFact  `json:"probe,omitempty"`
	Ext              string          `json:"ext,omitempty"`
	Size             int64           `json:"size,omitempty"`
	SourceDev        uint64          `json:"source_dev,omitempty"`
	SourceIno        uint64          `json:"source_ino,omitempty"`
	IdentityKnown    bool            `json:"identity_known,omitempty"`
	DestinationDev   uint64          `json:"destination_dev,omitempty"`
	DestinationKnown bool            `json:"destination_known,omitempty"`
}

// LinkAlternate is one source in a collision group that was not
// selected for the target.
type LinkAlternate struct {
	Source        string   `json:"source"`
	Reason        string   `json:"reason"`
	SamePhysical  bool     `json:"same_physical,omitempty"`
	AttachedFiles []string `json:"attached_files,omitempty"`
	// Size is the walk-measured size of this alternate's own
	// file (v1.3.4, additive): carried so the link-plan UI can
	// flag copies that measure 0 bytes with its 0-BYTE badge.
	// Always serialized — for an alternate the walk always
	// measured the file, so 0 is a measurement, not an
	// absence. No computed outcome reads it.
	Size int64 `json:"size"`
	// Diagnostic facts (v1.3.0, additive — see LinkSkip).
	Parsed *LinkParsedFact `json:"parsed,omitempty"`
	Probe  *LinkProbeFact  `json:"probe,omitempty"`
}

// LinkTargetConflict is one occupied target link mode will
// never touch, with the candidate sources it blocks.
type LinkTargetConflict struct {
	Target        string   `json:"target"`
	Sources       []string `json:"sources"`
	Occupant      string   `json:"occupant"` // regular file | directory | symbolic link | other | unknown identity
	Reason        string   `json:"reason"`
	AttachedFiles []string `json:"attached_files,omitempty"`
	// Occupant facts (v1.3.0, additive): the occupying file's
	// size and storage identity where the planner read them
	// (regular files), for the diagnostics export.
	OccupantSize          int64  `json:"occupant_size,omitempty"`
	OccupantSizeKnown     bool   `json:"occupant_size_known,omitempty"`
	OccupantDev           uint64 `json:"occupant_dev,omitempty"`
	OccupantIno           uint64 `json:"occupant_ino,omitempty"`
	OccupantIdentityKnown bool   `json:"occupant_identity_known,omitempty"`
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
	// The keeper's diagnostic facts (v1.3.0, additive — see
	// LinkSkip): what the parser extracted from the keeper's
	// name and the ranking evidence it won with.
	KeeperParsed *LinkParsedFact `json:"keeper_parsed,omitempty"`
	KeeperProbe  *LinkProbeFact  `json:"keeper_probe,omitempty"`
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
	// Movies / Episodes / Sidecars / Artwork / Extras count
	// the files within links-to-create only.
	Movies   int `json:"movies"`
	Episodes int `json:"episodes"`
	Sidecars int `json:"sidecars"`
	Artwork  int `json:"artwork"`
	Extras   int `json:"extras"`
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
	// size/sizeKnown carry the occupying file's size (regular
	// files only, v1.3.0) for the diagnostics export.
	size      int64
	sizeKnown bool
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
	return targetOccupant{state: "regular", dev: dev, ino: ino, known: ok, size: info.Size(), sizeKnown: true}
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
// classification. v1.4.0: extras ride the same shape (kind
// "extra") — an extra's owner is a WORK (its folder), not one
// video, so it carries no dependency: it links on the work's
// identification, whether or not any one copy of the work did.
type linkCandidate struct {
	file        orgFile
	target      string
	kind        string // sidecar | artwork | extra
	title       string
	ownerSource string
	depSources  []string
}

// extraItem is one parser-classified extra held back from the
// video flow (v1.4.0) while its owning work is resolved from
// the finished video targets.
type extraItem struct {
	file   orgFile
	parsed ParsedName
}

// pendingSpecial is one video held back from the
// not-identified list (v1.4.0) while the plan-wide Season 00
// numbering pass decides it, with the reason it would carry
// if it is not numbered after all.
type pendingSpecial struct {
	vp     *videoPlan
	reason string
}

type linkPlanBuilder struct {
	plan      *LinkPlan
	cfg       *Config
	resolve   linkRankResolver
	lv        map[*videoPlan]*linkVideo
	destCache map[string]destDeviceInfo
	pending   []pendingAttach
	// v1.4.0 per-video honesty flags and display titles,
	// consumed by makeEntry: assigned = the Season 00 number
	// was created by the numbering pass; noYear = the movie
	// linked without a year; cleanTitle overrides the entry
	// title where the planner cleaned it (year-less movies).
	assigned   map[*videoPlan]bool
	noYear     map[*videoPlan]bool
	cleanTitle map[*videoPlan]string
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
		assigned: map[*videoPlan]bool{}, noYear: map[*videoPlan]bool{},
		cleanTitle: map[*videoPlan]string{},
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
	var extras []extraItem
	var specials []pendingSpecial
	for _, files := range filesByDir {
		for _, f := range files {
			if !mediaExts[f.Ext] {
				continue
			}
			vp := &videoPlan{File: f, Parsed: parseName(f.Path, absSource)}
			// v1.4.0: extras divert OUT of the video flow when
			// the link_extras switch is on — they are not
			// videos (they must never own sidecars, win a
			// keeper contest, or count as a folder's work), so
			// they wait for the extras pass below, which
			// resolves each one's owning work from the
			// finished video targets. Samples never divert:
			// they stay exactly where they are today.
			if vp.Parsed.Kind == "extra" && cfg.LinkExtras && !isSamplePath(f, absSource) {
				extras = append(extras, extraItem{file: f, parsed: vp.Parsed})
				continue
			}
			lv := &linkVideo{vp: vp}
			b.lv[vp] = lv
			target, targetDir, targetStem, workDir, reason := buildVideoTarget(absTarget, f, vp.Parsed)
			if reason == "" && !pathWithinRoot(absTarget, target) {
				// Defensive confinement (SPEC §5.4): sanitization
				// should make this unreachable.
				reason = "planned target would escape the target folder — left in place"
			}
			if reason != "" && cfg.AssignSpecialNumbers && !isSamplePath(f, absSource) {
				// v1.4.0 Ruling 1: a special-signalling file
				// with an identified series is held for the
				// plan-wide Season 00 numbering pass instead
				// of being filed as unidentified here —
				// numbering needs every special of the series
				// in the plan, which only the whole walk knows.
				if _, _, ok := specialSeriesFor(f, vp.Parsed, absSource); ok {
					vp.Status = "special-pending"
					specials = append(specials, pendingSpecial{vp: vp, reason: reason})
					videos = append(videos, vp)
					videosByDir[f.Dir] = append(videosByDir[f.Dir], vp)
					continue
				}
			}
			if reason == "no year found" && cfg.AllowYearlessMovies && !isSamplePath(f, absSource) {
				// v1.4.0 Ruling 2: a year-less movie in a movie
				// context links as Title/Title.ext. The target
				// is built here, from the planner-cleaned
				// title; everything downstream (grouping,
				// collisions, occupants) treats it as an
				// ordinary movie candidate.
				if title, ok := yearlessMovieTitle(f, vp.Parsed, absSource); ok {
					if folder := sanitizeSegment(title); folder != "" {
						vp.TargetDir = filepath.Join(absTarget, folder)
						vp.TargetStem = folder
						vp.WorkDir = vp.TargetDir
						vp.Target = filepath.Join(vp.TargetDir, folder+f.ExtOrig)
						vp.Status = "candidate"
						b.noYear[vp] = true
						b.cleanTitle[vp] = title
						reason = ""
					}
				}
			}
			if reason != "" {
				vp.Status = "unidentified"
				plan.NotIdentified = append(plan.NotIdentified, LinkSkip{
					Path: f.Path, Reason: reason,
					Parsed: linkParsedFact(vp.Parsed), Ext: f.Ext, Size: f.Size,
				})
				lv.outKind, lv.outA = "notidentified", len(plan.NotIdentified)-1
			} else if vp.Status != "candidate" {
				vp.Target, vp.TargetDir, vp.TargetStem, vp.WorkDir = target, targetDir, targetStem, workDir
				vp.Status = "candidate"
			}
			videos = append(videos, vp)
			videosByDir[f.Dir] = append(videosByDir[f.Dir], vp)
		}
	}

	// ---- Pass 1.5 (v1.4.0): the Season 00 numbering pass.
	// Each held special gets a CREATED episode number:
	// Season 00, numbered 1..N per series in sorted
	// source-path order across ALL of the series' qualifying
	// specials in this plan — one numbering space per series,
	// deterministic for a given tree. The synthesized parse
	// goes through the ordinary episode branch of
	// buildVideoTarget, so an assigned special is a normal
	// episode candidate from here on (grouping, collisions,
	// sidecars), flagged number_assigned on its entry. ----
	b.assignSpecialNumbers(specials, absTarget)

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

	// ---- Pass 2.5 (v1.4.0): extras find their owning work.
	// An extra links only when exactly one work owns it: the
	// deepest folder on the extra's own ancestor chain (its
	// own folder included) whose SUBTREE holds target-built
	// videos of exactly one work decides — deeper folders win
	// over shallower ones, and a folder whose subtree mixes
	// two works refuses rather than guesses. The link target
	// is <that work's folder>/extras/<cleaned extra title>,
	// the Jellyfin-documented catch-all extras folder, and
	// the candidate rides the ordinary Pass 4 classification
	// (occupants, same-target groups, feasibility) — two
	// extras cleaning to one name are a collision there,
	// never a keeper contest. An extra with no unambiguous
	// owner stays not-identified with today's reason. ----
	var cands []*linkCandidate
	cands = append(cands, b.planExtras(extras, videos, absSource, absTarget)...)

	// ---- Pass 3: sidecars + folder artwork for selected
	// videos only; everything else is attached to its owner's
	// outcome or skipped in the move planner's shapes. ----
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
		case "extra":
			st.Extras++
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
	e := LinkEntry{
		Source: vp.File.Path, Target: vp.Target, Kind: vp.Parsed.Kind, Title: vp.Parsed.Title,
		SourceDev: vp.File.Dev, SourceIno: vp.File.Ino, SourceSize: vp.File.Size,
		SourceMtimeUnix: vp.File.ModTimeUnixNano, DestinationDev: destDev,
	}
	// v1.4.0 honesty flags: a created Season 00 number and a
	// year-less acceptance are planner inventions, and the
	// entry says so wherever it travels (UI, export, apply).
	if b.assigned[vp] {
		e.NumberAssigned = true
	}
	if b.noYear[vp] {
		e.NoYear = true
	}
	if t, ok := b.cleanTitle[vp]; ok {
		e.Title = t
	}
	return e
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
		OccupantSize: occ.size, OccupantSizeKnown: occ.sizeKnown,
		OccupantDev: occ.dev, OccupantIno: occ.ino, OccupantIdentityKnown: occ.known,
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
		KeeperParsed: linkParsedFact(linked.Parsed),
		KeeperProbe:  linkProbeFactFromFile(linked.File, linked.Parsed.Resolution),
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
		col.Alternates = append(col.Alternates, LinkAlternate{
			Source: vp.File.Path, Reason: reason, SamePhysical: same, Size: vp.File.Size,
			Parsed: linkParsedFact(vp.Parsed), Probe: linkProbeFactFromFile(vp.File, vp.Parsed.Resolution),
		})
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
			"not linked — this collision could not be resolved automatically", "", "", nil)
		return
	}
	// 2. Split-file guard: parts are segments of one work,
	// never interchangeable copies.
	if len(group) > 1 {
		for _, vp := range group {
			if vp.Parsed.Special == "split-file" {
				b.addUnresolvedCollision(target, group,
					"split-file parts are segments of one work, not copies of each other — none of them is linked",
					"not linked — split-file part", "", "", nil)
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
				KeeperParsed: linkParsedFact(rep.Parsed),
				KeeperProbe:  linkProbeFactFromFile(rep.File, rep.Parsed.Resolution),
			}
			for _, ex := range extras[rep] {
				col.Alternates = append(col.Alternates, LinkAlternate{
					Source: ex.File.Path, SamePhysical: true, Size: ex.File.Size,
					Reason: "same physical file as the keeper — no second link needed",
					Parsed: linkParsedFact(ex.Parsed), Probe: linkProbeFactFromFile(ex.File, ex.Parsed.Resolution),
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
			status, note, items)
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

	// The ranking evidence for one candidate (v1.3.0 facts):
	// the resolved ranking item when one exists, else the
	// filename-level inputs.
	probeFor := func(vp *videoPlan) *LinkProbeFact {
		if it := items[vp]; it != nil {
			return linkProbeFactFromItem(it)
		}
		return linkProbeFactFromFile(vp.File, vp.Parsed.Resolution)
	}
	colIdx := len(plan.Collisions)
	col := LinkCollision{
		Target:         target,
		KeeperSource:   keeper.File.Path,
		DurationStatus: status,
		DurationNote:   note,
		RankingNote:    rankingNote(items[keeper], anyFallback),
		KeeperParsed:   linkParsedFact(keeper.Parsed),
		KeeperProbe:    probeFor(keeper),
	}
	addAlt := func(vp *videoPlan, reason string, same bool, probe *LinkProbeFact) {
		col.Alternates = append(col.Alternates, LinkAlternate{
			Source: vp.File.Path, Reason: reason, SamePhysical: same, Size: vp.File.Size,
			Parsed: linkParsedFact(vp.Parsed), Probe: probe,
		})
		vp.Status = "alternate"
		lv := b.lv[vp]
		lv.outKind, lv.outA, lv.outB = "alternate", colIdx, len(col.Alternates)-1
	}
	for _, rep := range feasible {
		if rep == keeper {
			continue
		}
		addAlt(rep, "another copy of the same work — the keeper above was linked instead", false, probeFor(rep))
		for _, ex := range extras[rep] {
			addAlt(ex, "same physical file as an unlinked copy", true, probeFor(rep))
		}
	}
	for _, ex := range extras[keeper] {
		addAlt(ex, "same physical file as the keeper — no second link needed", true, probeFor(keeper))
	}
	for _, rep := range reps {
		reason, bad := ineligible[rep]
		if !bad {
			continue
		}
		col.Ineligible = append(col.Ineligible, LinkSkip{
			Path: rep.File.Path, Target: target, Reason: reason,
			Parsed: linkParsedFact(rep.Parsed), Probe: linkProbeFactFromItem(items[rep]),
			Ext: rep.File.Ext, Size: rep.File.Size,
			SourceDev: rep.File.Dev, SourceIno: rep.File.Ino, IdentityKnown: rep.File.IdentityKnown,
		})
		rep.Status = "ineligible"
		lv := b.lv[rep]
		lv.outKind, lv.outA, lv.outB = "ineligible", colIdx, len(col.Ineligible)-1
		for _, ex := range extras[rep] {
			col.Ineligible = append(col.Ineligible, LinkSkip{
				Path: ex.File.Path, Target: target, Reason: reason,
				Parsed: linkParsedFact(ex.Parsed), Probe: linkProbeFactFromItem(items[rep]),
				Ext: ex.File.Ext, Size: ex.File.Size,
				SourceDev: ex.File.Dev, SourceIno: ex.File.Ino, IdentityKnown: ex.File.IdentityKnown,
			})
			ex.Status = "ineligible"
			lvx := b.lv[ex]
			lvx.outKind, lvx.outA, lvx.outB = "ineligible", colIdx, len(col.Ineligible)-1
		}
	}
	plan.Collisions = append(plan.Collisions, col)
}

// addUnresolvedCollision records a collision group with no
// keeper: every source becomes an alternate with the given
// per-source reason. items carries the ranking metadata when
// the caller computed it (the duration-gate path) so the
// export can show the probe facts behind the refusal; nil
// falls back to filename-level facts.
func (b *linkPlanBuilder) addUnresolvedCollision(target string, group []*videoPlan, reason, altReason, durStatus, durNote string, items map[*videoPlan]*Item) {
	plan := b.plan
	colIdx := len(plan.Collisions)
	col := LinkCollision{Target: target, Reason: reason, DurationStatus: durStatus, DurationNote: durNote}
	for _, vp := range sortedVideosByPath(group) {
		probe := linkProbeFactFromFile(vp.File, vp.Parsed.Resolution)
		if it := items[vp]; it != nil {
			probe = linkProbeFactFromItem(it)
		}
		col.Alternates = append(col.Alternates, LinkAlternate{
			Source: vp.File.Path, Reason: altReason, Size: vp.File.Size,
			Parsed: linkParsedFact(vp.Parsed), Probe: probe,
		})
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
	di := b.destinationDevice(target)
	plan.Unlinkable = append(plan.Unlinkable, LinkSkip{
		Path: vp.File.Path, Target: target, Reason: reason,
		Parsed: linkParsedFact(vp.Parsed), Ext: vp.File.Ext, Size: vp.File.Size,
		SourceDev: vp.File.Dev, SourceIno: vp.File.Ino, IdentityKnown: vp.File.IdentityKnown,
		DestinationDev: di.dev, DestinationKnown: di.known,
	})
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
// filename/size fallback otherwise. Parsed identity fields
// always win; the size merges probe-first (v1.3.1): a probe or
// scan size already on the item stands, the walk-observed size
// fills a zero size, and a still-zero size gets one fresh stat
// at decision time — the v1.3.0 real-tree export showed whole
// packs ranking on 0 bytes because the walk stat itself came
// back zero, so the ranking must never consume a zero it can
// still replace with a real number. The resolution label falls
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
			ResolutionLabel: vp.Parsed.Resolution, Size: rankingSize(vp),
		}, true
	}
	cp := *it
	cp.Path, cp.Name = vp.File.Path, vp.File.Name
	cp.Kind, cp.Title, cp.NormTitle = vp.Parsed.Kind, vp.Parsed.Title, vp.Parsed.NormTitle
	cp.Year, cp.Season, cp.Episodes, cp.Edition = vp.Parsed.Year, vp.Parsed.Season, vp.Parsed.Episodes, vp.Parsed.Edition
	if cp.Size == 0 {
		cp.Size = rankingSize(vp)
	}
	if cp.Height == 0 {
		cp.ResolutionLabel = vp.Parsed.Resolution
	}
	return &cp, false
}

// rankingSize is the size a collision ranking may consume for
// one source: the walk-observed size when it is real, else one
// fresh stat of the file itself (a walk stat can come back zero
// for a file the filesystem has not yet materialized — the
// v1.3.0 export's 0-byte keepers — and the stat at decision
// time is the same truth source retried). A genuinely empty or
// unstattable file keeps 0, which is its true size.
func rankingSize(vp *videoPlan) int64 {
	if vp.File.Size > 0 {
		return vp.File.Size
	}
	if info, err := os.Stat(vp.File.Path); err == nil && info.Size() > 0 {
		return info.Size()
	}
	return vp.File.Size
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
					Source: c.file.Path, Size: c.file.Size,
					Reason: "not linked — a video target (" + src + ") takes this name",
					Probe:  linkProbeFactFromFile(c.file, ""),
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
				Source: c.file.Path, Reason: "not linked — the target is shared with another file", Size: c.file.Size,
				Probe: linkProbeFactFromFile(c.file, ""),
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
		KeeperProbe:  linkProbeFactFromFile(rep.file, ""),
	}
	for _, c := range ex {
		col.Alternates = append(col.Alternates, LinkAlternate{
			Source: c.file.Path, Reason: reason, SamePhysical: true, Size: c.file.Size,
			Probe: linkProbeFactFromFile(c.file, ""),
		})
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
		OccupantSize: occ.size, OccupantSizeKnown: occ.sizeKnown,
		OccupantDev: occ.dev, OccupantIno: occ.ino, OccupantIdentityKnown: occ.known,
	})
}

// addCandidateUnlinkable records a non-video representative and
// its same-physical extras as unlinkable.
func (b *linkPlanBuilder) addCandidateUnlinkable(rep *linkCandidate, ex []*linkCandidate, target, reason string) {
	plan := b.plan
	di := b.destinationDevice(target)
	skipFor := func(f orgFile) LinkSkip {
		return LinkSkip{
			Path: f.Path, Target: target, Reason: reason,
			Ext: f.Ext, Size: f.Size,
			SourceDev: f.Dev, SourceIno: f.Ino, IdentityKnown: f.IdentityKnown,
			DestinationDev: di.dev, DestinationKnown: di.known,
		}
	}
	plan.Unlinkable = append(plan.Unlinkable, skipFor(rep.file))
	for _, c := range ex {
		plan.Unlinkable = append(plan.Unlinkable, skipFor(c.file))
	}
}

// ---- v1.4.0: extras linking, assigned special numbers,
// year-less movies. The three diversions share two guards:
// the sample gate (a sample is junk, never bonus content and
// never a special) and the toggles on Config, each of which
// restores the pre-1.4.0 outcome exactly when off. ----

// isSamplePath is the sample gate every v1.4.0 diversion
// checks first: a file is a sample when any folder on its
// path is named exactly "sample"/"samples", or its stem
// carries a sample marker ("-sample", ".sample", " sample",
// or a leading "sample-"/"sample."/"sample "). These are the
// signals that already route real-tree samples to the extras
// reason today: the parser's extras suffix list contains
// "-sample", and sample folders are the standard pack layout
// for preview clips. (The scanner's health pattern list is
// deliberately NOT reused: it also matches trailers and
// featurettes, which are genuine, linkable extras.)
func isSamplePath(f orgFile, root string) bool {
	for _, part := range dirPartsOf(f, root) {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "sample", "samples":
			return true
		}
	}
	stem := strings.ToLower(f.Stem)
	if strings.Contains(stem, "-sample") || strings.Contains(stem, ".sample") || strings.Contains(stem, " sample") {
		return true
	}
	return strings.HasPrefix(stem, "sample-") || strings.HasPrefix(stem, "sample.") || strings.HasPrefix(stem, "sample ")
}

// dirPartsOf returns f's folder parts relative to the source
// root (empty when f sits in the root itself).
func dirPartsOf(f orgFile, root string) []string {
	rel, err := filepath.Rel(root, f.Dir)
	if err != nil || rel == "." {
		return nil
	}
	return strings.Split(rel, string(os.PathSeparator))
}

// folderTitleClean cleans one folder name to a display title
// the way the parser cleans folder context (parser.go's
// folder-title path): bracket blocks out, parenthesized
// release furniture out, release tags and years dropped.
func folderTitleClean(part string) string {
	var tags []string
	return strings.Join(cleanTokens(stripParenFurniture(reBrack.ReplaceAllString(part, " ")), &tags), " ")
}

// cleanedNameTitle cleans a filename stem to a display title
// with every bracket/parenthesis block removed first (group
// and release furniture), then the parser's movie-path token
// cleaning (years kept — a year-less movie has none to keep,
// and a year that is part of a title survives).
func cleanedNameTitle(stem string) string {
	s := reParen.ReplaceAllString(reBrack.ReplaceAllString(stem, " "), " ")
	var tags []string
	return strings.Join(cleanTokensMode(s, &tags, false), " ")
}

// specialSeriesFor decides whether a file the layout builder
// could not place is a Ruling 1 special: a non-extra,
// non-sample TV file with an identified series but no episode
// number, signalling special-ness. It returns the series
// identity the created number belongs to. Two routes:
//
//   - episode parses: the series is the parse's own title,
//     and any signal qualifies (see hasSpecialSignal).
//   - movie parses inside a series tree: an "OVA" folder
//     part claims the file for the series named by the
//     folder directly above it (the Yu Yu Hakusho shape:
//     "Yu Yu Hakusho/OVA/…" files whose names parse as
//     movies). The year-less movie path never sees these —
//     this check runs first in the planner.
//
// A fractional parse is refused: its number exists but is
// fractional — episode intent that merely failed a pattern,
// which the ruling explicitly leaves unidentified.
func specialSeriesFor(f orgFile, pn ParsedName, root string) (title string, year int, ok bool) {
	if pn.Special == "fractional" {
		return "", 0, false
	}
	parts := dirPartsOf(f, root)
	if pn.Kind == "movie" {
		for i, part := range parts {
			if strings.ToLower(strings.TrimSpace(part)) == "ova" && i > 0 {
				if t := folderTitleClean(parts[i-1]); t != "" {
					return t, 0, true
				}
			}
		}
		return "", 0, false
	}
	if pn.Kind != "episode" || pn.Title == "" || len(pn.Episodes) != 0 {
		return "", 0, false
	}
	if hasSpecialSignal(f, pn, parts) {
		return pn.Title, pn.Year, true
	}
	return "", 0, false
}

// hasSpecialSignal reports whether an episode parse with no
// episode number signals special-ness by any of the ruling's
// markers: an explicit Season 00 parse, an "S00" token in the
// name, the word "Special"/"Specials" in the name or a folder
// name, a Specials or OVA folder, an OVA token in the name,
// or "making of" in the name or a folder name.
func hasSpecialSignal(f orgFile, pn ParsedName, parts []string) bool {
	if pn.Season == 0 {
		return true
	}
	for _, tok := range looseTokens(f.Stem) {
		switch tok {
		case "s00", "special", "specials", "ova":
			return true
		}
	}
	if hasMakingOf(f.Stem) {
		return true
	}
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "specials", "ova":
			return true
		}
		for _, tok := range looseTokens(part) {
			if tok == "special" || tok == "specials" {
				return true
			}
		}
		if hasMakingOf(part) {
			return true
		}
	}
	return false
}

// hasMakingOf reports whether a name chunk carries the phrase
// "making of" under any of the usual separators ("The Making
// of …", "The.Making.of.…", "Making_of_…"): the chunk is
// separator-normalized before the phrase test, because the
// dotted release form is exactly how the real making-of file
// in the corpus is named.
func hasMakingOf(s string) bool {
	norm := strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(strings.ToLower(s))
	return strings.Contains(norm, "making of")
}

// yearlessMovieTitle decides whether a movie parse with no
// year sits in a MOVIE context (Ruling 2) and returns the
// planner-cleaned title it links under. The context guard is
// the whole rule — a clean title alone is not enough:
//
//   - (a) its parent folder is the work's own folder: the
//     parent's cleaned title EQUALS the movie's cleaned
//     title (both sides are furniture-stripped first, so a
//     file named "<Folder> (BD 720p) (Group)" still equals
//     its folder). Equality, never a prefix: a prefix test
//     lets pack-root strays in — a series pack root cleans
//     to the series name, and every file in it whose name
//     starts with that name (an OVA, an "Ending" clip in a
//     season folder) would pass. Measured on the real
//     corpus: the prefix form qualified exactly those
//     strays; equality qualifies only the own-folder movie.
//   - (b) an ancestor folder names a Movies context: its
//     cleaned title ends in the word "Movies"/"Movie" and
//     the movie's title starts with the text before that
//     word (the Macross Frontier shape: a "… Frontier
//     Movies" folder holding the Frontier movie).
//
// Files inside a series tree fail both tests by construction
// (no folder matches their title, no ancestor names a movies
// context they belong to), so OP/ED clips, CM/PV files and
// other series-tree strays stay unidentified exactly as
// before. The comparison title is the planner-cleaned one
// (bracket/paren furniture stripped): the parser's raw title
// for such files still carries group names, which are
// furniture, not title.
func yearlessMovieTitle(f orgFile, pn ParsedName, root string) (string, bool) {
	if pn.Kind != "movie" || pn.Year != 0 {
		return "", false
	}
	title := cleanedNameTitle(f.Stem)
	if title == "" {
		return "", false
	}
	movieNorm := normalizeTitle(title)
	parts := dirPartsOf(f, root)
	if len(parts) == 0 {
		return "", false // no folder context at all
	}
	if parentNorm := normalizeTitle(folderTitleClean(parts[len(parts)-1])); parentNorm != "" {
		if parentNorm == movieNorm {
			return title, true
		}
	}
	for _, part := range parts {
		toks := strings.Fields(normalizeTitle(folderTitleClean(part)))
		if len(toks) < 2 {
			continue
		}
		if last := toks[len(toks)-1]; last == "movies" || last == "movie" {
			pre := strings.Join(toks[:len(toks)-1], " ")
			if movieNorm == pre || strings.HasPrefix(movieNorm, pre+" ") {
				return title, true
			}
		}
	}
	return "", false
}

// assignSpecialNumbers is the plan-wide Season 00 numbering
// pass (Pass 1.5): held specials are grouped by series, each
// group is sorted by cleaned source path, and numbers are
// created 1..N in that order. The synthesized parse re-enters
// the ordinary episode layout, so the file competes and
// links like any episode from here on — with its entry
// flagged number_assigned. A special the layout builder
// still refuses (defensive; the candidate checks should
// make it unreachable) falls back to its original
// not-identified record, untouched.
func (b *linkPlanBuilder) assignSpecialNumbers(specials []pendingSpecial, absTarget string) {
	if len(specials) == 0 {
		return
	}
	type specGroup struct {
		title string
		year  int
		vps   []*videoPlan
	}
	groups := map[string]*specGroup{}
	var keys []string
	reasons := map[*videoPlan]string{}
	fileUnidentified := func(vp *videoPlan) {
		vp.Status = "unidentified"
		b.plan.NotIdentified = append(b.plan.NotIdentified, LinkSkip{
			Path: vp.File.Path, Reason: reasons[vp],
			Parsed: linkParsedFact(vp.Parsed), Ext: vp.File.Ext, Size: vp.File.Size,
		})
		lv := b.lv[vp]
		lv.outKind, lv.outA = "notidentified", len(b.plan.NotIdentified)-1
	}
	for _, ps := range specials {
		reasons[ps.vp] = ps.reason
		title, year, ok := specialSeriesFor(ps.vp.File, ps.vp.Parsed, b.plan.SourceRoot)
		if !ok {
			// Cannot happen (Pass 1 admitted the file via the
			// same check on the same inputs) — but a held file
			// is never dropped silently: it falls back to its
			// original not-identified record.
			fileUnidentified(ps.vp)
			continue
		}
		key := normalizeTitle(title) + "\x00" + fmt.Sprint(year)
		g, exists := groups[key]
		if !exists {
			g = &specGroup{title: title, year: year}
			groups[key] = g
			keys = append(keys, key)
		}
		g.vps = append(g.vps, ps.vp)
	}
	sort.Strings(keys)
	for _, key := range keys {
		g := groups[key]
		sort.Slice(g.vps, func(i, j int) bool { return g.vps[i].File.Path < g.vps[j].File.Path })
		for i, vp := range g.vps {
			synth := vp.Parsed
			synth.Kind = "episode"
			synth.Title = g.title
			synth.NormTitle = normalizeTitle(g.title)
			synth.Year = g.year
			synth.Season = 0
			synth.Episodes = []int{i + 1}
			synth.Special = ""
			target, targetDir, targetStem, workDir, reason := buildVideoTarget(absTarget, vp.File, synth)
			if reason != "" || !pathWithinRoot(absTarget, target) {
				fileUnidentified(vp)
				continue
			}
			vp.Parsed = synth
			vp.Target, vp.TargetDir, vp.TargetStem, vp.WorkDir = target, targetDir, targetStem, workDir
			vp.Status = "candidate"
			b.assigned[vp] = true
		}
	}
}

// planExtras resolves each diverted extra's owning work and
// builds its link candidate (Pass 2.5). The owner rule: walk
// the extra's folder chain upward — its own folder first —
// and let the FIRST folder whose subtree holds target-built
// videos decide: exactly one work in that subtree owns the
// extra; more than one refuses (never a guess). The extra's
// target is <owner work folder>/extras/<cleaned title>, the
// documented Jellyfin catch-all extras folder (Jellyfin
// documentation, "Movies" — media organization, Extras
// section: extras for movies, series and seasons live in
// subfolders of the work's folder, "extras" being the
// generic one). Extras with no unambiguous owner, or an
// empty cleaned title, stay not-identified with the
// pre-1.4.0 reason.
func (b *linkPlanBuilder) planExtras(extras []extraItem, videos []*videoPlan, absSource, absTarget string) []*linkCandidate {
	if len(extras) == 0 {
		return nil
	}
	// subtreeWorks[dir][workDir] = one source path of that
	// work under dir (the smallest, for determinism).
	subtreeWorks := map[string]map[string]string{}
	for _, vp := range videos {
		if vp.Target == "" || vp.WorkDir == "" {
			continue
		}
		for d := vp.File.Dir; ; d = filepath.Dir(d) {
			m := subtreeWorks[d]
			if m == nil {
				m = map[string]string{}
				subtreeWorks[d] = m
			}
			if cur, ok := m[vp.WorkDir]; !ok || vp.File.Path < cur {
				m[vp.WorkDir] = vp.File.Path
			}
			if d == absSource || filepath.Dir(d) == d {
				break
			}
		}
	}
	sorted := append([]extraItem{}, extras...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].file.Path < sorted[j].file.Path })
	var out []*linkCandidate
	for _, it := range sorted {
		var workDir, ownerSource string
		owned := false
		for d := it.file.Dir; ; d = filepath.Dir(d) {
			if m, ok := subtreeWorks[d]; ok && len(m) > 0 {
				if len(m) == 1 {
					for wd, src := range m {
						workDir, ownerSource = wd, src
					}
					owned = true
				}
				break // the deepest folder with any work decides
			}
			if d == absSource || filepath.Dir(d) == d {
				break
			}
		}
		title := sanitizeSegment(it.parsed.Title)
		target := ""
		if owned && title != "" {
			target = filepath.Join(workDir, "extras", title+it.file.ExtOrig)
		}
		if target == "" || !pathWithinRoot(absTarget, target) {
			b.plan.NotIdentified = append(b.plan.NotIdentified, LinkSkip{
				Path: it.file.Path, Reason: "extra content — left in place",
				Parsed: linkParsedFact(it.parsed), Ext: it.file.Ext, Size: it.file.Size,
			})
			continue
		}
		out = append(out, &linkCandidate{
			file: it.file, target: target, kind: "extra",
			title: it.parsed.Title, ownerSource: ownerSource,
		})
	}
	return out
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
