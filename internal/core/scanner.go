package core

// scanner.go — the read-only library scanner.
//
// Phases: walk roots (no symlink following) -> probe media with a
// bounded worker pool (ffprobe only; probe cache keyed by
// path+size+mtime) -> audit external subtitles -> sampled hashes
// for same-size files -> ASSEMBLY A publishes the library results
// -> audit embedded subtitles (its findings are cached with the
// probe, so unchanged files are never re-extracted) -> ASSEMBLY B
// merges the embedded findings in and publishes the final results.
//
// v0.3.4: the embedded audit used to run INSIDE the probe step, so
// the probe counter waited on ffmpeg subtitle extraction and no
// results existed until the slowest phase ended — on a real
// library a scan could sit at "probing 27/11375" with nothing to
// show. Extraction is now its own late phase: results land first,
// the audit streams in after, and its cost is paid once per file
// fingerprint, not once per scan.
// A scan never renames, moves, or deletes anything.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// mediaExts are the video container extensions the scanner inventories.
var mediaExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true,
	".wmv": true, ".ts": true, ".m2ts": true, ".webm": true,
}

// subtitleExts are the external subtitle extensions the audit reads.
var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".vtt": true, ".sub": true,
}

// maxTextSubtitleSamplesPerFile caps how many embedded TEXT
// subtitle streams are extracted (and language-audited) per file
// during a scan (v0.3.1 scan-speed cap). Streams past the cap are
// reported as cannot-verify with the reason, never silently
// skipped.
//
// v0.5.0: the cap is Config.SubtitleMaxTracks (Settings-tunable);
// this const remains as the DEFAULT the config falls back to when
// a Config reaches the audit unclamped.
const maxTextSubtitleSamplesPerFile = 6

// fileRef is one discovered file during the walk phase.
type fileRef struct {
	Path  string
	Root  string
	Size  int64
	Mtime int64 // unix nano
	// Dev / Ino / Nlink (v1.0.0): the file's storage identity,
	// captured from the walk's own os.FileInfo (see fileid_*.go).
	// Zero Ino = unknown on this platform/filesystem.
	Dev   uint64
	Ino   uint64
	Nlink uint64
}

// driveKeepAliveInterval is how often a running scan touches each
// scan root to keep external drives awake (see startDriveKeepAlive).
const driveKeepAliveInterval = 20 * time.Second

// startDriveKeepAlive launches the scan-duration drive keep-alive
// and returns a stop function that ends it and waits for it to
// exit. The caller must defer the stop function so the goroutine
// cannot outlive the scan on any exit path.
//
// Why this exists (v0.3.4): the user's library lives on external
// USB hard drives, which spin down after short idle periods and
// cost 5-15 seconds to wake. A scan busy on one drive can leave
// another untouched long enough to sleep, then pay the wake-up
// cost mid-scan, file after file. Touching every root (one
// os.Stat each) on a short tick keeps all of them spinning for
// the scan's duration. It is deliberately app-side and temporary:
// it changes no system settings, and it runs only while a scan
// runs — never when the app is idle.
func startDriveKeepAlive(ctx context.Context, roots []string) (stop func()) {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(driveKeepAliveInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				for _, r := range roots {
					// Errors are ignored on purpose: the tick is
					// best-effort wakefulness, not validation (a
					// bad root already produced its own finding in
					// the walk phase).
					_, _ = os.Stat(r)
				}
			}
		}
	}()
	// stop is idempotent and blocks until the goroutine has
	// exited, so a caller asserting "no leak after return" can
	// rely on it. The scan context ending also stops the loop on
	// its own; stop covers scans that end without cancelling.
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-exited
	}
}

