package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

// 本文件按 /root/src/EmbyRestApiDocs 的端点契约验证 §4.21 补齐的端点。
// 断言只针对「形状与状态码」，不断言业务语义——这些端点多数返回空集合，
// 真正的价值是「不再 404，客户端页面不报错」。

// TestEmbyDocEmptyCollectionEndpoints 覆盖文档有、本项目结构上无数据的端点。
// 全部必须 200 且返回空集合，而不是 404。
func TestEmbyDocEmptyCollectionEndpoints(t *testing.T) {
	_, ts, token := contractEnv(t)
	queryResults := []string{
		"/Users/1/Items/1/Intros",
		"/Users/1/Items/1/LocalTrailers",
		"/Users/1/Items/1/SpecialFeatures",
		"/Shows/1/Seasons",
		"/Shows/1/Episodes",
		"/Shows/Upcoming",
	}
	for _, path := range queryResults {
		resp, body := embyRaw(t, ts, http.MethodGet, path, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d, want 200 (%s)", path, resp.StatusCode, body)
		}
		var out struct {
			Items            []any
			TotalRecordCount int
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, body)
		}
		if len(out.Items) != 0 || out.TotalRecordCount != 0 {
			t.Fatalf("%s 应为空 QueryResult，得到 %s", path, body)
		}
	}
}

// TestEmbyDocThemeMediaShape 校验 AllThemeMediaResult 与 ThemeMediaResult 三段/单段形状。
func TestEmbyDocThemeMediaShape(t *testing.T) {
	_, ts, token := contractEnv(t)

	resp, body := embyRaw(t, ts, http.MethodGet, "/Items/1/ThemeMedia", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Items/1/ThemeMedia = %d (%s)", resp.StatusCode, body)
	}
	var all map[string]struct {
		OwnerId          int64
		Items            []any
		TotalRecordCount int
	}
	if err := json.Unmarshal(body, &all); err != nil {
		t.Fatalf("ThemeMedia: %v (%s)", err, body)
	}
	for _, key := range []string{"ThemeVideosResult", "ThemeSongsResult", "SoundtrackSongsResult"} {
		part, ok := all[key]
		if !ok {
			t.Fatalf("ThemeMedia 缺字段 %s：%s", key, body)
		}
		if len(part.Items) != 0 || part.TotalRecordCount != 0 {
			t.Fatalf("ThemeMedia.%s 应为空，得到 %+v", key, part)
		}
	}

	for _, path := range []string{"/Items/1/ThemeSongs", "/Items/1/ThemeVideos"} {
		resp, body := embyRaw(t, ts, http.MethodGet, path, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d (%s)", path, resp.StatusCode, body)
		}
		var one struct {
			Items            []any
			TotalRecordCount int
		}
		if err := json.Unmarshal(body, &one); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, body)
		}
		if len(one.Items) != 0 {
			t.Fatalf("%s 应为空，得到 %s", path, body)
		}
	}
}

