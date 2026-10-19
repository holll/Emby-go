package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// D06：所有可见变更都必须推进 g:version，缓存键随之换域，因此写后第一次读
// 就能看到自己的写入（单进程内即 ≤ 500ms，远小于窗口）。
func TestReadYourWritesAfterMutations(t *testing.T) {
	f := newAdminDeleteFixture(t)
	h := f.a.Handler()
	const token = "fixture-admin-delete"
	id := f.victim
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Emby-Token", token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	read := func(path string) map[string]any {
		w := call(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s body=%s", path, w.Body.String())
		}
		return v
	}
	detailPath := fmt.Sprintf("/Users/1/Items/%d", id)
	listPath := "/Users/1/Items?ParentId=" + fmt.Sprint(f.lib)
	userDataOf := func(v map[string]any) map[string]any {
		data, _ := v["UserData"].(map[string]any)
		if data == nil {
			t.Fatalf("missing UserData: %v", v)
		}
		return data
	}
	bumped := func(before, what string) {
		if f.a.db.Version("g:version") == before {
			t.Fatalf("%s did not bump global version", what)
		}
	}

	// 预热详情与列表缓存，确保后续断言只可能命中新版本键。
	read(detailPath)
	read(listPath)

	version := f.a.db.Version("g:version")
	if w := call(http.MethodPost, fmt.Sprintf("/Users/1/FavoriteItems/%d", id), ""); w.Code != http.StatusOK {
		t.Fatalf("favorite status=%d body=%s", w.Code, w.Body.String())
	}
	bumped(version, "favorite")
	if userDataOf(read(detailPath))["IsFavorite"] != true {
		t.Fatal("favorite not visible on next read")
	}

	version = f.a.db.Version("g:version")
	if w := call(http.MethodPost, fmt.Sprintf("/Users/1/Items/%d/Rating?Likes=true", id), ""); w.Code != http.StatusOK {
		t.Fatalf("rating status=%d body=%s", w.Code, w.Body.String())
	}
	bumped(version, "rating")
	// 详情 UserData 不暴露 Likes；写入本身由 userdata_atomic_test 断言，
	// 这里只验证版本推进后详情仍可读且不会回退到旧版本缓存。
	if data, err := f.a.db.DataUser(1, id); err != nil || data.Likes != 1 {
		t.Fatalf("rating write lost: %+v err=%v", data, err)
	}
	read(detailPath)

	version = f.a.db.Version("g:version")
	if w := call(http.MethodPost, fmt.Sprintf("/Users/1/PlayedItems/%d", id), ""); w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("played status=%d body=%s", w.Code, w.Body.String())
	}
	bumped(version, "played")
	if userDataOf(read(detailPath))["Played"] != true {
		t.Fatal("played flag not visible on next read")
	}

	version = f.a.db.Version("g:version")
	if w := call(http.MethodPatch, fmt.Sprintf("/api/admin/items/%d", id), `{"title":"rw-title"}`); w.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", w.Code, w.Body.String())
	}
	bumped(version, "patch")
	if got := read(detailPath)["Name"]; got != "rw-title" {
		t.Fatalf("edited title not visible on next read: %v", got)
	}

	version = f.a.db.Version("g:version")
	if w := call(http.MethodDelete, fmt.Sprintf("/api/admin/items/%d", id), ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	bumped(version, "delete")
	list := read(listPath)
	for _, raw := range list["Items"].([]any) {
		if item, _ := raw.(map[string]any); item["Id"] == fmt.Sprint(id) {
			t.Fatal("deleted item still visible from cached list")
		}
	}
}

// D06：没有版本段的缓存条目一律视为脏数据，永远不参与命中。
func TestUnversionedCacheEntriesAreIgnored(t *testing.T) {
	f := newAdminDeleteFixture(t)
	before, ok := f.a.cachedMovie(f.victim)
	if !ok {
		t.Fatal("fixture movie missing")
	}
	f.a.imgMeta.Set("m:"+fmt.Sprint(f.victim), []byte(`{"Title":"stale-unversioned"}`), 0)
	got, ok := f.a.cachedMovie(f.victim)
	if !ok || got.Title != before.Title {
		t.Fatalf("unversioned entry served: %+v ok=%v", got, ok)
	}
}
