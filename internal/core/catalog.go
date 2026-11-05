package core

// catalog.go — Part A of CATALOG-SIZES-PLEXDELETE-SPEC
// (v1.5.0): the catalog export, schema wellkeptlib.catalog
// v1 — one JSON document listing every scanned show, season,
// episode and movie, built to be read by an AI (or a person)
// as the stable reference for "what is in this library".
//
// It is a PROJECTION over the current scan results — the
// same items the Library shows, re-shaped. Nothing is
// scanned, stored or reclassified: an item's catalog section
// is decided by the identity the parser already gave it,
// with the planner's own reason strings (organize.go's
// buildVideoTarget) for the files that have no identity.
// Extras and unidentified files get their own sections and
// are never silently dropped (the 1.3.0 export's honesty
// discipline).

import (
	"path/filepath"
	"sort"
	"time"
)

// CatalogSchema / CatalogSchemaVersion name the document.
const (
	CatalogSchema        = "wellkeptlib.catalog"
	CatalogSchemaVersion = 1
)

// CatalogFile is the per-file fact set shared by the
// catalog's movie, extra and unidentified entries (episode
// entries embed the same facts with their episode numbers).
type CatalogFile struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	DurationSec int64  `json:"duration_sec"`
	Resolution  string `json:"resolution"`
	Codec       string `json:"codec"`
	Bitrate     int64  `json:"bitrate"`
	HDR         bool   `json:"hdr"`
	Large       bool   `json:"large"`
}

// CatalogEpisode is one episode FILE in a season (a
// multi-episode file appears once, with its full list —
// the CSV's convention).
type CatalogEpisode struct {
	Episodes []int `json:"episodes"`
	CatalogFile
}

// CatalogSeason is one season: its total is exactly the sum
// of its episode files.
type CatalogSeason struct {
	Season     int              `json:"season"`
	TotalBytes int64            `json:"total_bytes"`
	Episodes   []CatalogEpisode `json:"episodes"`
}

// CatalogShow is one show: its total is the sum of its
// seasons. The show's extras are NOT folded in — they sit
// in the document's extras section (§A.1: totals must say
// what they count).
type CatalogShow struct {
	Title        string          `json:"title"`
	Year         int             `json:"year"`
	TotalBytes   int64           `json:"total_bytes"`
	EpisodeFiles int             `json:"episode_files"`
	Seasons      []CatalogSeason `json:"seasons"`
}

// CatalogMovie is one movie file.
type CatalogMovie struct {
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Edition string `json:"edition"`
	CatalogFile
}

// CatalogExtra is one extra (trailer, featurette, …). When
// exactly one scanned work shares its folder, OwnerTitle
// names that work — the extra's bytes still stay out of
// the work's totals.
type CatalogExtra struct {
	Title      string `json:"title"`
	OwnerTitle string `json:"owner_title,omitempty"`
	CatalogFile
}

// CatalogUnidentified is one file the parser could not
// identify, with the planner's reason for it.
type CatalogUnidentified struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
	CatalogFile
}

// CatalogSummary is the document's headline counts. The
// counts are of ENTRIES: episodes counts episode files (a
// multi-episode file is one entry). TotalBytes is every
// scanned file's bytes, all sections together. The
// thresholds the large flags were computed with ride along
// so the document is self-describing (§A.1).
type CatalogSummary struct {
	Shows        int             `json:"shows"`
	Seasons      int             `json:"seasons"`
	Episodes     int             `json:"episodes"`
	Movies       int             `json:"movies"`
	Extras       int             `json:"extras"`
	Unidentified int             `json:"unidentified"`
	TotalBytes   int64           `json:"total_bytes"`
	Thresholds   LargeThresholds `json:"large_thresholds"`
}

