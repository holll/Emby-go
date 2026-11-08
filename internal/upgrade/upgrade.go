// Package upgrade 实现 /opt/emby-go 平铺部署下的 Web 自升级。
//
// 流程固定为：查 GitHub Release → 下载当前平台资产到程序同目录临时文件 →
// 用发布产出的 SHA256SUMS 校验 → 执行 -version 核对构建版本 → chmod + rename
// 原子替换 → 发出重启信号，由调用方正常关闭进程，交 systemd 拉起新程序。
//
// 不保留上一版、不建 releases/backups、不做自动回退：失败时旧程序保持不动，
// 成功替换后若新程序起不来，只能人工重新安装（这是部署政策的一部分）。
package upgrade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultRepo    = "holll/Emby-go"
	defaultAPIBase = "https://api.github.com"
	checksumsAsset = "SHA256SUMS"

	// checkTTL 避免每次打开页面都打 GitHub API（未认证 60 次/小时/IP）。
	checkTTL = 10 * time.Minute
	// runTimeout 覆盖「下载 + 校验 + 替换」整个升级过程；网络太慢就重来。
	runTimeout = 10 * time.Minute
	// verifyTimeout 限制执行未知新程序的 -version，避免它卡住关停预算。
	verifyTimeout = 30 * time.Second
	// maxBinaryBytes 是可接受的最大升级包。真实产物约 20-40 MiB。
	maxBinaryBytes = 200 << 20
	maxChecksums   = 1 << 20
)

const (
	PhaseIdle        = "idle"
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseInstalling  = "installing"
	PhaseRestarting  = "restarting"
	PhaseFailed      = "failed"
)

// Options 是 Manager 的构造参数；零值字段使用默认值。
type Options struct {
	Repo           string
	APIBase        string
	CurrentVersion string
	Client         *http.Client
	// MirrorBase 可选，形如 https://mirror.example.com/github.com，用于无法
	// 直连 GitHub 资产的环境；为空时直接使用 Release 里的资产地址。
	MirrorBase string
}

// Manager 串行化检查与安装，并把升级进度暴露给管理端。
type Manager struct {
	repo       string
	apiBase    string
	mirrorBase string
	current    string
	client     *http.Client

	// executable 为空时取 os.Executable()；测试注入临时路径，
	// 避免测试进程替换自己的二进制。
	executable string

	restart     chan struct{}
	restartOnce sync.Once

	// 下载进度只给状态查询读，用原子量避免和状态锁互相等待。
	downloaded atomic.Int64
	total      atomic.Int64

	mu        sync.Mutex
	latest    *release
	checkedAt time.Time
	state     State
}

// release 是检查阶段解析出的发布资产；URL 只在进程内使用，不回传浏览器。
type release struct {
	version     string
	notes       string
	publishedAt time.Time
	asset       string
	size        int64
	binaryURL   string
	sumsURL     string
}

// Result 是管理端检查更新的响应数据。
type Result struct {
	Current     string
	Latest      string
	Available   bool
	Notes       string
	PublishedAt time.Time
	CheckedAt   time.Time
}

