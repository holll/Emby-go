package server

import (
	"log/slog"
	"time"
)

// statsAnalyzeInterval 统计信息刷新周期：大库批量增删后让查询规划器重新采样。
// 迁移 6/11 会在升级时各跑一次；这里覆盖长期运行的实例。
const statsAnalyzeInterval = 7 * 24 * time.Hour

// startMaintenance 启动周期性维护任务（当前为 ANALYZE），随 Close 退出。
// 幂等：重复调用只启动一次。
func (a *App) startMaintenance() {
	a.maintenanceOnce.Do(func() {
		a.workerWG.Go(func() {
			a.runStatsAnalyze()
			ticker := time.NewTicker(statsAnalyzeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-a.rootCtx.Done():
					return
				case <-ticker.C:
					a.runStatsAnalyze()
				}
			}
		})
	})
}

func (a *App) runStatsAnalyze() {
	analyze := a.analyzeStats
	if analyze == nil {
		return
	}
	if err := analyze(); err != nil {
		slog.Warn("刷新 SQLite 统计信息失败", "error", err)
	}
}