// setPhase updates progress under the service lock.
func (s *Service) setProgress(phase string, total, done int, current string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Phase = phase
	if total >= 0 {
		s.state.Total = total
	}
	if done >= 0 {
		s.state.Done = done
	}
	if current != s.state.Current {
		// v0.3.1: stamp when the current file changed so the UI can
		// show "on this file for Ns" — a slow file must read as
		// slow, not as a frozen scan (the user's 25/2500 report).
		if current == "" {
			s.state.CurrentSince = ""
		} else {
			s.state.CurrentSince = time.Now().UTC().Format(time.RFC3339)
		}
	}
	s.state.Current = current
}

// walkProgress carries the walk phase's live counters (v0.3.8).
//
// Before v0.3.8 the walk published NOTHING until every root had
// been walked, so on a slow volume (the user's SSHFS-Win mount of
// his seedbox) a scan sat at "walking 0/0" indefinitely and was
// indistinguishable from a hang. The tracker keeps counters LOCAL
// to the walk callback (no lock per entry — WalkDir is sequential)
// and flushes them to the published scan state through setProgress:
//
//   - Directory changes flush IMMEDIATELY, never throttled. The
//     WalkDir callback fires on a directory BEFORE its contents are
//     read, so if the descent stalls inside one folder's read, the
//     header is sitting on that folder's path — the user can see
//     exactly where the walk is stuck.
//   - The file counter flushes on a throttle (every
//     walkFlushEveryFiles counted files or walkFlushInterval,
//     whichever comes first) so a fast local walk doesn't hammer
//     the state lock.
//
// The published Done and Total are both the files-seen-so-far
// count: a walk has no knowable total, and the header renders
// "walking D/T" from those fields — equal, growing numbers are
// the honest display.
type walkProgress struct {
	svc *Service
	now func() time.Time // injectable for tests

	files   int // counted files seen (media + subtitles + incomplete)
	dirs    int // directories visited
	current string

	skippedDirs   int    // non-root walk errors (unreadable folders)
	firstSkipRoot string // root under which the first skip happened

	flushedFiles int
	lastFlush    time.Time
}

const (
	walkFlushEveryFiles = 100
	walkFlushInterval   = 200 * time.Millisecond
)

func newWalkProgress(svc *Service) *walkProgress {
	return &walkProgress{svc: svc, now: time.Now, lastFlush: time.Now()}
}

// dirVisited records the walk entering a directory. Current moves
// to it and flushes immediately (see the type comment): the
// callback runs before the directory's contents are read, so a
// stall inside that read leaves Current naming the stuck folder.
func (wp *walkProgress) dirVisited(path string) {
	wp.dirs++
	wp.current = path
	wp.flush()
}

// fileSeen records one counted file; the counter flush is
// throttled to every walkFlushEveryFiles files or
// walkFlushInterval, whichever comes first.
func (wp *walkProgress) fileSeen() {
	wp.files++
	if wp.files-wp.flushedFiles >= walkFlushEveryFiles || !wp.now().Before(wp.lastFlush.Add(walkFlushInterval)) {
		wp.flush()
	}
}

// dirError records a non-root walk error (a folder that could not
// be read and was skipped). The first error's root is remembered
// for the walk-warning finding's path.
func (wp *walkProgress) dirError(root string) {
	wp.skippedDirs++
	if wp.firstSkipRoot == "" {
		wp.firstSkipRoot = root
	}
}

// flush publishes the counters via setProgress (which takes the
// service lock briefly — throttled for files, immediate for
// directory changes, never held across walk work).
func (wp *walkProgress) flush() {
	wp.flushedFiles = wp.files
	wp.lastFlush = wp.now()
	wp.svc.setProgress("walking", wp.files, wp.files, wp.current)
}

// warningFinding returns the single walk-warning health finding
// for the walk, or nil when every folder was readable. It rides
// the results' health list (via the incomplete-findings channel)
// so skipped folders are visible after the scan, not just in a
// transient state.
func (wp *walkProgress) warningFinding() *HealthFinding {
	if wp.skippedDirs == 0 {
		return nil
	}
	return &HealthFinding{
		Path:     wp.firstSkipRoot,
		Type:     "walk-warning",
		Evidence: fmt.Sprintf("%d folders could not be read during the walk (skipped)", wp.skippedDirs),
	}
}

