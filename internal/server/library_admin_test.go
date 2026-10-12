package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"emby-go/internal/cache"
	"emby-go/internal/config"
	"emby-go/internal/store"
)

// TestLibraryAdminCoverAndCounts 覆盖媒体库增强的三件事：列表带影片数与封面 URL、
// 上传封面后封面可见且落盘、移除后回退。
func TestLibraryAdminCoverAndCounts(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "lib")
	libDir2 := filepath.Join(root, "lib2")
	for _, dir := range []string{libDir, libDir2} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(libDir, "A-1.strm"), []byte("http://media.test/a.mp4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	first, err := a.db.AddLibrary("第一库", libDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.db.AddLibrary("第二库", libDir2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.UpsertMovie(store.Movie{
		LibraryID:  first.ID,
		Title:      "A-1",
		SourcePath: filepath.Join(libDir, "A-1.strm"),
		Status:     "success",
	}, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	if resp, body := adminJSON(t, http.MethodPost, ts.URL+"/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("初始化: %d %s", resp.StatusCode, body)
	}
	resp, body := adminJSON(t, http.MethodPost, ts.URL+"/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录: %d %s", resp.StatusCode, body)
	}
	var login struct{ AccessToken string }
	if err := json.Unmarshal([]byte(body), &login); err != nil {
		t.Fatal(err)
	}
	token := login.AccessToken

	libraries := func() []map[string]any {
		t.Helper()
		resp, body := adminJSON(t, http.MethodGet, ts.URL+"/api/admin/libraries", "", token)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("列库: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out.Items
	}

	// 1) 初始：两个库，第一库 1 部影片、无封面。
	items := libraries()
	if len(items) != 2 {
		t.Fatalf("媒体库数 = %d，期望 2", len(items))
	}
	if items[0]["Name"] != "第一库" {
		t.Fatalf("首个库 = %v，期望 第一库", items[0]["Name"])
	}
	if got := items[0]["count"]; got != float64(1) {
		t.Errorf("第一库 count = %v，期望 1", got)
	}
	if got := items[0]["cover"]; got != "" {
		t.Errorf("初始 cover = %v，期望空串", got)
	}

	// 2) 顺序调整：第二库上移。
	movePath := ts.URL + "/api/admin/libraries/" + strconv.FormatInt(second.ID, 10) + "/move"
	if resp, body := adminJSON(t, http.MethodPost, movePath, `{"direction":"up"}`, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("上移: %d %s", resp.StatusCode, body)
	}
	if items = libraries(); items[0]["Name"] != "第二库" {
		t.Fatalf("上移后首个库 = %v，期望 第二库", items[0]["Name"])
	}

	// 3) 上传封面（第一库此时排在第二位）。
	uploadLibraryCover(t, ts.URL+"/api/admin/libraries/"+strconv.FormatInt(first.ID, 10)+"/cover", token)
	items = libraries()
	cover, _ := items[1]["cover"].(string)
	if !strings.HasPrefix(cover, "/Items/") {
		t.Fatalf("上传后 cover = %q，期望以 /Items/ 开头的图片 URL", cover)
	}

	// 4) 封面落盘且图片端点能取到。
	if _, err := os.Stat(filepath.Join(libDir, libraryCoverFileName)); err != nil {
		t.Fatalf("封面文件未落盘: %v", err)
	}
	imgReq, _ := http.NewRequest(http.MethodGet, ts.URL+cover, nil)
	imgReq.Header.Set("X-Emby-Token", token)
	imgResp, err := http.DefaultClient.Do(imgReq)
	if err != nil {
		t.Fatal(err)
	}
	imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusOK {
		t.Errorf("取库封面 = %d，期望 200", imgResp.StatusCode)
	}

	// 5) 移除封面：文件删除，列表回退到空串（该库既无目录自带图也无影片代表图）。
	clearPath := ts.URL + "/api/admin/libraries/" + strconv.FormatInt(first.ID, 10) + "/cover"
	if resp, body := adminJSON(t, http.MethodDelete, clearPath, "", token); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("移除封面: %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(libDir, libraryCoverFileName)); !os.IsNotExist(err) {
		t.Errorf("移除后封面文件仍在")
	}
	if items = libraries(); items[1]["cover"] != "" {
		t.Errorf("移除后 cover = %v，期望空串", items[1]["cover"])
	}
}

// adminJSON 发一个（可选带 JSON body 与 token 的）请求并返回响应与正文。
func adminJSON(t *testing.T, method, url, body, token string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(raw)
}

// uploadLibraryCover 以 multipart 上传一张真实 JPEG 作为库封面。
func uploadLibraryCover(t *testing.T, url, token string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, nil); err != nil {
		t.Fatal(err)
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	part, err := writer.CreateFormFile("file", "cover.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(jpg.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &form)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Emby-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上传封面: %d %s", resp.StatusCode, raw)
	}
}
