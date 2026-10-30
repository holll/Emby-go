package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestScheduledProbeStatusCompatibility(t *testing.T) {
	for _, raw := range []map[string]any{nil, {}, {"status": nil}, {"status": ""}} {
		normalized, err := normalizeParams("probe", raw)
		if err != nil {
			t.Fatal(err)
		}
		var stored map[string]any
		if err := json.Unmarshal([]byte(normalized), &stored); err != nil {
			t.Fatal(err)
		}
		status, err := scheduledProbeStatus(stored)
		if err != nil || status != "" {
			t.Fatalf("legacy changed: %s %q %v", normalized, status, err)
		}
	}
	for _, status := range []string{"all", "success", "manual"} {
		encoded, err := normalizeParams("probe", map[string]any{"probe_status_contract": probeStatusContract, "status": status})
		if err != nil {
			t.Fatal(err)
		}
		var params map[string]any
		json.Unmarshal([]byte(encoded), &params)
		actual, err := scheduledProbeStatus(params)
		if err != nil || actual != status || params["probe_status_contract"] != probeStatusContract {
			t.Fatalf("round trip %s %q %v", encoded, actual, err)
		}
	}
	status, err := scheduledProbeStatus(map[string]any{"probe_status_contract": probeStatusContract})
	if err != nil || status != "success" {
		t.Fatalf("default=%q %v", status, err)
	}
	for _, bad := range []any{nil, "", "unknown", false, float64(2), []any{}, map[string]any{}} {
		raw := map[string]any{"probe_status_contract": probeStatusContract, "status": bad}
		if _, err := normalizeParams("probe", raw); err == nil {
			t.Fatalf("accepted strict status %#v", bad)
		}
		if _, err := scheduledProbeStatus(raw); err == nil {
			t.Fatalf("execution accepted %#v", bad)
		}
	}
	for _, bad := range []any{nil, "future", false, float64(2), map[string]any{}} {
		if _, err := normalizeParams("probe", map[string]any{"probe_status_contract": bad, "status": "all"}); err == nil {
			t.Fatalf("accepted version %#v", bad)
		}
	}
}

func TestProbeAllPreservesNFOLibraryAndLegacyEligibility(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), `<movie><title>Success</title></movie>`)
	app.startupWG.Wait()
	fake := filepath.Join(t.TempDir(), "ffprobe-fixture")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf 'ffprobe version fixture\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	app.cfg.FFProbePath = fake
	ts, token := detailServer(t, app)
	lib, err := app.db.AddLibrary("L", media)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(media, "manual.strm"), "https://media.test/manual.mp4\n")
	writeFile(t, filepath.Join(media, "manual.nfo"), `<movie><title>Manual</title></movie>`)
	writeFile(t, filepath.Join(media, "pending.strm"), "https://media.test/pending.mp4\n")
	other := t.TempDir()
	writeFile(t, filepath.Join(other, "other.strm"), "https://media.test/other.mp4\n")
	writeFile(t, filepath.Join(other, "other.nfo"), `<movie><title>Other</title></movie>`)
	if _, err := app.db.AddLibrary("Other", other); err != nil {
		t.Fatal(err)
	}
	scanOnce(t, ts, token)
	if err := app.db.MarkManual(filepath.Join(media, "manual.strm"), filepath.Join(media, "manual.nfo")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status string
		count  int
	}{{"", 1}, {"success", 1}, {"manual", 1}, {"all", 2}, {"pending", 0}} {
		plan, err := app.prepareProbe(probeRequest{LibraryID: lib.ID, Status: tc.status})
		if err != nil || plan.movies != tc.count {
			t.Fatalf("status=%q movies=%d err=%v", tc.status, plan.movies, err)
		}
	}
	all, err := app.prepareProbe(probeRequest{Status: "all"})
	if err != nil || all.movies != 3 {
		t.Fatalf("all libraries=%d %v", all.movies, err)
	}
	limited, err := app.prepareProbe(probeRequest{Status: "all", Limit: 1})
	if err != nil || limited.movies != 1 {
		t.Fatalf("limit=%d %v", limited.movies, err)
	}
	// Real admin write path persists the strict marker and rejects invalid inputs.
	for _, tc := range []struct {
		params string
		code   int
	}{{`{"probe_status_contract":"explicit-v1","status":"all"}`, 200}, {`{"probe_status_contract":"explicit-v1","status":null}`, 400}, {`{"probe_status_contract":"explicit-v1","status":""}`, 400}, {`{"probe_status_contract":"explicit-v1","status":"unexpected"}`, 400}, {`{"status":null}`, 200}, {`{"status":""}`, 200}} {
		resp, body := embyCall(t, ts, "POST", "/api/admin/scheduled", token, `{"name":"Scope fixture","type":"probe","cron":"0 3 * * *","enabled":false,"params":`+tc.params+`}`)
		if resp.StatusCode != tc.code {
			t.Fatalf("params=%s got=%d %+v", tc.params, resp.StatusCode, body)
		}
		if tc.code == 200 {
			tasks, err := app.db.ScheduledTasks()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, task := range tasks {
				if task.ID == int64(body["id"].(float64)) {
					var params map[string]any
					if err := json.Unmarshal([]byte(task.Params), &params); err != nil {
						t.Fatal(err)
					}
					if _, err := scheduledProbeStatus(params); err != nil {
						t.Fatal(err)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("task not persisted")
			}
		}
	}
}

// TestPrepareProbeNeedsFFProbeOnlyWhenThereIsWork 钉住 prepareProbe 内部的判断顺序。
//
// 背景（CI 抓到的问题）：旧实现先自检 ffprobe 再查库，于是在没装 ffmpeg 的机器上，
// 一个空库的计划探测会被报成任务失败「未找到 ffprobe」，而不是走
// 「计划任务探测跳过：没有可探测的影片」分支——这个失败与作用域无关，每天报一次，
// 只会把人骗去查错方向。这里用「显式指向不存在的 ffprobe」在任意机器上复现该环境，
// 断言：空库必须跳过成功，有影片必须失败且原因指向 ffprobe。
func TestPrepareProbeNeedsFFProbeOnlyWhenThereIsWork(t *testing.T) {
	app, media := detailTestApp(t, t.TempDir(), `<movie><title>Ready</title></movie>`)
	app.startupWG.Wait()
	app.cfg.FFProbePath = filepath.Join(t.TempDir(), "missing-ffprobe")
	lib, err := app.db.AddLibrary("L", media)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := app.prepareProbe(probeRequest{LibraryID: lib.ID, Status: "all"})
	if err != nil || len(plan.targets) != 0 {
		t.Fatalf("空库应先走跳过分支: targets=%d err=%v", len(plan.targets), err)
	}

	ts, token := detailServer(t, app)
	scanOnce(t, ts, token)
	if _, err := app.prepareProbe(probeRequest{LibraryID: lib.ID, Status: "all"}); err == nil {
		t.Fatal("有影片时缺 ffprobe 必须报错，不能静默跳过")
	}
}
