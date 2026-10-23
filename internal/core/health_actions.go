package core

// health_actions.go — the two per-finding actions on the Health tab:
// "Open location" (reveal) and a guarded "Delete file". The shared
// halves of both actions — the reveal launcher + result, the
// scan/root/regular-file delete guards, the audit-log append, and
// the post-delete results mutation — also serve the Duplicates
// tab's actions (duplicates_actions.go), which differ only in how
// the path is validated (current duplicate group member, plus
// last-copy protection) and what the audit record says.
//
// WHY THE VALIDATION IS THIS STRICT: these are the first endpoints
// in the app that act on an arbitrary-looking path from the client,
// and the app has no login of its own (on Ultra the proxied URL is
// reachable by anyone who has the link). So neither action accepts
// "any path": the path must be one of the CURRENT health findings —
// exactly the list /api/health is showing — and delete additionally
// requires the path to sit under a configured scan root and to be a
// regular file. The worst a caller can do with these endpoints is
// delete a file the scanner itself just flagged as empty/corrupt,
// inside a folder the user configured as a library root. Folders
// are never deleted: an "empty-folder" finding is reveal-only.
//
// The actions live on Service like every other operation
// (transport separation): server.go handlers decode -> call ->
// encode, nothing more.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RevealResult answers a reveal request. Mode is "revealed" when
// an OS file-manager open was actually launched on this host, or
// "path" when nothing was launched (headless host) and the UI
// should show the path with a copy button instead. Never claim
// "revealed" when nothing was launched.
type RevealResult struct {
	Mode string `json:"mode"` // revealed | path
	Path string `json:"path"`
}

// DeleteHealthResult answers a successful delete: the path, plus
// the refreshed summary so the UI can update its counts at once
// (it also refetches state via the normal render flow).
type DeleteHealthResult struct {
	OK      bool        `json:"ok"`
	Path    string      `json:"path"`
	Summary ScanSummary `json:"summary"`
}

// DeletedAction is one entry of the on-disk deletion audit log
// (<dataDir>/deleted-actions.json). Deleting is permanent; the log
// is the record of what went, when, how big it was, and what
// condemned it.
type DeletedAction struct {
	Time        string `json:"time"` // RFC3339 UTC
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	FindingKind string `json:"finding_kind"`     // the HealthFinding.Type acted on; "duplicate-copy" for a Duplicates delete
	Detail      string `json:"detail,omitempty"` // extra context: for a duplicates delete, the group's label
}

// healthFindingLocked returns the first CURRENT health finding for
// path — "current" meaning the latest results with the user's
// health exclusions applied, i.e. exactly what the Health tab is
// showing. Caller holds s.mu.
func (s *Service) healthFindingLocked(path string) (HealthFinding, bool) {
	for _, f := range s.filteredHealthLocked() {
		if f.Path == path {
			return f, true
		}
	}
	return HealthFinding{}, false
}

// RevealHealthPath validates path as a current health finding and
// asks the local desktop to show it in its file manager. On a
// headless host (the Ultra deployment) nothing is launched and the
// answer is mode "path" so the UI can offer copy-the-path instead.
func (s *Service) RevealHealthPath(path string) (RevealResult, error) {
	s.mu.Lock()
	_, ok := s.healthFindingLocked(path)
	s.mu.Unlock()
	if !ok {
		return RevealResult{}, fmt.Errorf("not a current health finding: %s", path)
	}
	// The mutex is released before launching an external process:
	// starting Explorer must never block the service.
	return revealPath(path), nil
}

// revealPath launches the local file manager at path (via the
// seam) and reports honestly what happened: mode "revealed" only
// when a launch actually occurred, else mode "path" so the UI
// can show the path with a copy button. Shared by the Health and
// Duplicates reveals — the launch behaviour is identical, only
// the callers' path validation differs.
func revealPath(path string) RevealResult {
	if revealInFileManagerFn(path) {
		return RevealResult{Mode: "revealed", Path: path}
	}
	return RevealResult{Mode: "path", Path: path}
}

// revealInFileManagerFn is a seam for tests (same pattern as
// extractEmbeddedSubtitleFn in scanner.go): the launch decision
// is stubbed there, the real OS behaviour below.
var revealInFileManagerFn = revealInFileManager

