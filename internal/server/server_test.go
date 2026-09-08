package server

import (
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func TestCoreAPI(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.strm"), []byte("http://media.test/a.mp4\n"), 0644)
	os.WriteFile(filepath.Join(root, "b.strm"), []byte("ed2k://|file|b.mp4|1|abc|/\n"), 0644)
	os.WriteFile(filepath.Join(root, "c.strm"), []byte("https://media.test/c.mp4\n"), 0644)
	os.WriteFile(filepath.Join(root, "a.nfo"), []byte(`<movie><title>Alpha</title><originaltitle>Original Alpha</originaltitle><sorttitle>Alpha Sort</sorttitle><mpaa>JP-18+</mpaa><tagline>Hi there</tagline><year>2024</year><premiered>2024-01-02</premiered><runtime>90</runtime><genre>Drama</genre><genre>BoxSetGenre</genre><studio>Studio X</studio><set><name>Drama Series</name></set><uniqueid type="metatube">M:1</uniqueid><fileinfo><streamdetails><video><codec>h264</codec><width>1920</width><height>1080</height><durationinseconds>5400</durationinseconds></video><audio><codec>aac</codec><channels>2</channels></audio></streamdetails></fileinfo><actor><name>Actor One</name><type>Actor</type><metatubeid>G:%31</metatubeid></actor></movie>`), 0644)
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	for _, base := range []string{"poster", "fanart", "landscape"} {
		file, _ := os.Create(filepath.Join(root, base+".jpg"))
		_ = jpeg.Encode(file, img, nil)
		file.Close()
	}
	db := filepath.Join(root, "test.db")
	a, err := newApp(config.Config{DBPath: db}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	post := func(path, body string) (*http.Response, map[string]any) {
		r, e := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		r.Body.Close()
		return r, v
	}
	r, _ := post("/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`)
	if r.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("uninitialized auth: %d", r.StatusCode)
	}
	r, _ = post("/api/auth/initialize", `{"Username":"admin","Pw":"password-1234"}`)
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d", r.StatusCode)
	}
	r, _ = post("/api/auth/initialize", `{"Username":"other","Pw":"password-1234"}`)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("repeat initialize: %d", r.StatusCode)
	}
	if valid, err := a.db.AuthenticateAdministrator("admin", "password-1234"); err != nil || !valid {
		t.Fatalf("stored admin invalid: valid=%v err=%v", valid, err)
	}
	r, v := post("/Users/AuthenticateByName", `{"Username":"admin","Pw":"password-1234"}`)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("auth: %d body=%v", r.StatusCode, v)
	}
	token := v["AccessToken"].(string)
	formResp, err := http.Post(ts.URL+"/emby/Users/AuthenticateByName", "application/x-www-form-urlencoded", strings.NewReader("Username=admin&Pw=password-1234"))
	if err != nil {
		t.Fatal(err)
	}
	if formResp.StatusCode != http.StatusOK {
		formResp.Body.Close()
		t.Fatalf("prefixed form auth: %d", formResp.StatusCode)
	}
	formResp.Body.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/admin/libraries", strings.NewReader(`{"Name":"test","Path":"`+strings.ReplaceAll(root, `\`, `\\`)+`"}`))
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Content-Type", "application/json")
	r, _ = http.DefaultClient.Do(req)
	if r.StatusCode != 200 {
		t.Fatalf("library: %d", r.StatusCode)
	}
	r.Body.Close()

	req, _ = http.NewRequest("POST", ts.URL+"/api/admin/scan", nil)
	req.Header.Set("X-Emby-Token", token)
	r, _ = http.DefaultClient.Do(req)
	if r.StatusCode != 200 {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("scan: %d body=%s", r.StatusCode, body)
	}
	r.Body.Close()
	req, _ = http.NewRequest("GET", ts.URL+"/Items?UserId=1&Limit=10&SortBy=IsFavoriteOrLiked%2CRandom", nil)
	req.Header.Set("X-Emby-Token", token)
	r, _ = http.DefaultClient.Do(req)
	if r.StatusCode != http.StatusOK {
		r.Body.Close()
		t.Fatalf("root items: %d", r.StatusCode)
	}
	r.Body.Close()
	req, _ = http.NewRequest("GET", ts.URL+"/Users/1/Items?Limit=10", nil)
	req.Header.Set("X-Emby-Token", token)
	r, _ = http.DefaultClient.Do(req)
	var items map[string]any
	json.NewDecoder(r.Body).Decode(&items)
	r.Body.Close()
	if r.StatusCode != 200 || items["TotalRecordCount"].(float64) != 1 {
		t.Fatalf("items: status=%d body=%v", r.StatusCode, items)
	}
	itemReq, _ := http.NewRequest("GET", ts.URL+"/Users/1/Items/1", nil)
	itemReq.Header.Set("X-Emby-Token", token)
	itemResp, _ := http.DefaultClient.Do(itemReq)
	itemBody, _ := io.ReadAll(itemResp.Body)
	itemResp.Body.Close()
	if !strings.Contains(string(itemBody), "Actor One") {
		t.Fatal("actor missing")
	}
	var itemMap map[string]any
	json.Unmarshal(itemBody, &itemMap)
	if itemMap["RunTimeTicks"].(float64) != 54000000000 {
		t.Fatalf("NFO runtime minutes not converted to ticks: %v", itemMap["RunTimeTicks"])
	}
	if itemMap["PremiereDate"] != "2024-01-02T00:00:00.0000000Z" {
		t.Fatalf("PremiereDate not normalized: %v", itemMap["PremiereDate"])
	}
	if itemMap["UserData"] == nil || itemMap["ImageTags"] == nil {
		t.Fatal("UserData/ImageTags must always be present for iPlay")
	}
	if _, ok := itemMap["BackdropImageTags"].([]any); !ok {
		t.Fatalf("BackdropImageTags must be a list: %v", itemMap["BackdropImageTags"])
	}
	if studios, ok := itemMap["Studios"].([]any); !ok || len(studios) != 1 || studios[0].(map[string]any)["Name"] != "Studio X" {
		t.Fatalf("studios must be {Name,Id} objects: %v", itemMap["Studios"])
	}
	if genres, ok := itemMap["Genres"].([]any); !ok || len(genres) < 1 {
		t.Fatalf("genres missing: %v", itemMap["Genres"])
	}
	if itemMap["OfficialRating"] != "JP-18+" {
		t.Fatalf("mpaa not parsed to OfficialRating: %v", itemMap["OfficialRating"])
	}
	if itemMap["SortName"] != "Alpha Sort" {
		t.Fatalf("sorttitle not parsed to SortName: %v", itemMap["SortName"])
	}
	taglines, _ := itemMap["Taglines"].([]any)
	if len(taglines) != 1 || taglines[0] != "Hi there" {
		t.Fatalf("tagline not parsed: %v", itemMap["Taglines"])
	}
	providers, _ := itemMap["ProviderIds"].(map[string]any)
	if providers["metatube"] != "M:1" {
		t.Fatalf("uniqueid metatube not parsed: %v", itemMap["ProviderIds"])
	}
	if _, err := os.Stat(filepath.Join(root, "poster.webp")); err != nil {
		t.Fatal("poster webp missing")
	}
	if _, err := os.Stat(filepath.Join(root, "poster.jpg")); !os.IsNotExist(err) {
		t.Fatal("source jpg was not removed")
	}
	for _, kind := range []string{"Primary", "Backdrop", "Landscape"} {
		imageReq, _ := http.NewRequest("GET", ts.URL+"/Items/1/Images/"+kind, nil)
		imageReq.Header.Set("X-Emby-Token", token)
		imageResp, _ := http.DefaultClient.Do(imageReq)
		if imageResp.StatusCode != 200 {
			t.Fatalf("webp %s: %d", kind, imageResp.StatusCode)
		}
		imageBody, _ := io.ReadAll(imageResp.Body)
		imageResp.Body.Close()
		if len(imageBody) == 0 || imageResp.Header.Get("Content-Type") != "image/webp" {
			t.Fatalf("webp %s response invalid: type=%q size=%d", kind, imageResp.Header.Get("Content-Type"), len(imageBody))
		}
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ = http.NewRequest("GET", ts.URL+"/videos/1/stream", nil)
	req.Header.Set("X-Emby-Token", token)
	r, _ = client.Do(req)
	r.Body.Close()
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "http://media.test/a.mp4" {
		t.Fatalf("stream redirect: status=%d location=%s", r.StatusCode, r.Header.Get("Location"))
	}
	headReq, _ := http.NewRequest("HEAD", ts.URL+"/videos/1/stream", nil)
	headReq.Header.Set("X-Emby-Token", token)
	headResp, _ := client.Do(headReq)
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusFound || headResp.Header.Get("Location") != "http://media.test/a.mp4" {
		t.Fatalf("stream head: status=%d location=%s", headResp.StatusCode, headResp.Header.Get("Location"))
	}
	req, _ = http.NewRequest("GET", ts.URL+"/unknown-probe?x=1", nil)
	r, _ = http.DefaultClient.Do(req)
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("probe: %d", r.StatusCode)
	}
	adminReq, _ := http.NewRequest("POST", ts.URL+"/api/admin/items/manual", strings.NewReader(`{"source_path":"`+strings.ReplaceAll(filepath.Join(root, "c.strm"), `\`, `\\`)+`","source_url":"http://media.test/c.mp4","title":"Charlie","year":2025,"genres":["Drama","CharlieOnly"]}`))
	adminReq.Header.Set("X-Emby-Token", token)
	adminReq.Header.Set("Content-Type", "application/json")
	adminResp, _ := http.DefaultClient.Do(adminReq)
	if adminResp.StatusCode != 200 {
		t.Fatalf("manual: %d", adminResp.StatusCode)
	}
	adminResp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "c.nfo")); err != nil {
		t.Fatal("manual nfo missing")
	}
	editReq, _ := http.NewRequest("PUT", ts.URL+"/api/admin/items/3", strings.NewReader(`{"title":"Charlie Updated"}`))
	editReq.Header.Set("X-Emby-Token", token)
	editReq.Header.Set("Content-Type", "application/json")
	editResp, _ := http.DefaultClient.Do(editReq)
	if editResp.StatusCode != 200 {
		t.Fatalf("edit: %d", editResp.StatusCode)
	}
	editResp.Body.Close()
	assetResp, _ := http.Get(ts.URL + "/web/app.js")
	if assetResp.StatusCode != 200 {
		t.Fatalf("web asset: %d", assetResp.StatusCode)
	}
	assetResp.Body.Close()
	filterReq, _ := http.NewRequest("GET", ts.URL+"/Users/1/Items?Years=2025&Genres=Drama&StartIndex=-10", nil)
	filterReq.Header.Set("X-Emby-Token", token)
	filterResp, _ := http.DefaultClient.Do(filterReq)
	var filterBody map[string]any
	json.NewDecoder(filterResp.Body).Decode(&filterBody)
	filterResp.Body.Close()
	if filterBody["StartIndex"].(float64) != 0 || filterBody["TotalRecordCount"].(float64) != 1 {
		t.Fatalf("emby filters: %v", filterBody)
	}
	pageReq, _ := http.NewRequest("GET", ts.URL+"/Users/1/Items?Years=2025&Genres=Drama&StartIndex=1&Limit=1", nil)
	pageReq.Header.Set("X-Emby-Token", token)
	pageResp, _ := http.DefaultClient.Do(pageReq)
	var pageBody map[string]any
	json.NewDecoder(pageResp.Body).Decode(&pageBody)
	pageResp.Body.Close()
	if pageBody["TotalRecordCount"].(float64) != 1 || len(pageBody["Items"].([]any)) != 0 {
		t.Fatalf("filtered pagination: %v", pageBody)
	}
	statusReq, _ := http.NewRequest("GET", ts.URL+"/api/admin/items?status=incompatible", nil)
	statusReq.Header.Set("X-Emby-Token", token)
	statusResp, _ := http.DefaultClient.Do(statusReq)
	var statusBody map[string]any
	json.NewDecoder(statusResp.Body).Decode(&statusBody)
	statusResp.Body.Close()
	if statusBody["total"].(float64) != 1 {
		t.Fatalf("status filter: %v", statusBody)
	}
	tasksReq, _ := http.NewRequest("GET", ts.URL+"/api/admin/tasks", nil)
	tasksReq.Header.Set("X-Emby-Token", token)
	tasksResp, _ := http.DefaultClient.Do(tasksReq)
	var tasks map[string]any
	json.NewDecoder(tasksResp.Body).Decode(&tasks)
	tasksResp.Body.Close()
	if len(tasks["items"].([]any)) == 0 {
		t.Fatal("tasks unavailable")
	}
	statusItems := statusBody["items"].([]any)
	if statusItems[0].(map[string]any)["source_protocol"] != "ed2k" {
		t.Fatalf("protocol missing: %v", statusItems[0])
	}
	nullStopReq, _ := http.NewRequest("POST", ts.URL+"/emby/Sessions/Playing/Stopped", strings.NewReader("null"))
	nullStopReq.Header.Set("X-Emby-Token", token)
	nullStopReq.Header.Set("Content-Type", "application/json")
	nullStopResp, _ := http.DefaultClient.Do(nullStopReq)
	nullStopResp.Body.Close()
	if nullStopResp.StatusCode != http.StatusNoContent {
		t.Fatalf("null stopped: %d", nullStopResp.StatusCode)
	}
	for i := 0; i < 2; i++ {
		stopReq, _ := http.NewRequest("POST", ts.URL+"/Sessions/Playing/Stopped", strings.NewReader(`{"ItemId":"1","PositionTicks":10}`))
		stopReq.Header.Set("X-Emby-Token", token)
		stopReq.Header.Set("Content-Type", "application/json")
		stopResp, _ := http.DefaultClient.Do(stopReq)
		stopResp.Body.Close()
	}
	detailReq, _ := http.NewRequest("GET", ts.URL+"/Items/1", nil)
	detailReq.Header.Set("X-Emby-Token", token)
	detailResp, _ := http.DefaultClient.Do(detailReq)
	var detail map[string]any
	json.NewDecoder(detailResp.Body).Decode(&detail)
	detailResp.Body.Close()
	if detail["UserData"].(map[string]any)["PlayCount"].(float64) != 1 {
		t.Fatalf("stopped not idempotent: %v", detail)
	}
	for _, path := range []string{"/Users/Me", "/System/Info", "/System/Configuration", "/System/Ext/ServerDomains", "/Genres?UserId=1", "/Users/1/Views"} {
		checkReq, _ := http.NewRequest("GET", ts.URL+path, nil)
		checkReq.Header.Set("X-Emby-Token", token)
		checkResp, _ := http.DefaultClient.Do(checkReq)
		checkResp.Body.Close()
		if checkResp.StatusCode != http.StatusOK {
			t.Fatalf("system endpoint %s: %d", path, checkResp.StatusCode)
		}
	}
	unplayedReq, _ := http.NewRequest("GET", ts.URL+"/Users/1/Items?Filters=IsUnplayed", nil)
	unplayedReq.Header.Set("X-Emby-Token", token)
	unplayedResp, _ := http.DefaultClient.Do(unplayedReq)
	unplayedResp.Body.Close()
	if unplayedResp.StatusCode != http.StatusOK {
		t.Fatalf("unplayed filter: %d", unplayedResp.StatusCode)
	}
	// Latest 需支持媒体库外部 id（libraryIDBase+内部 id）作为 ParentId，否则客户端首页媒体库行恒为空。
	for _, pid := range []string{"1", strconv.FormatInt(libraryIDBase+1, 10)} {
		latestReq, _ := http.NewRequest("GET", ts.URL+"/Users/1/Items/Latest?ParentId="+pid+"&Limit=10", nil)
		latestReq.Header.Set("X-Emby-Token", token)
		latestResp, _ := http.DefaultClient.Do(latestReq)
		var latestItems []any
		json.NewDecoder(latestResp.Body).Decode(&latestItems)
		latestResp.Body.Close()
		if latestResp.StatusCode != http.StatusOK || len(latestItems) == 0 {
			t.Fatalf("latest by parent %s: status=%d items=%d", pid, latestResp.StatusCode, len(latestItems))
		}
	}
	authHeaderReq, _ := http.NewRequest("GET", ts.URL+"/Items/Counts?UserId=1", nil)
	authHeaderReq.Header.Set("X-Emby-Authorization", `Emby UserId="1", Client="test", Token="`+token+`"`)
	authHeaderResp, _ := http.DefaultClient.Do(authHeaderReq)
	authHeaderResp.Body.Close()
	if authHeaderResp.StatusCode != http.StatusOK {
		t.Fatalf("Emby authorization parsing: %d", authHeaderResp.StatusCode)
	}
	streamReq, _ := http.NewRequest("GET", ts.URL+"/Videos/1/stream.mp4?ApiKey="+token, nil)
	streamResp, _ := client.Do(streamReq)
	streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusFound {
		t.Fatalf("uppercase stream: %d", streamResp.StatusCode)
	}
	invalidUserReq, _ := http.NewRequest("GET", ts.URL+"/Users/2/Items", nil)
	invalidUserReq.Header.Set("X-Emby-Token", token)
	invalidUserResp, _ := http.DefaultClient.Do(invalidUserReq)
	invalidUserResp.Body.Close()
	if invalidUserResp.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid user: %d", invalidUserResp.StatusCode)
	}
	playedReq, _ := http.NewRequest("POST", ts.URL+"/Users/1/PlayedItems/1", nil)
	playedReq.Header.Set("X-Emby-Token", token)
	playedResp, _ := http.DefaultClient.Do(playedReq)
	playedResp.Body.Close()
	if playedResp.StatusCode != http.StatusNoContent {
		t.Fatalf("mark played: %d", playedResp.StatusCode)
	}
	probeReq, _ := http.NewRequest("POST", ts.URL+"/Users/1/GroupingOptions", strings.NewReader(`{"probe":true}`))
	probeResp, _ := http.DefaultClient.Do(probeReq)
	probeResp.Body.Close()
	if probeResp.StatusCode != http.StatusNotFound {
		t.Fatalf("probe response: %d", probeResp.StatusCode)
	}
	probeListReq, _ := http.NewRequest("GET", ts.URL+"/api/admin/probe", nil)
	probeListReq.Header.Set("X-Emby-Token", token)
	probeListResp, _ := http.DefaultClient.Do(probeListReq)
	var probeList map[string]any
	json.NewDecoder(probeListResp.Body).Decode(&probeList)
	probeListResp.Body.Close()
	if len(probeList["items"].([]any)) == 0 {
		t.Fatal("probe not recorded")
	}
	reindexReq, _ := http.NewRequest("POST", ts.URL+"/api/admin/reindex", nil)
	reindexReq.Header.Set("X-Emby-Token", token)
	reindexResp, _ := http.DefaultClient.Do(reindexReq)
	reindexResp.Body.Close()
	if reindexResp.StatusCode != http.StatusOK {
		t.Fatalf("reindex: %d", reindexResp.StatusCode)
	}
	get := func(path string) map[string]any {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("X-Emby-Token", token)
		resp, _ := http.DefaultClient.Do(req)
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		return v
	}
	tagBody := get("/Users/1/Items?IncludeItemTypes=Tag&ParentId=1")
	if tagBody["TotalRecordCount"].(float64) != 0 || len(tagBody["Items"].([]any)) != 0 {
		t.Fatalf("tag browse must be empty not movies: %v", tagBody)
	}
	genreBody := get("/Users/1/Items?IncludeItemTypes=Genre&ParentId=1")
	if genreBody["TotalRecordCount"].(float64) < 1 {
		t.Fatalf("genre browse should list Drama: %v", genreBody)
	} else if it := genreBody["Items"].([]any)[0].(map[string]any); it["Type"] != "Genre" {
		t.Fatalf("genre browse item type mismatch: %v", it)
	}
	seriesBody := get("/Users/1/Items?IncludeItemTypes=Series&ParentId=1")
	if seriesBody["TotalRecordCount"].(float64) != 0 || len(seriesBody["Items"].([]any)) != 0 {
		t.Fatalf("unsupported Series browse must be empty: %v", seriesBody)
	}
	playBody := get("/Items/1/PlaybackInfo?UserId=1")
	playJson, _ := json.Marshal(playBody)
	if !strings.Contains(string(playJson), "mediasource_1") || playBody["PlaySessionId"] == nil {
		t.Fatalf("playback info shape mismatch: %s", playJson)
	}
	playSource := playBody["MediaSources"].([]any)[0].(map[string]any)
	if playSource["MediaStreams"] == nil || playSource["DirectStreamUrl"] == nil || playSource["Path"] == nil {
		t.Fatalf("media source must carry Path/DirectStreamUrl/MediaStreams: %s", playJson)
	}
	streams := playSource["MediaStreams"].([]any)
	streamJSON, _ := json.Marshal(streams)
	if !strings.Contains(string(streamJSON), "h264") || !strings.Contains(string(streamJSON), "aac") {
		t.Fatalf("media streams should come from NFO fileinfo: %s", streamJSON)
	}
	// Person/Genre 复合 Id 过滤（iPlay 演员页用 PersonIds）。
	actorId := entityId("Person", "Actor One")
	personBody := get("/Users/1/Items?PersonIds=" + actorId + "&IncludeItemTypes=Movie")
	if personBody["TotalRecordCount"].(float64) != 1 {
		t.Fatalf("PersonIds filter should match Alpha: %v", personBody)
	}
	genreId := entityId("Genre", "Drama")
	genreIdBody := get("/Users/1/Items?GenreIds=" + genreId + "&IncludeItemTypes=Movie")
	if genreIdBody["TotalRecordCount"].(float64) != 2 {
		t.Fatalf("GenreIds filter should match Alpha+Charlie: %v", genreIdBody)
	}
	// —— 合集（BoxSet）：Views 出现「合集」媒体库，BoxSet 列表/子项/范围内类型过滤 ——
	viewsBody := get("/Users/1/Views")
	boxsetViewFound := false
	for _, it := range viewsBody["Items"].([]any) {
		m := it.(map[string]any)
		if m["Id"] == boxsetViewID && m["CollectionType"] == "boxsets" {
			boxsetViewFound = true
		}
	}
	if !boxsetViewFound {
		t.Fatalf("Views should include 合集(boxsets) folder: %v", viewsBody)
	}
	// 媒体库 id 使用外部命名空间，与影片 id(1) 不冲突，且封面用宽图 fanart。
	var avView map[string]any
	for _, it := range viewsBody["Items"].([]any) {
		m := it.(map[string]any)
		if m["CollectionType"] == "movies" && m["Id"] != boxsetViewID {
			avView = m
		}
	}
	if avView == nil {
		t.Fatalf("views missing AV movie library: %v", viewsBody)
	}
	avID, _ := avView["Id"].(string)
	if avID == "1" || avID != externalLibraryID(1) {
		t.Fatalf("library view id should be in external namespace: %v", avView)
	}
	if ratio, ok := avView["PrimaryImageAspectRatio"].(float64); !ok || ratio <= 1 {
		t.Fatalf("library cover should prefer wide fanart (ratio>1): %v", avView)
	}
	if avView["ImageTags"] == nil {
		t.Fatalf("library view should carry cover ImageTags: %v", avView)
	}
	libDetail := get("/Users/1/Items/" + avID)
	if libDetail["Type"] != "CollectionFolder" {
		t.Fatalf("library external detail should be CollectionFolder: %v", libDetail)
	}
	libImgReq, _ := http.NewRequest("GET", ts.URL+"/Items/"+avID+"/Images/Primary", nil)
	libImgReq.Header.Set("X-Emby-Token", token)
	libImgResp, _ := http.DefaultClient.Do(libImgReq)
	libImgResp.Body.Close()
	if libImgResp.StatusCode != http.StatusOK || libImgResp.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("library cover image: status=%d type=%q", libImgResp.StatusCode, libImgResp.Header.Get("Content-Type"))
	}
	boxList := get("/Users/1/Items?ParentId=boxsets&IncludeItemTypes=BoxSet")
	if boxList["TotalRecordCount"].(float64) != 1 {
		t.Fatalf("boxset list should have Drama Series: %v", boxList)
	}
	bsItem := boxList["Items"].([]any)[0].(map[string]any)
	if bsItem["Type"] != "BoxSet" || bsItem["Name"] != "Drama Series" {
		t.Fatalf("boxset item shape: %v", bsItem)
	}
	bsID := bsItem["Id"].(string)
	if bsItem["ImageTags"] == nil {
		t.Fatalf("boxset item should carry a poster: %v", bsItem)
	}
	bsImgReq, _ := http.NewRequest("GET", ts.URL+"/Items/"+bsID+"/Images/Primary", nil)
	bsImgReq.Header.Set("X-Emby-Token", token)
	bsImgResp, _ := http.DefaultClient.Do(bsImgReq)
	bsImgResp.Body.Close()
	if bsImgResp.StatusCode != http.StatusOK || bsImgResp.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("boxset cover image: status=%d type=%q", bsImgResp.StatusCode, bsImgResp.Header.Get("Content-Type"))
	}
	children := get("/Users/1/Items?ParentId=" + bsID + "&IncludeItemTypes=Movie")
	if children["TotalRecordCount"].(float64) != 1 || children["Items"].([]any)[0].(map[string]any)["Id"] != "1" {
		t.Fatalf("boxset children should be Alpha(id=1): %v", children)
	}
	// 合集媒体库范围下类型筛选：只含合集成员的类型，CharlieOnly（不属于任何合集）不得出现。
	scopedGenres := get("/Users/1/Items?ParentId=boxsets&IncludeItemTypes=Genre&Limit=50")
	nameSet := map[string]bool{}
	for _, it := range scopedGenres["Items"].([]any) {
		nameSet[it.(map[string]any)["Name"].(string)] = true
	}
	if !nameSet["BoxSetGenre"] || !nameSet["Drama"] || nameSet["CharlieOnly"] {
		t.Fatalf("boxset-scoped genres should hide non-member types: %v", scopedGenres)
	}
	// 合集媒体库文件夹详情。
	if detail := get("/Users/1/Items/boxsets"); detail["Type"] != "CollectionFolder" {
		t.Fatalf("boxset folder detail: %v", detail)
	}
	// 相似推荐：Alpha 与 Charlie 共享 Drama（+10>2），应返回 Charlie。
	similarBody := get("/Items/1/Similar")
	if similarBody["TotalRecordCount"].(float64) != 1 || similarBody["Items"].([]any)[0].(map[string]any)["Id"] != "3" {
		t.Fatalf("similar should return Charlie by shared genre: %v", similarBody)
	}
	virtualBody := get("/Users/1/Items/genre:Drama")
	if virtualBody["Type"] != "Genre" {
		t.Fatalf("virtual entity detail: %v", virtualBody)
	}
	genreGrid := get("/Genres?UserId=1&ParentId=1")
	firstGenre := genreGrid["Items"].([]any)[0].(map[string]any)
	if firstGenre["ImageTags"] == nil {
		t.Fatalf("genre grid tile should carry ImageTags: %v", firstGenre)
	}
	genreImgReq, _ := http.NewRequest("GET", ts.URL+"/Items/genre:Drama/Images/Primary", nil)
	genreImgReq.Header.Set("X-Emby-Token", token)
	genreImgResp, _ := http.DefaultClient.Do(genreImgReq)
	genreImgBody, _ := io.ReadAll(genreImgResp.Body)
	genreImgResp.Body.Close()
	if genreImgResp.StatusCode != http.StatusOK || genreImgResp.Header.Get("Content-Type") != "image/webp" || len(genreImgBody) == 0 {
		t.Fatalf("genre cover image: status=%d type=%q size=%d", genreImgResp.StatusCode, genreImgResp.Header.Get("Content-Type"), len(genreImgBody))
	}
	// 系统存活/公开信息/搜索占位/显示偏好：这些端点客户端会探，须返回 2xx 而非 404。
	pingResp, err := http.Get(ts.URL + "/System/Ping")
	if err == nil {
		pingBody, _ := io.ReadAll(pingResp.Body)
		pingResp.Body.Close()
		if pingResp.StatusCode != http.StatusOK || strings.TrimSpace(string(pingBody)) != "Emby Server" {
			t.Fatalf("System/Ping: status=%d body=%q", pingResp.StatusCode, pingBody)
		}
	}
	hintsResp, _ := http.NewRequest("GET", ts.URL+"/Search/Hints?SearchTerm=Alpha", nil)
	hintsResp.Header.Set("X-Emby-Token", token)
	if hints, _ := http.DefaultClient.Do(hintsResp); hints.StatusCode != http.StatusOK {
		t.Fatalf("Search/Hints: %d", hints.StatusCode)
	}
	prefBody := get("/DisplayPreferences/usersettings?client=emby&userId=1")
	if prefBody["Id"] == nil || prefBody["Client"] != "emby" {
		t.Fatalf("DisplayPreferences shape: %v", prefBody)
	}
	pubReq, _ := http.NewRequest("GET", ts.URL+"/System/Info/Public", nil)
	pubResp, _ := http.DefaultClient.Do(pubReq)
	var pub map[string]any
	json.NewDecoder(pubResp.Body).Decode(&pub)
	pubResp.Body.Close()
	if _, ok := pub["LocalAddresses"].([]any); !ok || pub["ServerName"] == nil || pub["Version"] == nil {
		t.Fatalf("System/Info/Public shape: %v", pub)
	}
	// 收藏 / 评分 / 隐藏续播
	doUser := func(method, path string, query string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(method, ts.URL+path+"?"+query, nil)
		req.Header.Set("X-Emby-Token", token)
		resp, _ := http.DefaultClient.Do(req)
		var v map[string]any
		if resp.Body != nil {
			json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
		}
		return resp, v
	}
	resp, v := doUser("POST", "/Users/1/FavoriteItems/1", "")
	if resp.StatusCode != http.StatusOK || v["IsFavorite"] != true {
		t.Fatalf("favorite add: status=%d body=%v", resp.StatusCode, v)
	}
	favItems := get("/Users/1/Items?Filters=IsFavorite&ParentId=1")
	if favItems["TotalRecordCount"].(float64) != 1 {
		t.Fatalf("favorite filter: %v", favItems)
	}
	resp, v = doUser("DELETE", "/Users/1/FavoriteItems/1", "")
	if resp.StatusCode != http.StatusOK || v["IsFavorite"] != false {
		t.Fatalf("favorite remove: status=%d body=%v", resp.StatusCode, v)
	}
	if resp, _ := doUser("POST", "/Users/1/Items/1/Rating", "Likes=true"); resp.StatusCode != http.StatusOK {
		t.Fatalf("rating set: %d", resp.StatusCode)
	}
	if resp, _ := doUser("DELETE", "/Users/1/Items/1/Rating", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("rating clear: %d", resp.StatusCode)
	}
	resumeBefore := get("/Users/1/Items/Resume")
	found := false
	for _, it := range resumeBefore["Items"].([]any) {
		if it.(map[string]any)["Id"] == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("item 1 should be resumable before hide: %v", resumeBefore)
	}
	if resp, _ := doUser("POST", "/Users/1/Items/1/HideFromResume", "Hide=true"); resp.StatusCode != http.StatusOK {
		t.Fatalf("hide from resume: %d", resp.StatusCode)
	}
	resumeAfter := get("/Users/1/Items/Resume")
	for _, it := range resumeAfter["Items"].([]any) {
		if it.(map[string]any)["Id"] == "1" {
			t.Fatalf("hidden item still in resume: %v", resumeAfter)
		}
	}
	if resp, _ := doUser("POST", "/Users/1/Items/1/HideFromResume", "Hide=false"); resp.StatusCode != http.StatusOK {
		t.Fatalf("unhide from resume: %d", resp.StatusCode)
	}
}

func TestEntityIDURLSafeRoundTrip(t *testing.T) {
	name := "あら、スケベ/HERO"
	id := entityId("Tag", name)
	if strings.Contains(id, "/") || strings.ContainsAny(id, "?#") {
		t.Fatalf("entity id not URL-safe: %q", id)
	}
	kind, got, ok := entityKind(id)
	if !ok || kind != "Tag" || got != name {
		t.Fatalf("round-trip failed: id=%q kind=%q name=%q ok=%v", id, kind, got, ok)
	}
	// 旧明文 id 兼容
	if kind, got, ok := entityKind("genre:Drama"); !ok || kind != "Genre" || got != "Drama" {
		t.Fatalf("legacy plaintext id failed: %q %q %v", kind, got, ok)
	}
}
