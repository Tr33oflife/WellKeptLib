package core

// features_100_test.go — the v1.0.0 milestone wave: hardlink
// identity + honest reclaimable math, the report card (numbers
// == API numbers), scan history + deltas, the in-process
// scheduler, the scan-finished webhook, the data-dir lock, and
// the OpenSubtitles client (verified against a mock of the
// spec's documented shapes — see opensubtitles.go's
// verification-boundary note; no live API is claimed).
//
// The fixture-integration test at the end pins the standard
// corpus: headline 11/2/2/6 untouched, reclaimable == apparent
// (fixture files are distinct inodes), history records written
// with trigger "manual".

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- hardlinks: the duplicates math ----

// hlItem builds a movie item with an explicit storage identity.
func hlItem(path string, dev, ino uint64, size int64) *Item {
	it := mkItem(path, "Linked Film", 2020, "", size, 1080, "samehash")
	it.Dev, it.Ino = dev, ino
	return it
}

// Three entries: two hardlinks of one file (same dev+ino) and
// one separate file, all the same size. Apparent waste counts
// both extra entries; reclaimable counts only the separate
// file once.
func TestHardlinkMathTwoLinksOneSeparate(t *testing.T) {
	items := []*Item{
		hlItem("/lib/a.mkv", 7, 100, 1000),
		hlItem("/lib/b.mkv", 7, 100, 1000), // hardlink of a
		hlItem("/lib/c.mkv", 7, 200, 1000), // separate file
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.FileEntries != 3 || g.PhysicalCopies != 2 {
		t.Errorf("entries/physical = %d/%d, want 3/2", g.FileEntries, g.PhysicalCopies)
	}
	if g.WastedBytes != 2000 {
		t.Errorf("apparent wasted = %d, want 2000 (both extra entries at full size)", g.WastedBytes)
	}
	if g.ReclaimableBytes != 1000 {
		t.Errorf("reclaimable = %d, want 1000 (only the separate physical file)", g.ReclaimableBytes)
	}
	if !g.HardlinkKnown {
		t.Errorf("HardlinkKnown = false, want true (every copy's identity captured)")
	}
	links := 0
	for _, c := range g.Copies {
		if c.HardlinkOfKeeper {
			links++
		}
	}
	if links != 1 {
		t.Errorf("copies flagged hardlink-of-keeper = %d, want exactly 1", links)
	}
}

// All entries are hardlinks of ONE file: deleting down to one
// copy frees nothing at all.
func TestHardlinkMathAllOneFile(t *testing.T) {
	items := []*Item{
		hlItem("/lib/a.mkv", 7, 100, 1000),
		hlItem("/lib/b.mkv", 7, 100, 1000),
		hlItem("/lib/c.mkv", 7, 100, 1000),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	g := groups[0]
	if g.PhysicalCopies != 1 || g.ReclaimableBytes != 0 {
		t.Errorf("physical/reclaimable = %d/%d, want 1/0", g.PhysicalCopies, g.ReclaimableBytes)
	}
	if g.WastedBytes != 2000 {
		t.Errorf("apparent wasted = %d, want 2000 (the apparent figure keeps its old meaning)", g.WastedBytes)
	}
}

// Unknown identity (dev/ino zero): the fallback arithmetic —
// reclaimable equals apparent, and the group is flagged
// unknown so the UI can label it.
func TestHardlinkMathUnknownFallback(t *testing.T) {
	items := []*Item{
		hlItem("/lib/a.mkv", 0, 0, 1000),
		hlItem("/lib/b.mkv", 0, 0, 1000),
		hlItem("/lib/c.mkv", 0, 0, 1000),
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	g := groups[0]
	if g.HardlinkKnown {
		t.Errorf("HardlinkKnown = true with zero identities, want false")
	}
	if g.PhysicalCopies != 3 || g.ReclaimableBytes != g.WastedBytes || g.ReclaimableBytes != 2000 {
		t.Errorf("fallback: physical=%d reclaimable=%d wasted=%d, want 3/2000/2000",
			g.PhysicalCopies, g.ReclaimableBytes, g.WastedBytes)
	}
}

// Real files, real links: the walk-time identity capture must
// see both entries of an os.Link pair as the same inode, and
// fileIdentityFor must agree with what the scan stored.
func TestHardlinkWalkCaptureRealFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "Linked Film (2020).mkv")
	b := filepath.Join(dir, "Linked Film (2020).mp4")
	if err := os.WriteFile(a, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, b); err != nil {
		t.Fatalf("os.Link: %v", err)
	}
	ia, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	da, na, _, oka := fileIdentityFor(ia, a)
	db, nb, _, okb := fileIdentityFor(ib, b)
	if !oka || !okb {
		t.Fatalf("identity capture failed on this platform (ok=%v/%v) — the fallback must be honest, not silent", oka, okb)
	}
	if da != db || na != nb {
		t.Errorf("hardlinked files got different identities (%d:%d vs %d:%d)", da, na, db, nb)
	}
}

// ---- report card: numbers == API numbers ----

func TestReportMatchesAPIOnFixture(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	sum := svc.State().Scan.Summary
	r := svc.Report()
	if r.MediaCount != sum.MediaCount || r.DuplicateGroups != sum.DuplicateGroups ||
		r.DuplicateWasted != sum.DuplicateWasted || r.DuplicateReclaimable != sum.DuplicateReclaimable ||
		r.SubtitleFindings != sum.SubtitleFindings || r.TotalBytes != sum.TotalBytes ||
		r.SubtitleFileCount != sum.SubtitleFileCount || r.GapEpisodes != sum.GapEpisodes ||
		r.CoverageCovered != sum.CoverageCovered || r.CoverageNone != sum.CoverageNoSubtitles {
		t.Errorf("report disagrees with the state summary:\nreport = %+v\nsummary = %+v", r, sum)
	}
	if r.HealthTotal != len(svc.Health()) {
		t.Errorf("report health total = %d, Health() = %d", r.HealthTotal, len(svc.Health()))
	}
	if r.SubtitleFindings != len(svc.Subtitles()) {
		t.Errorf("report subtitle findings = %d, Subtitles() = %d", r.SubtitleFindings, len(svc.Subtitles()))
	}
	if r.GapRows != len(svc.EpisodeGaps()) {
		t.Errorf("report gap rows = %d, EpisodeGaps() = %d", r.GapRows, len(svc.EpisodeGaps()))
	}
	// The fixture files are distinct inodes: reclaimable must
	// equal the apparent figure on the shipped baseline.
	if r.DuplicateReclaimable != r.DuplicateWasted {
		t.Errorf("fixture reclaimable %d != apparent %d (distinct inodes)", r.DuplicateReclaimable, r.DuplicateWasted)
	}
	// Identity was captured for every fixture item on this
	// platform (Linux CI/dev): no unknown-identity groups.
	if r.DuplicateUnknownGroups != 0 {
		t.Errorf("unknown-identity groups = %d, want 0", r.DuplicateUnknownGroups)
	}
	for _, it := range svc.Library("", "") {
		if it.Ino == 0 {
			t.Errorf("item %s carries no inode — walk capture missed it", it.Path)
		}
	}
}

func TestReportExports(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	csvBytes, err := svc.ReportCSV()
	if err != nil {
		t.Fatal(err)
	}
	csvText := string(csvBytes)
	for _, want := range []string{"section,metric,value,detail", "reclaimable_bytes", "media_count,11", "duplicate_group"} {
		if !strings.Contains(csvText, want) {
			t.Errorf("CSV export missing %q", want)
		}
	}
	htmlBytes, err := svc.ReportHTML()
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, "WellKeptLib library report") || !strings.Contains(html, "Actually reclaimable") {
		t.Errorf("HTML report missing its title or the reclaimable section")
	}
	// Self-containment law: no external assets of any kind.
	for _, bad := range []string{"src=\"http", "href=\"http", "<script", "url(http", "@import"} {
		if strings.Contains(html, bad) {
			t.Errorf("HTML report is not self-contained: contains %q", bad)
		}
	}
}

