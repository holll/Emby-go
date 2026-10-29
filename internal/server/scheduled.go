package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/scanner"
	"emby-go/internal/scheduler"
	"emby-go/internal/scraper"
	"emby-go/internal/store"
)

// 计划任务：管理端配置 cron，到点执行扫库/重建索引/媒体信息探测。
// 定义存 DB（scheduled_tasks），执行历史不落库，只在任务行上留「上次结果」。
//
// 与「任务」（内存执行日志）的关系：到点执行时会往内存任务列表写一条，
// 便于在任务页看到实际执行情况；跳过的执行只更新任务行的上次结果，不占内存日志。

// scheduledTypes 已接入调度器的任务类型。
var scheduledTypes = map[string]string{
	"scan":           "扫描媒体库",
	"reindex":        "重建索引",
	"probe":          "媒体信息探测",
	"scrape":         "内置刮削",
	"scrape_avatars": "演员头像补全",
	"translate":      "独立翻译",
	"sprites":        "雪碧图生成",
}

// scheduledView 计划任务列表项：定义 + 下次执行时间。
type scheduledView struct {
	store.ScheduledTask
	NextRun    string `json:"next_run,omitempty"`
	ConfigETag string `json:"config_etag"`
}

// —— 执行器：把调度器回调接到具体的服务动作上 ——

// runScheduledTask 执行一条计划任务。返回 scheduler.ErrBusy 表示同类任务未结束、本次跳过。
func (a *App) runScheduledTask(ctx context.Context, task scheduler.Task) error {
	switch task.Type {
	case "sprites":
		return a.runScheduledSprites(ctx, task)
	case "scan", "reindex":
		return a.runScheduledScan(task)
	case "probe":
		return a.runScheduledProbe(task)
	case "scrape":
		return a.runScheduledScrape(task)
	case "scrape_avatars":
		return a.runScheduledScrapeAvatars(task)
	case "translate":
		return a.runScheduledTranslation(task)
	default:
		return fmt.Errorf("不支持的任务类型 %q", task.Type)
	}
}

// —— 日志辅助：开始/结束/跳过都要说清「跑了哪个范围、为什么没跑」——

// scheduledScope 把计划任务参数压成一条稳定的作用域描述（键排序、跳过空值）。
// 计划任务的日志以前只有任务名，看不出 library_id/limit 等实际作用域，
// 用户只看到「已开始/已结束」，无法判断执行是否真的覆盖了他配置的范围。
func scheduledScope(params map[string]any) string {
	keys := slices.Sorted(maps.Keys(params))
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := params[key]
		if value == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", key, value))
	}
	if len(parts) == 0 {
		return "全部范围"
	}
	return strings.Join(parts, " ")
}

// scheduledBusy 保留 ErrBusy 语义（调度器据此把本次记为「跳过」而不是失败），
// 同时把「被谁占着」写进文案：只返回「任务正在进行中」时，
// 运维无法区分是同类任务没结束、还是扫库/探测占着 NFO 通道。
func scheduledBusy(reason string) error {
	return fmt.Errorf("%w：%s", scheduler.ErrBusy, reason)
}

// scheduledFirstSample 取失败样例的第一条当「失败首因」；样例列表是限量的，
// 首因进汇总日志，全量样例仍由各任务的进展接口/失败汇总日志提供。
func scheduledFirstSample(samples []string) string {
	if len(samples) == 0 {
		return ""
	}
	return samples[0]
}

