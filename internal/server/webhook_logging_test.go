package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/logging"
	"emby-go/internal/store"
	"emby-go/internal/webhook"
)

// Exercise the real deletion handler and disk log API against local fixtures
// only. A failed notification must never undo deletion or replay the request.
func TestDeleteWebhookFailureDiagnosticsVisibleInDiskLogs(t *testing.T) {
	for _, mode := range []string{"status", "ignored", "shutdown", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdminDeleteFixture(t)
			var calls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "status":
					w.WriteHeader(503)
					w.Write([]byte("private-response-canary"))
				case "timeout":
					io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
				default:
					w.Write([]byte("ignored: emby 删除同步未启用 private-response-canary"))
				}
			}))
			defer target.Close()
			enableDeleteWebhook(t, f, target.URL+"/private-url-canary?token=private-token-canary")
			if mode == "shutdown" {
				f.a.BeginShutdown()
			}
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
			defer slog.SetDefault(previous)
			w := adminDeleteRequest(f, "items", "fixture-admin-delete")
			if w.Code != 204 || w.Header().Get("X-Webhook-Status") != "failed" {
				t.Fatalf("delete response: %d %v", w.Code, w.Header())
			}
			if _, err := f.a.db.Movie(f.victim); !store.NotFound(err) {
				t.Fatal("failed notification undid local deletion")
			}
			wantCalls := int32(1)
			if mode == "shutdown" {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatalf("requests=%d, want %d (must not retry)", calls.Load(), wantCalls)
			}
			// Freeze the captured log before querying the API (whose request logger
			// may produce additional lines). No production files are opened.
			raw := output.String()
			dir := t.TempDir()
			logging.SetAppLogDir(dir)
			defer logging.SetAppLogDir("")
			day := time.Now().Format("2006-01-02")
			if err := os.WriteFile(filepath.Join(dir, "app-"+day+".log"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, "/api/admin/logs/disk?kind=app&limit=50", nil)
			req.Header.Set("X-Emby-Token", "fixture-admin-delete")
			logs := httptest.NewRecorder()
			f.a.Handler().ServeHTTP(logs, req)
			if logs.Code != http.StatusOK {
				t.Fatalf("disk logs: %d %s", logs.Code, logs.Body.String())
			}
			var body struct {
				Items []struct{ Message string } `json:"items"`
			}
			if err := json.Unmarshal(logs.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			code := map[string]string{"status": "status_503", "ignored": "ignored", "shutdown": "canceled", "timeout": "timeout"}[mode]
			found := false
			for _, item := range body.Items {
				if strings.Contains(item.Message, "Webhook 投递失败") {
					for _, want := range []string{"event=item.deleted", fmt.Sprintf("item_id=%d", f.victim), "target_id=", "error_code=" + code, "不要重复删除或自动重发"} {
						if !strings.Contains(item.Message, want) {
							t.Fatalf("diagnostic missing %q in %q", want, item.Message)
						}
					}
					found = true
				}
			}
			if !found {
				t.Fatalf("diagnostic absent from disk log API: %s", logs.Body.String())
			}
			for _, secret := range []string{"private-response-canary", "private-url-canary", "private-token-canary", target.URL} {
				if strings.Contains(raw, secret) || strings.Contains(logs.Body.String(), secret) {
					t.Fatalf("secret leaked into log: %s", secret)
				}
			}
		})
	}
}

