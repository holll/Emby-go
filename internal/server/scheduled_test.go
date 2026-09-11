package server

import (
	"encoding/json"
	"testing"
)

func TestNormalizeParams(t *testing.T) {
	cases := []struct {
		name string
		kind string
		raw  map[string]any
		want string
	}{
		{"scan 默认全部库", "scan", nil, `{"library_id":0}`},
		{"scan 指定库", "scan", map[string]any{"library_id": float64(3)}, `{"library_id":3}`},
		{"reindex 无参数", "reindex", map[string]any{"library_id": float64(3)}, `{}`},
		{"probe 全字段", "probe", map[string]any{"library_id": float64(2), "status": "manual", "limit": float64(50), "only_missing": false},
			`{"library_id":2,"limit":50,"only_missing":false,"status":"manual"}`},
		{"probe 省略空状态与零 limit", "probe", map[string]any{"status": "", "limit": float64(0)}, `{"library_id":0}`},
		{"丢弃未知字段", "scan", map[string]any{"library_id": float64(1), "junk": "x"}, `{"library_id":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeParams(tc.kind, tc.raw)
			if err != nil {
				t.Fatalf("normalizeParams: %v", err)
			}
			// 比较解码后的对象，避免键顺序影响。
			var gotMap, wantMap map[string]any
			if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
				t.Fatalf("结果不是合法 JSON: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.want), &wantMap); err != nil {
				t.Fatal(err)
			}
			if len(gotMap) != len(wantMap) {
				t.Fatalf("参数 = %s，期望 %s", got, tc.want)
			}
			for key, want := range wantMap {
				if gotMap[key] != want {
					t.Errorf("参数 %s = %v，期望 %v", key, gotMap[key], want)
				}
			}
		})
	}

	if _, err := normalizeParams("unknown-type", nil); err == nil {
		t.Error("未接入的类型应被拒绝")
	}
}

func TestValidateScheduled(t *testing.T) {
	enabled := true
	ok, err := validateScheduled(scheduledRequest{Name: "  夜间扫库 ", Type: "SCAN", Cron: " 0 3 * * * ", Enabled: &enabled})
	if err != nil {
		t.Fatalf("合法请求不应报错: %v", err)
	}
	if ok.Name != "夜间扫库" {
		t.Errorf("名称应去空白，得到 %q", ok.Name)
	}
	if ok.Type != "scan" {
		t.Errorf("类型应归一化为小写，得到 %q", ok.Type)
	}
	if ok.Cron != "0 3 * * *" {
		t.Errorf("表达式应去空白，得到 %q", ok.Cron)
	}
	if !ok.Enabled {
		t.Error("enabled 应被保留")
	}

	// 未传 enabled 时默认启用。
	默认, err := validateScheduled(scheduledRequest{Name: "x", Type: "probe", Cron: "@daily"})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !默认.Enabled {
		t.Error("未显式传 enabled 时应默认启用")
	}

	bad := []struct {
		name string
		req  scheduledRequest
	}{
		{"名称为空", scheduledRequest{Name: "   ", Type: "scan", Cron: "0 3 * * *"}},
		{"类型未知", scheduledRequest{Name: "x", Type: "unknown-type", Cron: "0 3 * * *"}},
		{"类型为空", scheduledRequest{Name: "x", Cron: "0 3 * * *"}},
		{"表达式为空", scheduledRequest{Name: "x", Type: "scan"}},
		{"表达式非法", scheduledRequest{Name: "x", Type: "scan", Cron: "later"}},
		{"表达式秒级六段", scheduledRequest{Name: "x", Type: "scan", Cron: "0 0 3 * * *"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateScheduled(tc.req); err == nil {
				t.Error("应被拒绝")
			}
		})
	}
}

// TestNormalizeParamsScrape 两类刮削任务的参数规范化（需求 5i）。
func TestNormalizeParamsScrape(t *testing.T) {
	got, err := normalizeParams("scrape", map[string]any{
		"library_id": float64(2), "only_missing": false, "overwrite": true, "limit": float64(50), "junk": "x",
	})
	if err != nil {
		t.Fatalf("normalizeParams(scrape): %v", err)
	}
	want := `{"library_id":2,"limit":50,"only_missing":false,"overwrite":true}`
	assertSameJSON(t, got, want)

	got, err = normalizeParams("scrape_avatars", map[string]any{"library_id": float64(1), "junk": 1})
	if err != nil {
		t.Fatalf("normalizeParams(scrape_avatars): %v", err)
	}
	assertSameJSON(t, got, `{"library_id":1}`)

	// 两类刮削任务都应通过入口校验。
	for _, kind := range []string{"scrape", "scrape_avatars"} {
		if _, err := validateScheduled(scheduledRequest{Name: "n", Type: kind, Cron: "0 3 * * *"}); err != nil {
			t.Errorf("%s 应被接受: %v", kind, err)
		}
	}
}

// assertSameJSON 比较两份 JSON 的语义相等（键顺序无关）。
func assertSameJSON(t *testing.T, got, want string) {
	t.Helper()
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatalf("结果不是合法 JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantMap); err != nil {
		t.Fatal(err)
	}
	if len(gotMap) != len(wantMap) {
		t.Fatalf("参数 = %s，期望 %s", got, want)
	}
	for key, value := range wantMap {
		if gotMap[key] != value {
			t.Errorf("参数 %s = %v，期望 %v", key, gotMap[key], value)
		}
	}
}
