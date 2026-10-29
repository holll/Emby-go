package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/scheduler"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

func TestScheduledTranslationParamsAndETag(t *testing.T) {
	for _, raw := range []map[string]any{
		{"fields": []string{}}, {"fields": nil}, {"fields": []string{"summary"}},
		{"movie_ids": []int{1}}, {"library_id": -1}, {"limit": -1}, {"limit": 1.5}, {"limit": maxTranslationLimit + 1},
	} {
		if _, err := normalizeParams("translate", raw); err == nil {
			t.Fatalf("accepted invalid %#v", raw)
		}
	}
	for _, raw := range []map[string]any{nil, {"library_id": 0}, {"fields": []string{"plot", "title", "plot"}, "limit": 0, "opaque": map[string]any{"keep": true}}} {
		value, err := normalizeParams("translate", raw)
		if err != nil {
			t.Fatalf("valid %#v: %v", raw, err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(value), &got); err != nil {
			t.Fatal(err)
		}
		if got["fields"] == nil {
			t.Fatalf("default fields absent %s", value)
		}
		if _, present := raw["opaque"]; present && got["opaque"] == nil {
			t.Fatal("hidden parameter lost")
		}
	}
	f := newTranslationFixture(t)
	body := `{"name":"Translate","type":"translate","cron":"0 2 * * *","enabled":false,"params":{"library_id":0,"fields":["plot"],"limit":3,"future":{"flag":true}}}`
	w := translationCall(f.a, f.token, "POST", "/api/admin/scheduled", body)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created scheduledView
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ConfigETag == "" || !strings.Contains(created.Params, `"future"`) || !strings.Contains(created.Params, `"plot"`) {
		t.Fatalf("created %+v", created)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/scheduled/"+strconv.FormatInt(created.ID, 10), strings.NewReader(body))
	req.Header.Set("X-Emby-Token", f.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", created.ConfigETag)
	response := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("update %d %s", response.Code, response.Body.String())
	}
	row, err := f.a.db.ScheduledTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Params != created.Params {
		t.Fatalf("opaque params changed %s => %s", created.Params, row.Params)
	}
	// A supported invalid field is rejected on every run, including rows created
	// before the new admin validator or edited by other tools.
	if err := f.a.runScheduledTranslation(scheduler.Task{Type: "translate", Params: map[string]any{"fields": []any{}}}); err == nil {
		t.Fatal("runtime accepted invalid fields")
	}
}

func TestScheduledTranslationExecutionScopeProgressAndFailure(t *testing.T) {
	f := newTranslationFixture(t)
	source, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	for i := range translationBatchSize + 3 {
		status := "pending"
		if i == translationBatchSize+2 {
			status = "manual"
		}
		_, err := f.a.db.IndexMovie(store.Movie{LibraryID: f.lib, SourcePath: fmt.Sprintf("/fixture/translation-%d.strm", i), Title: fmt.Sprint(i), Status: status}, 1, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	other, err := f.a.db.AddLibrary("Other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.db.IndexMovie(store.Movie{LibraryID: other.ID, SourcePath: "/fixture/other.strm", Title: "other", Status: "success"}, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	var visited []int64
	f.a.translation.item = func(_ context.Context, m store.Movie, opts scraper.TranslationOptions) (scraper.TranslationResult, error) {
		visited = append(visited, m.ID)
		if !opts.Refresh || !opts.Plot || opts.Title {
			t.Errorf("scheduled opts %+v", opts)
		}
		if m.LibraryID != f.lib || !m.IsVisible() {
			t.Errorf("scope escaped %+v", m)
		}
		return scraper.TranslationResult{Changed: true}, nil
	}
	params := map[string]any{"library_id": f.lib, "fields": []any{"plot"}, "opaque": "keep"}
	if err := f.a.runScheduledTask(context.Background(), scheduler.Task{Type: "translate", Params: params}); err != nil {
		t.Fatal(err)
	}
	status := f.a.translationSnapshot()
	if status.Total != 2 || status.Done != 2 || status.Success != 2 || status.Running || len(visited) != 2 || visited[0] != source.ID {
		t.Fatalf("cursor scope %+v visited %v", status, visited)
	}
	visited = nil
	params["limit"] = 1
	if err := f.a.runScheduledTranslation(scheduler.Task{Type: "translate", Params: params}); err != nil {
		t.Fatal(err)
	}
	status = f.a.translationSnapshot()
	if status.Total != 1 || status.Done != 1 || len(visited) != 1 {
		t.Fatalf("limit status %+v visited %v", status, visited)
	}
	f.a.translation.item = func(context.Context, store.Movie, scraper.TranslationOptions) (scraper.TranslationResult, error) {
		return scraper.TranslationResult{RefreshError: "index unavailable"}, nil
	}
	if err := f.a.runScheduledTranslation(scheduler.Task{Type: "translate", Params: params}); err == nil || !errors.Is(err, errTranslationRefresh) {
		t.Fatalf("refresh failure swallowed %v", err)
	}
	if status = f.a.translationSnapshot(); status.Failed != 1 || !status.IndexRefreshFailed || status.Error == "" {
		t.Fatalf("failure %+v", status)
	}
	// Explicit all-library zero is accepted at execution as well as persistence.
	f.a.translation.item = func(context.Context, store.Movie, scraper.TranslationOptions) (scraper.TranslationResult, error) {
		return scraper.TranslationResult{Skipped: true}, nil
	}
	if err := f.a.runScheduledTranslation(scheduler.Task{Type: "translate", Params: map[string]any{"library_id": 0, "limit": 1}}); err != nil {
		t.Fatal(err)
	}
}
