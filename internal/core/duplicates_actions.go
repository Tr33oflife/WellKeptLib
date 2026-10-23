package core

// duplicates_actions.go — the Duplicates tab gets the Health
// tab's two actions (v0.3.6): "Open location" (reveal) and a
// guarded "Delete copy". The machinery is the Health actions'
// machinery (health_actions.go) — same reveal launcher and
// honest answer shape, same scan/root/regular-file guards,
// same audit log, same post-delete results mutation. What
// differs is the validation that makes a path actionable, and
// one extra rule:
//
//   - The path must be a copy in one of the CURRENT duplicate
//     groups — computed by buildDuplicates over the current
//     results with the user's exclusions applied, i.e. exactly
//     the groups the Duplicates tab is showing. A path in no
//     displayed group is refused, full stop.
//
//   - LAST-COPY PROTECTION: a delete can never take a group to
//     zero copies. Groups only ever display with 2+ copies, so
//     deleting from a 2-copy group dissolves the group and the
//     surviving copy stops being a "duplicate copy" at all —
//     the membership check then refuses it. The explicit
//     len(copies) < 2 check below states the invariant in code
//     anyway, so it holds even if group construction changes.
//
// The actions live on Service like every other operation
// (transport separation), same as the Health actions.

import (
	"fmt"
	"strings"
	"time"
)

// DeleteDuplicateResult answers a successful duplicates delete:
// the path, plus the refreshed summary so the UI can update its
// counts at once (same shape as DeleteHealthResult).
type DeleteDuplicateResult struct {
	OK      bool        `json:"ok"`
	Path    string      `json:"path"`
	Summary ScanSummary `json:"summary"`
}

// dupGroupLabel is the human label of a group for the confirm
// panel and the audit log — the same words the Duplicates tab
// heading uses: "Title (Year)" / "Title S01E01,E02", plus the
// edition when the group is one edition of the identity.
func dupGroupLabel(g DupGroup) string {
	label := g.Title
	if g.Kind == "episode" {
		eps := make([]string, len(g.Episodes))
		for i, e := range g.Episodes {
			eps[i] = fmt.Sprint(e)
		}
		label += fmt.Sprintf(" S%02dE%s", g.Season, strings.Join(eps, ",E"))
	} else if g.Year > 0 {
		label += fmt.Sprintf(" (%d)", g.Year)
	}
	if g.Edition != "" {
		label += " [" + g.Edition + "]"
	}
	return label
}

// dupGroupForPathLocked finds the CURRENT duplicate group (as
// displayed — exclusions applied) that contains path, and the
// copy itself. Caller holds s.mu.
func (s *Service) dupGroupForPathLocked(path string) (DupGroup, DupCopy, bool) {
	groups, _ := buildDuplicates(s.results.Items, s.cfg, s.cfg.Exclusions)
	for _, g := range groups {
		for _, c := range g.Copies {
			if c.Path == path {
				return g, c, true
			}
		}
	}
	return DupGroup{}, DupCopy{}, false
}

// RevealDuplicatePath validates path as a copy in a current
// duplicate group and asks the local desktop to show it in its
// file manager — the exact reveal behaviour (and honest answer
// shape) of RevealHealthPath.
func (s *Service) RevealDuplicatePath(path string) (RevealResult, error) {
	s.mu.Lock()
	_, _, ok := s.dupGroupForPathLocked(path)
	s.mu.Unlock()
	if !ok {
		return RevealResult{}, fmt.Errorf("not a copy in any current duplicate group: %s", path)
	}
	// The mutex is released before launching an external process:
	// starting Explorer must never block the service.
	return revealPath(path), nil
}

// DeleteDuplicateCopy permanently deletes ONE duplicate copy
// behind the guard chain, in order:
//  1. path is a copy in a current duplicate group (see
//     dupGroupForPathLocked — exclusions applied, as displayed);
//  2. last-copy protection: the group still has at least 2
//     copies, so this delete cannot take it to zero;
//  3. the shared file guards (checkDeletableFileLocked): no
//     scan running, under a configured scan root, a regular
//     file.
//
// After the delete it appends to the SAME audit log as the
// Health deletes (kind "duplicate-copy", the group label in
// Detail), removes the file from the published results without
// a rescan (the shared removePathFromResultsLocked: item +
// every finding naming the path; the groups and summary counts
// rebuild from the remaining items), and persists.
func (s *Service) DeleteDuplicateCopy(path string) (DeleteDuplicateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, _, ok := s.dupGroupForPathLocked(path)
	if !ok {
		return DeleteDuplicateResult{}, fmt.Errorf("not a copy in any current duplicate group: %s", path)
	}
	if len(group.Copies) < 2 {
		return DeleteDuplicateResult{}, fmt.Errorf("refusing to delete %s: it is the last remaining copy of %q — a delete can never remove every copy", path, dupGroupLabel(group))
	}
	info, err := s.checkDeletableFileLocked(path)
	if err != nil {
		return DeleteDuplicateResult{}, err
	}
	if err := removeFile(path); err != nil {
		return DeleteDuplicateResult{}, fmt.Errorf("delete failed for %s: %v", path, err)
	}
	// Best-effort audit log, same as the Health delete: the
	// file is already gone, so a log-write hiccup must not
	// report the delete as failed.
	_ = s.appendDeletedActionLocked(DeletedAction{
		Time:        time.Now().UTC().Format(time.RFC3339),
		Path:        path,
		Size:        info.Size(),
		FindingKind: "duplicate-copy",
		Detail:      dupGroupLabel(group),
	})
	s.removePathFromResultsLocked(path)
	return DeleteDuplicateResult{OK: true, Path: path, Summary: s.currentSummaryLocked()}, nil
}
