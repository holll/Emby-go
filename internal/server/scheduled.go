package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

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
}

// scheduledView 计划任务列表项：定义 + 下次执行时间。
type scheduledView struct {
	store.ScheduledTask
	NextRun string `json:"next_run,omitempty"`
}

// —— 执行器：把调度器回调接到具体的服务动作上 ——

// runScheduledTask 执行一条计划任务。返回 scheduler.ErrBusy 表示同类任务未结束、本次跳过。
func (a *App) runScheduledTask(_ context.Context, task scheduler.Task) error {
	switch task.Type {
	case "scan", "reindex":
		return a.runScheduledScan(task)
	case "probe":
		return a.runScheduledProbe(task)
	case "scrape":
		return a.runScheduledScrape(task)
	case "scrape_avatars":
		return a.runScheduledScrapeAvatars(task)
	default:
		return fmt.Errorf("不支持的任务类型 %q", task.Type)
	}
}

// runScheduledScan 执行扫描/重建索引（同步阻塞直到结束，调度器据此判定重叠）。
func (a *App) runScheduledScan(task scheduler.Task) error {
	libraryID := int64(0)
	if task.Type == "scan" {
		libraryID = paramInt64(task.Params, "library_id")
	}
	if a.scanning() {
		return scheduler.ErrBusy
	}
	taskID := a.startTask(task.Type)
	result, err := a.scanLibraries(libraryID)
	if errors.Is(err, errScanBusy) {
		a.finishTaskBusy(taskID, err.Error())
		return scheduler.ErrBusy
	}
	a.finishTask(taskID, err)
	if err != nil {
		return err
	}
	slog.Info("计划任务扫描完成", "task", task.Name, "library_id", libraryID,
		"success", result.Success, "pending", result.Pending,
		"incompatible", result.Incompatible, "failed", result.Failed)
	return nil
}

// runScheduledProbe 执行媒体信息探测（同步阻塞直到结束）。
func (a *App) runScheduledProbe(task scheduler.Task) error {
	req := probeRequest{
		LibraryID: paramInt64(task.Params, "library_id"),
		Status:    paramString(task.Params, "status"),
		Limit:     int(paramInt64(task.Params, "limit")),
	}
	if value, ok := task.Params["only_missing"]; ok {
		onlyMissing := paramBool(value)
		req.OnlyMissing = &onlyMissing
	}
	plan, err := a.prepareProbe(req)
	if errors.Is(err, errProbeBusy) {
		return scheduler.ErrBusy
	}
	if err != nil {
		return err
	}
	if len(plan.targets) == 0 {
		slog.Info("计划任务探测跳过：没有可探测的影片", "task", task.Name)
		return nil
	}
	if !a.beginProbe(len(plan.targets)) {
		return scheduler.ErrBusy
	}
	taskID := a.startTask("probe")
	return a.runProbe(taskID, plan.ffprobe, plan.targets, plan.onlyMissing)
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

// normalizeParams 按任务类型把请求参数整理成规范 JSON：
// 只保留该类型认识的键，避免把垃圾字段沉淀进库。
func normalizeParams(kind string, raw map[string]any) (string, error) {
	out := map[string]any{}
	switch kind {
	case "scan":
		out["library_id"] = paramInt64(raw, "library_id")
	case "reindex":
		// 无参数
	case "probe":
		out["library_id"] = paramInt64(raw, "library_id")
		if status := paramString(raw, "status"); status != "" {
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
	case "scrape_avatars":
		out["library_id"] = paramInt64(raw, "library_id")
		if limit := paramInt64(raw, "limit"); limit > 0 {
			out["limit"] = limit
		}
	default:
		return "", fmt.Errorf("不支持的任务类型 %q", kind)
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]scheduledView, 0, len(tasks))
	for _, task := range tasks {
		view := scheduledView{ScheduledTask: task}
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	task, err := validateScheduled(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, err := a.db.CreateScheduledTask(task)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.reloadScheduled()
	slog.Info("新建计划任务", "task_id", created.ID, "name", created.Name, "type", created.Type, "cron", created.Cron)
	c.JSON(http.StatusOK, created)
}

func (a *App) adminUpdateScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var req scheduledRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	task, err := validateScheduled(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	task.ID = id
	updated, err := a.db.UpdateScheduledTask(task)
	if err != nil {
		if store.NotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.reloadScheduled()
	slog.Info("修改计划任务", "task_id", updated.ID, "name", updated.Name, "enabled", updated.Enabled)
	c.JSON(http.StatusOK, updated)
}

// adminToggleScheduledTask 只切换启用状态；调度器随即重建，无需重启。
func (a *App) adminToggleScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	current, err := a.db.ScheduledTask(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if err := a.db.SetScheduledTaskEnabled(id, !current.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	a.reloadScheduled()
	updated, err := a.db.ScheduledTask(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	slog.Info("切换计划任务状态", "task_id", id, "enabled", updated.Enabled)
	c.JSON(http.StatusOK, updated)
}

func (a *App) adminDeleteScheduledTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err := a.db.DeleteScheduledTask(id); err != nil {
		if store.NotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if _, err := a.db.ScheduledTask(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	go func() {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
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
	c.JSON(http.StatusOK, gin.H{"valid": true, "next_runs": times})
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
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
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
		return scheduler.ErrBusy
	}
	total, err := a.db.CountMoviesForScrape(libraryID, onlyMissing)
	if err != nil {
		return err
	}
	if total == 0 {
		slog.Info("计划任务刮削跳过：没有符合条件的影片", "task", task.Name)
		return nil
	}
	if !a.beginScrape("scrape", total) {
		return scheduler.ErrBusy
	}
	if !a.claimNFO("scrape") {
		a.endScrape(nil)
		return scheduler.ErrBusy
	}
	slog.Info("计划任务刮削启动", "task", task.Name, "total", total,
		"library_id", libraryID, "only_missing", onlyMissing, "overwrite", overwrite)
	return a.executeScrape(cfg, scraper.RunOptions{
		LibraryID: libraryID, OnlyMissing: onlyMissing, Limit: limit, Overwrite: overwrite,
	})
}

// runScheduledScrapeAvatars 定时补演员头像。
func (a *App) runScheduledScrapeAvatars(task scheduler.Task) error {
	cfg := a.scrapeConfig()
	if !cfg.Configured() {
		return errors.New("未配置 MetaTube 地址或 token")
	}
	libraryID := paramInt64(task.Params, "library_id")
	limit := int(paramInt64(task.Params, "limit"))

	if owner := a.nfoBusyOwner(); owner != "" {
		return scheduler.ErrBusy
	}
	names, err := a.db.ActorsMissingAvatar(libraryID, limit)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		slog.Info("计划任务头像补全跳过：没有缺头像的演员", "task", task.Name)
		return nil
	}
	if !a.beginScrape("scrape_avatars", len(names)) {
		return scheduler.ErrBusy
	}
	if !a.claimNFO("scrape_avatars") {
		a.endScrape(nil)
		return scheduler.ErrBusy
	}
	slog.Info("计划任务头像补全启动", "task", task.Name, "actors", len(names), "library_id", libraryID)
	return a.executeScrapeAvatars(cfg, libraryID, limit)
}