// Catalog is the whole document.
type Catalog struct {
	Schema        string                `json:"schema"`
	SchemaVersion int                   `json:"schema_version"`
	AppVersion    string                `json:"app_version"`
	ExportedAt    string                `json:"exported_at"`
	Roots         []string              `json:"roots"`
	Summary       CatalogSummary        `json:"summary"`
	Shows         []CatalogShow         `json:"shows"`
	Movies        []CatalogMovie        `json:"movies"`
	Extras        []CatalogExtra        `json:"extras"`
	Unidentified  []CatalogUnidentified `json:"unidentified"`
	Notes         []string              `json:"notes"`
}

// catalogFileFacts projects one item into the shared fact set
// under cfg's thresholds.
func catalogFileFacts(it *Item, cfg *Config) CatalogFile {
	return CatalogFile{
		Path:        it.Path,
		SizeBytes:   it.Size,
		DurationSec: int64(it.Duration),
		Resolution:  it.ResolutionLabel,
		Codec:       it.Codec,
		Bitrate:     it.Bitrate,
		HDR:         it.HDR,
		Large:       cfg.largeForSize(it.Kind, it.Size),
	}
}

// BuildCatalog projects items into the catalog document.
// Pure and deterministic: shows sort by title, seasons and
// episodes ascending, movies by title, extras and
// unidentified by path.
func BuildCatalog(items []*Item, roots []string, cfg *Config, appVersion string, now time.Time) *Catalog {
	doc := &Catalog{
		Schema:        CatalogSchema,
		SchemaVersion: CatalogSchemaVersion,
		AppVersion:    appVersion,
		ExportedAt:    now.UTC().Format(time.RFC3339),
		Roots:         append([]string{}, roots...),
		Shows:         []CatalogShow{},
		Movies:        []CatalogMovie{},
		Extras:        []CatalogExtra{},
		Unidentified:  []CatalogUnidentified{},
		Notes: []string{
			"identity is parsed from filenames by WellKeptLib, not read from Plex metadata — a title, year or episode number here is what the file's name says, which may differ from what a metadata agent matched it to.",
			"show and season totals count episode files only: a show's extras are listed in the extras section and their bytes are not folded into the show's total.",
			"total_bytes counts every scanned file in every section of this document, identified or not.",
			"large flags are computed from the thresholds recorded in summary.large_thresholds (bytes; a threshold of 0 disables that flag) at export time — they are not stored scan facts.",
			"unidentified lists every scanned file the parser gave no movie/episode identity, each with the reason the organize planner reports for it.",
		},
	}
	if doc.Roots == nil {
		doc.Roots = []string{}
	}

	// Owner evidence for extras: the set of distinct works
	// (movie/episode identities) sharing each folder.
	type ownerInfo struct {
		norms map[string]string // norm title -> display title
	}
	ownersByDir := map[string]*ownerInfo{}
	for _, it := range items {
		if it.Kind != "movie" && it.Kind != "episode" {
			continue
		}
		dir := filepath.Dir(it.Path)
		oi, ok := ownersByDir[dir]
		if !ok {
			oi = &ownerInfo{norms: map[string]string{}}
			ownersByDir[dir] = oi
		}
		oi.norms[it.NormTitle] = it.Title
	}

	type seasonAcc struct {
		season *CatalogSeason
	}
	type showAcc struct {
		show    *CatalogShow
		seasons map[int]*seasonAcc
		norm    string
	}
	byKey := map[string]*showAcc{}
	var totalBytes int64
	for _, it := range items {
		totalBytes += it.Size
		switch it.Kind {
		case "episode":
			if len(it.Episodes) == 0 {
				doc.Unidentified = append(doc.Unidentified, CatalogUnidentified{
					Title: it.Title, Reason: "no episode number found",
					CatalogFile: catalogFileFacts(it, cfg),
				})
				continue
			}
			acc, ok := byKey[it.NormTitle]
			if !ok {
				acc = &showAcc{
					show:    &CatalogShow{Title: it.Title, Seasons: []CatalogSeason{}},
					seasons: map[int]*seasonAcc{},
					norm:    it.NormTitle,
				}
				byKey[it.NormTitle] = acc
			}
			if acc.show.Year == 0 && it.Year != 0 {
				acc.show.Year = it.Year
			}
			sa, ok := acc.seasons[it.Season]
			if !ok {
				sa = &seasonAcc{season: &CatalogSeason{Season: it.Season, Episodes: []CatalogEpisode{}}}
				acc.seasons[it.Season] = sa
			}
			eps := append([]int{}, it.Episodes...)
			sa.season.Episodes = append(sa.season.Episodes, CatalogEpisode{
				Episodes:    eps,
				CatalogFile: catalogFileFacts(it, cfg),
			})
			sa.season.TotalBytes += it.Size
			acc.show.TotalBytes += it.Size
			acc.show.EpisodeFiles++
		case "movie":
			doc.Movies = append(doc.Movies, CatalogMovie{
				Title: it.Title, Year: it.Year, Edition: it.Edition,
				CatalogFile: catalogFileFacts(it, cfg),
			})
		case "extra":
			ex := CatalogExtra{
				Title:       it.Title,
				CatalogFile: catalogFileFacts(it, cfg),
			}
			if oi, ok := ownersByDir[filepath.Dir(it.Path)]; ok && len(oi.norms) == 1 {
				for _, title := range oi.norms {
					ex.OwnerTitle = title
				}
			}
			doc.Extras = append(doc.Extras, ex)
		default:
			doc.Unidentified = append(doc.Unidentified, CatalogUnidentified{
				Title: it.Title, Reason: "couldn't identify this file",
				CatalogFile: catalogFileFacts(it, cfg),
			})
		}
	}
	accs := make([]*showAcc, 0, len(byKey))
	for _, acc := range byKey {
		seasons := []CatalogSeason{}
		for _, sa := range acc.seasons {
			s := sa.season
			sort.Slice(s.Episodes, func(i, j int) bool {
				a, b := s.Episodes[i], s.Episodes[j]
				if a.Episodes[0] != b.Episodes[0] {
					return a.Episodes[0] < b.Episodes[0]
				}
				return a.Path < b.Path
			})
			seasons = append(seasons, *s)
		}
		sort.Slice(seasons, func(i, j int) bool { return seasons[i].Season < seasons[j].Season })
		acc.show.Seasons = seasons
		accs = append(accs, acc)
	}
	sort.Slice(accs, func(i, j int) bool {
		if accs[i].norm != accs[j].norm {
			return accs[i].norm < accs[j].norm
		}
		return accs[i].show.Title < accs[j].show.Title
	})
	seasonCount, episodeCount := 0, 0
	for _, acc := range accs {
		doc.Shows = append(doc.Shows, *acc.show)
		seasonCount += len(acc.show.Seasons)
		episodeCount += acc.show.EpisodeFiles
	}
	sort.Slice(doc.Movies, func(i, j int) bool {
		if doc.Movies[i].Title != doc.Movies[j].Title {
			return doc.Movies[i].Title < doc.Movies[j].Title
		}
		return doc.Movies[i].Path < doc.Movies[j].Path
	})
	sort.Slice(doc.Extras, func(i, j int) bool { return doc.Extras[i].Path < doc.Extras[j].Path })
	sort.Slice(doc.Unidentified, func(i, j int) bool { return doc.Unidentified[i].Path < doc.Unidentified[j].Path })
	doc.Summary = CatalogSummary{
		Shows:        len(doc.Shows),
		Seasons:      seasonCount,
		Episodes:     episodeCount,
		Movies:       len(doc.Movies),
		Extras:       len(doc.Extras),
		Unidentified: len(doc.Unidentified),
		TotalBytes:   totalBytes,
		Thresholds:   cfg.thresholds(),
	}
	return doc
}

// ExportCatalog returns the catalog document for the current
// scan results, plus its download filename. Read-only; no
// scan is triggered — an unscanned library exports as a
// valid, empty document.
func (s *Service) ExportCatalog() (*Catalog, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := BuildCatalog(s.results.Items, s.cfg.Roots, s.cfg, Version, time.Now().UTC())
	name := "wellkeptlib-catalog-" + time.Now().UTC().Format("20060102-150405") + ".json"
	return doc, name
}
