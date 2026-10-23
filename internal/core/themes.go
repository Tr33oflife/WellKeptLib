package core

// themes.go — the UI theme registry (v1.2.0). The page's whole
// chrome is painted from CSS custom properties; a theme is one
// named palette scoped under [data-theme="<id>"] in the page.
// The server only stores the chosen id (Config.Theme) — the
// palettes themselves live in the page, so adding a theme is a
// page change, never a data migration.
//
// The default is "midnight": the palette the app has shipped
// since the v0.4.0 design sweep, defined by the :root variables
// themselves. An absent or unrecognized stored value falls back
// to it WITHOUT error — a config written by a build that does
// not know a newer theme id must still open cleanly here.

// themeIDs is the closed set the page ships palettes for, in
// selector order.
var themeIDs = []string{"midnight", "daylight", "ocean", "ember", "forest", "plum"}

// DefaultTheme is the fallback for absent/unknown values.
const DefaultTheme = "midnight"

// validTheme reports whether id is a shipped theme.
func validTheme(id string) bool {
	for _, t := range themeIDs {
		if t == id {
			return true
		}
	}
	return false
}

// normalizeTheme maps any stored/submitted value to a shipped
// theme id: a known id passes through, anything else (empty,
// unknown, a newer build's theme) becomes the default.
func normalizeTheme(id string) string {
	if validTheme(id) {
		return id
	}
	return DefaultTheme
}
