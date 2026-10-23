package core

// subtitles.go — subtitle text extraction and the language audit.
//
// Declared language comes from the filename (external) or the stream
// tag (embedded); actual language comes from DetectLanguage over the
// subtitle text itself. A finding is produced for mismatch,
// missing/und declared, uncertain (too little or too ambiguous), and
// image-based streams that cannot be verified at all.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	reHTMLTag = regexp.MustCompile(`<[^>]*>`)
	reASSTag  = regexp.MustCompile(`\{[^}]*\}`)
	reTimeArr = regexp.MustCompile(`\d{1,2}:\d{2}:\d{2}[.,]\d{1,3}\s*-->`)
	reVTTTime = regexp.MustCompile(`\d{1,2}:\d{2}([:.]\d{1,3})?\s*-->`)
	reASSLine = regexp.MustCompile(`^\d+:\d{2}:\d{2}\.\d{2},`)
)

// declaredLangFromFilename extracts the declared ISO 639-1 language
// from a subtitle filename: language token just before the extension,
// skipping flag tokens (forced/sdh/cc/hi/default/full).
// Returns "" when no language token is present.
func declaredLangFromFilename(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	toks := strings.FieldsFunc(stem, func(r rune) bool {
		return r == '.' || r == '_' || r == ' ' || r == '-'
	})
	flags := map[string]bool{"forced": true, "sdh": true, "cc": true, "hi": true, "default": true, "full": true, "sign": true, "signs": true, "songs": true}
	for i := len(toks) - 1; i >= 0; i-- {
		t := lowerASCII(toks[i])
		if flags[t] {
			continue
		}
		if code := langCodeNormal(t); code != "" {
			return code
		}
		// Only look at the last meaningful token position: if the token
		// right before the extension isn't a language (and isn't a flag
		// we skipped), there is no declared language.
		if i == len(toks)-1 {
			return ""
		}
	}
	// Loop ended while still skipping flags right after a language
	// token is impossible; check the token before skipped flags once.
	for i := len(toks) - 1; i >= 0; i-- {
		if code := langCodeNormal(lowerASCII(toks[i])); code != "" {
			return code
		}
		if !flags[lowerASCII(toks[i])] {
			break
		}
	}
	return ""
}

// parseSubtitleText extracts plain dialogue lines from SRT, VTT,
// ASS/SSA or MicroDVD (.sub) content. Also returns the cue count.
func parseSubtitleText(data []byte) (lines []string, cues int) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	rawLines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	isASS := strings.Contains(text, "[Events]") || strings.Contains(text, "Dialogue:")
	for _, ln := range rawLines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if isASS {
			if strings.HasPrefix(ln, "Dialogue:") {
				rest := strings.TrimPrefix(ln, "Dialogue:")
				parts := strings.SplitN(rest, ",", 10)
				if len(parts) == 10 {
					cues++
					if t := cleanCueText(parts[9]); t != "" {
						lines = append(lines, t)
					}
				}
			}
			continue
		}
		if strings.HasPrefix(ln, "WEBVTT") || strings.HasPrefix(ln, "NOTE") {
			continue
		}
		if reTimeArr.MatchString(ln) || reVTTTime.MatchString(ln) {
			cues++
			continue
		}
		if _, err := strconv.Atoi(ln); err == nil {
			continue // SRT cue index
		}
		if strings.HasPrefix(ln, "{") && strings.Contains(ln, "}{") {
			// MicroDVD: {start}{end}text
			cues++
			t := regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(ln, "")
			if t = cleanCueText(strings.ReplaceAll(t, "|", " ")); t != "" {
				lines = append(lines, t)
			}
			continue
		}
		if t := cleanCueText(ln); t != "" {
			lines = append(lines, t)
		}
	}
	if cues == 0 {
		cues = len(lines) // formats without explicit timing still count lines
	}
	return lines, cues
}

// cleanCueText strips markup from one dialogue line.
func cleanCueText(s string) string {
	s = reASSTag.ReplaceAllString(s, "")
	s = reHTMLTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\\N", " ")
	s = strings.ReplaceAll(s, "\\n", " ")
	return strings.TrimSpace(s)
}

// sampleLines returns up to n representative lines (first non-trivial).
func sampleLines(lines []string, n int) []string {
	var out []string
	for _, l := range lines {
		if len(strings.Fields(l)) >= 2 {
			out = append(out, l)
		}
		if len(out) >= n {
			break
		}
	}
	return out
}

// auditText runs detection over dialogue lines and builds a finding
// when the audit rules say to flag. Returns nil for a clean pass.
func auditText(path, source string, streamIdx int, declared string, lines []string, threshold float64) *SubtitleFinding {
	joined := strings.Join(lines, " ")
	det := DetectLanguage(joined)
	f := &SubtitleFinding{
		Path: path, Source: source, StreamIdx: streamIdx,
		Declared: declared, Detected: det.Code,
		Confidence: det.Confidence, Words: det.Words,
		Sample: sampleLines(lines, 3),
	}
	switch {
	case det.Words < 10 || det.Code == "":
		f.Status = "uncertain"
		f.Note = "too little subtitle text to detect a language reliably"
		return f
	case declared == "" || declared == "und":
		if det.Confidence >= threshold {
			f.Status = "missing-language"
			f.Note = "no language declared, but text detects as " + det.Code
			return f
		}
		f.Status = "uncertain"
		f.Note = "no language declared and detection is not confident"
		return f
	case declared == det.Code && det.Confidence >= threshold:
		return nil // clean pass
	case declared != det.Code && det.Confidence >= threshold:
		f.Status = "mismatch"
		f.Note = "declared " + declared + " but text detects as " + det.Code
		return f
	default:
		f.Status = "uncertain"
		f.Note = "detection confidence below threshold; declared language not confirmed"
		return f
	}
}

// auditExternalSubtitle audits one external subtitle file. The cue
// and line counts are returned so the health check can flag empty
// subtitle files (v0.4.1: the line count rides along from THIS
// capped parse — the scanner used to re-read the whole file,
// uncapped, only to recount lines it had already computed here).
// Line/cue ZERO-NESS is identical under the capped parse (a file
// whose first 64KB holds no dialogue lines is an empty subtitle by
// the health check's Lines==0 / Cues==0 test either way), and that
// zero-ness is all buildHealth consumes.
func auditExternalSubtitle(path string, threshold float64) (*SubtitleFinding, int, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	// Cap the read for detection: the first ~64KB of cues is plenty
	// and keeps huge subtitle files cheap on a seedbox.
	if len(data) > 65536 {
		data = data[:65536]
	}
	lines, cues := parseSubtitleText(data)
	if len(lines) == 0 {
		return nil, cues, 0, nil // health reports the empty file
	}
	return auditText(path, "external", -1, declaredLangFromFilename(path), lines, threshold), cues, len(lines), nil
}
