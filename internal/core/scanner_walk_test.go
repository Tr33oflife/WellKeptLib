package core

// scanner_walk_test.go — v0.3.8 walk-progress tests.
//
// Two layers, chosen to be deterministic rather than timing-lucky:
//
//   - The flush discipline (immediate on directory change,
//     throttled for file counters, Current naming the folder being
//     walked, the walk-warning finding) is pinned directly on the
//     walkProgress tracker with an injected fake clock — no real
//     walk, no sleeps, no flakes.
//   - An integration scan over a generated tree (12,000 files in
//     120 folders) observes the published state from a spinning
//     goroutine while the walk runs: the walk publishes >= 120
//     immediate directory flushes spread across it, so at least
//     one observation with Phase "walking", Done > 0 and Current
//     set is a practical certainty, not a race. The scan is then
//     cancelled (the test asserts walk behaviour, not probing).

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestWalkProgressFlushDiscipline(t *testing.T) {
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wp := newWalkProgress(svc)
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	now := base
	wp.now = func() time.Time { return now }
	wp.lastFlush = base

	scan := func() ScanState { return svc.State().Scan }

	// Entering a directory publishes Current IMMEDIATELY.
	wp.dirVisited("/lib/ShowA")
	st := scan()
	if st.Phase != "walking" || st.Current != "/lib/ShowA" || st.Done != 0 || st.Total != 0 {
		t.Fatalf("after dirVisited: state = %+v, want walking in /lib/ShowA at 0/0", st)
	}
	if st.CurrentSince == "" {
		t.Fatal("CurrentSince not stamped when Current changed")
	}

	// File counters are throttled: 99 files with a frozen clock
	// publish nothing new.
	for i := 0; i < 99; i++ {
		wp.fileSeen()
	}
	if st = scan(); st.Done != 0 {
		t.Fatalf("after 99 throttled files: Done = %d, want 0 (no flush yet)", st.Done)
	}
	// The 100th counted file crosses the count threshold.
	wp.fileSeen()
	if st = scan(); st.Done != 100 || st.Total != 100 {
		t.Fatalf("after 100 files: Done/Total = %d/%d, want 100/100", st.Done, st.Total)
	}
	// Below both thresholds again: nothing moves.
	for i := 0; i < 5; i++ {
		wp.fileSeen()
	}
	if st = scan(); st.Done != 100 {
		t.Fatalf("after 5 more throttled files: Done = %d, want 100", st.Done)
	}
	// Crossing the time threshold flushes the counter.
	now = now.Add(walkFlushInterval)
	wp.fileSeen()
	if st = scan(); st.Done != 106 {
		t.Fatalf("after interval flush: Done = %d, want 106", st.Done)
	}
	// A directory change flushes IMMEDIATELY even with no new
	// files and a frozen clock — this is what leaves the header
	// sitting on a folder whose read stalls.
	wp.dirVisited("/lib/ShowB")
	if st = scan(); st.Current != "/lib/ShowB" || st.Done != 106 {
		t.Fatalf("after second dirVisited: Current/Done = %q/%d, want /lib/ShowB/106", st.Current, st.Done)
	}
	if wp.dirs != 2 || wp.files != 106 {
		t.Fatalf("tracker counters: dirs/files = %d/%d, want 2/106", wp.dirs, wp.files)
	}

	// Skipped-folder errors aggregate into ONE walk-warning
	// finding, pathed at the first root that had one.
	if f := wp.warningFinding(); f != nil {
		t.Fatalf("warningFinding before any error = %+v, want nil", f)
	}
	wp.dirError("/lib")
	wp.dirError("/lib")
	f := wp.warningFinding()
	if f == nil {
		t.Fatal("warningFinding after errors = nil, want the walk-warning finding")
	}
	if f.Type != "walk-warning" || f.Path != "/lib" ||
		f.Evidence != "2 folders could not be read during the walk (skipped)" {
		t.Fatalf("walk-warning finding = %+v, want type walk-warning at /lib with the exact evidence text", f)
	}
}

func TestScanWalkProgressIsObservable(t *testing.T) {
	root := t.TempDir()
	// 120 folders x 100 empty .mkv files: the walk takes long
	// enough (and publishes on every one of the 120 directory
	// changes) for the observer below to catch it mid-flight.
	for d := 0; d < 120; d++ {
		dir := filepath.Join(root, fmt.Sprintf("show%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 100; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("ep%03d.mkv", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var sawProgress, sawCurrent atomic.Bool
	stopObs := make(chan struct{})
	obsDone := make(chan struct{})
	go func() {
		defer close(obsDone)
		for {
			select {
			case <-stopObs:
				return
			default:
			}
			st := svc.State().Scan
			if st.Phase == "walking" {
				if st.Done > 0 {
					sawProgress.Store(true)
				}
				if st.Current != "" {
					sawCurrent.Store(true)
				}
			}
			runtime.Gosched()
		}
	}()

	if _, err := svc.StartScan([]string{root}); err != nil {
		t.Fatal(err)
	}
	// Wait for the walk to end (phase moves on) — bounded.
	deadline := time.Now().Add(60 * time.Second)
	for svc.State().Scan.Phase == "walking" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	svc.CancelScan()
	// Wait for the scan goroutine to fully stop.
	deadline = time.Now().Add(30 * time.Second)
	for svc.State().Scan.Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stopObs)
	<-obsDone

	if svc.State().Scan.Running {
		t.Fatal("scan did not stop after cancel")
	}
	if !sawProgress.Load() {
		t.Fatal("never observed a walking state with Done > 0 — walk progress is not being published")
	}
	if !sawCurrent.Load() {
		t.Fatal("never observed a walking state with Current set — the current folder is not being published")
	}
}
