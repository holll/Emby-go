package server

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"emby-go/internal/nfo"
)

func TestMetadataEditAndRereadRejectConcurrentScan(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), "<movie><title>Old</title></movie>")
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	if !app.beginScan(1) {
		t.Fatal("无法占用扫描通道")
	}
	for _, kind := range []struct{ method, suffix, body string }{
		{"PUT", "", `{"title":"New"}`}, {"POST", "/reread", ""},
	} {
		resp, _ := embyCall(t, ts, kind.method, "/api/admin/items/"+strconv.FormatInt(id, 10)+kind.suffix, token, kind.body)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s应拒绝并发: %d", kind.method, resp.StatusCode)
		}
	}
	app.endScan(nil)
	meta, err := nfo.Read(filepath.Join(media, "ABF-018.nfo"))
	if err != nil || meta.Title != "Old" {
		t.Fatalf("冲突请求不得改写NFO: %+v, %v", meta, err)
	}
	if !app.claimNFO("edit") {
		t.Fatal("编辑通道应释放")
	}
	if app.claimNFO("edit") {
		t.Error("独立编辑不得并发重入")
	}
	app.releaseNFO("edit")
}

func TestMetadataLocksRoundTripAndReindex(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), "<movie><title>Old</title><lockdata>true</lockdata><lockedfields>Name|FutureField</lockedfields></movie>")
	ts, token := detailServer(t, app)
	if _, err := app.db.AddLibrary("L", media); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	id, err := app.db.MovieIDByPath(filepath.Join(media, "ABF-018.strm"))
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/items/" + strconv.FormatInt(id, 10)
	resp, _ := embyCall(t, ts, "PUT", path, token, `{"title":"Manual","locked_fields":["Name","Genres","FutureField"],"lock_data":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("编辑: %d", resp.StatusCode)
	}
	meta, err := nfo.Read(filepath.Join(media, "ABF-018.nfo"))
	if err != nil || meta.Title != "Manual" || meta.LockedFields != "Name|Genres|FutureField" || meta.LockData != "false" {
		t.Fatalf("人工编辑及锁配置: %+v, %v", meta, err)
	}
	scanOnce(t, ts, token)
	resp, detail := embyCall(t, ts, "GET", path+"/detail", token, "")
	if resp.StatusCode != http.StatusOK || detail["lock_data"] != false {
		t.Fatalf("详情: %d %+v", resp.StatusCode, detail)
	}
	fields, ok := detail["locked_fields"].([]any)
	if !ok || len(fields) != 3 {
		t.Fatalf("重建后锁丢失: %+v", detail)
	}
	resp, _ = embyCall(t, ts, "PUT", path, token, `{"locked_fields":[],"lock_data":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("解锁: %d", resp.StatusCode)
	}
	meta, err = nfo.Read(filepath.Join(media, "ABF-018.nfo"))
	if err != nil || meta.LockedFields != "" {
		t.Fatalf("解锁未持久化: %+v, %v", meta, err)
	}
}