// runScan is the scan goroutine body (started by StartScan).
func (s *Service) runScan(ctx context.Context, roots []string) {
	scanStart := time.Now()
	finish := func(errMsg string) {
		s.mu.Lock()
		// v0.3.1: persist the probe cache on EVERY exit path —
		// completion, cancel, error. Before this, the store was
		// only saved at the end of a completed scan, so cancelling
		// a long first scan threw away every file already probed.
		s.saveStoreLocked()
		s.state.Running = false
		s.state.Phase = ""
		s.state.Current = ""
		s.state.CurrentSince = ""
		if errMsg != "" {
			s.state.LastError = errMsg
		}
		s.cancel = nil
		summary := s.currentSummaryLocked()
		s.mu.Unlock()
		// v1.0.0 webhook: fired AFTER the state is published and
		// results are persisted, from this goroutine's tail — and
		// the delivery itself is fire-and-forget (webhook.go), so
		// a dead endpoint can never delay or fail this path. A
		// completed scan announces scan_finished; an errored one
		// scan_failed; a CANCELLED scan announces nothing
		// (FEATURE-PLAN §6 default).
		switch {
		case errMsg == "":
			s.fireWebhook("scan_finished", summary)
		case errMsg != "scan cancelled":
			s.fireWebhook("scan_failed", summary)
		}
	}
	// Snapshot config for the scan duration.
	s.mu.Lock()
	cfg := *s.cfg
	s.mu.Unlock()

	// v0.3.4: keep the scan's drives awake for its duration (the
	// library lives on external USB drives that spin down when
	// idle). Deferred so the keep-alive goroutine stops on EVERY
	// exit path — completion, cancel, and error alike.
	stopKeepAlive := startDriveKeepAlive(ctx, roots)
	defer stopKeepAlive()

	// ---- Phase 1: walk ----
	var media []fileRef
	var subs []fileRef
	var sidecars []fileRef
	var incomplete []HealthFinding
	var dirs []string
	fileDirs := map[string]bool{} // dirs containing any file (directly or below)
	// v0.3.8: publish walk progress as the walk proceeds — on a
	// slow or stalled volume the header now counts files/folders
	// and names the folder being walked instead of sitting at 0/0.
	wp := newWalkProgress(s)
	for _, root := range roots {
		absRoot := cleanAbs(root)
		err := filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				// v0.1.1: an error AT THE ROOT (missing/unreadable root)
				// is returned so the caller records it as a finding —
				// v0.1.0 returned nil here, which swallowed the error
				// and made the scan "succeed" with zero files. Errors
				// below the root are still skipped, not fatal.
				if path == absRoot {
					return err
				}
				// v0.3.8: a non-root error here is a folder the
				// walk could not read — count it; the walk-warning
				// finding after the walk reports the total.
				wp.dirError(absRoot)
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil // never follow symlinks
			}
			if d.IsDir() {
				// v0.3.8: this callback fires BEFORE the folder's
				// contents are read, and dirVisited flushes Current
				// immediately — a stall inside this folder's read
				// leaves the header naming this exact folder.
				wp.dirVisited(path)
				if path != absRoot {
					dirs = append(dirs, path)
				}
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			ref := fileRef{Path: path, Root: absRoot, Size: info.Size(), Mtime: info.ModTime().UnixNano()}
			// v1.0.0: capture the storage identity while the
			// walk's stat is in hand (no extra IO on Linux; the
			// Windows route opens a handle — see fileid_windows.go).
			if dev, ino, nlink, ok := fileIdentityFor(info, path); ok {
				ref.Dev, ref.Ino, ref.Nlink = dev, ino, nlink
			}
			for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
				fileDirs[dir] = true
				if dir == absRoot || filepath.Dir(dir) == dir {
					break
				}
			}
			name := d.Name()
			ext := strings.ToLower(filepath.Ext(name))
			switch {
			case mediaExts[ext]:
				media = append(media, ref)
				wp.fileSeen()
			case subtitleExts[ext]:
				subs = append(subs, ref)
				wp.fileSeen()
			case ext == ".nfo" || organizeImageExts[ext]:
				// v0.9.0: .nfo and image files are recorded ONLY
				// so the health check can spot orphan sidecars —
				// they are not media, not subtitles, and change
				// no other count.
				sidecars = append(sidecars, ref)
				wp.fileSeen()
			case isIncompleteName(name):
				incomplete = append(incomplete, HealthFinding{
					Path: path, Type: "incomplete", Size: info.Size(),
					Evidence: "filename matches an incomplete-download pattern (" + filepath.Ext(name) + ")",
				})
				wp.fileSeen()
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			// A missing root is reported but does not kill the scan of others.
			incomplete = append(incomplete, HealthFinding{Path: absRoot, Type: "probe-failure", Evidence: "scan root unreadable: " + err.Error()})
		}
	}
	// v0.3.8: folders skipped during the walk surface as ONE
	// walk-warning health finding (it reaches the published
	// results through buildHealth's incomplete channel).
	if f := wp.warningFinding(); f != nil {
		incomplete = append(incomplete, *f)
	}
	if ctx.Err() != nil {
		finish("scan cancelled")
		return
	}
	var emptyFolders []string
	for _, d := range dirs {
		if !fileDirs[d] {
			emptyFolders = append(emptyFolders, d)
		}
	}
	sort.Strings(emptyFolders)
	// v0.9.0: a chain of nested empty folders (an empty Season
	// folder inside an empty Series folder) is ONE dead tree, not
	// a finding per level — keep only the top-most empty folder
	// of each chain (FEATURE-PLAN §5's precise definition).
	emptyFolders = topMostEmptyFolders(emptyFolders)

	// ---- Phase 2: probe media (bounded workers, ffprobe only) ----
	// v0.3.4: probing no longer extracts embedded subtitles (that
	// is the late audit phase, below), so the counter measures
	// ffprobe work only and moves at ffprobe speed. Each file's
	// ProbeResult is kept alongside its Item for the audit phase.
	s.setProgress("probing", len(media), 0, "")
	items := make([]*Item, len(media))
	probes := make([]*ProbeResult, len(media))
	sem := make(chan struct{}, cfg.Workers)
	var wg sync.WaitGroup
	doneCount := 0
	var doneMu sync.Mutex
	for i, ref := range media {
		wg.Add(1)
		go func(i int, ref fileRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			items[i], probes[i] = s.probeOne(ctx, ref, &cfg)
			doneMu.Lock()
			doneCount++
			d := doneCount
			doneMu.Unlock()
			if d%100 == 0 {
				// v0.3.1: incremental cache persistence — save the
				// probe store every 100 completed files so progress
				// survives an interrupted scan, not just a finished
				// one. Same mutex discipline as probeOne's cache
				// writes (the store is only ever touched under s.mu),
				// so this write can never race another one.
				// v0.4.1: the save itself is throttled (at most once
				// per incrementalStoreSaveInterval) — the every-100
				// cadence now triggers an ATTEMPT, so a fast 10k-file
				// probe phase no longer pays 100+ full-store writes.
				s.mu.Lock()
				s.saveStoreThrottledLocked()
				s.mu.Unlock()
			}
			s.setProgress("probing", -1, d, ref.Path)
		}(i, ref)
	}
	wg.Wait()
	if ctx.Err() != nil {
		finish("scan cancelled")
		return
	}

	// ---- Phase 3: external subtitle audit ----
	// v0.3.4: this list is EXTERNAL findings only. Embedded
	// findings join it at assembly B, after the audit phase.
	s.setProgress("subtitles", len(subs), 0, "")
	externalFindings := []SubtitleFinding{}
	subInfos := []subFileInfo{}
	for i, ref := range subs {
		if ctx.Err() != nil {
			finish("scan cancelled")
			return
		}
		info := subFileInfo{Path: ref.Path, Size: ref.Size}
		// v0.4.1: the audit returns the cue AND line counts from its
		// own capped parse — the scanner no longer re-reads the file
		// in full (uncapped) just to recount them.
		f, cues, lines, err := auditExternalSubtitle(ref.Path, cfg.SubtitleConfidence)
		if err != nil {
			info.ParseErr = err.Error()
		} else {
			info.Cues = cues
			info.Lines = lines
			if f != nil {
				externalFindings = append(externalFindings, *f)
			}
		}
		subInfos = append(subInfos, info)
		s.setProgress("subtitles", -1, i+1, ref.Path)
	}

	// ---- Phase 4: sampled hashes for same-size media ----
	s.setProgress("hashing", -1, -1, "")
	bySize := map[int64][]*Item{}
	for _, it := range items {
		if it != nil && it.Size > 0 {
			bySize[it.Size] = append(bySize[it.Size], it)
		}
	}
	for _, group := range bySize {
		if len(group) < 2 {
			continue
		}
		for _, it := range group {
			if h, err := sampledHash(it.Path, it.Size); err == nil {
				it.SampleHash = h
			}
		}
	}

	// ---- Assembly inputs: final item list + health ----
	finalItems := []*Item{}
	var totalBytes int64
	for _, it := range items {
		if it == nil {
			continue
		}
		finalItems = append(finalItems, it)
		totalBytes += it.Size
	}
	sort.Slice(finalItems, func(i, j int) bool { return finalItems[i].Path < finalItems[j].Path })
	health := buildHealth(finalItems, subInfos, incomplete, emptyFolders, sidecars, &cfg)

	// ---- Phase 5 / Assembly A: publish results BEFORE the audit ----
	// v0.3.4: the library, duplicates, health, and external-subtitle
	// findings are complete at this point — publish them now, with
	// AuditPending set, instead of holding everything hostage to
	// the embedded audit (the slowest phase). The user can browse
	// real results while the audit streams in behind them.
	//
	// v0.8.0: with the embedded audit OFF in Settings
	// (cfg.EmbeddedAudit false), assembly A is also the FINAL
	// assembly: AuditPending stays false, AuditSkipped is set, and
	// the scan ends here — phase 6 never runs, no audit jobs are
	// created, ffmpeg extraction is never invoked, and cached
	// audit findings are neither read nor republished (the read
	// filter in filteredSubtitlesLocked backs this up for results
	// an earlier audit-on scan left behind).
	s.assembleAndPersist(finalItems, health, externalFindings, subs, len(roots), totalBytes, &cfg, cfg.EmbeddedAudit, !cfg.EmbeddedAudit)
	if !cfg.EmbeddedAudit {
		// v1.0.0: a completed scan leaves its history record
		// (the audit-off path ends here, so it records here).
		s.recordScanHistory(scanStart)
		finish("")
		return
	}

	// ---- Phase 6: embedded subtitle audit (bounded workers) ----
	// Only files whose probe carries at least one subtitle stream
	// are jobs. Findings are collected per file (indexed like
	// items/probes/media) so assembly B merges them in a stable
	// order regardless of worker completion order.
	embeddedByItem := make([][]SubtitleFinding, len(media))
	auditJobs := []int{}
	for i, pr := range probes {
		if pr != nil && hasSubtitleStreams(pr) {
			auditJobs = append(auditJobs, i)
		}
	}
	if len(auditJobs) > 0 {
		s.setProgress("subtitle-audit", len(auditJobs), 0, "")
		sem := make(chan struct{}, cfg.Workers)
		var wg sync.WaitGroup
		doneCount := 0
		var doneMu sync.Mutex
		for _, ji := range auditJobs {
			wg.Add(1)
			go func(ji int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				embeddedByItem[ji] = s.auditOneEmbedded(ctx, &cfg, media[ji], items[ji], probes[ji])
				doneMu.Lock()
				doneCount++
				d := doneCount
				doneMu.Unlock()
				if d%100 == 0 {
					// Incremental audit-cache persistence, same
					// discipline as the probe phase: audits banked
					// so far survive an interrupted scan. v0.4.1:
					// throttled like the probe phase's saves.
					s.mu.Lock()
					s.saveStoreThrottledLocked()
					s.mu.Unlock()
				}
				s.setProgress("subtitle-audit", -1, d, media[ji].Path)
			}(ji)
		}
		wg.Wait()
	}
	if ctx.Err() != nil {
		// Cancelled mid-audit: assembly A's results stay published
		// (flagged audit-pending in state), and finish() persists
		// the store, so every audit completed so far is banked in
		// the cache for the next scan.
		finish("scan cancelled")
		return
	}

	// ---- Phase 7 / Assembly B: merge embedded findings, republish ----
	mergedFindings := append([]SubtitleFinding{}, externalFindings...)
	for _, fs := range embeddedByItem {
		mergedFindings = append(mergedFindings, fs...)
	}
	s.assembleAndPersist(finalItems, health, mergedFindings, subs, len(roots), totalBytes, &cfg, false, false)
	// v1.0.0: history record for the completed scan (trigger,
	// duration, summary, and the path snapshot deltas are
	// computed from — history.go).
	s.recordScanHistory(scanStart)
	finish("")
}

