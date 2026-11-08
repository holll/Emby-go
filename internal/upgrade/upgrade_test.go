package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeRelease struct {
	tag      string
	binary   []byte
	sums     string
	status   int
	omitSums bool
}

// fakeAPI 用 TLS 提供服务：生产代码只接受 https 资产地址。
func fakeAPI(t *testing.T, rel fakeRelease) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if rel.status != 0 && rel.status != http.StatusOK {
			w.WriteHeader(rel.status)
			return
		}
		asset := "emby-go-" + runtime.GOOS + "-" + runtime.GOARCH
		assets := []map[string]any{
			{"name": asset, "browser_download_url": srv.URL + "/binary", "size": len(rel.binary)},
		}
		if !rel.omitSums {
			assets = append(assets, map[string]any{"name": checksumsAsset, "browser_download_url": srv.URL + "/SHA256SUMS"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     rel.tag,
			"body":         "测试版本说明",
			"published_at": "2026-11-01T00:00:00Z",
			"assets":       assets,
		})
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(rel.binary)
	})
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		sums := rel.sums
		if sums == "" {
			sum := sha256.Sum256(rel.binary)
			sums = fmt.Sprintf("%s  emby-go-%s-%s\n", hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH)
		}
		_, _ = w.Write([]byte(sums))
	})
	return srv, &hits
}

func fakeBinary(tag string) []byte {
	return []byte("#!/bin/sh\nprintf 'Emby-go " + tag + "-abcdef0 (commit abcdef0, built now)\\n'\n")
}

func TestCheckReportsAvailabilityAndCaches(t *testing.T) {
	srv, hits := fakeAPI(t, fakeRelease{tag: "v1.2.3", binary: fakeBinary("v1.2.3")})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client()})

	result, err := m.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Latest != "v1.2.3" || result.Current != "v1.0.0" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := m.Check(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("TTL 内应复用缓存，实际请求 %d 次", got)
	}
	if _, err := m.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("force 应重新请求，实际 %d 次", got)
	}
}

func TestCheckSameVersionIsNotAnUpdate(t *testing.T) {
	srv, _ := fakeAPI(t, fakeRelease{tag: "v1.2.3", binary: fakeBinary("v1.2.3")})
	// 发布构建带提交号后缀，比较时只看 vX.Y.Z。
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.2.3-abcdef0", Client: srv.Client()})
	result, err := m.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Available {
		t.Fatalf("同版本不应提示更新：%+v", result)
	}
}

func TestCheckReportsMissingChecksumsAsset(t *testing.T) {
	// 真实场景：旧发布流程产出的 Release 只有二进制、没有 SHA256SUMS；
	// 检查必须报错并指出缺哪一个资产，不能降级成无校验安装。
	srv, _ := fakeAPI(t, fakeRelease{tag: "v2.2.6", binary: fakeBinary("v2.2.6"), omitSums: true})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v2.2.5", Client: srv.Client()})
	if _, err := m.Check(context.Background(), false); err == nil || !strings.Contains(err.Error(), checksumsAsset) {
		t.Fatalf("Check() = %v, want error naming %s", err, checksumsAsset)
	}
}

func TestCheckFailureIsAnErrorNotLatest(t *testing.T) {
	srv, _ := fakeAPI(t, fakeRelease{tag: "v1.2.3", binary: fakeBinary("v1.2.3"), status: http.StatusInternalServerError})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client()})
	if result, err := m.Check(context.Background(), true); err == nil {
		t.Fatalf("检查失败必须返回错误，不能返回 %+v", result)
	}
}

func TestRunInstallsVerifiedBinaryAndRequestsRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("升级资产是 Linux 部署形态，Windows 不执行该测试")
	}
	srv, _ := fakeAPI(t, fakeRelease{tag: "v1.2.3", binary: fakeBinary("v1.2.3")})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client()})
	dir := t.TempDir()
	exe := filepath.Join(dir, "emby-go")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.executable = exe

	result, err := m.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Begin(result.Latest); err != nil {
		t.Fatal(err)
	}
	m.Run(context.Background(), result.Latest)

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fakeBinary("v1.2.3")) {
		t.Fatalf("程序未被替换：%q", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("权限 = %o，期望 0755", info.Mode().Perm())
	}
	select {
	case <-m.Restart():
	default:
		t.Fatal("替换成功后必须发出重启信号")
	}
	state := m.State()
	if state.Phase != PhaseRestarting || state.Target != "v1.2.3" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if want := int64(len(fakeBinary("v1.2.3"))); state.TotalBytes != want || state.DownloadedBytes != want {
		t.Fatalf("进度 = %d/%d，期望 %d/%d", state.DownloadedBytes, state.TotalBytes, want, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("临时文件未清理：%v", entries)
	}
}

func TestRunKeepsOldBinaryWhenChecksumMismatches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("升级资产是 Linux 部署形态，Windows 不执行该测试")
	}
	srv, _ := fakeAPI(t, fakeRelease{
		tag:    "v1.2.3",
		binary: fakeBinary("v1.2.3"),
		sums:   "0000000000000000000000000000000000000000000000000000000000000000  emby-go-" + runtime.GOOS + "-" + runtime.GOARCH + "\n",
	})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client()})
	dir := t.TempDir()
	exe := filepath.Join(dir, "emby-go")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.executable = exe

	result, err := m.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Begin(result.Latest); err != nil {
		t.Fatal(err)
	}
	m.Run(context.Background(), result.Latest)

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old-binary" {
		t.Fatalf("校验失败不应替换程序：%q", got)
	}
	if state := m.State(); state.Phase != PhaseFailed || state.Message == "" {
		t.Fatalf("失败状态缺失：%+v", state)
	}
	select {
	case <-m.Restart():
		t.Fatal("校验失败不得发出重启信号")
	default:
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("临时文件未清理：%v", entries)
	}
}

