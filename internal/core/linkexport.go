package core

// linkexport.go — the link-plan diagnostics export (v1.3.0):
// ONE JSON document describing EVERY problem in a link plan —
// both conflict kinds, every not-identified file, every
// unlinkable file — each with the planner's own evidence
// attached (what the parser extracted, the probe facts the
// ranking consumed, the occupant facts at a blocked target),
// so naming failures can be read and fixed in bulk away from
// the app.
//
// It is a read-only projection of the plan the Service already
// holds in memory: nothing is recomputed, rescanned, probed or
// persisted, and no plan is consumed — the same preview can be
// exported repeatedly. The healthy majority (links-to-create,
// already-linked) and the skipped noise are exported as COUNTS
// ONLY: this file exists to carry the failure signal, and the
// excluded sections are named in its own notes field.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The export's schema identity: a reader (human or script) can
// pin these two values and trust the shape below.
const (
	LinkDiagnosticsSchema        = "wellkeptlib.linkplan.diagnostics"
	LinkDiagnosticsSchemaVersion = 1
)

// LinkDiagnostics is the exported document. The field order is
// the emitted key order (encoding/json on structs).
type LinkDiagnostics struct {
	Schema          string                  `json:"schema"`
	SchemaVersion   int                     `json:"schema_version"`
	AppVersion      string                  `json:"app_version"`
	ExportedAt      string                  `json:"exported_at"` // UTC, RFC 3339
	PlanID          string                  `json:"plan_id"`
	SourceRoot      string                  `json:"source_root"`
	DestinationRoot string                  `json:"destination_root"`
	Counts          LinkPlanStats           `json:"counts"`
	Conflicts       []LinkProblemConflict   `json:"conflicts"`
	NotFound        []LinkProblemNotFound   `json:"not_found"`
	Unlinkable      []LinkProblemUnlinkable `json:"unlinkable"`
	// NumberAssigned / YearlessMovies (v1.4.0, additive):
	// the plan entries whose Season 00 number was CREATED
	// by the planner, and the movies accepted with no year.
	// These are healthy links (otherwise counts-only in this
	// document), listed here because the honesty rule for
	// invented facts applies to the export too: an invented
	// number or a year-less acceptance is never silent.
	NumberAssigned []LinkFlaggedEntry `json:"number_assigned,omitempty"`
	YearlessMovies []LinkFlaggedEntry `json:"yearless_movies,omitempty"`
	Notes          []string           `json:"notes"`
}

// LinkFlaggedEntry is one healthy plan entry carrying a
// v1.4.0 honesty flag (a created number, a year-less
// acceptance): the source and the target it links to.
type LinkFlaggedEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// LinkProblemConflict is one entry of the conflicts list. The
// Kind field discriminates the two shapes; fields belonging to
// the other shape are simply absent (or, for Keeper, null).
type LinkProblemConflict struct {
	Kind   string `json:"kind"` // collision | target_conflict
	Target string `json:"target"`

	// Collision shape (kind = "collision"): every source that
	// wanted the target, in plan order — keeper first, then
	// alternates, then ineligible copies.
	Status         string              `json:"status,omitempty"` // resolved | unresolved
	Keeper         *string             `json:"keeper"`           // the linked source; null when unresolved
	KeeperReason   string              `json:"keeper_reason,omitempty"`
	Reason         string              `json:"reason,omitempty"`
	DurationStatus string              `json:"duration_status,omitempty"`
	DurationNote   string              `json:"duration_note,omitempty"`
	RankingNote    string              `json:"ranking_note,omitempty"`
	Sources        []LinkProblemSource `json:"sources,omitempty"`

	// Target-conflict shape (kind = "target_conflict"): the
	// target is occupied by something that is NOT one of our
	// links and is never overwritten or removed.
	BlockedSources []string          `json:"blocked_sources,omitempty"`
	Occupant       *LinkOccupantFact `json:"occupant,omitempty"`
	AttachedFiles  []string          `json:"attached_files,omitempty"`
}

// LinkProblemSource is one source inside a collision entry,
// with its role and the evidence behind the decision.
type LinkProblemSource struct {
	Source        string          `json:"source"`
	Role          string          `json:"role"` // keeper | alternate | ineligible
	Reason        string          `json:"reason,omitempty"`
	SamePhysical  bool            `json:"same_physical,omitempty"`
	Parsed        *LinkParsedFact `json:"parsed,omitempty"`
	Probe         *LinkProbeFact  `json:"probe,omitempty"`
	AttachedFiles []string        `json:"attached_files,omitempty"`
}

