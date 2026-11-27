package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"emby-go/internal/probe"
	"emby-go/internal/regularfile"
	"emby-go/internal/scanner"
)

// mediainfo.json：.strm 同目录下的探测缓存，保存 ffprobe 的技术数据。
//
// 为什么要这个文件（而不是只看 NFO）：
//  1. 「是否已探测」需要一个确定判据。看 NFO 有没有 <streamdetails> 不可靠——
//     刮削器也会写 streamdetails（且常不完整：缺位深/色彩特性/体积），
//     按有无判定会把该补齐的条目永久跳过。
//  2. 除了输入地址，保留原始输出。NFO 只承载映射后的子集（丢掉 TimeBase/Chapters/color_range 等），
//     有了原始数据，将来新增字段可直接在本地回填，不必为补一个字段重新联网探测
//     （远程直链单条要 20-30 秒）。
const (
	mediaInfoName       = "mediainfo.json"
	mediaInfoNameSuffix = ".mediainfo.json"
	maxMediaInfoSize    = 8 << 20 // 4 MiB ffprobe stdout plus room for the legacy URL and envelope.
)

// mediaInfoFile mediainfo.json 的结构：探测元信息 + ffprobe 原始输出。
type mediaInfoFile struct {
	ProbeVersion int             `json:"probe_version"`         // 写入时的探测版本，用于版本升级判定
	ProbedAt     string          `json:"probed_at"`             // 探测时间（UTC RFC3339）
	Source       string          `json:"source,omitempty"`      // 旧缓存按直链匹配
	SourceHash   string          `json:"source_hash,omitempty"` // STRM 原始字节的 SHA-256
	MediaFile    string          `json:"media_file"`            // 媒体文件名，供人工核对归属
	FFProbe      json.RawMessage `json:"ffprobe"`               // ffprobe 原始输出
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
//   - 文件不存在、不是普通文件、超限或读不动 —— 没有缓存
//   - JSON 解析失败或 ffprobe 段为空 —— 缓存损坏
//   - STRM 哈希不匹配（旧缓存按直链比较）—— 不复用旧参数
//
// 不在此处比较 ProbeVersion：版本判定留给调用方，便于区分「跳过」与「可本地升级」。
func loadMediaInfo(strmPath, source, sourceHash string) (mediaInfoFile, bool) {
	sidecar, err := regularfile.Open(mediaInfoPath(strmPath))
	if err != nil {
		return mediaInfoFile{}, false
	}
	defer sidecar.Close()
	raw, err := io.ReadAll(io.LimitReader(sidecar, maxMediaInfoSize+1))
	if err != nil || len(raw) > maxMediaInfoSize {
		return mediaInfoFile{}, false
	}
	var file mediaInfoFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return mediaInfoFile{}, false
	}
	if len(file.FFProbe) == 0 {
		return mediaInfoFile{}, false
	}
	if file.SourceHash != "" && file.SourceHash != sourceHash || file.SourceHash == "" && (file.Source == "" || file.Source != source) {
		return mediaInfoFile{}, false
	}
	return file, true
}

// saveMediaInfo 原子写入缓存（先写临时文件再改名，避免中途失败留半截 JSON）。
// 使用紧凑 JSON，减少缓存文件的空白字符与体积。
func saveMediaInfo(strmPath, source, sourceHash string, raw []byte) error {
	file := mediaInfoFile{
		ProbeVersion: probeVersion,
		ProbedAt:     time.Now().UTC().Format(time.RFC3339),
		SourceHash:   sourceHash,
		MediaFile:    mediaBaseName(source),
		FFProbe:      json.RawMessage(raw),
	}
	if sourceHash == "" {
		file.Source = source
	} else {
		// ffprobe echoes its input URL in format.filename; remove it but preserve the technical fields.
		var output map[string]json.RawMessage
		if err := json.Unmarshal(raw, &output); err != nil {
			return err
		}
		if formatJSON := output["format"]; len(formatJSON) != 0 {
			var format map[string]json.RawMessage
			if err := json.Unmarshal(formatJSON, &format); err != nil {
				return err
			}
			if _, ok := format["filename"]; ok {
				delete(format, "filename")
				output["format"], _ = json.Marshal(format)
				file.FFProbe, _ = json.Marshal(output)
			}
		}
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return err
	}
	path := mediaInfoPath(strmPath)
	// 临时名必须唯一：固定 path+".tmp" 时两次刮削（或刮削与清理）会写同一个
	// 临时文件，后到者截断前者，改名后留下半截 JSON。
	return writeFileAtomic(path, func(tmpPath string) error {
		return os.WriteFile(tmpPath, append(encoded, '\n'), 0o644)
	}, nil)
}

// mediaBaseName 取直链末尾的文件名，仅用于人工核对缓存归属。
func mediaBaseName(source string) string {
	parsed, err := url.Parse(source)
	if err != nil || strings.Trim(parsed.EscapedPath(), "/") == "" {
		return ""
	}
	return path.Base(parsed.EscapedPath())
}

// readMediaInfoFile validates the cache against the current STRM before display.
func readMediaInfoFile(strmPath string) (mediaInfoFile, bool) {
	url, hash, err := scanner.ReadSourceWithHash(strmPath)
	if err != nil {
		return mediaInfoFile{}, false
	}
	return loadMediaInfo(strmPath, url, hash)
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