// ---- scan history + deltas ----

func TestHistoryRecordsAndDelta(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	h := svc.History()
	if len(h) != 1 {
		t.Fatalf("history records = %d, want 1 after one scan", len(h))
	}
	if h[0].Trigger != "manual" {
		t.Errorf("trigger = %q, want manual", h[0].Trigger)
	}
	if h[0].Delta.Available {
		t.Errorf("the oldest record claims a delta")
	}
	if h[0].Summary.MediaCount != 11 {
		t.Errorf("recorded media count = %d, want 11", h[0].Summary.MediaCount)
	}
	// Remove one media file, rescan: the delta must show it.
	victim := filepath.Join(lib, "Movies", "Control Film (2022)", "Control Film (2022).mkv")
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("fixture victim missing: %v", err)
	}
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	h = svc.History()
	if len(h) != 2 {
		t.Fatalf("history records = %d, want 2", len(h))
	}
	d := h[0].Delta
	if !d.Available || d.RemovedFiles != 1 || d.AddedFiles != 0 {
		t.Errorf("delta = %+v, want available with removed=1 added=0", d)
	}
	if d.MediaDelta != -1 {
		t.Errorf("media delta = %d, want -1", d.MediaDelta)
	}
}

func TestHistoryDeltaMissingRootSuppression(t *testing.T) {
	mk := func(p string) ScanSnapshotEntry { return ScanSnapshotEntry{Size: 10, Mtime: 1} }
	prev := ScanHistoryRecord{
		ScannedAt: "2026-01-01T00:00:00Z",
		Roots:     []string{"/a", "/b"},
		Files: map[string]ScanSnapshotEntry{
			"/a/one.mkv": mk(""), "/b/two.mkv": mk(""), "/b/three.mkv": mk(""),
		},
	}
	cur := ScanHistoryRecord{
		ScannedAt: "2026-01-02T00:00:00Z",
		Roots:     []string{"/a"},
		Files:     map[string]ScanSnapshotEntry{"/a/one.mkv": mk("")},
	}
	d := computeScanDelta(prev, cur)
	if d.RemovedFiles != 0 {
		t.Errorf("removed = %d, want 0 — root /b simply wasn't scanned (unmounted-drive case)", d.RemovedFiles)
	}
	if len(d.RootWarnings) != 1 || !strings.Contains(d.RootWarnings[0], "/b") {
		t.Errorf("root warnings = %v, want one naming /b", d.RootWarnings)
	}
}

