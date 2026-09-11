// Package probe 调用系统 ffprobe 读取媒体文件/流的真实技术参数。
//
// 该包只负责「取信息」，不写库也不改 NFO——落盘与索引更新由调用方决定，
// 以便与扫库（scanner）保持独立。
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Info 一次探测的结果，字段已按 NFO streamdetails 的语义归整。
type Info struct {
	DurationSeconds int64
	SizeBytes       int64
	Bitrate         int64 // 容器总码率
	FormatName      string
	Video           *VideoInfo
	Audio           *AudioInfo
	// Subtitles 全部字幕轨（可多条：不同语言/内封外挂），不像音视频轨那样只取首条——
	// 字幕轨的多寡直接决定客户端能否选到想要的语言。
	Subtitles []SubtitleInfo
}

// SubtitleInfo 一条字幕轨。Embedded 区分内封与外挂：
// 外挂字幕的文件不由本服务转发，客户端只能看到「有这条轨」而取不到内容。
type SubtitleInfo struct {
	Index           int
	Codec           string
	CodecTag        string
	Language        string
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool
	Embedded        bool
}

type VideoInfo struct {
	Codec           string
	CodecTag        string
	Profile         string
	Level           int
	PixelFormat     string
	BitDepth        int
	RefFrames       int
	Width           int
	Height          int
	Bitrate         int64
	Framerate       float64
	AspectRatio     string
	Language        string
	ScanType        string
	DurationSeconds int64
	Default         bool
	Forced          bool
	// 色彩特性：判定 SDR/HDR 的准确依据（不能用位深猜——10bit SDR 片源存在）。
	ColorTransfer  string
	ColorPrimaries string
	ColorSpace     string
	ColorRange     string
}

type AudioInfo struct {
	Codec         string
	CodecTag      string
	Profile       string
	Language      string
	ChannelLayout string
	Bitrate       int64
	Channels      int
	SamplingRate  int
	Default       bool
	Forced        bool
}

// ffprobeOutput 是 ffprobe -print_format json 的响应结构（只声明用得到的字段）。
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeFormat struct {
	FormatName string  `json:"format_name"`
	Duration   flexNum `json:"duration"`
	Size       flexNum `json:"size"`
	BitRate    flexNum `json:"bit_rate"`
}

type ffprobeStream struct {
	Index          int               `json:"index"`
	CodecType      string            `json:"codec_type"`
	CodecName      string            `json:"codec_name"`
	CodecTagStr    string            `json:"codec_tag_string"`
	Profile        string            `json:"profile"`
	Level          flexNum           `json:"level"`
	Width          flexNum           `json:"width"`
	Height         flexNum           `json:"height"`
	BitRate        flexNum           `json:"bit_rate"`
	PixFmt         string            `json:"pix_fmt"`
	BitsPerRaw     flexNum           `json:"bits_per_raw_sample"`
	Refs           flexNum           `json:"refs"`
	AvgFrameRate   string            `json:"avg_frame_rate"`
	RFrameRate     string            `json:"r_frame_rate"`
	SampleRate     flexNum           `json:"sample_rate"`
	Channels       flexNum           `json:"channels"`
	ChannelLayout  string            `json:"channel_layout"`
	FieldOrder     string            `json:"field_order"`
	ColorTransfer  string            `json:"color_transfer"`
	ColorPrimaries string            `json:"color_primaries"`
	ColorSpace     string            `json:"color_space"`
	ColorRange     string            `json:"color_range"`
	Disposition    map[string]int    `json:"disposition"`
	Tags           map[string]string `json:"tags"`
}

// flexNum 兼容 ffprobe 的不一致输出：同一字段可能是数字（385576）或字符串（"234616"），
// 也常见 "N/A"。统一按「能转就转，转不了归零」处理，避免因单个字段导致整次解析失败。
type flexNum int64

func (n *flexNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" || s == "N/A" {
		*n = 0
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*n = flexNum(v)
		return nil
	}
	// 极少数情况会是 "1234/1000" 之类的分数，取整数部分。
	if idx := strings.IndexByte(s, '/'); idx > 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s[:idx]), 64); err == nil {
			*n = flexNum(v)
			return nil
		}
	}
	*n = 0
	return nil
}

func (n flexNum) Int() int     { return int(n) }
func (n flexNum) Int64() int64 { return int64(n) }

