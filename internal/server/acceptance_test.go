package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAcceptanceLive 是「验收集」里可自动化的子集（A03/A05/A14 部分），
// 需要一个已初始化、可登录的**真实运行实例**；未设置 ACCEPTANCE_BASE_URL 时跳过，
// 因此可留在 CI 中而不依赖外部环境。
//
//	ACCEPTANCE_BASE_URL=https://emby.example.com \
//	ACCEPTANCE_USER=admin ACCEPTANCE_PASS=... \
//	go test ./internal/server -run TestAcceptanceLive -v
//
// 手册与剩余人工项见 PLAN-STATUS.md §3。
func TestAcceptanceLive(t *testing.T) {
	base := strings.TrimRight(os.Getenv("ACCEPTANCE_BASE_URL"), "/")
	if base == "" {
		t.Skip("未设置 ACCEPTANCE_BASE_URL，跳过真实实例验收")
	}
	user := os.Getenv("ACCEPTANCE_USER")
	if user == "" {
		user = "admin"
	}
	pass := os.Getenv("ACCEPTANCE_PASS")
	if pass == "" {
		t.Fatal("ACCEPTANCE_PASS 必填")
	}
	client := &acceptanceClient{base: base, http: &http.Client{Timeout: 15 * time.Second}}

	// A03：未认证访问受保护端点必须 401，而不是业务数据或 500。
	if status, body := client.do(t, http.MethodGet, "/api/admin/settings", nil, ""); status != http.StatusUnauthorized {
		t.Fatalf("A03 未认证 GET /api/admin/settings status=%d body=%s", status, body)
	}
	// A14：超限请求体在认证前被 413 拒绝（Content-Length 已知的路径）。
	if status, body := client.do(t, http.MethodPost, "/Users/AuthenticateByName",
		bytes.Repeat([]byte("x"), (1<<20)+1024), ""); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("A14 1MiB+ body status=%d body=%s", status, body)
	}
	// A11：readiness 端点公开且为 200（SIGTERM 期间的 503 由部署侧演练覆盖）。
	if status, body := client.do(t, http.MethodGet, "/api/health", nil, ""); status != http.StatusOK {
		t.Fatalf("A11 /api/health status=%d body=%s", status, body)
	}

	token := client.login(t, user, pass)

	// A05：选一部真实影片，两个会话各停止一次 → PlayCount +2；重放同一停止与
	// 停止后的迟到 Progress 不得再改计数或覆盖停止位置。
	itemID, baseCount, ok := client.pickItem(t, token)
	if !ok {
		t.Skip("实例媒体库为空，A05 播放计数验收需先入库至少一部影片")
	}
	sessionA := fmt.Sprintf("acceptance-%d-a", time.Now().UnixNano())
	sessionB := fmt.Sprintf("acceptance-%d-b", time.Now().UnixNano())
	client.play(t, token, itemID, sessionA, "", 0)
	client.play(t, token, itemID, sessionA, "/Stopped", 120_000_000)
	client.play(t, token, itemID, sessionB, "", 0)
	client.play(t, token, itemID, sessionB, "/Stopped", 120_000_000)
	if got := client.playCount(t, token, itemID); got != baseCount+2 {
		t.Fatalf("A05 双会话后 PlayCount=%d，期望 %d", got, baseCount+2)
	}
	client.play(t, token, itemID, sessionB, "/Stopped", 120_000_000) // 重放
	client.play(t, token, itemID, sessionB, "/Progress", 1_000_000)  // 迟到心跳
	if got := client.playCount(t, token, itemID); got != baseCount+2 {
		t.Fatalf("A05 重放/迟到心跳后 PlayCount=%d，期望 %d", got, baseCount+2)
	}
}

type acceptanceClient struct {
	base  string
	http  *http.Client
	token string
}

