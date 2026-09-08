package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// accessTokenTTL 会话令牌在 Redis 中的存活时长。SQLite access_tokens 仍是持久真源，
// Redis 仅作每请求校验的快速通道，miss 时回源 DB 并回填。
const accessTokenTTL = 7 * 24 * time.Hour

func (a *App) authOK(c *gin.Context) bool {
	token := c.GetHeader("X-Emby-Token")
	if token == "" {
		token = c.GetHeader("X-MediaBrowser-Token")
	}
	if token == "" {
		token = c.Query("api_key")
	}
	if token == "" {
		token = c.Query("ApiKey")
	}
	if token == "" {
		token = c.Query("X-Emby-Token")
	}
	if token == "" {
		authorization := c.GetHeader("X-Emby-Authorization")
		if authorization == "" {
			authorization = c.GetHeader("Authorization")
		}
		for _, part := range strings.Split(authorization, ",") {
			key, value, found := strings.Cut(strings.TrimSpace(part), "=")
			if found && strings.EqualFold(key, "Token") {
				token = strings.Trim(strings.TrimSpace(value), "\\\"")
				break
			}
		}
	}
	if token == "" {
		return false
	}
	if b, ok := a.cache.Get("token:" + token); ok && len(b) > 0 {
		return true
	}
	valid, err := a.db.HasAccessToken(token)
	if err == nil && valid {
		a.cache.Set("token:"+token, []byte("1"), accessTokenTTL)
	}
	return valid
}

func (a *App) requireAuth(c *gin.Context) {
	if !a.authOK(c) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.Next()
}

func (a *App) authStatus(c *gin.Context) {
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"initialized": initialized})
}

func validCredentials(username, password string) bool {
	return len([]rune(strings.TrimSpace(username))) >= 3 && len([]rune(password)) >= 9
}

func (a *App) initialize(c *gin.Context) {
	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
	}
	if c.ShouldBindJSON(&req) != nil || !validCredentials(req.Username, req.Pw) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "账号至少 3 个字符，密码至少 9 个字符"})
		return
	}
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if initialized {
		c.JSON(http.StatusConflict, gin.H{"error": "管理员已初始化"})
		return
	}
	if err := a.db.InitializeAdministrator(strings.TrimSpace(req.Username), req.Pw); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "初始化失败，请更换账号后重试"})
		return
	}
	a.adminName = strings.TrimSpace(req.Username)
	c.Status(http.StatusNoContent)
}

func (a *App) authenticate(c *gin.Context) {
	var req struct {
		Username string `json:"Username"`
		Pw       string `json:"Pw"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credentials payload"})
		return
	}
	initialized, err := a.db.HasAdministrator()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !initialized {
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "管理员尚未初始化"})
		return
	}
	valid, err := a.db.AuthenticateAdministrator(strings.TrimSpace(req.Username), req.Pw)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成访问令牌失败"})
		return
	}
	a.adminName = strings.TrimSpace(req.Username)
	token := hex.EncodeToString(b)
	if err := a.db.SaveAccessToken(token); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存访问令牌失败"})
		return
	}
	a.cache.Set("token:"+token, []byte("1"), accessTokenTTL)
	c.JSON(http.StatusOK, gin.H{
		"AccessToken": token,
		"ServerId":    a.serverID,
		"User": gin.H{
			"Id": "1", "Name": a.adminName, "ServerId": a.serverID, "HasPassword": true,
			"Configuration": gin.H{}, "Policy": gin.H{},
		},
	})
}

func (a *App) me(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"Id": "1", "Name": a.adminName, "ServerId": a.serverID, "HasPassword": true})
}

func (a *App) validUser(c *gin.Context) bool {
	if c.Param("uid") != "1" {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return false
	}
	return true
}
