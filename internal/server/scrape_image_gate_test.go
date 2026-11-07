package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScrapeImageSaturatedGate(t *testing.T) {
	app, ts, _ := contractEnv(t)
	if cap(app.scrapeSem) != 4 || maxScrapeImageBytes != 8<<20 {
		t.Fatalf("download limits: capacity=%d bytes=%d", cap(app.scrapeSem), maxScrapeImageBytes)
	}
	var fetched atomic.Int32
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		if strings.Contains(r.URL.Path, "/held-") {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fixture-image"))
	}))
	defer upstream.Close()
	defer close(release)
	for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture-token"} {
		if err := app.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	client := &http.Client{Timeout: 2 * time.Second}
	imageURL := ts.URL + "/api/admin/scrape/image?kind=primary&provider=test&id="
	fetch := func(id string, want int) {
		t.Helper()
		resp, err := client.Get(imageURL + id)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s: status=%d want=%d body=%q", id, resp.StatusCode, want, body)
		}
		if want == http.StatusOK && string(body) != "fixture-image" {
			t.Fatalf("%s: image body=%q", id, body)
		}
	}
	// Populate the byte cache through the router before occupying all four slots.
	fetch("cached", http.StatusOK)
	if fetched.Load() != 1 || len(app.scrapeSem) != 0 {
		t.Fatalf("priming cache: fetched=%d held=%d", fetched.Load(), len(app.scrapeSem))
	}
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			resp, err := client.Get(imageURL + fmt.Sprintf("held-%d", i))
			if err == nil {
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				err = readErr
				if err == nil && (resp.StatusCode != http.StatusOK || string(body) != "fixture-image") {
					err = fmt.Errorf("held-%d: status=%d body=%q", i, resp.StatusCode, body)
				}
			}
			done <- err
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("four downloads did not reach the upstream fixture")
		}
	}
	if fetched.Load() != 5 || len(app.scrapeSem) != 4 {
		t.Fatalf("four actual downloads: fetched=%d held=%d", fetched.Load(), len(app.scrapeSem))
	}

	// The four upstream requests remain blocked: a fifth miss must complete
	// without waiting for a slot, not merely after another download finishes.
	fetch("rejected", http.StatusServiceUnavailable)
	fetch("cached", http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, imageURL+"canceled-rejected", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("already-canceled saturated miss: status=%d", response.Code)
	}
	if fetched.Load() != 5 || len(app.scrapeSem) != 4 {
		t.Fatalf("rejected misses/cache hit changed admission: fetched=%d held=%d", fetched.Load(), len(app.scrapeSem))
	}

	// Release one fixture request at a time without sleeps, then verify that
	// successful downloads returned every slot and a fresh HTTP GET can enter.
	for i := 0; i < 4; i++ {
		release <- struct{}{}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if held := len(app.scrapeSem); held != 3-i {
			t.Fatalf("after completed download %d: held=%d", i+1, held)
		}
	}
	fetch("rejected", http.StatusOK)
	if fetched.Load() != 6 || len(app.scrapeSem) != 0 {
		t.Fatalf("later GET: fetched=%d held=%d", fetched.Load(), len(app.scrapeSem))
	}
}

func TestScrapeImageAdmittedGateRelease(t *testing.T) {
	for _, outcome := range []string{"success", "error", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			app, ts, _ := contractEnv(t)
			var fetched atomic.Int32
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fetched.Add(1)
				if strings.HasSuffix(r.URL.Path, "/first") {
					entered <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if outcome == "error" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				_, _ = w.Write([]byte("fixture-image"))
			}))
			defer upstream.Close()
			defer close(release)
			for key, value := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "fixture-token"} {
				if err := app.db.SetSetting(key, value); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := "/api/admin/scrape/image?kind=primary&provider=test&id="
			request := httptest.NewRequest(http.MethodGet, path+"first", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			handler := app.Handler()
			done := make(chan struct{})
			go func() {
				handler.ServeHTTP(response, request)
				close(done)
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("admitted download did not reach the upstream fixture")
			}
			if len(app.scrapeSem) != 1 {
				t.Fatalf("admitted download: held=%d", len(app.scrapeSem))
			}
			if outcome == "canceled" {
				cancel()
			} else {
				release <- struct{}{}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("admitted download did not complete")
			}
			want := http.StatusBadGateway
			if outcome == "success" {
				want = http.StatusOK
			}
			if response.Code != want || len(app.scrapeSem) != 0 || fetched.Load() != 1 {
				t.Fatalf("%s: status=%d want=%d held=%d fetched=%d", outcome, response.Code, want, len(app.scrapeSem), fetched.Load())
			}
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(ts.URL + path + "later")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || string(body) != "fixture-image" || len(app.scrapeSem) != 0 || fetched.Load() != 2 {
				t.Fatalf("later GET: status=%d body=%q held=%d fetched=%d", resp.StatusCode, body, len(app.scrapeSem), fetched.Load())
			}
		})
	}
}
