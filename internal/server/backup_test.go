package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminBackupRunAndStatus(t *testing.T) {
	app, ts, token := contractEnv(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/backup/run", nil)
	req.Header.Set("X-Emby-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d: %s", resp.StatusCode, raw)
	}
	var submitted struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &submitted); err != nil || submitted.TaskID == 0 {
		t.Fatalf("task_id missing: %s", raw)
	}

	type lastView struct {
		Running   bool   `json:"running"`
		Dir       string `json:"dir"`
		Retention int    `json:"retention"`
		Last      *struct {
			TaskID int64  `json:"task_id"`
			Status string `json:"status"`
			Error  string `json:"error"`
			Result *struct {
				Dir      string   `json:"dir"`
				NFOCount int      `json:"nfo_count"`
				Pruned   []string `json:"pruned"`
			} `json:"result"`
		} `json:"last"`
	}
	deadline := time.Now().Add(15 * time.Second)
	var got lastView
	for {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/backup", nil)
		req.Header.Set("X-Emby-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !got.Running && got.Last != nil && got.Last.TaskID == submitted.TaskID && got.Last.Status != "queued" && got.Last.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup did not finish: %s", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Last.Status != "success" || got.Last.Result == nil || got.Last.Result.Dir == "" || got.Last.Result.NFOCount < 1 {
		t.Fatalf("unexpected last backup: %+v", got.Last)
	}
	if got.Last.Error != "" {
		t.Fatalf("successful backup carries error: %q", got.Last.Error)
	}
	dir := filepath.Join(filepath.Dir(app.cfg.DBPath), "backups")
	if got.Dir != dir {
		t.Fatalf("dir = %q, want %q", got.Dir, dir)
	}
	if filepath.Dir(got.Last.Result.Dir) != dir || got.Last.Result.Dir == dir {
		t.Fatalf("run dir = %q, want child of %q", got.Last.Result.Dir, dir)
	}
	if got.Retention != backupRetention {
		t.Fatalf("retention = %d", got.Retention)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir mode = %v, want 0700", info.Mode().Perm())
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) == 0 {
		t.Fatalf("backup dir empty: %v %d", err, len(entries))
	}
}

func TestAdminBackupRejectsNonAdmin(t *testing.T) {
	_, ts, adminToken := contractEnv(t)
	viewer := viewerToken(t, ts, adminToken)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/admin/backup"
		if method == http.MethodPost {
			path = "/api/admin/backup/run"
		}
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.Header.Set("X-Emby-Token", viewer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s as viewer = %d, want 403", method, resp.StatusCode)
		}
	}
}

func TestAdminBackupRejectsDuplicateRun(t *testing.T) {
	app, ts, token := contractEnv(t)
	app.backupRunning.Store(true)
	t.Cleanup(func() { app.backupRunning.Store(false) })
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/backup/run", nil)
	req.Header.Set("X-Emby-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate run status %d", resp.StatusCode)
	}
}
