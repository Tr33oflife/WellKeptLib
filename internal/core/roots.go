package core

// roots.go — scan-root input hygiene (v0.1.1).
//
// WHY THIS FILE EXISTS: v0.1.0 silently scanned zero files when a root
// didn't resolve exactly as typed (found by a Windows live test). The
// classic trigger is Windows 11 "Copy as path", which pastes a path
// wrapped in double quotes ("D:\Movies"); other triggers are a leading
// "~", environment variables, or a plain typo. The walk swallowed the
// error and the scan "succeeded" with 0 media and no message anywhere.
// Roots are now cleaned, expanded, and validated BEFORE a scan starts,
// and every problem is reported back to the UI — never swallowed.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// winEnvRe matches Windows-style %VAR% environment references.
var winEnvRe = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)

// cleanRootInput normalizes one raw root line as typed/pasted by the
// user: trims whitespace, strips surrounding quotes, expands %VAR% and
// $VAR environment references, expands a leading "~" to the user's
// home directory, and cleans the result. It does not check existence —
// that is validateRoots' job.
func cleanRootInput(raw string) string {
	s := strings.TrimSpace(raw)
	// Strip surrounding quote pairs (Windows copy-as-path, shell pastes).
	for len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	if s == "" {
		return ""
	}
	// %VAR% (Windows) — only replaced when the variable actually exists,
	// so a literal % in a folder name survives untouched.
	s = winEnvRe.ReplaceAllStringFunc(s, func(m string) string {
		name := winEnvRe.FindStringSubmatch(m)[1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
	// $VAR / ${VAR} (POSIX style, os.ExpandEnv leaves unknown vars empty,
	// which is the expected shell behaviour).
	s = os.ExpandEnv(s)
	// Leading "~" means the user's home directory.
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if s == "~" {
				s = home
			} else {
				s = filepath.Join(home, s[2:])
			}
		}
	}
	return filepath.Clean(s)
}

// validateRoots cleans and stats every candidate root. It returns the
// usable roots (existing directories, deduplicated, order preserved)
// and one human-readable problem string per rejected root. Callers must
// surface the problems — a rejected root is information, not silence.
func validateRoots(candidates []string) (valid []string, problems []string) {
	seen := map[string]bool{}
	for _, raw := range candidates {
		p := cleanRootInput(raw)
		if p == "" {
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		info, err := os.Stat(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("root not found: %s", p))
			continue
		}
		if !info.IsDir() {
			problems = append(problems, fmt.Sprintf("root is not a folder: %s", p))
			continue
		}
		valid = append(valid, p)
	}
	return valid, problems
}
