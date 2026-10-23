package core

// report.go — the library report card + exports (v1.0.0,
// FEATURE-PLAN §4).
//
// One aggregation, three renderings. buildReportLocked takes
// every figure from the SAME derivations the tabs use —
// currentSummaryLocked for the headline counts (which itself
// derives duplicates / coverage / gaps), Duplicates()'
// buildDuplicates for group detail, filteredSubtitlesLocked /
// filteredHealthLocked for findings, buildEpisodeGaps for gap
// rows, buildSeries for series sizes — so the report can never
// disagree with the app: there is exactly one copy of the math
// (a test asserts report numbers == API numbers field by field).
//
// Renderings: the Report view (JSON via GET /api/report), a CSV
// export (GET /api/report.csv — sectioned: one file, "section"
// as the first column, so a spreadsheet user can filter), and a
// standalone HTML report (GET /api/report.html — fully
// self-contained: inline CSS, no scripts, no external assets of
// any kind, per the icon/CDN law).

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html/template"
	"sort"
	"strconv"
	"time"
)

// ReportFile is one row of the biggest-files table.
type ReportFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ReportSeries is one row of the biggest-series table.
type ReportSeries struct {
	Title string `json:"title"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// ReportCard is the complete aggregated report.
type ReportCard struct {
	Version     string `json:"version"`
	GeneratedAt string `json:"generated_at"`
	ScannedAt   string `json:"scanned_at,omitempty"`

	// Totals.
	MediaCount        int   `json:"media_count"`
	SubtitleFileCount int   `json:"subtitle_file_count"`
	TotalBytes        int64 `json:"total_bytes"`

	// Duplicates — apparent vs reclaimable (v1.0.0 honest pair).
	DuplicateGroups      int   `json:"duplicate_groups"`
	DuplicateWasted      int64 `json:"duplicate_wasted_bytes"`
	DuplicateReclaimable int64 `json:"duplicate_reclaimable_bytes"`
	// DuplicateUnknownGroups counts groups where at least one
	// copy's storage identity was unknown, so their reclaimable
	// figure is the fallback arithmetic (labelled in the UI).
	DuplicateUnknownGroups int `json:"duplicate_unknown_identity_groups"`

	// Health by kind (HealthFinding.Type strings).
	HealthTotal  int            `json:"health_total"`
	HealthByKind map[string]int `json:"health_by_kind"`

	// Subtitles: findings by detector status + coverage classes.
	SubtitleFindings     int            `json:"subtitle_findings"`
	SubtitleByStatus     map[string]int `json:"subtitle_by_status"`
	CoverageCovered      int            `json:"coverage_covered"`
	CoverageNoWanted     int            `json:"coverage_no_wanted"`
	CoverageNone         int            `json:"coverage_no_subtitles"`
	CoverageUnverifiable int            `json:"coverage_unverifiable"`

	// Episode gaps + folder hygiene.
	GapRows         int `json:"gap_rows"`
	GapEpisodes     int `json:"gap_episodes"`
	HygieneFindings int `json:"hygiene_findings"`

	BiggestSeries []ReportSeries `json:"biggest_series"`
	BiggestFiles  []ReportFile   `json:"biggest_files"`
}

// buildReportLocked is the single aggregation. Caller holds s.mu.
func (s *Service) buildReportLocked() ReportCard {
	sum := s.currentSummaryLocked()
	r := ReportCard{
		Version:              Version,
		GeneratedAt:          time.Now().UTC().Format(time.RFC3339),
		ScannedAt:            s.results.ScannedAt,
		MediaCount:           sum.MediaCount,
		SubtitleFileCount:    sum.SubtitleFileCount,
		TotalBytes:           sum.TotalBytes,
		DuplicateGroups:      sum.DuplicateGroups,
		DuplicateWasted:      sum.DuplicateWasted,
		DuplicateReclaimable: sum.DuplicateReclaimable,
		SubtitleFindings:     sum.SubtitleFindings,
		CoverageCovered:      sum.CoverageCovered,
		CoverageNoWanted:     sum.CoverageNoWanted,
		CoverageNone:         sum.CoverageNoSubtitles,
		CoverageUnverifiable: sum.CoverageUnverifiable,
		GapRows:              sum.GapCount,
		GapEpisodes:          sum.GapEpisodes,
		HealthByKind:         map[string]int{},
		SubtitleByStatus:     map[string]int{},
		BiggestSeries:        []ReportSeries{},
		BiggestFiles:         []ReportFile{},
	}
	groups, _ := buildDuplicates(s.results.Items, s.cfg, s.cfg.Exclusions)
	for _, g := range groups {
		if !g.HardlinkKnown {
			r.DuplicateUnknownGroups++
		}
	}
	for _, f := range s.filteredHealthLocked() {
		r.HealthByKind[f.Type]++
		r.HealthTotal++
		if f.Type == "empty-folder" || f.Type == "orphan-sidecar" {
			r.HygieneFindings++
		}
	}
	for _, f := range s.filteredSubtitlesLocked() {
		r.SubtitleByStatus[f.Status]++
	}
	// Top-5 series from the Size tab's own aggregation.
	series := buildSeries(s.results.Items)
	sort.Slice(series, func(i, j int) bool { return series[i].TotalBytes > series[j].TotalBytes })
	for i := 0; i < len(series) && i < 5; i++ {
		r.BiggestSeries = append(r.BiggestSeries, ReportSeries{
			Title: series[i].Series, Files: series[i].EpisodeFiles, Bytes: series[i].TotalBytes,
		})
	}
	// Top-10 files by size from the same item set the Size tab
	// sorts.
	items := append([]*Item{}, s.results.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Size > items[j].Size })
	for i := 0; i < len(items) && i < 10; i++ {
		if items[i] != nil {
			r.BiggestFiles = append(r.BiggestFiles, ReportFile{Path: items[i].Path, Size: items[i].Size})
		}
	}
	return r
}

// Report returns the aggregated card (GET /api/report).
func (s *Service) Report() ReportCard {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildReportLocked()
}

// i64 is the CSV number formatter.
func i64(n int64) string { return strconv.FormatInt(n, 10) }

// ReportCSV renders the card + per-finding tables as one
// sectioned CSV (GET /api/report.csv).
func (s *Service) ReportCSV() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.buildReportLocked()
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	row := func(cols ...string) {
		_ = w.Write(cols)
	}
	row("section", "metric", "value", "detail")
	row("summary", "version", r.Version, "")
	row("summary", "scanned_at", r.ScannedAt, "")
	row("summary", "media_count", i64(int64(r.MediaCount)), "")
	row("summary", "subtitle_file_count", i64(int64(r.SubtitleFileCount)), "")
	row("summary", "total_bytes", i64(r.TotalBytes), "")
	row("duplicates", "groups", i64(int64(r.DuplicateGroups)), "")
	row("duplicates", "apparent_wasted_bytes", i64(r.DuplicateWasted), "")
	row("duplicates", "reclaimable_bytes", i64(r.DuplicateReclaimable), "deleting down to one physical copy per group")
	row("duplicates", "unknown_identity_groups", i64(int64(r.DuplicateUnknownGroups)), "reclaimable for these groups is the per-file fallback figure")
	row("health", "total", i64(int64(r.HealthTotal)), "")
	for _, kind := range healthKindOrder(r.HealthByKind) {
		row("health", kind, i64(int64(r.HealthByKind[kind])), healthKindLabel(kind))
	}
	row("subtitles", "findings", i64(int64(r.SubtitleFindings)), "")
	for _, st := range sortedKeys(r.SubtitleByStatus) {
		row("subtitles", "status_"+st, i64(int64(r.SubtitleByStatus[st])), "")
	}
	row("coverage", "covered", i64(int64(r.CoverageCovered)), "")
	row("coverage", "has_subtitles_none_wanted", i64(int64(r.CoverageNoWanted)), "")
	row("coverage", "no_subtitles", i64(int64(r.CoverageNone)), "")
	row("coverage", "unverifiable", i64(int64(r.CoverageUnverifiable)), "")
	row("gaps", "rows", i64(int64(r.GapRows)), "")
	row("gaps", "missing_episodes", i64(int64(r.GapEpisodes)), "")

	// Per-finding tables.
	groups, _ := buildDuplicates(s.results.Items, s.cfg, s.cfg.Exclusions)
	for _, g := range groups {
		row("duplicate_group", g.Title, i64(g.ReclaimableBytes),
			fmt.Sprintf("year=%d kind=%s copies=%d physical=%d apparent_wasted=%d", g.Year, g.Kind, g.FileEntries, g.PhysicalCopies, g.WastedBytes))
		for _, c := range g.Copies {
			row("duplicate_copy", c.Path, i64(c.Size), fmt.Sprintf("keeper=%v hardlink_of_keeper=%v identity_known=%v", c.IsKeeper, c.HardlinkOfKeeper, c.IdentityKnown))
		}
	}
	for _, f := range s.filteredHealthLocked() {
		row("health_finding", f.Path, i64(f.Size), f.Type+": "+f.Evidence)
	}
	for _, f := range s.filteredSubtitlesLocked() {
		row("subtitle_finding", f.Path, "", fmt.Sprintf("status=%s declared=%s detected=%s", f.Status, f.Declared, f.Detected))
	}
	for _, g := range buildEpisodeGaps(s.results.Items) {
		missing := ""
		for i, ep := range g.Missing {
			if i > 0 {
				missing += ","
			}
			missing += strconv.Itoa(ep)
		}
		row("gap", fmt.Sprintf("%s S%02d", g.Series, g.Season), i64(int64(len(g.Missing))), "missing episodes: "+missing)
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// healthKindOrder returns the kinds present in a stable,
// human-sensible order (severity-ish first), then any extras
// alphabetically.
func healthKindOrder(m map[string]int) []string {
	order := []string{
		"empty", "tiny", "probe-failure", "no-video-stream",
		"no-duration", "suspect-truncated", "incomplete",
		"subtitle-empty", "subtitle-unparseable", "empty-folder",
		"orphan-sidecar", "walk-warning",
	}
	out := []string{}
	seen := map[string]bool{}
	for _, k := range order {
		if _, ok := m[k]; ok {
			out = append(out, k)
			seen[k] = true
		}
	}
	for _, k := range sortedKeys(m) {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// healthKindLabel is the plain label for a health kind.
func healthKindLabel(kind string) string {
	switch kind {
	case "empty":
		return "Empty file (0 bytes)"
	case "tiny":
		return "Tiny file (below the plausible floor)"
	case "probe-failure":
		return "Probe failure"
	case "no-video-stream":
		return "No video stream"
	case "no-duration":
		return "No duration"
	case "suspect-truncated":
		return "Suspect truncated"
	case "incomplete":
		return "Incomplete download"
	case "subtitle-empty":
		return "Subtitle file with no dialogue"
	case "subtitle-unparseable":
		return "Subtitle file unreadable"
	case "empty-folder":
		return "Empty folder"
	case "orphan-sidecar":
		return "Orphan sidecar/subtitle (no matching media)"
	case "walk-warning":
		return "Walk warning"
	}
	return kind
}

// sortedKeys is the small map-keys helper.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// reportHTMLTemplate is the standalone report: inline CSS only,
// no scripts, no external references (the icon/CDN law applied
// to exports — an emailed report must render identically
// offline).
var reportHTMLTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"bytes": humanBytes,
}).Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<title>WellKeptLib library report</title>
<style>
body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;margin:2rem auto;max-width:52rem;padding:0 1rem;color:#1c2430;background:#f6f8fa}
h1{font-size:1.5rem} h2{font-size:1.1rem;margin-top:1.8rem;border-bottom:2px solid #d6dde5;padding-bottom:.3rem}
table{border-collapse:collapse;width:100%;margin:.6rem 0;background:#fff}
th,td{border:1px solid #d6dde5;padding:.4rem .6rem;text-align:left;font-size:.92rem}
th{background:#eef2f6} td.n,th.n{text-align:right;font-variant-numeric:tabular-nums}
.meta{color:#5a6a7a;font-size:.9rem} .path{word-break:break-all;font-family:ui-monospace,Consolas,monospace;font-size:.83rem}
</style></head><body>
<h1>WellKeptLib library report</h1>
<p class="meta">Generated {{.GeneratedAt}} · library scanned {{if .ScannedAt}}{{.ScannedAt}}{{else}}(no scan yet){{end}} · app version {{.Version}}</p>
<h2>Totals</h2>
<table>
<tr><th>Media files</th><td class="n">{{.MediaCount}}</td><th>Subtitle files</th><td class="n">{{.SubtitleFileCount}}</td></tr>
<tr><th>Total size</th><td class="n">{{bytes .TotalBytes}}</td><th>Health findings</th><td class="n">{{.HealthTotal}}</td></tr>
</table>
<h2>Duplicates</h2>
<table>
<tr><th>Groups</th><td class="n">{{.DuplicateGroups}}</td></tr>
<tr><th>Apparent wasted</th><td class="n">{{bytes .DuplicateWasted}}</td></tr>
<tr><th>Actually reclaimable</th><td class="n">{{bytes .DuplicateReclaimable}}</td></tr>
{{if .DuplicateUnknownGroups}}<tr><th>Groups with unknown file identity</th><td class="n">{{.DuplicateUnknownGroups}} (their reclaimable figure is the per-file fallback)</td></tr>{{end}}
</table>
<h2>Subtitles</h2>
<table>
<tr><th>Findings</th><td class="n">{{.SubtitleFindings}}</td></tr>
{{range $k, $v := .SubtitleByStatus}}<tr><th>Status: {{$k}}</th><td class="n">{{$v}}</td></tr>{{end}}
<tr><th>Coverage — has a wanted language</th><td class="n">{{.CoverageCovered}}</td></tr>
<tr><th>Coverage — subtitles, none wanted</th><td class="n">{{.CoverageNoWanted}}</td></tr>
<tr><th>Coverage — no subtitles</th><td class="n">{{.CoverageNone}}</td></tr>
<tr><th>Coverage — unverifiable</th><td class="n">{{.CoverageUnverifiable}}</td></tr>
</table>
<h2>Episode gaps</h2>
<table><tr><th>Series/seasons with gaps</th><td class="n">{{.GapRows}}</td><th>Missing episodes</th><td class="n">{{.GapEpisodes}}</td></tr></table>
<h2>Biggest series</h2>
<table><tr><th>Series</th><th class="n">Files</th><th class="n">Size</th></tr>
{{range .BiggestSeries}}<tr><td>{{.Title}}</td><td class="n">{{.Files}}</td><td class="n">{{bytes .Bytes}}</td></tr>{{end}}
</table>
<h2>Biggest files</h2>
<table><tr><th>File</th><th class="n">Size</th></tr>
{{range .BiggestFiles}}<tr><td class="path">{{.Path}}</td><td class="n">{{bytes .Size}}</td></tr>{{end}}
</table>
<p class="meta">Produced by WellKeptLib {{.Version}}. Every figure is computed from the same scan results the app itself displays.</p>
</body></html>`))

// ReportHTML renders the standalone HTML report.
func (s *Service) ReportHTML() ([]byte, error) {
	s.mu.Lock()
	r := s.buildReportLocked()
	s.mu.Unlock()
	var buf bytes.Buffer
	if err := reportHTMLTemplate.Execute(&buf, r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