// TestEmbyDocReusedEntityEndpoints 校验复用实体聚合的端点能给出真实数据。
func TestEmbyDocReusedEntityEndpoints(t *testing.T) {
	_, ts, token := contractEnv(t)

	// /Persons 在契约 fixture 里有演员数据，必须非空（证明真的走了聚合）。
	resp, body := embyRaw(t, ts, http.MethodGet, "/Persons", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Persons = %d (%s)", resp.StatusCode, body)
	}
	var persons struct {
		Items            []map[string]any
		TotalRecordCount int
	}
	if err := json.Unmarshal(body, &persons); err != nil {
		t.Fatalf("/Persons: %v (%s)", err, body)
	}
	if persons.TotalRecordCount == 0 || len(persons.Items) == 0 {
		t.Fatalf("/Persons 应有演员，得到 %s", body)
	}
	if persons.Items[0]["Type"] != "Person" {
		t.Fatalf("/Persons 的 Type 应为 Person，得到 %v", persons.Items[0]["Type"])
	}

	// /Genres/{Name} 返回单个 BaseItemDto；不存在的流派必须 404。
	resp, body = embyRaw(t, ts, http.MethodGet, "/Genres/剧情", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Genres/剧情 = %d (%s)", resp.StatusCode, body)
	}
	var genre map[string]any
	if err := json.Unmarshal(body, &genre); err != nil {
		t.Fatalf("/Genres/剧情: %v (%s)", err, body)
	}
	if genre["Type"] != "Genre" || genre["Name"] != "剧情" {
		t.Fatalf("/Genres/剧情 形状不对：%s", body)
	}
	resp, _ = embyRaw(t, ts, http.MethodGet, "/Genres/不存在的流派", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/Genres/不存在的流派 = %d, want 404", resp.StatusCode)
	}

	// /Years 从 movies.year 聚合，fixture 是 2024。
	resp, body = embyRaw(t, ts, http.MethodGet, "/Years", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Years = %d (%s)", resp.StatusCode, body)
	}
	var years []map[string]any
	if err := json.Unmarshal(body, &years); err != nil {
		t.Fatalf("/Years: %v (%s)", err, body)
	}
	found := false
	for _, y := range years {
		if y["Name"] == "2024" {
			found = true
		}
	}
	if !found {
		t.Fatalf("/Years 应含 2024，得到 %s", body)
	}

	// /Library/MediaFolders 与 /Users/{uid}/Views 同源，但必须是 QueryResult。
	resp, body = embyRaw(t, ts, http.MethodGet, "/Library/MediaFolders", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Library/MediaFolders = %d (%s)", resp.StatusCode, body)
	}
	var folders struct {
		Items            []map[string]any
		TotalRecordCount int
	}
	if err := json.Unmarshal(body, &folders); err != nil {
		t.Fatalf("/Library/MediaFolders: %v (%s)", err, body)
	}
	if folders.TotalRecordCount == 0 {
		t.Fatalf("/Library/MediaFolders 应有媒体库，得到 %s", body)
	}
}

// TestEmbyDocRootAndAncestors 校验 /Users/{uid}/Items/Root 与 /Items/{Id}/Ancestors。
func TestEmbyDocRootAndAncestors(t *testing.T) {
	_, ts, token := contractEnv(t)

	resp, body := embyRaw(t, ts, http.MethodGet, "/Users/1/Items/Root", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Users/1/Items/Root = %d (%s)", resp.StatusCode, body)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("Root: %v (%s)", err, body)
	}
	if root["IsFolder"] != true {
		t.Fatalf("Root 必须是文件夹：%s", body)
	}

	// 祖先链：影片 → 媒体库（fixture 无合集，因此只有一项）。
	resp, body = embyRaw(t, ts, http.MethodGet, "/Items/1/Ancestors", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Items/1/Ancestors = %d (%s)", resp.StatusCode, body)
	}
	var ancestors []map[string]any
	if err := json.Unmarshal(body, &ancestors); err != nil {
		t.Fatalf("Ancestors: %v (%s)", err, body)
	}
	if len(ancestors) != 1 || ancestors[0]["Type"] != "CollectionFolder" {
		t.Fatalf("Ancestors 应为单个 CollectionFolder，得到 %s", body)
	}

	resp, _ = embyRaw(t, ts, http.MethodGet, "/Items/99999/Ancestors", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/Items/99999/Ancestors = %d, want 404", resp.StatusCode)
	}
}

