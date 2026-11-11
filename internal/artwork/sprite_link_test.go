package artwork

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// linkServer starts a server that records the headers it receives and lets the
// test decide the response.
func linkServer(t *testing.T, handler func(http.ResponseWriter, *http.Request, *http.Header)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Clone()
		handler(w, r, &header)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveSpriteLinkFollowsRedirectAndSendsUserAgent(t *testing.T) {
	var directUA, directRange string
	direct := linkServer(t, func(w http.ResponseWriter, r *http.Request, h *http.Header) {
		directUA = h.Get("User-Agent")
		directRange = h.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("frame"))
	})
	var redirects atomic.Int32
	var redirectUA string
	entry := linkServer(t, func(w http.ResponseWriter, r *http.Request, h *http.Header) {
		redirects.Add(1)
		redirectUA = h.Get("User-Agent")
		http.Redirect(w, r, direct.URL+"/movie.mp4?sig=abc", http.StatusFound)
	})

	got, err := resolveSpriteLink(context.Background(), &http.Client{}, entry.URL+"/play")
	if err != nil {
		t.Fatal(err)
	}
	if want := direct.URL + "/movie.mp4?sig=abc"; got != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
	if redirects.Load() != 1 {
		t.Fatalf("redirect hits %d", redirects.Load())
	}
	// 115 signs the link against the request UA, so both hops must carry it.
	for name, ua := range map[string]string{"entry": redirectUA, "direct": directUA} {
		if ua != spriteUserAgent {
			t.Fatalf("%s hop UA %q", name, ua)
		}
	}
	if directRange != "bytes=0-0" {
		t.Fatalf("probe range %q", directRange)
	}
}

func TestResolveSpriteLinkKeepsDirectURL(t *testing.T) {
	var hits atomic.Int32
	srv := linkServer(t, func(w http.ResponseWriter, r *http.Request, _ *http.Header) {
		hits.Add(1)
		w.WriteHeader(http.StatusPartialContent)
	})
	got, err := resolveSpriteLink(context.Background(), &http.Client{}, srv.URL+"/direct?sig=x")
	if err != nil {
		t.Fatal(err)
	}
	if got != srv.URL+"/direct?sig=x" || hits.Load() != 1 {
		t.Fatalf("direct %q hits %d", got, hits.Load())
	}
}

