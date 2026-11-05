package core

// plex_delete.go — Part C of CATALOG-SIZES-PLEXDELETE-SPEC
// (v1.5.0): delete via Plex.
//
// The owner deletes media through Plex's own channels, not
// by removing files behind its back: Plex removes the file
// from disk itself and its database stays in sync (no ghost
// entries, no trash step). That only works when the
// signed-in account OWNS the server and the server has
// "Allow media deletion" enabled (§C.1) — so this slice is
// built around a PREFLIGHT that must pass before the delete
// action exists at all (§C.4):
//
//   - PlexPreflight reads the server's own answers — the
//     root document's allowMediaDeletion / owned attributes,
//     the allowMediaDeletion preference at /:/prefs, and (for
//     ownership only) the plex.tv resource list when an
//     account sign-in is available. Unknown is reported as
//     unknown and FAILS the preflight: ownership is never
//     guessed. The preflight is CHECK-ONLY — the app never
//     flips allowMediaDeletion for the user (§5 ruling).
//   - PlexMatches re-runs the cross-check fetch (§C.3) and
//     matches Plex's file paths (mappings applied) against
//     the scanned library. The match snapshot is retained;
//     PlexDelete validates every requested path against it.
//     A file with no Plex match gets NO delete — and there
//     is no silent fallback to the file-level delete tools:
//     this slice either deletes through Plex or refuses,
//     saying why.
//   - A delete names ONE file. Its default scope is the
//     file's own media VERSION (DELETE .../media/{mediaId});
//     deleting the whole ITEM (every version's files) is a
//     separate, explicit scope the caller must choose per
//     entry. Batches are allowed (§5 ruling): one call, one
//     confirm in the UI listing everything — the service
//     then attempts each entry in order.
//   - EVERY attempt — success, refusal, server error — is
//     audited to plex-delete-actions.json in the data dir.
//     Server refusals surface with their status and the
//     server's own words, never swallowed (§C.1's 403/400
//     discipline).
//   - A successful delete does NOT rewrite the scan: the
//     file's row stays and is marked deleted-pending-rescan
//     in the matches view. The next scan is what removes it
//     everywhere (the file really is gone from disk — Plex
//     removed it).
//
// Token secrecy and transport separation are unchanged: the
// views below have no token field, and no method takes or
// returns HTTP types.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

// PlexPreflight is the delete readiness report. Ready is
// true only when every condition is CONFIRMED: configured,
// reachable with an accepted token, account owns the server,
// and media deletion is enabled on it. Problems names every
// unmet or unconfirmable condition in plain language.
type PlexPreflight struct {
	Configured      bool     `json:"configured"`
	Reachable       bool     `json:"reachable"`
	ServerName      string   `json:"server_name,omitempty"`
	Owned           bool     `json:"owned"`
	OwnedKnown      bool     `json:"owned_known"`
	OwnedSource     string   `json:"owned_source,omitempty"` // "server" | "plex.tv account"
	DeletionEnabled bool     `json:"deletion_enabled"`
	DeletionKnown   bool     `json:"deletion_known"`
	DeletionSource  string   `json:"deletion_source,omitempty"` // "server" | "server preferences"
	Ready           bool     `json:"ready"`
	Problems        []string `json:"problems"`
}

// accountTokenForPreflight returns the plex.tv account token
// when one is available (retained in memory, or remembered
// on disk by the user's opt-in), else "".
func (s *Service) accountTokenForPreflight() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retained != nil && s.retained.accountToken != "" {
		return s.retained.accountToken
	}
	return s.plex.AccountToken
}

// PlexPreflight runs the delete preflight against the saved
// Plex settings. Read-only in every direction: server GETs
// and one plex.tv GET at most.
func (s *Service) PlexPreflight() PlexPreflight {
	p := s.plexSnapshot()
	acct := s.accountTokenForPreflight()
	return runPlexPreflight(p, acct)
}

