package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
