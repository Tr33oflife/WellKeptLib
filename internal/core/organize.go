package core

// organize.go — the Organize planner (v0.3.0): turn a messy folder
// into a Jellyfin layout, as a DRY-RUN PLAN. This file never moves a
// file — it only computes what Apply would do (see organize_apply.go
// for the guarded execution + undo).
//
// THE AGREED SHAPE (do not improvise formats):
//   movie:   <root>/<Title> (<Year>)/<Title> (<Year>)<ext>
//   episode: <root>/<Series> (<Year>)/Season NN/<Series> - SNNENN[-ENN] [- Episode Title]<ext>
//            (series folder drops the " (Year)" when the year is
//            unknown; season 0 is "Season 00" — specials)
//   Sidecars (same-basename subtitles / .nfo / artwork) travel with
//   their video; folder artwork for a single work follows it.
//   Anything the parser cannot identify is LEFT IN PLACE and listed —
//   never guessed into a folder. Name collisions are reported, never
//   resolved by overwriting or auto-renaming.
//
// Identity comes from the existing release-name parser (parser.go) —
// the same parse the duplicate finder uses — so Organize can never
// disagree with the rest of the app about what a file is.

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OrganizeMove is one planned file move inside an OrganizePlan.
type OrganizeMove struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"` // movie | episode | sidecar | artwork
	Title string `json:"title"`
}

// OrganizeSkip is one file the planner deliberately left alone, with
// the reason. Unidentified files and skipped sidecars share this
// shape but live in separate plan lists so the UI (and the user) can
// tell "we don't know what this is" from "we know, but it stays".
type OrganizeSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// OrganizeCollision is a set of moves that were NOT planned because
// they would overwrite an existing file or fight over one target.
type OrganizeCollision struct {
	Target  string   `json:"target"`
	Sources []string `json:"sources"`
	Reason  string   `json:"reason"`
}

// OrganizeStats is the headline counts of a plan.
type OrganizeStats struct {
	Movies           int `json:"movies"`
	Episodes         int `json:"episodes"`
	Sidecars         int `json:"sidecars"`
	Artwork          int `json:"artwork"`
	TotalMoves       int `json:"total_moves"`
	AlreadyOrganized int `json:"already_organized"`
	Unidentified     int `json:"unidentified"`
	Collisions       int `json:"collisions"`
	Skipped          int `json:"skipped"`
}

// OrganizePlan is the full dry-run output for one root: the moves
// Apply would make, plus every file deliberately NOT moved and why.
type OrganizePlan struct {
	ID               string              `json:"id"`
	Root             string              `json:"root"`
	Moves            []OrganizeMove      `json:"moves"`
	Unidentified     []OrganizeSkip      `json:"unidentified"`
	Collisions       []OrganizeCollision `json:"collisions"`
	AlreadyOrganized []string            `json:"already_organized"`
	Skipped          []OrganizeSkip      `json:"skipped"`
	Stats            OrganizeStats       `json:"stats"`
}

// organizeImageExts are the still-image extensions folder artwork can
// have (basename sidecars are extension-agnostic; folder artwork is
// not — a stray .txt named "poster" is not artwork).
var organizeImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".bmp": true,
}

// artworkStems are the well-known folder-artwork basenames (matched
// case-insensitively against the file's stem).
var artworkStems = map[string]bool{
	"poster": true, "folder": true, "cover": true, "backdrop": true,
	"fanart": true, "banner": true, "logo": true, "thumb": true,
}

// orgFile is one file discovered under the organize root.
type orgFile struct {
	Path    string
	Dir     string
	Name    string
	Stem    string // filename without extension
	Ext     string // lower-case extension, with dot
	ExtOrig string // extension exactly as it appears in the name
}

// videoPlan is one video file plus the planner's verdict for it.
type videoPlan struct {
	File       orgFile
	Parsed     ParsedName
	Target     string // full target path ("" when unidentified)
	TargetDir  string // folder the video lands in
	TargetStem string // target filename without extension (sidecar naming base)
	WorkDir    string // target work folder (movie folder / series folder)
	Status     string // move | already | unidentified
}

