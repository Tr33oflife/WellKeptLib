package core

// organize_apply.go — Organize execution: Apply and Undo (v0.3.0).
//
// This is the ONLY code in the app that moves files, and it is built
// to fail closed, in line with the SPEC safety model:
//
//   - Apply runs only against a plan the user previewed (plans are
//     held in memory by ID; a restart means previewing again).
//   - Before the first move, EVERY move is re-validated (source
//     still exists, target still free, both still inside the plan
//     root). Any failure aborts the whole apply — the tree may have
//     changed since the preview, and a partial surprise is worse
//     than none.
//   - A manifest is written to disk BEFORE the first file moves and
//     rewritten after every file, so even a crash mid-apply leaves
//     an accurate undo record. Manifests live in
//     <dataDir>/organize-manifests/ and survive restarts — Undo
//     always reads from disk, never from memory.
//   - Moves are os.Rename only (same root = same filesystem by
//     construction). There is deliberately NO copy+delete fallback.
//   - Plan and Apply (and Undo) refuse to run while a scan is
//     running: no file action happens during a scan (SPEC rule).
//   - Undo never overwrites: if an original location is occupied
//     again, that file is reported and left where it is; the rest
//     still restore. Created folders are removed only when empty.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// errScanRunning is the shared refusal for Organize operations
// requested during a scan.
var errScanRunning = errors.New("a scan is running — wait for it to finish")

// errFileOpBusy is the refusal for a second file operation while an
// Organize apply/undo already holds the guard (v0.4.1).
var errFileOpBusy = errors.New("another organize operation is in progress — wait for it to finish")

// beginFileOp takes the file-operation guard (v0.4.1) for the whole
// duration of an Apply or Undo. Before this, Apply/Undo checked
// state.Running only at entry and set no flag of their own: a scan
// could START mid-apply (StartScan likewise only checked Running),
// and two applies — or an apply and an undo of the same manifest —
// could interleave on the same manifest and .tmp paths. Taking the
// guard makes the exclusion mutual in both directions: StartScan
// refuses while the guard is held (see service.go), and a second
// file operation refuses with errFileOpBusy. The per-file deletes
// (Health / Duplicates) do NOT need the guard: they hold s.mu
// continuously through validation and the remove itself, so no
// scan or organize can interleave inside one.
func (s *Service) beginFileOp() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Running {
		return errScanRunning
	}
	if s.fileOpBusy {
		return errFileOpBusy
	}
	s.fileOpBusy = true
	return nil
}

// endFileOp releases the guard taken by beginFileOp.
func (s *Service) endFileOp() {
	s.mu.Lock()
	s.fileOpBusy = false
	s.mu.Unlock()
}

// OrganizeFailure is one file an apply or undo could not process,
// with the reason. Returned in-band (not as a Go error) so the UI can
// show partial progress honestly.
type OrganizeFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// OrganizeApplyResult reports what an Apply call did. When
// re-validation fails, Applied is 0, Failed lists every problem, and
// ManifestID is empty (nothing was written or moved). When a move
// fails mid-apply, Applied counts the files that DID move and
// ManifestID names the manifest that can undo them.
type OrganizeApplyResult struct {
	ManifestID    string            `json:"manifest_id,omitempty"`
	Applied       int               `json:"applied"`
	Failed        []OrganizeFailure `json:"failed"`
	EmptyDirsLeft int               `json:"empty_dirs_left"`
}

// OrganizeUndoResult reports what an Undo call did.
type OrganizeUndoResult struct {
	Restored int               `json:"restored"`
	Failed   []OrganizeFailure `json:"failed"`
}

// organizeManifest is the on-disk undo record for one applied plan.
// It is written before the first move and updated after each move,
// so at any moment it describes exactly what has happened so far.
type organizeManifest struct {
	ID          string                 `json:"id"`
	CreatedAt   string                 `json:"created_at"`
	Root        string                 `json:"root"`
	CreatedDirs []string               `json:"created_dirs"`
	Moves       []organizeManifestMove `json:"moves"`
	UndoneAt    string                 `json:"undone_at,omitempty"`
}

