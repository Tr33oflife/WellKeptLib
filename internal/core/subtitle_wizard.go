package core

// subtitle_wizard.go — the wizard's read model and its one
// non-relabel file action (v0.9.0, SUBTITLE-WIZARD-SPEC §2.2
// Step 0 + §2.4 Step D delete).
//
// SubtitleOverview is the Step 0 summary the wizard panel and
// the Subtitles tab's overview strip both render: per-class
// counts over the CURRENT published findings (the same filtered
// list the table shows — audit-off withholding, exclusions and
// dismissals all applied by filteredSubtitlesLocked, so the
// counts can never disagree with the table), the coverage
// counts from the one coverage derivation, the gap counts, and
// the scan's audit state so provisional numbers are labelled
// provisional.
//
// DeleteSubtitleFinding is the wizard's delete: the Health
// delete's guard chain and audit log, retargeted at subtitle
// findings — with the one refusal that makes it subtitle-
// specific: an EMBEDDED finding's path is a media file, and
// deleting it would delete the video, so embedded paths are
// refused explicitly, never deleted.

import (
	"fmt"
	"strings"
	"time"
)

// SubtitleOverview is GET /api/subtitles/overview.
type SubtitleOverview struct {
	ExternalMismatch int `json:"external_mismatch"`
	EmbeddedMismatch int `json:"embedded_mismatch"`
	MissingLanguage  int `json:"missing_language"`
	Uncertain        int `json:"uncertain"`
	// CannotVerify is the full cannot-verify class; Image and
	// Cap are the two named sub-reasons the wizard's Step E
	// calls out (image-based codecs, and text tracks past the
	// scan speed cap). Other cannot-verify reasons (unsupported
	// codec, extraction error) count in CannotVerify only.
	CannotVerify      int `json:"cannot_verify"`
	CannotVerifyImage int `json:"cannot_verify_image"`
	CannotVerifyCap   int `json:"cannot_verify_cap"`
	Total             int `json:"total"`
	// Dismissed is how many findings the user's verified marks
	// are currently suppressing (Settings manages the marks).
	Dismissed int `json:"dismissed"`
	// Audit state of the scan that produced these numbers:
	// EmbeddedAudit is the Settings switch, AuditPending means
	// the embedded audit is still running behind the published
	// results (counts are provisional), AuditSkipped means the
	// producing scan ran with the audit off.
	EmbeddedAudit bool `json:"embedded_audit"`
	AuditPending  bool `json:"audit_pending"`
	AuditSkipped  bool `json:"audit_skipped"`
	// Coverage counts (coverage.go derivation; dismissed items
	// excluded, reported in CoverageDismissed).
	CoverageCovered      int `json:"coverage_covered"`
	CoverageNoWanted     int `json:"coverage_no_wanted"`
	CoverageNoSubtitles  int `json:"coverage_no_subtitles"`
	CoverageUnverifiable int `json:"coverage_unverifiable"`
	CoverageDismissed    int `json:"coverage_dismissed"`
	// Gap counts (gaps.go derivation).
	GapCount    int `json:"gap_count"`
	GapEpisodes int `json:"gap_episodes"`
}

