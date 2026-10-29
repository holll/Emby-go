package artwork

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/store"
)

func TestRejectUnsafeTokensAndTimestampOverflow(t *testing.T) {
	for _, token := range []string{"", ".", "..", "a/b", "a\\b", "a?b", "a#b", "a&b", "a%2fb", "a\x00b", "a\nb", " a", strings.Repeat("x", 257)} {
		if err := validate(Request{MovieID: 1, Mode: "provider", Provider: token, ProviderID: "id"}); err == nil {
			t.Errorf("accepted provider %q", token)
		}
		if err := validate(Request{MovieID: 1, Mode: "provider", Provider: "source", ProviderID: token}); err == nil {
			t.Errorf("accepted ID %q", token)
		}
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.MaxFloat64, maxSeekSeconds * 2} {
		if err := validate(Request{MovieID: 1, Mode: "screenshot", PositionSeconds: ptr(v)}); err == nil {
			t.Errorf("accepted seconds %v", v)
		}
	}
	if err := validate(Request{MovieID: 1, Mode: "screenshot", Provider: "unused"}); err == nil {
		t.Fatal("mixed mode accepted")
	}
	for _, raw := range []string{"data:image/jpeg;base64,x", "file:///etc/passwd", "concat:https://x|https://y", "http://:80/x", "http:///x", "https://user@host/x", "https://host/x\x00", "https://host/x\r\nHeader: x"} {
		if _, err := screenshot(context.Background(), raw, Request{PositionSeconds: ptr(0)}, Options{}); err == nil {
			t.Errorf("accepted STRM %q", raw)
		}
	}
}

func TestSharedPosterProtectionAndExplicitOverwrite(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip", true: "overwrite"}[overwrite], func(t *testing.T) {
			db, m, dir := fixture(t)
			shared := filepath.Join(dir, "poster.jpg")
			original := picture()
			if err := os.WriteFile(shared, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := db.SetIndexedImage(m.ID, "poster", shared); err != nil {
				t.Fatal(err)
			}
			before, err := db.Movie(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			nfo := filepath.Join(dir, "A.nfo")
			metadata := []byte("<movie><title>untouched</title></movie>")
			if err := os.WriteFile(nfo, metadata, 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			c := provider(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write(picture()) })
			res, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i", Overwrite: overwrite}, Options{Client: c})
			if err != nil || res.Changed != overwrite || res.Skipped == overwrite {
				t.Fatalf("%+v %v", res, err)
			}
			if (calls.Load() == 1) != overwrite {
				t.Fatalf("download calls=%d", calls.Load())
			}
			data, err := os.ReadFile(shared)
			if err != nil || string(data) != string(original) {
				t.Fatal("shared poster overwritten", err)
			}
			data, err = os.ReadFile(nfo)
			if err != nil || string(data) != string(metadata) {
				t.Fatal("NFO changed", err)
			}
			after, err := db.Movie(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if overwrite && after.PosterPath != m.SourcePath+".poster.jpg" {
				t.Fatal("not an independent poster")
			}
			after.PosterPath = before.PosterPath
			if !reflect.DeepEqual(before, after) {
				t.Fatal("nonposter metadata changed")
			}
		})
	}
}

func TestExistingSharedImageProtectedDuringDownload(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "fill", true: "overwrite"}[overwrite], func(t *testing.T) {
			db, m, dir := fixture(t)
			shared := filepath.Join(dir, "poster.jpg")
			if err := os.WriteFile(shared, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := db.SetIndexedImage(m.ID, "poster", shared); err != nil {
				t.Fatal(err)
			}
			before, err := db.Movie(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			c := provider(t, func(w http.ResponseWriter, r *http.Request) {
				if err := os.WriteFile(shared, picture(), 0600); err != nil {
					t.Error(err)
				}
				w.Write(picture())
			})
			res, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "source", ProviderID: "id", Overwrite: overwrite}, Options{Client: c})
			if !errors.Is(err, store.ErrArtworkChanged) || res.Changed {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			after, e := db.Movie(m.ID)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("changed index after indexed poster raced")
			}
			if _, e := os.Stat(m.SourcePath + ".poster.jpg"); !os.IsNotExist(e) {
				t.Fatalf("installed private poster: %v", e)
			}
			assertNoArtworkTemp(t, dir)
		})
	}
}

func assertNoArtworkTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".artwork-") {
			t.Fatalf("leaked temporary file %s", entry.Name())
		}
	}
}

func TestProviderDeadlineNoInstall(t *testing.T) {
	db, m, dir := fixture(t)
	c := provider(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	res, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "id"}, Options{Client: c, Timeout: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	after, e := db.Movie(m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(m, after) {
		t.Fatal("timeout changed metadata")
	}
	assertNoArtworkTemp(t, dir)
}

func TestSourceParentSwapRejected(t *testing.T) {
	db, m, dir := fixture(t)
	moved := dir + "-moved"
	defer os.RemoveAll(moved)
	c := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if err := os.Rename(dir, moved); err != nil {
			t.Error(err)
			return
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(m.SourcePath, []byte("replacement STRM"), 0600); err != nil {
			t.Error(err)
			return
		}
		w.Write(picture())
	})
	res, err := Run(context.Background(), db, Request{MovieID: m.ID, Mode: "provider", Provider: "p", ProviderID: "i"}, Options{Client: c})
	if !errors.Is(err, store.ErrArtworkChanged) || res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	for _, path := range []string{m.SourcePath + ".poster.jpg", filepath.Join(moved, filepath.Base(m.SourcePath)+".poster.jpg")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("installed into changed directory: %v", err)
		}
	}
	assertNoArtworkTemp(t, dir)
	assertNoArtworkTemp(t, moved)
}

func TestOpenRegularRejectsDirectoryAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "image.jpg"), picture(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("image.jpg", filepath.Join(dir, "linked.jpg")); err != nil {
		t.Skip(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, p := range []string{".", "linked.jpg"} {
		if f, e := openRegular(root, p); e == nil {
			f.Close()
			t.Fatalf("opened nonregular %q", p)
		}
		if realImage(root, p) {
			t.Fatalf("protected nonregular %q as image", p)
		}
	}
}