func TestWebhookDeliveryResultLogs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  string
		status string
		level  slog.Level
	}{
		{"success", webhook.ItemUpdated, webhook.ResultSent, slog.LevelInfo},
		{"partial", webhook.ItemUpdated, webhook.ResultPartial, slog.LevelWarn},
		{"failed", webhook.PlaybackProgress, webhook.ResultFailed, slog.LevelWarn},
		{"progress", webhook.PlaybackProgress, webhook.ResultSent, slog.LevelDebug},
		{"disabled", webhook.ItemUpdated, webhook.ResultDisabled, slog.LevelDebug},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer slog.SetDefault(previous)
			result := webhook.Result{Status: tc.status}
			if tc.status == webhook.ResultSent || tc.status == webhook.ResultPartial {
				result.Targets = append(result.Targets, webhook.TargetOutcome{ID: "good-target", Name: "secret-target-name", Status: webhook.TargetSent})
			}
			if tc.status == webhook.ResultFailed || tc.status == webhook.ResultPartial {
				result.Targets = append(result.Targets,
					webhook.TargetOutcome{ID: "bad-target", Status: webhook.TargetFailed, ErrorCode: "status_503"},
					webhook.TargetOutcome{ID: "budget-target", Status: webhook.TargetSkippedBudget, ErrorCode: "budget_exhausted"})
			}
			event := webhook.Event{Event: tc.event, Item: webhook.Item{Id: "42", Name: "测试影片", Path: "/media/movie.strm"},
				User: &webhook.User{Id: "1", Name: "管理员"}, Session: &webhook.Session{Id: "secret-session"}}
			logWebhookDelivery(event, result, 123*time.Millisecond)
			decoder := json.NewDecoder(&output)
			var summary struct {
				Level      string `json:"level"`
				Message    string `json:"msg"`
				DurationMS int64  `json:"duration_ms"`
				Sent       int    `json:"sent"`
				Failed     int    `json:"failed"`
				Skipped    int    `json:"skipped_budget"`
			}
			if err := decoder.Decode(&summary); err != nil {
				t.Fatal(err)
			}
			if summary.Level != tc.level.String() || summary.DurationMS != 123 {
				t.Fatalf("unexpected summary: %+v", summary)
			}
			sent, failed, skipped := webhookResultCounts(result)
			if summary.Sent != sent || summary.Failed != failed || summary.Skipped != skipped {
				t.Fatalf("incorrect counts: %+v", summary)
			}
			for _, want := range []string{"event=" + tc.event, "item_id=42", "status=" + tc.status, "duration_ms=123",
				fmt.Sprintf("targets=%d sent=%d failed=%d skipped_budget=%d", len(result.Targets), sent, failed, skipped),
				"通知=" + webhookEventDescription(tc.event), `"Name":"测试影片"`, `"Path":"/media/movie.strm"`, `"Name":"管理员"`} {
				if !strings.Contains(summary.Message, want) {
					t.Fatalf("disk viewer message missing %q: %s", want, summary.Message)
				}
			}
			var records []map[string]any
			for decoder.More() {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}
				records = append(records, record)
			}
			if len(records) != len(result.Targets) {
				t.Fatalf("target logs=%d, want %d", len(records), len(result.Targets))
			}
			for i, record := range records {
				if record["target_id"] != result.Targets[i].ID {
					t.Fatalf("wrong target log: %v", record)
				}
				if result.Targets[i].Status == webhook.TargetSent {
					wantLevel := slog.LevelInfo
					if tc.event == webhook.PlaybackProgress {
						wantLevel = slog.LevelDebug
					}
					if record["level"] != wantLevel.String() || !strings.Contains(record["msg"].(string), "不代表远端业务执行完成") {
						t.Fatalf("incorrect acknowledgment semantics: %v", record)
					}
				} else if record["level"] != slog.LevelWarn.String() {
					t.Fatalf("failure not WARN: %v", record)
				}
			}
			// Inspect a fresh rendering: decoder consumption above changes the buffer.
			output.Reset()
			logWebhookDelivery(event, result, time.Millisecond)
			if strings.Contains(output.String(), "secret") {
				t.Fatalf("private payload or target name leaked: %s", output.String())
			}
		})
	}
}

func TestWebhookNotificationContent(t *testing.T) {
	event := webhook.Event{
		Event: webhook.PlaybackPause,
		Item: webhook.Item{Id: "42", Name: "测试影片", Path: "/media/测试影片/movie.strm", Type: "Movie", LibraryId: "7", RunTimeTicks: 900000000,
			UserData: map[string]any{"IsFavorite": true, "Played": false, "Likes": false, "PlayCount": int64(2), "PlayedPercentage": float64(50), "PlaybackPositionTicks": int64(450000000), "LastPlayedDate": "2026-11-02T11:00:00Z", "token": "secret-userdata", "Unknown": "secret-unknown"}},
		User:         &webhook.User{Id: "1", Name: "管理员"},
		Server:       webhook.Server{Id: "server-1", Name: "家庭媒体库"},
		Timestamp:    time.Date(2026, 11, 2, 19, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		Session:      &webhook.Session{Id: "secret-session", DeviceId: "secret-device", RemoteEndPoint: "secret-ip"},
		PlaybackInfo: &webhook.PlaybackInfo{PositionTicks: 450000000, IsPaused: true, PlaySessionId: "secret-play-session"},
	}
	before, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	content := webhookLogContent(event)
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Event":"playback.pause"`, `"Name":"测试影片"`, `"Path":"/media/测试影片/movie.strm"`, `"LibraryId":"7"`, `"Name":"管理员"`, `"IsFavorite":true`, `"Played":false`, `"Likes":false`, `"PositionSeconds":45`, `"IsPaused":true`, `"Date":"2026-11-02T11:00:00Z"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("notification content missing %s: %s", want, content)
		}
	}
	if strings.Contains(content, "secret") {
		t.Fatalf("unapproved fields leaked: %s", content)
	}
	after, _ := json.Marshal(event)
	if !bytes.Equal(before, after) {
		t.Fatal("logging modified the transmitted notification")
	}
}

