package nfo

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Match the scanner's terminal CD part convention. Do not strip release marks
// or other suffixes that are not part of a recognized stacked movie.
var cdPartSuffix = regexp.MustCompile(`(?i)^(.*?)[ ._-]+CD([1-9][0-9]*)$`)

// SidecarPath returns the default NFO path for a source. A stacked movie shares
// one NFO without the CD suffix; an independent source keeps its complete name.
// Callers should retain an explicitly registered NFO path for legacy files.
func SidecarPath(source string, stacked bool) string {
	base := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	if stacked {
		if match := cdPartSuffix.FindStringSubmatch(base); match != nil {
			base = match[1]
		}
	}
	return filepath.Join(filepath.Dir(source), base+".nfo")
}