// sanitizeSegment makes one generated path segment safe for Jellyfin
// libraries on any OS: the banned characters < > : " / \ | ? * and
// control characters are removed, whitespace collapses to single
// spaces, and trailing dots/spaces are trimmed (Windows trims those
// silently, which would break the from->to mapping). An all-banned
// title sanitizes to "" — the caller treats that as unidentified.
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			// control character: dropped
		case strings.ContainsRune(`<>:"/\|?*`, r):
			// banned character: dropped
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	out = strings.TrimRight(out, ". ")
	return strings.TrimSpace(out)
}

// pathWithinRoot reports whether p (after cleaning) is the root
// itself or lives under it, on a separator boundary. This is the
// confinement property the whole Organize feature rests on: no
// planned or executed path may ever escape the chosen folder.
func pathWithinRoot(root, p string) bool {
	r := cleanAbs(root)
	c := cleanAbs(p)
	return c == r || strings.HasPrefix(c, r+string(os.PathSeparator))
}

// stemHasReleaseTag reports whether the stem carries any known
// release token — the planner-side mirror of collectGroupTag's
// gate in the parser (a trailing -GROUP only counts as a group
// when the name is a release name at all).
func stemHasReleaseTag(stem string) bool {
	for _, tok := range strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ").Replace(stem)) {
		if tagTokens[strings.ToLower(tok)] {
			return true
		}
	}
	return false
}

// episodeTitleFromName extracts an episode title from the filename
// text AFTER the SxxEyy / NxN marker (planner-side only — the parser
// intentionally does not do this; its output must not change). The
// remainder is run through the parser's token cleaning, so release
// tags and years vanish and only real title words survive.
func episodeTitleFromName(stem string) string {
	rest := ""
	if loc := reSxE.FindStringIndex(stem); loc != nil {
		rest = stem[loc[1]:]
	} else if loc := reNxN.FindStringIndex(stem); loc != nil {
		rest = stem[loc[1]:]
	} else {
		return "" // folder-context episode: no marker in the name
	}
	// v0.3.9: a trailing -GROUP on the remainder is the release
	// group, not an episode title ("…S02E05.1080p.….x264-SYNCOPY"
	// planned the episode title "SYNCOPY"). Strip it before
	// cleaning — but only when the stem carries known release
	// tokens (collectGroupTag's condition), so a spaced human
	// title is never touched: it has no hyphen glued to its
	// final token for reGroup to match in the first place.
	if loc := reGroup.FindStringIndex(rest); loc != nil && stemHasReleaseTag(stem) {
		rest = rest[:loc[0]]
	}
	var tags []string
	return strings.Join(cleanTokens(rest, &tags), " ")
}

// newPlanID mints a plan identifier (timestamp + random suffix).
// Plans live in memory on the Service; the ID is the handle Apply
// uses to refer back to exactly the plan the user reviewed.
func newPlanID() string {
	var b [4]byte
	suffix := ""
	if _, err := rand.Read(b[:]); err == nil {
		suffix = fmt.Sprintf("-%x", b[:])
	}
	return "plan-" + time.Now().UTC().Format("20060102-150405") + suffix
}

// buildVideoTarget computes the Jellyfin target for one parsed video.
// It returns the target facts, or a non-empty reason when the file is
// unidentified (the reason strings are user-facing — keep them plain).
func buildVideoTarget(absRoot string, f orgFile, pn ParsedName) (target, targetDir, targetStem, workDir, reason string) {
	switch pn.Kind {
	case "movie":
		if pn.Year == 0 {
			return "", "", "", "", "no year found"
		}
		title := sanitizeSegment(pn.Title)
		if title == "" {
			return "", "", "", "", "title is empty after removing banned characters"
		}
		folder := fmt.Sprintf("%s (%d)", title, pn.Year)
		targetDir = filepath.Join(absRoot, folder)
		targetStem = folder
		target = filepath.Join(targetDir, folder+f.ExtOrig)
		workDir = targetDir
		return target, targetDir, targetStem, workDir, ""
	case "episode":
		if len(pn.Episodes) == 0 {
			return "", "", "", "", "no episode number found"
		}
		title := sanitizeSegment(pn.Title)
		if title == "" {
			return "", "", "", "", "title is empty after removing banned characters"
		}
		seriesFolder := title
		if pn.Year > 0 {
			seriesFolder = fmt.Sprintf("%s (%d)", title, pn.Year)
		}
		code := fmt.Sprintf("S%02dE%02d", pn.Season, pn.Episodes[0])
		for _, e := range pn.Episodes[1:] {
			code += fmt.Sprintf("-E%02d", e)
		}
		fname := title + " - " + code
		if et := sanitizeSegment(episodeTitleFromName(f.Stem)); et != "" {
			fname += " - " + et
		}
		workDir = filepath.Join(absRoot, seriesFolder)
		targetDir = filepath.Join(workDir, fmt.Sprintf("Season %02d", pn.Season))
		targetStem = fname
		target = filepath.Join(targetDir, fname+f.ExtOrig)
		return target, targetDir, targetStem, workDir, ""
	case "extra":
		// Extras (trailers, featurettes, deleted scenes, …) are
		// identified — the parser classified them on purpose —
		// but they are not movies or episodes and never move.
		return "", "", "", "", "extra content — left in place"
	default:
		return "", "", "", "", "couldn't identify this file"
	}
}

