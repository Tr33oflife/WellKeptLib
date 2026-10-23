package core

// history.go — scan history + deltas (v1.0.0, FEATURE-PLAN §5).
//
// Every COMPLETED scan appends one compact record to
// scan-history.json in the data dir: when it ran, how long it
// took, who triggered it (manual / scheduled / cli), the roots
// it covered, the headline summary, and a path → {size, mtime}
// snapshot of the media it saw. The History view lists the
// records newest-first, each with its delta against the
// PREVIOUS record — added / removed / changed paths plus the
// summary-count movements.
//
// Honesty rules (from the plan):
//   - Deltas describe disk state, never intent: "removed" means
//     "not seen by this scan", nothing more.
//   - If a whole root from the previous record is absent from
//     this one (unmounted drive, edited roots), that root's
//     paths are EXCLUDED from the removed count and the delta
//     carries a warning naming it — an unmounted drive must
//     never read as a mass deletion.
//   - Cancelled and failed scans leave no record: history is
//     the ledger of scans that actually published results.
//
// Retention: the last historyRetention records are kept. (The
// feature plan proposed 10 as a default with the count still an
// open owner question; this build keeps 50 — a record is a few
// hundred KB of snapshot on a large library, and the longer
// ledger makes deltas useful across a month of weekly scans.
// The number is a named constant, not buried arithmetic.)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// historyRetention is how many scan records are kept.
const historyRetention = 50

// ScanSnapshotEntry is one file's delta evidence: size + mtime
// (unix seconds) at scan time.
type ScanSnapshotEntry struct {
	Size  int64 `json:"size"`
	Mtime int64 `json:"mtime"`
}

// ScanHistoryRecord is one completed scan's persisted record.
type ScanHistoryRecord struct {
	ScannedAt   string                       `json:"scanned_at"`
	DurationSec float64                      `json:"duration_sec"`
	Trigger     string                       `json:"trigger"` // manual | scheduled | cli
	Roots       []string                     `json:"roots"`
	Summary     ScanSummary                  `json:"summary"`
	Files       map[string]ScanSnapshotEntry `json:"files"`
}

// ScanDelta is one record's change against the previous record.
type ScanDelta struct {
	Available        bool     `json:"available"` // false for the oldest record
	ComparedTo       string   `json:"compared_to,omitempty"`
	AddedFiles       int      `json:"added_files"`
	RemovedFiles     int      `json:"removed_files"`
	ChangedFiles     int      `json:"changed_files"`
	MediaDelta       int      `json:"media_delta"`
	GroupsDelta      int      `json:"groups_delta"`
	ReclaimableDelta int64    `json:"reclaimable_delta"`
	SubtitleDelta    int      `json:"subtitle_delta"`
	HealthDelta      int      `json:"health_delta"`
	RootWarnings     []string `json:"root_warnings,omitempty"`
}

// HistoryEntry is one record as the History view serves it: the
// record WITHOUT its snapshot map (an 11k-file map per row would
// swamp the payload), plus its computed delta.
type HistoryEntry struct {
	ScannedAt   string      `json:"scanned_at"`
	DurationSec float64     `json:"duration_sec"`
	Trigger     string      `json:"trigger"`
	Roots       []string    `json:"roots"`
	Summary     ScanSummary `json:"summary"`
	FileCount   int         `json:"file_count"`
	Delta       ScanDelta   `json:"delta"`
}

// scanHistoryPath is the history store's location.
func (s *Service) scanHistoryPath() string {
	return filepath.Join(s.dataDir, "scan-history.json")
}

// loadScanHistory reads the history store (best-effort: a
// missing or unreadable file means no history, never a startup
// failure — the same discipline as the dismissal store).
func loadScanHistory(dataDir string) []ScanHistoryRecord {
	b, err := os.ReadFile(filepath.Join(dataDir, "scan-history.json"))
	if err != nil {
		return nil
	}
	var recs []ScanHistoryRecord
	if err := json.Unmarshal(b, &recs); err != nil {
		return nil
	}
	return recs
}

// saveScanHistoryLocked persists the records (tmp + rename, the
// house discipline). Caller holds s.mu.
func (s *Service) saveScanHistoryLocked() {
	b, err := json.Marshal(s.history)
	if err != nil {
		return
	}
	tmp := s.scanHistoryPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, s.scanHistoryPath())
	}
}

