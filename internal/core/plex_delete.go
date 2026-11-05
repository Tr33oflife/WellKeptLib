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
//     entry. ONE exception, §C.4's own rule (restored spec,
//     reconciled in v1.5.1): when the matched item has
//     exactly one version, the action BECOMES the item
//     delete (DELETE /library/metadata/{ratingKey}) —
//     deleting its only version and deleting the item are
//     the same act, and the item shape is the one that
//     leaves no empty shell behind. The outcome and the
//     audit record the scope the action became. Batches
//     are allowed (§5 ruling): one call, one confirm in
//     the UI listing everything — the service then
//     attempts each entry in order.
//   - EVERY attempt — success, refusal, server error — is
//     audited to plex-delete-actions.json in the data dir.
//     Server refusals surface with their status and the
//     server's own words, never swallowed (§C.1's 403/400
//     discipline). A 404 is neither: the item is already
//     gone, so the attempt is RESOLVED — recorded as
//     "resolved", marked like a success, and said so in
//     the outcome (§C.4; reconciled in v1.5.1).
//   - A successful (or resolved) delete does NOT rewrite
//     the scan: the file's row stays and is marked
//     deleted-pending-rescan in the matches view. The next
//     scan is what removes it everywhere (the file really
//     is gone from disk — Plex removed it, or already had).
//
// v1.5.2 completes §C.4's record-keeping, the four points
// RESULTS-1.5.1 reported as open: the confirm payload now
// carries the SERVER each entry will be deleted from and
// the SIZE(S) of the file(s) it will remove (§C.4's "title,
// server, file path(s), size(s)"); every audit record names
// its server (§C.4's audit field list); the audit is read
// back and appears in History (PlexDeleteActions,
// GET /api/plex/delete-actions); and a 403 re-reads the
// preflight facts and names the one that changed (§C.4's
// "(say which preflight fact changed)") — or says plainly
// that every readable check still passes. Delete semantics
// are exactly v1.5.0 + v1.5.1's: no request path, scope
// rule, or outcome class moved.
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

// changedPreflightFact phrases a FRESH preflight as the
// one fact a 403 turns on (§C.4: "say which preflight fact
// changed"). The facts are checked in gate order —
// reachable, owned, deletion enabled — and the first one
// that no longer holds is named. When the fresh preflight
// is Ready (every readable fact still holds), the sentence
// says exactly that: the server refused although the
// checks the app can read all still pass. Pure: a
// preflight in, a sentence out.
func changedPreflightFact(now PlexPreflight) string {
	switch {
	case !now.Configured:
		return "What changed: Plex is no longer configured in the app."
	case !now.Reachable:
		return "What changed: the server no longer answers this sign-in — it did when this batch was checked."
	case now.OwnedKnown && !now.Owned:
		return "What changed: the signed-in account no longer owns this server."
	case !now.OwnedKnown:
		return "What changed: the app can no longer confirm the signed-in account owns this server."
	case now.DeletionKnown && !now.DeletionEnabled:
		return "What changed: “Allow media deletion” is now turned off on the server (in Plex: Settings > Library, advanced)."
	case !now.DeletionKnown:
		return "What changed: the app can no longer confirm “Allow media deletion” is on for this server."
	default:
		return "Every check still passes — signed in, the account owns the server, and “Allow media deletion” is on — but Plex refused anyway."
	}
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
	// Server / Size (v1.5.2, §C.4's confirmation fields):
	// the connected server's name as the app knows it, and
	// the size of the scanned file this match is built on —
	// the scan item's own retained size, the file a version
	// delete removes.
	Server string
	Size   int64
}