// State 是升级进度；重启后进程从 idle 重新开始。
type State struct {
	Phase           string    `json:"phase"`
	Target          string    `json:"target,omitempty"`
	Message         string    `json:"message,omitempty"`
	DownloadedBytes int64     `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64     `json:"total_bytes,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// New 构造 Manager。
func New(o Options) *Manager {
	if o.Repo == "" {
		o.Repo = defaultRepo
	}
	if o.APIBase == "" {
		o.APIBase = defaultAPIBase
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}
	return &Manager{
		repo:       o.Repo,
		apiBase:    strings.TrimRight(o.APIBase, "/"),
		mirrorBase: strings.TrimRight(o.MirrorBase, "/"),
		current:    o.CurrentVersion,
		client:     o.Client,
		restart:    make(chan struct{}),
		state:      State{Phase: PhaseIdle, UpdatedAt: time.Now()},
	}
}

// Restart 在新程序就位后关闭，调用方据此走正常退出（systemd 负责拉起）。
func (m *Manager) Restart() <-chan struct{} { return m.restart }

// State 返回当前升级进度快照。
func (m *Manager) State() State {
	m.mu.Lock()
	state := m.state
	m.mu.Unlock()
	state.DownloadedBytes = m.downloaded.Load()
	state.TotalBytes = m.total.Load()
	return state
}

// Check 查询最新 Release。force 为 false 时复用 checkTTL 内的缓存。
// 查询失败返回错误——绝不能把失败伪装成「已是最新」。
func (m *Manager) Check(ctx context.Context, force bool) (Result, error) {
	m.mu.Lock()
	if !force && m.latest != nil && time.Since(m.checkedAt) < checkTTL {
		result := m.resultLocked()
		m.mu.Unlock()
		return result, nil
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rel, err := m.fetchRelease(ctx)
	if err != nil {
		return Result{}, err
	}
	m.mu.Lock()
	m.latest = rel
	m.checkedAt = time.Now()
	result := m.resultLocked()
	m.mu.Unlock()
	return result, nil
}

func (m *Manager) resultLocked() Result {
	return Result{
		Current:     m.current,
		Latest:      m.latest.version,
		Available:   newerVersion(m.latest.version, m.current),
		Notes:       m.latest.notes,
		PublishedAt: m.latest.publishedAt,
		CheckedAt:   m.checkedAt,
	}
}

// Begin 预留一次安装。target 必须与最近一次检查到的 Release 一致，
// 防止页面上的旧版本号驱动一次下载。返回错误表示已有任务在执行。
func (m *Manager) Begin(target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.state.Phase {
	case PhaseDownloading, PhaseVerifying, PhaseInstalling, PhaseRestarting:
		return errors.New("已有升级任务在执行")
	}
	if m.latest == nil || m.latest.version != target {
		return errors.New("目标版本已变化，请重新检查更新")
	}
	m.downloaded.Store(0)
	m.total.Store(m.latest.size)
	m.state = State{Phase: PhaseDownloading, Target: target, UpdatedAt: time.Now()}
	return nil
}

// Run 执行下载、校验与原子替换，成功后发出重启信号。它假设 Begin 已成功，
// 串行由调用方（管理端单次任务）保证。
func (m *Manager) Run(ctx context.Context, target string) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	rel := m.releaseFor(target)
	if rel == nil {
		m.fail(target, errors.New("升级目标已失效，请重新检查更新"))
		return
	}
	m.downloaded.Store(0)
	m.total.Store(rel.size)
	exe := m.executable
	if exe == "" {
		resolved, err := os.Executable()
		if err != nil {
			m.fail(target, fmt.Errorf("定位当前程序失败：%w", err))
			return
		}
		exe = resolved
	}

	// 临时文件必须与目标程序同目录，rename 才是同一文件系统内的原子替换。
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".emby-go-update-*")
	if err != nil {
		m.fail(target, fmt.Errorf("创建临时文件失败：%w", err))
		return
	}
	tmpName := tmp.Name()
	installed := false
	defer func() {
		if !installed {
			os.Remove(tmpName)
		}
	}()

	sum, err := m.download(ctx, rel.binaryURL, tmp)
	if err != nil {
		m.fail(target, err)
		return
	}
	m.setPhase(PhaseVerifying, target, "")
	want, err := m.fetchChecksum(ctx, rel.sumsURL, rel.asset)
	if err != nil {
		m.fail(target, err)
		return
	}
	if !strings.EqualFold(sum, want) {
		m.fail(target, errors.New("升级包校验失败：SHA256 不匹配"))
		return
	}
	// 先给执行权限：verifyBinary 要真正运行这个候选程序。
	if err := os.Chmod(tmpName, 0o755); err != nil {
		m.fail(target, fmt.Errorf("设置执行权限失败：%w", err))
		return
	}
	if err := verifyBinary(ctx, tmpName, target); err != nil {
		m.fail(target, err)
		return
	}

	m.setPhase(PhaseInstalling, target, "")
	if err := os.Rename(tmpName, exe); err != nil {
		m.fail(target, fmt.Errorf("替换程序失败：%w", err))
		return
	}
	installed = true
	m.setPhase(PhaseRestarting, target, "")
	slog.Info("升级包已原子替换，等待 systemd 重启", "version", target, "path", exe)
	m.restartOnce.Do(func() { close(m.restart) })
}

func (m *Manager) releaseFor(target string) *release {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest == nil || m.latest.version != target {
		return nil
	}
	copied := *m.latest
	return &copied
}

func (m *Manager) setPhase(phase, target, message string) {
	m.mu.Lock()
	m.state = State{Phase: phase, Target: target, Message: message, UpdatedAt: time.Now()}
	m.mu.Unlock()
}

func (m *Manager) fail(target string, err error) {
	m.setPhase(PhaseFailed, target, err.Error())
	slog.Warn("自升级失败，当前程序保持不变", "version", target, "error", err)
}

