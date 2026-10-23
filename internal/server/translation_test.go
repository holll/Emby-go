package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/scheduler"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

type translationFixture struct {
	a          *App
	token      string
	movie, lib int64
}

func newTranslationFixture(t *testing.T) translationFixture {
	t.Helper()
	root := t.TempDir()
	a, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.WaitBackground(ctx)
		a.Close()
	})
	seedOwner(t, a)
	token := "translation-admin"
	if err := a.db.SaveAccessTokenForUser(token, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{settingTranslateAPIURL: "http://127.0.0.1:1", settingTranslateAPIKey: "test-key"} {
		if err := a.db.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	lib, err := a.db.AddLibrary("Translation", root)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "ABC-123.strm")
	sidecar := filepath.Join(root, "ABC-123.nfo")
	if err := os.WriteFile(source, []byte("https://example.com/video.mkv"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, []byte(`<movie><num>ABC-123</num><title>old</title><originaltitle>原題</originaltitle><plot>old plot</plot></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := a.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: source, NFOPath: sidecar, Title: "old", Status: "success"}, 1, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return translationFixture{a: a, token: token, movie: id, lib: lib.ID}
}

func translationCall(a *App, token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	return w
}

func awaitTranslation(t *testing.T, a *App) translationStatus {
	t.Helper()
	done := make(chan struct{})
	go func() { a.workerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("translation worker did not finish")
	}
	status := a.translationSnapshot()
	if status.Running {
		t.Fatal("worker joined before terminal status")
	}
	return status
}

func TestTranslationAuthorizationAndValidation(t *testing.T) {
	f := newTranslationFixture(t)
	u, err := f.a.db.CreateUser("translation-user", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.SaveAccessTokenForUser("ordinary", u.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{"POST", "/api/admin/translate/run"}, {"GET", "/api/admin/translate/status"}, {"POST", "/api/admin/translate/cancel"}} {
		if w := translationCall(f.a, "", route.method, route.path, `{}`); w.Code != 401 {
			t.Fatalf("anonymous %s %d", route.path, w.Code)
		}
		if w := translationCall(f.a, "ordinary", route.method, route.path, `{}`); w.Code != 403 {
			t.Fatalf("ordinary %s %d", route.path, w.Code)
		}
	}
	for _, body := range []string{`null`, `[]`, `{"fields":[]}`, `{"fields":null}`, `{"fields":["summary"]}`, `{"fields":["Title"]}`, `{"fields":"title"}`, `{"movie_ids":[]}`, `{"movie_ids":null}`, `{"movie_ids":[0]}`, `{"movie_ids":[1.2]}`, `{"movie_ids":[1],"library_id":0}`, `{"movie_ids":[1],"library_id":1}`, `{"library_id":-1}`, `{"library_id":null}`, `{"limit":-1}`, `{"limit":100001}`, `{"limit":1.5}`, `{"limit":null}`, `{"unknown":1}`, `{} {}`} {
		if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", body); w.Code != 400 {
			t.Fatalf("body=%s status=%d %s", body, w.Code, w.Body.String())
		}
	}
	ids := make([]int64, maxTranslationIDs+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	data, _ := json.Marshal(map[string]any{"movie_ids": ids})
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", string(data)); w.Code != 400 {
		t.Fatalf("too many IDs: %d", w.Code)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{"movie_ids":[999999]}`); w.Code != 404 {
		t.Fatalf("missing target: %d %s", w.Code, w.Body.String())
	}
	m, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	m.Status = "pending"
	if _, err := f.a.db.IndexMovie(m, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", fmt.Sprintf(`{"movie_ids":[%d]}`, f.movie)); w.Code != 404 {
		t.Fatalf("invisible target: %d", w.Code)
	}
	if f.a.translationSnapshot().TaskID != 0 {
		t.Fatal("invalid request started a task")
	}
}

func TestTranslationBusyCancellationAndTaskGeneration(t *testing.T) {
	f := newTranslationFixture(t)
	entered := make(chan struct{})
	var calls atomic.Int32
	f.a.translation.item = func(ctx context.Context, m store.Movie, opts scraper.TranslationOptions) (scraper.TranslationResult, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return scraper.TranslationResult{}, ctx.Err()
	}
	if !f.a.claimNFO("edit") {
		t.Fatal("claim edit")
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`); w.Code != 409 {
		t.Fatalf("edit busy: %d", w.Code)
	}
	f.a.releaseNFO("edit")
	w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", fmt.Sprintf(`{"movie_ids":[%d,%d]}`, f.movie, f.movie))
	if w.Code != 202 {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	var accepted translationStatus
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TaskID <= 0 || accepted.Total != 1 || !accepted.Running {
		t.Fatalf("accepted %+v", accepted)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("item not entered")
	}
	if f.a.nfoBusyOwner() != "翻译" || f.a.claimNFO("scan") {
		t.Fatal("translation did not retain shared NFO lane")
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`); w.Code != 409 {
		t.Fatalf("duplicate start: %d", w.Code)
	}
	if err := f.a.runScheduledTranslation(scheduler.Task{Type: "translate"}); !errors.Is(err, scheduler.ErrBusy) {
		t.Fatalf("scheduled overlap %v", err)
	}
	if w := translationCall(f.a, f.token, "GET", "/api/admin/translate/status?task_id="+strconv.FormatInt(accepted.TaskID+1, 10), ""); w.Code != 404 {
		t.Fatalf("stale status: %d", w.Code)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/cancel", fmt.Sprintf(`{"task_id":%d}`, accepted.TaskID+1)); w.Code != 409 {
		t.Fatalf("stale cancel: %d", w.Code)
	}
	if !f.a.translationSnapshot().Running {
		t.Fatal("stale cancellation stopped task")
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/cancel", fmt.Sprintf(`{"task_id":%d}`, accepted.TaskID)); w.Code != 200 {
		t.Fatalf("cancel %d", w.Code)
	}
	status := awaitTranslation(t, f.a)
	if !status.Cancelled || status.Failed != 0 || status.Done != 0 || status.FinishedAt == "" || calls.Load() != 1 {
		t.Fatalf("cancel status %+v calls %d", status, calls.Load())
	}
	if f.a.nfoBusyOwner() != "" {
		t.Fatal("cancel did not release NFO gate")
	}
	f.a.translation.item = func(context.Context, store.Movie, scraper.TranslationOptions) (scraper.TranslationResult, error) {
		return scraper.TranslationResult{Changed: true}, nil
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{"library_id":0}`); w.Code != 202 {
		t.Fatalf("restart %d %s", w.Code, w.Body.String())
	}
	status = awaitTranslation(t, f.a)
	if status.TaskID == accepted.TaskID || status.Success != 1 || status.Cancelled {
		t.Fatalf("new generation %+v", status)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/cancel", fmt.Sprintf(`{"task_id":%d}`, accepted.TaskID)); w.Code != 409 {
		t.Fatalf("finished stale cancel %d", w.Code)
	}
}

func TestTranslationFailureRefreshAndSettingsRead(t *testing.T) {
	for _, tc := range []struct {
		name            string
		itemErr         error
		result          scraper.TranslationResult
		failed, skipped int
		refresh         bool
	}{
		{name: "service", itemErr: errors.New("provider failed"), failed: 1},
		{name: "service-timeout", itemErr: context.DeadlineExceeded, failed: 1},
		{name: "committed-refresh", result: scraper.TranslationResult{Changed: true, RefreshError: "db write failed"}, failed: 1, refresh: true},
		{name: "unchanged-refresh", result: scraper.TranslationResult{Changed: false, RefreshError: "db write failed"}, failed: 1, refresh: true},
		{name: "locked", result: scraper.TranslationResult{Skipped: true, Reason: "locked"}, skipped: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTranslationFixture(t)
			f.a.translation.item = func(_ context.Context, _ store.Movie, opts scraper.TranslationOptions) (scraper.TranslationResult, error) {
				if !opts.Refresh || !opts.Title || opts.Plot {
					t.Errorf("default opts %+v", opts)
				}
				return tc.result, tc.itemErr
			}
			w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`)
			if w.Code != 202 {
				t.Fatalf("start %d %s", w.Code, w.Body.String())
			}
			status := awaitTranslation(t, f.a)
			if status.Total != 1 || status.Done != 1 || status.Failed != tc.failed || status.Skipped != tc.skipped || status.IndexRefreshFailed != tc.refresh || (tc.failed > 0 && status.Error == "") {
				t.Fatalf("status %+v", status)
			}
			if tc.failed > 0 {
				copy := f.a.translationSnapshot()
				copy.Failures[0] = "mutated"
				if f.a.translationSnapshot().Failures[0] == "mutated" {
					t.Fatal("status aliases failure slice")
				}
			}
		})
	}
	t.Run("settings-read", func(t *testing.T) {
		f := newTranslationFixture(t)
		if err := f.a.db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.a.prepareTranslation(translationRequest{Fields: []string{"title"}}); err == nil || errors.Is(err, errTranslationDisabled) {
			t.Fatalf("settings failure swallowed: %v", err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		f := newTranslationFixture(t)
		if err := f.a.db.SetSetting(settingTranslateAPIKey, ""); err != nil {
			t.Fatal(err)
		}
		if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`); w.Code != 400 {
			t.Fatalf("disabled %d", w.Code)
		}
	})
}

func TestTranslationCancellationRetainsCommittedRefreshFailure(t *testing.T) {
	f := newTranslationFixture(t)
	entered := make(chan struct{})
	f.a.translation.item = func(ctx context.Context, _ store.Movie, _ scraper.TranslationOptions) (scraper.TranslationResult, error) {
		close(entered)
		<-ctx.Done()
		return scraper.TranslationResult{Changed: true, RefreshError: "committed output refresh failed"}, nil
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`); w.Code != 202 {
		t.Fatal(w.Code)
	}
	<-entered
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/cancel", `{}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	status := awaitTranslation(t, f.a)
	if !status.Cancelled || !status.IndexRefreshFailed || status.Failed != 1 || status.Done != 1 || !strings.Contains(status.Error, errTranslationRefresh.Error()) {
		t.Fatalf("cancellation hid saved-output failure: %+v", status)
	}
}

func TestTranslationRealExecutionAndShutdown(t *testing.T) {
	t.Run("refresh", func(t *testing.T) {
		f := newTranslationFixture(t)
		var calls atomic.Int32
		svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			var req struct {
				Text []string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if len(req.Text) != 2 {
				t.Errorf("texts %v", req.Text)
			}
			_, _ = w.Write([]byte(`{"translations":[{"text":"译标题"},{"text":"译剧情"}]}`))
		}))
		defer svc.Close()
		if err := f.a.db.SetSetting(settingTranslateAPIURL, svc.URL); err != nil {
			t.Fatal(err)
		}
		w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", fmt.Sprintf(`{"movie_ids":[%d],"fields":["title","plot"]}`, f.movie))
		if w.Code != 202 {
			t.Fatalf("start %d %s", w.Code, w.Body.String())
		}
		status := awaitTranslation(t, f.a)
		if status.Success != 1 || status.Failed != 0 || status.IndexRefreshFailed || calls.Load() != 1 {
			t.Fatalf("actual status %+v calls %d", status, calls.Load())
		}
		m, err := f.a.db.Movie(f.movie)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(m.Title, "译标题") || m.Plot != "译剧情" {
			t.Fatalf("index not refreshed %+v", m)
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		f := newTranslationFixture(t)
		entered := make(chan struct{})
		f.a.translation.item = func(ctx context.Context, _ store.Movie, _ scraper.TranslationOptions) (scraper.TranslationResult, error) {
			close(entered)
			<-ctx.Done()
			return scraper.TranslationResult{}, ctx.Err()
		}
		if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`); w.Code != 202 {
			t.Fatal(w.Code)
		}
		<-entered
		f.a.BeginShutdown()
		status := awaitTranslation(t, f.a)
		if !status.Cancelled || f.a.nfoBusyOwner() != "" {
			t.Fatalf("shutdown status %+v", status)
		}
	})
}
