package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/scheduler"
	"emby-go/internal/store"
)

func TestScheduledSpritesStrictParamsAndPersistence(t *testing.T) {
	for _, raw := range []map[string]any{{"library_id": -1}, {"library_id": 1.5}, {"library_id": "1"}, {"library_id": nil}, {"limit": -1}, {"limit": 1.5}, {"limit": 1000001}, {"overwrite": nil}, {"overwrite": 1}, {"movie_ids": []int{1}}, {"interval_seconds": 20}} {
		if _, err := normalizeParams("sprites", raw); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
	f := artworkFixture(t)
	w := translationCall(f.a, f.token, "POST", "/api/admin/scheduled", `{"name":"Sprites","type":"sprites","cron":"0 2 * * *","enabled":false}`)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created scheduledView
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, created.Params, `{"library_id":0,"limit":0,"overwrite":false}`)
	row, err := f.a.db.ScheduledTask(created.ID)
	if err != nil || row.Type != "sprites" || row.Enabled || row.Params != created.Params {
		t.Fatalf("stored %+v %v", row, err)
	}
	request := httptest.NewRequest("PATCH", fmt.Sprintf("/api/admin/scheduled/%d", created.ID), strings.NewReader(fmt.Sprintf(`{"name":"Sprites","type":"sprites","cron":"0 2 * * *","enabled":false,"params":{"library_id":%d,"limit":2,"overwrite":true}}`, f.lib)))
	request.Header.Set("X-Emby-Token", f.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", created.ConfigETag)
	w = httptest.NewRecorder()
	f.a.Handler().ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatalf("update %d %s", w.Code, w.Body.String())
	}
	row, err = f.a.db.ScheduledTask(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, row.Params, fmt.Sprintf(`{"library_id":%d,"limit":2,"overwrite":true}`, f.lib))
	if err = f.a.sched.Reload(); err != nil {
		t.Fatal(err)
	}
	if !f.a.sched.Next(created.ID).IsZero() {
		t.Fatal("disabled sprite task scheduled")
	}
	for _, token := range []string{"", "invalid-token"} {
		if w := translationCall(f.a, token, "POST", "/api/admin/scheduled", `{"name":"x","type":"sprites","cron":"@daily"}`); w.Code != 401 {
			t.Fatalf("unauthorized create %d", w.Code)
		}
	}
}

