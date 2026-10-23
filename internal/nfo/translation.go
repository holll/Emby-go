package nfo

import (
	"context"
	"errors"
	"os"
	"strings"
)

// ErrTranslationConflict means a source, destination or field lock changed
// while translation was in flight. The caller must obtain a new snapshot.
var ErrTranslationConflict = errors.New("nfo: translation source, destination or locks changed")

// TranslationSnapshot is an immutable optimistic-write token. Only direct movie
// children are considered; extension elements and nested names are never sources.
type TranslationSnapshot struct {
	OriginalTitle   string
	Title           string
	SortTitle       string
	Plot            string
	Number          string
	TitleLocked     bool
	PlotLocked      bool
	SortTitleLocked bool
	path            string
	fields          map[string]string
}

func translationBlocks(text, tag string) string {
	var b strings.Builder
	for _, e := range elementsNamed(text, tag) {
		b.WriteString(text[e.start:e.end])
		b.WriteByte(0)
	}
	return b.String()
}

// ReadTranslationSnapshot holds the normal NFO path lock only for the read, not
// during any network operation. It never creates a missing sidecar.
func ReadTranslationSnapshot(path string) (TranslationSnapshot, error) {
	defer lockNFOPath(path)()
	raw, err := os.ReadFile(path)
	if err != nil {
		return TranslationSnapshot{}, err
	}
	text := string(raw)
	if _, err := findClosingMovie(text); err != nil {
		return TranslationSnapshot{}, err
	}
	locks := lockedTags(text, false)
	snap := TranslationSnapshot{OriginalTitle: ScrapedTagValue(text, "originaltitle"), Title: ScrapedTagValue(text, "title"), SortTitle: ScrapedTagValue(text, "sorttitle"), Plot: ScrapedTagValue(text, "plot"), Number: ScrapedTagValue(text, "num"), TitleLocked: locks["title"], PlotLocked: locks["plot"], SortTitleLocked: locks["sorttitle"], path: path, fields: make(map[string]string)}
	for _, tag := range []string{"originaltitle", "title", "sorttitle", "plot", "num", "lockedfields", "lockdata"} {
		snap.fields[tag] = translationBlocks(text, tag)
	}
	return snap, nil
}

// CommitTranslation rechecks selected sources, destinations and locks under the
// same lock as every other NFO writer. Nonempty values are explicit updates;
// originaltitle, unselected fields and unknown XML are preserved byte-for-byte.
// Context is checked again immediately before the atomic write.
func CommitTranslation(ctx context.Context, path string, snap TranslationSnapshot, title, plot string) (bool, error) {
	defer lockNFOPath(path)()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if snap.path != path || snap.fields == nil {
		return false, ErrTranslationConflict
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(raw)
	if _, err := findClosingMovie(text); err != nil {
		return false, err
	}
	tags := []string{"lockedfields", "lockdata"}
	if title != "" {
		tags = append(tags, "originaltitle", "title", "num")
		if !snap.SortTitleLocked {
			tags = append(tags, "sorttitle")
		}
	}
	if plot != "" {
		tags = append(tags, "plot")
	}
	for _, tag := range tags {
		if translationBlocks(text, tag) != snap.fields[tag] {
			return false, ErrTranslationConflict
		}
	}
	locks := lockedTags(text, false)
	indent := childIndent(text)
	if title != "" {
		if locks["title"] {
			return false, ErrTranslationConflict
		}
		if ScrapedTagValue(text, "title") != title {
			text = upsertScalar(text, "title", title, indent)
		}
		if !locks["sorttitle"] && ScrapedTagValue(text, "sorttitle") != title {
			text = upsertScalar(text, "sorttitle", title, indent)
		}
	}
	if plot != "" {
		if locks["plot"] {
			return false, ErrTranslationConflict
		}
		if ScrapedTagValue(text, "plot") != plot {
			text = upsertScalar(text, "plot", plot, indent)
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if text == string(raw) {
		return false, nil
	}
	if err := writeFileAtomic(path, text); err != nil {
		return false, err
	}
	return true, nil
}