// runScheduledScan 执行扫描/重建索引（同步阻塞直到结束，调度器据此判定重叠）。
func (a *App) runScheduledScan(task scheduler.Task) error {
	scope := scheduledScope(task.Params)
	libraryID := int64(0)
	mode := scanner.ScanModeFull
	if task.Type == "scan" {
		var err error
		libraryID, mode, err = scheduledScanParams(task.Params)
		if err != nil {
			// 参数非法时也要落一条日志：此前只把 err 写回任务行，
			// 日志里没有这次执行的任何痕迹，等于「到点了但什么都没发生」。
			slog.Warn("计划任务扫描参数非法，本次未执行", "task", task.Name, "type", task.Type,
				"scope", scope, "error", err)
			return err
		}
	}
	marker, err := scheduledScanMarker(task.Params)
	if err != nil {
		slog.Warn("计划任务扫描参数非法，本次未执行", "task", task.Name, "type", task.Type,
			"scope", scope, "error", err)
		return err
	}
	if a.scanning() {
		slog.Info("计划任务扫描跳过", "task", task.Name, "type", task.Type, "scope", scope,
			"reason", "扫库/重建索引任务正在进行")
		return scheduledBusy("扫库/重建索引任务正在进行")
	}
	start := time.Now()
	// 跳过日志只说「没跑」，这里补「即将跑什么」：模式与作用域是用户配置后最想确认的两项。
	slog.Info("计划任务扫描开始", "task", task.Name, "type", task.Type, "scope", scope,
		"mode", string(mode), "incremental_marker", marker.Path != "")
	taskID := a.startTask(task.Type)
	result, err := a.scanLibraries(libraryID, taskID, mode, false, marker)
	if errors.Is(err, errScanBusy) {
		a.finishTaskBusy(taskID, err.Error())
		slog.Info("计划任务扫描跳过", "task", task.Name, "type", task.Type, "scope", scope,
			"reason", err.Error(), "elapsed", time.Since(start).Round(time.Millisecond).String())
		return scheduledBusy(err.Error())
	}
	a.finishTask(taskID, err)
	elapsed := time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		slog.Error("计划任务扫描失败", "task", task.Name, "type", task.Type, "scope", scope,
			"mode", string(mode), "elapsed", elapsed, "error", err)
		return err
	}
	slog.Info("计划任务扫描完成", "task", task.Name, "type", task.Type,
		"library_id", libraryID, "mode", string(mode),
		"success", result.Success, "pending", result.Pending,
		"skipped", result.Skipped, "failed", result.Failed, "elapsed", elapsed)
	return nil
}

// runScheduledProbe 执行媒体信息探测（同步阻塞直到结束）。
func (a *App) runScheduledProbe(task scheduler.Task) error {
	scope := scheduledScope(task.Params)
	status, err := scheduledProbeStatus(task.Params)
	if err != nil {
		slog.Warn("计划任务探测参数非法，本次未执行", "task", task.Name, "scope", scope, "error", err)
		return err
	}
	req := probeRequest{
		LibraryID: paramInt64(task.Params, "library_id"),
		Status:    status,
		Limit:     int(paramInt64(task.Params, "limit")),
	}
	if value, ok := task.Params["only_missing"]; ok {
		onlyMissing := paramBool(value)
		req.OnlyMissing = &onlyMissing
	}
	plan, err := a.prepareProbe(req)
	if errors.Is(err, errProbeBusy) {
		slog.Info("计划任务探测跳过", "task", task.Name, "scope", scope,
			"reason", "同类探测任务或 NFO 通道已被占用")
		return scheduledBusy("媒体信息探测或 NFO 通道已被占用")
	}
	if err != nil {
		// 准备阶段失败（ffprobe 自检、读库）以前只有任务行里的一句 message，
		// 日志补上作用域与原始 err，便于区分「环境坏了」还是「范围选错了」。
		slog.Error("计划任务探测准备失败", "task", task.Name, "scope", scope, "error", err)
		return err
	}
	if len(plan.targets) == 0 {
		slog.Info("计划任务探测跳过：没有可探测的影片", "task", task.Name, "scope", scope,
			"status", statusOrAll(status), "movies", plan.movies)
		return nil
	}
	if err := a.beginProbe(len(plan.targets)); err != nil {
		if errors.Is(err, errServerShuttingDown) {
			return context.Canceled
		}
		slog.Info("计划任务探测跳过", "task", task.Name, "scope", scope,
			"reason", "启动探测被拒绝（服务正在关停或探测通道被占用）")
		return scheduledBusy("媒体信息探测通道已被占用")
	}
	taskID := a.startTask("probe")
	slog.Info("计划任务探测开始", "task", task.Name, "task_id", taskID, "scope", scope,
		"status", statusOrAll(status), "only_missing", plan.onlyMissing,
		"movies", plan.movies, "files", len(plan.targets))
	return a.runProbe(taskID, plan.ffprobe, plan.targets, plan.onlyMissing)
}

// statusOrAll 把空状态（历史任务，语义为 success）显示成具体值，
// 否则日志里 status= 是空的，读者会以为筛选条件没生效。
func statusOrAll(status string) string {
	if status == "" {
		return "success"
	}
	return status
}

// —— 参数取值：Params 来自 JSON 解码，数字一律是 float64 ——

