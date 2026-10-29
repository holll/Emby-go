package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/store"
)

func multiuserLogin(t *testing.T, h http.Handler, username, password string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"Username": username, "Pw": password})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	return w.Code, got
}

func TestMultiUserLoginAndAdminBoundary(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "multi.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.db.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	viewer, err := a.db.CreateUser("viewer", "viewer-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()

	status, login := multiuserLogin(t, h, "viewer", "viewer-password-1")
	if status != http.StatusOK {
		t.Fatalf("viewer login=%d body=%v", status, login)
	}
	token, _ := login["AccessToken"].(string)
	if token == "" {
		t.Fatalf("no token: %v", login)
	}
	user, _ := login["User"].(map[string]any)
	if user["Id"] != strconv.FormatInt(viewer.ID, 10) {
		t.Fatalf("user id=%v want %d", user["Id"], viewer.ID)
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy["IsAdministrator"] != false {
		t.Fatalf("viewer policy=%v", policy)
	}

	// 非管理员不能访问管理面。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("X-Emby-Token", token)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer admin status=%d body=%s", w.Code, w.Body.String())
	}

	// 管理员登录后仍可访问管理面。
	status, adminLogin := multiuserLogin(t, h, "admin", "admin-password-1")
	if status != http.StatusOK {
		t.Fatalf("admin login=%d body=%v", status, adminLogin)
	}
	adminToken, _ := adminLogin["AccessToken"].(string)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("X-Emby-Token", adminToken)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin settings status=%d body=%s", w.Code, w.Body.String())
	}

	// 未知用户 id 仍返回 404，而不是把任意 id 当管理员。
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/Users/9999", nil)
	req.Header.Set("X-Emby-Token", token)
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown user status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUserDataIsolationByUser(t *testing.T) {
	root := t.TempDir()
	a, err := newApp(config.Config{DBPath: filepath.Join(root, "iso.db")}, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.db.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	viewer, err := a.db.CreateUser("viewer", "viewer-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.db.CreateUser("other", "other-password-1", false)
	if err != nil {
		t.Fatal(err)
	}
	lib, err := a.db.AddLibrary("users", root)
	if err != nil {
		t.Fatal(err)
	}
	movieID, err := a.db.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "a.strm"), SourceProtocol: "https", Status: "success"}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.db.SetIndexedFavoriteDataForUser(viewer.ID, movieID, true); err != nil {
		t.Fatal(err)
	}
	if d, err := a.db.DataUser(viewer.ID, movieID); err != nil || !d.IsFavorite {
		t.Fatalf("viewer favorite=%+v err=%v", d, err)
	}
	for _, uid := range []int64{other.ID, 1} {
		if d, err := a.db.DataUser(uid, movieID); err != nil || d.IsFavorite {
			t.Fatalf("user %d leaked favorite: %+v err=%v", uid, d, err)
		}
	}

	for range 2 {
		if _, err := a.db.RecordPlaybackEvent(store.PlaybackEvent{
			SessionKey: "viewer-session", UserID: viewer.ID, MovieID: movieID,
			Kind: store.PlaybackStopped, PositionTicks: 100, Now: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if d, _ := a.db.DataUser(viewer.ID, movieID); d.PlayCount != 1 || d.PositionTicks != 100 {
		t.Fatalf("viewer playback=%+v", d)
	}
	if d, _ := a.db.DataUser(other.ID, movieID); d.PlayCount != 0 {
		t.Fatalf("other playback leaked: %+v", d)
	}
}

func TestLoginAuditRecordsSuccessAndFailure(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "audit.db")}, cache.NewMemory(32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.db.InitializeAdministrator("admin", "admin-password-1"); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	if status, _ := multiuserLogin(t, h, "admin", "wrong-password"); status != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d", status)
	}
	if status, _ := multiuserLogin(t, h, "admin", "admin-password-1"); status != http.StatusOK {
		t.Fatalf("login status=%d", status)
	}
	events, err := a.db.LoginEvents(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events=%d %+v", len(events), events)
	}
	if events[0].Success != true || events[0].Name != "admin" {
		t.Fatalf("latest=%+v", events[0])
	}
	if events[1].Success || events[1].Name != "admin" {
		t.Fatalf("failure=%+v", events[1])
	}
}
