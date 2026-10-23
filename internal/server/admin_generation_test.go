package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

// A middleware barrier runs after the real authentication/admin gates and before
// each handler. No sleeps: every revocation commits before the write transaction.
func TestAdminRequestGenerationBarrier(t *testing.T) {
	for _, mode := range []string{"password", "revoke-user", "revoke-all", "logout", "role-ABA", "admin-role-ABA", "unchanged"} {
		for _, action := range []string{"create", "update", "libraries", "delete"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				a, _, _ := asyncScanFixture(t)
				actor, err := a.db.CreateUser("generation-admin", "password-1234", true)
				if err != nil {
					t.Fatal(err)
				}
				target, err := a.db.CreateUser("generation-target", "password-1234", false)
				if err != nil {
					t.Fatal(err)
				}
				if err := a.db.SaveAccessTokenForUser("generation-session", actor.ID, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if err := a.db.SaveAccessTokenForUser("owner-proof", 1, time.Time{}); err != nil {
					t.Fatal(err)
				}
				owner, err := a.db.RequestCredentialUser("owner-proof", 1, true)
				if err != nil {
					t.Fatal(err)
				}
				barrierRan := false
				r := gin.New()
				r.Use(a.requireAuth, a.requireAdmin, func(c *gin.Context) {
					barrierRan = true
					var err error
					switch mode {
					case "password":
						err = a.db.SetUserPassword(actor.ID, "changed-password-1234")
					case "revoke-user":
						err = a.db.RevokeUserAccessTokens(actor.ID)
					case "revoke-all":
						_, err = a.db.RevokeAllAccessTokens()
					case "logout":
						err = a.db.DeleteAccessToken("generation-session")
					case "role-ABA":
						err = a.db.SetUserAdmin(actor.ID, false)
						if err == nil {
							err = a.db.SetUserAdmin(actor.ID, true)
						}
					case "admin-role-ABA":
						no, yes := false, true
						err = a.db.AdminUpdateUser(1, actor.ID, store.AdminUserUpdate{IsAdmin: &no}, owner)
						if err == nil {
							err = a.db.AdminUpdateUser(1, actor.ID, store.AdminUserUpdate{IsAdmin: &yes}, owner)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					c.Next()
				})
				method, path, body := "PUT", "/users/"+strconv.FormatInt(target.ID, 10), `{"name":"generation-renamed","password":"must-not-apply-password","is_admin":true,"library_ids":[]}`
				switch action {
				case "create":
					method, path, body = "POST", "/users", `{"name":"generation-injected","password":"password-1234","is_admin":true}`
					r.POST(path, a.adminCreateUser)
				case "update":
					r.PUT("/users/:id", a.adminUpdateUser)
				case "libraries":
					path += "/libraries"
					body = `{"library_ids":[]}`
					r.PUT("/users/:id/libraries", a.adminSetUserLibraries)
				case "delete":
					method, body = "DELETE", ""
					r.DELETE("/users/:id", a.adminDeleteUser)
				}
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Emby-Token", "generation-session")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if !barrierRan {
					t.Fatal("authentication barrier not reached")
				}
				if mode == "unchanged" {
					if w.Code < 200 || w.Code >= 300 {
						t.Fatalf("fresh request status=%d %s", w.Code, w.Body.String())
					}
					return
				}
				if w.Code != http.StatusForbidden {
					t.Fatalf("stale request status=%d %s", w.Code, w.Body.String())
				}
				current, err := a.db.UserByID(actor.ID)
				if err != nil || !current.IsAdmin {
					t.Fatalf("test must leave actor admin: %+v %v", current, err)
				}
				got, err := a.db.UserByID(target.ID)
				if err != nil || got.Name != target.Name || got.IsAdmin {
					t.Fatalf("partial mutation: %+v %v", got, err)
				}
				if _, valid, err := a.db.AuthenticateUser(target.Name, "password-1234"); err != nil || !valid {
					t.Fatalf("target password changed: %v", err)
				}
				users, err := a.db.ListUsers()
				if err != nil {
					t.Fatal(err)
				}
				for _, u := range users {
					if u.Name == "generation-injected" {
						t.Fatal("stale create committed")
					}
				}
			})
		}
	}
}
