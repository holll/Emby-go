package server

import (
	"context"
	"emby-go/internal/scraper"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// Executable admission matrix: a real middleware credential snapshot is taken,
// then logout commits before entering every worker-start/cancel handler.
func TestStabilityPermissionsAdmissionMatrix(t *testing.T) {
	a, _, _ := asyncScanFixture(t)
	task, err := a.db.CreateScheduledTask(store.ScheduledTask{Name: "permissions", Type: "scan", Cron: "0 0 * * *", Params: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	movie, err := a.db.IndexMovie(store.Movie{LibraryID: 1, SourcePath: "/stability-never-read.strm", NFOPath: "/stability-never-read.nfo", Title: "permission target", Status: "success"}, 1, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, path, body string
		handler          gin.HandlerFunc
	}{
		{"scan", "/scan", "", a.adminScan},
		{"async-scan", "/tasks/scan", "", a.adminSubmitScan},
		{"reindex", "/reindex", "", a.adminReindex},
		{"probe", "/probe/media", "{}", a.adminProbeMedia},
		{"probe-cancel", "/probe/media/cancel", "", a.adminProbeMediaCancel},
		{"item-probe", "/items/" + strconv.FormatInt(movie, 10) + "/probe", "", a.adminProbeMediaItem},
		{"scrape-confirm", "/items/" + strconv.FormatInt(movie, 10) + "/scrape", `{"provider":"test","id":"123"}`, a.adminScrapeConfirm},
		{"scrape", "/scrape/run", "{}", a.adminScrapeRun},
		{"avatars", "/scrape/avatars", "{}", a.adminScrapeAvatars},
		{"scrape-cancel", "/scrape/cancel", "", a.adminScrapeCancel},
		{"translate", "/translate/run", "{}", a.adminTranslationRun},
		{"translate-cancel", "/translate/cancel", "{}", a.adminTranslationCancel},
		{"scheduled", "/scheduled/" + strconv.FormatInt(task.ID, 10) + "/run", "", a.adminRunScheduledTask},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := "admission-" + tc.name
			if err := a.db.SaveAccessTokenForUser(token, 1, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			ran := false
			r := gin.New()
			r.Use(a.requireAuth, a.requireAdmin, func(c *gin.Context) {
				ran = true
				if err := a.db.DeleteAccessToken(token); err != nil {
					t.Fatal(err)
				}
				c.Next()
			})
			route := tc.path
			if tc.name == "scheduled" {
				route = "/scheduled/:id/run"
			}
			if tc.name == "item-probe" {
				route = "/items/:id/probe"
			}
			if tc.name == "scrape-confirm" {
				route = "/items/:id/scrape"
			}
			r.POST(route, tc.handler)
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Emby-Token", token)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if !ran || w.Code != http.StatusForbidden {
				t.Fatalf("barrier=%v status=%d body=%s", ran, w.Code, w.Body.String())
			}
			if a.scanning() || a.scrapeStatus.Running || a.probeStatus.Running || a.translationSnapshot().Running {
				t.Fatal("revoked request started worker")
			}
		})
	}
}

func TestStabilityPermissionsRevokeDuringRequestBody(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/api/admin/apikeys", `{"name":"must-not-create"}`},
		{"/api/admin/scrape/run", `{}`},
		{"/api/admin/translate/run", `{}`},
		{"/api/admin/probe/media", `{}`},
		{"/api/admin/security/unlock", `{"target_type":"account","value":"admin","reason":"stability","current_password":"password-1234"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			a, _, token := asyncScanFixture(t)
			ran := false
			body := &passwordIdentityReader{Reader: strings.NewReader(tc.body), beforeRead: func() {
				ran = true
				if err := a.db.DeleteAccessToken(token); err != nil {
					t.Fatal(err)
				}
			}}
			req := httptest.NewRequest("POST", tc.path, body)
			req.ContentLength = int64(len(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Emby-Token", token)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, req)
			if !ran || w.Code != http.StatusForbidden {
				t.Fatalf("read=%v status=%d body=%s", ran, w.Code, w.Body.String())
			}
			keys, err := a.db.APIKeys()
			if err != nil || len(keys) != 0 {
				t.Fatalf("partial key write: %+v %v", keys, err)
			}
		})
	}
}

func TestStabilityPermissionsSensitiveWriteMatrix(t *testing.T) {
	for _, mode := range []string{"logout", "role-ABA", "key-rotation", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := asyncScanFixture(t)
			actor, err := a.db.CreateUser("sensitive-admin", "password-1234", true)
			if err != nil {
				t.Fatal(err)
			}
			source, err := a.db.CreateAPIKeyForUser("source", actor.ID)
			if err != nil {
				t.Fatal(err)
			}
			target, err := a.db.CreateAPIKeyForUser("target", 1)
			if err != nil {
				t.Fatal(err)
			}
			token := "sensitive-session"
			session := true
			if mode == "key-rotation" {
				token = source.Key
				session = false
			} else if err := a.db.SaveAccessTokenForUser(token, actor.ID, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			proof, err := a.db.RequestCredentialUser(token, actor.ID, session)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "logout":
				err = a.db.DeleteAccessToken(token)
			case "role-ABA":
				err = a.db.SetUserAdmin(actor.ID, false)
				if err == nil {
					err = a.db.SetUserAdmin(actor.ID, true)
				}
			case "key-rotation":
				authenticated, ok, e := a.db.AuthenticateUser(actor.Name, "password-1234")
				if e != nil || !ok {
					t.Fatal(e)
				}
				_, err = a.db.RotateAPIKey(authenticated, source.ID, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				method, path, body string
				handler            gin.HandlerFunc
			}{
				{"POST", "/apikeys", `{"name":"injected"}`, a.adminCreateAPIKey},
				{"DELETE", "/apikeys/" + target.ID, "", a.adminDeleteAPIKey},
				{"DELETE", "/access-tokens", "", a.adminRevokeAllSessions},
			}
			for _, tc := range cases {
				r := gin.New()
				r.Use(func(c *gin.Context) { c.Set(currentUserKey, proof) })
				route := tc.path
				if tc.handler != nil && strings.HasPrefix(route, "/apikeys/") {
					route = "/apikeys/:key"
				}
				r.Handle(tc.method, route, tc.handler)
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if mode == "unchanged" {
					if w.Code < 200 || w.Code >= 300 {
						t.Fatalf("%s status=%d %s", tc.path, w.Code, w.Body.String())
					}
				} else if w.Code != http.StatusForbidden {
					t.Fatalf("%s status=%d %s", tc.path, w.Code, w.Body.String())
				}
			}
			if mode != "unchanged" {
				if valid, err := a.db.HasAPIKey(target.Key); err != nil || !valid {
					t.Fatalf("target changed: %v %v", valid, err)
				}
			}
		})
	}
}

// Logout after requireInteractiveSession but during JSON decode used to leave
// password reauthentication sufficient to commit rotation with a dead session.
func TestStabilityPermissionsRotationRequiresOriginalSession(t *testing.T) {
	a, _, token := asyncScanFixture(t)
	key, err := a.db.CreateAPIKeyForUser("rotation-target", 1)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	raw := `{"current_password":"password-1234","reason":"stability boundary"}`
	body := &passwordIdentityReader{Reader: strings.NewReader(raw), beforeRead: func() {
		ran = true
		if err := a.db.DeleteAccessToken(token); err != nil {
			t.Fatal(err)
		}
	}}
	req := httptest.NewRequest("POST", "/api/admin/apikeys/"+key.ID+"/rotate", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("If-Match", `"ak-`+strings.TrimPrefix(key.ID, "ak_")+`-1"`)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	if !ran || w.Code != http.StatusUnauthorized {
		t.Fatalf("read=%v status=%d body=%s", ran, w.Code, w.Body.String())
	}
	if valid, err := a.db.HasAPIKey(key.Key); err != nil || !valid {
		t.Fatalf("revoked session rotated key: %v %v", valid, err)
	}
	keys, err := a.db.APIKeys()
	if err != nil || len(keys) != 1 || keys[0].Version != 1 {
		t.Fatalf("partial rotation: %+v %v", keys, err)
	}
}

// Admission is not a long transaction or an obligation to cancel accepted work.
// A second Store can revoke credentials while the admitted worker is parked.
func TestStabilityPermissionsAdmittedWorkerOutlivesRevocation(t *testing.T) {
	f := newTranslationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	f.a.translation.item = func(ctx context.Context, m store.Movie, opts scraper.TranslationOptions) (scraper.TranslationResult, error) {
		close(entered)
		select {
		case <-release:
			return scraper.TranslationResult{}, nil
		case <-ctx.Done():
			return scraper.TranslationResult{}, ctx.Err()
		}
	}
	w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("admission %d %s", w.Code, w.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker not entered")
	}
	other, err := store.Open(f.a.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	revoked := make(chan error, 1)
	go func() { revoked <- other.DeleteAccessToken(f.token) }()
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation blocked by admitted worker")
	}
	if !f.a.translationSnapshot().Running {
		t.Fatal("logout retroactively cancelled admitted worker")
	}
	if w := translationCall(f.a, f.token, "POST", "/api/admin/translate/run", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked credential reused: %d", w.Code)
	}
	close(release)
	released = true
	status := awaitTranslation(t, f.a)
	if status.Cancelled || status.Done != 1 {
		t.Fatalf("admitted worker did not complete: %+v", status)
	}
}
