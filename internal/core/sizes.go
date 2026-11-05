package core

// sizes.go — Part B of CATALOG-SIZES-PLEXDELETE-SPEC (v1.5.0):
// the size roll-ups behind the Library tab's Sizes view.
//
// This is a PROJECTION over the current scan results and the
// current thresholds — it stores nothing and changes no
// classification. buildSeries (service.go) already aggregates
// per-series totals for the Library header block; this file
// extends the same aggregation down to seasons and episodes
// and adds the large flags (§B.3): a season at or over its
// threshold carries its own flag, a show row carries only the
// COUNT of its large seasons, and file flags are computed here
// (and in the catalog) from the live config, never stored.

import (
	"sort"
)

// SizeEpisode is one episode file inside a season roll-up:
// the file facts the Sizes view lists, size-sorted.
type SizeEpisode struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Episodes  []int  `json:"episodes"`
	Large     bool   `json:"large"`
}

// SizeSeason is one season of one show: total bytes, episode-
// file count, largest episode, and its own large flag.
type SizeSeason struct {
	Season       int           `json:"season"`
	TotalBytes   int64         `json:"total_bytes"`
	EpisodeFiles int           `json:"episode_files"`
	LargestBytes int64         `json:"largest_episode_bytes"`
	Large        bool          `json:"large"`
	Episodes     []SizeEpisode `json:"episodes"`
}

// SizeShow is one show's roll-up: total bytes (episode files
// only — extras never fold into show totals, the same rule
// the catalog keeps), season count, episode-file count,
// largest single episode, average episode size, and the
// COUNT of its large seasons (§B.3: counts, not nested
// badges, on the show row).
type SizeShow struct {
	Title        string       `json:"title"`
	Year         int          `json:"year,omitempty"`
	TotalBytes   int64        `json:"total_bytes"`
	SeasonCount  int          `json:"season_count"`
	EpisodeFiles int          `json:"episode_files"`
	LargestBytes int64        `json:"largest_episode_bytes"`
	LargestPath  string       `json:"largest_episode_path,omitempty"`
	AvgBytes     int64        `json:"avg_episode_bytes"`
	LargeSeasons int          `json:"large_seasons"`
	Seasons      []SizeSeason `json:"seasons"`
}

// SizeMovie is one movie file's row facts for the Sizes view.
// The view links/filters to the Library's file list rather
// than duplicating it (§B.1); these rows carry the counts
// (how many movies, how many large) that strip needs.
type SizeMovie struct {
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Large     bool   `json:"large"`
}

// SizesReport is the whole Sizes view payload: shows with
// their seasons and episodes, the movie strip facts, and the
// thresholds the flags were computed from (so the view can
// label them and the document is self-describing).
type SizesReport struct {
	Shows      []SizeShow      `json:"shows"`
	Movies     []SizeMovie     `json:"movies"`
	Thresholds LargeThresholds `json:"thresholds"`
}

// LargeThresholds is the live large-file threshold set,
// carried by the catalog summary and the Sizes report so
// every flagged surface names the numbers it used.
type LargeThresholds struct {
	MovieBytes   int64 `json:"movie_bytes"`
	SeasonBytes  int64 `json:"season_bytes"`
	EpisodeBytes int64 `json:"episode_bytes"`
}

// thresholds snapshots the config's three thresholds.
func (c *Config) thresholds() LargeThresholds {
	return LargeThresholds{
		MovieBytes:   c.LargeMovieBytes,
		SeasonBytes:  c.LargeSeasonBytes,
		EpisodeBytes: c.LargeEpisodeBytes,
	}
}

// largeSeason reports whether a season total flags under the
// config: at-or-over, and a 0 threshold disables the flag.
func (c *Config) largeSeason(total int64) bool {
	return c.LargeSeasonBytes > 0 && total >= c.LargeSeasonBytes
}

