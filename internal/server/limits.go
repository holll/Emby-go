package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// 请求体预算。JSON 端点都很小，只有图片上传需要更大空间。
// 不设上限时单个客户端就能把任意大小的请求体灌进内存或临时盘
// （JSON 在内存里解码，multipart 会落到临时文件）。
const (
	defaultRequestBodyLimit int64 = 1 << 20  // 1 MiB
	imageUploadBodyLimit    int64 = 32 << 20 // 32 MiB：海报/背景/横幅/媒体库封面
)

// bodyLimitFor 返回路径对应的请求体预算。白名单之外一律用默认预算，
// 不存在“不限量”的路径。
func bodyLimitFor(path string) int64 {
	if strings.HasPrefix(path, "/api/admin/items/") && strings.Contains(path, "/images/") {
		return imageUploadBodyLimit
	}
	if strings.HasPrefix(path, "/api/admin/libraries/") && strings.HasSuffix(path, "/cover") {
		return imageUploadBodyLimit
	}
	return defaultRequestBodyLimit
}

// requestBodyLimit 在 handler 读请求体之前施加预算。
// 声明了 Content-Length 且超预算的，直接 413。
// chunked 请求体由 MaxBytesReader 截断：handler 的 bind 会失败（通常 400），
// 连接随即关闭。这里只能做到尽力而为——handler 已经写出的响应无法再改成 413。
func requestBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := bodyLimitFor(c.Request.URL.Path)
		if c.Request.ContentLength > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "request body too large", "code": "BODY_TOO_LARGE",
			})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