// recordScanHistory appends the just-completed scan's record.
// Called by runScan on its two completion exits, AFTER the final
// assembly has published results — s.results therefore describes
// exactly the scan being recorded. The trigger is the one the
// start path stashed in s.scanTrigger (consumed here).
func (s *Service) recordScanHistory(started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	trigger := s.scanTrigger
	if trigger == "" {
		trigger = "manual"
	}
	s.scanTrigger = ""
	files := make(map[string]ScanSnapshotEntry, len(s.results.Items))
	for _, it := range s.results.Items {
		if it == nil {
			continue
		}
		files[it.Path] = ScanSnapshotEntry{Size: it.Size, Mtime: it.ModTimeUnix}
	}
	rec := ScanHistoryRecord{
		ScannedAt:   s.results.ScannedAt,
		DurationSec: time.Since(started).Seconds(),
		Trigger:     trigger,
		Roots:       append([]string{}, s.cfg.Roots...),
		Summary:     s.currentSummaryLocked(),
		Files:       files,
	}
	if rec.ScannedAt == "" {
		rec.ScannedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.history = append(s.history, rec)
	if len(s.history) > historyRetention {
		s.history = s.history[len(s.history)-historyRetention:]
	}
	s.saveScanHistoryLocked()
}

// computeScanDelta derives cur's delta against prev. Pure — the
// two records in, the delta out — so the arithmetic (including
// the missing-root suppression) is unit-testable without a
// service.
func computeScanDelta(prev, cur ScanHistoryRecord) ScanDelta {
	d := ScanDelta{Available: true, ComparedTo: prev.ScannedAt}
	// Missing-root suppression: a root the previous scan covered
	// that this scan did not is a warning, and its files are not
	// "removed".
	curRoots := map[string]bool{}
	for _, r := range cur.Roots {
		curRoots[cleanAbs(r)] = true
	}
	suppressed := map[string]bool{} // roots whose paths don't count as removed
	for _, r := range prev.Roots {
		if !curRoots[cleanAbs(r)] {
			suppressed[cleanAbs(r)] = true
			d.RootWarnings = append(d.RootWarnings, "root not scanned this time: "+r+" — its files are not counted as removed")
		}
	}
	pathSuppressed := func(p string) bool {
		for root := range suppressed {
			if pathWithinRoot(root, p) {
				return true
			}
		}
		return false
	}
	for p, curE := range cur.Files {
		prevE, ok := prev.Files[p]
		if !ok {
			d.AddedFiles++
			continue
		}
		if prevE.Size != curE.Size || prevE.Mtime != curE.Mtime {
			d.ChangedFiles++
		}
	}
	for p := range prev.Files {
		if _, ok := cur.Files[p]; !ok {
			if pathSuppressed(p) {
				continue
			}
			d.RemovedFiles++
		}
	}
	d.MediaDelta = cur.Summary.MediaCount - prev.Summary.MediaCount
	d.GroupsDelta = cur.Summary.DuplicateGroups - prev.Summary.DuplicateGroups
	d.ReclaimableDelta = cur.Summary.DuplicateReclaimable - prev.Summary.DuplicateReclaimable
	d.SubtitleDelta = cur.Summary.SubtitleFindings - prev.Summary.SubtitleFindings
	d.HealthDelta = cur.Summary.HealthFindings - prev.Summary.HealthFindings
	return d
}

// History returns the records newest-first with deltas computed
// against each record's predecessor (GET /api/history).
func (s *Service) History() []HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []HistoryEntry{}
	for i := len(s.history) - 1; i >= 0; i-- {
		rec := s.history[i]
		e := HistoryEntry{
			ScannedAt:   rec.ScannedAt,
			DurationSec: rec.DurationSec,
			Trigger:     rec.Trigger,
			Roots:       rec.Roots,
			Summary:     rec.Summary,
			FileCount:   len(rec.Files),
		}
		if i > 0 {
			e.Delta = computeScanDelta(s.history[i-1], rec)
		} else {
			e.Delta = ScanDelta{Available: false}
		}
		out = append(out, e)
	}
	return out
}