func TestBeginRejectsStaleTargetAndConcurrentRun(t *testing.T) {
	srv, _ := fakeAPI(t, fakeRelease{tag: "v1.2.3", binary: fakeBinary("v1.2.3")})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client()})
	if err := m.Begin("v9.9.9"); err == nil {
		t.Fatal("未检查过目标版本时必须拒绝")
	}
	if _, err := m.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := m.Begin("v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := m.Begin("v1.2.3"); err == nil {
		t.Fatal("已有任务执行时必须拒绝第二次 Begin")
	}
}

func TestReportsVersionAcceptsBuildSuffix(t *testing.T) {
	cases := []struct {
		output, target string
		want           bool
	}{
		{"Emby-go v2.2.7-abcdef0 (commit abcdef0, built now)", "v2.2.7", true},
		{"Emby-go v2.2.7 (commit none, built now)", "v2.2.7", true},
		{"Emby-go v2.2.70-abcdef0 (commit abcdef0, built now)", "v2.2.7", false},
		{"Emby-go v2.2.6-abcdef0 (commit abcdef0, built now)", "v2.2.7", false},
		{"", "v2.2.7", false},
	}
	for _, c := range cases {
		if got := reportsVersion(c.output, c.target); got != c.want {
			t.Errorf("reportsVersion(%q, %q) = %v，期望 %v", c.output, c.target, got, c.want)
		}
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v2.2.7", "v2.2.6-abc", true},
		{"v2.2.7", "v2.2.7-abc", false},
		{"v2.3.0", "v2.2.9", true},
		{"v2.2.10", "v2.2.9", true},
		{"v2.2.6", "v2.2.7", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.latest, c.current); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v，期望 %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestCheckRejectsNonHTTPSMirror(t *testing.T) {
	srv, _ := fakeAPI(t, fakeRelease{tag: "v1.2.3", binary: fakeBinary("v1.2.3")})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client(), MirrorBase: "http://mirror.example.com"})
	_, err := m.Check(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "镜像地址无效") {
		t.Fatalf("非 https 镜像应报错，得到 %v", err)
	}
}

func TestRunDownloadsFromMirrorBase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("升级资产是 Linux 部署形态，Windows 不执行该测试")
	}
	tag := "v2.0.0"
	bin := fakeBinary(tag)
	asset := "emby-go-" + runtime.GOOS + "-" + runtime.GOARCH
	sum := sha256.Sum256(bin)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	var directHits, mirrorHits atomic.Int64
	mux := http.NewServeMux()
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag,
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/direct/binary"},
				{"name": checksumsAsset, "browser_download_url": srv.URL + "/direct/SHA256SUMS"},
			},
		})
	})
	mux.HandleFunc("/direct/", func(w http.ResponseWriter, r *http.Request) {
		directHits.Add(1)
		http.Error(w, "must not fetch direct", http.StatusBadGateway)
	})
	mux.HandleFunc("/mirror/github.com/o/r/releases/download/"+tag+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		_, _ = w.Write(bin)
	})
	mux.HandleFunc("/mirror/github.com/o/r/releases/download/"+tag+"/"+checksumsAsset, func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		_, _ = w.Write([]byte(sums))
	})
	m := New(Options{Repo: "o/r", APIBase: srv.URL, CurrentVersion: "v1.0.0", Client: srv.Client(), MirrorBase: srv.URL + "/mirror/github.com"})
	dir := t.TempDir()
	exe := filepath.Join(dir, "emby-go")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.executable = exe
	result, err := m.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Begin(result.Latest); err != nil {
		t.Fatal(err)
	}
	m.Run(context.Background(), result.Latest)
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bin) {
		t.Fatalf("程序未被镜像资产替换：%q", got)
	}
	if directHits.Load() != 0 {
		t.Fatalf("配置镜像后仍访问了直连资产 %d 次", directHits.Load())
	}
	if mirrorHits.Load() != 2 {
		t.Fatalf("镜像命中 %d 次，期望 2", mirrorHits.Load())
	}
	select {
	case <-m.Restart():
	default:
		t.Fatal("镜像替换成功后必须发出重启信号")
	}
}
