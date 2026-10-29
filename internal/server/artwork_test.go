package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/store"
)

func artworkFixture(t *testing.T) translationFixture {
	f := newTranslationFixture(t)
	f.a.artwork.lookPath = func(s string) (string, error) { return "/mock/" + s, nil }
	return f
}

func artworkJoin(t *testing.T, a *App) artworkStatus {
	t.Helper()
	done := make(chan struct{})
	go func() { a.workerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("artwork worker did not finish")
	}
	a.artwork.mu.RLock()
	defer a.artwork.mu.RUnlock()
	return a.artwork.status
}

func artworkBody(id int64) string { return fmt.Sprintf(`{"mode":"screenshot","movie_ids":[%d]}`, id) }

func TestArtworkPermissionsAndParameterPresence(t *testing.T) {
	f := artworkFixture(t)
	user, err := f.a.db.CreateUser("artwork-user", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("artwork-ordinary", user.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{"POST", "run"}, {"GET", "status"}, {"POST", "cancel"}, {"GET", "capabilities"}, {"GET", "candidates"}} {
		path := "/api/admin/artwork/" + route.path
		for token, want := range map[string]int{"": 401, "artwork-ordinary": 403} {
			if w := translationCall(f.a, token, route.method, path, `{}`); w.Code != want {
				t.Fatalf("%s %s: %d %s", route.method, path, w.Code, w.Body.String())
			}
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{} {}`, `{"mode":"screenshot"}`, `{"mode":"screenshot","movie_ids":[]}`, `{"mode":"screenshot","movie_ids":null}`, `{"mode":"screenshot","movie_ids":[1],"library_id":0}`, `{"mode":"screenshot","movie_ids":[1],"limit":0}`, `{"mode":"screenshot","movie_ids":[1,1]}`, `{"mode":"screenshot","movie_ids":[0]}`, `{"mode":"screenshot","movie_ids":[1],"position_seconds":null}`, `{"mode":"screenshot","movie_ids":[1],"position_seconds":0,"position_percent":0}`, `{"mode":"screenshot","movie_ids":[1],"position_percent":101}`, `{"mode":"screenshot","movie_ids":[1],"position_seconds":-1}`, `{"mode":"screenshot","library_id":1}`, `{"mode":"screenshot","library_id":1,"limit":0}`, `{"mode":"provider","movie_ids":[1,2],"provider":"M","id":"1"}`, `{"mode":"provider","movie_ids":[1],"provider":"http://bad","id":"1"}`, `{"mode":"screenshot","movie_ids":[1],"url":"https://bad"}`} {
		if w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", body); w.Code != 400 {
			t.Fatalf("%s -> %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, field := range []string{"position_seconds", "position_percent"} {
		req, err := decodeArtworkRequest(strings.NewReader(fmt.Sprintf(`{"mode":"screenshot","movie_ids":[1],"%s":0}`, field)))
		if err != nil || (field == "position_seconds" && req.PositionSeconds == nil) || (field == "position_percent" && req.PositionPercent == nil) {
			t.Fatalf("zero presence %s: %+v %v", field, req, err)
		}
	}
	f.a.artwork.lookPath = func(string) (string, error) { return "", errors.New("secret path") }
	if w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie)); w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("capability: %d %s", w.Code, w.Body.String())
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", fmt.Sprintf(`{"mode":"provider","movie_ids":[%d],"provider":"M","id":"1"}`, f.movie)); w.Code != 503 {
		t.Fatalf("unconfigured %d %s", w.Code, w.Body.String())
	}
}

func TestArtworkPendingFailurePreservesMetadataAndTaskID(t *testing.T) {
	f := artworkFixture(t)
	m, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	m.Status = "pending"
	m.LastScrapeError = "keep prior scrape failure"
	m.LastScrapeAt = "2020-01-01"
	if _, err = f.a.db.IndexMovie(m, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	before, _ := f.a.db.Movie(f.movie)
	nfoBefore, _ := os.ReadFile(m.NFOPath)
	f.a.artwork.run = func(ctx context.Context, db *store.Store, req artwork.Request, opts artwork.Options) (artwork.Result, error) {
		if req.MovieID != f.movie || req.Overwrite || opts.Timeout != 90*time.Second {
			t.Errorf("request/options %+v %+v", req, opts)
		}
		return artwork.Result{}, errors.New("http://upstream/?token=SECRET")
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var accepted artworkStatus
	if err = json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	s := artworkJoin(t, f.a)
	if accepted.TaskID <= 0 || accepted.TaskID != s.TaskID || s.Failed != 1 || s.Running || strings.Contains(s.Error, "SECRET") {
		t.Fatalf("accepted=%+v status=%+v", accepted, s)
	}
	after, _ := f.a.db.Movie(f.movie)
	nfoAfter, _ := os.ReadFile(m.NFOPath)
	if !reflect.DeepEqual(before, after) || string(nfoBefore) != string(nfoAfter) {
		t.Fatal("failed artwork modified metadata/scrape result")
	}
	if w = translationCall(f.a, f.token, "GET", "/api/admin/artwork/status?instance_id="+f.a.instanceID+"&task_id="+strconv.FormatInt(s.TaskID, 10), ""); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestArtworkExclusiveGateCancellationAndGeneration(t *testing.T) {
	f := artworkFixture(t)
	entered := make(chan struct{}, 2)
	f.a.artwork.run = func(ctx context.Context, _ *store.Store, _ artwork.Request, _ artwork.Options) (artwork.Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return artwork.Result{}, ctx.Err()
	}
	start := func() artworkStatus {
		t.Helper()
		w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie))
		if w.Code != 202 {
			t.Fatalf("run %d %s", w.Code, w.Body.String())
		}
		var s artworkStatus
		json.Unmarshal(w.Body.Bytes(), &s)
		return s
	}
	cancel := func(id int64) int {
		return translationCall(f.a, f.token, "POST", "/api/admin/artwork/cancel", fmt.Sprintf(`{"task_id":%d,"instance_id":%q}`, id, f.a.instanceID)).Code
	}
	first := start()
	<-entered
	if f.a.claimNFO("artwork") || f.a.claimNFO("scan") {
		t.Fatal("artwork channel not exclusive")
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie)); w.Code != 409 {
		t.Fatalf("busy %d", w.Code)
	}
	if cancel(first.TaskID) != 200 {
		t.Fatal("cancel failed")
	}
	if s := artworkJoin(t, f.a); !s.Cancelled {
		t.Fatalf("not cancelled %+v", s)
	}
	second := start()
	<-entered
	if first.TaskID == second.TaskID || cancel(first.TaskID) != 404 {
		t.Fatal("stale cancel not rejected")
	}
	f.a.artwork.mu.RLock()
	running := f.a.artwork.status.Running
	f.a.artwork.mu.RUnlock()
	if !running {
		t.Fatal("old cancellation stopped new task")
	}
	f.a.BeginShutdown()
	s := artworkJoin(t, f.a)
	if !s.Cancelled {
		t.Fatal("shutdown did not cancel worker")
	}
	if !f.a.claimNFO("scan") {
		t.Fatal("gate leaked")
	}
	f.a.releaseNFO("scan")
}

func TestArtworkFastCompletionBatchLimitRefreshAndCache(t *testing.T) {
	f := artworkFixture(t)
	m, _ := f.a.db.Movie(f.movie)
	m.ID = 0
	m.SourcePath += "second"
	if _, err := f.a.db.IndexMovie(m, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.a.artwork.run = func(context.Context, *store.Store, artwork.Request, artwork.Options) (artwork.Result, error) {
		calls++
		return artwork.Result{Changed: true, RefreshError: "SECRET", PosterPath: "poster"}, nil
	}
	f.a.tags["poster"] = tagEntry{tag: "old"}
	w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", fmt.Sprintf(`{"mode":"screenshot","library_id":%d,"limit":1}`, f.lib))
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var accepted artworkStatus
	json.Unmarshal(w.Body.Bytes(), &accepted)
	s := artworkJoin(t, f.a)
	if calls != 1 || s.Total != 1 || s.Done != 1 || s.IndexRefreshFailed != 1 || s.Failed != 1 || accepted.TaskID != s.TaskID || strings.Contains(s.Error, "SECRET") {
		t.Fatalf("accepted=%+v status=%+v calls=%d", accepted, s, calls)
	}
	if _, exists := f.a.tags["poster"]; exists {
		t.Fatal("image tag not invalidated")
	}
}

func TestArtworkCandidatesWhitelistAndFixedConfiguration(t *testing.T) {
	f := artworkFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/movies/search" || r.URL.Query().Get("q") != "ABC" {
			t.Errorf("unexpected upstream %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"provider":"M","id":"one:two","title":"candidate","cover_url":"https://SECRET/","homepage":"https://SECRET/","thumb_url":"https://SECRET/"}]}`)
	}))
	defer upstream.Close()
	for k, v := range map[string]string{settingMetatubeURL: upstream.URL, settingMetatubeToken: "SECRET", settingTranslateAPIURL: "", settingTranslateAPIKey: ""} {
		if err := f.a.db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	w := translationCall(f.a, f.token, "GET", fmt.Sprintf("/api/admin/artwork/candidates?movie_id=%d&q=ABC", f.movie), "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "SECRET") || !strings.Contains(w.Body.String(), "/api/admin/scrape/image?") {
		t.Fatalf("candidates %d %s", w.Code, w.Body.String())
	}
	f.a.artwork.run = func(_ context.Context, _ *store.Store, req artwork.Request, opts artwork.Options) (artwork.Result, error) {
		if opts.Client == nil || req.Provider != "M" || req.ProviderID != "one:two" {
			t.Errorf("provider config %+v %+v", req, opts)
		}
		return artwork.Result{Skipped: true}, nil
	}
	w = translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", fmt.Sprintf(`{"mode":"provider","movie_ids":[%d],"provider":"M","id":"one:two"}`, f.movie))
	if w.Code != 202 {
		t.Fatalf("provider without translation %d %s", w.Code, w.Body.String())
	}
	artworkJoin(t, f.a)
}

func TestArtworkAdmissionAfterBodyParsing(t *testing.T) {
	for _, path := range []string{"run", "cancel"} {
		t.Run(path, func(t *testing.T) {
			f := artworkFixture(t)
			raw := artworkBody(f.movie)
			if path == "cancel" {
				raw = fmt.Sprintf(`{"task_id":1,"instance_id":%q}`, f.a.instanceID)
			}
			read := false
			body := &passwordIdentityReader{Reader: strings.NewReader(raw), beforeRead: func() {
				read = true
				if err := f.a.db.DeleteAccessToken(f.token); err != nil {
					t.Fatal(err)
				}
			}}
			req := httptest.NewRequest("POST", "/api/admin/artwork/"+path, body)
			req.ContentLength = int64(len(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Emby-Token", f.token)
			w := httptest.NewRecorder()
			f.a.Handler().ServeHTTP(w, req)
			if !read || w.Code != 403 {
				t.Fatalf("read=%v status=%d %s", read, w.Code, w.Body.String())
			}
			if f.a.artwork.status.TaskID != 0 {
				t.Fatal("revoked request admitted work")
			}
		})
	}
}
