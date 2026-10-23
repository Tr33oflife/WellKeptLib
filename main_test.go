package main

// main_test.go — the -scan-once exit-code table (FEATURE-PLAN
// §7), pinned at the decision function: main() itself maps a
// data-dir lock refusal and a no-roots config to exit 2 before
// a scan exists (see runScanOnce), which the shipped-binary
// smoke exercises end to end.

import (
	"testing"

	"plexfilemanager/internal/core"
)

func TestExitCodeTable(t *testing.T) {
	clean := core.ScanSummary{}
	findings := core.ScanSummary{DuplicateReclaimable: 5}
	healthOnly := core.ScanSummary{HealthFindings: 2}
	apparentOnly := core.ScanSummary{DuplicateWasted: 9} // apparent waste does NOT gate
	cases := []struct {
		name string
		err  string
		sum  core.ScanSummary
		fail bool
		want int
	}{
		{"completed clean", "", clean, false, 0},
		{"completed clean, gate on", "", clean, true, 0},
		{"completed with findings, gate off", "", findings, false, 0},
		{"completed with reclaimable, gate on", "", findings, true, 3},
		{"completed with health findings, gate on", "", healthOnly, true, 3},
		{"apparent waste alone does not gate", "", apparentOnly, true, 0},
		{"scan failed", "boom", clean, false, 1},
		{"scan failed beats the gate", "boom", findings, true, 1},
	}
	for _, c := range cases {
		if got := exitCodeForScan(c.err, c.sum, c.fail); got != c.want {
			t.Errorf("%s: exitCodeForScan = %d, want %d", c.name, got, c.want)
		}
	}
}
