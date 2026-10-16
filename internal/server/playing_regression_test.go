package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/store"
)

func ginContext(w *httptest.ResponseRecorder, req *http.Request) (*gin.Context, *gin.Engine) {
	c, engine := gin.CreateTestContext(w)
	c.Request = req
	return c, engine
}

func TestPlayingPartAndStoppedRegression(t *testing.T) {
	for _, event := range []string{"Playing", "Playing/Progress", "Playing/Stopped", "playing/stopped"} {
		for _, part := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/part=%v", event, part), func(t *testing.T) {
				root := t.TempDir()
				a, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(32))
				if err != nil {
					t.Fatal(err)
				}
				defer a.Close()
				lib, err := a.db.AddLibrary("test", root)
				if err != nil {
					t.Fatal(err)
				}
				id, err := a.db.UpsertMovie(store.Movie{LibraryID: lib.ID, SourcePath: filepath.Join(root, "a.strm"), SourceProtocol: "https", Status: "success", AdditionalParts: []string{filepath.Join(root, "b.strm")}}, 1, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				rawID := fmt.Sprint(id)
				if part {
					rawID = virtualPartID(id, 2)
				}
				stopped := strings.HasSuffix(strings.ToLower(event), "stopped")
				for i := 0; i < 2; i++ {
					before := a.db.Version("g:version")
					key := "item:" + before + ":" + fmt.Sprint(id)
					a.cache.Set(key, []byte("stale"), time.Hour)
					req := httptest.NewRequest(http.MethodPost, "/Sessions/"+event, strings.NewReader(fmt.Sprintf(`{"ItemId":%q,"PositionTicks":123}`, rawID)))
					w := httptest.NewRecorder()
					// 直接验证处理函数；路由注册与鉴权由现有 API 测试覆盖。
					c, _ := ginContext(w, req)
					a.playing(c)
					c.Writer.WriteHeaderNow()
					if w.Code != http.StatusNoContent {
						t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
					}
					d, err := a.db.Data(id)
					if err != nil {
						t.Fatal(err)
					}
					if d.PositionTicks != 123 || d.LastPlayedAt == "" {
						t.Fatalf("main movie userdata not updated: %+v", d)
					}
					if stopped {
						if d.PlayCount != 1 || d.StoppedTicks != 123 {
							t.Fatalf("stop not applied idempotently to main movie: %+v", d)
						}
						if a.db.Version("g:version") == before {
							t.Fatal("stopped did not invalidate version")
						}
					} else {
						if d.PlayCount != 0 {
							t.Fatalf("progress changed play count: %+v", d)
						}
						if a.db.Version("g:version") != before {
							t.Fatal("progress bumped version")
						}
						if _, ok := a.cache.Get(key); ok {
							t.Fatal("main movie detail cache not invalidated")
						}
					}
				}
				before, _ := a.db.Data(id)
				version := a.db.Version("g:version")
				for _, invalid := range []string{virtualPartID(id, 1), virtualPartID(id, 3), "part-999999-2", "part-invalid-2"} {
					w := httptest.NewRecorder()
					c, _ := ginContext(w, httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(fmt.Sprintf(`{"ItemId":%q,"PositionTicks":456}`, invalid))))
					a.playing(c)
					if w.Code != http.StatusNotFound {
						t.Fatalf("invalid %s status=%d", invalid, w.Code)
					}
				}
				after, _ := a.db.Data(id)
				if after != before || a.db.Version("g:version") != version {
					t.Fatal("invalid part modified userdata or version")
				}
			})
		}
	}
}
