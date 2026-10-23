package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The handler reads the account by ID before binding the body. A first-read
// callback deterministically schedules the rename/replacement into that gap,
// without sleeps, production hooks or relying on goroutine scheduling.
type passwordIdentityReader struct {
	io.Reader
	beforeRead func()
}

func (r *passwordIdentityReader) Read(p []byte) (int, error) {
	if f := r.beforeRead; f != nil {
		r.beforeRead = nil
		f()
	}
	return r.Reader.Read(p)
}

func TestUsersPasswordRejectsRenamedReplacementIdentity(t *testing.T) {
	a, _, _ := authorityApp(t)
	seedOwner(t, a)
	const originalName = "password-original"
	const originalPassword = "shared-current-password"
	const changedPassword = "must-not-be-applied-password"
	original, err := a.db.CreateUser(originalName, originalPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := a.db.CreateUser("password-replacement", originalPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	for token, id := range map[string]int64{"original-session": original.ID, "replacement-session": replacement.ID} {
		if err := a.db.SaveAccessTokenForUser(token, id, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	renamed := false
	body := &passwordIdentityReader{
		Reader: strings.NewReader(`{"current_password":"` + originalPassword + `","new_password":"` + changedPassword + `"}`),
		beforeRead: func() {
			if err := a.db.RenameUser(original.ID, "password-renamed-original"); err != nil {
				t.Fatal(err)
			}
			if err := a.db.RenameUser(replacement.ID, originalName); err != nil {
				t.Fatal(err)
			}
			renamed = true
		},
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(currentUserKey, original)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/password", body)
	c.Request.Header.Set("Content-Type", "application/json")
	a.changePassword(c)
	if !renamed {
		t.Fatal("rename interleaving did not execute")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("identity mismatch status=%d body=%s", w.Code, w.Body.String())
	}
	for _, name := range []string{"password-renamed-original", originalName} {
		if _, ok, err := a.db.AuthenticateUser(name, originalPassword); err != nil || !ok {
			t.Fatalf("original password changed for %s: ok=%v err=%v", name, ok, err)
		}
		if _, ok, err := a.db.AuthenticateUser(name, changedPassword); err != nil || ok {
			t.Fatalf("new password applied to %s: ok=%v err=%v", name, ok, err)
		}
	}
	for token, id := range map[string]int64{"original-session": original.ID, "replacement-session": replacement.ID} {
		got, valid, err := a.db.ValidateAccessTokenUser(token, time.Now(), 0)
		if err != nil || !valid || got != id {
			t.Fatalf("identity mismatch revoked session %s: id=%d valid=%v err=%v", token, got, valid, err)
		}
	}
}
