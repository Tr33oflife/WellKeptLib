package core

// subtitle_dismissals.go — the dismissal store (v0.9.0,
// SUBTITLE-WIZARD-SPEC §2.5, wizard slice W2).
//
// A dismissal is the user's recorded judgement "this one is
// verified / intentional — stop flagging it": the escape hatch
// that separates a trusted findings list from alarm fatigue. It
// applies to ONE finding (a path + source + stream + problem
// class) or to ONE coverage item (source "coverage"), and it is
// bound to the file's fingerprint (size + mtime) at the moment
// it was recorded: if the file changes afterwards, the mark is
// stale and the finding RESURFACES — a dismissal can never
// silently outlive the file it was made about.
//
// Dismissals live in <dataDir>/subtitle-dismissals.json, are
// managed in Settings (list + remove), and are applied inside
// the read paths themselves (filteredSubtitlesLocked for
// findings, coverageAllLocked for coverage), so every surface —
// findings list, summary counts, wizard counts, export — agrees
// without each one re-implementing the rule. Dismissed findings
// also leave the guarded-action lists: you cannot act on a
// finding the app is not showing (undismiss first).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SubtitleDismissal is one recorded mark. For findings, Class is
// the finding's Status and StreamIdx its stream (-1 for external
// files); for coverage items Source is "coverage", Class is
// "coverage" and StreamIdx is 0.
type SubtitleDismissal struct {
	Path      string `json:"path"`
	Source    string `json:"source"` // external | embedded | coverage
	StreamIdx int    `json:"stream_index"`
	Class     string `json:"class"`
	Size      int64  `json:"size"`
	MtimeUnix int64  `json:"mtime_unix"`
	Note      string `json:"note,omitempty"`
	At        string `json:"at"` // RFC3339 UTC
}

// dismissalKey is the identity a dismissal binds to: the file,
// WHICH finding about it (source + stream), and WHAT problem
// class was judged — a file whose problem changes (mislabeled
// yesterday, unverifiable today) is a new question, not the one
// the user answered.
func dismissalKey(path, source string, streamIdx int, class string) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", path, source, streamIdx, class)
}

func (d SubtitleDismissal) key() string {
	return dismissalKey(d.Path, d.Source, d.StreamIdx, d.Class)
}

// loadSubtitleDismissals reads the store (missing or unreadable
// file = no marks; a corrupt store must never stop the app).
func loadSubtitleDismissals(dataDir string) []SubtitleDismissal {
	b, err := os.ReadFile(filepath.Join(dataDir, "subtitle-dismissals.json"))
	if err != nil {
		return []SubtitleDismissal{}
	}
	var list []SubtitleDismissal
	if json.Unmarshal(b, &list) != nil || list == nil {
		return []SubtitleDismissal{}
	}
	return list
}

