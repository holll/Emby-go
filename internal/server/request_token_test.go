package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestTokenAuthorizationSchemes(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Emby-Authorization"} {
		for _, scheme := range []string{"MediaBrowser", "Emby", "mEdIaBrOwSeR", "eMbY"} {
			for _, params := range []string{
				`Token="secret+value&tail"`,
				`Token="secret+value&tail", Client="fixture"`,
				`Client="fixture", Token="secret+value&tail", Device="test"`,
				`Client="fixture", Token="secret+value&tail"`,
			} {
				for _, separator := range []string{" ", "\t", "  "} {
					t.Run(header+"/"+scheme+"/"+params+"/"+separator, func(t *testing.T) {
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
						c.Request.Header.Set(header, scheme+separator+params)
						if got := requestToken(c); got != "secret+value&tail" {
							t.Fatalf("token=%q", got)
						}
					})
				}
			}
		}
	}
}

func TestRequestTokenRetainsSourcePrecedenceAndLegacyFormat(t *testing.T) {
	tests := []struct {
		name, query, want string
		headers           map[string]string
	}{
		{"empty", "", "", nil},
		{"legacy bare token", "", "bare", map[string]string{"Authorization": `Token="bare"`}},
		{"legacy bare middle", "", "bare", map[string]string{"Authorization": `Client="fixture", tOkEn="bare"`}},
		{"first duplicate retained", "", "first", map[string]string{"Authorization": `Emby Token="first", Token="second"`}},
		{"no bearer support", "", "", map[string]string{"Authorization": "Bearer not-an-emby-token"}},
		{"no prefix substring", "", "", map[string]string{"Authorization": `NotEmby Token="bad"`}},
		{"emby token header first", "?api_key=query", "direct", map[string]string{"X-Emby-Token": "direct", "X-MediaBrowser-Token": "other", "Authorization": `Emby Token="auth"`}},
		{"mediabrowser token before query", "?api_key=query", "other", map[string]string{"X-MediaBrowser-Token": "other", "Authorization": `Emby Token="auth"`}},
		{"query before authorization", "?api_key=lower&ApiKey=upper&X-Emby-Token=legacy", "lower", map[string]string{"Authorization": `Emby Token="auth"`}},
		{"uppercase query fallback", "?ApiKey=upper&X-Emby-Token=legacy", "upper", map[string]string{"Authorization": `Emby Token="auth"`}},
		{"legacy query fallback", "?X-Emby-Token=legacy", "legacy", map[string]string{"Authorization": `Emby Token="auth"`}},
		{"emby auth header before standard", "", "emby", map[string]string{"X-Emby-Authorization": `Emby Token="emby"`, "Authorization": `MediaBrowser Token="standard"`}},
		{"nonempty selected header does not fall through", "", "", map[string]string{"X-Emby-Authorization": `Emby Client="fixture"`, "Authorization": `Emby Token="standard"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/"+tt.query, nil)
			for key, value := range tt.headers {
				c.Request.Header.Set(key, value)
			}
			if got := requestToken(c); got != tt.want {
				t.Fatalf("token=%q want=%q", got, tt.want)
			}
		})
	}
	if got := requestToken(nil); got != "" {
		t.Fatalf("nil context token=%q", got)
	}
}

func TestEmbyAuthorizationTokenFirstUsesAuthoritativeStore(t *testing.T) {
	a, legacy, _ := authorityApp(t)
	seedOwner(t, a)
	token := "fixture-token-first"
	if err := a.db.SaveAccessToken(token); err != nil {
		t.Fatal(err)
	}
	router := authorityRouter(a)
	request := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/private", nil)
		req.Header.Set("Authorization", `MediaBrowser Token="`+token+`", Client="fixture"`)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if code := request(token); code != http.StatusNoContent {
		t.Fatalf("valid token status=%d", code)
	}
	if code := request("fixture-unknown"); code != http.StatusUnauthorized {
		t.Fatalf("unknown token status=%d", code)
	}
	if err := a.db.DeleteAccessToken(token); err != nil {
		t.Fatal(err)
	}
	if code := request(token); code != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d", code)
	}
	if calls := legacy.calls.Load(); calls != 0 {
		t.Fatalf("credential positive cache used %d times", calls)
	}
}