// assembleAndPersist replaces the published scan results with a
// freshly assembled snapshot and writes results + probe store to
// disk, under the service lock. runScan calls it twice (v0.3.4):
// assembly A publishes the library with external-subtitle findings
// only and AuditPending set; assembly B republishes with the
// embedded-audit findings merged in and the flag cleared. The
// summary is derived by currentSummaryLocked from whatever
// findings the snapshot carries, so assembly A's subtitle count
// honestly reflects external-only until B lands.
//
// v0.8.0: when the embedded audit is off, runScan calls it ONCE
// (assembly A) with auditPending=false and auditSkipped=true —
// the same external-only snapshot, but flagged as the scan's
// final state rather than an interim one.
func (s *Service) assembleAndPersist(finalItems []*Item, health []HealthFinding, findings []SubtitleFinding, subFiles []fileRef, rootCount int, totalBytes int64, cfg *Config, auditPending, auditSkipped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// v0.9.0: the external subtitle file paths ride the results
	// so the coverage derivation can associate sidecars with
	// items at read time (and after a restart).
	subtitlePaths := make([]string, 0, len(subFiles))
	for _, ref := range subFiles {
		subtitlePaths = append(subtitlePaths, ref.Path)
	}
	s.results = &ScanResults{
		Items:             finalItems,
		Subtitles:         findings,
		Health:            health,
		SubtitleFilePaths: subtitlePaths,
		ScannedAt:         time.Now().UTC().Format(time.RFC3339),
		FFprobeFound:      cfg.FFprobePath,
		FFmpegFound:       cfg.FFmpegPath,
	}
	s.results.Summary = ScanSummary{
		MediaCount:        len(finalItems),
		SubtitleFileCount: len(subFiles),
		TotalBytes:        totalBytes,
		ScannedRootCount:  rootCount,
	}
	s.results.Summary = s.currentSummaryLocked()
	s.state.Summary = s.results.Summary
	s.state.ScannedAt = s.results.ScannedAt
	s.state.AuditPending = auditPending
	s.state.AuditSkipped = auditSkipped
	s.saveResultsLocked()
	s.saveStoreLocked()
}