func paramInt64(params map[string]any, key string) int64 {
	switch v := params[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func paramString(params map[string]any, key string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return ""
}

func paramBool(v any) bool {
	switch value := v.(type) {
	case bool:
		return value
	case string:
		return value != "false" && value != "0" && value != ""
	case float64:
		return value != 0
	}
	return false
}

// probeStatusContract opts only probe status into the strict, additive contract.
// Unmarked legacy tasks retain their empty/null => success behavior.
const probeStatusContract = "explicit-v1"

func scheduledProbeStatus(raw map[string]any) (string, error) {
	version, strict := raw["probe_status_contract"]
	if !strict {
		return paramString(raw, "status"), nil
	}
	if version != probeStatusContract {
		return "", fmt.Errorf("unsupported probe_status_contract")
	}
	value, present := raw["status"]
	if !present {
		return "success", nil
	}
	status, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("probe status must be all, success or manual")
	}
	switch status {
	case "all", "success", "manual":
		return status, nil
	default:
		return "", fmt.Errorf("probe status must be all, success or manual")
	}
}

func scheduledScanMarker(raw map[string]any) (scanRootMarker, error) {
	pathValue, pathPresent := raw["root_marker_path"]
	digestValue, digestPresent := raw["root_marker_sha256"]
	path, pathOK := pathValue.(string)
	digest, digestOK := digestValue.(string)
	if (pathPresent && !pathOK) || (digestPresent && !digestOK) {
		return scanRootMarker{}, fmt.Errorf("root marker parameters must be strings")
	}
	return normalizeScanMarker(path, digest, pathPresent, digestPresent)
}

// scheduledScanParams preserves missing-mode legacy full behavior while refusing
// explicit malformed mode or library scope (including non-string modes).
func scheduledScanParams(raw map[string]any) (int64, scanner.ScanMode, error) {
	mode := scanner.ScanModeFull
	if value, present := raw["mode"]; present {
		text, ok := value.(string)
		if !ok {
			return 0, "", fmt.Errorf("scan mode must be full or incremental")
		}
		mode, ok = parseScanMode(text)
		if !ok {
			return 0, "", fmt.Errorf("scan mode must be full or incremental")
		}
	}
	libraryID := int64(0)
	if value, present := raw["library_id"]; present {
		var err error
		if text, ok := value.(string); ok {
			libraryID, err = strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		} else {
			data, marshalErr := json.Marshal(value)
			err = marshalErr
			if err == nil {
				if value == nil {
					err = fmt.Errorf("null scope")
				} else {
					err = json.Unmarshal(data, &libraryID)
				}
			}
		}
		if err != nil || libraryID < 0 {
			return 0, "", fmt.Errorf("scan library_id must be nonnegative")
		}
	}
	return libraryID, mode, nil
}

// normalizeParams 按任务类型把请求参数整理成规范 JSON：
// 只保留该类型认识的键，避免把垃圾字段沉淀进库。
func normalizeParams(kind string, raw map[string]any) (string, error) {
	out := map[string]any{}
	switch kind {
	case "sprites":
		params, err := parseScheduledSpriteParams(raw)
		if err != nil {
			return "", err
		}
		out["library_id"], out["limit"], out["overwrite"] = params.LibraryID, params.Limit, params.Overwrite
	case "scan":
		libraryID, mode, err := scheduledScanParams(raw)
		if err != nil {
			return "", err
		}
		// Retain opaque fields so conditional editors do not destroy future
		// options. Missing mode remains absent for stable legacy ETags.
		maps.Copy(out, raw)
		out["library_id"] = libraryID
		if _, present := raw["mode"]; present {
			out["mode"] = string(mode)
		}
	case "reindex":
		// Mode/scope never alter reindex execution, but opaque options round-trip.
		maps.Copy(out, raw)
	case "probe":
		out["library_id"] = paramInt64(raw, "library_id")
		status, err := scheduledProbeStatus(raw)
		if err != nil {
			return "", err
		}
		if _, strict := raw["probe_status_contract"]; strict {
			out["probe_status_contract"] = probeStatusContract
		}
		if status != "" {
			out["status"] = status
		}
		if limit := paramInt64(raw, "limit"); limit > 0 {
			out["limit"] = limit
		}
		if value, ok := raw["only_missing"]; ok {
			out["only_missing"] = paramBool(value)
		}
	case "scrape":
		out["library_id"] = paramInt64(raw, "library_id")
		if limit := paramInt64(raw, "limit"); limit > 0 {
			out["limit"] = limit
		}
		if value, ok := raw["only_missing"]; ok {
			out["only_missing"] = paramBool(value)
		}
		if value, ok := raw["overwrite"]; ok {
			out["overwrite"] = paramBool(value)
		}
	case "translate":
		var err error
		out, err = normalizeTranslationParams(raw)
		if err != nil {
			return "", err
		}
	case "scrape_avatars":
		out["library_id"] = paramInt64(raw, "library_id")
		if limit := paramInt64(raw, "limit"); limit > 0 {
			out["limit"] = limit
		}
	default:
		return "", fmt.Errorf("不支持的任务类型 %q", kind)
	}
	if kind == "scan" || kind == "reindex" {
		marker, err := scheduledScanMarker(raw)
		if err != nil {
			return "", err
		}
		if _, present := raw["root_marker_path"]; present {
			out["root_marker_path"] = marker.Path
			out["root_marker_sha256"] = marker.SHA256
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// —— HTTP handlers ——

func (a *App) adminScheduledTasks(c *gin.Context) {
	tasks, err := a.db.ScheduledTasks()
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]scheduledView, 0, len(tasks))
	for _, task := range tasks {
		view := scheduledResponse(task)
		if next := a.sched.Next(task.ID); !next.IsZero() {
			view.NextRun = next.Format(time.RFC3339)
		}
		items = append(items, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items), "types": scheduledTypes})
}

// scheduledRequest 新建/修改计划任务的入参。
type scheduledRequest struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Cron    string         `json:"cron"`
	Params  map[string]any `json:"params"`
	Enabled *bool          `json:"enabled"`
}