// PlanOrganize computes the full dry-run plan for one root folder.
// It is pure with respect to the filesystem: it READS the tree
// (walk + stat for collision checks) and never writes anything.
// The root is validated with the same helper scans use, so a bad
// root is a named error, never an empty plan.
func PlanOrganize(root string) (*OrganizePlan, error) {
	valid, problems := validateRoots([]string{root})
	if len(valid) == 0 {
		if len(problems) > 0 {
			return nil, fmt.Errorf("cannot organize — %s", strings.Join(problems, "; "))
		}
		return nil, fmt.Errorf("cannot organize — no folder given")
	}
	absRoot := cleanAbs(valid[0])
	plan := &OrganizePlan{
		ID:               newPlanID(),
		Root:             absRoot,
		Moves:            []OrganizeMove{},
		Unidentified:     []OrganizeSkip{},
		Collisions:       []OrganizeCollision{},
		AlreadyOrganized: []string{},
		Skipped:          []OrganizeSkip{},
	}

	// ---- Walk: collect every regular file, grouped by folder. ----
	// Symlinks are never followed (same discipline as the scanner):
	// a symlinked file is listed as skipped, a symlinked folder is
	// not descended into at all.
	filesByDir := map[string][]orgFile{}
	var walkErr error
	err := filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if path == absRoot {
				return err
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			plan.Skipped = append(plan.Skipped, OrganizeSkip{Path: path, Reason: "symbolic link — left alone"})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			return nil
		}
		name := d.Name()
		extOrig := filepath.Ext(name)
		f := orgFile{
			Path: path, Dir: filepath.Dir(path), Name: name,
			Stem: strings.TrimSuffix(name, extOrig), Ext: strings.ToLower(extOrig), ExtOrig: extOrig,
		}
		filesByDir[f.Dir] = append(filesByDir[f.Dir], f)
		return nil
	})
	if err != nil {
		walkErr = err
	}
	if walkErr != nil {
		return nil, fmt.Errorf("cannot read the folder %s: %w", absRoot, walkErr)
	}

	// ---- Pass 1: videos. Parse identity, build targets, classify. ----
	videosByDir := map[string][]*videoPlan{}
	var videos []*videoPlan
	for _, files := range filesByDir {
		for _, f := range files {
			if !mediaExts[f.Ext] {
				continue
			}
			vp := &videoPlan{File: f, Parsed: parseName(f.Path, absRoot)}
			target, targetDir, targetStem, workDir, reason := buildVideoTarget(absRoot, f, vp.Parsed)
			if reason != "" {
				vp.Status = "unidentified"
				plan.Unidentified = append(plan.Unidentified, OrganizeSkip{Path: f.Path, Reason: reason})
			} else {
				vp.Target, vp.TargetDir, vp.TargetStem, vp.WorkDir = target, targetDir, targetStem, workDir
				if cleanAbs(f.Path) == cleanAbs(target) {
					vp.Status = "already"
					plan.AlreadyOrganized = append(plan.AlreadyOrganized, f.Path)
				} else if !pathWithinRoot(absRoot, target) {
					// Defensive: a sanitized name must never escape the
					// root. Sanitization should make this unreachable.
					vp.Status = "unidentified"
					vp.Target = ""
					plan.Unidentified = append(plan.Unidentified, OrganizeSkip{Path: f.Path, Reason: "planned target would escape the chosen folder — left in place"})
				} else {
					vp.Status = "move"
				}
			}
			videos = append(videos, vp)
			videosByDir[f.Dir] = append(videosByDir[f.Dir], vp)
		}
	}

	// ---- Pass 2: video collisions. Two sources wanting one target
	// are BOTH collisions (neither moves — picking a winner would be
	// a guess), and a target that already exists on disk is a
	// collision too. Colliding videos leave their sidecars behind. ----
	byTarget := map[string][]*videoPlan{}
	for _, vp := range videos {
		if vp.Status == "move" {
			byTarget[vp.Target] = append(byTarget[vp.Target], vp)
		}
	}
	for target, group := range byTarget {
		if len(group) > 1 {
			sources := []string{}
			for _, vp := range group {
				vp.Status = "collision"
				sources = append(sources, vp.File.Path)
			}
			sort.Strings(sources)
			plan.Collisions = append(plan.Collisions, OrganizeCollision{
				Target: target, Sources: sources,
				Reason: "more than one file would land on this name — none of them move",
			})
			continue
		}
		vp := group[0]
		if _, err := os.Lstat(vp.Target); err == nil {
			vp.Status = "collision"
			plan.Collisions = append(plan.Collisions, OrganizeCollision{
				Target: vp.Target, Sources: []string{vp.File.Path},
				Reason: "a file already exists at the target path — never overwritten",
			})
		}
	}
	for _, vp := range videos {
		if vp.Status == "move" {
			plan.Moves = append(plan.Moves, OrganizeMove{
				From: vp.File.Path, To: vp.Target, Kind: vp.Parsed.Kind, Title: vp.Parsed.Title,
			})
		}
	}

	// ---- Pass 3: sidecars + folder artwork. ----
	// movingByDir indexes the videos that will actually move, per folder.
	movingByDir := map[string][]*videoPlan{}
	for _, vp := range videos {
		if vp.Status == "move" {
			movingByDir[vp.File.Dir] = append(movingByDir[vp.File.Dir], vp)
		}
	}
	for dir, files := range filesByDir {
		for _, f := range files {
			if mediaExts[f.Ext] {
				continue // videos were pass 1
			}
			planNonVideo(plan, f, videosByDir[dir], movingByDir[dir])
		}
	}

	// ---- Pass 4: final collision sweep across ALL moves. A sidecar
	// can still collide (with a video target, another sidecar, or a
	// file already on disk); it is excluded on its own — its video
	// still moves. ----
	plan.Moves = sweepCollisions(plan, plan.Moves)

	// ---- Stats + stable ordering for display and tests. ----
	for _, mv := range plan.Moves {
		plan.Stats.TotalMoves++
		switch mv.Kind {
		case "movie":
			plan.Stats.Movies++
		case "episode":
			plan.Stats.Episodes++
		case "sidecar":
			plan.Stats.Sidecars++
		case "artwork":
			plan.Stats.Artwork++
		}
	}
	plan.Stats.AlreadyOrganized = len(plan.AlreadyOrganized)
	plan.Stats.Unidentified = len(plan.Unidentified)
	plan.Stats.Collisions = len(plan.Collisions)
	plan.Stats.Skipped = len(plan.Skipped)
	sort.Slice(plan.Moves, func(i, j int) bool { return plan.Moves[i].From < plan.Moves[j].From })
	sort.Slice(plan.Unidentified, func(i, j int) bool { return plan.Unidentified[i].Path < plan.Unidentified[j].Path })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].Path < plan.Skipped[j].Path })
	sort.Slice(plan.Collisions, func(i, j int) bool { return plan.Collisions[i].Target < plan.Collisions[j].Target })
	sort.Strings(plan.AlreadyOrganized)
	return plan, nil
}

