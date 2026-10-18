package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func decodeJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("响应不是 JSON: %v %s", err, raw)
	}
	return body
}

// TestAdminUsersCRUDGrantsAndAudit 覆盖批次 86：用户管理、媒体库授权、登录/播放审计。
func TestAdminUsersCRUDGrantsAndAudit(t *testing.T) {
	ts, adminToken := compatFixture(t)

	status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/users", adminToken, `{"name":"viewer","password":"viewer-pass-1","is_admin":false,"library_ids":[]}`)
	if status != http.StatusCreated {
		t.Fatalf("创建用户 status=%d body=%s", status, raw)
	}
	body := decodeJSONMap(t, raw)
	user := body["user"].(map[string]any)
	if user["name"] != "viewer" || user["is_admin"] != false {
		t.Fatalf("用户字段错误: %s", raw)
	}
	if _, ok := user["password_hash"]; ok {
		t.Fatalf("不应返回密码哈希: %s", raw)
	}
	viewerID := int64(user["id"].(float64))

	// 媒体库默认不可见，分配后回显。
	status, raw = embyDo(t, ts, http.MethodPut, "/api/admin/users/2/libraries", adminToken, `{"library_ids":[1]}`)
	if status != http.StatusOK {
		t.Fatalf("分配媒体库 status=%d body=%s", status, raw)
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/users", adminToken, "")
	if status != http.StatusOK {
		t.Fatalf("用户列表 status=%d body=%s", status, raw)
	}
	var list struct {
		Users []struct {
			ID         int64   `json:"id"`
			Name       string  `json:"name"`
			LibraryIDs []int64 `json:"library_ids"`
		} `json:"users"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Users) != 2 {
		t.Fatalf("用户列表解码失败: %v %s", err, raw)
	}
	found := false
	for _, u := range list.Users {
		if u.ID == viewerID {
			found = true
			if len(u.LibraryIDs) != 1 || u.LibraryIDs[0] != 1 {
				t.Fatalf("媒体库授权未回显: %s", raw)
			}
		}
	}
	if !found {
		t.Fatalf("列表中缺少新用户: %s", raw)
	}

	// 失败登录与成功登录都进入审计。
	if status, _ = embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"wrong-pass-00"}`); status != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，得到 %d", status)
	}
	status, raw = embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"viewer-pass-1"}`)
	if status != http.StatusOK {
		t.Fatalf("登录失败 status=%d body=%s", status, raw)
	}
	var login struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(raw, &login); err != nil || login.AccessToken == "" {
		t.Fatalf("登录响应缺少 token: %s", raw)
	}
	viewerToken := login.AccessToken

	// 非管理员不能访问用户管理。
	if status, _ = embyDo(t, ts, http.MethodGet, "/api/admin/users", viewerToken, ""); status != http.StatusForbidden {
		t.Fatalf("非管理员应 403，得到 %d", status)
	}

	// 播放一次，供活动记录查询。
	if status, raw = embyDo(t, ts, http.MethodPost, "/Sessions/Playing", viewerToken, `{"ItemId":"1","PlaySessionId":"sess-a","PositionTicks":0,"PlayMethod":"DirectPlay"}`); status != http.StatusNoContent {
		t.Fatalf("Playing status=%d body=%s", status, raw)
	}
	if status, raw = embyDo(t, ts, http.MethodPost, "/Sessions/Playing/Stopped", viewerToken, `{"ItemId":"1","PlaySessionId":"sess-a","PositionTicks":0,"PlayMethod":"DirectPlay"}`); status != http.StatusNoContent {
		t.Fatalf("Stopped status=%d body=%s", status, raw)
	}

	// 失败登录不可归因到具体账户（避免用户名枚举），记 user_id=0；成功登录归户。
	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/login-events", adminToken, "")
	if status != http.StatusOK {
		t.Fatalf("登录审计 status=%d body=%s", status, raw)
	}
	var loginEvents struct {
		Events []struct {
			UserID  int64  `json:"user_id"`
			Name    string `json:"name"`
			IP      string `json:"ip"`
			Success bool   `json:"success"`
		} `json:"events"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(raw, &loginEvents); err != nil {
		t.Fatalf("登录审计解码失败: %v %s", err, raw)
	}
	var sawFailure, sawSuccess bool
	for _, e := range loginEvents.Events {
		if e.Name != "viewer" || e.IP == "" {
			continue
		}
		if e.Success {
			if e.UserID != 2 {
				t.Fatalf("成功登录应归户 user_id=2: %s", raw)
			}
			sawSuccess = true
		} else {
			if e.UserID != 0 {
				t.Fatalf("失败登录不应归因到账户: %s", raw)
			}
			sawFailure = true
		}
	}
	if !sawFailure || !sawSuccess {
		t.Fatalf("登录审计应同时包含失败与成功: %s", raw)
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/login-events?user_id=2", adminToken, "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"user_id":2`) {
		t.Fatalf("按用户过滤登录审计失败: %d %s", status, raw)
	}

	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/playback-events?user_id=2", adminToken, "")
	if status != http.StatusOK {
		t.Fatalf("播放审计 status=%d body=%s", status, raw)
	}
	var playback struct {
		Events []struct {
			UserID     int64  `json:"user_id"`
			UserName   string `json:"user_name"`
			MovieID    int64  `json:"movie_id"`
			MovieTitle string `json:"movie_title"`
			Kind       string `json:"kind"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &playback); err != nil || len(playback.Events) == 0 {
		t.Fatalf("播放审计为空或解码失败: %v %s", err, raw)
	}
	first := playback.Events[0]
	if first.UserID != 2 || first.UserName != "viewer" || first.MovieTitle != "Contract Movie" || first.Kind != "stopped" {
		t.Fatalf("播放审计字段错误: %s", raw)
	}

	// 自助改密：验证当前密码，成功后撤销旧会话。
	if status, raw = embyDo(t, ts, http.MethodPost, "/api/auth/password", viewerToken, `{"current_password":"bad-current","new_password":"viewer-pass-2"}`); status != http.StatusBadRequest {
		t.Fatalf("错误当前密码应 400，得到 %d %s", status, raw)
	}
	if status, raw = embyDo(t, ts, http.MethodPost, "/api/auth/password", viewerToken, `{"current_password":"viewer-pass-1","new_password":"viewer-pass-2"}`); status != http.StatusNoContent {
		t.Fatalf("改密 status=%d body=%s", status, raw)
	}
	if status, _ = embyDo(t, ts, http.MethodGet, "/Users/2", viewerToken, ""); status != http.StatusUnauthorized {
		t.Fatalf("改密后旧会话应失效，得到 %d", status)
	}
	if status, _ = embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"viewer-pass-1"}`); status != http.StatusUnauthorized {
		t.Fatalf("旧密码应失效，得到 %d", status)
	}
	if status, _ = embyDo(t, ts, http.MethodPost, "/Users/AuthenticateByName", "", `{"Username":"viewer","Pw":"viewer-pass-2"}`); status != http.StatusOK {
		t.Fatalf("新密码应可登录，得到 %d", status)
	}

	// 删除用户：审计保留，账户消失；不能删除当前管理员。
	if status, _ = embyDo(t, ts, http.MethodDelete, "/api/admin/users/1", adminToken, ""); status != http.StatusBadRequest {
		t.Fatalf("删除当前账户应 400，得到 %d", status)
	}
	if status, _ = embyDo(t, ts, http.MethodDelete, "/api/admin/users/2", adminToken, ""); status != http.StatusNoContent {
		t.Fatalf("删除用户 status=%d", status)
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/login-events?user_id=2", adminToken, "")
	if status != http.StatusOK || !strings.Contains(string(raw), `"total":`) {
		t.Fatalf("删除后登录审计应保留: %d %s", status, raw)
	}
	var after struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(raw, &after); err != nil || after.Total == 0 {
		t.Fatalf("删除用户后审计丢失: %s", raw)
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/playback-events?user_id=2", adminToken, "")
	var playbackAfter struct {
		Events []struct {
			MovieTitle string `json:"movie_title"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &playbackAfter); err != nil || len(playbackAfter.Events) == 0 || playbackAfter.Events[0].MovieTitle != "Contract Movie" {
		t.Fatalf("删除用户后播放审计丢失: %d %s", status, raw)
	}
	status, raw = embyDo(t, ts, http.MethodGet, "/api/admin/users", adminToken, "")
	if status != http.StatusOK || strings.Contains(string(raw), "viewer") {
		t.Fatalf("删除后用户仍存在: %d %s", status, raw)
	}
}