// LinkOccupantFact is what the planner recorded about the file
// (or folder, or link) occupying a conflicted target: it is a
// real occupant, not one of our links. Size and identity are
// present only where the planner read them (regular files).
type LinkOccupantFact struct {
	Kind          string  `json:"kind"` // regular file | directory | symbolic link | other | unknown identity
	Size          *int64  `json:"size,omitempty"`
	Dev           *uint64 `json:"dev,omitempty"`
	Ino           *uint64 `json:"ino,omitempty"`
	IdentityKnown bool    `json:"identity_known"`
}

// LinkProblemNotFound is one file the parser could not
// identify, with the reason and everything the parser DID
// extract from its name.
type LinkProblemNotFound struct {
	Path          string          `json:"path"`
	Reason        string          `json:"reason"`
	Parsed        *LinkParsedFact `json:"parsed,omitempty"`
	Ext           string          `json:"ext,omitempty"`
	Size          int64           `json:"size"`
	AttachedFiles []string        `json:"attached_files,omitempty"`
}

// LinkProblemUnlinkable is one identified file a hardlink
// cannot be proven for, with the target it would have taken
// and the device facts the feasibility proof used.
type LinkProblemUnlinkable struct {
	Path             string          `json:"path"`
	Target           string          `json:"target,omitempty"`
	Reason           string          `json:"reason"`
	Parsed           *LinkParsedFact `json:"parsed,omitempty"`
	Ext              string          `json:"ext,omitempty"`
	Size             int64           `json:"size"`
	SourceDev        *uint64         `json:"source_dev,omitempty"`
	SourceIno        *uint64         `json:"source_ino,omitempty"`
	IdentityKnown    bool            `json:"identity_known"`
	DestinationDev   *uint64         `json:"destination_dev,omitempty"`
	DestinationKnown bool            `json:"destination_known"`
	AttachedFiles    []string        `json:"attached_files,omitempty"`
}

func u64ptr(v uint64) *uint64 { return &v }
func i64ptr(v int64) *int64   { return &v }