// planNonVideo classifies one non-video file: basename sidecar of a
// moving video, folder artwork for a single-work folder, an orphaned
// subtitle/.nfo (listed as skipped so nothing is silently stranded),
// or a file the organizer simply does not manage (ignored, unlisted).
func planNonVideo(plan *OrganizePlan, f orgFile, dirVideos, dirMoving []*videoPlan) {
	// Basename sidecar: the filename starts with a video's stem plus
	// a separator (Film.en.srt, Film-thumb.jpg). The separator check
	// keeps "Film2.srt" from attaching to "Film.mkv".
	var matches, movingMatches []*videoPlan
	for _, vp := range dirVideos {
		stem := vp.File.Stem
		if strings.HasPrefix(f.Name, stem) && len(f.Name) > len(stem) {
			if c := f.Name[len(stem)]; c == '.' || c == '-' || c == '_' {
				matches = append(matches, vp)
				if vp.Status == "move" {
					movingMatches = append(movingMatches, vp)
				}
			}
		}
	}
	if len(matches) > 0 {
		switch {
		case len(movingMatches) == 1:
			vp := movingMatches[0]
			suffix := f.Name[len(vp.File.Stem):] // e.g. ".en.srt" — preserved exactly
			plan.Moves = append(plan.Moves, OrganizeMove{
				From: f.Path, To: filepath.Join(vp.TargetDir, vp.TargetStem+suffix),
				Kind: "sidecar", Title: vp.Parsed.Title,
			})
		case len(movingMatches) > 1:
			plan.Skipped = append(plan.Skipped, OrganizeSkip{
				Path: f.Path, Reason: "matches more than one video in its folder — left in place",
			})
		default:
			// Its video is staying (unidentified / collision / already
			// organized), so the sidecar stays with it — silently,
			// because the video's own listing already explains why.
		}
		return
	}

	// Folder artwork: a known artwork name (poster.jpg, tvshow.nfo…)
	// in a folder whose MOVING videos all belong to one work follows
	// that work, keeping its own filename.
	if artworkStems[strings.ToLower(f.Stem)] && (organizeImageExts[f.Ext] || f.Ext == ".nfo") {
		works := map[string]bool{}
		for _, vp := range dirMoving {
			works[vp.WorkDir] = true
		}
		switch {
		case len(works) == 1:
			for workDir := range works {
				plan.Moves = append(plan.Moves, OrganizeMove{
					From: f.Path, To: filepath.Join(workDir, f.Name),
					Kind: "artwork", Title: dirMoving[0].Parsed.Title,
				})
			}
		case len(works) > 1:
			plan.Skipped = append(plan.Skipped, OrganizeSkip{
				Path: f.Path, Reason: "folder holds more than one work — artwork left in place",
			})
		case len(dirVideos) == 0:
			plan.Skipped = append(plan.Skipped, OrganizeSkip{
				Path: f.Path, Reason: "sidecar with no matching video",
			})
		default:
			// Videos are here but none are moving; the artwork is
			// already with its work. Not listed.
		}
		return
	}

	// Orphaned subtitle / .nfo: no video in this folder shares its
	// basename. Listed so it is never silently stranded.
	if subtitleExts[f.Ext] || f.Ext == ".nfo" {
		plan.Skipped = append(plan.Skipped, OrganizeSkip{
			Path: f.Path, Reason: "sidecar with no matching video",
		})
	}
	// Anything else (notes, images with other names, …) is not media
	// the organizer manages: ignored entirely, by design.
}