func (c *acceptanceClient) do(t *testing.T, method, path string, body []byte, token string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func (c *acceptanceClient) login(t *testing.T, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"Username": user, "Pw": pass})
	status, out := c.do(t, http.MethodPost, "/Users/AuthenticateByName", body, "")
	if status != http.StatusOK {
		t.Fatalf("A03 登录 status=%d body=%s", status, out)
	}
	var parsed struct {
		AccessToken string
	}
	if err := json.Unmarshal(out, &parsed); err != nil || parsed.AccessToken == "" {
		t.Fatalf("登录响应缺少 AccessToken: %s (%v)", out, err)
	}
	return parsed.AccessToken
}

func (c *acceptanceClient) pickItem(t *testing.T, token string) (string, int64, bool) {
	t.Helper()
	status, out := c.do(t, http.MethodGet, "/Users/1/Items?Limit=10", nil, token)
	if status != http.StatusOK {
		t.Fatalf("A05 列表 status=%d body=%s", status, out)
	}
	var parsed struct {
		Items []struct {
			Id       string
			UserData struct{ PlayCount int64 }
		}
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("列表 JSON: %v (%s)", err, out)
	}
	if len(parsed.Items) == 0 {
		return "", 0, false
	}
	return parsed.Items[0].Id, parsed.Items[0].UserData.PlayCount, true
}

func (c *acceptanceClient) play(t *testing.T, token, itemID, sessionID, event string, position int64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"ItemId": itemID, "PositionTicks": position, "PlaySessionId": sessionID})
	status, out := c.do(t, http.MethodPost, "/Sessions/Playing"+event, body, token)
	if status != http.StatusNoContent {
		t.Fatalf("A05 %s status=%d body=%s", event, status, out)
	}
}

func (c *acceptanceClient) playCount(t *testing.T, token, itemID string) int64 {
	t.Helper()
	status, out := c.do(t, http.MethodGet, "/Users/1/Items/"+itemID, nil, token)
	if status != http.StatusOK {
		t.Fatalf("A05 详情 status=%d body=%s", status, out)
	}
	var parsed struct {
		UserData struct{ PlayCount int64 }
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("详情 JSON: %v (%s)", err, out)
	}
	return parsed.UserData.PlayCount
}

// TestAcceptanceConcurrentPlayback（A06 子集）：同一影片、多个会话并发停止时，
// play_count 必须精确加 N，不得丢更新或 500。验证真实实例上的 SQLite 写事务。
//
//	ACCEPTANCE_BASE_URL=... ACCEPTANCE_USER=admin ACCEPTANCE_PASS=... \
//	ACCEPTANCE_CONCURRENCY=8 go test ./internal/server -run TestAcceptanceConcurrentPlayback -v
func TestAcceptanceConcurrentPlayback(t *testing.T) {
	base := strings.TrimRight(os.Getenv("ACCEPTANCE_BASE_URL"), "/")
	if base == "" {
		t.Skip("未设置 ACCEPTANCE_BASE_URL，跳过真实实例验收")
	}
	pass := os.Getenv("ACCEPTANCE_PASS")
	if pass == "" {
		t.Fatal("ACCEPTANCE_PASS 必填")
	}
	concurrency := 8
	if v := os.Getenv("ACCEPTANCE_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 || n > 32 {
			t.Fatalf("ACCEPTANCE_CONCURRENCY 需为 2-32 的整数，得到 %q", v)
		}
		concurrency = n
	}
	client := &acceptanceClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
	token := client.login(t, userOrDefault(), pass)
	itemID, baseCount, ok := client.pickItem(t, token)
	if !ok {
		t.Skip("实例媒体库为空，并发播放计数验收需先入库至少一部影片")
	}

	// 并发停止：每个会话一个独立 PlaySessionId，停在同一位点。
	// 用 error channel 而不是 t.Fatalf，避免在非测试 goroutine 里终止。
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{
				"ItemId":        itemID,
				"PositionTicks": 300_000_000,
				"PlaySessionId": fmt.Sprintf("acceptance-conc-%d-%d", time.Now().UnixNano(), i),
			})
			req, err := http.NewRequest(http.MethodPost, base+"/Sessions/Playing/Stopped", bytes.NewReader(body))
			if err != nil {
				errs <- err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Emby-Token", token)
			resp, err := client.http.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusNoContent {
				errs <- fmt.Errorf("并发停止 status=%d", resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("A06 并发停止失败: %v", err)
		}
	}
	if got := client.playCount(t, token, itemID); got != baseCount+int64(concurrency) {
		t.Fatalf("A06 并发后 PlayCount=%d，期望 %d", got, baseCount+int64(concurrency))
	}
}