// TestEmbyDocUserDataReadWrite 校验通用 UserData 端点：
// POST 应用 body 字段（200 无返回内容），GET 返回 UserItemDataDto。
func TestEmbyDocUserDataReadWrite(t *testing.T) {
	_, ts, token := contractEnv(t)

	resp, body := embyRaw(t, ts, http.MethodPost, "/Users/1/Items/1/UserData",
		token, `{"Played":true,"IsFavorite":true,"Rating":8}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST UserData = %d (%s)", resp.StatusCode, body)
	}
	if len(body) != 0 {
		t.Fatalf("POST UserData 应无返回内容，得到 %s", body)
	}

	resp, body = embyRaw(t, ts, http.MethodGet, "/Users/1/Items/1/UserData", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET UserData = %d (%s)", resp.StatusCode, body)
	}
	var ud map[string]any
	if err := json.Unmarshal(body, &ud); err != nil {
		t.Fatalf("GET UserData: %v (%s)", err, body)
	}
	if ud["Played"] != true || ud["IsFavorite"] != true {
		t.Fatalf("UserData 未应用 Played/IsFavorite：%s", body)
	}
	// Rating 8 > 5 → 内部记为「赞」→ 回读 10（ratingFromLikes 的逆映射）。
	if ud["Rating"] != float64(10) {
		t.Fatalf("Rating 应回读 10，得到 %v", ud["Rating"])
	}

	// 踩：Rating 1 → 内部 -1 → 回读 1。
	resp, _ = embyRaw(t, ts, http.MethodPost, "/Users/1/Items/1/UserData", token, `{"Rating":1}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST UserData(Rating=1) = %d", resp.StatusCode)
	}
	_, body = embyRaw(t, ts, http.MethodGet, "/Users/1/Items/1/UserData", token, "")
	if err := json.Unmarshal(body, &ud); err != nil {
		t.Fatalf("GET UserData: %v (%s)", err, body)
	}
	if ud["Rating"] != float64(1) {
		t.Fatalf("Rating 应回读 1，得到 %v", ud["Rating"])
	}

	resp, _ = embyRaw(t, ts, http.MethodGet, "/Users/1/Items/99999/UserData", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET UserData(不存在) = %d, want 404", resp.StatusCode)
	}
}

// TestEmbyDocLegacyPlayingItems 校验旧式上报族 4 条与 /Sessions/Playing* 等价可用。
func TestEmbyDocLegacyPlayingItems(t *testing.T) {
	_, ts, token := contractEnv(t)

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/Users/1/PlayingItems/1?PlaySessionId=legacy-1&PositionTicks=10000000"},
		{http.MethodPost, "/Users/1/PlayingItems/1/Progress?PlaySessionId=legacy-1&PositionTicks=20000000"},
		{http.MethodPost, "/Users/1/PlayingItems/1/Delete?PlaySessionId=legacy-1&PositionTicks=30000000"},
		{http.MethodDelete, "/Users/1/PlayingItems/1?PlaySessionId=legacy-2"},
	} {
		resp, body := embyRaw(t, ts, c.method, c.path, token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = %d (%s)", c.method, c.path, resp.StatusCode, body)
		}
	}

	// 旧族写入的进度应体现在通用 UserData 上（两族共用同一份落库路径）。
	resp, body := embyRaw(t, ts, http.MethodGet, "/Users/1/Items/1/UserData", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET UserData = %d (%s)", resp.StatusCode, body)
	}
	var ud map[string]any
	if err := json.Unmarshal(body, &ud); err != nil {
		t.Fatalf("GET UserData: %v (%s)", err, body)
	}
	if ud["PlayCount"] == float64(0) {
		t.Fatalf("旧式 Stopped 应累加播放次数，得到 %s", body)
	}

	resp, _ = embyRaw(t, ts, http.MethodPost, "/Users/1/PlayingItems/99999?PlaySessionId=x", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("PlayingItems(不存在) = %d, want 404", resp.StatusCode)
	}
}

