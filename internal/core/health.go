package core

// health.go — the empty/corrupt (fast-probe) health check.
//
// Every finding carries its evidence (the probe error, the numbers, the
// filename pattern). Nothing here asserts "corrupt" from a name alone:
// the incomplete check reports the *name pattern* as its type, and the
// truncation heuristic reports its measurements.

import (
	"fmt"
	"sort"
	"strings"
)

// subFileInfo is what the scanner learned about one external subtitle
// file while auditing it (reused here so files aren't re-read).
type subFileInfo struct {
	Path     string
	Size     int64
	Cues     int
	Lines    int
	ParseErr string
}

// buildHealth assembles health findings from scanned items, subtitle
// info, incomplete-download files, and empty folders.
func buildHealth(items []*Item, subs []subFileInfo, incomplete []HealthFinding, emptyFolders []string, cfg *Config) []HealthFinding {
	out := []HealthFinding{}
	seen := map[string]bool{}
	add := func(f HealthFinding) {
		k := f.Path + "|" + f.Type
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, f)
	}
	for _, it := range items {
		if it.Size == 0 {
			add(HealthFinding{Path: it.Path, Type: "empty", Size: 0, Evidence: "file size is 0 bytes"})
			continue
		}
		if !it.ProbeOK {
			// One finding per broken file: the probe failure is the
			// stronger signal (a 44-byte text file is not also "tiny").
			add(HealthFinding{Path: it.Path, Type: "probe-failure", Size: it.Size,
				Evidence: "ffprobe failed: " + it.ProbeError})
			continue
		}
		if it.Size < cfg.TinyFileFloorBytes {
			add(HealthFinding{Path: it.Path, Type: "tiny", Size: it.Size,
				Evidence: fmt.Sprintf("size %d bytes is below the floor of %d bytes for a video file", it.Size, cfg.TinyFileFloorBytes)})
		}
		if !it.HasVideo {
			add(HealthFinding{Path: it.Path, Type: "no-video-stream", Size: it.Size,
				Evidence: "ffprobe parsed the container but found no video stream"})
		}
		if it.Duration <= 0 {
			add(HealthFinding{Path: it.Path, Type: "no-duration", Size: it.Size,
				Evidence: "ffprobe reports no duration for this file"})
		} else if it.Bitrate > 0 {
			// Truncation heuristic: the container's declared bitrate
			// implies a much larger file than the one on disk.
			implied := float64(it.Size) * 8 / it.Duration
			if implied < 0.4*float64(it.Bitrate) {
				add(HealthFinding{Path: it.Path, Type: "suspect-truncated", Size: it.Size,
					Evidence: fmt.Sprintf("declared bitrate %.0f bps over %.0fs implies ~%.0f MB, actual size %.1f MB (implied %.0f bps)",
						float64(it.Bitrate), it.Duration, float64(it.Bitrate)*it.Duration/8/1e6, float64(it.Size)/1e6, implied)})
			}
		}
	}
	for _, sf := range subs {
		if sf.ParseErr != "" {
			add(HealthFinding{Path: sf.Path, Type: "subtitle-unparseable", Size: sf.Size, Evidence: "reading subtitle failed: " + sf.ParseErr})
		} else if sf.Cues == 0 || sf.Lines == 0 {
			add(HealthFinding{Path: sf.Path, Type: "subtitle-empty", Size: sf.Size,
				Evidence: fmt.Sprintf("subtitle file contains no dialogue cues (size %d bytes)", sf.Size)})
		}
	}
	for _, f := range incomplete {
		add(f)
	}
	for _, d := range emptyFolders {
		add(HealthFinding{Path: d, Type: "empty-folder", Evidence: "folder contains no files at all"})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// incompletePatterns are filename endings of in-progress downloads.
var incompleteSuffixes = []string{".part", ".crdownload", ".partial", ".download", ".opdownload", ".!qb"}

// isIncompleteName reports whether a filename looks like an unfinished
// download artefact.
func isIncompleteName(name string) bool {
	low := strings.ToLower(name)
	for _, suf := range incompleteSuffixes {
		if strings.HasSuffix(low, suf) {
			return true
		}
	}
	return strings.Contains(low, ".!qb")
}

// samplePattern reports whether a path looks like a sample / trailer /
// extras artefact (excluded from duplicate groups, still inventoried).
func samplePattern(path string) bool {
	low := strings.ToLower(path)
	for _, pat := range []string{"-sample", ".sample", " sample", "trailer", "featurette", "/extras/", "\\extras\\", "behind the scenes"} {
		if strings.Contains(low, pat) {
			return true
		}
	}
	return false
}