// hasSubtitleStreams reports whether a probe result carries any
// subtitle stream at all — the embedded-audit phase's job filter.
func hasSubtitleStreams(pr *ProbeResult) bool {
	for _, st := range pr.Streams {
		if st.Type == "subtitle" {
			return true
		}
	}
	return false
}

// auditOneEmbedded returns the embedded-subtitle findings for one
// file, reusing the cached audit when the cache entry matches the
// file's fingerprint (path + size + mtime) and a previous scan
// already completed the audit for exactly that file state. The
// extraction — the scan's expensive step — therefore runs at most
// once per file fingerprint, never once per scan. A fresh audit's
// findings are banked into the cache entry (its Probe preserved)
// under the same fingerprint; persistence rides the phase's
// incremental store saves and finish(), like probe results.
func (s *Service) auditOneEmbedded(ctx context.Context, cfg *Config, ref fileRef, item *Item, pr *ProbeResult) []SubtitleFinding {
	s.mu.Lock()
	entry, hit := s.store.Cache[ref.Path]
	s.mu.Unlock()
	if hit && entry.Size == ref.Size && entry.MtimeNano == ref.Mtime && entry.AuditDone {
		return append([]SubtitleFinding{}, entry.EmbeddedFindings...)
	}
	findings := auditEmbeddedForItem(ctx, cfg, item, pr)
	s.mu.Lock()
	if cur, ok := s.store.Cache[ref.Path]; ok && cur.Size == ref.Size && cur.MtimeNano == ref.Mtime {
		cur.EmbeddedFindings = append([]SubtitleFinding{}, findings...)
		cur.AuditDone = true
		s.store.Cache[ref.Path] = cur
	}
	s.mu.Unlock()
	return findings
}

