package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServerDomain 备用线路候选。JSON 字段名必须是小写 name/url，
// 与真实 Emby /System/Ext/ServerDomains 的返回一致（客户端按小写键解析）。
type ServerDomain struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`
}

type Config struct {
	Port          int            `yaml:"port"` // 监听端口（如 18080）
	DBPath        string         `yaml:"db_path"`
	ServerName    string         `yaml:"server_name"` // 对外站点名（System/Info 的 ServerName）
	ServerID      string         `yaml:"server_id"`   // Emby ServerId；留空则首次启动生成稳定 UUID 存 DB 并回写
	Debug         bool           `yaml:"debug"`       // 开启后启用 gin 调试模式与详细请求日志
	RedisAddr     string         `yaml:"redis_addr"`
	RedisPassword string         `yaml:"redis_password"`
	RedisDB       int            `yaml:"redis_db"`
	ServerDomains []ServerDomain `yaml:"server_domains"`

	path string // 配置文件来源路径（非 yaml 字段），供写回使用
}

// Addr 返回 http 监听地址（":"+port；默认 18080）。
func (c Config) Addr() string {
	if c.Port <= 0 {
		return ":18080"
	}
	return fmt.Sprintf(":%d", c.Port)
}

// Redis 为必选缓存后端：服务启动时即连接并 Ping，连不上直接拒绝启动。
func Load(path string) (Config, error) {
	cfg := Config{Port: 18080, DBPath: "emby-go.db", ServerName: "Emby-go", path: path}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Port <= 0 {
		cfg.Port = 18080
	}
	cfg.path = path
	return cfg, nil
}

// PersistServerID 把自动生成的 ServerId 手术式写回配置文件对应行，保留注释与其它字段。
// 无配置文件来源（path 为空）或写失败时返回错误，由调用方决定是否忽略。
func (c Config) PersistServerID(serverID string) error {
	if c.path == "" {
		return nil
	}
	file, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	line := regexp.MustCompile(`(?m)^[ \t]*server_id[ \t]*:[^\r\n]*$`)
	quoted := `server_id: "` + serverID + `"`
	if line.Match(file) {
		return os.WriteFile(c.path, line.ReplaceAll(file, []byte(quoted)), 0644)
	}
	// 文件里没有 server_id 键：在 server_name 行后补一行（保证顶层缩进），否则追加到文件末尾。
	lines := strings.Split(string(file), "\n")
	insert := len(lines)
	for i, ln := range lines {
		if strings.HasPrefix(ln, "server_name:") {
			insert = i + 1
			break
		}
	}
	lines = append(lines[:insert], append([]string{quoted}, lines[insert:]...)...)
	return os.WriteFile(c.path, []byte(strings.Join(lines, "\n")), 0644)
}
