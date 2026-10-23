package core

// subtitle_relabel.go — wizard slice W3 (v0.9.0,
// SUBTITLE-WIZARD-SPEC §2.4 Step D): fixing a wrong language
// label on an EXTERNAL subtitle file by renaming the file so
// its filename language token matches the detected language.
//
// The mechanics deliberately mirror Organize — the app's only
// other file-moving feature — rather than inventing a second
// safety model:
//
//   - Preview computes the exact old -> new pair and flags
//     collisions; nothing moves in preview.
//   - Apply re-validates every file against the CURRENT
//     findings (a path is only actionable while the app is
//     showing the finding about it), refuses while a scan or
//     another file operation runs (beginFileOp), writes an undo
//     manifest to <dataDir>/subtitle-manifests/ BEFORE the first
//     rename and rewrites it after each one, and NEVER
//     overwrites: a target that exists is skipped and reported.
//   - Undo restores from the manifest in reverse order, with
//     every recorded path confined to a configured scan root
//     (the manifest is data, not authority), and never
//     overwrites a re-occupied original name either.
//
// Only renames happen here — file contents are never touched,
// so an apply/undo round trip is byte-identical by construction.
// Embedded findings have NO relabel (or any file action): their
// path is a media file and this app never rewrites media;
// planRelabel says so explicitly when asked.
//
// The new name keeps the file's own form: the trailing language
// token in the stem is replaced with the finding's Detected
// code (ISO 639-1, the form the detector reports), preserving
// any flag suffixes after it (.forced, .sdh, …); a file with no
// language token gets ".<detected>" inserted before its flags
// (or appended when it has none).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RelabelEntry is one preview/apply row: the exact rename, or
// the reason there isn't one.
type RelabelEntry struct {
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Detected  string `json:"detected,omitempty"`
	Collision bool   `json:"collision,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RelabelApplyResult reports an apply: the manifest that can
// undo it (empty when nothing was applied), what moved, and
// what was skipped and why.
type RelabelApplyResult struct {
	ManifestID string         `json:"manifest_id,omitempty"`
	Applied    []RelabelEntry `json:"applied"`
	Failed     []RelabelEntry `json:"failed"`
}

// RelabelUndoResult reports an undo: how many names restored,
// and what was skipped and why.
type RelabelUndoResult struct {
	Restored int               `json:"restored"`
	Failed   []OrganizeFailure `json:"failed"`
}

// subtitleManifest is the on-disk undo record for one relabel
// apply — the organizeManifest shape, minus created dirs (a
// rename never creates folders) and minus a single root (the
// files can span roots; undo confines each path against every
// configured root instead).
type subtitleManifest struct {
	ID        string               `json:"id"`
	CreatedAt string               `json:"created_at"`
	Moves     []subtitleRenameMove `json:"moves"`
	UndoneAt  string               `json:"undone_at,omitempty"`
}

type subtitleRenameMove struct {
	From string `json:"from"`
	To   string `json:"to"`
	Done bool   `json:"done"`
}

// SubtitleManifestSummary is one row of the relabel history list.
type SubtitleManifestSummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Moves     int    `json:"moves"`
	Undone    bool   `json:"undone"`
}

// subtitleFlagTokens are the filename tokens that may follow a
// language token and must survive a relabel — the same set
// declaredLangFromFilename skips when reading a declared
// language.
var subtitleFlagTokens = map[string]bool{
	"forced": true, "sdh": true, "cc": true, "hi": true,
	"default": true, "full": true, "sign": true, "signs": true, "songs": true,
}

// stemTokenSpans tokenizes a filename stem on the separator
// characters subtitle names use ('.', '_', '-', ' '), keeping
// each token's byte span so a token can be replaced or inserted
// around without disturbing the rest of the name.
type stemTokenSpan struct {
	start, end int
	text       string
}

func stemTokenSpans(stem string) []stemTokenSpan {
	var spans []stemTokenSpan
	i := 0
	for i < len(stem) {
		if stem[i] == '.' || stem[i] == '_' || stem[i] == '-' || stem[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(stem) && stem[j] != '.' && stem[j] != '_' && stem[j] != '-' && stem[j] != ' ' {
			j++
		}
		spans = append(spans, stemTokenSpan{start: i, end: j, text: stem[i:j]})
		i = j
	}
	return spans
}

// isLangToken reports whether a filename token names a language
// (any form langCodeNormal knows, plus "und").
func isLangToken(tok string) bool {
	t := lowerASCII(tok)
	return t == "und" || langCodeNormal(t) != ""
}

// planRelabelName computes the new BASE name for an external
// subtitle finding: the trailing language token replaced by the
// detected code (flag suffixes preserved in place), or the code
// inserted before trailing flags when the name has no language
// token. It is a pure function of the finding — existence and
// collision checks happen in planRelabelLocked.
func planRelabelName(f SubtitleFinding) (string, error) {
	if f.Source != "external" {
		return "", fmt.Errorf("embedded tracks live inside the media file — this app never rewrites media files, so there is nothing to rename")
	}
	if f.Detected == "" {
		return "", fmt.Errorf("no detected language to relabel to")
	}
	base := filepath.Base(f.Path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	spans := stemTokenSpans(stem)
	// Walk the trailing run: flags may follow the language token.
	j := len(spans)
	for j > 0 && subtitleFlagTokens[lowerASCII(spans[j-1].text)] {
		j--
	}
	if j > 0 && isLangToken(spans[j-1].text) {
		sp := spans[j-1]
		if lowerASCII(sp.text) == f.Detected || langCodeNormal(lowerASCII(sp.text)) == f.Detected {
			return "", fmt.Errorf("the filename already labels this %s", f.Detected)
		}
		return stem[:sp.start] + f.Detected + stem[sp.end:] + ext, nil
	}
	// No language token: insert before the trailing flags, or
	// append when there are none.
	if j < len(spans) {
		sp := spans[j]
		if sp.start == 0 {
			return f.Detected + "." + stem + ext, nil
		}
		return stem[:sp.start-1] + "." + f.Detected + stem[sp.start-1:] + ext, nil
	}
	return stem + "." + f.Detected + ext, nil
}

// currentFindingLocked finds the CURRENT published finding for
// an action: the filtered list (audit-off, exclusions and
// dismissals applied) — exactly what the UI is showing. Caller
// holds s.mu.
func (s *Service) currentSubtitleFindingLocked(path string) (SubtitleFinding, bool) {
	for _, f := range s.filteredSubtitlesLocked() {
		if f.Path == path {
			return f, true
		}
	}
	return SubtitleFinding{}, false
}

// planRelabelLocked plans one relabel against current state:
// the finding must be current, external, and plannable, and
// the target's on-disk existence is reported as Collision (a
// plan fact, not an error — apply decides to skip it).
// Caller holds s.mu.
func (s *Service) planRelabelLocked(path string) RelabelEntry {
	e := RelabelEntry{From: path}
	f, ok := s.currentSubtitleFindingLocked(path)
	if !ok {
		// Say the quiet part when the path IS a finding, just an
		// embedded one: the refusal reason is different and the
		// spec wants it in the copy, not discovered by trial.
		for _, raw := range s.results.Subtitles {
			if raw.Path == path && raw.Source == "embedded" {
				e.Error = "embedded tracks live inside the media file — this app never rewrites media files, so there is nothing to rename"
				return e
			}
		}
		e.Error = "not a current external subtitle finding (rescan, or remove a verified mark in Settings)"
		return e
	}
	e.Detected = f.Detected
	name, err := planRelabelName(f)
	if err != nil {
		e.Error = err.Error()
		return e
	}
	e.To = filepath.Join(filepath.Dir(path), name)
	if _, err := os.Lstat(e.To); err == nil {
		e.Collision = true
	}
	return e
}

// RelabelPreview plans relabels for the given finding paths
// without moving anything (POST /api/subtitles/relabel/preview).
func (s *Service) RelabelPreview(paths []string) []RelabelEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RelabelEntry, 0, len(paths))
	for _, p := range paths {
		out = append(out, s.planRelabelLocked(p))
	}
	return out
}

// RelabelApply executes relabels for the given finding paths.
// Plans are recomputed server-side (a preview is advice, not
// authority), the manifest is written before the first rename,
// collisions are skipped and never overwritten, and the
// published results drop the renamed files' findings at once —
// the finding's premise (the old name) no longer exists. A
// mid-apply rename failure stops the run; what already moved
// stays moved and undoable via the manifest.
func (s *Service) RelabelApply(paths []string) (*RelabelApplyResult, error) {
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	defer s.endFileOp()
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &RelabelApplyResult{Applied: []RelabelEntry{}, Failed: []RelabelEntry{}}
	var moves []subtitleRenameMove
	var entries []RelabelEntry
	for _, p := range paths {
		e := s.planRelabelLocked(p)
		switch {
		case e.Error != "":
			res.Failed = append(res.Failed, e)
		case e.Collision:
			e.Error = "a file already exists at the target name — skipped, never overwritten"
			res.Failed = append(res.Failed, e)
		default:
			moves = append(moves, subtitleRenameMove{From: e.From, To: e.To})
			entries = append(entries, e)
		}
	}
	if len(moves) == 0 {
		return res, nil
	}
	m := &subtitleManifest{
		ID:        s.newSubtitleManifestID(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Moves:     moves,
	}
	if err := s.writeSubtitleManifest(m); err != nil {
		return nil, fmt.Errorf("cannot write the undo record, nothing was renamed: %w", err)
	}
	res.ManifestID = m.ID
	for i, mv := range moves {
		if err := os.Rename(mv.From, mv.To); err != nil {
			e := entries[i]
			e.Error = err.Error()
			res.Failed = append(res.Failed, e)
			break
		}
		m.Moves[i].Done = true
		res.Applied = append(res.Applied, entries[i])
		if werr := s.writeSubtitleManifest(m); werr != nil {
			e := entries[i]
			e.Error = "the file was renamed, but the undo record could not be updated: " + werr.Error()
			res.Failed = append(res.Failed, e)
			break
		}
	}
	if len(res.Applied) > 0 {
		s.afterRelabelAppliedLocked(res.Applied)
	}
	return res, nil
}

// afterRelabelAppliedLocked drops the renamed files' findings
// from the published results and re-points their sidecar paths,
// then recomputes the summary and persists — the same
// refresh-without-rescan discipline as
// removePathFromResultsLocked, minus the inventory surgery (the
// files still exist, under new names; the subtitle FILE count
// does not change). Caller holds s.mu.
func (s *Service) afterRelabelAppliedLocked(applied []RelabelEntry) {
	renamed := map[string]string{}
	for _, e := range applied {
		renamed[e.From] = e.To
	}
	subs := make([]SubtitleFinding, 0, len(s.results.Subtitles))
	for _, f := range s.results.Subtitles {
		if _, gone := renamed[f.Path]; gone && f.Source == "external" {
			continue
		}
		subs = append(subs, f)
	}
	s.results.Subtitles = subs
	for i, p := range s.results.SubtitleFilePaths {
		if to, ok := renamed[p]; ok {
			s.results.SubtitleFilePaths[i] = to
		}
	}
	s.results.Summary = s.currentSummaryLocked()
	s.state.Summary = s.results.Summary
	s.saveResultsLocked()
}

// RelabelUndo restores the original names recorded in a relabel
// manifest, in reverse order. Every recorded path must sit
// under a configured scan root (confinement: the manifest is
// data, not authority); a re-occupied original name is reported
// and left alone — undo never overwrites either. The manifest
// is marked undone only when every move restored, so a partial
// undo stays listed and retryable. Findings for the restored
// names return with the next scan (undo restores files, not
// scan snapshots — the same contract as Organize undo).
func (s *Service) RelabelUndo(manifestID string) (*RelabelUndoResult, error) {
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	defer s.endFileOp()
	m, err := s.loadSubtitleManifest(manifestID)
	if err != nil {
		return nil, err
	}
	if m.UndoneAt != "" {
		return nil, fmt.Errorf("relabel %s was already undone at %s", m.ID, m.UndoneAt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &RelabelUndoResult{Failed: []OrganizeFailure{}}
	for i := len(m.Moves) - 1; i >= 0; i-- {
		mv := &m.Moves[i]
		if !mv.Done {
			continue
		}
		confined := false
		for _, r := range s.cfg.Roots {
			if pathWithinRoot(r, mv.From) && pathWithinRoot(r, mv.To) {
				confined = true
				break
			}
		}
		if !confined {
			res.Failed = append(res.Failed, OrganizeFailure{
				Path:  mv.To,
				Error: "recorded path is outside every configured scan root — left in place",
			})
			continue
		}
		if _, err := os.Lstat(mv.From); err == nil {
			res.Failed = append(res.Failed, OrganizeFailure{
				Path:  mv.To,
				Error: "the original name is occupied again — file left where it is",
			})
			continue
		}
		if err := os.Rename(mv.To, mv.From); err != nil {
			res.Failed = append(res.Failed, OrganizeFailure{Path: mv.To, Error: err.Error()})
			continue
		}
		mv.Done = false
		res.Restored++
		_ = s.writeSubtitleManifest(m)
	}
	if len(res.Failed) == 0 {
		m.UndoneAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := s.writeSubtitleManifest(m); err != nil {
		return res, fmt.Errorf("files were restored, but the undo record could not be updated: %w", err)
	}
	return res, nil
}

// SubtitleManifests lists the relabel undo records, newest
// first (the wizard's "past relabels" list).
func (s *Service) SubtitleManifests() ([]SubtitleManifestSummary, error) {
	dir := s.subtitleManifestsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []SubtitleManifestSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []SubtitleManifestSummary{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m subtitleManifest
		if json.Unmarshal(b, &m) != nil || m.ID == "" {
			continue
		}
		out = append(out, SubtitleManifestSummary{
			ID: m.ID, CreatedAt: m.CreatedAt,
			Moves: len(m.Moves), Undone: m.UndoneAt != "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// subtitleManifestsDir is where relabel undo records live.
func (s *Service) subtitleManifestsDir() string {
	return filepath.Join(s.dataDir, "subtitle-manifests")
}

// newSubtitleManifestID mints a manifest ID from the timestamp,
// checking the disk so two applies in the same second never
// share a record (same discipline as newManifestID).
func (s *Service) newSubtitleManifestID() string {
	base := "subtitle-" + time.Now().UTC().Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(s.subtitleManifestsDir(), id+".json")); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// writeSubtitleManifest persists a manifest (tmp+rename).
func (s *Service) writeSubtitleManifest(m *subtitleManifest) error {
	dir := s.subtitleManifestsDir()
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

// loadSubtitleManifest reads one manifest by ID; the ID is
// validated to be a bare file name, exactly like
// loadOrganizeManifest.
func (s *Service) loadSubtitleManifest(id string) (*subtitleManifest, error) {
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid manifest id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(s.subtitleManifestsDir(), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("manifest %q not found", id)
	}
	var m subtitleManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %q is unreadable: %w", id, err)
	}
	return &m, nil
}
