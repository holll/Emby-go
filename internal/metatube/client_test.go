package metatube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestServer 起一个假 MetaTube 后端，记录收到的请求以便断言鉴权与查询参数。
func newTestServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *[]*http.Request) {
	t.Helper()
	seen := &[]*http.Request{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(context.Background())
		*seen = append(*seen, clone)
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	return New(ts.URL, "secret-token", 5*time.Second), seen
}

func TestSearchMovies(t *testing.T) {
	client, seen := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"abc","number":"ABF-018","title":"标题","provider":"fanza",
			"thumb_url":"https://t/1.jpg","cover_url":"https://c/1.jpg","score":9.5,"actors":["A"]}]}`))
	})
	results, err := client.SearchMovies(context.Background(), "ABF-018", "", true)
	if err != nil {
		t.Fatalf("SearchMovies: %v", err)
	}
	if len(results) != 1 || results[0].Number != "ABF-018" || results[0].Provider != "fanza" {
		t.Fatalf("结果不对: %+v", results)
	}
	request := (*seen)[0]
	if got := request.URL.Path; got != "/v1/movies/search" {
		t.Errorf("路径 = %q", got)
	}
	if got := request.URL.Query().Get("q"); got != "ABF-018" {
		t.Errorf("q = %q", got)
	}
	if got := request.URL.Query().Get("fallback"); got != "true" {
		t.Errorf("fallback = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
		t.Errorf("鉴权头 = %q", got)
	}
}

func TestSearchMoviesNotFound(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	})
	results, err := client.SearchMovies(context.Background(), "nope", "", true)
	if err != nil {
		t.Fatalf("无结果不应报错: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("应返回空结果: %+v", results)
	}
}

func TestMovieInfoAndLazyParam(t *testing.T) {
	client, seen := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"abc","provider":"fanza","number":"ABF-018","title":"T",
			"summary":"S","maker":"M","label":"L","series":"Ser","genres":["剧情"],"score":8.1,
			"runtime":120,"release_date":"2024-01-02","actors":["A","B"],
			"cover_url":"https://c/1.jpg","big_cover_url":"https://c/big.jpg"}}`))
	})
	info, err := client.MovieInfo(context.Background(), "fanza", "abc", true)
	if err != nil {
		t.Fatalf("MovieInfo: %v", err)
	}
	if info.Title != "T" || info.Runtime != 120 || len(info.Actors) != 2 || info.ReleaseDate != "2024-01-02" {
		t.Fatalf("详情不对: %+v", info)
	}
	request := (*seen)[0]
	if got := request.URL.Path; got != "/v1/movies/fanza/abc" {
		t.Errorf("路径 = %q", got)
	}
	if got := request.URL.Query().Get("lazy"); got != "true" {
		t.Errorf("lazy = %q", got)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"invalid token"}}`))
	})
	_, err := client.MovieInfo(context.Background(), "p", "1", true)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("错误信息应带后端消息，得到 %q", err.Error())
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Errorf("应解出 APIError(401): %v", err)
	}
}

func TestSearchActors(t *testing.T) {
	client, seen := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"1","name":"三上悠亜","provider":"fanza",
			"aliases":["みかみゆあ"],"images":["https://i/1.jpg"]}]}`))
	})
	results, err := client.SearchActors(context.Background(), "三上悠亜", "", true)
	if err != nil {
		t.Fatalf("SearchActors: %v", err)
	}
	if len(results) != 1 || results[0].Name != "三上悠亜" || len(results[0].Images) != 1 {
		t.Fatalf("演员结果不对: %+v", results)
	}
	if got := (*seen)[0].URL.Path; got != "/v1/actors/search" {
		t.Errorf("路径 = %q", got)
	}
}

func TestImageURL(t *testing.T) {
	client := New("https://mt.example.com/", "", 0)
	got := client.ImageURL("primary", "fanza", "abc", 90, "")
	want := "https://mt.example.com/v1/images/primary/fanza/abc?quality=90"
	if got != want {
		t.Errorf("ImageURL = %q，期望 %q", got, want)
	}
	got = client.ImageURL("thumb", "p", "i", 0, "https://remote/x.jpg")
	if !strings.Contains(got, "url=") || strings.Contains(got, "quality") {
		t.Errorf("带远端 url 且不指定质量时不应出现 quality: %q", got)
	}
}

func TestDownloadAndHealth(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"app":"metatube","version":"1.0"}`))
			return
		}
		_, _ = w.Write([]byte("binary-image"))
	})
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	data, err := client.Download(context.Background(), client.BaseURL()+"/v1/images/primary/p/i", 0)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(data) != "binary-image" {
		t.Errorf("内容 = %q", string(data))
	}
	// 图片端点不带鉴权头（后端把它归为公开端点）。
	if data, err := client.Download(context.Background(), client.BaseURL()+"/x", 0); err != nil || len(data) == 0 {
		t.Fatalf("Download 失败: %v", err)
	}
}

func TestDecodeErrorFallback(t *testing.T) {
	client, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	})
	_, err := client.MovieInfo(context.Background(), "p", "1", true)
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("非 JSON 错误体应退回原文片段，得到 %v", err)
	}
}

// asAPIError 是 errors.As 的薄封装，避免测试文件重复 import errors。
func asAPIError(err error, target **APIError) bool {
	for err != nil {
		if apiErr, ok := err.(*APIError); ok {
			*target = apiErr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