// revealInFileManager launches the host's file manager showing
// path (selected on Windows, its folder elsewhere) and reports
// whether anything was launched at all. Platform choice is a
// runtime.GOOS switch (no build tags), matching openBrowser in
// main.go, because the app cross-compiles from one tree. The
// command construction itself lives in the pure revealCommand
// below, so the exact per-platform spec is unit-testable from
// any host.
func revealInFileManager(path string) bool {
	hasDesktop := os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	_, lookErr := exec.LookPath("xdg-open")
	name, args, ok := revealCommand(runtime.GOOS, path, hasDesktop, lookErr == nil)
	if !ok {
		return false
	}
	// Start-only: explorer exits non-zero even on success, so its
	// exit code can never be the success signal.
	return exec.Command(name, args...).Start() == nil
}

// revealCommand is the pure command-construction half of the
// reveal: given the platform, the path, and (for Linux) whether
// a desktop session exists and xdg-open is on PATH, it returns
// the file-manager command to launch — or ok=false when nothing
// should be launched on this host (headless: the caller answers
// mode "path" instead).
//
// The Windows spec is TWO arguments on purpose: "explorer",
// "/select," and then the path as its own argument. v0.3.5
// shipped a single "/select,"+path argument; os/exec quotes an
// argument containing spaces as a whole, so for real library
// paths the command line became
//
//	explorer "/select,C:\path with spaces\file.mkv"
//
// — the switch itself inside quotes, which Explorer ignores: it
// opened its default view (the user's Documents) instead of the
// file's folder. That is the user's live bug report this fixes.
// With two arguments, "/select," stays a bare token and the path
// is quoted on its own — the shape in Microsoft's documentation
// of the switch.
func revealCommand(goos, path string, hasDesktop, haveXdgOpen bool) (name string, args []string, ok bool) {
	switch goos {
	case "windows":
		return "explorer", []string{"/select,", path}, true
	case "darwin":
		return "open", []string{"-R", path}, true
	default:
		// Linux and friends: without a desktop session there is
		// nothing on this host to open (the Ultra case), and
		// without xdg-open there is nothing to open it with.
		if !hasDesktop || !haveXdgOpen {
			return "", nil, false
		}
		return "xdg-open", []string{filepath.Dir(path)}, true
	}
}

// DeleteHealthFile permanently deletes ONE file behind the full
// guard chain, in order:
//  1. path is a current health finding (see healthFindingLocked);
//  2. no scan is running (a running scan would re-publish its
//     walk snapshot over the mutation — Organize refuses during
//     scans for the same reason);
//  3. path lies under one of the configured scan roots
//     (pathWithinRoot — the Organize confinement helper);
//  4. the target exists and is a regular file (directories are
//     refused outright: this feature deletes files only).
//
// After the delete it appends to the audit log, removes the file
// from the published results without a rescan (item + every
// finding naming the path; duplicate groups are rebuilt from the
// remaining items by the normal summary path), and persists.
func (s *Service) DeleteHealthFile(path string) (DeleteHealthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	finding, ok := s.healthFindingLocked(path)
	if !ok {
		return DeleteHealthResult{}, fmt.Errorf("not a current health finding: %s", path)
	}
	info, err := s.checkDeletableFileLocked(path)
	if err != nil {
		return DeleteHealthResult{}, err
	}
	if err := removeFile(path); err != nil {
		return DeleteHealthResult{}, fmt.Errorf("delete failed for %s: %v", path, err)
	}
	// Best-effort audit log, mirroring the other savers in this
	// package (they persist silently): the file is already gone,
	// so a log-write hiccup must not report the delete as failed.
	_ = s.appendDeletedActionLocked(DeletedAction{
		Time:        time.Now().UTC().Format(time.RFC3339),
		Path:        path,
		Size:        info.Size(),
		FindingKind: finding.Type,
	})
	s.removePathFromResultsLocked(path)
	return DeleteHealthResult{OK: true, Path: path, Summary: s.currentSummaryLocked()}, nil
}

