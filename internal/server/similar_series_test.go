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

// TestSimilarTierOrdering 相似作品的分层排序：同系列 > 同番号系列 > 演员重合 >
// 仅类型/标签特征；分级/年份这类全库恒定或弱信号不再抬高名次。
func TestSimilarTierOrdering(t *testing.T) {
	app, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "tiers.db")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	lib, err := app.db.AddLibrary("AV", "/tmp/similar-tiers")
	if err != nil {
		t.Fatal(err)
	}
	movie := func(title, collection, number, maker string, year int, genres []string, actors ...string) int64 {
		refs := make([]store.ActorRef, 0, len(actors))
		for _, name := range actors {
			refs = append(refs, store.ActorRef{Name: name})
		}
		id, err := app.db.IndexMovie(store.Movie{
			LibraryID: lib.ID, SourcePath: "/tmp/similar-tiers/" + title + ".strm", Status: "success",
			Title: title, Collection: collection, Number: number, Maker: maker, Year: year,
			Genres: genres, OfficialRating: "JP-18+",
		}, 1, time.Now(), refs)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	src := movie("Source", "S0", "ABC-100", "Maker X", 2020, []string{"G1"}, "A1", "A2", "A3")
	seriesCand := movie("Series Candidate", "S0", "ZZZ-900", "Other", 2021, []string{"G9"})
	prefixCand := movie("Prefix Candidate", "S1", "ABC-101", "Other", 2020, []string{"G1"})
	actor2Cand := movie("Two Actors", "S2", "XYZ-001", "Other", 2020, []string{"G2"}, "A1", "A2")
	brandCand := movie("Same Brand", "S3", "QQQ-001", "Maker X", 2020, []string{"G1"}, "A1")
	actor1Cand := movie("One Actor", "S4", "RRR-001", "Other", 2021, []string{"G9"}, "A3")
	genreOnly := movie("Genre Only", "S5", "TTT-001", "Other", 2022, []string{"G1"})
	movie("Rating Only", "S6", "UUU-001", "Other", 2020, []string{"G9"})

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
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []int64{seriesCand, prefixCand, actor2Cand, brandCand, actor1Cand, genreOnly}
	if len(body.Items) != len(want) {
		t.Fatalf("相似数量=%d 期望=%d body=%s", len(body.Items), len(want), w.Body.String())
	}
	for i, id := range want {
		if body.Items[i].Id != strconv.FormatInt(id, 10) {
			t.Fatalf("第 %d 位=%s 期望 %d，body=%s", i, body.Items[i].Id, id, w.Body.String())
		}
	}
}
