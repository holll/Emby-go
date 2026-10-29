package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"emby-go/internal/artwork"
	"emby-go/internal/store"
)

// spriteSeed exercises the actual generator/publication with a deterministic
// bounded media executable. Real FFmpeg integration is tested by artwork.
func spriteSeed(t *testing.T, f translationFixture) artwork.SpriteManifest {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX deterministic media fixture")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 80, 255})
		}
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '20\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s' '" + base64.StdEncoding.EncodeToString(data.Bytes()) + "' | base64 -d\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.a.artwork.lookPath = func(s string) (string, error) {
		if s == "ffmpeg" {
			return ffmpeg, nil
		}
		return probe, nil
	}
	opts, err := f.a.spriteOptions()
	if err != nil {
		t.Fatal(err)
	}
	result, err := artwork.GenerateSprite(context.Background(), f.a.db, artwork.SpriteRequest{MovieID: f.movie, IntervalMs: 10000}, opts)
	if err != nil || !result.Changed {
		t.Fatalf("seed: %+v %v", result, err)
	}
	return result.Manifest
}
func spriteJoin(t *testing.T, a *App) spriteStatus {
	t.Helper()
	done := make(chan struct{})
	go func() { a.workerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sprite worker did not finish")
	}
	a.sprites.mu.RLock()
	defer a.sprites.mu.RUnlock()
	return a.sprites.status
}
func TestSpritesPermissionsStrictInputsAndIdentity(t *testing.T) {
	f := artworkFixture(t)
	u, err := f.a.db.CreateUser("sprite-reader", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("sprite-reader", u.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ method, path string }{{"GET", "capabilities"}, {"GET", "status"}, {"POST", "run"}, {"POST", "cancel"}} {
		for token, want := range map[string]int{"": 401, "sprite-reader": 403} {
			w := translationCall(f.a, token, r.method, "/api/admin/sprites/"+r.path, `{}`)
			if w.Code != want {
				t.Fatalf("%s %s: %d", r.method, r.path, w.Code)
			}
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"movie_ids":null}`, `{"movie_ids":[1,1]}`, `{"movie_ids":[0]}`, `{"movie_ids":[1],"url":"https://bad"}`, `{"Movie_IDS":[1]}`, `{"movie_ids":[1],"movie_ids":[2]}`, `{"movie_ids":[1],"overwrite":null}`, `{"movie_ids":[1],"interval_seconds":0}`, `{"movie_ids":[1],"interval_seconds":3601}`, `{"movie_ids":[1],"interval_seconds":1.5}`, strings.Repeat(" ", 65537) + `{}`} {
		if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", body); w.Code != 400 {
			t.Fatalf("body %q => %d", body[:min(len(body), 100)], w.Code)
		}
	}
	entered := make(chan struct{})
	f.a.sprites.run = func(ctx context.Context, _ *store.Store, _ artwork.SpriteRequest, o artwork.SpriteOptions) (artwork.SpriteResult, error) {
		o.Progress(1, 2)
		close(entered)
		<-ctx.Done()
		return artwork.SpriteResult{}, ctx.Err()
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", fmt.Sprintf(`{"movie_ids":[%d]}`, f.movie))
	if w.Code != 202 {
		t.Fatalf("run %d %s", w.Code, w.Body.String())
	}
	<-entered
	var s spriteStatus
	if err = json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", fmt.Sprintf(`{"movie_ids":[%d]}`, f.movie)); w.Code != 409 {
		t.Fatalf("duplicate %d", w.Code)
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/artwork/run", artworkBody(f.movie)); w.Code != 409 {
		t.Fatalf("artwork gate %d", w.Code)
	}
	latest := translationCall(f.a, f.token, "GET", "/api/admin/sprites/status", "")
	var got spriteStatus
	if json.Unmarshal(latest.Body.Bytes(), &got) != nil || got.TaskID != s.TaskID || got.InstanceID != s.InstanceID || got.FramesDone != 1 || !reflect.DeepEqual(got.MovieIDs, []int64{f.movie}) {
		t.Fatalf("latest %s", latest.Body.String())
	}
	stale := fmt.Sprintf(`{"task_id":%d,"instance_id":"old-instance"}`, s.TaskID)
	if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/cancel", stale); w.Code != 404 {
		t.Fatalf("stale %d", w.Code)
	}
	for _, q := range []string{"?task_id=", "?task_id=0", "?task_id=1", "?task_id=1&task_id=1", "?instance_id=x"} {
		if w := translationCall(f.a, f.token, "GET", "/api/admin/sprites/status"+q, ""); w.Code != 400 {
			t.Fatalf("status %s: %d", q, w.Code)
		}
	}
	body := fmt.Sprintf(`{"task_id":%d,"instance_id":%q}`, s.TaskID, s.InstanceID)
	if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/cancel", body); w.Code != 200 {
		t.Fatalf("cancel %d", w.Code)
	}
	if s := spriteJoin(t, f.a); s.Running || !s.Cancelled {
		t.Fatalf("terminal %+v", s)
	}
	if !f.a.claimNFO("sprites") {
		t.Fatal("gate leaked")
	}
	f.a.releaseNFO("sprites")
}
func TestSpritesNativeProtocolWebImagesAndAccess(t *testing.T) {
	f := artworkFixture(t)
	before, _ := f.a.db.Movie(f.movie)
	nfo, _ := os.ReadFile(before.NFOPath)
	global := f.a.db.Version("g:version")
	m := spriteSeed(t, f)
	after, _ := f.a.db.Movie(f.movie)
	afterNFO, _ := os.ReadFile(before.NFOPath)
	if !reflect.DeepEqual(before, after) || !bytes.Equal(nfo, afterNFO) || global != f.a.db.Version("g:version") {
		t.Fatal("sprite generation changed metadata or cover namespace")
	}
	base := fmt.Sprintf("/Items/%d", f.movie)
	for _, prefix := range []string{"", "/emby"} {
		w := translationCall(f.a, f.token, "GET", prefix+base+"/ThumbnailSet?Width=320", "")
		var dto struct {
			AspectRatio float64
			Thumbnails  []struct {
				PositionTicks int64
				ImageTag      string
			}
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &dto) != nil || len(dto.Thumbnails) != 2 || dto.Thumbnails[1].PositionTicks != 100000000 || dto.Thumbnails[0].ImageTag != m.Generation {
			t.Fatalf("set %d %s", w.Code, w.Body.String())
		}
		frame := translationCall(f.a, f.token, "GET", prefix+base+"/Images/Thumbnail?PositionTicks=100000000&tag="+m.Generation+fmt.Sprintf("&MediaSourceId=mediasource_%d&maxWidth=320", f.movie), "")
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame.Body.Bytes()))
		if frame.Code != 200 || err != nil || cfg.Width != 300 || cfg.Height != 169 || frame.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("frame %d %v %+v", frame.Code, err, cfg)
		}
	}
	w := translationCall(f.a, f.token, "GET", fmt.Sprintf("/api/items/%d/sprites", f.movie), "")
	if w.Code != 200 || strings.Contains(w.Body.String(), before.SourcePath) || strings.Contains(w.Body.String(), "source_hash") {
		t.Fatalf("web %d %s", w.Code, w.Body.String())
	}
	var web struct {
		Sheets []struct {
			URL string `json:"url"`
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &web) != nil || len(web.Sheets) != 1 {
		t.Fatal(w.Body.String())
	}
	sheet := translationCall(f.a, f.token, "GET", web.Sheets[0].URL, "")
	if sheet.Code != 200 {
		t.Fatalf("sheet %d %s", sheet.Code, sheet.Body.String())
	}
	bifPath := fmt.Sprintf("/Videos/%d/index.bif?Width=320", f.movie)
	bif := translationCall(f.a, f.token, "GET", bifPath, "")
	if bif.Code != 200 || !bytes.Equal(bif.Body.Bytes()[:8], []byte{0x89, 'B', 'I', 'F', 13, 10, 26, 10}) || binary.LittleEndian.Uint32(bif.Body.Bytes()[12:16]) != 2 {
		t.Fatalf("bif %d", bif.Code)
	}
	req := httptest.NewRequest("GET", bifPath, nil)
	req.Header.Set("X-Emby-Token", f.token)
	req.Header.Set("Range", "bytes=0-63")
	r := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(r, req)
	if r.Code != 206 || r.Body.Len() != 64 {
		t.Fatalf("range %d len%d", r.Code, r.Body.Len())
	}
	head := translationCall(f.a, f.token, "HEAD", bifPath, "")
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("head %d len%d", head.Code, head.Body.Len())
	}
	u, err := f.a.db.CreateUser("sprites-denied", "password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.db.SaveAccessTokenForUser("sprites-denied", u.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base + "/ThumbnailSet", base + "/Images/Thumbnail?PositionTicks=0", bifPath, fmt.Sprintf("/api/items/%d/sprites", f.movie), web.Sheets[0].URL} {
		for token, want := range map[string]int{"": 401, "sprites-denied": 404} {
			if w := translationCall(f.a, token, "GET", path, ""); w.Code != want {
				t.Fatalf("permission %s %s: %d", token, path, w.Code)
			}
		}
	}
	for _, query := range []string{"?PositionTicks=-1", "?PositionTicks=9223372036854775808", "?PositionTicks=200000000", "?PositionTicks=0&tag=stale", "?PositionTicks=0&MediaSourceId=mediasource_other"} {
		if w := translationCall(f.a, f.token, "GET", base+"/Images/Thumbnail"+query, ""); w.Code == 200 {
			t.Fatalf("invalid query accepted %s", query)
		}
	}
}
func TestSpritesShutdownCancelsWorker(t *testing.T) {
	f := artworkFixture(t)
	entered := make(chan struct{})
	f.a.sprites.run = func(ctx context.Context, _ *store.Store, _ artwork.SpriteRequest, _ artwork.SpriteOptions) (artwork.SpriteResult, error) {
		close(entered)
		<-ctx.Done()
		return artwork.SpriteResult{}, ctx.Err()
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/sprites/run", fmt.Sprintf(`{"movie_ids":[%d]}`, f.movie)); w.Code != 202 {
		t.Fatal(w.Code)
	}
	<-entered
	f.a.BeginShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.a.WaitBackground(ctx); err != nil {
		t.Fatal(err)
	}
	if !spriteJoin(t, f.a).Cancelled {
		t.Fatal("shutdown not reflected")
	}
}