// TestEmbyDocSessionsAndAPIKeys 校验 /Sessions 与 /Auth/Keys 三条。
func TestEmbyDocSessionsAndAPIKeys(t *testing.T) {
	_, ts, token := contractEnv(t)

	// 无播放时 /Sessions 是空数组（不是 null）。
	resp, body := embyRaw(t, ts, http.MethodGet, "/Sessions", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Sessions = %d (%s)", resp.StatusCode, body)
	}
	if string(body) != "[]" {
		t.Fatalf("/Sessions 应为空数组，得到 %s", body)
	}

	// 开一次播放后 /Sessions 应出现一条，且带 NowPlayingItem。
	embyRaw(t, ts, http.MethodPost, "/Users/1/PlayingItems/1?PlaySessionId=session-a&PositionTicks=5000000", token, "")
	resp, body = embyRaw(t, ts, http.MethodGet, "/Sessions", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/Sessions = %d (%s)", resp.StatusCode, body)
	}
	var sessions []map[string]any
	if err := json.Unmarshal(body, &sessions); err != nil {
		t.Fatalf("/Sessions: %v (%s)", err, body)
	}
	if len(sessions) != 1 {
		t.Fatalf("/Sessions 应有 1 条，得到 %s", body)
	}
	if sessions[0]["NowPlayingItem"] == nil {
		t.Fatalf("/Sessions 应带 NowPlayingItem，得到 %s", body)
	}

	// API 密钥：列表 → 创建 → 列表含新键 → 删除 → 列表为空。
	resp, body = embyRaw(t, ts, http.MethodGet, "/Auth/Keys", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /Auth/Keys = %d (%s)", resp.StatusCode, body)
	}
	resp, body = embyRaw(t, ts, http.MethodPost, "/Auth/Keys?App=契约测试", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /Auth/Keys = %d (%s)", resp.StatusCode, body)
	}
	_, body = embyRaw(t, ts, http.MethodGet, "/Auth/Keys", token, "")
	var keys []map[string]any
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("GET /Auth/Keys: %v (%s)", err, body)
	}
	if len(keys) != 1 || keys[0]["AppName"] != "契约测试" {
		t.Fatalf("/Auth/Keys 应有 1 个「契约测试」，得到 %s", body)
	}
	id, _ := keys[0]["Id"].(string)
	if id == "" {
		t.Fatalf("/Auth/Keys 的 Id 不能为空：%s", body)
	}
	resp, body = embyRaw(t, ts, http.MethodDelete, "/Auth/Keys/"+id, token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /Auth/Keys/%s = %d (%s)", id, resp.StatusCode, body)
	}
	_, body = embyRaw(t, ts, http.MethodGet, "/Auth/Keys", token, "")
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("GET /Auth/Keys: %v (%s)", err, body)
	}
	if len(keys) != 0 {
		t.Fatalf("删除后 /Auth/Keys 应为空，得到 %s", body)
	}

	// POST /Auth/Keys/{Key}/Delete 是文档里的兼容写法，对不存在的键必须 404。
	resp, _ = embyRaw(t, ts, http.MethodPost, "/Auth/Keys/nonexistent/Delete", token, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /Auth/Keys/nonexistent/Delete = %d, want 404", resp.StatusCode)
	}
}

// TestEmbyDocAdminOnlyEndpoints 校验 /Auth/Keys 三条的管理员限制。
// 非管理员令牌通过「管理员建号 + 该号登录」获得，而不是直接造库记录。
func TestEmbyDocAdminOnlyEndpoints(t *testing.T) {
	_, ts, adminToken := contractEnv(t)
	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/users", adminToken,
		`{"name":"plain-viewer","password":"viewer-pass-1","is_admin":false,"library_ids":[]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("建普通用户 = %d (%s)", resp.StatusCode, body)
	}
	loginResp, loginBody := embyRaw(t, ts, http.MethodPost, "/Users/AuthenticateByName", "",
		`{"Username":"plain-viewer","Pw":"viewer-pass-1"}`)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("普通用户登录 = %d (%s)", loginResp.StatusCode, loginBody)
	}
	var login struct{ AccessToken string }
	if err := json.Unmarshal(loginBody, &login); err != nil || login.AccessToken == "" {
		t.Fatalf("登录响应缺 AccessToken: %s (%v)", loginBody, err)
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/Auth/Keys"},
		{http.MethodPost, "/Auth/Keys?App=x"},
		{http.MethodDelete, "/Auth/Keys/whatever"},
	} {
		resp, body := embyRaw(t, ts, c.method, c.path, login.AccessToken, "")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403 (%s)", c.method, c.path, resp.StatusCode, body)
		}
	}
}
