package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// 扫描成功后自动补封面的两个开关，默认都关：apply 会写库并替换客户端看到的
// 封面，属于修改 Emby 数据，不能让默认配置在用户不知情时发生。
const (
	settingCollectionCoverAuto = "collection.cover_auto"
	settingLibraryCoverAuto    = "library.cover_auto"
)

// errCoverTaskBusy 与 errScrapeBusy 同义：同一时刻只允许一个封面任务。
var errCoverTaskBusy = errors.New("封面任务正在进行中")

// coverTargetKind 区分两类封面任务的目标：合集按名字，媒体库按 id。
type coverTargetKind string

const (
	coverTargetCollection coverTargetKind = "collection"
	coverTargetLibrary    coverTargetKind = "library"
)

// coverTarget 是一次批量任务里的单个目标。
type coverTarget struct {
	Kind coverTargetKind
	Name string // 展示名：合集名或媒体库名
	ID   int64  // 媒体库 id；合集为 0
}

func (a *App) collectionCoverAuto() bool {
	return a.coverAuto(settingCollectionCoverAuto)
}

func (a *App) libraryCoverAuto() bool {
	return a.coverAuto(settingLibraryCoverAuto)
}

// coverAuto 读自动触发开关。默认开启，对齐 Emby「合集/媒体库本该自己出图」的行为；
// 用户显式关掉才落 DB 设置项。读不到设置时按关闭处理——读不到状态就不主动改数据。
func (a *App) coverAuto(setting string) bool {
	values, err := a.db.Settings()
	if err != nil {
		return false
	}
	return settingBool(values, setting, true)
}

// startCoverTask 登记并启动一次封面批量任务。手动端点与扫描后的自动触发共用
// 这一条路径：同一时刻只允许一个任务，登记进 workerWG 保证关停时被 Wait 收齐。
func (a *App) startCoverTask(targets []coverTarget, opts coverOptions, mode string) (coverTaskStatus, error) {
	a.coverTaskMu.Lock()
	if a.rootCtx.Err() != nil {
		a.coverTaskMu.Unlock()
		return coverTaskStatus{}, errServerShuttingDown
	}
	// 登记在已持有的状态锁内、置运行态之前：关停窗口里既不会留下「Running 但没人跑」
	// 的状态，也不会出现 Add 与 workerWG.Wait 并发。
	release, err := a.beginWorker()
	if err != nil {
		a.coverTaskMu.Unlock()
		return coverTaskStatus{}, err
	}
	if a.coverStatus.Running {
		a.coverTaskMu.Unlock()
		release()
		return coverTaskStatus{}, errCoverTaskBusy
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	label := targetKindLabel(targets)
	taskID := a.startTask("cover_" + label + "_" + mode)
	accepted := coverTaskStatus{
		InstanceID: a.instanceID, TaskID: taskID, Kind: label, Mode: mode,
		Running: true, Total: len(targets),
	}
	for _, t := range targets {
		if t.Kind == coverTargetLibrary {
			accepted.Libraries = append(accepted.Libraries, t.ID)
		} else {
			accepted.Collections = append(accepted.Collections, t.Name)
		}
	}
	a.coverStatus = accepted
	a.coverCancel = cancel
	a.coverTaskMu.Unlock()
	go func() { defer release(); a.executeCoverTargets(ctx, taskID, targets, opts) }()
	return accepted, nil
}

// targetKindLabel 是任务类型标签：目标全是同类就取该类，混着就标 mixed。
func targetKindLabel(targets []coverTarget) string {
	if len(targets) == 0 {
		return ""
	}
	kind := targets[0].Kind
	for _, t := range targets {
		if t.Kind != kind {
			return "mixed"
		}
	}
	return string(kind)
}

// collectionTargets 把合集名折成任务目标。
func collectionTargets(names []string) []coverTarget {
	targets := make([]coverTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, coverTarget{Kind: coverTargetCollection, Name: name})
	}
	return targets
}

// maybeAutoCovers 在扫描提交了索引变更后按开关补一遍封面。
// 内容没变的目标在 generate*Cover 里按 SourceHash 直接判 up to date，
// 所以这里不做差异比对，交给缓存键判断。
func (a *App) maybeAutoCovers() {
	// 两类目标合成一个任务：同一时刻只允许一个封面任务，分两次启动第二次必然
	// 撞上 errCoverTaskBusy 而整批跳过。
	var targets []coverTarget
	if a.collectionCoverAuto() {
		targets = append(targets, collectionTargets(a.cachedCollections())...)
	}
	if a.libraryCoverAuto() {
		targets = append(targets, a.allLibraryTargets()...)
	}
	if len(targets) == 0 {
		return
	}
	if _, err := a.startCoverTask(targets, coverOptions{Reflection: true, Apply: true}, "auto"); err != nil {
		// 手动任务在跑时自动触发让位：不排队、不报错，下一轮扫描再来。
		slog.Info("自动封面跳过", "reason", err)
	}
}

// allLibraryTargets 返回全部媒体库目标。
func (a *App) allLibraryTargets() []coverTarget {
	libraries, err := a.db.Libraries()
	if err != nil {
		return nil
	}
	targets := make([]coverTarget, 0, len(libraries))
	for _, l := range libraries {
		targets = append(targets, coverTarget{Kind: coverTargetLibrary, Name: l.Name, ID: l.ID})
	}
	return targets
}

// adminCollectionCoverSettings 读写自动触发开关。
func (a *App) adminCollectionCoverSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"auto": a.collectionCoverAuto()})
}

func (a *App) adminSetCollectionCoverSettings(c *gin.Context) {
	a.setCoverAuto(c, settingCollectionCoverAuto)
}

func (a *App) adminLibraryCoverSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"auto": a.libraryCoverAuto()})
}

func (a *App) adminSetLibraryCoverSettings(c *gin.Context) {
	a.setCoverAuto(c, settingLibraryCoverAuto)
}

func (a *App) setCoverAuto(c *gin.Context, setting string) {
	var req struct {
		Auto *bool `json:"auto"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || !errors.Is(dec.Decode(new(any)), io.EOF) || req.Auto == nil {
		apiError(c, http.StatusBadRequest, "invalid cover settings")
		return
	}
	if err := a.db.SetSetting(setting, strconv.FormatBool(*req.Auto)); err != nil {
		apiError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"auto": *req.Auto})
}
