package scraper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"emby-go/internal/metatube"
	"emby-go/internal/nfo"
	"emby-go/internal/store"
)

// TranslationOptions selects existing NFO fields independently of scrape flags.
type TranslationOptions struct {
	Title   bool
	Plot    bool
	Refresh bool
}

type TranslationResult struct {
	MovieID      int64  `json:"movie_id"`
	Changed      bool   `json:"changed"`
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason,omitempty"`
	Title        string `json:"title,omitempty"`
	RefreshError string `json:"refresh_error,omitempty"`
}

// TranslateExisting translates only the existing sidecar. It never searches
// MetaTube, downloads images, creates NFOs or records scrape results. Title's
// sole source is originaltitle; plot's sole source is the current plot.
func (s *Scraper) TranslateExisting(ctx context.Context, movie store.Movie, opts TranslationOptions) (TranslationResult, error) {
	result := TranslationResult{MovieID: movie.ID}
	skip := func(reason string) (TranslationResult, error) {
		result.Skipped = true
		result.Reason = reason
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !opts.Title && !opts.Plot {
		return skip("no fields selected")
	}
	path := strings.TrimSpace(movie.NFOPath)
	if path == "" {
		path = nfo.SidecarPath(movie.SourcePath, len(movie.AdditionalParts) > 0)
	}
	snap, err := nfo.ReadTranslationSnapshot(path)
	if errors.Is(err, os.ErrNotExist) {
		return skip("NFO does not exist")
	}
	if err != nil {
		return result, err
	}
	result.Title = snap.Title
	titleSelected := opts.Title && !snap.TitleLocked && strings.TrimSpace(snap.OriginalTitle) != ""
	plotSelected := opts.Plot && !snap.PlotLocked && strings.TrimSpace(snap.Plot) != ""
	if !titleSelected && !plotSelected {
		return skip("selected fields are locked or have no source text")
	}
	if !s.cfg.Translate.Enabled() {
		return result, errors.New("translation service is not configured")
	}
	texts := make([]string, 0, 2)
	if titleSelected {
		texts = append(texts, snap.OriginalTitle)
	}
	if plotSelected {
		texts = append(texts, snap.Plot)
	}
	response, err := s.tr.Translate(ctx, texts)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	title, plot := "", ""
	index := 0
	if titleSelected {
		// Use the NFO number, never a potentially stale index copy.
		number := dashedNumber(metatube.Normalize(snap.Number))
		title = joinNumberTitle(number, response.Texts[index])
		index++
	}
	if plotSelected {
		plot = response.Texts[index]
	}
	changed, err := nfo.CommitTranslation(ctx, path, snap, title, plot)
	if err != nil {
		return result, fmt.Errorf("commit translation: %w", err)
	}
	result.Changed = changed
	if title != "" {
		result.Title = title
	}
	// Once committed, cancellation must not prevent requested index maintenance.
	// rescan intentionally has no request context; report a refresh failure
	// separately so callers do not mistake a persisted translation for rollback.
	if opts.Refresh {
		if s.store == nil {
			result.RefreshError = "index refresh unavailable: store is nil"
		} else if err := s.rescan(movie); err != nil {
			result.RefreshError = err.Error()
		}
	}
	if !changed {
		result.Reason = "translation already matches NFO"
	}
	return result, nil
}
