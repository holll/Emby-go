package probe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"emby-go/internal/outbound"
)

// This finite-input component is not yet connected to Run: a complete download
// budget would reject ordinary large movies, unlike the current seekable URL
// path. Do not use a truncated prefix as successful probe input.
const maxInputSize = 64 << 20

var (
	errInputTooLarge = errors.New("probe input exceeds 64 MiB limit")
	errInputRequest  = errors.New("probe input HTTP request failed")
	errInputRead     = errors.New("probe input HTTP body read failed")
	inputHTTPClient  = outbound.NewClient(outbound.Options{
		// Unlike playback, probing must never delegate resolution to an
		// environment proxy or inherit an allowance for private media hosts.
		ProxyFunc: func(*http.Request) (*url.URL, error) { return nil, nil },
	})
)

// downloadInput owns the request and its body; the caller owns the whole-job
// deadline and admission. Errors deliberately contain no source URL, query,
// userinfo, Location header or arbitrary transport/body diagnostics.
func downloadInput(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errInputRequest
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := inputHTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errInputRequest
	}
	defer resp.Body.Close()
	return readInput(ctx, resp)
}

// readInput validates the untrusted response once. A 206 or a truncated body
// is not a complete finite input, even if ffprobe could parse a stream from it.
func readInput(ctx context.Context, resp *http.Response) ([]byte, error) {
	// net/http canonicalizes wire header keys; even an empty or duplicated
	// Content-Range header is outside this complete-response contract.
	_, partial := resp.Header["Content-Range"]
	if resp.StatusCode != http.StatusOK || partial {
		return nil, errors.New("probe input requires a complete HTTP 200 response")
	}
	if resp.ContentLength > maxInputSize {
		return nil, errInputTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInputSize+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(body) > maxInputSize {
		return nil, errInputTooLarge
	}
	if err != nil || (resp.ContentLength >= 0 && int64(len(body)) != resp.ContentLength) {
		return nil, errInputRead
	}
	return body, nil
}