// organizeManifestMove is one recorded move in a manifest. Done
// flips to true as the move lands (and back to false when Undo
// restores it), which is what makes a partial undo retryable.
type organizeManifestMove struct {
	From string `json:"from"`
	To   string `json:"to"`
	Done bool   `json:"done"`
}

// OrganizeManifestSummary is one row of the "past organizes" list.
type OrganizeManifestSummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Root      string `json:"root"`
	Moves     int    `json:"moves"`
	Undone    bool   `json:"undone"`
}

// OrganizePlan computes a plan via PlanOrganize and remembers it
// under its ID so Apply can execute exactly what was previewed.
// Refuses while a scan is running.
func (s *Service) OrganizePlan(root string) (*OrganizePlan, error) {
	s.mu.Lock()
	running := s.state.Running
	s.mu.Unlock()
	if running {
		return nil, errScanRunning
	}
	plan, err := PlanOrganize(root)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.orgPlans == nil {
		s.orgPlans = map[string]*OrganizePlan{}
	}
	s.orgPlans[plan.ID] = plan
	s.mu.Unlock()
	return plan, nil
}

// revalidateMove checks one planned move against the live tree just
// before Apply: the source must still be a regular file, the target
// must still be free, and both must still be inside the plan root.
// It returns "" when the move is safe, or the failure reason.
func revalidateMove(root string, mv OrganizeMove) string {
	if !pathWithinRoot(root, mv.From) {
		return "source is no longer inside the organized folder"
	}
	if !pathWithinRoot(root, mv.To) {
		return "target is no longer inside the organized folder"
	}
	info, err := os.Lstat(mv.From)
	if err != nil {
		return "source no longer exists"
	}
	if !info.Mode().IsRegular() {
		return "source is no longer a regular file"
	}
	if _, err := os.Lstat(mv.To); err == nil {
		return "a file already exists at the target path"
	} else if !os.IsNotExist(err) {
		return "cannot check the target path: " + err.Error()
	}
	return ""
}

// OrganizeApply executes a previously previewed plan. See the file
// header for the fail-closed contract. A mid-apply failure stops the
// run; what already moved stays moved and undoable via the manifest.
func (s *Service) OrganizeApply(planID string) (*OrganizeApplyResult, error) {
	// v0.4.1: hold the file-operation guard for the WHOLE apply —
	// no scan can start and no second apply/undo can interleave
	// while files are moving.
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	defer s.endFileOp()
	s.mu.Lock()
	plan, ok := s.orgPlans[planID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown plan %q — preview the plan again", planID)
	}
	res := &OrganizeApplyResult{Failed: []OrganizeFailure{}}

	// Re-validate EVERY move before touching anything: all-or-nothing
	// at the gate.
	for _, mv := range plan.Moves {
		if msg := revalidateMove(plan.Root, mv); msg != "" {
			res.Failed = append(res.Failed, OrganizeFailure{Path: mv.From, Error: msg})
		}
	}
	if len(res.Failed) > 0 {
		return res, nil
	}

	// Manifest FIRST, before the first move.
	m := &organizeManifest{
		ID:          s.newManifestID(),
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Root:        plan.Root,
		CreatedDirs: []string{},
	}
	for _, mv := range plan.Moves {
		m.Moves = append(m.Moves, organizeManifestMove{From: mv.From, To: mv.To})
	}
	if err := s.writeOrganizeManifest(m); err != nil {
		return nil, fmt.Errorf("cannot write the undo manifest, nothing was moved: %w", err)
	}
	res.ManifestID = m.ID

	// Execute. After each landed move, flip its Done flag and rewrite
	// the manifest so the undo record is never behind reality.
	for i, mv := range plan.Moves {
		created, err := ensureParentDirs(mv.To)
		if err == nil && len(created) > 0 {
			m.CreatedDirs = appendStringsUnique(m.CreatedDirs, created...)
			err = s.writeOrganizeManifest(m)
		}
		if err == nil {
			err = os.Rename(mv.From, mv.To)
		}
		if err != nil {
			res.Failed = append(res.Failed, OrganizeFailure{Path: mv.From, Error: err.Error()})
			break
		}
		m.Moves[i].Done = true
		res.Applied++
		if werr := s.writeOrganizeManifest(m); werr != nil {
			res.Failed = append(res.Failed, OrganizeFailure{
				Path:  mv.From,
				Error: "the file moved, but the undo manifest could not be updated: " + werr.Error(),
			})
			break
		}
	}
	res.EmptyDirsLeft = countEmptyDirs(plan.Root)
	return res, nil
}