// PlexItemFile is one file of a matched Plex item: a path
// and its size, both from the scan the matches were built
// against. An item-scope delete removes every one of these.
type PlexItemFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// PlexFileMatch is the matches view's row for one matched
// scanned file. It is also the confirm payload's source:
// the batch confirm renders title, server, file path(s)
// and size(s) from these fields (§C.4).
type PlexFileMatch struct {
	Path                 string         `json:"path"`
	Title                string         `json:"title"`
	Type                 string         `json:"type"`
	Section              string         `json:"section"`
	RatingKey            string         `json:"rating_key"`
	MediaID              string         `json:"media_id"`
	Versions             int            `json:"versions"`
	Server               string         `json:"server,omitempty"`
	Size                 int64          `json:"size"`
	ItemFiles            []PlexItemFile `json:"item_files"`
	DeletedPendingRescan bool           `json:"deleted_pending_rescan"`
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
	// The server every match belongs to (§C.3: the match
	// records which server it came from) — the name the
	// server calls itself in its root document, else the
	// name held in the saved settings.
	serverName := preflight.ServerName
	if serverName == "" {
		serverName = p.ServerName
	}
	s.mu.Lock()
	scanned := map[string]bool{}
	sizes := map[string]int64{}
	for _, it := range s.results.Items {
		scanned[it.Path] = true
		sizes[it.Path] = it.Size
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
			Server: serverName, Size: sizes[local],
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
	// Every version's files per item (v1.5.2): the confirm
	// for an item-scope entry lists each file it removes,
	// with sizes — all of them are matches in this snapshot.
	itemFiles := map[string][]PlexItemFile{}
	for _, path := range keys {
		m := snapshot[path]
		if m.RatingKey == "" {
			continue
		}
		itemFiles[m.RatingKey] = append(itemFiles[m.RatingKey], PlexItemFile{Path: m.Path, Size: m.Size})
	}
	for _, path := range keys {
		m := snapshot[path]
		files := itemFiles[m.RatingKey]
		if files == nil {
			files = []PlexItemFile{}
		}
		res.Matches = append(res.Matches, PlexFileMatch{
			Path: m.Path, Title: m.Title, Type: m.Type, Section: m.Section,
			RatingKey: m.RatingKey, MediaID: m.MediaID, Versions: m.Versions,
			Server: m.Server, Size: m.Size, ItemFiles: files,
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
// files go). Item scope is never implied for a multi-version
// item; it must be named. For a SINGLE-version item the
// distinction does not exist (§C.4): a "version" request
// resolves to the item delete, and the outcome reports the
// scope the action became.
type PlexDeleteEntry struct {
	Path  string `json:"path"`
	Scope string `json:"scope,omitempty"` // "version" | "item"
}

// PlexDeleteOutcome is one entry's result: what was asked,
// whether Plex did it, and — when not — the refusal or the
// server's answer, in words. OK is true both when Plex
// deleted the item (2xx) and when Plex reported it already
// gone (404 — resolved; the Detail says which).
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
	// Server (v1.5.2, §C.4's audit field list): the server
	// the delete was issued against, named as the app knows
	// it. omitempty on purpose: audit files written by
	// v1.5.0/v1.5.1 have no such field and must read back
	// exactly as they were written.
	Server string `json:"server,omitempty"`
	Result string `json:"result"` // "deleted" | "resolved" | "refused" | "error"
	Status int    `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
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

// PlexDeleteActions reads the delete audit back, newest
// first — the read path §C.4's "appears in History" names
// (v1.5.2; GET /api/plex/delete-actions). A missing or
// unparseable log is an honest empty list, never an error:
// the History surface renders its empty state from it.
// Entries written before v1.5.2 simply carry no server.
func (s *Service) PlexDeleteActions() []PlexDeleteAction {
	out := []PlexDeleteAction{}
	b, err := os.ReadFile(filepath.Join(s.dataDir, "plex-delete-actions.json"))
	if err != nil {
		return out
	}
	var list []PlexDeleteAction
	if err := json.Unmarshal(b, &list); err != nil {
		return out
	}
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i])
	}
	return out
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
	// The server this batch is issued against (§C.4: the
	// audit names it) — the preflight's own server name,
	// else the name in the saved settings.
	serverName := preflight.ServerName
	if serverName == "" {
		serverName = p.ServerName
	}
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
			Server: serverName,
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
		// §C.4's single-version rule: when the matched item
		// has exactly one version, the action becomes the
		// item delete — whatever scope was named, the item
		// DELETE is what is sent, and the outcome + audit
		// record the scope the action became.
		effScope := scope
		if effScope == "version" && m.Versions <= 1 {
			effScope = "item"
			out.Scope = effScope
			rec.Scope = effScope
		}
		var delPath string
		if effScope == "item" {
			delPath = "/library/metadata/" + url.PathEscape(m.RatingKey)
		} else {
			if m.MediaID == "" {
				out.Detail = "Plex did not report a media version ID for this file — the whole item could still be deleted by choosing the item scope explicitly"
				finish()
				continue
			}
			delPath = "/library/metadata/" + url.PathEscape(m.RatingKey) + "/media/" + url.PathEscape(m.MediaID)
		}
		// markDeleted records the deleted-pending-rescan
		// marks for a delete that took effect (a 2xx, or a
		// 404 resolved): this file, and for an item delete
		// every version's files with it.
		markDeleted := func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if effScope == "item" {
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
			markDeleted()
		case status == 404:
			// §C.4: 404 = already gone — treat as resolved,
			// say so. The end state is the one the user
			// asked for, so the attempt records as
			// "resolved" (not "deleted": Plex did not
			// delete anything just now) and the row is
			// marked exactly as a success marks it.
			rec.Result = "resolved"
			out.OK = true
			out.Status = status
			out.Detail = "already gone in Plex — treated as resolved"
			if body != "" {
				out.Detail += ": " + truncateDetail(body)
			}
			markDeleted()
		case status == 403:
			// §C.1: deletion not allowed by the server's
			// options. Surfaced, never worked around.
			// §C.4 (v1.5.2): the refusal names WHICH
			// preflight fact changed — the facts are
			// re-read now (reads only; the app never
			// writes the setting) and the stale one is
			// named; if every readable fact still holds,
			// the detail says that instead of guessing.
			rec.Result = "refused"
			out.Status = status
			out.Detail = "Plex refused the delete (HTTP 403). " +
				changedPreflightFact(runPlexPreflight(p, s.accountTokenForPreflight()))
			if body != "" {
				out.Detail += " Server said: " + truncateDetail(body)
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
