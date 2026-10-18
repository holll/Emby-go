package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUserLibraryVisibilityEnforced 验证批次 87：默认无授权看不见任何库，
// 授权后只看见授权库，未授权的详情/播放一律 404；管理员不受限。
func TestUserLibraryVisibilityEnforced(t *testing.T) {
	ts, adminToken := compatFixture(t)

	// 第二个媒体库：独立目录 + 一个影片，随后扫描。
	secondRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(secondRoot, "Second (2021).strm"), []byte("https://example.com/second.mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "Second (2021).nfo"), []byte(`<movie><title>Second Movie</title><year>2021</year></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/libraries", adminToken, `{"Name":"Second","Path":"`+secondRoot+`"}`); status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("添加第二个库失败: %d %s", status, raw)
	}
	if status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/scan", adminToken, ""); status != http.StatusOK {
		t.Fatalf("扫描失败: %d %s", status, raw)
	}

	// 创建 viewer，暂不授权。
	if status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/users", adminToken, `{"name":"visitor","password":"visitor-pass-1","is_admin":false,"library_ids":[]}`); status != http.StatusCreated {
		t.Fatalf("创建用户失败: %d %s", status, raw)
	}
	status, raw := embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", `{"Username":"visitor","Pw":"visitor-pass-1"}`)
	if status != http.StatusOK {
		t.Fatalf("登录失败: %d %s", status, raw)
	}
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(raw, &login); err != nil || login.AccessToken == "" {
		t.Fatalf("登录响应异常: %s", raw)
	}

	emptyList := func(path string) {
		t.Helper()
		status, raw := embyDo(t, ts, http.MethodGet, path, login.AccessToken, "")
		if status != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, status, raw)
		}
		if !containsJSONKey(raw, `"Items":[]`) {
			t.Fatalf("%s 应返回空列表: %s", path, raw)
		}
	}
	emptyList("/Users/" + login.User.ID + "/Items")

	// 管理员仍能看到两个库。
	status, raw = embyDo(t, ts, http.MethodGet, "/Items", adminToken, "")
	if status != http.StatusOK {
		t.Fatalf("管理员列表失败: %d %s", status, raw)
	}
	var adminList struct {
		Total int `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(raw, &adminList); err != nil || adminList.Total < 2 {
		t.Fatalf("管理员应看到至少 2 部影片: %s", raw)
	}

	// 未授权库的详情/播放必须 404。
	if status, _ := embyDo(t, ts, http.MethodGet, "/Items/1", login.AccessToken, ""); status != http.StatusNotFound {
		t.Fatalf("未授权详情应 404，得到 %d", status)
	}
	if status, _ := embyDo(t, ts, http.MethodGet, "/Items/1/PlaybackInfo", login.AccessToken, ""); status != http.StatusNotFound {
		t.Fatalf("未授权 PlaybackInfo 应 404，得到 %d", status)
	}
	if status, _ := embyDo(t, ts, http.MethodGet, "/Videos/1/stream", login.AccessToken, ""); status != http.StatusNotFound {
		t.Fatalf("未授权流应 404，得到 %d", status)
	}

	// 只授权第一个库后，viewer 只看见该库影片。
	if status, raw := embyDo(t, ts, http.MethodPut, "/api/admin/users/2/libraries", adminToken, `{"library_ids":[1]}`); status != http.StatusOK {
		t.Fatalf("授权失败: %d %s", status, raw)
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/Users/"+login.User.ID+"/Items", login.AccessToken, "")
	if status != http.StatusOK {
		t.Fatalf("授权后列表失败: %d %s", status, raw)
	}
	var viewerList struct {
		Total int `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(raw, &viewerList); err != nil || viewerList.Total != 1 {
		t.Fatalf("viewer 应只看到 1 部影片: %s", raw)
	}
	if status, raw := embyDo(t, ts, http.MethodGet, "/Items/1", login.AccessToken, ""); status != http.StatusOK {
		t.Fatalf("授权后详情应 200: %d %s", status, raw)
	}
	if status, _ := embyDo(t, ts, http.MethodGet, "/Users/"+login.User.ID+"/Items?ParentId=2", login.AccessToken, ""); status != http.StatusOK {
		t.Fatalf("未授权库列表应返回空而非错误，得到 %d", status)
	}
}

// containsJSONKey 判断响应里是否含完整片段（用于避免重复解析空列表）。
func containsJSONKey(raw []byte, want string) bool {
	return len(raw) > 0 && len(want) > 0 && strings.Contains(string(raw), want)
}
