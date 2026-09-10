package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"emby-go/internal/probe"
)

// mediainfo.json：.strm 同目录下的探测缓存，保存 ffprobe 的原始 JSON。
//
// 为什么要这个文件（而不是只看 NFO）：
//  1. 「是否已探测」需要一个确定判据。看 NFO 有没有 <streamdetails> 不可靠——
//     刮削器也会写 streamdetails（且常不完整：缺位深/色彩特性/体积），
//     按有无判定会把该补齐的条目永久跳过。
//  2. 原始输出完整保留。NFO 只承载映射后的子集（丢掉 TimeBase/Chapters/color_range 等），
//     有了原始数据，将来新增字段可直接在本地回填，不必为补一个字段重新联网探测
//     （远程直链单条要 20-30 秒）。
const (
	mediaInfoName       = "mediainfo.json"
	mediaInfoNameSuffix = ".mediainfo.json"
)

// mediaInfoFile mediainfo.json 的结构：探测元信息 + ffprobe 原始输出。
type mediaInfoFile struct {
	ProbeVersion int             `json:"probe_version"` // 写入时的探测版本，用于版本升级判定
	ProbedAt     string          `json:"probed_at"`     // 探测时间（UTC RFC3339）
	Source       string          `json:"source"`        // 探测用的直链，变化即视为缓存过期
	MediaFile    string          `json:"media_file"`    // 媒体文件名，供人工核对归属
	FFProbe      json.RawMessage `json:"ffprobe"`       // ffprobe 原始输出
}

// mediaInfoPath 返回 .strm 对应的 mediainfo.json 路径。
//
// 同目录只有一个 .strm 时用固定的 mediainfo.json（即期望的库布局：一片一目录）；
// 同目录存在多个 .strm（CD1/CD2 或多片共用目录）时改用 <basename>.mediainfo.json，
// 避免它们互相覆盖——固定的单一文件名在这种情况下会持续互相冲掉、导致每次都重探。
func mediaInfoPath(strmPath string) string {
	dir := filepath.Dir(strmPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return filepath.Join(dir, mediaInfoName)
	}
	strmCount := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
			strmCount++
		}
	}
	if strmCount <= 1 {
		return filepath.Join(dir, mediaInfoName)
	}
	base := strings.TrimSuffix(filepath.Base(strmPath), filepath.Ext(strmPath))
	return filepath.Join(dir, base+mediaInfoNameSuffix)
}

// loadMediaInfo 读取并校验缓存。任一条件不满足即返回 false（调用方会重新探测）：
//   - 文件不存在/读不动 —— 没有缓存
//   - JSON 解析失败或 ffprobe 段为空 —— 缓存损坏
//   - 记录的直链与当前 .strm 指向不一致 —— 源已更换，旧参数不再适用
//
// 不在此处比较 ProbeVersion：版本判定留给调用方，便于区分「跳过」与「可本地升级」。
func loadMediaInfo(strmPath, source string) (mediaInfoFile, bool) {
	raw, err := os.ReadFile(mediaInfoPath(strmPath))
	if err != nil {
		return mediaInfoFile{}, false
	}
	var file mediaInfoFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return mediaInfoFile{}, false
	}
	if len(file.FFProbe) == 0 {
		return mediaInfoFile{}, false
	}
	if file.Source != source {
		return mediaInfoFile{}, false
	}
	return file, true
}

// saveMediaInfo 原子写入缓存（先写临时文件再改名，避免中途失败留半截 JSON）。
// 原始输出做缩进处理，便于人工查看与 diff。
func saveMediaInfo(strmPath, source string, raw []byte) error {
	pretty := raw
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err == nil {
		pretty = buf.Bytes()
	}
	file := mediaInfoFile{
		ProbeVersion: probeVersion,
		ProbedAt:     time.Now().UTC().Format(time.RFC3339),
		Source:       source,
		MediaFile:    mediaBaseName(source),
		FFProbe:      json.RawMessage(pretty),
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := mediaInfoPath(strmPath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(encoded, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mediaBaseName 取直链末尾的文件名，仅用于人工核对缓存归属。
func mediaBaseName(source string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(source), "/")
	if idx := strings.LastIndexAny(trimmed, "/\\"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return trimmed
}

// readMediaInfoFile 读取缓存文件本身，不校验来源。
// 供「按 .strm 路径展示媒体信息」的只读场景使用：那里拿不到待比对的直链，
// 也不该因为源变更就让客户端一片空白（源变更会在下次探测时被 loadMediaInfo 拦下并重探）。
func readMediaInfoFile(strmPath string) (mediaInfoFile, bool) {
	raw, err := os.ReadFile(mediaInfoPath(strmPath))
	if err != nil {
		return mediaInfoFile{}, false
	}
	var file mediaInfoFile
	if err := json.Unmarshal(raw, &file); err != nil || len(file.FFProbe) == 0 {
		return mediaInfoFile{}, false
	}
	return file, true
}

// removeMediaInfo 删除缓存；源文件已消失时清理陈旧缓存用。
func removeMediaInfo(strmPath string) {
	if err := os.Remove(mediaInfoPath(strmPath)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
}

// infoFromMediaInfo 用缓存的原始输出重新解析出探测结果（不联网）。
func infoFromMediaInfo(file mediaInfoFile) (probe.Info, error) {
	return probe.Parse(file.FFProbe)
}
