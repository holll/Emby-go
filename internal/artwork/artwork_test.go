package artwork

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/metatube"
	"emby-go/internal/store"
)

func picture() []byte {
	var b bytes.Buffer
	m := image.NewRGBA(image.Rect(0, 0, 32, 48))
	m.Set(0, 0, color.White)
	_ = jpeg.Encode(&b, m, nil)
	return b.Bytes()
}
func fixture(t *testing.T) (*store.Store, store.Movie, string) {
	t.Helper()
	dir := t.TempDir()
	db, e := store.Open(filepath.Join(dir, "store.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	lib, e := db.AddLibrary("test", dir)
	if e != nil {
		t.Fatal(e)
	}
	src := filepath.Join(dir, "A.strm")
	if e = os.WriteFile(src, []byte("https://trusted.example/video"), 0600); e != nil {
		t.Fatal(e)
	}
	id, e := db.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: src, Status: "pending", Title: "original", Plot: "plot", ProviderID: "keep", Genres: []string{"genre"}}, 1, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	m, e := db.Movie(id)
	if e != nil {
		t.Fatal(e)
	}
	return db, m, dir
}
func provider(t *testing.T, handler http.HandlerFunc) *metatube.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return metatube.New(srv.URL, "", time.Second*5)
}
func TestProviderIndependentPendingAndOverwrite(t *testing.T) {
	db, m, dir := fixture(t)
	shared := filepath.Join(dir, "poster.jpg")
	os.WriteFile(shared, picture(), 0600)
	other := filepath.Join(dir, "B.strm")
	os.WriteFile(other, []byte("https://example/video"), 0600)
	id, e := db.UpsertMovie(store.Movie{LibraryID: m.LibraryID, SourcePath: other, Status: "pending", PosterPath: shared}, 1, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	c := provider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/images/primary/source/42" || r.URL.Query().Get("url") != "" {
			t.Error(r.URL)
		}
		w.Write(picture())
	})
	req := Request{MovieID: m.ID, Mode: "provider", Provider: "source", ProviderID: "42"}
	res, e := Run(context.Background(), db, req, Options{Client: c})
	if e != nil || !res.Changed || res.RefreshError != "" {
		t.Fatalf("%+v %v", res, e)
	}
	if res.PosterPath != m.SourcePath+".poster.jpg" {
		t.Fatal(res)
	}
	after, _ := db.Movie(m.ID)
	after.PosterPath = m.PosterPath
	if !reflect.DeepEqual(m, after) {
		t.Fatalf("metadata changed: before=%+v after=%+v", m, after)
	}
	b, _ := db.Movie(id)
	if b.PosterPath != shared {
		t.Fatal("other film changed")
	}
	res, e = Run(context.Background(), db, req, Options{Client: c})
	if e != nil || !res.Skipped || calls.Load() != 1 {
		t.Fatalf("%+v %v calls=%d", res, e, calls.Load())
	}
	req.Overwrite = true
	res, e = Run(context.Background(), db, req, Options{Client: c})
	if e != nil || !res.Changed || calls.Load() != 2 {
		t.Fatalf("%+v %v", res, e)
	}
}
func TestWrongImageCancelAndSymlink(t *testing.T) {
	for _, kind := range []string{"wrong", "cancel", "source-link", "output-link", "directory-link"} {
		t.Run(kind, func(t *testing.T) {
			db, m, dir := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancel" {
				cancel()
			}
			outside := filepath.Join(t.TempDir(), "outside")
			os.WriteFile(outside, picture(), 0600)
			if kind == "source-link" {
				os.Remove(m.SourcePath)
				os.Symlink(outside, m.SourcePath)
			}
			if kind == "output-link" {
				os.Symlink(outside, m.SourcePath+".poster.jpg")
			}
			if kind == "directory-link" {
				moved := dir + "-moved"
				os.Rename(dir, moved)
				defer os.RemoveAll(moved)
				os.Symlink(moved, dir)
			}
			c := provider(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not an image")) })
			res, e := Run(ctx, db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}, Options{Client: c})
			if e == nil || res.Changed {
				t.Fatalf("%+v %v", res, e)
			}
			after, _ := db.Movie(m.ID)
			if !reflect.DeepEqual(m, after) {
				t.Fatal("index changed")
			}
		})
	}
}
func TestConcurrentAndDiskCAS(t *testing.T) {
	for _, kind := range []string{"source", "target", "metadata", "cancel", "concurrent"} {
		t.Run(kind, func(t *testing.T) {
			db, m, _ := fixture(t)
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			c := provider(t, func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-release; w.Write(picture()) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}
			var wg sync.WaitGroup
			var results [2]Result
			var errs [2]error
			count := 1
			if kind == "concurrent" {
				count = 2
			}
			for i := 0; i < count; i++ {
				wg.Add(1)
				go func(i int) { defer wg.Done(); results[i], errs[i] = Run(ctx, db, req, Options{Client: c}) }(i)
			}
			for i := 0; i < count; i++ {
				<-entered
			}
			switch kind {
			case "source":
				os.WriteFile(m.SourcePath, []byte("https://changed"), 0600)
			case "target":
				os.WriteFile(m.SourcePath+".poster.jpg", picture(), 0600)
			case "metadata":
				db.SetIndexedImage(m.ID, "landscape", "changed")
			case "cancel":
				cancel()
			}
			close(release)
			wg.Wait()
			if count == 1 {
				if errs[0] == nil || results[0].Changed {
					t.Fatalf("%+v %v", results[0], errs[0])
				}
			} else {
				success := 0
				for i := range errs {
					if errs[i] == nil && results[i].Changed {
						success++
					}
				}
				if success != 1 {
					t.Fatalf("%+v %v", results, errs)
				}
			}
		})
	}
}
func TestRepairIndexWithoutDownload(t *testing.T) {
	db, m, _ := fixture(t)
	path := m.SourcePath + ".poster.jpg"
	os.WriteFile(path, picture(), 0600)
	res, e := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}, Options{})
	if e != nil || !res.Changed || res.Reason != "index_repaired" {
		t.Fatalf("%+v %v", res, e)
	}
	after, _ := db.Movie(m.ID)
	if after.PosterPath != path || after.Status != "pending" {
		t.Fatal(after)
	}
}
func TestMediaHelper(t *testing.T) {
	if os.Getenv("ARTWORK_HELPER") != "1" {
		return
	}
	args := os.Args[1:]
	has := func(x string) bool {
		return slices.Contains(args, x)
	}
	if !has("-protocol_whitelist") || !has(protocols) {
		os.Exit(2)
	}
	if has("-show_entries") {
		os.Stdout.WriteString("100\n")
	} else {
		os.Stdout.Write(picture())
	}
	os.Exit(0)
}
func TestScreenshotProtocolAndPositions(t *testing.T) {
	// An executable helper verifies exact protocol arguments without external media.
	if os.Getenv("ARTWORK_HELPER") == "1" {
		return
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("ARTWORK_HELPER", "1")
	// Go's test executable accepts media arguments only after -- via this wrapper.
	wrapper := filepath.Join(t.TempDir(), "media")
	script := "#!/bin/sh\nexec \"" + exe + "\" -test.run=TestMediaHelper -- \"$@\"\n"
	os.WriteFile(wrapper, []byte(script), 0700)
	for _, seconds := range []*float64{nil, new(float64(3))} {
		b, e := screenshot(context.Background(), "https://trusted.example/movie", Request{PositionSeconds: seconds}, Options{FFmpegPath: wrapper, FFprobePath: wrapper})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = validatedJPEG(b); e != nil {
			t.Fatal(e)
		}
	}
	for _, raw := range []string{"file:///etc/passwd", "concat:https://x|file:///etc/passwd", "http://user:pass@host/x", "https://host/x\nhttps://other"} {
		if _, e = screenshot(context.Background(), raw, Request{}, Options{}); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}

//go:fix inline
func ptr(v float64) *float64 { return new(v) }
func TestRealFFmpegHTTP(t *testing.T) {
	ff, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, e := exec.LookPath("ffprobe")
	if e != nil {
		t.Skip("ffprobe unavailable")
	}
	video := filepath.Join(t.TempDir(), "clip.mp4")
	cmd := exec.Command(ff, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x96:d=2", "-c:v", "mpeg4", "-movflags", "+faststart", video)
	if e = cmd.Run(); e != nil {
		t.Fatal(e)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(video))))
	defer srv.Close()
	b, e := screenshot(context.Background(), srv.URL+"/clip.mp4", Request{}, Options{FFmpegPath: ff, FFprobePath: probe})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = validatedJPEG(b); e != nil {
		t.Fatal(e)
	}
}
func TestRefreshErrorRetry(t *testing.T) {
	db, m, dir := fixture(t)
	raw := openArtworkFixtureSQL(t, filepath.Join(dir, "store.db"))
	if _, e := raw.Exec("CREATE TRIGGER fail_art BEFORE UPDATE OF poster_path ON movies BEGIN SELECT RAISE(ABORT,'injected refresh failure'); END"); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	c := provider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write(picture())
	})
	req := Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}
	res, e := Run(context.Background(), db, req, Options{Client: c})
	path := m.SourcePath + ".poster.jpg"
	if e != nil || !res.Changed || res.RefreshError == "" || res.PosterPath != path || calls.Load() != 1 {
		t.Fatalf("%+v %v calls=%d", res, e, calls.Load())
	}
	preserved, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = jpeg.Decode(bytes.NewReader(preserved)); e != nil {
		t.Fatalf("refresh failure did not preserve a valid JPEG: %v", e)
	}
	after, e := db.Movie(m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(m, after) {
		t.Fatalf("failed index changed metadata: before=%+v after=%+v", m, after)
	}
	if _, e = raw.Exec("DROP TRIGGER fail_art"); e != nil {
		t.Fatal(e)
	}
	res, e = Run(context.Background(), db, req, Options{Client: c})
	if e != nil || res.Reason != "index_repaired" || !res.Changed || res.RefreshError != "" || res.PosterPath != path {
		t.Fatalf("%+v %v", res, e)
	}
	if calls.Load() != 1 {
		t.Fatalf("index repair downloaded again: calls=%d", calls.Load())
	}
	repaired, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(preserved, repaired) {
		t.Fatal("index repair changed the preserved image")
	}
	after, e = db.Movie(m.ID)
	if e != nil {
		t.Fatal(e)
	}
	want := m
	want.PosterPath = path
	if !reflect.DeepEqual(want, after) {
		t.Fatalf("index repair changed fields other than poster: want=%+v after=%+v", want, after)
	}
}

func TestValidationAndOutputCap(t *testing.T) {
	for _, req := range []Request{{MovieID: 1, Mode: "x"}, {MovieID: 1, Mode: "screenshot", PositionSeconds: new(float64(-1))}, {MovieID: 1, Mode: "screenshot", PositionPercent: new(float64(100))}, {MovieID: 1, Mode: "screenshot", PositionSeconds: new(float64(1)), PositionPercent: new(float64(2))}} {
		if validate(req) == nil {
			t.Fatal(req)
		}
	}
	b := &cappedBuffer{limit: 2}
	if _, e := b.Write([]byte("123")); e == nil {
		t.Fatal("cap")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := command(ctx, "ffmpeg", nil, 100); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