func TestHistoryRetention(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	for i := 0; i < historyRetention+5; i++ {
		svc.history = append(svc.history, ScanHistoryRecord{ScannedAt: fmt.Sprintf("t%03d", i)})
	}
	svc.mu.Unlock()
	svc.recordScanHistory(time.Now())
	if len(svc.History()) != historyRetention {
		t.Errorf("records after retention = %d, want %d", len(svc.History()), historyRetention)
	}
	// And the file on disk holds the same trimmed set.
	onDisk := loadScanHistory(svc.dataDir)
	if len(onDisk) != historyRetention {
		t.Errorf("persisted records = %d, want %d", len(onDisk), historyRetention)
	}
}

// ---- scheduler ----

func TestScheduleDueMatrix(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.Local)
	old := now.Add(-72 * time.Hour)
	cases := []struct {
		name     string
		cfg      Config
		lastFire time.Time
		lastScan time.Time
		wantDue  bool
		wantNext bool // a next time is reported
	}{
		{"disabled never fires", Config{ScheduleIntervalHours: 1}, old, old, false, false},
		{"interval not yet due", Config{ScheduleEnabled: true, ScheduleIntervalHours: 24}, now.Add(-time.Hour), time.Time{}, false, true},
		{"interval due", Config{ScheduleEnabled: true, ScheduleIntervalHours: 24}, now.Add(-25 * time.Hour), time.Time{}, true, true},
		{"interval from last scan when never fired", Config{ScheduleEnabled: true, ScheduleIntervalHours: 24}, time.Time{}, now.Add(-25 * time.Hour), true, true},
		{"fresh schedule waits one interval", Config{ScheduleEnabled: true, ScheduleIntervalHours: 24}, time.Time{}, time.Time{}, false, true},
		{"many missed intervals collapse to one due", Config{ScheduleEnabled: true, ScheduleIntervalHours: 1}, old, time.Time{}, true, true},
		{"daily before the time", Config{ScheduleEnabled: true, ScheduleDailyTime: "16:30"}, time.Time{}, time.Time{}, false, true},
		{"daily after the time, not fired", Config{ScheduleEnabled: true, ScheduleDailyTime: "14:00"}, now.Add(-26 * time.Hour), time.Time{}, true, true},
		{"daily already fired today", Config{ScheduleEnabled: true, ScheduleDailyTime: "14:00"}, now.Add(-time.Hour), time.Time{}, false, true},
		{"interval wins over daily", Config{ScheduleEnabled: true, ScheduleIntervalHours: 24, ScheduleDailyTime: "14:00"}, now.Add(-time.Hour), time.Time{}, false, true},
		{"enabled with no shape never fires", Config{ScheduleEnabled: true}, time.Time{}, time.Time{}, false, false},
	}
	for _, c := range cases {
		due, next := scheduleDue(c.cfg, c.lastFire, c.lastScan, now)
		if due != c.wantDue {
			t.Errorf("%s: due = %v, want %v", c.name, due, c.wantDue)
		}
		if c.wantNext && next.IsZero() {
			t.Errorf("%s: no next time reported", c.name)
		}
		if !c.wantNext && !next.IsZero() {
			t.Errorf("%s: next = %v, want none", c.name, next)
		}
	}
}