func (m *Manager) fetchRelease(ctx context.Context) (*release, error) {
	endpoint := m.apiBase + "/repos/" + m.repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造 Release 请求失败：%w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub API 拒绝没有 User-Agent 的请求。
	req.Header.Set("User-Agent", "Emby-go/"+m.current)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 Release 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求 Release 失败：HTTP %d", resp.StatusCode)
	}
	var payload struct {
		TagName     string    `json:"tag_name"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析 Release 失败：%w", err)
	}
	if payload.TagName == "" {
		return nil, errors.New("Release 缺少版本号")
	}
	asset := "emby-go-" + runtime.GOOS + "-" + runtime.GOARCH
	rel := &release{
		version:     payload.TagName,
		notes:       payload.Body,
		publishedAt: payload.PublishedAt,
		asset:       asset,
	}
	var foundBinary, foundSums bool
	for _, candidate := range payload.Assets {
		switch candidate.Name {
		case asset:
			rel.binaryURL = candidate.BrowserDownloadURL
			rel.size = candidate.Size
			foundBinary = true
		case checksumsAsset:
			rel.sumsURL = candidate.BrowserDownloadURL
			foundSums = true
		}
	}
	var missing []string
	if !foundBinary {
		missing = append(missing, asset)
	}
	if !foundSums {
		missing = append(missing, checksumsAsset)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Release %s 缺少资产：%s", rel.version, strings.Join(missing, "、"))
	}
	if m.mirrorBase != "" {
		if err := requireHTTPS(m.mirrorBase); err != nil {
			return nil, fmt.Errorf("升级镜像地址无效：%w", err)
		}
		base := m.mirrorBase + "/" + m.repo + "/releases/download/" + url.PathEscape(rel.version) + "/"
		rel.binaryURL = base + asset
		rel.sumsURL = base + checksumsAsset
	}
	return rel, nil
}

func (m *Manager) download(ctx context.Context, rawURL string, dst *os.File) (string, error) {
	defer dst.Close()
	if err := requireHTTPS(rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造下载请求失败：%w", err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载升级包失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载升级包失败：HTTP %d", resp.StatusCode)
	}
	hash := sha256.New()
	// 管理端按已写字节展示进度；总量来自 Release 资产声明的 size。
	body := io.TeeReader(resp.Body, &countingWriter{done: &m.downloaded})
	written, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(body, maxBinaryBytes+1))
	if err != nil {
		return "", fmt.Errorf("下载升级包失败：%w", err)
	}
	if written > maxBinaryBytes {
		return "", errors.New("升级包超过 200 MiB 上限")
	}
	if written == 0 {
		return "", errors.New("升级包为空")
	}
	// rename 只保证目录项原子；不落盘的话掉电后可能留下空文件。
	if err := dst.Sync(); err != nil {
		return "", fmt.Errorf("写入升级包失败：%w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// countingWriter 累计已写入的字节数，只用于进度展示。
type countingWriter struct {
	done *atomic.Int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.done.Add(int64(len(p)))
	return len(p), nil
}

func (m *Manager) fetchChecksum(ctx context.Context, rawURL, asset string) (string, error) {
	if err := requireHTTPS(rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造校验文件请求失败：%w", err)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件失败：HTTP %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxChecksums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != sha256.Size*2 {
			return "", errors.New("校验文件格式错误")
		}
		return sum, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取校验文件失败：%w", err)
	}
	return "", fmt.Errorf("校验文件中没有 %s", asset)
}

// verifyBinary 执行新程序自己的 -version，确认资产与目标标签一致。
// 发布构建的版本号形如 v2.2.7-<commit>，标签是 v2.2.7。
func verifyBinary(ctx context.Context, path, target string) error {
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	out, err := exec.CommandContext(vctx, path, "-version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("升级包无法执行：%w", err)
	}
	if !reportsVersion(string(out), target) {
		return fmt.Errorf("升级包版本不符：%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func reportsVersion(output, target string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(output), "Emby-go "+target)
	if !ok {
		return false
	}
	return rest == "" || strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, " ")
}

// newerVersion 比较 vX.Y.Z 三元组；构建后缀（-<commit>）不参与比较。
// 无法解析时退化为「标签不同即视为新版本」，保证 dev 构建也能升级。
func newerVersion(latest, current string) bool {
	latestParts, okLatest := versionTriple(latest)
	currentParts, okCurrent := versionTriple(current)
	if !okLatest || !okCurrent {
		return latest != current
	}
	for i := range latestParts {
		if latestParts[i] != currentParts[i] {
			return latestParts[i] > currentParts[i]
		}
	}
	return false
}

func versionTriple(raw string) ([3]int, bool) {
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "v") {
		return [3]int{}, false
	}
	text = text[1:]
	if i := strings.IndexByte(text, '-'); i >= 0 {
		text = text[:i]
	}
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

func requireHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("下载地址无效：%w", err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return errors.New("下载地址必须是 https")
	}
	return nil
}
