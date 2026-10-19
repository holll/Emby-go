package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"path/filepath"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// 凭据有效但账户已删除时必须 401，不能借用 administrators 旧行或空库管理员身份。
func TestAuthFailsClosedWhenTokenUserIsGone(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "closed.db")}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	seedOwner(t, a)
	const token = "orphan-session"
	if err := a.db.SaveAccessTokenForUser(token, 7, time.Time{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.Header.Set("X-Emby-Token", token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("orphan token status=%d body=%s", w.Code, w.Body.String())
	}
}

// 旧 administrators 行即使还在，也不能接受已从 users 改掉的旧口令。
func TestAuthDoesNotFallBackToLegacyAdministratorPassword(t *testing.T) {
	a, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "legacy.db")}, cache.NewMemory(16))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.db.InitializeAdministrator("admin", "old-password-123"); err != nil {
		t.Fatal(err)
	}
	u, err := a.db.UserByID(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetUserPassword(u.ID, "new-password-456"); err != nil {
		t.Fatal(err)
	}
	login := func(pw string) int {
		body := `{"Username":"admin","Pw":"` + pw + `"}`
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if code := login("old-password-123"); code != http.StatusUnauthorized {
		t.Fatalf("旧口令被接受: %d", code)
	}
	if code := login("new-password-456"); code != http.StatusOK {
		t.Fatalf("新口令登录失败: %d", code)
	}
}