func TestSchedulerTickSkipsWhileBusyAndConsumesSlot(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.cfg.ScheduleEnabled = true
	svc.cfg.ScheduleIntervalHours = 1
	svc.cfg.Roots = []string{t.TempDir()}
	svc.history = []ScanHistoryRecord{{ScannedAt: time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)}}
	svc.state.Running = true // a scan is already running
	svc.mu.Unlock()
	note := svc.SchedulerTick(time.Now())
	if !strings.Contains(note, "skipped") {
		t.Fatalf("tick note = %q, want a recorded skip", note)
	}
	// The slot was consumed: an immediate second tick is not due.
	if note2 := svc.SchedulerTick(time.Now()); note2 != "" {
		t.Errorf("second tick note = %q, want none (skip consumed the slot, nothing queued)", note2)
	}
}

func TestSchedulerTickFiresWhenIdle(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.cfg.ScheduleEnabled = true
	svc.cfg.ScheduleIntervalHours = 1
	svc.cfg.Roots = []string{lib}
	svc.history = []ScanHistoryRecord{{ScannedAt: time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)}}
	svc.mu.Unlock()
	note := svc.SchedulerTick(time.Now())
	if !strings.Contains(note, "started a scheduled scan") {
		t.Fatalf("tick note = %q, want a started scan", note)
	}
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) && svc.State().Scan.Running {
		time.Sleep(25 * time.Millisecond)
	}
	if svc.State().Scan.Running {
		t.Fatal("scheduled scan did not finish")
	}
	h := svc.History()
	if len(h) != 2 || h[0].Trigger != "scheduled" {
		t.Errorf("history = %d records, newest trigger %q; want 2 records, newest scheduled", len(h), h[0].Trigger)
	}
	// Next-run is visible in state while enabled.
	if svc.State().ScheduleNextRun == "" {
		t.Errorf("state carries no schedule_next_run while the schedule is enabled")
	}
}

// ---- webhook ----

func webhookTestService(t *testing.T, url, flavor string) *Service {
	t.Helper()
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.cfg.WebhookEnabled = true
	svc.cfg.WebhookURL = url
	svc.cfg.WebhookFlavor = flavor
	svc.mu.Unlock()
	return svc
}

