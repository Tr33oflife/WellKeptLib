package core

// parser_round2_test.go — v0.4.0: the eight fixes from
// NAMING-RESEARCH-2.md (TRaSH/Servarr naming round 2). Every
// acceptance filename below is the memo's, verbatim; the memo's
// measured "before" outputs are quoted in the comments so the
// change each test pins is auditable.

import (
	"reflect"
	"testing"
)

func wantParse(t *testing.T, path, root, kind, title string, year, season int, eps []int) ParsedName {
	t.Helper()
	p := parseName(path, root)
	if p.Kind != kind {
		t.Errorf("%s: kind = %q, want %q (parsed %+v)", path, p.Kind, kind, p)
	}
	if p.Title != title {
		t.Errorf("%s: title = %q, want %q", path, p.Title, title)
	}
	if p.Year != year {
		t.Errorf("%s: year = %d, want %d", path, p.Year, year)
	}
	if p.Season != season {
		t.Errorf("%s: season = %d, want %d", path, p.Season, season)
	}
	if !reflect.DeepEqual(p.Episodes, eps) {
		t.Errorf("%s: episodes = %v, want %v", path, p.Episodes, eps)
	}
	return p
}

// --- Fix 1 (§1): series year from the episode filename. Before:
// all of these parsed as episodes with year 0, so Organize built
// yearless series folders for TRaSH/Servarr-standard names. ---

func TestParserRound2SeriesYearFromFilename(t *testing.T) {
	wantParse(t,
		"/lib/The Series Title! (2010) - S01E01 - Episode Title 1 [AMZN WEBDL-1080p Proper][DV HDR10][DTS 5.1][x264]-RlsGrp.mkv",
		"/lib", "episode", "The Series Title!", 2010, 1, []int{1})
	wantParse(t,
		"/lib/The.Series.Title's!.2010.S01E01.Episode.Title.1.ATVP.WEBDL-2160p.HDR10Plus.h265-RlsGrp.mkv",
		"/lib", "episode", "The Series Title's!", 2010, 1, []int{1})
	wantParse(t,
		"/lib/Series (2010) 1x01 Episode Title.mkv",
		"/lib", "episode", "Series", 2010, 1, []int{1})
	wantParse(t,
		"/lib/Series.2010.1x01.Episode.Title.mkv",
		"/lib", "episode", "Series", 2010, 1, []int{1})
	// The anime absolute path: the paren year before the number
	// is the series' own tag (memo §5 case, year 0 before).
	wantParse(t,
		"/lib/Series x Title/[KaiDubs] Series x Title (2011) - 142 [1080p][Opus 2.0][ByScorpio][945D4552].mkv",
		"/lib", "episode", "Series x Title", 2011, 1, []int{142})
	// A folder year still wins over the filename year.
	wantParse(t,
		"/lib/Series (1999)/Season 01/Series (2010) - S01E01.mkv",
		"/lib", "episode", "Series", 1999, 1, []int{1})
}

// --- Fix 2 (§2): multi-episode Extend + Duplicate styles.
// Before: "S01E01-02-03" → [1 2] (episode 3 lost);
// "S01E01.S01E02" → [1] (episode 2 lost). ---

func TestParserRound2MultiEpisodeExtendDuplicate(t *testing.T) {
	wantParse(t, "/lib/Series - S01E01-02-03.mkv", "/lib",
		"episode", "Series", 0, 1, []int{1, 2, 3})
	wantParse(t, "/lib/Series - S01E01.S01E02 - Episode Title [1080p].mkv", "/lib",
		"episode", "Series", 0, 1, []int{1, 2})
	// The four styles that already worked must not move.
	wantParse(t, "/lib/Series - S01E01E02E03.mkv", "/lib",
		"episode", "Series", 0, 1, []int{1, 2, 3})
	wantParse(t, "/lib/Series - S01E01-E02-E03.mkv", "/lib",
		"episode", "Series", 0, 1, []int{1, 2, 3})
	wantParse(t, "/lib/Series - S01E01-03.mkv", "/lib",
		"episode", "Series", 0, 1, []int{1, 2, 3})
	wantParse(t, "/lib/Series - S01E01-E03 - Episode Title [HQ].mkv", "/lib",
		"episode", "Series", 0, 1, []int{1, 2, 3})
}

