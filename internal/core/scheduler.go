package core

// scheduler.go — in-process scheduled scans (v1.0.0,
// FEATURE-PLAN §5).
//
// A ticker in the server process wakes every minute and asks:
// is a scan due? If yes and the operation guard is free, it
// starts a scan through the SAME StartScan path a manual click
// uses (trigger "scheduled") — so scheduled scans inherit root
// validation, history records, webhooks, and state publication
// for free. No cron, no systemd timer, no OS integration: the
// schedule lives and dies with the app process, which matches
// how the app is actually run (an always-on seedbox service or
// a desktop exe left open).
//
// Schedule shapes (config): EITHER an interval in hours
// (schedule_interval_hours > 0 — due when the anchor is at
// least that old) OR a daily local time (schedule_daily_time
// "HH:MM" — due once a day when the local clock passes it).
// The interval wins when both are set. The ANCHOR is the last
// scheduled action (fire or skip), persisted as
// schedule_last_run_unix; before any scheduled action exists it
// falls back to the last completed scan's time, then to "now"
// (a freshly enabled schedule first fires one interval later —
// except on the startup check, where a library already overdue
// fires immediately: the catch-up rule below).
//
// Skip semantics (plan §5): a tick that comes due while a scan
// is running or a file op holds the guard is RECORDED and the
// slot is CONSUMED — the anchor advances and the note explains
// — never queued. Queuing would fire a second full-library scan
// the moment a long one ends, which is exactly the pile-up the
// plan forbids.
//
// Catch-up (plan §5): missed slots never stack. If the app was
// off through several intervals, ONE scan is due, not N.

import (
	"fmt"
	"time"
)

// parseDailyTime parses the "HH:MM" schedule setting (24-hour,
// local). Used by SaveConfig validation and the due math.
func parseDailyTime(v string) (hour, min int, err error) {
	var h, m int
	n, serr := fmt.Sscanf(v, "%d:%d", &h, &m)
	if serr != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 || len(v) != 5 {
		return 0, 0, fmt.Errorf("daily scan time %q is not HH:MM (24-hour)", v)
	}
	return h, m, nil
}

// scheduleDue is the scheduler's decision function — pure, so
// the whole due/skip/catch-up matrix is pinned by unit tests
// with a fake clock. lastScanAt is the most recent completed
// scan's time (zero when none).
//
// Returns (due, next): when due is false, next is the earliest
// future fire time (zero when no schedule is active). When due
// is true, next is the fire time AFTER this one (so the UI can
// still show what's coming).
func scheduleDue(cfg Config, lastFire, lastScanAt, now time.Time) (bool, time.Time) {
	if !cfg.ScheduleEnabled {
		return false, time.Time{}
	}
	if cfg.ScheduleIntervalHours > 0 {
		ivl := time.Duration(cfg.ScheduleIntervalHours) * time.Hour
		anchor := lastFire
		if anchor.IsZero() {
			anchor = lastScanAt
		}
		if anchor.IsZero() {
			// Nothing has ever run: first fire one interval out.
			return false, now.Add(ivl)
		}
		next := anchor.Add(ivl)
		if !next.After(now) {
			// Overdue — possibly by many intervals (app was off):
			// exactly one scan is due (catch-up collapses).
			return true, now.Add(ivl)
		}
		return false, next
	}
	if cfg.ScheduleDailyTime != "" {
		h, m, err := parseDailyTime(cfg.ScheduleDailyTime)
		if err != nil {
			return false, time.Time{}
		}
		loc := now.Location()
		today := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
		firedToday := !lastFire.IsZero() && lastFire.Year() == now.Year() && lastFire.YearDay() == now.YearDay()
		if now.Before(today) {
			return false, today
		}
		if firedToday {
			return false, today.AddDate(0, 0, 1)
		}
		return true, today.AddDate(0, 0, 1)
	}
	return false, time.Time{}
}

// scheduleNextRunLocked computes the next fire time for the
// State view. Caller holds s.mu.
func (s *Service) scheduleNextRunLocked(now time.Time) (time.Time, bool) {
	if !s.cfg.ScheduleEnabled {
		return time.Time{}, false
	}
	lastFire, lastScanAt := s.scheduleAnchorsLocked()
	_, next := scheduleDue(*s.cfg, lastFire, lastScanAt, now)
	if next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

// scheduleAnchorsLocked returns (last scheduled action, last
// completed scan time). Caller holds s.mu.
func (s *Service) scheduleAnchorsLocked() (time.Time, time.Time) {
	var lastFire time.Time
	if s.cfg.ScheduleLastRunUnix > 0 {
		lastFire = time.Unix(s.cfg.ScheduleLastRunUnix, 0)
	}
	var lastScanAt time.Time
	if len(s.history) > 0 {
		if t, err := time.Parse(time.RFC3339, s.history[len(s.history)-1].ScannedAt); err == nil {
			lastScanAt = t
		}
	}
	return lastFire, lastScanAt
}

// SchedulerTick is one scheduler decision at `now` — exported
// for the tests; the running ticker calls it every minute. It
// answers: due? busy? and acts: fire, or record the skip. The
// returned note describes the action taken ("" when nothing was
// due or the schedule is off).
func (s *Service) SchedulerTick(now time.Time) string {
	s.mu.Lock()
	cfg := s.cfg
	lastFire, lastScanAt := s.scheduleAnchorsLocked()
	due, _ := scheduleDue(*cfg, lastFire, lastScanAt, now)
	if !cfg.ScheduleEnabled || !due {
		s.mu.Unlock()
		return ""
	}
	busy := s.state.Running || s.fileOpBusy
	// Consume the slot either way (skip = recorded, not queued):
	// advance the anchor and persist it with the config.
	s.cfg.ScheduleLastRunUnix = now.Unix()
	_ = s.saveConfigLocked()
	note := ""
	if busy {
		note = fmt.Sprintf("skipped %s — a scan or file operation was already running", now.Format("15:04"))
		s.schedNote = note
		s.schedNoteAt = now
		s.mu.Unlock()
		return note
	}
	note = fmt.Sprintf("started a scheduled scan at %s", now.Format("15:04"))
	s.schedNote = note
	s.schedNoteAt = now
	roots := append([]string{}, cfg.Roots...)
	s.mu.Unlock()
	// Fire through the standard start path (guard re-checked
	// there — a manual scan that slipped in between the check
	// and here turns this into a recorded refusal below).
	if _, err := s.StartScanTriggered(roots, "scheduled"); err != nil {
		s.mu.Lock()
		s.schedNote = fmt.Sprintf("scheduled scan at %s did not start: %v", now.Format("15:04"), err)
		s.mu.Unlock()
		return s.schedNote
	}
	return note
}

// StartScheduler launches the minute ticker. Called ONLY by the
// server entry point: the CLI one-shot and the tests never
// start it (a library call to NewService must not be able to
// fire a scan an hour later on its own). Safe to call once;
// further calls are no-ops.
func (s *Service) StartScheduler() {
	s.mu.Lock()
	if s.schedulerLive {
		s.mu.Unlock()
		return
	}
	s.schedulerLive = true
	s.mu.Unlock()
	go func() {
		// Startup check: a schedule already overdue when the
		// app boots gets its ONE catch-up scan now, not at the
		// first minute tick (plan §5).
		s.SchedulerTick(time.Now())
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for now := range t.C {
			s.SchedulerTick(now)
		}
	}()
}
