// Package avatar 定义演员头像的落盘位置与内容标识。
//
// 头像采用三层模型（见需求 A4）：NFO 的 <actor><thumb> 是**真源**，
// avatars/ 目录下是服务本地副本（服务用），actors 表只作索引/快路径。
//
// 文件名由演员名稳定映射（同一演员重刮直接覆盖，不产生孤儿文件）；
// 内容标识由图片字节算出，作为 Emby 的 PrimaryImageTag——
// 内容变了标识才变，客户端才会重新拉图。
package avatar

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Ext 本地副本扩展名；与影片图片一致统一为 webp。
const Ext = ".webp"

// tagLen 标识与文件名取哈希前多少位十六进制字符（足够避免碰撞且不冗长）。
const tagLen = 16

// FileName 返回演员头像的文件名。按姓名归一化（去空白 + 小写）后哈希，
// 保证同一演员只有一份副本。
func FileName(name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	return hex.EncodeToString(sum[:])[:tagLen] + Ext
}

// Path 返回演员头像在 dir 下的完整路径。
func Path(dir, name string) string { return filepath.Join(dir, FileName(name)) }

// Tag 由图片字节算出内容标识；data 为空时返回空串，调用方据此跳过写库。
func Tag(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:tagLen]
}

// DefaultDir 返回默认头像目录：与数据库文件同级的 avatars/。
func DefaultDir(dbPath string) string {
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		return "avatars"
	}
	return filepath.Join(dir, "avatars")
}