// OrganizeUndo reverses one applied organize, reading its manifest
// from disk (so it works across restarts). Moves restore in reverse
// order; original folders are recreated as needed; folders the apply
// created are removed afterwards, deepest first, only when empty.
// A restore blocked by a re-occupied original path is reported and
// skipped — undo never overwrites. The manifest is marked undone
// only when every move restored, so a partial undo can be retried
// (already-restored moves are flipped back to Done=false in the
// manifest and are skipped on the retry).
func (s *Service) OrganizeUndo(manifestID string) (*OrganizeUndoResult, error) {
	// v0.4.1: same guard as Apply, held for the whole undo.
	if err := s.beginFileOp(); err != nil {
		return nil, err
	}
	defer s.endFileOp()
	m, err := s.loadOrganizeManifest(manifestID)
	if err != nil {
		return nil, err
	}
	if m.UndoneAt != "" {
		return nil, fmt.Errorf("organize %s was already undone at %s", m.ID, m.UndoneAt)
	}
	res := &OrganizeUndoResult{Failed: []OrganizeFailure{}}
	for i := len(m.Moves) - 1; i >= 0; i-- {
		mv := &m.Moves[i]
		if !mv.Done {
			continue
		}
		// v0.4.1 confinement: the manifest is data, not authority.
		// Apply re-validates every planned move against the plan
		// root before moving anything, but Undo used to trust the
		// manifest JSON's From/To absolutely — a corrupted or
		// hand-edited manifest could rename arbitrary files
		// anywhere on disk. Both recorded paths must sit inside
		// the manifest's own organized root; a violating move is
		// reported, skipped, and never renamed. (Landing in Failed
		// also keeps the manifest un-marked as undone, so the
		// partial undo stays listed and retryable.)
		if !pathWithinRoot(m.Root, mv.To) || !pathWithinRoot(m.Root, mv.From) {
			res.Failed = append(res.Failed, OrganizeFailure{
				Path:  mv.To,
				Error: "recorded path is outside the organized folder — left in place",
			})
			continue
		}
		if _, err := os.Lstat(mv.From); err == nil {
			res.Failed = append(res.Failed, OrganizeFailure{
				Path:  mv.To,
				Error: "the original location is occupied again — file left where it is",
			})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(mv.From), 0o755); err != nil {
			res.Failed = append(res.Failed, OrganizeFailure{Path: mv.To, Error: err.Error()})
			continue
		}
		if err := os.Rename(mv.To, mv.From); err != nil {
			res.Failed = append(res.Failed, OrganizeFailure{Path: mv.To, Error: err.Error()})
			continue
		}
		mv.Done = false
		res.Restored++
		// Keep the on-disk manifest truthful move by move; a failed
		// bookkeeping write is not worth aborting the restore over —
		// the final write below reports itself if it fails.
		_ = s.writeOrganizeManifest(m)
	}
	// Remove the folders Apply created, deepest first, only when now
	// empty (os.Remove fails on a non-empty folder, which is exactly
	// the guard wanted — errors here are expected and ignored).
	created := append([]string{}, m.CreatedDirs...)
	sort.Slice(created, func(i, j int) bool { return pathDepth(created[i]) > pathDepth(created[j]) })
	for _, d := range created {
		_ = os.Remove(d)
	}
	if len(res.Failed) == 0 {
		m.UndoneAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := s.writeOrganizeManifest(m); err != nil {
		return res, fmt.Errorf("files were restored, but the manifest could not be updated: %w", err)
	}
	return res, nil
}

// OrganizeManifests lists every undo manifest in the data dir,
// newest first. Manifests for organizes that were fully undone stay
// listed (marked undone) as the action history.
func (s *Service) OrganizeManifests() ([]OrganizeManifestSummary, error) {
	dir := s.organizeManifestsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []OrganizeManifestSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []OrganizeManifestSummary{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var m organizeManifest
		if json.Unmarshal(b, &m) != nil || m.ID == "" {
			continue
		}
		out = append(out, OrganizeManifestSummary{
			ID: m.ID, CreatedAt: m.CreatedAt, Root: m.Root,
			Moves: len(m.Moves), Undone: m.UndoneAt != "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// organizeManifestsDir is where undo manifests live in the data dir.
func (s *Service) organizeManifestsDir() string {
	return filepath.Join(s.dataDir, "organize-manifests")
}

// newManifestID mints a manifest ID from the timestamp, checking the
// disk so two applies in the same second never share a manifest.
func (s *Service) newManifestID() string {
	base := "organize-" + time.Now().UTC().Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(s.organizeManifestsDir(), id+".json")); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// writeOrganizeManifest persists a manifest (write temp + rename, so
// a crash never leaves a half-written undo record).
func (s *Service) writeOrganizeManifest(m *organizeManifest) error {
	dir := s.organizeManifestsDir()
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

// loadOrganizeManifest reads one manifest from disk by ID. The ID is
// validated to be a bare file name — it is never allowed to become a
// path outside the manifests folder.
func (s *Service) loadOrganizeManifest(id string) (*organizeManifest, error) {
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return nil, fmt.Errorf("invalid manifest id %q", id)
	}
	b, err := os.ReadFile(filepath.Join(s.organizeManifestsDir(), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("manifest %q not found", id)
	}
	var m organizeManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %q is unreadable: %w", id, err)
	}
	return &m, nil
}

// ensureParentDirs creates the parent folder chain of a target file
// and reports which folders did not exist before (shallow first) —
// those are the folders the manifest records as created, so Undo can
// remove them again once empty.
func ensureParentDirs(target string) ([]string, error) {
	dir := filepath.Dir(target)
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		missing = append(missing, d) // collected deep -> shallow
		if filepath.Dir(d) == d {
			break
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Reverse into shallow -> deep order for the manifest.
	created := make([]string, 0, len(missing))
	for i := len(missing) - 1; i >= 0; i-- {
		created = append(created, missing[i])
	}
	return created, nil
}

// countEmptyDirs counts the folders under root (excluding root
// itself) that hold no file at or below them after an apply. Apply
// never deletes folders; this count tells the user how many empty
// shells the organize left behind.
func countEmptyDirs(root string) int {
	fileDirs := map[string]bool{}
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root {
				dirs = append(dirs, path)
			}
			return nil
		}
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			fileDirs[dir] = true
			if dir == root || filepath.Dir(dir) == dir {
				break
			}
		}
		return nil
	})
	empty := 0
	for _, d := range dirs {
		if !fileDirs[d] {
			empty++
		}
	}
	return empty
}

// pathDepth counts separators in a path (for deepest-first ordering).
func pathDepth(p string) int {
	return strings.Count(filepath.ToSlash(p), "/")
}

// appendStringsUnique appends values that are not already present.
func appendStringsUnique(list []string, vals ...string) []string {
	for _, v := range vals {
		found := false
		for _, x := range list {
			if x == v {
				found = true
				break
			}
		}
		if !found {
			list = append(list, v)
		}
	}
	return list
}