// LookPath 解析 ffprobe 可执行文件：优先用配置项，未配置则走 PATH。
func LookPath(configured string) (string, error) {
	if path := strings.TrimSpace(configured); path != "" {
		if abs, err := exec.LookPath(path); err == nil {
			return abs, nil
		}
		return "", fmt.Errorf("ffprobe 不可执行: %s", path)
	}
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", errors.New("未找到 ffprobe：请安装 ffmpeg 或设置 ffprobe_path")
	}
	return path, nil
}

// Result 一次探测的结果。
// Raw 保留 ffprobe 的原始 JSON——调用方会把它落盘为 mediainfo.json，作用有二：
// 一是给「是否已探测」提供确定依据，二是完整数据可用于后续新增字段的本地回填，
// 不必为了补一个字段就对远程源重新探测。
type Result struct {
	Info Info
	Raw  []byte
}

// Verify 运行 ffprobe -version 确认它真的能执行。
//
// 探测任务启动前调用：早失败早报错。否则一旦 ffprobe 本身不可用（未安装、无执行
// 权限、架构不匹配、被同名程序占用），会对成千上万条目逐个重试同一个环境问题，
// 既浪费一轮全量扫描的时间，又只留下一堆难以归因的失败记录。
func Verify(ctx context.Context, ffprobePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffprobePath, "-version")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("ffprobe 无法执行（%s）: %s", ffprobePath, firstLine(message))
	}
	if !strings.Contains(stdout.String(), "ffprobe version") {
		return fmt.Errorf("ffprobe 输出异常（%s）: %s", ffprobePath, firstLine(strings.TrimSpace(stdout.String())))
	}
	return nil
}

// Run 探测 target（本地路径或 http(s) 直链）。timeout<=0 时用 90 秒兜底。
// 失败时返回的 error 带 ffprobe 的 stderr 摘要，便于在管理端定位（如链接失效返回 5XX）。
func Run(ctx context.Context, ffprobePath, target string, timeout time.Duration) (Result, error) {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		target,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, fmt.Errorf("探测超时（%s）", timeout)
	}
	if err != nil {
		// 区分「ffprobe 根本没跑起来」与「跑起来了但报错」：两者的排查方向完全不同
		// （前者是环境问题、需整体中止；后者是本条源的问题、跳过继续即可）。
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return Result{}, fmt.Errorf("ffprobe 无法启动（%s）: %s", ffprobePath, firstLine(err.Error()))
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = fmt.Sprintf("ffprobe 退出码 %d（无错误输出）", exitErr.ExitCode())
		}
		return Result{}, errors.New(firstLine(message))
	}
	info, err := Parse(stdout.Bytes())
	if err != nil {
		return Result{}, err
	}
	return Result{Info: info, Raw: stdout.Bytes()}, nil
}

// Parse 解析 ffprobe -print_format json 的输出。
// 独立导出：mediainfo.json 里存的就是原始输出，升级映射时可据此在本地重新解析，
// 无需再对远程源发起一次探测。
func Parse(raw []byte) (Info, error) {
	var output ffprobeOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		return Info{}, fmt.Errorf("解析 ffprobe 输出失败: %w", err)
	}
	if len(output.Streams) == 0 {
		return Info{}, errors.New("ffprobe 未返回任何流")
	}
	info := Info{
		DurationSeconds: int64(math.Round(float64(output.Format.Duration))),
		SizeBytes:       output.Format.Size.Int64(),
		Bitrate:         output.Format.BitRate.Int64(),
		FormatName:      output.Format.FormatName,
	}
	for _, stream := range output.Streams {
		switch stream.CodecType {
		case "video":
			if info.Video != nil {
				continue // 只取首条视频轨（多轨如封面图会排在后面）
			}
			info.Video = videoInfo(stream)
		case "audio":
			if info.Audio != nil {
				continue
			}
			info.Audio = audioInfo(stream)
		case "subtitle":
			info.Subtitles = append(info.Subtitles, subtitleInfo(stream))
		}
	}
	if info.Video == nil && info.Audio == nil {
		return Info{}, errors.New("ffprobe 未返回音视频轨")
	}
	return info, nil
}

// subtitleInfo 归整一条字幕轨。探测目标是单个文件，ffprobe 只会报出容器内的字幕轨，
// 故 Embedded 恒为 true（外挂字幕不在本服务的转发范围内）。
func subtitleInfo(stream ffprobeStream) SubtitleInfo {
	return SubtitleInfo{
		Index:           stream.Index,
		Codec:           stream.CodecName,
		CodecTag:        stream.CodecTagStr,
		Language:        stream.Tags["language"],
		Title:           stream.Tags["title"],
		Default:         stream.Disposition["default"] == 1,
		Forced:          stream.Disposition["forced"] == 1,
		HearingImpaired: stream.Disposition["hearing_impaired"] == 1,
		Embedded:        true,
	}
}