// validateScheduled 校验并规范化入参，返回落库用的记录（不含 ID）。
func validateScheduled(req scheduledRequest) (store.ScheduledTask, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return store.ScheduledTask{}, errors.New("名称不能为空")
	}
	kind := strings.ToLower(strings.TrimSpace(req.Type))
	if _, ok := scheduledTypes[kind]; !ok {
		return store.ScheduledTask{}, fmt.Errorf("不支持的任务类型 %q", kind)
	}
	expr := strings.TrimSpace(req.Cron)
	if expr == "" {
		return store.ScheduledTask{}, errors.New("cron 表达式不能为空")
	}
	if err := scheduler.Validate(expr); err != nil {
		return store.ScheduledTask{}, fmt.Errorf("cron 表达式非法: %w", err)
	}
	params, err := normalizeParams(kind, req.Params)
	if err != nil {
		return store.ScheduledTask{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return store.ScheduledTask{Name: name, Type: kind, Cron: expr, Params: params, Enabled: enabled}, nil
}

func (a *App) adminCreateScheduledTask(c *gin.Context) {
	var req scheduledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "invalid json")
		return
	}
	task, err := validateScheduled(req)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	created, err := a.db.CreateScheduledTask(task)
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	a.reloadScheduled()
	slog.Info("新建计划任务", "task_id", created.ID, "name", created.Name, "type", created.Type, "cron", created.Cron)
	c.JSON(http.StatusOK, scheduledResponse(created))
}

// adminConditionalScheduledTask requires a validator. This additive endpoint
// prevents a stale new UI from falling back to an older server's unconditional PUT.
// The body is the same complete configuration as PUT, not a JSON Merge Patch.
func (a *App) adminConditionalScheduledTask(c *gin.Context) {
	if _, present := c.Request.Header["If-Match"]; !present {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "configuration validator required", "code": "SCHEDULED_PRECONDITION_REQUIRED"})
		return
	}
	a.adminUpdateScheduledTask(c)
}