// SubtitleOverview builds the Step 0 read model from current
// state. Every count derives from the same filtered findings,
// coverage and gap derivations the other surfaces use.
func (s *Service) SubtitleOverview() SubtitleOverview {
	s.mu.Lock()
	defer s.mu.Unlock()
	ov := SubtitleOverview{
		EmbeddedAudit: s.cfg.EmbeddedAudit,
		AuditPending:  s.state.AuditPending,
		AuditSkipped:  s.state.AuditSkipped,
	}
	for _, f := range s.filteredSubtitlesLocked() {
		ov.Total++
		switch f.Status {
		case "mismatch":
			if f.Source == "embedded" {
				ov.EmbeddedMismatch++
			} else {
				ov.ExternalMismatch++
			}
		case "missing-language":
			ov.MissingLanguage++
		case "uncertain":
			ov.Uncertain++
		case "cannot-verify":
			ov.CannotVerify++
			switch {
			case strings.Contains(f.Note, "image-based"):
				ov.CannotVerifyImage++
			case strings.Contains(f.Note, "scan speed cap"):
				ov.CannotVerifyCap++
			}
		}
	}
	ov.Dismissed = s.dismissedFindingsCountLocked()
	_, covCounts, covDismissed := s.coverageAllLocked()
	ov.CoverageCovered = covCounts[coverageCovered]
	ov.CoverageNoWanted = covCounts[coverageNoWanted]
	ov.CoverageNoSubtitles = covCounts[coverageNone]
	ov.CoverageUnverifiable = covCounts[coverageUnverifiable]
	ov.CoverageDismissed = covDismissed
	gaps := buildEpisodeGaps(s.results.Items)
	ov.GapCount = len(gaps)
	for _, g := range gaps {
		ov.GapEpisodes += g.MissingCount
	}
	return ov
}

// DeleteSubtitleFinding permanently deletes ONE external
// subtitle file behind the shared guard chain, in order:
//  1. the path is a CURRENT published subtitle finding (the
//     filtered list — audit-off, exclusions, dismissals
//     applied), so a dismissed or excluded finding cannot be
//     deleted until it is visible again;
//  2. the finding is EXTERNAL. An embedded finding names a
//     media file: deleting it would delete the video the track
//     lives in. This is refused explicitly, before any other
//     check could act on the path;
//  3. checkDeletableFileLocked — the Health delete's guards:
//     no scan running, under a configured scan root, a regular
//     file.
//
// After the delete it appends to the same audit log the other
// deletes use, and removes the file from the published results
// without a rescan (findings naming it, plus its sidecar path
// in the coverage evidence).
func (s *Service) DeleteSubtitleFinding(path string) (DeleteHealthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	finding, ok := s.currentSubtitleFindingLocked(path)
	if !ok {
		// Name the embedded refusal even when the finding is not
		// in the current list for another reason (e.g. the audit
		// is off): the user pointed at a media file, and "not a
		// finding" would send them hunting for the wrong cause.
		for _, raw := range s.results.Subtitles {
			if raw.Path == path && raw.Source == "embedded" {
				return DeleteHealthResult{}, fmt.Errorf("refusing to delete %s: this finding is an embedded track — its path is a media file, and deleting it would delete the video. Embedded tracks are never deleted", path)
			}
		}
		return DeleteHealthResult{}, fmt.Errorf("not a current subtitle finding: %s", path)
	}
	if finding.Source == "embedded" {
		return DeleteHealthResult{}, fmt.Errorf("refusing to delete %s: this finding is an embedded track — its path is a media file, and deleting it would delete the video. Embedded tracks are never deleted", path)
	}
	info, err := s.checkDeletableFileLocked(path)
	if err != nil {
		return DeleteHealthResult{}, err
	}
	if err := removeFile(path); err != nil {
		return DeleteHealthResult{}, fmt.Errorf("delete failed for %s: %v", path, err)
	}
	_ = s.appendDeletedActionLocked(DeletedAction{
		Time:        time.Now().UTC().Format(time.RFC3339),
		Path:        path,
		Size:        info.Size(),
		FindingKind: "subtitle:" + finding.Status,
		Detail:      fmt.Sprintf("declared %q, detected %q", finding.Declared, finding.Detected),
	})
	// The sidecar path is coverage evidence too: drop it before
	// removePathFromResultsLocked persists, so the persisted
	// results never reference a file that is gone.
	paths := make([]string, 0, len(s.results.SubtitleFilePaths))
	for _, p := range s.results.SubtitleFilePaths {
		if p != path {
			paths = append(paths, p)
		}
	}
	s.results.SubtitleFilePaths = paths
	s.removePathFromResultsLocked(path)
	return DeleteHealthResult{OK: true, Path: path, Summary: s.currentSummaryLocked()}, nil
}
