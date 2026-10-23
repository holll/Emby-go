package server

import (
	"context"
	"emby-go/internal/scanner"
	"encoding/json"
	"io"
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
)

// asyncScanFixture 起真实路由、初始化管理员并建一个含单个影片的空壳目录。
func asyncScanFixture(t *testing.T) (*App, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Test Movie (2020).strm"), []byte("https://example.com/video.mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := newApp(config.Config{DBPath: filepath.Join(t.TempDir(), "tasks.db")}, cache.NewMemory(64))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		app.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.WaitBackground(ctx); err != nil {
			t.Error(err)
		}
		app.Close()
	})
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)

	call := func(method, path, token, body string, header http.Header) (*http.Response, []byte) {
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		for key, values := range header {
			for _, value := range values {
				req.Header.Set(key, value)
			}
		}
		if token != "" {
			req.Header.Set("X-Emby-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}
	if resp, raw := call(http.MethodPost, "/api/auth/initialize", "", `{"Username":"admin","Pw":"password-1234"}`, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("initialize: %d %s", resp.StatusCode, raw)
	}
	_, raw := call(http.MethodPost, "/Users/AuthenticateByName", "", `{"Username":"admin","Pw":"password-1234"}`, nil)
	var auth struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil || auth.AccessToken == "" {
		t.Fatalf("登录失败: %v %s", err, raw)
	}
	libBody := `{"Name":"Async","Path":"` + root + `"}`
	if resp, raw := call(http.MethodPost, "/api/admin/libraries", auth.AccessToken, libBody, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("add library: %d %s", resp.StatusCode, raw)
	}
	return app, ts, auth.AccessToken
}

func TestAsyncScanAcceptsAndCompletes(t *testing.T) {
	_, ts, token := asyncScanFixture(t)
	client := ts.Client()
	post := func(header http.Header) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/tasks/scan", nil)
		req.Header.Set("X-Emby-Token", token)
		for key, values := range header {
			req.Header.Set(key, values[0])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, raw
	}
	resp, raw := post(nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("异步扫描应 202，得到 %d %s", resp.StatusCode, raw)
	}
	var created struct {
		TaskID int64  `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.TaskID == 0 || created.Status != "queued" {
		t.Fatalf("202 响应非法: %v %s", err, raw)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/tasks/"+strconv.FormatInt(created.TaskID, 10), nil)
		req.Header.Set("X-Emby-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("查询任务: %d %s", resp.StatusCode, raw)
		}
		var task struct {
			Status string `json:"status"`
			Result *struct {
				Success int `json:"success"`
				Pending int `json:"pending"`
				Failed  int `json:"failed"`
			} `json:"result"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Fatal(err)
		}
		if task.Status == "succeeded" || task.Status == "failed" || task.Status == "skipped" {
			if task.Status != "succeeded" {
				t.Fatalf("任务未成功: %s %s", task.Status, task.Error)
			}
			if task.Result == nil || task.Result.Success+task.Result.Pending+task.Result.Failed < 1 {
				t.Fatalf("任务结果缺少扫描计数: %s", raw)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务超时未完成: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAsyncScanIdempotencyKeyReplays(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	submit := func() (int64, bool) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/tasks/scan", nil)
		req.Header.Set("X-Emby-Token", token)
		req.Header.Set("Idempotency-Key", "fixture-scan-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			TaskID   int64 `json:"task_id"`
			Replayed bool  `json:"replayed"`
		}
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s", raw)
		}
		return body.TaskID, body.Replayed
	}
	first, replayed := submit()
	if first == 0 || replayed {
		t.Fatalf("首次提交应新建任务: id=%d replayed=%v", first, replayed)
	}
	second, replayed := submit()
	if second != first || !replayed {
		t.Fatalf("同幂等键应返回同一任务: first=%d second=%d replayed=%v", first, second, replayed)
	}
	// 等待完成后再重放，仍应返回同一任务且状态已推进。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if task, err := app.scanTaskByID(first); err == nil && task.finished() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("任务未在期限内完成")
		}
		time.Sleep(20 * time.Millisecond)
	}
	third, replayed := submit()
	if third != first || !replayed {
		t.Fatalf("完成后重放应命中: %d %v", third, replayed)
	}
	if task, _ := app.scanTaskByID(first); task.Status != "succeeded" {
		t.Fatalf("重放后状态异常: %s", task.Status)
	}
}

func TestAsyncScanRejectsWhileBusy(t *testing.T) {
	app, ts, token := asyncScanFixture(t)
	if !app.beginScan(1, 0, scanner.ScanModeFull) {
		t.Fatal("测试夹具占用扫描状态失败")
	}
	defer app.endScan(nil)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/tasks/scan", nil)
	req.Header.Set("X-Emby-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("扫描进行中应 409，得到 %d %s", resp.StatusCode, raw)
	}
}

func TestScanTaskRegistryCapacity(t *testing.T) {
	app, _, _ := asyncScanFixture(t)
	now := time.Now()
	for i := int64(1); i <= scanTaskCapacity; i++ {
		if _, _, err := app.registerScanTask(i, "scan", 0, scanner.ScanModeFull, false, "", "", now); err != nil {
			t.Fatalf("登记 %d: %v", i, err)
		}
		app.finishScanTask(i, "succeeded", nil, nil)
	}
	if _, _, err := app.registerScanTask(scanTaskCapacity+1, "scan", 0, scanner.ScanModeFull, false, "", "", now); err != nil {
		t.Fatalf("满容有已完成任务时应淘汰最旧: %v", err)
	}
	if _, err := app.scanTaskByID(1); err == nil {
		t.Fatal("最旧任务应被淘汰")
	}
	if _, err := app.scanTaskByID(scanTaskCapacity + 1); err != nil {
		t.Fatal("新任务应保留")
	}

	// 全部在跑且满容：拒绝。
	allRunning, _, _ := asyncScanFixture(t)
	for i := int64(1); i <= scanTaskCapacity; i++ {
		if _, _, err := allRunning.registerScanTask(i, "scan", 0, scanner.ScanModeFull, false, "", "", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := allRunning.registerScanTask(scanTaskCapacity+1, "scan", 0, scanner.ScanModeFull, false, "", "", now); err == nil {
		t.Fatal("满容且全部在跑时应拒绝")
	}
}

func TestScanIdempotencyExpiresAfterOneHour(t *testing.T) {
	app, _, _ := asyncScanFixture(t)
	now := time.Now()
	if _, _, err := app.registerScanTask(7, "scan", 0, scanner.ScanModeFull, false, "", "expiring-key", now); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.replayScanTask("expiring-key", now.Add(59*time.Minute)); !ok {
		t.Fatal("59 分钟内应命中幂等键")
	}
	if _, ok := app.replayScanTask("expiring-key", now.Add(61*time.Minute)); ok {
		t.Fatal("超过 1 小时应失效")
	}
}

func TestScanTaskStatusUnknownIs404(t *testing.T) {
	_, ts, token := asyncScanFixture(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/tasks/99999", nil)
	req.Header.Set("X-Emby-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知任务应 404，得到 %d", resp.StatusCode)
	}
}