func (a *App) adminUpdateScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	var req scheduledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "invalid json")
		return
	}
	task, err := validateScheduled(req)
	if err != nil {
		apiError(c, http.StatusBadRequest, err.Error())
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	task.ID = id
	var updated store.ScheduledTask
	if values, conditional := c.Request.Header["If-Match"]; conditional {
		if len(values) != 1 {
			apiError(c, http.StatusBadRequest, "one configuration validator required")
			return
		}
		current, readErr := a.db.ScheduledTask(id)
		if readErr != nil {
			if store.NotFound(readErr) {
				c.JSON(http.StatusPreconditionFailed, gin.H{"code": "SCHEDULED_CONFIG_CHANGED", "error": "计划配置已变化或删除，请保留当前输入，刷新后核对"})
			} else {
				apiError(c, http.StatusInternalServerError, "configuration unavailable")
			}
			return
		}
		if strings.TrimSpace(values[0]) != scheduledConfigETag(current) {
			c.JSON(http.StatusPreconditionFailed, gin.H{"code": "SCHEDULED_CONFIG_CHANGED", "error": "计划配置已变化，请保留当前输入，刷新后核对再保存"})
			return
		}
		updated, err = a.db.CompareAndSwapScheduledTask(current, task)
	} else {
		// Compatibility: existing clients without validators retain PUT behavior.
		updated, err = a.db.UpdateScheduledTask(task)
	}
	if err != nil {
		if errors.Is(err, store.ErrScheduledConfigChanged) {
			c.JSON(http.StatusPreconditionFailed, gin.H{"code": "SCHEDULED_CONFIG_CHANGED", "error": "计划配置已变化，请保留当前输入，刷新后核对再保存"})
			return
		}
		if store.NotFound(err) {
			apiError(c, http.StatusNotFound, "task not found")
			return
		}
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	a.reloadScheduled()
	slog.Info("修改计划任务", "task_id", updated.ID, "name", updated.Name, "enabled", updated.Enabled)
	c.JSON(http.StatusOK, scheduledResponse(updated))
}

// adminToggleScheduledTask 只切换启用状态；调度器随即重建，无需重启。
func (a *App) adminToggleScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	current, err := a.db.ScheduledTask(id)
	if err != nil {
		apiError(c, http.StatusNotFound, "task not found")
		return
	}
	if err := a.db.SetScheduledTaskEnabled(id, !current.Enabled); err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	a.reloadScheduled()
	updated, err := a.db.ScheduledTask(id)
	if err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("切换计划任务状态", "task_id", id, "enabled", updated.Enabled)
	c.JSON(http.StatusOK, scheduledResponse(updated))
}

func (a *App) adminDeleteScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	if err := a.db.DeleteScheduledTask(id); err != nil {
		if store.NotFound(err) {
			apiError(c, http.StatusNotFound, "task not found")
			return
		}
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	a.reloadScheduled()
	slog.Info("删除计划任务", "task_id", id)
	c.Status(http.StatusNoContent)
}

// adminRunScheduledTask 立即执行一次（异步；结果走任务页与任务行的上次结果）。
func (a *App) adminRunScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		apiError(c, http.StatusNotFound, "not found")
		return
	}
	if _, err := a.db.ScheduledTask(id); err != nil {
		apiError(c, http.StatusNotFound, "task not found")
		return
	}
	if !a.admitAdminOperation(c) {
		return
	}
	// 关停门：drain 期间不再接受「立即执行」，否则 worker 会与 Wait/关库并发。
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	go func() {
		defer release()
		if err := a.sched.RunNow(id); err != nil {
			slog.Warn("立即执行计划任务失败", "task_id", id, "error", err)
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"status": "started"})
}