// runPlexPreflight is PlexPreflight over an explicit settings
// snapshot + account token (the shape tests drive).
func runPlexPreflight(p plexSettings, accountToken string) PlexPreflight {
	pf := PlexPreflight{Configured: p.configured(), Problems: []string{}}
	if !pf.Configured {
		pf.Problems = append(pf.Problems, "Plex is not configured — set the server URL and token in Settings first")
		return pf
	}
	client := p.client()
	root, err := client.rootInfo()
	if err != nil {
		pf.Problems = append(pf.Problems, err.Error())
		return pf
	}
	pf.Reachable = true
	pf.ServerName = root.FriendlyName
	// Ownership: the server's own root document first.
	if root.Owned != nil {
		pf.Owned, pf.OwnedKnown, pf.OwnedSource = plexBoolAttr(*root.Owned), true, "server"
	} else if accountToken != "" && root.MachineIdentifier != "" {
		// The account's plex.tv resource list is the other
		// place Plex states ownership — matched by machine
		// identifier, never by name.
		if servers, ferr := fetchServers(accountToken, p.ClientID); ferr == nil {
			for _, srv := range servers {
				if srv.ClientID == root.MachineIdentifier {
					pf.Owned, pf.OwnedKnown, pf.OwnedSource = srv.Owned, true, "plex.tv account"
					break
				}
			}
		}
	}
	// Deletion: the root document's attribute first, the
	// preference store as the second read.
	if root.AllowMediaDeletion != nil {
		pf.DeletionEnabled, pf.DeletionKnown, pf.DeletionSource = plexBoolAttr(*root.AllowMediaDeletion), true, "server"
	} else if enabled, known, perr := client.prefAllowMediaDeletion(); perr == nil && known {
		pf.DeletionEnabled, pf.DeletionKnown, pf.DeletionSource = enabled, true, "server preferences"
	}
	switch {
	case !pf.OwnedKnown:
		pf.Problems = append(pf.Problems, "could not confirm the signed-in account owns this server — delete stays off rather than guessing (signing in with Plex, rather than a pasted token, lets the app check)")
	case !pf.Owned:
		pf.Problems = append(pf.Problems, "the signed-in account does not own this server — Plex only lets a server's owner delete media")
	}
	switch {
	case !pf.DeletionKnown:
		pf.Problems = append(pf.Problems, "could not confirm the server's \"Allow media deletion\" setting — delete stays off rather than guessing")
	case !pf.DeletionEnabled:
		pf.Problems = append(pf.Problems, "the server has \"Allow media deletion\" turned off (in Plex: Settings > Library, advanced) — the app only checks this setting; turning it on is done in Plex itself")
	}
	pf.Ready = pf.Reachable && pf.OwnedKnown && pf.Owned && pf.DeletionKnown && pf.DeletionEnabled
	return pf
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

// plexMatch is one retained cross-check match: a scanned
// local file and the Plex identity §C.3 retained for it.
type plexMatch struct {
	Path      string
	Title     string
	Type      string
	Section   string
	RatingKey string
	MediaID   string
	Versions  int // distinct media versions the item has
}

// PlexFileMatch is the matches view's row for one matched
// scanned file.
type PlexFileMatch struct {
	Path                 string `json:"path"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	Section              string `json:"section"`
	RatingKey            string `json:"rating_key"`
	MediaID              string `json:"media_id"`
	Versions             int    `json:"versions"`
	DeletedPendingRescan bool   `json:"deleted_pending_rescan"`
}

// PlexMatchResult is the PlexMatches payload: the preflight
// (the delete action exists only when it is Ready), the
// matched files, and the counts that account for every
// scanned file — unmatched files are counted and named as a
// class, never given a delete affordance.
type PlexMatchResult struct {
	Preflight      PlexPreflight   `json:"preflight"`
	Matches        []PlexFileMatch `json:"matches"`
	ScannedFiles   int             `json:"scanned_files"`
	MatchedFiles   int             `json:"matched_files"`
	UnmatchedFiles int             `json:"unmatched_files"`
	Warnings       []string        `json:"warnings"`
}

// PlexMatches runs the cross-check fetch and matches Plex's
// items against the scanned library, retaining the snapshot
// PlexDelete validates against. The fetch semantics are the
// cross-check's own (§C.3): sections that fail are warnings,
// not fatalities.
func (s *Service) PlexMatches() (PlexMatchResult, error) {
	p := s.plexSnapshot()
	if !p.configured() {
		return PlexMatchResult{}, fmt.Errorf("Plex is not configured — set the server URL and token in Settings first")
	}
	preflight := s.PlexPreflight()
	client := p.client()
	sections, err := client.sections()
	if err != nil {
		return PlexMatchResult{}, err
	}
	titles := map[string]string{}
	for _, sec := range sections {
		titles[sec.Key] = sec.Title
	}
	paths, warnings := client.itemPaths(sections)
	if warnings == nil {
		warnings = []string{}
	}
	// Versions per item: distinct media IDs under one
	// ratingKey across every row the fetch returned.
	versions := map[string]map[string]bool{}
	for _, it := range paths {
		if it.RatingKey == "" {
			continue
		}
		set, ok := versions[it.RatingKey]
		if !ok {
			set = map[string]bool{}
			versions[it.RatingKey] = set
		}
		set[it.MediaID] = true
	}
	s.mu.Lock()
	scanned := map[string]bool{}
	for _, it := range s.results.Items {
		scanned[it.Path] = true
	}
	snapshot := map[string]*plexMatch{}
	for _, it := range paths {
		local := applyMapping(it.File, p.Mappings)
		if !scanned[local] {
			continue // Plex knows it; this library scan does not — no row, no affordance
		}
		if _, dup := snapshot[local]; dup {
			continue
		}
		snapshot[local] = &plexMatch{
			Path: local, Title: displayTitle(it), Type: it.Type,
			Section: titles[it.SectionKey], RatingKey: it.RatingKey,
			MediaID: it.MediaID, Versions: len(versions[it.RatingKey]),
		}
	}
	s.plexMatchByPath = snapshot
	deleted := s.plexDeleted
	res := PlexMatchResult{
		Preflight:    preflight,
		Matches:      []PlexFileMatch{},
		ScannedFiles: len(scanned),
		Warnings:     warnings,
	}
	keys := make([]string, 0, len(snapshot))
	for path := range snapshot {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	for _, path := range keys {
		m := snapshot[path]
		res.Matches = append(res.Matches, PlexFileMatch{
			Path: m.Path, Title: m.Title, Type: m.Type, Section: m.Section,
			RatingKey: m.RatingKey, MediaID: m.MediaID, Versions: m.Versions,
			DeletedPendingRescan: deleted[path],
		})
	}
	s.mu.Unlock()
	res.MatchedFiles = len(res.Matches)
	res.UnmatchedFiles = res.ScannedFiles - res.MatchedFiles
	return res, nil
}

// ---------------------------------------------------------------------------
// The guarded delete action
// ---------------------------------------------------------------------------

// PlexDeleteEntry is one requested deletion: a scanned local
// file path and its scope — "version" (only this file's
// media version; the default, and what an empty scope
// means) or "item" (the whole Plex item: every version's
// files go). Item scope is never implied; it must be named.
type PlexDeleteEntry struct {
	Path  string `json:"path"`
	Scope string `json:"scope,omitempty"` // "version" | "item"
}

// PlexDeleteOutcome is one entry's result: what was asked,
// whether Plex did it, and — when not — the refusal or the
// server's answer, in words.
type PlexDeleteOutcome struct {
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Title  string `json:"title,omitempty"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"` // HTTP status when the server answered
	Detail string `json:"detail,omitempty"`
}

// PlexDeleteResult is the batch payload: one outcome per
// requested entry, in request order, plus the preflight the
// batch was gated on.
type PlexDeleteResult struct {
	Preflight PlexPreflight       `json:"preflight"`
	Outcomes  []PlexDeleteOutcome `json:"outcomes"`
}

// PlexDeleteAction is one entry of the on-disk audit log
// (<dataDir>/plex-delete-actions.json): every attempt the
// slice makes or refuses, with the server's answer when
// there was one. The file-level deletes' log
// (deleted-actions.json) is a separate record for a
// separate mechanism — a Plex delete never lands there, and
// a file delete never lands here.
type PlexDeleteAction struct {
	Time      string `json:"time"` // RFC3339 UTC
	Path      string `json:"path"`
	Title     string `json:"title,omitempty"`
	Scope     string `json:"scope"` // "version" | "item"
	RatingKey string `json:"rating_key,omitempty"`
	MediaID   string `json:"media_id,omitempty"`
	Result    string `json:"result"` // "deleted" | "refused" | "error"
	Status    int    `json:"status,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// appendPlexDeleteActionLocked appends one record to the
// audit log (created when absent; an unparseable existing
// file starts over rather than blocking an action — the
// deleted-actions.json pattern). Caller holds s.mu.
func (s *Service) appendPlexDeleteActionLocked(rec PlexDeleteAction) error {
	file := filepath.Join(s.dataDir, "plex-delete-actions.json")
	list := []PlexDeleteAction{}
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

// auditPlexDelete records one attempt. An audit failure is
// reported in the outcome's detail — never hidden, never
// fatal to the batch.
func (s *Service) auditPlexDelete(rec PlexDeleteAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendPlexDeleteActionLocked(rec)
}

// markPlexDeletedLocked records a successful delete's local
// paths as deleted-pending-rescan. Caller holds s.mu.
func (s *Service) markPlexDeletedLocked(paths ...string) {
	if s.plexDeleted == nil {
		s.plexDeleted = map[string]bool{}
	}
	for _, p := range paths {
		s.plexDeleted[p] = true
	}
}

// PlexDelete attempts the requested deletions through Plex,
// in order. The batch is gated on a fresh preflight and on
// the retained match snapshot; each entry is then attempted
// (or refused, with the reason) and audited independently —
// one entry's failure never stops or swallows another's.
func (s *Service) PlexDelete(entries []PlexDeleteEntry) PlexDeleteResult {
	preflight := s.PlexPreflight()
	res := PlexDeleteResult{Preflight: preflight, Outcomes: []PlexDeleteOutcome{}}
	p := s.plexSnapshot()
	s.mu.Lock()
	snapshot := s.plexMatchByPath
	s.mu.Unlock()
	client := p.client()
	for _, entry := range entries {
		scope := entry.Scope
		if scope == "" {
			scope = "version"
		}
		out := PlexDeleteOutcome{Path: entry.Path, Scope: scope}
		rec := PlexDeleteAction{
			Time: time.Now().UTC().Format(time.RFC3339),
			Path: entry.Path, Scope: scope, Result: "refused",
		}
		finish := func() {
			// Copy the final outcome facts into the audit
			// record and write it — every attempt, always.
			rec.Title = out.Title
			rec.Status = out.Status
			rec.Detail = out.Detail
			if err := s.auditPlexDelete(rec); err != nil && out.Detail == "" {
				out.Detail = "deleted through Plex, but the audit log write failed: " + err.Error()
			} else if err != nil {
				out.Detail += " (audit log write failed: " + err.Error() + ")"
			}
			res.Outcomes = append(res.Outcomes, out)
		}
		if scope != "version" && scope != "item" {
			out.Detail = fmt.Sprintf("unknown delete scope %q — want \"version\" or \"item\"", entry.Scope)
			finish()
			continue
		}
		m := snapshot[entry.Path]
		if m == nil {
			// No match in the retained cross-check snapshot.
			// There is deliberately NO fallback to deleting
			// the file directly — through-Plex or not at all.
			out.Detail = "no Plex match for this file in the last cross-check — run the cross-check again; files Plex doesn't know can't be deleted through Plex"
			finish()
			continue
		}
		out.Title = m.Title
		rec.Title = m.Title
		rec.RatingKey = m.RatingKey
		rec.MediaID = m.MediaID
		if !preflight.Ready {
			out.Detail = "delete via Plex is not ready: " + strings.Join(preflight.Problems, "; ")
			finish()
			continue
		}
		if m.RatingKey == "" {
			out.Detail = "Plex did not report an item ID for this file — it can't be addressed for deletion"
			finish()
			continue
		}
		var delPath string
		if scope == "item" {
			delPath = "/library/metadata/" + url.PathEscape(m.RatingKey)
		} else {
			if m.MediaID == "" {
				out.Detail = "Plex did not report a media version ID for this file — the whole item could still be deleted by choosing the item scope explicitly"
				finish()
				continue
			}
			delPath = "/library/metadata/" + url.PathEscape(m.RatingKey) + "/media/" + url.PathEscape(m.MediaID)
		}
		status, body, err := client.del(delPath)
		switch {
		case err != nil:
			rec.Result = "error"
			out.Detail = err.Error()
		case status >= 200 && status < 300:
			rec.Result = "deleted"
			out.OK = true
			out.Status = status
			// Mark the row(s) deleted-pending-rescan: this
			// file, and for an item delete every version's
			// files with it.
			s.mu.Lock()
			if scope == "item" {
				kin := []string{}
				for path, other := range snapshot {
					if other.RatingKey == m.RatingKey {
						kin = append(kin, path)
					}
				}
				s.markPlexDeletedLocked(kin...)
			} else {
				s.markPlexDeletedLocked(entry.Path)
			}
			s.mu.Unlock()
		case status == 403:
			// §C.1: deletion not allowed by the server's
			// options. Surfaced, never worked around.
			rec.Result = "refused"
			out.Status = status
			out.Detail = "Plex refused the delete (HTTP 403) — media deletion is not allowed for this server/account"
			if body != "" {
				out.Detail += ": " + truncateDetail(body)
			}
		default:
			// §C.1's wild failure mode (400 and friends):
			// the server's own answer, verbatim-ish.
			rec.Result = "error"
			out.Status = status
			out.Detail = fmt.Sprintf("Plex answered HTTP %d", status)
			if body != "" {
				out.Detail += ": " + truncateDetail(body)
			}
		}
		finish()
	}
	return res
}

// truncateDetail bounds a server body excerpt for outcome
// and audit display: the first 300 bytes, on one line.
func truncateDetail(body string) string {
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > 300 {
		return body[:300] + "…"
	}
	return body
}