// sweepCollisions removes any moves that share a target or whose
// target already exists on disk, recording them as collisions. It
// runs over the complete move list (videos + sidecars + artwork).
func sweepCollisions(plan *OrganizePlan, moves []OrganizeMove) []OrganizeMove {
	byTarget := map[string][]OrganizeMove{}
	var order []string
	for _, mv := range moves {
		if _, ok := byTarget[mv.To]; !ok {
			order = append(order, mv.To)
		}
		byTarget[mv.To] = append(byTarget[mv.To], mv)
	}
	kept := []OrganizeMove{}
	for _, target := range order {
		group := byTarget[target]
		if len(group) > 1 {
			sources := []string{}
			for _, mv := range group {
				sources = append(sources, mv.From)
			}
			sort.Strings(sources)
			plan.Collisions = append(plan.Collisions, OrganizeCollision{
				Target: target, Sources: sources,
				Reason: "more than one file would land on this name — none of them move",
			})
			continue
		}
		mv := group[0]
		if cleanAbs(mv.To) != cleanAbs(mv.From) {
			if _, err := os.Lstat(mv.To); err == nil {
				plan.Collisions = append(plan.Collisions, OrganizeCollision{
					Target: target, Sources: []string{mv.From},
					Reason: "a file already exists at the target path — never overwritten",
				})
				continue
			}
		}
		kept = append(kept, mv)
	}
	return kept
}
