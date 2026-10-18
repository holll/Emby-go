package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// embyDo 发一个带 token 的 Emby 请求；token 为空表示匿名端点。
func embyDo(t *testing.T, ts *httptest.Server, method, path, token, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// decodeEnvelope 断言错误响应是 {error, code} 且 code 精确匹配。
func decodeEnvelope(t *testing.T, status int, raw []byte, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", status, wantStatus, raw)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Error == "" {
		t.Fatalf("错误响应不是含 error 的 JSON: %v %s", err, raw)
	}
	if body.Code != wantCode {
		t.Errorf("code=%q want=%q body=%s", body.Code, wantCode, raw)
	}
}

// compatFixture 在 asyncScanFixture 基础上补一份可索引的 NFO，并同步扫描一次。
func compatFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	a, ts, token := asyncScanFixture(t)
	libs, err := a.db.Libraries()
	if err != nil || len(libs) == 0 {
		t.Fatalf("读取媒体库失败: %v", err)
	}
	nfo := `<movie><title>Contract Movie</title><year>2024</year></movie>`
	if err := os.WriteFile(filepath.Join(libs[0].Path, "Test Movie (2020).nfo"), []byte(nfo), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, raw := embyDo(t, ts, http.MethodPost, "/api/admin/scan", token, ""); status != http.StatusOK {
		t.Fatalf("初始化扫描: %d %s", status, raw)
	}
	return ts, token
}