// --- Fix 3 (§3): daily harm guard. Before: root dailies became
// movies keyed on the date's year (two episodes in one year
// shared one identity); a season-folder daily invented an
// episode from the date's day token. After: kind "unknown",
// no episodes, no movie identity. ---

func TestParserRound2DailyGuard(t *testing.T) {
	p := wantParse(t,
		"/lib/The Series Title! (2010) - 2013-10-30 - Episode Title 1 [AMZN WEBDL-1080p Proper][x264]-RlsGrp.mkv",
		"/lib", "unknown", "The Series Title!", 0, 0, nil)
	_ = p
	wantParse(t,
		"/lib/Some.Daily.Show.2021.03.04.1080p.HDTV.x264-DARKSPORT.mkv",
		"/lib", "unknown", "Some Daily Show", 0, 0, nil)
	wantParse(t,
		"/lib/Some.Daily.Show.2021.05.06.1080p.HDTV.x264-GRP.mkv",
		"/lib", "unknown", "Some Daily Show", 0, 0, nil)
	// Servarr's space-separated {Air Date} form.
	wantParse(t,
		"/lib/Daily Show - 2021 03 04 - Episode Title.mkv",
		"/lib", "unknown", "Daily Show", 0, 0, nil)
	// The season-folder shape no longer invents episode 3. The
	// year here is 2010 — the SERIES year supplied by the folder
	// — which is legitimate metadata; what the guard forbids is
	// the DATE's year (2021) becoming an identity, and kind
	// "unknown" has no identity in the first place.
	wantParse(t,
		"/lib/Daily Show (2010)/Season 01/Daily Show - 2021-03-04 - Episode Title.mkv",
		"/lib", "unknown", "Daily Show", 2010, 0, nil)
	// An INVALID date is not a date: today's behaviour stands
	// (movie, year 2021 — memo §3 acceptance).
	wantParse(t,
		"/lib/Some.Show.2021.13.40.1080p.HDTV.x264-GRP.mkv",
		"/lib", "movie", "Some Show", 2021, 0, nil)
	// Two guarded dailies can never form a duplicate group.
	for _, path := range []string{
		"/lib/Some.Daily.Show.2021.03.04.1080p.HDTV.x264-DARKSPORT.mkv",
		"/lib/Some.Daily.Show.2021.05.06.1080p.HDTV.x264-GRP.mkv",
	} {
		p := parseName(path, "/lib")
		it := &Item{Path: path, Kind: p.Kind, Title: p.Title, NormTitle: p.NormTitle, Year: p.Year}
		if eligibleForDuplicates(it) {
			t.Errorf("%s: a guarded daily must never be group-eligible", path)
		}
	}
}

// --- Fix 4 (§6): bracketed anime episode number. Before:
// "Series/[Group] Series [12].mkv" → yearless movie "Series",
// and [12]/[13] shared one identity. ---

func TestParserRound2BracketedAnimeNumber(t *testing.T) {
	wantParse(t, "/lib/Series/[Group] Series [12].mkv", "/lib",
		"episode", "Series", 0, 1, []int{12})
	wantParse(t, "/lib/Series/[Group] Series [01].mkv", "/lib",
		"episode", "Series", 0, 1, []int{1})
	wantParse(t, "/lib/Series/[Group] Series [001].mkv", "/lib",
		"episode", "Series", 0, 1, []int{1})
	// The two files the old parse collapsed into one identity
	// now carry distinct episode identities.
	a := parseName("/lib/Series/[Group] Series [12].mkv", "/lib")
	b := parseName("/lib/Series/[Group] Series [13].mkv", "/lib")
	if a.NormTitle == b.NormTitle && reflect.DeepEqual(a.Episodes, b.Episodes) {
		t.Errorf("[12] and [13] still share one identity: %+v vs %+v", a, b)
	}
	// A year bracket is furniture, not an episode number: the
	// file stays a movie (no series folder claim on a number).
	p := parseName("/lib/Series/[Group] Series [2010].mkv", "/lib")
	if p.Kind == "episode" {
		t.Errorf("[2010] bracket parsed as episode: %+v", p)
	}
}

// --- Fix 5 (§8): glued vN versions. Before: "01v2" killed the
// absolute parse (movie), "SP01v2" defeated the specials marker
// (episode with no number). ---

