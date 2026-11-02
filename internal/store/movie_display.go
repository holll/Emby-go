package store

import (
	"path"
	"regexp"
	"strings"
)

var displayCDSuffix = regexp.MustCompile(`(?i)[ ._-]+CD[1-9][0-9]*$`)

// CleanDisplayName hides terminal CD part markers in display text only.
// It must not be used for source paths, indexing, or playback identity.
func CleanDisplayName(name string) string {
	name = strings.TrimSpace(name)
	return strings.TrimSpace(displayCDSuffix.ReplaceAllString(name, ""))
}

// DisplayTitle returns a human-facing name without changing stored metadata.
// A lone CD1 is hidden even when the source has not formed a multi-part stack.
func (m Movie) DisplayTitle() string {
	for _, value := range []string{m.Title, m.Number} {
		if name := CleanDisplayName(value); name != "" {
			return name
		}
	}
	// Accept both filesystem separator styles without exposing parent directories.
	base := path.Base(strings.ReplaceAll(m.SourcePath, `\`, "/"))
	if base == "." || base == "/" {
		return ""
	}
	return CleanDisplayName(strings.TrimSuffix(base, path.Ext(base)))
}

// DisplaySortName uses the same display-only cleanup as the title.
func (m Movie) DisplaySortName() string {
	if name := CleanDisplayName(m.SortName); name != "" {
		return name
	}
	return m.DisplayTitle()
}
