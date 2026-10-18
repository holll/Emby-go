package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const privateAuthBody = `{"Username":"private-admin-canary","Pw":"private-password-canary"}`

func authPrivacyRequest(h http.Handler, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(privateAuthBody))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func assertAuthPrivateError(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body map[string]any
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 2 || body["error"] != message || body["code"] != errorCodeForStatus(status) {
		t.Fatalf("status=%d body=%s; expected %d %q with code %q", w.Code, w.Body.String(), status, message, errorCodeForStatus(status))
	}
	for _, secret := range []string{"private-admin-canary", "private-password-canary", "private-sql-canary", "sql:", "password_hash", "no such table", "NULL"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("response leaked %q", secret)
		}
	}
}

func TestAuthenticationClosedStoreErrorsArePrivate(t *testing.T) {
	for _, route := range []struct{ method, path string }{{"GET", "/api/auth/status"}, {"POST", "/api/auth/initialize"}, {"POST", "/Users/AuthenticateByName"}} {
		t.Run(route.path, func(t *testing.T) {
			a, _, _ := authorityApp(t)
			h := a.Handler()
			a.Close()
			w := authPrivacyRequest(h, route.method, route.path)
			assertAuthPrivateError(t, w, 500, "authentication storage unavailable")
		})
	}
}

func TestAuthenticationPasswordReadFailureIsPrivateAndRecoverable(t *testing.T) {
	a, legacy, path := authorityApp(t)
	if err := a.db.InitializeAdministrator("private-admin-canary", "private-password-canary"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// COUNT(id=1) remains valid, but the later password read cannot Scan NULL.
	if _, err := db.Exec(`ALTER TABLE users RENAME TO private_auth_original;
CREATE VIEW users AS SELECT id,name,NULL AS password_hash,is_admin,created_at,updated_at FROM private_auth_original;`); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	w := authPrivacyRequest(h, "POST", "/Users/AuthenticateByName")
	assertAuthPrivateError(t, w, 500, "authentication storage unavailable")
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM access_tokens").Scan(&count); err != nil || count != 0 {
		t.Fatalf("tokens=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DROP VIEW users; ALTER TABLE private_auth_original RENAME TO users;`); err != nil {
		t.Fatal(err)
	}
	w = authPrivacyRequest(h, "POST", "/Users/AuthenticateByName")
	var response struct{ AccessToken string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.AccessToken == "" {
		t.Fatalf("retry=%d body=%s", w.Code, w.Body.String())
	}
	if valid, err := a.db.HasAccessToken(response.AccessToken); err != nil || !valid {
		t.Fatalf("token not persisted: %v", err)
	}
	if legacy.calls.Load() != 0 {
		t.Fatal("credential cache used")
	}
}

func TestAuthenticationNonStorageContract(t *testing.T) {
	a, _, _ := authorityApp(t)
	h := a.Handler()
	status := func(want bool) {
		t.Helper()
		w := authPrivacyRequest(h, "GET", "/api/auth/status")
		var body struct {
			Initialized bool `json:"initialized"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Initialized != want {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	status(false)
	assertAuthPrivateError(t, authPrivacyRequest(h, "POST", "/Users/AuthenticateByName"), 428, "管理员尚未初始化")
	if w := authPrivacyRequest(h, "POST", "/api/auth/initialize"); w.Code != 204 {
		t.Fatalf("initialize=%d", w.Code)
	}
	status(true)
	assertAuthPrivateError(t, authPrivacyRequest(h, "POST", "/api/auth/initialize"), 409, "管理员已初始化")
	r := httptest.NewRequest("POST", "/Users/AuthenticateByName", strings.NewReader(`{"Username":"private-admin-canary","Pw":"incorrect-password"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertAuthPrivateError(t, w, 401, "账号或密码错误")
	r = httptest.NewRequest("POST", "/Users/AuthenticateByName", strings.NewReader(`{`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertAuthPrivateError(t, w, 400, "invalid credentials payload")
}

func TestAuthenticationWriteFailuresRemainPrivate(t *testing.T) {
	for _, initializing := range []bool{true, false} {
		name := "session-save"
		if initializing {
			name = "administrator-insert"
		}
		t.Run(name, func(t *testing.T) {
			a, _, path := authorityApp(t)
			if !initializing {
				if err := a.db.InitializeAdministrator("private-admin-canary", "private-password-canary"); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			table := "access_tokens"
			route := "/Users/AuthenticateByName"
			status := 500
			message := "保存访问令牌失败"
			if initializing {
				table = "users"
				route = "/api/auth/initialize"
				status = 409
				message = "初始化失败，请更换账号后重试"
			}
			if _, err := db.Exec("CREATE TRIGGER fail_private_auth BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'private-sql-canary'); END"); err != nil {
				t.Fatal(err)
			}
			h := a.Handler()
			w := authPrivacyRequest(h, "POST", route)
			assertAuthPrivateError(t, w, status, message)
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("failed insert persisted: %d %v", n, err)
			}
			if _, err := db.Exec("DROP TRIGGER fail_private_auth"); err != nil {
				t.Fatal(err)
			}
			w = authPrivacyRequest(h, "POST", route)
			expected := 200
			if initializing {
				expected = 204
			}
			if w.Code != expected {
				t.Fatalf("retry=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
