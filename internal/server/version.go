package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 构建期 -ldflags 写入 main.version/commit/buildTime，main 在启动前转发。
// 发布版本格式为 v<tag>-<7位提交号>；未注入的本地构建明确显示 dev。
// 这些值仅在启动时设置，服务运行期间不再修改。
var (
	BuildVersion = "dev"
	BuildCommit  = "none"
	BuildTime    = "unknown"
)

// buildInfo 与 Emby 的能力版本分开，便于确认正在运行的实际二进制。
func (a *App) buildInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"product":    "Emby-go",
		"version":    BuildVersion,
		"commit":     BuildCommit,
		"build_time": BuildTime,
	})
}

// repoURL 项目仓库地址，管理页左下角展示。
const repoURL = "https://github.com/holll/Emby-go"
