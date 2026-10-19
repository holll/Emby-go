package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/config"
	"emby-go/internal/store"
)

// TestSimilarSeriesComesFirst 详情页「相似作品」必须把同系列排在更髙分的
// 演员/标签重合候选之前。
func TestSimilarSeriesComesFirst(t *testing.T) {
	app, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "similar.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	lib, err := app.db.AddLibrary("AV", "/tmp/similar-series")
	if err != nil {
		t.Fatal(err)
	}
	actors := make([]store.ActorRef, 0, 8)
	for i := 0; i < 8; i++ {
		actors = append(actors, store.ActorRef{Name: "Actor " + strconv.Itoa(i)})
	}
	index := func(title, collection string, withActors bool) int64 {
		refs := []store.ActorRef(nil)
		if withActors {
			refs = actors
		}
		id, err := app.db.IndexMovie(store.Movie{LibraryID: lib.ID, SourcePath: "/tmp/similar-series/" + title + ".strm", Status: "success", Title: title, Collection: collection, Genres: []string{"Drama"}}, 1, time.Now(), refs)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	src := index("Source", "Series S", true)
	sameSeries := index("Same Series", "Series S", false)
	index("Actor Heavy", "Series T", true)

	r := gin.New()
	r.GET("/similar/:id", app.similar)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/similar/"+strconv.FormatInt(src, 10)+"?Limit=10", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			Id string `json:"Id"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Items) < 2 {
		t.Fatalf("相似列表为空或解析失败: %v body=%s", err, w.Body.String())
	}
	if body.Items[0].Id != strconv.FormatInt(sameSeries, 10) {
		t.Fatalf("首位相似 = %s，期望同系列 %d，body=%s", body.Items[0].Id, sameSeries, w.Body.String())
	}
}