func TestWebhookGenericPayload(t *testing.T) {
	var got []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	svc := webhookTestService(t, srv.URL, "generic")
	sum := ScanSummary{MediaCount: 11, DuplicateGroups: 2, DuplicateWasted: 500, DuplicateReclaimable: 400, SubtitleFindings: 2, HealthFindings: 6, GapEpisodes: 3}
	svc.deliverWebhook("scan_finished", sum)
	var payload struct {
		App     string `json:"app"`
		Event   string `json:"event"`
		Summary struct {
			Media       int   `json:"media"`
			Reclaimable int64 `json:"duplicate_reclaimable_bytes"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("payload did not parse: %v (%s)", err, got)
	}
	if payload.App != "WellKeptLib" || payload.Event != "scan_finished" || payload.Summary.Media != 11 || payload.Summary.Reclaimable != 400 {
		t.Errorf("payload = %+v, want the WellKeptLib scan_finished summary", payload)
	}
	if gotCT != "application/json" {
		t.Errorf("content type = %q, want application/json", gotCT)
	}
	// Privacy by design: counts only — no path-looking content.
	if strings.Contains(string(got), "/") && strings.Contains(string(got), ".mkv") {
		t.Errorf("payload leaks file paths: %s", got)
	}
	if st := svc.WebhookLast(); !st.OK || st.Event != "scan_finished" {
		t.Errorf("delivery status = %+v, want a recorded OK", st)
	}
}

func TestWebhookFlavors(t *testing.T) {
	sum := ScanSummary{MediaCount: 3, DuplicateReclaimable: 10}
	// Discord: {"content": "WellKeptLib scan FAILED — ..."}.
	var dBody []byte
	dSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dSrv.Close()
	webhookTestService(t, dSrv.URL, "discord").deliverWebhook("scan_failed", sum)
	var discord struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(dBody, &discord); err != nil || !strings.Contains(discord.Content, "WellKeptLib scan FAILED") {
		t.Errorf("discord payload = %s, want a content line labeled FAILED", dBody)
	}
	// ntfy: plain text + Title header.
	var nTitle, nBody string
	nSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nTitle = r.Header.Get("Title")
		b, _ := io.ReadAll(r.Body)
		nBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer nSrv.Close()
	webhookTestService(t, nSrv.URL, "ntfy").deliverWebhook("scan_finished", sum)
	if nTitle != "WellKeptLib scan finished" || !strings.Contains(nBody, "3 media") {
		t.Errorf("ntfy delivery: title %q body %q", nTitle, nBody)
	}
}

// The fire-and-forget contract: a dead webhook URL must neither
// block nor fail a scan, and the failure lands in the status.
func TestWebhookDeadURLScanStillCompletes(t *testing.T) {
	// A listener that accepts and never answers: the worst
	// realistic endpoint. The delivery goroutine hangs until
	// the client timeout; the scan must not.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // never respond
		}
	}()
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.cfg.WebhookEnabled = true
	svc.cfg.WebhookURL = "http://" + ln.Addr().String() + "/hook"
	svc.cfg.WebhookFlavor = "generic"
	svc.mu.Unlock()
	start := time.Now()
	runScanToCompletion(t, svc, []string{lib})
	if elapsed := time.Since(start); elapsed > 120*time.Second {
		t.Errorf("scan took %v with a hung webhook — delivery must be fire-and-forget", elapsed)
	}
	if svc.State().Scan.Summary.MediaCount != 11 {
		t.Errorf("scan results wrong after webhook-armed scan")
	}
	// The delivery attempt eventually records its failure
	// (client timeout is 10s; allow generous slack).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if st := svc.WebhookLast(); st.Event == "scan_finished" && !st.OK {
			return // recorded failure, as designed
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("dead-webhook failure was never recorded in the status")
}

func TestWebhookDisabledStaysSilent(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.cfg.WebhookURL = srv.URL // URL set, switch OFF
	svc.mu.Unlock()
	svc.fireWebhook("scan_finished", ScanSummary{})
	time.Sleep(300 * time.Millisecond)
	if hits != 0 {
		t.Errorf("disabled webhook delivered %d requests, want 0", hits)
	}
}

// ---- data-dir lock ----

func TestDataLockRefusalAndStaleTakeover(t *testing.T) {
	dir := t.TempDir()
	l1, err := AcquireDataLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDataLock(dir); err == nil {
		t.Errorf("second acquire succeeded — the lock must refuse")
	} else if _, ok := err.(*DataLockError); !ok {
		t.Errorf("refusal error type = %T, want *DataLockError", err)
	}
	l1.Release()
	l2, err := AcquireDataLock(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	l2.Release()
	// Stale: a lock file naming a dead PID is taken over.
	dead := filepath.Join(dir, "app.lock")
	if err := os.WriteFile(dead, []byte("999999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l3, err := AcquireDataLock(dir)
	if err != nil {
		t.Fatalf("stale lock was not taken over: %v", err)
	}
	l3.Release()
}

// ---- automation settings are preferences, not tuning ----

func TestAutomationSettingsSurviveTuningReset(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := svc.GetConfig()
	cfg.ScheduleEnabled = true
	cfg.ScheduleIntervalHours = 12
	cfg.WebhookEnabled = true
	cfg.WebhookURL = "http://127.0.0.1:9/hook"
	cfg.WebhookFlavor = "discord"
	cfg.Workers = 99 // a tuning value, to prove the reset did run
	if err := svc.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetTuning(); err != nil {
		t.Fatal(err)
	}
	after := svc.GetConfig()
	if !after.ScheduleEnabled || after.ScheduleIntervalHours != 12 || !after.WebhookEnabled ||
		after.WebhookURL != cfg.WebhookURL || after.WebhookFlavor != "discord" {
		t.Errorf("tuning reset touched automation preferences: %+v", after)
	}
	if after.Workers == 99 {
		t.Errorf("tuning reset did not run (workers still 99)")
	}
	// SaveConfig validation: a nonsense daily time is an error,
	// an unknown flavor falls back to generic.
	bad := svc.GetConfig()
	bad.ScheduleDailyTime = "tea-time"
	if err := svc.SaveConfig(bad); err == nil {
		t.Errorf("an unparseable daily time saved without complaint")
	}
	bad.ScheduleDailyTime = ""
	bad.WebhookFlavor = "smoke-signals"
	if err := svc.SaveConfig(bad); err != nil {
		t.Fatal(err)
	}
	if got := svc.GetConfig().WebhookFlavor; got != "generic" {
		t.Errorf("unknown flavor became %q, want the generic fallback", got)
	}
}

// ---- OpenSubtitles: hash vectors ----

func TestOpenSubtitlesHashVectors(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		data []byte
		want string
	}{
		// Empty file: hash = size + 0 + 0.
		{"empty", []byte{}, "0000000000000000"},
		// 100 zero bytes: both 64 KiB windows cover the file,
		// every word is 0, so hash = size = 100 = 0x64.
		{"zeros-100", make([]byte, 100), "0000000000000064"},
		// 128 KiB of zeros: hash = size = 131072 = 0x20000.
		{"zeros-128k", make([]byte, 131072), "0000000000020000"},
		// One LE word of value 1 in an 8-byte file: head sum 1,
		// tail (same window) 1, size 8 → 10 = 0xa.
		{"one-word", []byte{1, 0, 0, 0, 0, 0, 0, 0}, "000000000000000a"},
	}
	for _, c := range cases {
		got, err := openSubtitlesHash(write(c.name+".bin", c.data))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: hash = %s, want %s", c.name, got, c.want)
		}
	}
	// A word placed only in the TAIL window of a >128 KiB file:
	// size 131080; tail starts at 65544; a LE 1 at the very end
	// lands in the tail's last word → hash = size + 1.
	big := make([]byte, 131080)
	big[len(big)-8] = 1
	got, err := openSubtitlesHash(write("big.bin", big))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%016x", uint64(131080+1)); got != want {
		t.Errorf("tail-word hash = %s, want %s", got, want)
	}
}

// ---- OpenSubtitles: mock-server flows (spec shapes) ----

// englishSRT / frenchSRT are detector-friendly samples (dense
// stopwords from the detector's own lists, well over the
// 10-word floor).
const englishSRT = `1
00:00:01,000 --> 00:00:03,000
You know that it was the one they said would come

2
00:00:04,000 --> 00:00:06,000
We have been here before and we will be here again

3
00:00:07,000 --> 00:00:09,000
This is what they have said to you and to all of them
`

const frenchSRT = `1
00:00:01,000 --> 00:00:03,000
Je sais que tu es dans la maison avec nous

2
00:00:04,000 --> 00:00:06,000
Elle est pour vous et pour tous les gens de cette ville

3
00:00:07,000 --> 00:00:09,000
Nous avons dit que cela serait plus pour leur bien
`

// osMock is the spec-shaped fake OpenSubtitles API: search by
// key only, login → token + user base_url, download link +
// quota from the response, and the file bytes themselves.
// downloadLang picks which sample the "file host" serves.
func osMock(t *testing.T, downloadLang string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/subtitles", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"missing api key"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_pages":1,"total_count":2,"page":1,"data":[
			{"id":"11","type":"subtitle","attributes":{"language":"en","release":"Test.Film.2020.1080p.WEB","download_count":50,"ratings":7.5,"hearing_impaired":false,"foreign_parts_only":false,"ai_translated":false,"machine_translated":false,"from_trusted":false,"moviehash_match":false,"files":[{"file_id":101,"file_name":"Test.Film.en.srt"}]}},
			{"id":"12","type":"subtitle","attributes":{"language":"en","release":"Test Film 2020 hash matched","download_count":5,"ratings":0,"hearing_impaired":true,"foreign_parts_only":false,"ai_translated":true,"machine_translated":false,"from_trusted":true,"moviehash_match":true,"files":[{"file_id":102,"file_name":"Test.Film.en.sdh.srt"}]}}
		]}`)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Username != "someuser" || body.Password != "somepass" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"bad credentials"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"mock-jwt","base_url":%q,"user":{"username":"someuser","level":"vip","vip":true,"downloads_count":100,"remaining_downloads":42}}`, srv.URL)
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-jwt" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"login required"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"link":%q,"file_name":"Test.Film.en.srt","requests":7,"remaining":17,"message":"Your quota will be renewed in 3 hours","reset_time":"2026-10-24T00:00:00Z"}`, srv.URL+"/files/sub.srt")
	})
	mux.HandleFunc("/files/sub.srt", func(w http.ResponseWriter, r *http.Request) {
		if downloadLang == "fr" {
			fmt.Fprint(w, frenchSRT)
			return
		}
		fmt.Fprint(w, englishSRT)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// osTestService builds a service with a scanned one-movie
// library and OpenSubtitles enabled against the mock.
func osTestService(t *testing.T, mockURL string, withLogin bool) (*Service, string) {
	t.Helper()
	lib := t.TempDir()
	movie := filepath.Join(lib, "Test Film (2020).mkv")
	if err := os.WriteFile(movie, []byte("not a real video, enough bytes to hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	old := osAPIBase
	osAPIBase = mockURL
	t.Cleanup(func() { osAPIBase = old })
	in := OSSecretsInput{Enabled: true, APIKey: "planted-api-key-123"}
	if withLogin {
		in.Username = "someuser"
		in.Password = "somepass"
	}
	if _, err := svc.SaveOSSecrets(in); err != nil {
		t.Fatal(err)
	}
	return svc, movie
}

func TestOpenSubtitlesSearchRanksHashMatches(t *testing.T) {
	mock := osMock(t, "en")
	svc, movie := osTestService(t, mock.URL, false)
	resp, err := svc.SearchSubtitles(OSSearchRequest{Path: movie, Mode: "hash", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	first := resp.Results[0]
	if first.FileID != 102 || !first.HashMatch || !first.FromTrusted || !first.HearingImpaired || !first.AITranslated {
		t.Errorf("first result = %+v, want the hash-matched trusted SDH/AI entry ranked first", first)
	}
	// Search does not log in: quota honestly unreported.
	if resp.Quota.LoggedIn || resp.Quota.Remaining != -1 {
		t.Errorf("quota after search = %+v, want logged-out / unreported (-1)", resp.Quota)
	}
	// Query mode works too (same mock answers any /subtitles).
	resp2, err := svc.SearchSubtitles(OSSearchRequest{Path: movie, Mode: "query", Language: "en"})
	if err != nil || len(resp2.Results) != 2 {
		t.Errorf("query search = %d results, err %v", len(resp2.Results), err)
	}
}

func TestOpenSubtitlesDownloadFlow(t *testing.T) {
	mock := osMock(t, "en")
	svc, movie := osTestService(t, mock.URL, true)
	res, err := svc.DownloadSubtitle(OSDownloadRequest{Path: movie, FileID: 101, Language: "en", Release: "Test.Film.2020.1080p.WEB"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(movie), "Test Film (2020).en.srt")
	if res.SavedPath != want {
		t.Errorf("saved path = %q, want %q", res.SavedPath, want)
	}
	data, err := os.ReadFile(want)
	if err != nil || string(data) != englishSRT {
		t.Errorf("saved file content wrong (err %v)", err)
	}
	if !res.Verified || res.DetectedLanguage != "en" {
		t.Errorf("verify = %v detected %q, want verified en", res.Verified, res.DetectedLanguage)
	}
	// Quota verbatim from the download response (the mock's
	// numbers — proof nothing is hardcoded).
	if res.Remaining != 17 || res.ResetTime != "2026-10-24T00:00:00Z" || !res.QuotaReported {
		t.Errorf("quota = %d / %q / reported %v, want the response's 17 + reset time", res.Remaining, res.ResetTime, res.QuotaReported)
	}
	if q := svc.Quota(); !q.LoggedIn || q.Remaining != 17 || q.Allowed != 100 {
		t.Errorf("session quota = %+v, want logged in, allowed 100 (login), remaining 17 (download)", q)
	}
	// Never overwrite: a second download of the same pick lands
	// at the .1 suffix and the first file is untouched.
	res2, err := svc.DownloadSubtitle(OSDownloadRequest{Path: movie, FileID: 101, Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	want2 := filepath.Join(filepath.Dir(movie), "Test Film (2020).en.1.srt")
	if res2.SavedPath != want2 {
		t.Errorf("second save = %q, want the .1 path %q", res2.SavedPath, want2)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("first sidecar vanished: %v", err)
	}
	// The action log recorded both placements.
	actions := svc.SubtitleActions()
	if len(actions) != 2 || actions[0].Saved != want || !actions[0].Verified {
		t.Errorf("actions log = %+v, want two entries starting with the verified placement", actions)
	}
	// And the sidecar joined the live results without a rescan.
	found := false
	for _, p := range svcResultsSubtitlePaths(svc) {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Errorf("downloaded sidecar not attached to the current results")
	}
}

// svcResultsSubtitlePaths is a tiny test accessor.
func svcResultsSubtitlePaths(svc *Service) []string {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return append([]string{}, svc.results.SubtitleFilePaths...)
}

func TestOpenSubtitlesDownloadMismatchIsHonest(t *testing.T) {
	mock := osMock(t, "fr") // the "English" entry actually serves French
	svc, movie := osTestService(t, mock.URL, true)
	res, err := svc.DownloadSubtitle(OSDownloadRequest{Path: movie, FileID: 101, Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified {
		t.Errorf("a French file listed as English verified — the detector check is broken")
	}
	if res.DetectedLanguage != "fr" {
		t.Errorf("detected = %q, want fr", res.DetectedLanguage)
	}
	if !strings.Contains(res.Note, "detector reads") {
		t.Errorf("note = %q, want the mismatch stated plainly", res.Note)
	}
	// Kept anyway — the user's call, per spec W5.
	if _, err := os.Stat(res.SavedPath); err != nil {
		t.Errorf("mismatched file was not kept: %v", err)
	}
}

func TestOpenSubtitlesSecretsNeverReturned(t *testing.T) {
	mock := osMock(t, "en")
	svc, _ := osTestService(t, mock.URL, true)
	// The settings view: presence only.
	view := svc.OSSecretsState()
	if !view.Enabled || !view.Configured || !view.HasLogin {
		t.Errorf("view = %+v, want enabled/configured/has-login", view)
	}
	// Plant and sweep every user-facing surface.
	surfaces := map[string]any{
		"os-settings": view,
		"diagnostics": svc.Diagnostics(),
		"state":       svc.State(),
		"config":      svc.GetConfig(),
		"report":      svc.Report(),
	}
	blob, err := json.Marshal(surfaces)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"planted-api-key-123", "somepass", "mock-jwt"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("secret %q appears in a user-facing surface", secret)
		}
	}
	// The secrets file itself is 0600.
	info, err := os.Stat(svc.osSecretsPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("opensubtitles.json mode = %o, want 600", perm)
	}
	// Search-only configuration: download refuses with the
	// login explanation (the documented login-first flow).
	svc2, movie2 := osTestService(t, mock.URL, false)
	if _, err := svc2.DownloadSubtitle(OSDownloadRequest{Path: movie2, FileID: 101, Language: "en"}); err == nil ||
		!strings.Contains(err.Error(), "username") {
		t.Errorf("key-only download error = %v, want the login-first explanation", err)
	}
	// And with the feature OFF entirely, even search refuses.
	svc3, movie3 := osTestService(t, mock.URL, true)
	if _, err := svc3.SaveOSSecrets(OSSecretsInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc3.SearchSubtitles(OSSearchRequest{Path: movie3, Mode: "hash", Language: "en"}); err == nil {
		t.Errorf("search ran while OpenSubtitles was disabled")
	}
}

// ---- fixture integration: the v1.0.0 baseline ----

func TestFixtureV100Baseline(t *testing.T) {
	lib := copyFixtureLibrary(t)
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runScanToCompletion(t, svc, []string{lib})
	sum := svc.State().Scan.Summary
	if sum.MediaCount != 11 || sum.DuplicateGroups != 2 || sum.SubtitleFindings != 2 || sum.HealthFindings != 6 {
		t.Fatalf("headline moved: %d/%d/%d/%d, want the untouched 11/2/2/6",
			sum.MediaCount, sum.DuplicateGroups, sum.SubtitleFindings, sum.HealthFindings)
	}
	if sum.DuplicateReclaimable != sum.DuplicateWasted {
		t.Errorf("reclaimable %d != apparent %d on distinct-inode fixtures", sum.DuplicateReclaimable, sum.DuplicateWasted)
	}
	h := svc.History()
	if len(h) != 1 || h[0].Trigger != "manual" || h[0].Summary.MediaCount != 11 {
		t.Errorf("history after the fixture scan = %+v, want one manual record at 11 media", h)
	}
	// The diagnostics surface knows the new world.
	d := svc.Diagnostics()
	if d.HistoryRecords != 1 || d.WebhookFlavor != "generic" {
		t.Errorf("diagnostics v1.0.0 fields = records %d flavor %q, want 1/generic", d.HistoryRecords, d.WebhookFlavor)
	}
}
