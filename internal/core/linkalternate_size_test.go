package core

// linkalternate_size_test.go — v1.3.4: LinkAlternate carries
// the walk-measured size of its own file (additive payload
// field) so the link-plan UI can flag copies that measure
// 0 bytes with its 0-BYTE badge. The field is always
// serialized — 0 is a measurement here, not an absence —
// and these tests pin both the struct value and the JSON
// payload shape the page consumes.

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// alternatePayloadSize marshals one alternate the way the
// plan endpoint does and reads the "size" key back out of
// the JSON object, reporting whether the key was present.
func alternatePayloadSize(t *testing.T, alt LinkAlternate) (float64, bool) {
	t.Helper()
	raw, err := json.Marshal(alt)
	if err != nil {
		t.Fatalf("marshal alternate: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal alternate payload: %v", err)
	}
	v, ok := m["size"]
	if !ok {
		return 0, false
	}
	f, isNum := v.(float64)
	if !isNum {
		t.Fatalf("payload size is %T, want a number", v)
	}
	return f, true
}

func TestLinkAlternateSizeReachesPayload(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src, tgt := filepath.Join(base, "source"), filepath.Join(base, "target")

	// A duplicate pair with known, distinct contents: the
	// 1080p copy wins the filename ranking, the 720p copy is
	// the alternate whose size must reach the payload.
	alt := filepath.Join(src, "duplicates", "Film (2020) 720p.mkv")
	writeOrgFile(t, alt, "720p-copy") // 9 bytes
	writeOrgFile(t, filepath.Join(src, "duplicates", "Film (2020) 1080p.mkv"),
		"1080p-copy-with-more-bytes")

	plan := mustLinkPlan(t, src, tgt)
	if len(plan.Collisions) != 1 || len(plan.Collisions[0].Alternates) != 1 {
		t.Fatalf("collisions = %+v, want exactly one group with one alternate", plan.Collisions)
	}
	got := plan.Collisions[0].Alternates[0]
	if got.Source != alt {
		t.Fatalf("alternate = %s, want %s", got.Source, alt)
	}
	if got.Size != 9 {
		t.Errorf("alternate.Size = %d, want 9 (the alternate file's measured size)", got.Size)
	}
	if v, present := alternatePayloadSize(t, got); !present || v != 9 {
		t.Errorf("payload size = %v (present %v), want 9 present", v, present)
	}
}

func TestLinkAlternateZeroSizeReachesPayload(t *testing.T) {
	requireIdentity(t)
	base := t.TempDir()
	src, tgt := filepath.Join(base, "source"), filepath.Join(base, "target")

	// The owner's case: one copy measures 0 bytes. It is
	// still planned as the alternate, and the payload must
	// carry "size":0 explicitly — the UI flags a measured 0,
	// which only works if 0 is serialized, not omitted.
	alt := filepath.Join(src, "duplicates", "Film (2020) 720p.mkv")
	writeOrgFile(t, alt, "")
	writeOrgFile(t, filepath.Join(src, "duplicates", "Film (2020) 1080p.mkv"),
		"1080p-copy-with-more-bytes")

	plan := mustLinkPlan(t, src, tgt)
	if len(plan.Collisions) != 1 || len(plan.Collisions[0].Alternates) != 1 {
		t.Fatalf("collisions = %+v, want exactly one group with one alternate", plan.Collisions)
	}
	got := plan.Collisions[0].Alternates[0]
	if got.Source != alt {
		t.Fatalf("alternate = %s, want %s", got.Source, alt)
	}
	if got.Size != 0 {
		t.Errorf("alternate.Size = %d, want 0", got.Size)
	}
	if v, present := alternatePayloadSize(t, got); !present || v != 0 {
		t.Errorf("payload size = %v (present %v), want 0 present (measured 0 must serialize)", v, present)
	}
}
