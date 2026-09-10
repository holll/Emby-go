package probe

import (
	"context"
	"testing"
	"time"
)

// realSample 截取自真实 115 直链（HEVC Main 10，10bit）的 ffprobe 输出。
// 保留关键特征：bit_rate 为字符串、bits_per_raw_sample 缺失（需由 pix_fmt 兜底）、
// avg_frame_rate 为分数、disposition 用于判定默认轨。
const realSample = `{
  "streams": [
    {
      "index": 0,
      "codec_name": "hevc",
      "codec_tag_string": "hev1",
      "codec_type": "video",
      "width": 1920,
      "height": 1080,
      "pix_fmt": "yuv420p10le",
      "level": 120,
      "profile": "Main 10",
      "refs": 1,
      "avg_frame_rate": "30000/1001",
      "r_frame_rate": "30000/1001",
      "field_order": "progressive",
      "bit_rate": "7559545",
      "color_transfer": "smpte2084",
      "color_primaries": "bt2020",
      "color_space": "bt2020nc",
      "color_range": "tv",
      "disposition": {"default": 1, "forced": 0},
      "tags": {"language": "und"}
    },
    {
      "index": 1,
      "codec_name": "aac",
      "codec_tag_string": "mp4a",
      "codec_type": "audio",
      "profile": "LC",
      "sample_rate": "48000",
      "channels": 2,
      "channel_layout": "stereo",
      "bit_rate": "64000",
      "disposition": {"default": 1, "forced": 0},
      "tags": {"language": "und"}
    }
  ],
  "format": {
    "format_name": "mov,mp4,m4a,3gp,3g2,mj2",
    "duration": "8069.375833",
    "size": "7700423855",
    "bit_rate": "7634220"
  }
}`