// saveDismissalsLocked persists the store (tmp+rename, the
// package's usual pattern). Caller holds s.mu.
func (s *Service) saveDismissalsLocked() error {
	b, err := json.MarshalIndent(s.dismissals, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(s.dataDir, "subtitle-dismissals.json")
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// fingerprintCurrent reports whether the file at path still has
// exactly the recorded size + mtime (seconds — the granularity
// Item.ModTimeUnix already uses). Any stat failure counts as
// changed: a file that cannot be checked cannot keep a mark
// made about a different state of it.
func fingerprintCurrent(path string, size, mtimeUnix int64) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() == size && info.ModTime().Unix() == mtimeUnix
}

// dismissalIndexLocked indexes the store by key. Caller holds s.mu.
func (s *Service) dismissalIndexLocked() map[string]SubtitleDismissal {
	idx := make(map[string]SubtitleDismissal, len(s.dismissals))
	for _, d := range s.dismissals {
		idx[d.key()] = d
	}
	return idx
}

// findingDismissedLocked reports whether finding f carries a
// CURRENT dismissal (same key, fingerprint still matching).
// Caller holds s.mu.
func (s *Service) findingDismissedLocked(f SubtitleFinding) bool {
	d, ok := s.dismissalIndexLocked()[dismissalKey(f.Path, f.Source, f.StreamIdx, f.Status)]
	return ok && fingerprintCurrent(d.Path, d.Size, d.MtimeUnix)
}

// coverageDismissedLocked reports whether the coverage item at
// path carries a current dismissal. Caller holds s.mu.
func (s *Service) coverageDismissedLocked(path string) bool {
	d, ok := s.dismissalIndexLocked()[dismissalKey(path, "coverage", 0, "coverage")]
	return ok && fingerprintCurrent(d.Path, d.Size, d.MtimeUnix)
}

// dismissedFindingsCountLocked counts how many findings the
// dismissal store is currently suppressing — over the RAW
// results, with the same audit-off and exclusion filters
// filteredSubtitlesLocked applies, so the wizard's "marked
// verified by you: N" is exactly the number of findings the user
// is not seeing because of their marks. Caller holds s.mu.
func (s *Service) dismissedFindingsCountLocked() int {
	idx := s.dismissalIndexLocked()
	n := 0
	for _, f := range s.results.Subtitles {
		if !s.cfg.EmbeddedAudit && f.Source == "embedded" {
			continue
		}
		if isExcluded(s.cfg.Exclusions, f.Path, "subtitles") {
			continue
		}
		if d, ok := idx[dismissalKey(f.Path, f.Source, f.StreamIdx, f.Status)]; ok &&
			fingerprintCurrent(d.Path, d.Size, d.MtimeUnix) {
			n++
		}
	}
	return n
}

// Dismissals returns the store for the Settings list, newest
// first. Stale marks (fingerprint no longer matching) are
// included — Settings is where the user sees and removes them;
// they simply have no suppressing effect.
func (s *Service) Dismissals() []SubtitleDismissal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]SubtitleDismissal{}, s.dismissals...)
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// upsertDismissalLocked records (or replaces) one mark and
// persists the store. Caller holds s.mu.
func (s *Service) upsertDismissalLocked(d SubtitleDismissal) error {
	k := d.key()
	replaced := false
	for i := range s.dismissals {
		if s.dismissals[i].key() == k {
			s.dismissals[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		s.dismissals = append(s.dismissals, d)
	}
	return s.saveDismissalsLocked()
}

// DismissFinding marks ONE current subtitle finding as verified
// / intentional. The finding must be in the CURRENT published
// list (filteredSubtitlesLocked — the same list the guards use),
// so a mark can only ever be made about something the app is
// actually showing. The fingerprint is stat-ed from the file
// itself: for embedded findings that is the media file, for
// external ones the subtitle file.
func (s *Service) DismissFinding(path, source string, streamIdx int, note string) (SubtitleDismissal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var target *SubtitleFinding
	current := s.filteredSubtitlesLocked()
	for i := range current {
		if current[i].Path == path && current[i].Source == source && current[i].StreamIdx == streamIdx {
			target = &current[i]
			break
		}
	}
	if target == nil {
		return SubtitleDismissal{}, fmt.Errorf("no current subtitle finding for %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return SubtitleDismissal{}, fmt.Errorf("cannot check %s: %v", path, err)
	}
	d := SubtitleDismissal{
		Path:      path,
		Source:    target.Source,
		StreamIdx: target.StreamIdx,
		Class:     target.Status,
		Size:      info.Size(),
		MtimeUnix: info.ModTime().Unix(),
		Note:      note,
		At:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.upsertDismissalLocked(d); err != nil {
		return SubtitleDismissal{}, err
	}
	return d, nil
}

// DismissCoverage marks one scanned media item "not wanted" for
// coverage: it leaves the coverage list and counts until the
// file changes or the mark is removed in Settings. The item
// must be in the current results.
func (s *Service) DismissCoverage(path, note string) (SubtitleDismissal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, it := range s.results.Items {
		if it != nil && it.Path == path {
			found = true
			break
		}
	}
	if !found {
		return SubtitleDismissal{}, fmt.Errorf("no scanned item at %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return SubtitleDismissal{}, fmt.Errorf("cannot check %s: %v", path, err)
	}
	d := SubtitleDismissal{
		Path:      path,
		Source:    "coverage",
		StreamIdx: 0,
		Class:     "coverage",
		Size:      info.Size(),
		MtimeUnix: info.ModTime().Unix(),
		Note:      note,
		At:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.upsertDismissalLocked(d); err != nil {
		return SubtitleDismissal{}, err
	}
	return d, nil
}

// Undismiss removes one mark by its full key (the Settings
// list's Remove button). Removing a mark that no longer exists
// is an error so the UI can say so honestly.
func (s *Service) Undismiss(path, source string, streamIdx int, class string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dismissalKey(path, source, streamIdx, class)
	out := s.dismissals[:0]
	removed := false
	for _, d := range s.dismissals {
		if d.key() == k {
			removed = true
			continue
		}
		out = append(out, d)
	}
	if !removed {
		return fmt.Errorf("no such verified mark for %s", path)
	}
	s.dismissals = out
	return s.saveDismissalsLocked()
}