// adminValidateCron 即时校验表达式并预览后续执行时间（不落库）。
func (a *App) adminValidateCron(c *gin.Context) {
	var req struct {
		Cron string `json:"cron"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		apiError(c, http.StatusBadRequest, "invalid json")
		return
	}
	runs, err := scheduler.Preview(strings.TrimSpace(req.Cron), 5)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"valid": false, "error": err.Error()})
		return
	}
	times := make([]string, 0, len(runs))
	for _, run := range runs {
		times = append(times, run.Format(time.RFC3339))
	}
	c.JSON(http.StatusOK, gin.H{"valid": true, "next_runs": times, "time_zone": time.Now().Location().String()})
}

// reloadScheduled 让调度器重新装载（保存后即时生效）。失败只告警：库里已有定义，重启仍会生效。
func (a *App) reloadScheduled() {
	if err := a.sched.Reload(); err != nil {
		slog.Error("重新装载计划任务失败", "error", err)
	}
}

// —— 计划任务里的刮削类型（需求 5i）——

// runScheduledScrape 定时批量刮削：同步执行，调度器据此判定重叠。
// 与手动批量走同一条路径，因此同样「全部结束后整库扫一次」。
func (a *App) runScheduledScrape(task scheduler.Task) error {
	scope := scheduledScope(task.Params)
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		// 以前这里直接 return errors.New，日志里连「为什么没跑」都没有，只有任务行一句话。
		slog.Warn("计划任务刮削未执行：未配置 MetaTube 地址或 token", "task", task.Name, "scope", scope)
		return errors.New("未配置 MetaTube 地址或 token")
	}
	onlyMissing := true
	if value, ok := task.Params["only_missing"]; ok {
		onlyMissing = paramBool(value)
	}
	// 计划任务的覆盖策略默认取全局配置，允许在任务参数里覆盖。
	overwrite := cfg.Overwrite
	if value, ok := task.Params["overwrite"]; ok {
		overwrite = paramBool(value)
	}
	libraryID := paramInt64(task.Params, "library_id")
	limit := int(paramInt64(task.Params, "limit"))

	if owner := a.nfoBusyOwner(); owner != "" {
		slog.Info("计划任务刮削跳过", "task", task.Name, "scope", scope,
			"reason", owner+" 正在占用 NFO 写入通道")
		return scheduledBusy(owner + " 正在占用 NFO 写入通道")
	}
	total, err := a.db.CountMoviesForScrape(libraryID, onlyMissing)
	if err != nil {
		slog.Error("计划任务刮削统计待刮削影片失败，本次未执行", "task", task.Name, "scope", scope,
			"library_id", libraryID, "only_missing", onlyMissing, "error", err)
		return err
	}
	if total == 0 {
		slog.Info("计划任务刮削跳过：没有符合条件的影片", "task", task.Name, "scope", scope,
			"library_id", libraryID, "only_missing", onlyMissing)
		return nil
	}
	if err := a.beginScrape("scrape", total); err != nil {
		if errors.Is(err, errServerShuttingDown) {
			return context.Canceled
		}
		slog.Info("计划任务刮削跳过", "task", task.Name, "scope", scope,
			"reason", "同类刮削任务正在进行")
		return scheduledBusy("同类刮削任务正在进行")
	}
	slog.Info("计划任务刮削启动", "task", task.Name, "scope", scope, "total", total,
		"library_id", libraryID, "limit", limit, "only_missing", onlyMissing, "overwrite", overwrite)
	// NFO 通道由 executeScrape 在自己的栈里 claim + defer release。
	return a.executeScrape(cfg, scraper.RunOptions{
		LibraryID: libraryID, OnlyMissing: onlyMissing, Limit: limit, Overwrite: overwrite,
	})
}

// runScheduledScrapeAvatars 定时补演员头像。
func (a *App) runScheduledScrapeAvatars(task scheduler.Task) error {
	scope := scheduledScope(task.Params)
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		slog.Warn("计划任务头像补全未执行：未配置 MetaTube 地址或 token", "task", task.Name, "scope", scope)
		return errors.New("未配置 MetaTube 地址或 token")
	}
	libraryID := paramInt64(task.Params, "library_id")
	limit := int(paramInt64(task.Params, "limit"))

	if owner := a.nfoBusyOwner(); owner != "" {
		slog.Info("计划任务头像补全跳过", "task", task.Name, "scope", scope,
			"reason", owner+" 正在占用 NFO 写入通道")
		return scheduledBusy(owner + " 正在占用 NFO 写入通道")
	}
	names, err := a.db.ActorsMissingAvatar(libraryID, limit)
	if err != nil {
		slog.Error("计划任务头像补全统计缺头像演员失败，本次未执行", "task", task.Name, "scope", scope,
			"library_id", libraryID, "limit", limit, "error", err)
		return err
	}
	if len(names) == 0 {
		slog.Info("计划任务头像补全跳过：没有缺头像的演员", "task", task.Name, "scope", scope,
			"library_id", libraryID, "limit", limit)
		return nil
	}
	if err := a.beginScrape("scrape_avatars", len(names)); err != nil {
		if errors.Is(err, errServerShuttingDown) {
			return context.Canceled
		}
		slog.Info("计划任务头像补全跳过", "task", task.Name, "scope", scope,
			"reason", "同类刮削任务正在进行")
		return scheduledBusy("同类刮削任务正在进行")
	}
	slog.Info("计划任务头像补全启动", "task", task.Name, "scope", scope, "actors", len(names),
		"library_id", libraryID, "limit", limit)
	// NFO 通道由 executeScrapeAvatars 在自己的栈里 claim + defer release。
	return a.executeScrapeAvatars(cfg, libraryID, limit)
}
