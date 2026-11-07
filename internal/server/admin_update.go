package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// adminUpdate 返回最新版本检查结果。GET 使用缓存（页面打开自动检查时不必每次都打
// GitHub API），POST /update/check 强制重新查询。查询失败返回 502 与错误原因，
// 前端据此区分「检查失败」与「已是最新」。
func (a *App) adminUpdate(c *gin.Context) {
	result, err := a.updater.Check(c.Request.Context(), c.Request.Method != http.MethodGet)
	if err != nil {
		apiError(c, http.StatusBadGateway, "检查更新失败："+err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"current":      result.Current,
		"latest":       result.Latest,
		"available":    result.Available,
		"notes":        result.Notes,
		"published_at": result.PublishedAt,
		"checked_at":   result.CheckedAt,
	})
}

// adminUpdateApply 预留安装并用后台 worker 下载 / 校验 / 原子替换。
// 版本号必须等于刚检查到的 Release：客户端不能指定任意下载源或安装路径。
func (a *App) adminUpdateApply(c *gin.Context) {
	var body struct {
		Version string `json:"version"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		apiError(c, http.StatusBadRequest, "请求格式错误")
		return
	}
	result, err := a.updater.Check(c.Request.Context(), true)
	if err != nil {
		apiError(c, http.StatusBadGateway, "检查更新失败："+err.Error())
		return
	}
	if !result.Available || body.Version != result.Latest {
		apiError(c, http.StatusConflict, "目标版本已变化，请重新检查更新")
		return
	}
	release, ok := a.beginWorkerHTTP(c)
	if !ok {
		return
	}
	if err := a.updater.Begin(result.Latest); err != nil {
		release()
		apiError(c, http.StatusConflict, err.Error())
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"version": result.Latest})
	go func() {
		// 升级 worker 由关停门登记：先完成「替换 → 发出重启信号」，main 才关停进程。
		defer release()
		a.updater.Run(a.rootCtx, result.Latest)
	}()
}

// adminUpdateStatus 供前端在下载/重启过程中轮询进度。
func (a *App) adminUpdateStatus(c *gin.Context) {
	c.JSON(http.StatusOK, a.updater.State())
}

// UpgradeRestart 在新程序已就位时返回关闭信号，main 据此走与 SIGTERM 相同的
// 有界关闭路径；进程以 0 退出，由 systemd（Restart=always）拉起新版本。
func (a *App) UpgradeRestart() <-chan struct{} { return a.updater.Restart() }