func TestWebhookNotificationContentBoundedAndSanitized(t *testing.T) {
	event := webhook.Event{Event: webhook.ItemDeleted,
		Item: webhook.Item{Id: "42", Name: "token=credential-canary\n" + strings.Repeat("片", 10000), Path: "https://receiver.invalid/path?token=url-canary",
			UserData: map[string]any{"IsFavorite": "wrong-type-canary", "PlayedPercentage": math.Inf(1), "PlayCount": math.NaN(), "LastPlayedDate": "date-canary", "password": "password-canary"}},
	}
	content := webhookLogContent(event)
	if !json.Valid([]byte(content)) || len(content) > 2048 || !strings.Contains(content, "truncated") {
		t.Fatalf("content not valid and bounded: %s", content)
	}
	for _, secret := range []string{"credential-canary", "url-canary", "receiver.invalid", "wrong-type-canary", "password-canary", "date-canary", "\n"} {
		if strings.Contains(content, secret) {
			t.Fatalf("unsafe content %q: %s", secret, content)
		}
	}
}

func TestWebhookNotificationEventDescriptions(t *testing.T) {
	for _, event := range []string{webhook.ItemDeleted, webhook.ItemUpdated, webhook.ItemRate, webhook.ItemUnrate,
		webhook.ItemMarkPlayed, webhook.ItemMarkUnplayed, webhook.PlaybackStart, webhook.PlaybackProgress,
		webhook.PlaybackStop, webhook.PlaybackPause, webhook.PlaybackUnpause, webhook.UserAuthenticated, webhook.UserLogout} {
		if webhookEventDescription(event) == "未知事件" {
			t.Fatalf("missing event description: %s", event)
		}
	}
	for _, name := range []string{webhook.UserAuthenticated, webhook.UserLogout} {
		content := webhookLogContent(webhook.Event{Event: name, User: &webhook.User{Id: "1", Name: "管理员"}})
		if !strings.Contains(content, `"Name":"管理员"`) || strings.Contains(content, `"Item"`) {
			t.Fatalf("incorrect user notification: %s", content)
		}
	}
}

func TestWebhookProgressSuccessQuietAtDefaultLevel(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	result := webhook.Result{Status: webhook.ResultSent, Targets: []webhook.TargetOutcome{{ID: "target", Status: webhook.TargetSent}}}
	logWebhookDelivery(webhook.Event{Event: webhook.PlaybackProgress}, result, time.Millisecond)
	if output.Len() != 0 {
		t.Fatalf("high-frequency success logged at default level: %s", output.String())
	}
}

func TestWebhookTargetFailureLogsExcludeSuccessfulTargetsAndNames(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	logWebhookTargetFailures(webhook.Event{Event: "item.updated", Item: webhook.Item{Id: "42"}}, webhook.Result{
		Status: webhook.ResultPartial,
		Targets: []webhook.TargetOutcome{
			{ID: "good-target", Name: "private-name-canary", Status: webhook.TargetSent},
			{ID: "bad-target", Name: "private-name-canary", Status: webhook.TargetFailed, ErrorCode: "read_response"},
			{ID: "budget-target", Status: webhook.TargetSkippedBudget, ErrorCode: "budget_exhausted"},
		},
	})
	log := output.String()
	for _, want := range []string{"bad-target", "budget-target", "read_response", "budget_exhausted"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing diagnostic %q: %s", want, log)
		}
	}
	for _, unwanted := range []string{"good-target", "private-name-canary", "索引已删除"} {
		if strings.Contains(log, unwanted) {
			t.Fatalf("unexpected diagnostic %q: %s", unwanted, log)
		}
	}
}
