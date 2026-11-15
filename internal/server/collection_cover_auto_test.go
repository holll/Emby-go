package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

// TestCoverAutoDefaultsOn 两个自动触发开关出厂即开启（对齐 Emby 自己给封面出图的行为）。
// 不用 collectionCoverFixture：那个夹具会显式关掉开关。
func TestCoverAutoDefaultsOn(t *testing.T) {
	root := t.TempDir()
	app, err := newApp(config.Config{DBPath: filepath.Join(root, "test.db")}, cache.NewMemory(512))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)
	token := loginFixtureAdmin(t, ts)

	for _, path := range []string{"/api/admin/collection-cover/settings", "/api/admin/library-cover/settings"} {
		status, raw := doJSON(t, ts, "GET", path, token, "")
		if status != http.StatusOK {
			t.Fatalf("get %s: %d %s", path, status, raw)
		}
		var got struct {
			Auto bool `json:"auto"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || !got.Auto {
			t.Fatalf("%s 默认应为开启: %v %s", path, err, raw)
		}
	}
}

// TestCollectionCoverSettingsRoundTrip 开关写入后能读回（夹具初始为关闭）。
func TestCollectionCoverSettingsRoundTrip(t *testing.T) {
	_, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})

	var got struct {
		Auto bool `json:"auto"`
	}
	status, raw := doJSON(t, ts, "GET", "/api/admin/collection-cover/settings", token, "")
	if status != http.StatusOK {
		t.Fatalf("get settings: %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Auto {
		t.Fatalf("夹具初始应为关闭: %v %s", err, raw)
	}

	if status, raw := doJSON(t, ts, "PUT", "/api/admin/collection-cover/settings", token, `{"auto":true}`); status != http.StatusOK {
		t.Fatalf("put settings: %d %s", status, raw)
	}
	_, raw = doJSON(t, ts, "GET", "/api/admin/collection-cover/settings", token, "")
	if err := json.Unmarshal(raw, &got); err != nil || !got.Auto {
		t.Fatalf("写入后应读回开启: %v %s", err, raw)
	}

	// 字段缺失与未知字段都是坏请求，不能静默兜底成关闭。
	for _, body := range []string{`{}`, `{"auto":"yes"}`, `{"autoo":true}`} {
		if status, raw := doJSON(t, ts, "PUT", "/api/admin/collection-cover/settings", token, body); status != http.StatusBadRequest {
			t.Errorf("body %s 应返回 400，得到 %d %s", body, status, raw)
		}
	}
}

// TestCollectionCoverAutoRunsAfterScan 开关打开时，扫描提交了新影片就自动补封面；
// 关着时同样的扫描不产生任何生效记录。
func TestCollectionCoverAutoRunsAfterScan(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
			// 两个分支都显式写一遍，测试不依赖开关的默认值。
			body := `{"auto":false}`
			if enabled {
				body = `{"auto":true}`
			}
			if status, raw := doJSON(t, ts, "PUT", "/api/admin/collection-cover/settings", token, body); status != http.StatusOK {
				t.Fatalf("set auto: %d %s", status, raw)
			}
			addCollectionMember(t, app, "d")
			if status, raw := doJSON(t, ts, "POST", "/api/admin/scan", token, ""); status != http.StatusOK {
				t.Fatalf("scan: %d %s", status, raw)
			}
			waitCollectionCoverTask(t, app)

			_, applied, err := app.db.AppliedCover("系列A")
			if err != nil {
				t.Fatalf("读生效记录失败: %v", err)
			}
			if applied != enabled {
				t.Fatalf("enabled=%v 时 applied=%v，与开关不符", enabled, applied)
			}
		})
	}
}

// TestCollectionCoverAutoSkipsUnchanged 源图与参数都没变时不重写生效记录。
func TestCollectionCoverAutoSkipsUnchanged(t *testing.T) {
	app, _, _ := collectionCoverFixture(t, map[string]bool{"a": true, "b": true})
	opts := coverOptions{Apply: true}
	first, err := app.generateCollectionCover(context.Background(), "系列A", opts)
	if err != nil || !first.Applied {
		t.Fatalf("首次生成失败: %+v err=%v", first, err)
	}
	// 预置一个明显旧的 AppliedAt：内容没变时连记录都不该重写，只更新 Reason。
	sentinel := "2001-01-01T00:00:00Z"
	applied, ok, err := app.db.AppliedCover("系列A")
	if err != nil || !ok {
		t.Fatalf("读生效记录失败: ok=%v err=%v", ok, err)
	}
	applied.AppliedAt = sentinel
	if err := app.db.SetAppliedCover(applied); err != nil {
		t.Fatal(err)
	}
	second, err := app.generateCollectionCover(context.Background(), "系列A", opts)
	if err != nil {
		t.Fatalf("二次生成失败: %v", err)
	}
	if !second.Applied || second.Reason != "up to date" {
		t.Errorf("内容未变时应判 up to date，得到 %+v", second)
	}
	if second.SourceHash != first.SourceHash {
		t.Errorf("同一输入应得到同一 SourceHash：%s vs %s", first.SourceHash, second.SourceHash)
	}
	if again, ok, err := app.db.AppliedCover("系列A"); err != nil || !ok || again.AppliedAt != sentinel {
		t.Errorf("内容未变时不应重写生效记录: %+v ok=%v err=%v", again, ok, err)
	}
}

// TestMaybeAutoCoversMergesCollectionAndLibraryTargets 合集与媒体库目标必须合成一个任务：
// 分两次启动时第二次必然撞上 errCoverTaskBusy，那批封面会被整批静默跳过（自动触发不排队）。
func TestMaybeAutoCoversMergesCollectionAndLibraryTargets(t *testing.T) {
	app, ts, token := collectionCoverFixture(t, map[string]bool{"a": true, "b": true, "c": true})
	for _, path := range []string{"/api/admin/collection-cover/settings", "/api/admin/library-cover/settings"} {
		if status, raw := doJSON(t, ts, "PUT", path, token, `{"auto":true}`); status != http.StatusOK {
			t.Fatalf("开启自动封面 %s: %d %s", path, status, raw)
		}
	}
	app.maybeAutoCovers()
	s := waitCollectionCoverTask(t, app)
	if s.Kind != "mixed" {
		t.Errorf("合集+媒体库目标应合成 mixed 任务，得到 %q", s.Kind)
	}
	if s.Done != 2 || s.Failed != 0 {
		t.Errorf("两个目标都该生成成功: %+v", s)
	}
	if _, ok, err := app.db.AppliedCover("系列A"); err != nil || !ok {
		t.Errorf("合集封面应写库: ok=%v err=%v", ok, err)
	}
	if covers, err := app.db.AppliedLibraryCovers(); err != nil || len(covers) != 1 {
		t.Errorf("媒体库封面应写库: %v err=%v", covers, err)
	}
}

// addCollectionMember 往媒体库里加一部带海报的新片，制造一次真实的索引变更。
func addCollectionMember(t *testing.T, app *App, name string) {
	t.Helper()
	libs, err := app.db.Libraries()
	if err != nil || len(libs) == 0 {
		t.Fatalf("取媒体库失败: %v", err)
	}
	dir := filepath.Join(libs[0].Path, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, name+".strm"), "http://media.test/"+name+".mp4\n")
	writeFile(t, filepath.Join(dir, name+".nfo"), "<movie><title>"+name+"</title><set><name>系列A</name></set></movie>\n")
	writePNGImage(t, filepath.Join(dir, "poster.png"), 320, 480)
}
