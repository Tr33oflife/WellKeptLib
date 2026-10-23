package core

// duplicates_signature_test.go — v0.3.7 Fix 2: the grouper
// guard for yearless title-only movie items (defense in depth
// behind the parser's series-folder absolute-numbering fix).
// The v0.3.5 guard covers EPISODE items with no episode number;
// it cannot see a pack that the parser mislabels as yearless
// MOVIES with one shared title — the Vanitas pack grouped as
// "movie|vanitas no karte part|0|" because every filename's
// distinguishing number sat past the title cut. So a yearless
// movie's identity now also carries the filename's standalone-
// number signature (see numberSignature): items whose stems
// disagree on their bare numbers can never share a group, while
// true yearless duplicates — same title, same numbers, only the
// release tags differ — still can.

import (
	"fmt"
	"testing"
)

// TestDuplicatesYearlessNumberSignatureVanitas: the twelve
// Vanitas items AS THE OLD PARSER PRODUCED THEM (movie kind,
// shared title, year 0 — constructed directly, Fix 1 bypassed)
// must produce twelve distinct signatures and ZERO groups, even
// with near-identical sizes and durations squarely inside the
// confirmation tolerance.
func TestDuplicatesYearlessNumberSignatureVanitas(t *testing.T) {
	var items []*Item
	sigs := map[string]int{}
	for ep := 1; ep <= 12; ep++ {
		path := "/TV Shows/Vanitas no Karte/" + vanitasName(ep)
		it := mkItem(path, "Vanitas no Karte Part", 0, "", int64(480_000_000+ep*3_000_000), 1080, fmt.Sprintf("h%d", ep))
		it.Duration = 1421 + float64(ep%3)*4 // 23:41–23:49, like the live group
		items = append(items, it)
		sig := numberSignature(it)
		if sig == "" {
			t.Fatalf("ep %d: empty signature — the guard has nothing to separate on", ep)
		}
		sigs[sig] = ep
	}
	if len(sigs) != 12 {
		t.Fatalf("distinct signatures = %d, want 12 (%v)", len(sigs), sigs)
	}
	groups, _ := buildDuplicates(items, testCfg(), nil)
	if len(groups) != 0 {
		t.Fatalf("groups = %d, want 0 (first group: %+v)", len(groups), groups[0])
	}
}

// TestDuplicatesYearlessTrueDupesStillGroup: the positive
// control — two encodes of one yearless film, differing only in
// release tags, carry EQUAL (empty) signatures and still group.
func TestDuplicatesYearlessTrueDupesStillGroup(t *testing.T) {
	a := mkItem("/lib/Film.1080p.WEB-DL.x264.mkv", "Film", 0, "", 4_000_000_000, 1080, "h1")
	b := mkItem("/lib/Film.720p.WEB-DL.x264.mkv", "Film", 0, "", 1_500_000_000, 720, "h2")
	if sa, sb := numberSignature(a), numberSignature(b); sa != sb {
		t.Fatalf("signatures differ: %q vs %q — true yearless dupes must match", sa, sb)
	}
	groups, _ := buildDuplicates([]*Item{a, b}, testCfg(), nil)
	if len(groups) != 1 || len(groups[0].Copies) != 2 {
		t.Fatalf("groups = %+v, want exactly one 2-copy group", groups)
	}
}

// TestIdentityKeyYearUnaffected: items WITH a year keep the
// exact pre-v0.3.7 key shape — the signature segment exists
// only for yearless movies.
func TestIdentityKeyYearUnaffected(t *testing.T) {
	it := mkItem("/lib/Film (2020).mkv", "Film", 2020, "", 100, 1080, "h")
	if got, want := identityKey(it), "movie|film|2020|"; got != want {
		t.Fatalf("identityKey = %q, want %q", got, want)
	}
	if got, want := baseKey(it), "movie|film|2020"; got != want {
		t.Fatalf("baseKey = %q, want %q", got, want)
	}
}
