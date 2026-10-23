package core

// parser_test.go — release-name parser cases from the SPEC examples
// plus folder-context and edition cases.

import "testing"

// mustParse is a tiny helper that parses and fails the test on a kind
// mismatch, to keep the table compact.
func mustParse(t *testing.T, path, root, wantKind string) ParsedName {
	t.Helper()
	p := parseName(path, root)
	if p.Kind != wantKind {
		t.Fatalf("parseName(%q): kind = %q, want %q (parsed %+v)", path, p.Kind, wantKind, p)
	}
	return p
}

// TestParserMovieParenYear covers "Movie Name (2020) 1080p BluRay x264-GROUP.mkv".
func TestParserMovieParenYear(t *testing.T) {
	p := mustParse(t, "/lib/Movie Name (2020) 1080p BluRay x264-GROUP.mkv", "/lib", "movie")
	if p.NormTitle != "movie name" || p.Year != 2020 {
		t.Fatalf("title/year = %q/%d", p.NormTitle, p.Year)
	}
	if p.Resolution != "1080p" {
		t.Fatalf("resolution = %q", p.Resolution)
	}
	foundGroup := false
	for _, tag := range p.Tags {
		if tag == "group:group" {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatalf("expected group:group tag, got %v", p.Tags)
	}
}

// TestParserMovieDotted covers "Movie.Name.2020.720p.WEB-DL.x265.mkv".
func TestParserMovieDotted(t *testing.T) {
	p := mustParse(t, "/lib/Movie.Name.2020.720p.WEB-DL.x265.mkv", "/lib", "movie")
	if p.NormTitle != "movie name" || p.Year != 2020 || p.Resolution != "720p" {
		t.Fatalf("got title=%q year=%d res=%q", p.NormTitle, p.Year, p.Resolution)
	}
}

// TestParserEpisodeSxE covers "Show Name S01E02 ...".
func TestParserEpisodeSxE(t *testing.T) {
	p := mustParse(t, "/lib/Show Name S01E02 1080p WEB-DL x264.mkv", "/lib", "episode")
	if p.NormTitle != "show name" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 2 {
		t.Fatalf("got %+v", p)
	}
}

// TestParserEpisodeNxN covers the "1x02" pattern.
func TestParserEpisodeNxN(t *testing.T) {
	p := mustParse(t, "/lib/Show.Name.1x02.HDTV.x264.mkv", "/lib", "episode")
	if p.NormTitle != "show name" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 2 {
		t.Fatalf("got %+v", p)
	}
}

// TestParserFolderContext covers Show/Season 01/<bare episode file>.
func TestParserFolderContext(t *testing.T) {
	p := mustParse(t, "/lib/Great Show/Season 01/Great Show E05.mkv", "/lib", "episode")
	if p.NormTitle != "great show" || p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 5 {
		t.Fatalf("got %+v", p)
	}
}

// TestParserEdition covers edition detection + separation in keys.
func TestParserEdition(t *testing.T) {
	p := mustParse(t, "/lib/Some Film (2019) Extended 1080p BluRay x264.mkv", "/lib", "movie")
	if p.Edition != "Extended" {
		t.Fatalf("edition = %q", p.Edition)
	}
	p2 := mustParse(t, "/lib/Some Film (2019) 1080p BluRay x264.mkv", "/lib", "movie")
	if p2.Edition != "" {
		t.Fatalf("plain copy edition = %q", p2.Edition)
	}
	p3 := mustParse(t, "/lib/Other.Film.2021.Directors.Cut.720p.WEB-DL.mkv", "/lib", "movie")
	if p3.Edition != "Directors Cut" {
		t.Fatalf("dotted edition = %q", p3.Edition)
	}
}

// TestParserFolderEpisodeWordForm is the v0.3.5 live bug, verbatim:
// a season pack named "... - Episode 39 ..." under an S01 folder
// parsed with NO episode number (only a bare E-token was tried),
// and the grouper then collapsed the season into one false group.
func TestParserFolderEpisodeWordForm(t *testing.T) {
	p := mustParse(t, "/lib/TV Shows/Eureka Seven/S01/[bonkai77] Eureka Seven - Episode 39 [BD 1080p Dual Audio x265 10bit].mkv", "/lib", "episode")
	if p.Title != "Eureka Seven" || p.NormTitle != "eureka seven" {
		t.Fatalf("title = %q / %q", p.Title, p.NormTitle)
	}
	if p.Season != 1 || len(p.Episodes) != 1 || p.Episodes[0] != 39 {
		t.Fatalf("season/episodes = %d/%v, want 1/[39]", p.Season, p.Episodes)
	}
}

// TestParserFolderEpisodeVariants covers the remaining v0.3.5
// folder-season patterns in precedence order: abbreviation forms
// and the bare-number fallback.
func TestParserFolderEpisodeVariants(t *testing.T) {
	cases := []struct {
		path        string
		wantSeason  int
		wantEpisode int
	}{
		{"/lib/Great Show/Season 01/Great Show - Episode 01.mkv", 1, 1}, // word form
		{"/lib/Great Show/Season 01/Great Show EP 07.mkv", 1, 7},        // abbreviation, spaced
		{"/lib/Great Show/Season 01/Great Show ep7.mkv", 1, 7},          // abbreviation, joined
		{"/lib/Great Show/S02/39.mkv", 2, 39},                           // bare number as whole stem
		{"/lib/Series Name/Season 2/Series Name - 12.mkv", 2, 12},       // bare number token
	}
	for _, c := range cases {
		p := mustParse(t, c.path, "/lib", "episode")
		if p.Season != c.wantSeason || len(p.Episodes) != 1 || p.Episodes[0] != c.wantEpisode {
			t.Errorf("parseName(%q): season/episodes = %d/%v, want %d/[%d]", c.path, p.Season, p.Episodes, c.wantSeason, c.wantEpisode)
		}
	}
}

// TestParserFolderPatternsStayInFolderContext: the word/abbrev/
// bare-number forms must NOT leak into standalone movie parsing —
// a film with "Episode" in its title stays a movie — and a bare
// resolution value inside a season folder is not an episode.
func TestParserFolderPatternsStayInFolderContext(t *testing.T) {
	p := mustParse(t, "/lib/Some Film Episode (2020).mkv", "/lib", "movie")
	if p.Year != 2020 || len(p.Episodes) != 0 {
		t.Fatalf("movie with Episode in title: year=%d episodes=%v", p.Year, p.Episodes)
	}
	p2 := mustParse(t, "/lib/Great Show/Season 01/Great Show 1080.mkv", "/lib", "episode")
	if len(p2.Episodes) != 0 {
		t.Fatalf("bare resolution token became episodes: %v", p2.Episodes)
	}
}

// TestParserMultiEpisode covers S01E01-E02 => multi-episode special.
func TestParserMultiEpisode(t *testing.T) {
	p := mustParse(t, "/lib/Show S01E01-E02.mkv", "/lib", "episode")
	if p.Special != "multi-episode" || len(p.Episodes) != 2 {
		t.Fatalf("got %+v", p)
	}
}