// TestEmbySystemUserAndViewsShape 抽检系统/用户/视图三组稳定字段（API-CONTRACT §4）。
func TestEmbySystemUserAndViewsShape(t *testing.T) {
	ts, token := compatFixture(t)

	status, raw := embyDo(t, ts, http.MethodGet, "/System/Info/Public", "", "")
	if status != http.StatusOK {
		t.Fatalf("Info/Public: %d %s", status, raw)
	}
	var info struct {
		Id         string `json:"Id"`
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
	}
	if err := json.Unmarshal(raw, &info); err != nil || info.Id == "" || info.ServerName == "" || info.Version == "" {
		t.Fatalf("Info/Public 缺稳定字段: %v %s", err, raw)
	}

	status, raw = embyDo(t, ts, http.MethodGet, "/Users/Me", token, "")
	if status != http.StatusOK {
		t.Fatalf("Users/Me: %d %s", status, raw)
	}
	var me struct {
		Id   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(raw, &me); err != nil || me.Id == "" || me.Name == "" {
		t.Fatalf("Users/Me 缺 Id/Name: %v %s", err, raw)
	}

	status, raw = embyDo(t, ts, http.MethodGet, "/Users/Public", "", "")
	if status != http.StatusOK {
		t.Fatalf("Users/Public: %d %s", status, raw)
	}
	// 单用户服务不公开用户列表（API-CONTRACT §4）：必须是空数组而非 null。
	var public *[]json.RawMessage
	if err := json.Unmarshal(raw, &public); err != nil || public == nil || len(*public) != 0 {
		t.Fatalf("Users/Public 应为空数组: %v %s", err, raw)
	}

	status, raw = embyDo(t, ts, http.MethodGet, "/Users/1/Views", token, "")
	if status != http.StatusOK {
		t.Fatalf("Views: %d %s", status, raw)
	}
	var views struct {
		Items []struct {
			Id             string `json:"Id"`
			Name           string `json:"Name"`
			CollectionType string `json:"CollectionType"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(raw, &views); err != nil || len(views.Items) == 0 {
		t.Fatalf("Views 无媒体库: %v %s", err, raw)
	}
	for i, v := range views.Items {
		if v.Id == "" || v.Name == "" || v.CollectionType == "" {
			t.Errorf("Views[%d] 缺 Id/Name/CollectionType: %s", i, raw)
		}
	}
	if views.TotalRecordCount != len(views.Items) {
		t.Errorf("Views TotalRecordCount=%d len=%d", views.TotalRecordCount, len(views.Items))
	}
}

// TestEmbyPaginationAndTolerantQueries 越界分页返回空数组而非 null；未知查询参数被忽略。
func TestEmbyPaginationAndTolerantQueries(t *testing.T) {
	ts, token := compatFixture(t)

	status, raw := embyDo(t, ts, http.MethodGet, "/Users/1/Items?Limit=10", token, "")
	if status != http.StatusOK {
		t.Fatalf("Items: %d %s", status, raw)
	}
	var first struct {
		Items            []json.RawMessage `json:"Items"`
		TotalRecordCount int               `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(raw, &first); err != nil || first.TotalRecordCount < 1 || len(first.Items) < 1 {
		t.Fatalf("Items 基本形状: %v %s", err, raw)
	}

	status, raw = embyDo(t, ts, http.MethodGet, "/Users/1/Items?StartIndex=9999&Limit=10", token, "")
	if status != http.StatusOK {
		t.Fatalf("越界分页: %d %s", status, raw)
	}
	var beyond struct {
		Items            *[]json.RawMessage `json:"Items"`
		TotalRecordCount int                `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(raw, &beyond); err != nil || beyond.Items == nil || len(*beyond.Items) != 0 {
		t.Fatalf("越界分页必须返回空数组（非 null）: %v %s", err, raw)
	}
	if beyond.TotalRecordCount != first.TotalRecordCount {
		t.Errorf("越界分页 TotalRecordCount=%d want=%d", beyond.TotalRecordCount, first.TotalRecordCount)
	}

	for _, path := range []string{"/Users/1/Items?Foo=bar&Limit=1", "/Items?Foo=bar", "/Items/1?X=1"} {
		if status, raw := embyDo(t, ts, http.MethodGet, path, token, ""); status != http.StatusOK {
			t.Errorf("%s 应忽略未知参数，得到 %d %s", path, status, raw)
		}
	}
}

// TestEmbyNotFoundAndPayloadEnvelope 404/400 统一带 code；评分未知字段忽略。
func TestEmbyNotFoundAndPayloadEnvelope(t *testing.T) {
	ts, token := compatFixture(t)

	cases := []struct{ path, code string }{
		{"/Items/999999", "NOT_FOUND"},
		{"/Users/42/Items", "NOT_FOUND"},
		{"/Users/1/Items/not-a-number", "NOT_FOUND"},
		{"/Items/999999/PlaybackInfo", "NOT_FOUND"},
	}
	for _, tc := range cases {
		status, raw := embyDo(t, ts, http.MethodGet, tc.path, token, "")
		decodeEnvelope(t, status, raw, http.StatusNotFound, tc.code)
	}

	status, raw := embyDo(t, ts, http.MethodPost, "/Sessions/Playing", token, "{")
	decodeEnvelope(t, status, raw, http.StatusBadRequest, "INVALID_REQUEST")

	status, raw = embyDo(t, ts, http.MethodPost, "/Users/1/Items/1/Rating?Likes=maybe", token, "")
	decodeEnvelope(t, status, raw, http.StatusBadRequest, "INVALID_REQUEST")

	status, raw = embyDo(t, ts, http.MethodPost, "/Users/1/Items/1/Rating?Likes=true&Unknown=1", token, "")
	if status != http.StatusOK {
		t.Fatalf("带未知查询参数的评分应被接受: %d %s", status, raw)
	}
	var rating struct {
		Likes *bool `json:"Likes"`
	}
	if err := json.Unmarshal(raw, &rating); err != nil || rating.Likes == nil || !*rating.Likes {
		t.Fatalf("评分响应 Likes 不符: %v %s", err, raw)
	}
	// 详情 UserData 也要回显 Likes（客户端据此渲染赞/踩状态）。
	status, raw = embyDo(t, ts, http.MethodGet, "/Items/1", token, "")
	if status != http.StatusOK {
		t.Fatalf("详情: %d %s", status, raw)
	}
	var detail struct {
		UserData struct {
			Likes *bool `json:"Likes"`
		} `json:"UserData"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil || detail.UserData.Likes == nil || !*detail.UserData.Likes {
		t.Fatalf("详情 UserData.Likes 不符: %v %s", err, raw)
	}

	// 图片端点对缺失资源返回空 404（浏览器 <img> 场景，不套 JSON 信封）。
	if status, _ := embyDo(t, ts, http.MethodGet, "/Items/1/Images/Nope", "", ""); status != http.StatusNotFound {
		t.Errorf("未知图片类型应 404，得到 %d", status)
	}
}