func videoInfo(stream ffprobeStream) *VideoInfo {
	video := &VideoInfo{
		Codec:          stream.CodecName,
		CodecTag:       stream.CodecTagStr,
		Profile:        stream.Profile,
		Level:          stream.Level.Int(),
		PixelFormat:    stream.PixFmt,
		BitDepth:       stream.BitsPerRaw.Int(),
		RefFrames:      stream.Refs.Int(),
		Width:          stream.Width.Int(),
		Height:         stream.Height.Int(),
		Bitrate:        stream.BitRate.Int64(),
		Framerate:      parseFrameRate(stream.AvgFrameRate, stream.RFrameRate),
		Language:       stream.Tags["language"],
		ScanType:       scanType(stream.FieldOrder),
		Default:        stream.Disposition["default"] == 1,
		Forced:         stream.Disposition["forced"] == 1,
		ColorTransfer:  stream.ColorTransfer,
		ColorPrimaries: stream.ColorPrimaries,
		ColorSpace:     stream.ColorSpace,
		ColorRange:     stream.ColorRange,
	}
	video.AspectRatio = aspectRatio(video.Width, video.Height)
	// 部分容器不报 bits_per_raw_sample，从像素格式兜底（yuv420p10le → 10）。
	if video.BitDepth == 0 {
		video.BitDepth = bitDepthFromPixelFormat(stream.PixFmt)
	}
	return video
}

func audioInfo(stream ffprobeStream) *AudioInfo {
	return &AudioInfo{
		Codec:         stream.CodecName,
		CodecTag:      stream.CodecTagStr,
		Profile:       stream.Profile,
		Language:      stream.Tags["language"],
		ChannelLayout: stream.ChannelLayout,
		Bitrate:       stream.BitRate.Int64(),
		Channels:      stream.Channels.Int(),
		SamplingRate:  stream.SampleRate.Int(),
		Default:       stream.Disposition["default"] == 1,
		Forced:        stream.Disposition["forced"] == 1,
	}
}

// parseFrameRate 优先用 avg_frame_rate（真实平均帧率），为空/0 时回退 r_frame_rate。
// ffprobe 以 "30000/1001" 形式返回，需做分数还原。
func parseFrameRate(values ...string) float64 {
	for _, value := range values {
		if rate := fraction(value); rate > 0 {
			return rate
		}
	}
	return 0
}

func fraction(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" || raw == "0/0" {
		return 0
	}
	if idx := strings.IndexByte(raw, '/'); idx > 0 {
		num, errNum := strconv.ParseFloat(strings.TrimSpace(raw[:idx]), 64)
		den, errDen := strconv.ParseFloat(strings.TrimSpace(raw[idx+1:]), 64)
		if errNum == nil && errDen == nil && den != 0 {
			return num / den
		}
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

// scanType 归一为 NFO/Emby 使用的 progressive / interlaced。
func scanType(fieldOrder string) string {
	switch strings.ToLower(strings.TrimSpace(fieldOrder)) {
	case "progressive":
		return "progressive"
	case "tt", "bb", "tb", "bt":
		return "interlaced"
	}
	return ""
}

// aspectRatio 把像素宽高比约分为 "16:9" 形式；非法尺寸返回空串。
func aspectRatio(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	divisor := gcd(width, height)
	return strconv.Itoa(width/divisor) + ":" + strconv.Itoa(height/divisor)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// bitDepthFromPixelFormat 从像素格式尾部推断位深，如 yuv420p10le → 10、yuv420p → 8。
func bitDepthFromPixelFormat(pixelFormat string) int {
	value := strings.ToLower(pixelFormat)
	value = strings.TrimSuffix(value, "le")
	value = strings.TrimSuffix(value, "be")
	digits := ""
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] < '0' || value[i] > '9' {
			break
		}
		digits = string(value[i]) + digits
	}
	if digits == "" {
		return 8 // yuv420p 之类的默认 8 位
	}
	depth, err := strconv.Atoi(digits)
	if err != nil || depth <= 0 {
		return 0
	}
	return depth
}

func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	return strings.TrimSpace(text)
}