func userOrDefault() string {
	if v := os.Getenv("ACCEPTANCE_USER"); v != "" {
		return v
	}
	return "admin"
}

// TestAcceptanceScanTaskAPI（A10 子集）：异步扫描的 202+task_id、状态轮询、
// Idempotency-Key 重放返回同一任务。会触发一次真实媒体库扫描，默认跳过。
//
//	ACCEPTANCE_BASE_URL=... ACCEPTANCE_USER=admin ACCEPTANCE_PASS=... \
//	ACCEPTANCE_ALLOW_SCAN=1 go test ./internal/server -run TestAcceptanceScanTaskAPI -v
func TestAcceptanceScanTaskAPI(t *testing.T) {
	base := strings.TrimRight(os.Getenv("ACCEPTANCE_BASE_URL"), "/")
	if base == "" {
		t.Skip("未设置 ACCEPTANCE_BASE_URL，跳过真实实例验收")
	}
	if os.Getenv("ACCEPTANCE_ALLOW_SCAN") != "1" {
		t.Skip("未设置 ACCEPTANCE_ALLOW_SCAN=1，跳过会触发扫描的验收")
	}
	pass := os.Getenv("ACCEPTANCE_PASS")
	if pass == "" {
		t.Fatal("ACCEPTANCE_PASS 必填")
	}
	client := &acceptanceClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
	token := client.login(t, userOrDefault(), pass)

	status, out := client.do(t, http.MethodGet, "/api/admin/libraries", nil, token)
	if status != http.StatusOK {
		t.Fatalf("A10 媒体库列表 status=%d body=%s", status, out)
	}
	var libs struct {
		Items []struct{ Id int64 }
	}
	if err := json.Unmarshal(out, &libs); err != nil || len(libs.Items) == 0 {
		t.Skipf("实例没有媒体库，跳过扫描任务验收 (%v)", err)
	}
	libraryID := libs.Items[0].Id
	key := fmt.Sprintf("acceptance-scan-%d", time.Now().UnixNano())

	submit := func() (int, int64, bool) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("%s/api/admin/tasks/scan?library_id=%d", base, libraryID), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Emby-Token", token)
		req.Header.Set("Idempotency-Key", key)
		resp, err := client.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var parsed struct {
			TaskID   int64 `json:"task_id"`
			Replayed bool  `json:"replayed"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed.TaskID == 0 {
			t.Fatalf("A10 提交扫描 status=%d body=%s (%v)", resp.StatusCode, raw, err)
		}
		return resp.StatusCode, parsed.TaskID, parsed.Replayed
	}

	code, taskID, replayed := submit()
	if code != http.StatusAccepted || replayed {
		t.Fatalf("A10 首次提交 code=%d replayed=%v，期望 202/false", code, replayed)
	}
	// 轮询到终态（真实扫描可能较慢，上限 60s）。
	deadline := time.Now().Add(60 * time.Second)
	for {
		code, out = client.do(t, http.MethodGet, fmt.Sprintf("/api/admin/tasks/%d", taskID), nil, token)
		if code != http.StatusOK {
			t.Fatalf("A10 状态查询 code=%d body=%s", code, out)
		}
		var task struct {
			TaskID int64  `json:"task_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(out, &task); err != nil {
			t.Fatalf("A10 状态 JSON: %v (%s)", err, out)
		}
		if task.Status == "succeeded" || task.Status == "failed" || task.Status == "skipped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("A10 任务 %d 60s 未到终态，最后状态 %s", taskID, task.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// 幂等重放：同一 key 返回同一 task_id，且标记 replayed。
	code, replayID, replayed := submit()
	if code != http.StatusAccepted || !replayed || replayID != taskID {
		t.Fatalf("A10 重放 code=%d replayed=%v id=%d，期望 202/true/%d", code, replayed, replayID, taskID)
	}
}