// BuildLinkPlanDiagnostics projects a link plan into the
// export document. Pure: it reads only what the plan retained
// at preview time (the v1.3.0 diagnostic facts on the outcome
// records carry the evidence).
func BuildLinkPlanDiagnostics(plan *LinkPlan, appVersion string, now time.Time) *LinkDiagnostics {
	doc := &LinkDiagnostics{
		Schema:          LinkDiagnosticsSchema,
		SchemaVersion:   LinkDiagnosticsSchemaVersion,
		AppVersion:      appVersion,
		ExportedAt:      now.UTC().Format(time.RFC3339),
		PlanID:          plan.ID,
		SourceRoot:      plan.SourceRoot,
		DestinationRoot: plan.TargetRoot,
		Counts:          plan.Stats,
		Conflicts:       []LinkProblemConflict{},
		NotFound:        []LinkProblemNotFound{},
		Unlinkable:      []LinkProblemUnlinkable{},
		Notes: []string{
			"links_to_create and already_linked entries are the healthy majority and are exported as counts only (see counts) — no healthy file appears in this document.",
			"skipped entries are exported as counts only: orphan sidecars and similar noise would drown the naming-failure signal this file exists to carry.",
			"conflicts lists target collisions first (in plan order), then target conflicts (in plan order).",
			"probe facts are the values the duplicate ranking consumed; where no probe ran for a file (for example an occupied target's copies), the fact set is filename-level — resolution label and size only, with height, bitrate and duration absent (unknown).",
			"in a parsed block, a year of 0, an empty title or an empty episodes list means the parser extracted nothing there — the absence is the signal.",
			"an occupant at a target conflict is a real file, folder or link that is NOT one of this app's links; link mode never overwrites or removes it.",
		},
	}
	for _, c := range plan.Collisions {
		pc := LinkProblemConflict{
			Kind:           "collision",
			Target:         c.Target,
			Reason:         c.Reason,
			DurationStatus: c.DurationStatus,
			DurationNote:   c.DurationNote,
			RankingNote:    c.RankingNote,
		}
		if c.KeeperSource != "" {
			pc.Status = "resolved"
			keeper := c.KeeperSource
			pc.Keeper = &keeper
			pc.KeeperReason = c.RankingNote
			if pc.KeeperReason == "" {
				pc.KeeperReason = c.Reason
			}
			pc.Sources = append(pc.Sources, LinkProblemSource{
				Source: c.KeeperSource, Role: "keeper",
				Parsed: c.KeeperParsed, Probe: c.KeeperProbe,
			})
		} else {
			pc.Status = "unresolved"
		}
		for _, a := range c.Alternates {
			pc.Sources = append(pc.Sources, LinkProblemSource{
				Source: a.Source, Role: "alternate", Reason: a.Reason,
				SamePhysical: a.SamePhysical, Parsed: a.Parsed, Probe: a.Probe,
				AttachedFiles: a.AttachedFiles,
			})
		}
		for _, ig := range c.Ineligible {
			pc.Sources = append(pc.Sources, LinkProblemSource{
				Source: ig.Path, Role: "ineligible", Reason: ig.Reason,
				Parsed: ig.Parsed, Probe: ig.Probe,
				AttachedFiles: ig.AttachedFiles,
			})
		}
		doc.Conflicts = append(doc.Conflicts, pc)
	}
	for _, tc := range plan.TargetConflicts {
		occ := &LinkOccupantFact{Kind: tc.Occupant, IdentityKnown: tc.OccupantIdentityKnown}
		if tc.OccupantSizeKnown {
			occ.Size = i64ptr(tc.OccupantSize)
		}
		if tc.OccupantIdentityKnown {
			occ.Dev = u64ptr(tc.OccupantDev)
			occ.Ino = u64ptr(tc.OccupantIno)
		}
		doc.Conflicts = append(doc.Conflicts, LinkProblemConflict{
			Kind:           "target_conflict",
			Target:         tc.Target,
			Reason:         tc.Reason,
			BlockedSources: tc.Sources,
			Occupant:       occ,
			AttachedFiles:  tc.AttachedFiles,
		})
	}
	for _, nf := range plan.NotIdentified {
		doc.NotFound = append(doc.NotFound, LinkProblemNotFound{
			Path: nf.Path, Reason: nf.Reason, Parsed: nf.Parsed,
			Ext: nf.Ext, Size: nf.Size, AttachedFiles: nf.AttachedFiles,
		})
	}
	for _, u := range plan.Unlinkable {
		lu := LinkProblemUnlinkable{
			Path: u.Path, Target: u.Target, Reason: u.Reason, Parsed: u.Parsed,
			Ext: u.Ext, Size: u.Size, IdentityKnown: u.IdentityKnown,
			DestinationKnown: u.DestinationKnown, AttachedFiles: u.AttachedFiles,
		}
		if u.IdentityKnown {
			lu.SourceDev = u64ptr(u.SourceDev)
			lu.SourceIno = u64ptr(u.SourceIno)
		}
		if u.DestinationKnown {
			lu.DestinationDev = u64ptr(u.DestinationDev)
		}
		doc.Unlinkable = append(doc.Unlinkable, lu)
	}
	// v1.4.0: the flagged healthy entries (created numbers,
	// year-less movies), from both link lists — the plan's
	// lists are source-sorted, so the sections are too.
	for _, list := range [][]LinkEntry{plan.LinksToCreate, plan.AlreadyLinked} {
		for _, e := range list {
			if e.NumberAssigned {
				doc.NumberAssigned = append(doc.NumberAssigned, LinkFlaggedEntry{Source: e.Source, Target: e.Target})
			}
			if e.NoYear {
				doc.YearlessMovies = append(doc.YearlessMovies, LinkFlaggedEntry{Source: e.Source, Target: e.Target})
			}
		}
	}
	sort.Slice(doc.NumberAssigned, func(i, j int) bool { return doc.NumberAssigned[i].Source < doc.NumberAssigned[j].Source })
	sort.Slice(doc.YearlessMovies, func(i, j int) bool { return doc.YearlessMovies[i].Source < doc.YearlessMovies[j].Source })
	if len(doc.NumberAssigned) > 0 {
		doc.Notes = append(doc.Notes, "number_assigned lists the entries whose Season 00 episode number was CREATED by the app (the file named no episode number): numbers are assigned per plan, in sorted source-path order across the plan's specials of that series — if a future plan's set of specials differs, the numbers can shift, and a shifted target already occupied by a different file surfaces as a target conflict rather than being overwritten.")
	}
	if len(doc.YearlessMovies) > 0 {
		doc.Notes = append(doc.Notes, "yearless_movies lists the movies accepted with no year in the name, on their folder context (their own movie folder, or a folder naming a Movies context): they link as Title/Title.ext with no (Year) in the folder name.")
	}
	return doc
}

// LinkProblemsFilename is the download name for a plan's
// export: it carries the plan's own timestamp (plan IDs are
// "plan-<yyyymmdd>-<hhmmss>-<random>").
func LinkProblemsFilename(plan *LinkPlan) string {
	return "wellkeptlib-link-problems-" + strings.TrimPrefix(plan.ID, "plan-") + ".json"
}

// LinkPlanDiagnostics returns the diagnostics export for a
// previewed link plan plus its download filename. The plan is
// NOT consumed (unlike LinkApply): the export can be
// downloaded repeatedly while the preview lives. An unknown
// id is an error — plans live only in memory.
func (s *Service) LinkPlanDiagnostics(planID string) (*LinkDiagnostics, string, error) {
	s.mu.Lock()
	plan, ok := s.linkPlans[planID]
	s.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown link plan %q — preview the plan again", planID)
	}
	return BuildLinkPlanDiagnostics(plan, Version, time.Now().UTC()), LinkProblemsFilename(plan), nil
}