func TestParseRealSample(t *testing.T) {
	info, err := Parse([]byte(realSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.DurationSeconds != 8069 {
		t.Errorf("duration = %d, want 8069", info.DurationSeconds)
	}
	if info.SizeBytes != 7700423855 {
		t.Errorf("size = %d, want 7700423855", info.SizeBytes)
	}
	if info.Bitrate != 7634220 {
		t.Errorf("bitrate = %d, want 7634220", info.Bitrate)
	}
	video := info.Video
	if video == nil {
		t.Fatal("video 轨缺失")
	}
	if video.Codec != "hevc" || video.CodecTag != "hev1" {
		t.Errorf("codec = %q/%q, want hevc/hev1", video.Codec, video.CodecTag)
	}
	if video.Profile != "Main 10" || video.Level != 120 {
		t.Errorf("profile/level = %q/%d, want Main 10/120", video.Profile, video.Level)
	}
	if video.Width != 1920 || video.Height != 1080 {
		t.Errorf("size = %dx%d, want 1920x1080", video.Width, video.Height)
	}
	if video.Bitrate != 7559545 {
		t.Errorf("video bitrate = %d, want 7559545", video.Bitrate)
	}
	// bits_per_raw_sample 缺失，须由 yuv420p10le 推断出 10bit。
	if video.BitDepth != 10 {
		t.Errorf("bit depth = %d, want 10 (由 pix_fmt 推断)", video.BitDepth)
	}
	if video.PixelFormat != "yuv420p10le" {
		t.Errorf("pixel format = %q", video.PixelFormat)
	}
	if video.AspectRatio != "16:9" {
		t.Errorf("aspect = %q, want 16:9", video.AspectRatio)
	}
	if video.ScanType != "progressive" {
		t.Errorf("scan type = %q, want progressive", video.ScanType)
	}
	// 30000/1001 ≈ 29.97
	if video.Framerate < 29.96 || video.Framerate > 29.98 {
		t.Errorf("framerate = %v, want ≈29.97", video.Framerate)
	}
	if !video.Default || video.Forced {
		t.Errorf("default/forced = %v/%v, want true/false", video.Default, video.Forced)
	}
	if video.Language != "und" {
		t.Errorf("language = %q, want und", video.Language)
	}
	// 色彩特性是判定 HDR/SDR 的依据，必须完整取出。
	if video.ColorTransfer != "smpte2084" || video.ColorPrimaries != "bt2020" {
		t.Errorf("色彩特性 = %q/%q, want smpte2084/bt2020", video.ColorTransfer, video.ColorPrimaries)
	}
	if video.ColorSpace != "bt2020nc" || video.ColorRange != "tv" {
		t.Errorf("色彩空间/范围 = %q/%q", video.ColorSpace, video.ColorRange)
	}

	audio := info.Audio
	if audio == nil {
		t.Fatal("audio 轨缺失")
	}
	if audio.Codec != "aac" || audio.Profile != "LC" {
		t.Errorf("audio codec/profile = %q/%q", audio.Codec, audio.Profile)
	}
	if audio.Channels != 2 || audio.SamplingRate != 48000 || audio.Bitrate != 64000 {
		t.Errorf("audio ch/sr/bitrate = %d/%d/%d", audio.Channels, audio.SamplingRate, audio.Bitrate)
	}
	if audio.ChannelLayout != "stereo" {
		t.Errorf("channel layout = %q, want stereo", audio.ChannelLayout)
	}
}

// 数字型 bit_rate（本地文件探测常见）同样要能解析。
func TestParseNumericBitrate(t *testing.T) {
	sample := `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"bit_rate":234616}],
	 "format":{"duration":1,"size":48197,"bit_rate":385576}}`
	info, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Video.Bitrate != 234616 || info.Bitrate != 385576 {
		t.Errorf("bitrate = %d/%d", info.Video.Bitrate, info.Bitrate)
	}
	if info.Video.BitDepth != 8 {
		t.Errorf("默认位深 = %d, want 8", info.Video.BitDepth)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("非法 JSON 应报错")
	}
	if _, err := Parse([]byte(`{"streams":[],"format":{}}`)); err == nil {
		t.Error("无流应报错")
	}
	// 只有字幕轨时也应视为失败，避免落一份无意义的 streamdetails。
	if _, err := Parse([]byte(`{"streams":[{"index":0,"codec_type":"subtitle","codec_name":"subrip"}]}`)); err == nil {
		t.Error("无音视频轨应报错")
	}
}

func TestBitDepthFromPixelFormat(t *testing.T) {
	cases := map[string]int{
		"yuv420p":     8,
		"yuv420p10le": 10,
		"yuv422p12be": 12,
		"yuv444p16le": 16,
		"":            8,
		"gray":        8,
	}
	for format, want := range cases {
		if got := bitDepthFromPixelFormat(format); got != want {
			t.Errorf("bitDepthFromPixelFormat(%q) = %d, want %d", format, got, want)
		}
	}
}

func TestAspectRatio(t *testing.T) {
	cases := []struct {
		width, height int
		want          string
	}{
		{1920, 1080, "16:9"},
		{1280, 720, "16:9"},
		{720, 480, "3:2"},
		{0, 1080, ""},
	}
	for _, item := range cases {
		if got := aspectRatio(item.width, item.height); got != item.want {
			t.Errorf("aspectRatio(%d,%d) = %q, want %q", item.width, item.height, got, item.want)
		}
	}
}

func TestLookPath(t *testing.T) {
	if _, err := LookPath("definitely-not-an-ffprobe-binary"); err == nil {
		t.Error("不存在的路径应报错")
	}
	// 真实环境有 ffprobe 时验证解析；无则跳过（CI 不应因缺 ffprobe 失败）。
	path, err := LookPath("")
	if err != nil {
		t.Skipf("环境无 ffprobe: %v", err)
	}
	if path == "" {
		t.Error("ffprobe 路径为空")
	}
}

// Run 的失败路径：不存在的二进制应即刻报错而不是挂起。
func TestRunMissingBinary(t *testing.T) {
	_, err := Run(context.Background(), "definitely-not-an-ffprobe-binary", "whatever.mp4", time.Second)
	if err == nil {
		t.Error("不存在的 ffprobe 应报错")
	}
}