func TestScheduledSpritesPagedScopeIntervalsAndMetadata(t *testing.T) {
	f := artworkFixture(t)
	before, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	nfo, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 202; i++ {
		_, err = f.a.db.IndexMovie(store.Movie{LibraryID: f.lib, SourcePath: fmt.Sprintf("/fixture/scheduled-%d.strm", i), Title: fmt.Sprint(i), Status: "success"}, 1, time.Now(), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	other, err := f.a.db.AddLibrary("Other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.db.IndexMovie(store.Movie{LibraryID: other.ID, SourcePath: "/fixture/other.strm", Title: "other"}, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.db.IndexMovie(store.Movie{LibraryID: f.lib, SourcePath: "/fixture/not-strm.mp4", Title: "local"}, 1, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SetLibrarySpriteInterval(context.Background(), f.lib, 17); err != nil {
		t.Fatal(err)
	}
	global := f.a.db.Version("g:version")
	visited := map[int64]bool{}
	f.a.sprites.run = func(ctx context.Context, db *store.Store, req artwork.SpriteRequest, opts artwork.SpriteOptions) (artwork.SpriteResult, error) {
		m, err := db.Movie(req.MovieID)
		if err != nil {
			return artwork.SpriteResult{}, err
		}
		if m.LibraryID != f.lib || !strings.HasSuffix(m.SourcePath, ".strm") || req.IntervalMs != 17000 || req.Overwrite {
			t.Errorf("scope/opts %+v %+v", m, req)
		}
		if visited[m.ID] {
			t.Errorf("duplicate %d", m.ID)
		}
		visited[m.ID] = true
		opts.Progress(1, 2)
		return artwork.SpriteResult{Skipped: true}, nil
	}
	if err = f.a.runScheduledTask(context.Background(), scheduler.Task{Type: "sprites", Params: map[string]any{"library_id": f.lib}}); err != nil {
		t.Fatal(err)
	}
	s := spriteJoin(t, f.a)
	if s.Running || s.Total != 203 || s.Done != 203 || s.Skipped != 203 || len(visited) != 203 || len(s.MovieIDs) != 0 || s.TaskID == 0 || s.Error != "" {
		t.Fatalf("status %+v visited %d", s, len(visited))
	}
	after, _ := f.a.db.Movie(f.movie)
	afterNFO, _ := os.ReadFile(before.NFOPath)
	if !reflect.DeepEqual(before, after) || string(nfo) != string(afterNFO) || global != f.a.db.Version("g:version") {
		t.Fatal("scheduled generation touched metadata/index")
	}
	// Explicit zero scope and limit traverse every enabled STRM, including the
	// second library, with its own persisted density and no fixed-ID-list cap.
	if err = f.a.db.SetLibrarySpriteInterval(context.Background(), other.ID, 33); err != nil {
		t.Fatal(err)
	}
	visited = map[int64]bool{}
	f.a.sprites.run = func(_ context.Context, db *store.Store, r artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		m, err := db.Movie(r.MovieID)
		if err != nil {
			return artwork.SpriteResult{}, err
		}
		want := int64(17000)
		if m.LibraryID == other.ID {
			want = 33000
		}
		if r.IntervalMs != want || r.Overwrite || !strings.HasSuffix(m.SourcePath, ".strm") || visited[m.ID] {
			t.Errorf("all-library options %+v movie %+v", r, m)
		}
		visited[m.ID] = true
		return artwork.SpriteResult{Skipped: true}, nil
	}
	if err = f.a.runScheduledTask(context.Background(), scheduler.Task{Type: "sprites", Params: map[string]any{"library_id": 0, "limit": 0}}); err != nil {
		t.Fatal(err)
	}
	if s = spriteJoin(t, f.a); s.Total != 204 || s.Done != 204 || len(visited) != 204 {
		t.Fatalf("all-library %+v visited %d", s, len(visited))
	}
	// Re-run uses the same generator call site but a bounded target limit and explicit overwrite.
	count := 0
	f.a.sprites.run = func(_ context.Context, _ *store.Store, r artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		count++
		if !r.Overwrite {
			t.Error("overwrite lost")
		}
		return artwork.SpriteResult{Changed: true}, nil
	}
	if err = f.a.runScheduledTask(context.Background(), scheduler.Task{Type: "sprites", Params: map[string]any{"library_id": f.lib, "limit": 2, "overwrite": true}}); err != nil {
		t.Fatal(err)
	}
	if s = spriteJoin(t, f.a); s.Total != 2 || s.Done != 2 || s.Success != 2 || count != 2 {
		t.Fatalf("limited %+v count %d", s, count)
	}
}

func TestScheduledSpritesSchedulerOutcomesCancelAndSharedGate(t *testing.T) {
	f := artworkFixture(t)
	row, err := f.a.db.CreateScheduledTask(store.ScheduledTask{Name: "sprite outcome", Type: "sprites", Cron: "@daily", Params: fmt.Sprintf(`{"library_id":%d,"limit":1}`, f.lib)})
	if err != nil {
		t.Fatal(err)
	}
	if !f.a.claimNFO("scrape") {
		t.Fatal("claim")
	}
	if err = f.a.sched.RunNow(row.ID); err != nil {
		t.Fatal(err)
	}
	f.a.releaseNFO("scrape")
	row, _ = f.a.db.ScheduledTask(row.ID)
	if row.LastStatus != scheduler.StatusSkipped {
		t.Fatalf("busy result %+v", row)
	}
	f.a.sprites.run = func(context.Context, *store.Store, artwork.SpriteRequest, artwork.SpriteOptions) (artwork.SpriteResult, error) {
		return artwork.SpriteResult{}, errors.New("canary-secret-url")
	}
	if err = f.a.sched.RunNow(row.ID); err != nil {
		t.Fatal(err)
	}
	row, _ = f.a.db.ScheduledTask(row.ID)
	if row.LastStatus != scheduler.StatusFailed || strings.Contains(row.LastMessage, "canary") || row.LastMessage == "" {
		t.Fatalf("failed result %+v", row)
	}
	f.a.sprites.run = func(context.Context, *store.Store, artwork.SpriteRequest, artwork.SpriteOptions) (artwork.SpriteResult, error) {
		return artwork.SpriteResult{Skipped: true}, nil
	}
	if err = f.a.sched.RunNow(row.ID); err != nil {
		t.Fatal(err)
	}
	row, _ = f.a.db.ScheduledTask(row.ID)
	if row.LastStatus != scheduler.StatusSuccess {
		t.Fatalf("success result %+v", row)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	f.a.sprites.run = func(ctx context.Context, _ *store.Store, _ artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		close(entered)
		<-ctx.Done()
		return artwork.SpriteResult{}, ctx.Err()
	}
	go func() {
		done <- f.a.runScheduledSprites(context.Background(), scheduler.Task{Type: "sprites", Params: map[string]any{"library_id": f.lib, "limit": 1}})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("not entered")
	}
	s := spriteJoinStatus(f.a)
	if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", fmt.Sprintf(`{"movie_ids":[%d]}`, f.movie)); w.Code != 409 {
		t.Errorf("manual gate %d", w.Code)
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/cancel", fmt.Sprintf(`{"task_id":%d,"instance_id":%q}`, s.TaskID, s.InstanceID))
	if w.Code != 200 {
		t.Errorf("cancel %d %s", w.Code, w.Body.String())
	}
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel result %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not canceled")
	}
	if s = spriteJoinStatus(f.a); !s.Cancelled || s.Running {
		t.Fatalf("cancelled %+v", s)
	}
	if !f.a.claimNFO("scrape") {
		t.Fatal("gate leaked")
	}
	f.a.releaseNFO("scrape")
	// An independently canceled scheduler context cancels the shared process context.
	entered = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- f.a.runScheduledSprites(ctx, scheduler.Task{Type: "sprites", Params: map[string]any{"limit": 1}})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parent not entered")
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancel not propagated")
	}
}

func TestScheduledSpritesRejectStaleAuthorityAfterBody(t *testing.T) {
	for _, mode := range []string{"create", "update"} {
		t.Run(mode, func(t *testing.T) {
			f := artworkFixture(t)
			method, path := "POST", "/api/admin/scheduled"
			var original store.ScheduledTask
			var err error
			if mode == "update" {
				original, err = f.a.db.CreateScheduledTask(store.ScheduledTask{Name: "unchanged", Type: "sprites", Cron: "@daily", Params: `{"library_id":0,"limit":0,"overwrite":false}`})
				if err != nil {
					t.Fatal(err)
				}
				method, path = "PATCH", fmt.Sprintf("/api/admin/scheduled/%d", original.ID)
			}
			raw := `{"name":"must not save","type":"sprites","cron":"@daily","params":{"overwrite":true}}`
			read := false
			body := &passwordIdentityReader{Reader: strings.NewReader(raw), beforeRead: func() {
				read = true
				if err := f.a.db.DeleteAccessToken(f.token); err != nil {
					t.Fatal(err)
				}
			}}
			req := httptest.NewRequest(method, path, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Emby-Token", f.token)
			if mode == "update" {
				req.Header.Set("If-Match", scheduledConfigETag(original))
			}
			w := httptest.NewRecorder()
			f.a.Handler().ServeHTTP(w, req)
			// 撤销凭据在读取请求体之后才被复核，与 artwork/sprites 的管理接口保持一致：
			// 已通过路由层鉴权的会话现在返回 403 ADMIN_REQUIRED，而不是更早的 401。
			if !read || w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"ADMIN_REQUIRED"`) {
				t.Fatalf("stale %d %s read=%t", w.Code, w.Body.String(), read)
			}
			rows, err := f.a.db.ScheduledTasks()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "create" && len(rows) != 0 || mode == "update" && (len(rows) != 1 || !reflect.DeepEqual(rows[0], original)) {
				t.Fatalf("stale authority mutated %+v", rows)
			}
		})
	}
}

func TestScheduledSpritesProtectsPublishedGenerationAndShutdown(t *testing.T) {
	f := artworkFixture(t)
	manifest := spriteSeed(t, f)
	before, err := f.a.db.Movie(f.movie)
	if err != nil {
		t.Fatal(err)
	}
	nfo, err := os.ReadFile(before.NFOPath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.a.db.SpriteContext(context.Background(), f.movie)
	if err != nil {
		t.Fatal(err)
	}
	global := f.a.db.Version("g:version")
	// Actual production GenerateSprite, not the execution seam: an old valid
	// preview survives a newly configured density unless overwrite was explicit.
	if err = f.a.db.SetLibrarySpriteInterval(context.Background(), f.lib, 60); err != nil {
		t.Fatal(err)
	}
	if err = f.a.runScheduledSprites(context.Background(), scheduler.Task{Type: "sprites", Params: map[string]any{"library_id": f.lib}}); err != nil {
		t.Fatal(err)
	}
	status := spriteJoinStatus(f.a)
	afterState, err := f.a.db.SpriteContext(context.Background(), f.movie)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := f.a.db.Movie(f.movie)
	afterNFO, _ := os.ReadFile(before.NFOPath)
	if status.Skipped != 1 || status.Success != 0 || !reflect.DeepEqual(state, afterState) || !reflect.DeepEqual(before, after) || string(nfo) != string(afterNFO) || global != f.a.db.Version("g:version") {
		t.Fatalf("protected state changed %+v %+v", status, manifest)
	}
	entered := make(chan struct{})
	f.a.sprites.run = func(ctx context.Context, _ *store.Store, _ artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		close(entered)
		<-ctx.Done()
		return artwork.SpriteResult{}, ctx.Err()
	}
	row, err := f.a.db.CreateScheduledTask(store.ScheduledTask{Name: "shutdown sprites", Type: "sprites", Cron: "@daily", Params: fmt.Sprintf(`{"library_id":%d}`, f.lib)})
	if err != nil {
		t.Fatal(err)
	}
	if w := translationCall(f.a, f.token, "POST", fmt.Sprintf("/api/admin/scheduled/%d/run", row.ID), `{}`); w.Code != 202 {
		t.Fatalf("run %d %s", w.Code, w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled run not entered")
	}
	f.a.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = f.a.WaitBackground(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = f.a.db.ScheduledTask(row.ID)
	if err != nil || row.LastStatus != scheduler.StatusFailed || !spriteJoinStatus(f.a).Cancelled {
		t.Fatalf("shutdown result %+v %v", row, err)
	}
}

func spriteJoinStatus(a *App) spriteStatus {
	a.sprites.mu.RLock()
	defer a.sprites.mu.RUnlock()
	return a.sprites.status
}