func TestResolveSpriteLinkHidesURLOnFailure(t *testing.T) {
	srv := linkServer(t, func(w http.ResponseWriter, r *http.Request, _ *http.Header) {
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := resolveSpriteLink(context.Background(), &http.Client{}, srv.URL+"/secret-canary?sig=token-canary")
	if err == nil {
		t.Fatal("403 resolved")
	}
	text := err.Error()
	if !strings.Contains(text, "403") {
		t.Fatalf("missing status: %v", err)
	}
	for _, secret := range []string{srv.URL, "secret-canary", "token-canary", "127.0.0.1"} {
		if strings.Contains(text, secret) {
			t.Fatalf("leaked %q in %q", secret, text)
		}
	}
}

// TestSpriteResolvesLinkOnceForEveryFrame proves the whole generation reuses one
// resolved link: the redirect chain is entered exactly once while every frame
// invocation is handed the direct URL.
func TestSpriteResolvesLinkOnceForEveryFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	db, m, dir := fixture(t)
	var directHits, redirects atomic.Int32
	var wrongUA atomic.Bool
	direct := linkServer(t, func(w http.ResponseWriter, r *http.Request, h *http.Header) {
		directHits.Add(1)
		if h.Get("User-Agent") != spriteUserAgent {
			wrongUA.Store(true)
		}
		w.WriteHeader(http.StatusPartialContent)
	})
	entry := linkServer(t, func(w http.ResponseWriter, r *http.Request, _ *http.Header) {
		redirects.Add(1)
		http.Redirect(w, r, direct.URL+"/movie.mp4", http.StatusFound)
	})
	if err := os.WriteFile(m.SourcePath, []byte(entry.URL+"/play"), 0600); err != nil {
		t.Fatal(err)
	}

	var jpg bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := range 180 {
		for x := range 320 {
			img.Set(x, y, color.RGBA{10, 10, 240, 255})
		}
	}
	if err := jpeg.Encode(&jpg, img, nil); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(dir, "ffmpeg-args")
	frame := "printf '%s\\n' \"$@\" >> '" + args + "'; printf -- '--\\n' >> '" + args +
		"'; printf '%s' '" + base64.StdEncoding.EncodeToString(jpg.Bytes()) + "' | base64 -d"
	ff := spriteScript(t, dir, "link-ffmpeg", frame)
	probe := spriteScript(t, dir, "link-probe", "printf '%s\\n' \"$@\" >> '"+args+"'; printf -- '--\\n' >> '"+args+"'; printf '2.5\\n'")

	opts := SpriteOptions{RootDir: filepath.Join(dir, "video-previews"), FFmpegPath: ff, FFprobePath: probe, Timeout: 60 * time.Second, LinkClient: &http.Client{}}
	out, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: m.ID, IntervalMs: 1000}, opts)
	if err != nil || !out.Changed {
		t.Fatalf("%+v %v", out, err)
	}
	if redirects.Load() != 1 {
		t.Fatalf("redirect chain entered %d times", redirects.Load())
	}
	if wrongUA.Load() {
		t.Fatal("resolution request missing the sprite user agent")
	}
	if directHits.Load() != 1 {
		t.Fatalf("direct link probed %d times, want only the resolution request", directHits.Load())
	}
	data, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, block := range strings.Split(string(data), "--\n") {
		if block = strings.TrimSpace(block); block != "" {
			calls = append(calls, block)
		}
	}
	// One probe plus 3 frames for a 2.5s source at the 1s interval.
	if len(calls) != 4 {
		t.Fatalf("calls %d: %q", len(calls), calls)
	}
	directURL := direct.URL + "/movie.mp4"
	for i, call := range calls {
		if !strings.Contains(call, directURL) {
			t.Fatalf("call %d not on the resolved link: %q", i, call)
		}
		if i > 0 && strings.Contains(call, entry.URL) {
			t.Fatalf("call %d re-entered the redirect: %q", i, call)
		}
		if strings.Contains(call, spriteUserAgent) == false {
			t.Fatalf("call %d missing UA: %q", i, call)
		}
	}
	// The published source hash still comes from the STRM file, not the link.
	state, err := db.SpriteContext(context.Background(), m.ID)
	if err != nil || state.SourceHash == "" || state.Generation != out.Manifest.Generation {
		t.Fatalf("state %+v %v", state, err)
	}
}

// TestSpriteStageArgvTemplateKeepsUserAgent pins the diagnostic template to the
// real command line: the -user_agent pair shifts every URL index, and a silent
// mismatch would drop argv_template from the media_process log.
func TestSpriteStageArgvTemplateKeepsUserAgent(t *testing.T) {
	dir := t.TempDir()
	fake := spriteScript(t, dir, "fail", "exit 7")
	opts := SpriteOptions{FFmpegPath: fake, FFprobePath: fake}
	raw := "https://direct.invalid/private-canary?sig=query-canary"
	for stage, call := range map[string]func() error{
		"probe": func() error {
			_, err := spriteProbe(context.Background(), raw, opts)
			return err
		},
		"frame": func() error {
			_, err := spriteFrame(context.Background(), raw, 4200, opts)
			return err
		},
	} {
		t.Run(stage, func(t *testing.T) {
			err := call()
			var diagnostic *MediaProcessError
			if !errors.As(err, &diagnostic) {
				t.Fatalf("missing diagnostics: %v", err)
			}
			if len(diagnostic.argv) == 0 {
				t.Fatal("argv template missing")
			}
			var logs bytes.Buffer
			slog.New(slog.NewJSONHandler(&logs, nil)).Warn("failed", "media_process", diagnostic)
			joined := logs.String()
			for _, want := range []string{`"argv_template"`, "$STREAM_URL", "-user_agent", spriteUserAgent} {
				if !strings.Contains(joined, want) {
					t.Fatalf("missing %q: %s", want, joined)
				}
			}
			if stage == "frame" && !strings.Contains(joined, "4.200") {
				t.Fatalf("missing seek: %s", joined)
			}
			for _, secret := range []string{"private-canary", "query-canary", "direct.invalid"} {
				if strings.Contains(joined, secret) || strings.Contains(err.Error(), secret) {
					t.Fatalf("leaked %q", secret)
				}
			}
		})
	}
}