// auditEmbeddedForItem audits one file's embedded subtitle streams
// from its probe result (no re-probing): image and unsupported
// codecs are cannot-verify; text streams are extracted with
// ffmpeg and language-checked from the extracted text.
//
// This is the audit half of the pre-v0.3.4 probeOne, unchanged in
// its rules — including the v0.3.1 per-file sampling budget: at
// most cfg.SubtitleMaxTracks TEXT streams are extracted per file
// (v0.5.0; the budget used to be the maxTextSubtitleSamplesPerFile
// const, which is now its default). Image and unsupported-codec
// streams keep their findings and do NOT consume the budget
// (reporting them costs no extraction); text streams past the cap
// are reported cannot-verify with the reason, never silently
// skipped.
func auditEmbeddedForItem(ctx context.Context, cfg *Config, item *Item, pr *ProbeResult) []SubtitleFinding {
	out := []SubtitleFinding{}
	if pr == nil {
		return out
	}
	maxTracks := cfg.SubtitleMaxTracks
	if maxTracks <= 0 {
		maxTracks = maxTextSubtitleSamplesPerFile
	}
	textSampled := 0
	for _, st := range pr.Streams {
		if st.Type != "subtitle" {
			continue
		}
		declared := langCodeNormal(st.Language)
		if st.Language != "" && declared == "" {
			declared = lowerASCII(st.Language)
		}
		if imageSubtitleCodecs[st.Codec] {
			out = append(out, SubtitleFinding{
				Path: item.Path, Source: "embedded", StreamIdx: st.Index,
				Declared: declared, Status: "cannot-verify",
				Note: "image-based subtitle codec " + st.Codec + " has no text to detect",
			})
			continue
		}
		if !textSubtitleCodecs[st.Codec] {
			out = append(out, SubtitleFinding{
				Path: item.Path, Source: "embedded", StreamIdx: st.Index,
				Declared: declared, Status: "cannot-verify",
				Note: "subtitle codec " + st.Codec + " is not a supported text codec",
			})
			continue
		}
		textSampled++
		if textSampled > maxTracks {
			out = append(out, SubtitleFinding{
				Path: item.Path, Source: "embedded", StreamIdx: st.Index,
				Declared: declared, Status: "cannot-verify",
				Note: fmt.Sprintf("not sampled — file has more than %d text subtitle tracks (scan speed cap)", maxTracks),
			})
			continue
		}
		lines, err := extractEmbeddedSubtitleFn(ctx, cfg.FFmpegPath, item.Path, st.Index, cfg.SubtitleSampleSec, cfg.SubtitleExtractTimeoutSec)
		if err != nil {
			out = append(out, SubtitleFinding{
				Path: item.Path, Source: "embedded", StreamIdx: st.Index,
				Declared: declared, Status: "cannot-verify", Note: err.Error(),
			})
			continue
		}
		if f := auditText(item.Path, "embedded", st.Index, declared, lines, cfg.SubtitleConfidence); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// probeOne probes (or reuses the cache for) one media file and
// builds its Item. v0.3.4: it no longer audits embedded subtitles —
// that work moved to auditEmbeddedForItem, a separate late scan
// phase whose results merge in after the library is published
// (see runScan). The ProbeResult travels back with the Item so
// the audit phase works from it (and from the cache) without ever
// re-probing. A fresh probe also replaces the cache entry, which
// clears any audit banked under the file's previous fingerprint —
// the audit cache is keyed to the same fingerprint as the probe.
func (s *Service) probeOne(ctx context.Context, ref fileRef, cfg *Config) (*Item, *ProbeResult) {
	pn := parseName(ref.Path, ref.Root)
	it := &Item{
		Path: ref.Path, Root: ref.Root, Name: filepath.Base(ref.Path),
		Ext: strings.ToLower(filepath.Ext(ref.Path)), Size: ref.Size, ModTimeUnix: ref.Mtime / 1e9,
		Kind: pn.Kind, Title: pn.Title, NormTitle: pn.NormTitle, Year: pn.Year,
		Season: pn.Season, Episodes: pn.Episodes, Edition: pn.Edition, ReleaseTags: pn.Tags,
		Special: pn.Special, IsSample: samplePattern(ref.Path),
		// v1.0.0: the walk's fresh identity is authoritative —
		// the Item always carries THIS scan's stat, never a
		// cached one (a replaced file under the same path gets
		// its new inode on the scan that notices the change).
		Dev: ref.Dev, Ino: ref.Ino, Nlink: ref.Nlink,
	}
	// Cache check: path + size + mtime must all match.
	s.mu.Lock()
	cached, hit := s.store.Cache[ref.Path]
	s.mu.Unlock()
	var pr *ProbeResult
	if hit && cached.Size == ref.Size && cached.MtimeNano == ref.Mtime {
		pr = cached.Probe
	} else {
		pr = probeFile(ctx, cfg.FFprobePath, ref.Path, cfg.ProbeTimeoutSec)
		s.mu.Lock()
		s.store.Cache[ref.Path] = CacheEntry{Size: ref.Size, MtimeNano: ref.Mtime, Probe: pr, Dev: ref.Dev, Ino: ref.Ino, Nlink: ref.Nlink}
		s.mu.Unlock()
	}
	it.ProbeOK = pr.OK
	it.ProbeError = pr.Error
	it.Duration = pr.Duration
	it.Bitrate = pr.Bitrate
	it.Codec = pr.VideoCodec
	it.Width, it.Height = pr.Width, pr.Height
	it.HDR = pr.HDR
	it.AudioDesc = pr.AudioDesc
	it.HasVideo = pr.HasVideo
	it.ResolutionLabel = resolutionLabel(pr.Width, pr.Height)
	if it.ResolutionLabel == "" {
		it.ResolutionLabel = pn.Resolution
	}
	return it, pr
}

// sampledHash hashes head + middle + tail 256KB chunks of a file with
// sha256 (stdlib has no xxhash; sha256 over samples is the documented
// substitute). Used only to flag exact duplicates inside a group.
func sampledHash(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	const chunk = 256 * 1024
	buf := make([]byte, chunk)
	readAt := func(off int64) {
		n, _ := f.ReadAt(buf, off)
		h.Write(buf[:n])
	}
	if size <= chunk*3 {
		_, err := io.Copy(h, f)
		return fmt.Sprintf("%x", h.Sum(nil)), err
	}
	readAt(0)
	readAt(size/2 - chunk/2)
	readAt(size - chunk)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