func TestParserRound2GluedVersions(t *testing.T) {
	wantParse(t,
		"/lib/Toradora!/[TaigaSubs]_Toradora!_(2008)_-_01v2_-_Tiger_and_Dragon_[1080p_H.264_FLAC][BAD7A16A].mkv",
		"/lib", "episode", "Toradora!", 2008, 1, []int{1})
	wantParse(t,
		"/lib/Series/[Group] Series - 01v2 [BAD7A16A].mkv",
		"/lib", "episode", "Series", 0, 1, []int{1})
	wantParse(t,
		"/lib/Series/Season 01/[Group] Series - 01v2 [BAD7A16A].mkv",
		"/lib", "episode", "Series", 0, 1, []int{1})
	wantParse(t,
		"/lib/Series/Specials/[Group] Series - SP01v2.mkv",
		"/lib", "episode", "Series", 0, 0, []int{1})
	// Bracketed number with a glued version (fixes 4 + 5).
	wantParse(t,
		"/lib/Series/[Group] Series [12v2].mkv",
		"/lib", "episode", "Series", 0, 1, []int{12})
}

// --- Fix 6 (§9): NCOP/NCED are extras. Before: movies titled
// "Series NCOP" / "Series NCED01". OP/ED/OVA/OAD stay deferred —
// deliberately not asserted here. ---

func TestParserRound2NCOPNCEDExtras(t *testing.T) {
	for _, path := range []string{
		"/lib/[Group] Series - NCOP.mkv",
		"/lib/[Group] Series - NCED.mkv",
		"/lib/[Group] Series - NCOP01.mkv",
		"/lib/Series/[Group] Series - NCED01.mkv",
	} {
		p := parseName(path, "/lib")
		if p.Kind != "extra" {
			t.Errorf("%s: kind = %q, want extra (parsed %+v)", path, p.Kind, p)
		}
		if p.Title != "Series" {
			t.Errorf("%s: title = %q, want %q (token removed)", path, p.Title, "Series")
		}
	}
}

// --- Fix 7 (§11): bracketed edition phrases. Before: edition ""
// for every bracketed form; the paren form already worked. ---

func TestParserRound2BracketedEditions(t *testing.T) {
	cases := []struct{ path, edition string }{
		{"/lib/The Movie Title (2010) [Director's Cut].mkv", "Directors Cut"},
		{"/lib/The Movie Title [Director's Cut] (2010).mkv", "Directors Cut"},
		{"/lib/The Movie Title (2010) [Extended].mkv", "Extended"},
		{"/lib/The Movie Title (2010) [Unrated].mkv", "Unrated"},
		// Already-working forms stay put.
		{"/lib/The Movie Title (2010) (Director's Cut).mkv", "Directors Cut"},
		{"/lib/The Movie Title (2010).mkv", ""},
	}
	for _, c := range cases {
		p := parseName(c.path, "/lib")
		if p.Edition != c.edition {
			t.Errorf("%s: edition = %q, want %q", c.path, p.Edition, c.edition)
		}
		if p.Kind != "movie" || p.Title != "The Movie Title" || p.Year != 2010 {
			t.Errorf("%s: parsed %+v, want movie The Movie Title (2010)", c.path, p)
		}
	}
}

// --- Fix 8 (§12): the release-group tag guard. Before:
// "…Remux-2160p" carried tag "group:2160p" and "…Blu-ray"
// carried "group:ray". ---

func TestParserRound2GroupTagGuard(t *testing.T) {
	for _, path := range []string{
		"/lib/The Movie Title (2010) BluRay Remux-2160p.mkv",
		"/lib/The Movie Title (2010) Blu-ray.mkv",
	} {
		p := parseName(path, "/lib")
		for _, tag := range p.Tags {
			if len(tag) >= 6 && tag[:6] == "group:" {
				t.Errorf("%s: bogus group tag %q (tags %v)", path, tag, p.Tags)
			}
		}
	}
	// A real trailing group still records (memo §1 standard name).
	p := parseName("/lib/The.Movie.Title.2010.MA.WEBDL-2160p.TrueHD.Atmos.7.1.DV.HDR10Plus.h265-RlsGrp.mkv", "/lib")
	found := false
	for _, tag := range p.Tags {
		if tag == "group:rlsgrp" {
			found = true
		}
	}
	if !found {
		t.Errorf("real group tag lost: tags = %v", p.Tags)
	}
}