// removeFileFn / chmodFileFn are seams for the read-only retry
// test (the function-variable pattern used across this
// package): POSIX cannot be made to fail os.Remove through a
// file's own mode bits — unlink is governed by the DIRECTORY's
// permissions — so the retry LOGIC is what the test pins
// (first failure, chmod, single retry, outcome), with these
// two operations stubbed.
var removeFileFn = os.Remove
var chmodFileFn = os.Chmod

// removeFile deletes path, with the Windows read-only retry:
// Windows refuses to delete a read-only file, so on failure
// the file is made writable (0666) and the remove retried
// ONCE; the second error, if any, is what the caller sees.
// POSIX ignores the read-only flag for unlink, so the retry
// simply never engages there. Shared by the Health and
// Duplicates deletes.
func removeFile(path string) error {
	if err := removeFileFn(path); err != nil {
		_ = chmodFileFn(path, 0o666)
		return removeFileFn(path)
	}
	return nil
}

// checkDeletableFileLocked runs the delete guards that do not
// depend on WHY the path is being deleted (the caller validates
// the path against its own current list first — a health
// finding, a duplicate copy): no scan running (a running scan
// would re-publish its walk snapshot over the mutation —
// Organize refuses during scans for the same reason); the path
// lies under a configured scan root (pathWithinRoot — the
// Organize confinement helper); the target exists and is a
// regular file (directories are refused outright: these
// features delete files only). Shared by the Health and
// Duplicates deletes. Caller holds s.mu.
func (s *Service) checkDeletableFileLocked(path string) (os.FileInfo, error) {
	if s.state.Running {
		return nil, fmt.Errorf("a scan is running — delete again when it finishes")
	}
	underRoot := false
	for _, r := range s.cfg.Roots {
		if pathWithinRoot(r, path) {
			underRoot = true
			break
		}
	}
	if !underRoot {
		return nil, fmt.Errorf("refusing to delete %s: it is not under any configured scan root", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot check %s: %v", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("refusing to delete %s: it is a folder — delete files only", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing to delete %s: not a regular file", path)
	}
	return info, nil
}

// appendDeletedActionLocked appends one record to
// <dataDir>/deleted-actions.json (created when absent; an
// unparseable existing file starts over rather than blocking a
// delete). Read-modify-write with the tmp+rename pattern of
// saveResultsLocked. Caller holds s.mu.
func (s *Service) appendDeletedActionLocked(rec DeletedAction) error {
	file := filepath.Join(s.dataDir, "deleted-actions.json")
	list := []DeletedAction{}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	list = append(list, rec)
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// removePathFromResultsLocked drops path from the published
// results after its file was deleted: the media item (when it was
// one), every health finding, and every subtitle finding naming
// it. Duplicate groups need no surgery — they are never stored;
// currentSummaryLocked rebuilds them from the remaining items.
// The stored summary's base fields are refreshed the same way
// the scanner's assembleAndPersist builds them (item count,
// total bytes, subtitle-file count), then the dynamic counts are
// recomputed and the results persisted. Caller holds s.mu.
func (s *Service) removePathFromResultsLocked(path string) {
	items := make([]*Item, 0, len(s.results.Items))
	for _, it := range s.results.Items {
		if it.Path != path {
			items = append(items, it)
		}
	}
	s.results.Items = items
	subs := make([]SubtitleFinding, 0, len(s.results.Subtitles))
	for _, f := range s.results.Subtitles {
		if f.Path != path {
			subs = append(subs, f)
		}
	}
	s.results.Subtitles = subs
	health := make([]HealthFinding, 0, len(s.results.Health))
	for _, f := range s.results.Health {
		if f.Path != path {
			health = append(health, f)
		}
	}
	s.results.Health = health
	var total int64
	for _, it := range items {
		total += it.Size
	}
	s.results.Summary.MediaCount = len(items)
	s.results.Summary.TotalBytes = total
	// A deleted external subtitle file leaves the inventory too:
	// the scan counts subtitle FILES, not findings, so mirror the
	// removal there (floored at zero).
	if subtitleExts[strings.ToLower(filepath.Ext(path))] && s.results.Summary.SubtitleFileCount > 0 {
		s.results.Summary.SubtitleFileCount--
	}
	s.results.Summary = s.currentSummaryLocked()
	s.state.Summary = s.results.Summary
	s.saveResultsLocked()
}
