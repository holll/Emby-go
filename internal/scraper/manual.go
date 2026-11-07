package scraper

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
)

// ErrInvalidManualSearch identifies invalid user input, not an upstream failure.
var ErrInvalidManualSearch = errors.New("invalid manual search")

// ManualSearch distinguishes omitted parameters from explicit empty values.
// MetaTubeID is a provider name or Provider:movie-id; movie-id is opaque, not a number.
type ManualSearch struct {
	Query      *string
	MetaTubeID *string
}

// Validate checks the explicit search without contacting MetaTube or changing state.
func (o ManualSearch) Validate() error {
	_, _, err := o.parse()
	return err
}

func (o ManualSearch) parse() (query string, pid metatube.ProviderID, err error) {
	invalid := func(message string) (string, metatube.ProviderID, error) {
		return "", metatube.ProviderID{}, fmt.Errorf("%w: %s", ErrInvalidManualSearch, message)
	}
	if o.Query != nil {
		query = strings.TrimSpace(*o.Query)
		if query == "" || strings.ContainsFunc(query, unicode.IsControl) {
			return invalid("q must not be blank or contain control characters")
		}
		// URLs must be forwarded intact: MetaTube's search route resolves them
		// via GetMovieInfoByURL rather than treating their path as a number.
		lower := strings.ToLower(query)
		if strings.Contains(query, "://") || strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") {
			u, parseErr := url.Parse(query)
			if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
				return invalid("q URL must be an absolute HTTP(S) URL without credentials")
			}
		}
	}
	if o.MetaTubeID != nil {
		selector := strings.TrimSpace(*o.MetaTubeID)
		if strings.Contains(selector, ":") {
			var ok bool
			pid, ok = metatube.ParseProviderIDValue(selector)
			if !ok {
				return invalid("metatube_id must be a valid Provider:movie-id plugin identity")
			}
		} else {
			pid = metatube.ProviderID{Provider: selector}
		}
		if pid.Provider == "" || strings.ContainsAny(pid.Provider, ":/\\?#") || strings.ContainsFunc(pid.Provider, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) {
			return invalid("metatube_id must be Provider or Provider:movie-id")
		}
		if strings.Contains(selector, ":") && (pid.ID == "" || pid.ID == "." || pid.ID == ".." || strings.ContainsAny(pid.ID, ":/\\?#") || strings.ContainsFunc(pid.ID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })) {
			return invalid("metatube_id requires a nonempty movie ID after the colon")
		}
	}
	if query == "" && pid.ID == "" {
		return invalid("q is required unless metatube_id is Provider:movie-id")
	}
	return query, pid, nil
}

// PreviewManual explicitly searches user input. It never infers a query from the
// existing movie and never translates, downloads artwork, or writes local state.
func (s *Scraper) PreviewManual(ctx context.Context, m store.Movie, opts ManualSearch) (PreviewResult, error) {
	result := PreviewResult{MovieID: m.ID, Recommended: -1}
	query, pid, err := opts.parse()
	if err != nil {
		return result, err
	}
	if pid.ID != "" {
		// A provider's movie ID need not resemble its displayed movie number.
		// Do not keyword-search it or substitute the existing movie's number.
		result.Query = pid.String()
		info, err := s.client.MovieInfo(ctx, pid.Provider, pid.ID, pid.Lazy())
		if metatube.IsNotFound(err) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		result = previewCandidates(result, []metatube.MovieSearchResult{{
			Provider: info.Provider, ID: info.ID, Number: info.Number, Title: info.Title,
			Score: info.Score, ReleaseDate: info.ReleaseDate,
		}})
		carryLookupOptions(result.Candidates, pid)
		return result, nil
	}
	result.Query = query
	// Match against the manual number, never stale metadata from the item being
	// corrected. URLs have no expected number; resolution belongs to MetaTube.
	if u, err := url.Parse(query); err != nil || u.Scheme == "" {
		result.Expected = metatube.Normalize(query)
	}
	results, err := s.client.SearchMovies(ctx, query, pid.Provider, true)
	if err != nil {
		return result, err
	}
	// The upstream URL resolver ignores its provider argument. Keep an explicit
	// restriction strict even for URLs (and never offer cross-provider fallbacks).
	if pid.Provider != "" {
		filtered := results[:0]
		for _, candidate := range results {
			if strings.EqualFold(candidate.Provider, pid.Provider) {
				filtered = append(filtered, candidate)
			}
		}
		results = filtered
	}
	return previewCandidates(result, results), nil
}
