package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/store"

	"github.com/gin-gonic/gin"
)

func TestAdminLibraryCoverLookupErrors(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			root := t.TempDir()
			db, err := store.Open(filepath.Join(root, "cover.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			lib, err := db.AddLibrary("Cover library", root)
			if err != nil {
				t.Fatal(err)
			}
			cover := filepath.Join(root, libraryCoverFileName)
			const original = "original cover must survive lookup errors"
			if err := os.WriteFile(cover, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			mem := cache.NewMemory(8)
			a := &App{db: db, cache: mem}
			router := gin.New()
			router.POST("/libraries/:id/cover", a.adminLibraryCover)
			router.DELETE("/libraries/:id/cover", a.adminLibraryCoverDelete)
			for _, tc := range []struct {
				name, id, message string
				closed            bool
			}{
				{name: "malformed", id: "bad", message: "not found"},
				{name: "fraction", id: "1.5", message: "not found"},
				{name: "overflow", id: "9223372036854775808", message: "not found"},
				{name: "missing", id: strconv.FormatInt(lib.ID+1, 10), message: "library not found"},
				{name: "zero", id: "0", message: "library not found"},
				{name: "negative", id: "-1", message: "library not found"},
				{name: "database_error", id: strconv.FormatInt(lib.ID, 10), message: "library not found", closed: true},
				{name: "malformed_before_database_error", id: "bad", message: "not found"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.closed {
						if err := db.Close(); err != nil {
							t.Fatal(err)
						}
					}
					mem.Set("cover-lookup-canary", []byte("keep"), 0)
					rec := httptest.NewRecorder()
					// No multipart file: lookup errors must precede upload validation.
					router.ServeHTTP(rec, httptest.NewRequest(method, "/libraries/"+tc.id+"/cover", nil))
					var body struct {
						Error string `json:"error"`
						Code  string `json:"code"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if rec.Code != http.StatusNotFound || body.Error != tc.message || body.Code != "NOT_FOUND" {
						t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
					}
					if got, err := os.ReadFile(cover); err != nil || string(got) != original {
						t.Fatalf("cover changed: %q, %v", got, err)
					}
					if got, ok := mem.Get("cover-lookup-canary"); !ok || string(got) != "keep" {
						t.Fatal("lookup error cleared cache")
					}
				})
			}
		})
	}
}