// buildSizes projects items into the Sizes report under cfg's
// thresholds. Pure: same inputs, same report — the sorting is
// total (size descending, title ascending as the tiebreak at
// every level; episodes inside a season sort by size
// descending per §B.1, path as the final tiebreak).
func buildSizes(items []*Item, cfg *Config) SizesReport {
	type seasonAcc struct {
		season *SizeSeason
	}
	type showAcc struct {
		show    *SizeShow
		seasons map[int]*seasonAcc
	}
	byKey := map[string]*showAcc{}
	movies := []SizeMovie{}
	for _, it := range items {
		switch it.Kind {
		case "movie":
			movies = append(movies, SizeMovie{
				Title: it.Title, Year: it.Year, Path: it.Path,
				SizeBytes: it.Size, Large: cfg.largeForSize("movie", it.Size),
			})
		case "episode":
			if len(it.Episodes) == 0 {
				continue // unnumbered: not an episode row anywhere (catalog lists it as unidentified)
			}
			key := it.NormTitle
			acc, ok := byKey[key]
			if !ok {
				acc = &showAcc{
					show:    &SizeShow{Title: it.Title, Year: it.Year, Seasons: []SizeSeason{}},
					seasons: map[int]*seasonAcc{},
				}
				byKey[key] = acc
			}
			sa, ok := acc.seasons[it.Season]
			if !ok {
				sa = &seasonAcc{season: &SizeSeason{Season: it.Season, Episodes: []SizeEpisode{}}}
				acc.seasons[it.Season] = sa
			}
			sa.season.TotalBytes += it.Size
			sa.season.EpisodeFiles++
			if it.Size > sa.season.LargestBytes {
				sa.season.LargestBytes = it.Size
			}
			eps := append([]int{}, it.Episodes...)
			sa.season.Episodes = append(sa.season.Episodes, SizeEpisode{
				Path: it.Path, SizeBytes: it.Size, Episodes: eps,
				Large: cfg.largeForSize("episode", it.Size),
			})
			sh := acc.show
			sh.TotalBytes += it.Size
			sh.EpisodeFiles++
			if it.Size > sh.LargestBytes {
				sh.LargestBytes = it.Size
				sh.LargestPath = it.Path
			}
		}
	}
	shows := []SizeShow{}
	for _, acc := range byKey {
		sh := acc.show
		seasons := []SizeSeason{}
		for _, sa := range acc.seasons {
			s := sa.season
			s.Large = cfg.largeSeason(s.TotalBytes)
			if s.Large {
				sh.LargeSeasons++
			}
			sort.Slice(s.Episodes, func(i, j int) bool {
				if s.Episodes[i].SizeBytes != s.Episodes[j].SizeBytes {
					return s.Episodes[i].SizeBytes > s.Episodes[j].SizeBytes
				}
				return s.Episodes[i].Path < s.Episodes[j].Path
			})
			seasons = append(seasons, *s)
		}
		// Seasons sort by size descending inside the show
		// (§B.1: every roll-up level sorts by size, title —
		// here the season number — as the tiebreak).
		sort.Slice(seasons, func(i, j int) bool {
			if seasons[i].TotalBytes != seasons[j].TotalBytes {
				return seasons[i].TotalBytes > seasons[j].TotalBytes
			}
			return seasons[i].Season < seasons[j].Season
		})
		sh.Seasons = seasons
		sh.SeasonCount = len(seasons)
		if sh.EpisodeFiles > 0 {
			sh.AvgBytes = sh.TotalBytes / int64(sh.EpisodeFiles)
		}
		shows = append(shows, *sh)
	}
	sort.Slice(shows, func(i, j int) bool {
		if shows[i].TotalBytes != shows[j].TotalBytes {
			return shows[i].TotalBytes > shows[j].TotalBytes
		}
		return shows[i].Title < shows[j].Title
	})
	sort.Slice(movies, func(i, j int) bool {
		if movies[i].SizeBytes != movies[j].SizeBytes {
			return movies[i].SizeBytes > movies[j].SizeBytes
		}
		return movies[i].Title < movies[j].Title
	})
	if movies == nil {
		movies = []SizeMovie{}
	}
	return SizesReport{Shows: shows, Movies: movies, Thresholds: cfg.thresholds()}
}

// Sizes returns the Sizes view projection over the current
// results under the current thresholds.
func (s *Service) Sizes() SizesReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return buildSizes(s.results.Items, s.cfg)
}
