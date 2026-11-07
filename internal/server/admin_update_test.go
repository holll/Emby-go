package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/upgrade"
)

// updateTestAPI 模拟 GitHub Release：latest 返回带二进制与 SHA256SUMS 的发布。
// binaryStatus 非 200 时二进制下载失败，用来验证失败状态与「旧程序不动」。
func updateTestAPI(t *testing.T, tag string, binaryStatus int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var checks atomic.Int64
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	asset := "emby-go-" + runtime.GOOS + "-" + runtime.GOARCH
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     tag,
			"body":         "测试版本",
			"published_at": "2026-11-01T00:00:00Z",
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/binary"},
				{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/SHA256SUMS"},
			},
		})
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		if binaryStatus != 0 && binaryStatus != http.StatusOK {
			w.WriteHeader(binaryStatus)
			return
		}
		_, _ = w.Write([]byte("emby-go test asset"))
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  " + asset + "\n"))
	})
	return srv, &checks
}

func useFakeUpdater(t *testing.T, app *App, srv *httptest.Server, current string) {
	t.Helper()
	app.updater = upgrade.New(upgrade.Options{
		Repo: "o/r", APIBase: srv.URL, CurrentVersion: current, Client: srv.Client(),
	})
}

func TestAdminUpdateCheckReturnsAvailability(t *testing.T) {
	app, ts, token := contractEnv(t)
	srv, _ := updateTestAPI(t, "v9.9.9", http.StatusOK)
	useFakeUpdater(t, app, srv, "v1.0.0")

	resp, body := embyRaw(t, ts, http.MethodGet, "/api/admin/update", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/update = %d: %s", resp.StatusCode, body)
	}
	var payload struct {
		Current   string `json:"current"`
		Latest    string `json:"latest"`
		Available bool   `json:"available"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Current != "v1.0.0" || payload.Latest != "v9.9.9" || !payload.Available {
		t.Fatalf("unexpected payload: %s", body)
	}

	statusResp, statusBody := embyRaw(t, ts, http.MethodGet, "/api/admin/update/status", token, "")
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/update/status = %d: %s", statusResp.StatusCode, statusBody)
	}
	var state upgrade.State
	if err := json.Unmarshal(statusBody, &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != upgrade.PhaseIdle {
		t.Fatalf("初始状态应为 idle：%s", statusBody)
	}
	if state.UpdatedAt.IsZero() {
		t.Fatalf("idle 状态应带更新时间：%s", statusBody)
	}

	if resp, _ := embyRaw(t, ts, http.MethodGet, "/api/admin/update", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录访问 = %d，期望 401", resp.StatusCode)
	}
}

func TestAdminUpdateCheckFailureIsNotLatest(t *testing.T) {
	app, ts, token := contractEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	useFakeUpdater(t, app, srv, "v1.0.0")

	resp, body := embyRaw(t, ts, http.MethodGet, "/api/admin/update", token, "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("检查失败应返回 502，实际 %d: %s", resp.StatusCode, body)
	}
}

func TestAdminUpdateApplyRejectsChangedVersion(t *testing.T) {
	app, ts, token := contractEnv(t)
	srv, _ := updateTestAPI(t, "v9.9.9", http.StatusOK)
	useFakeUpdater(t, app, srv, "v1.0.0")

	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/update/apply", token, `{"version":"v9.9.8"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("版本不匹配应返回 409，实际 %d: %s", resp.StatusCode, body)
	}
	if state := app.updater.State(); state.Phase != upgrade.PhaseIdle {
		t.Fatalf("拒绝后不应启动任务：%+v", state)
	}
}

func TestAdminUpdateApplyRunsAndReportsDownloadFailure(t *testing.T) {
	app, ts, token := contractEnv(t)
	srv, _ := updateTestAPI(t, "v9.9.9", http.StatusNotFound)
	useFakeUpdater(t, app, srv, "v1.0.0")

	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/update/apply", token, `{"version":"v9.9.9"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("apply = %d: %s", resp.StatusCode, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := app.updater.State()
		if state.Phase == upgrade.PhaseFailed {
			if state.Message == "" {
				t.Fatalf("失败状态缺少原因：%+v", state)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待失败状态超时：%+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// viewerToken 管理员建一个普通账户，再用它登录取令牌；用于验证更新接口的权限边界。
func viewerToken(t *testing.T, ts *httptest.Server, adminToken string) string {
	t.Helper()
	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/users", adminToken,
		`{"name":"viewer","password":"password-1234"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create viewer = %d: %s", resp.StatusCode, body)
	}
	resp, body = embyRaw(t, ts, http.MethodPost, "/Users/AuthenticateByName", "",
		`{"Username":"viewer","Pw":"password-1234"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login viewer = %d: %s", resp.StatusCode, body)
	}
	var login struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatal(err)
	}
	if login.AccessToken == "" {
		t.Fatalf("viewer login returned no token: %s", body)
	}
	return login.AccessToken
}

func waitUpdaterPhase(t *testing.T, app *App, phase string) upgrade.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := app.updater.State()
		if state.Phase == phase {
			if state.UpdatedAt.IsZero() {
				t.Fatalf("phase %s has zero updated_at: %+v", phase, state)
			}
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %s 超时：%+v", phase, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAdminUpdateRejectsNonAdmin(t *testing.T) {
	app, ts, token := contractEnv(t)
	srv, _ := updateTestAPI(t, "v9.9.9", http.StatusOK)
	useFakeUpdater(t, app, srv, "v1.0.0")
	viewer := viewerToken(t, ts, token)

	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/update", ""},
		{http.MethodPost, "/api/admin/update/check", ""},
		{http.MethodPost, "/api/admin/update/apply", `{"version":"v9.9.9"}`},
		{http.MethodGet, "/api/admin/update/status", ""},
	} {
		resp, body := embyRaw(t, ts, req.method, req.path, viewer, req.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s = %d，期望 403: %s", req.method, req.path, resp.StatusCode, body)
		}
	}
	if state := app.updater.State(); state.Phase != upgrade.PhaseIdle {
		t.Fatalf("非管理员不应启动任务：%+v", state)
	}
}

// updateTestAPISlowBinary 让二进制下载停在 downloading 阶段，用于验证并发 apply 被拒绝。
func updateTestAPISlowBinary(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	asset := "emby-go-" + runtime.GOOS + "-" + runtime.GOARCH
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/binary"},
				{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/SHA256SUMS"},
			},
		})
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write([]byte("slow asset"))
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000  " + asset + "\n"))
	})
	return srv
}

func TestAdminUpdateApplySerializesAndAllowsRetryAfterFailure(t *testing.T) {
	app, ts, token := contractEnv(t)
	srv := updateTestAPISlowBinary(t, "v9.9.9")
	useFakeUpdater(t, app, srv, "v1.0.0")

	resp, body := embyRaw(t, ts, http.MethodPost, "/api/admin/update/apply", token, `{"version":"v9.9.9"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("首次 apply = %d: %s", resp.StatusCode, body)
	}
	resp, body = embyRaw(t, ts, http.MethodPost, "/api/admin/update/apply", token, `{"version":"v9.9.9"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("下载中再次 apply = %d，期望 409: %s", resp.StatusCode, body)
	}
	waitUpdaterPhase(t, app, upgrade.PhaseFailed)

	// 失败后旧程序仍在，同一目标允许重新尝试（不自动重试，由管理员再次确认）。
	resp, body = embyRaw(t, ts, http.MethodPost, "/api/admin/update/apply", token, `{"version":"v9.9.9"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("失败后重试 apply = %d: %s", resp.StatusCode, body)
	}
	waitUpdaterPhase(t, app, upgrade.PhaseFailed)
}
