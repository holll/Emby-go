package nfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleDetails() *StreamDetails {
	return &StreamDetails{
		Video: &VideoStream{
			Codec: "hevc", CodecTag: "hev1", Profile: "Main 10", Level: 120,
			PixelFormat: "yuv420p10le", BitDepth: 10, RefFrames: 1,
			Bitrate: 7559545, Width: 1920, Height: 1080, AspectRatio: "16:9",
			Framerate: 29.97003, Language: "und", ScanType: "progressive",
			DurationMinutes: 134, DurationSeconds: 8069, Default: "True", Forced: "False",
		},
		Audio: &AudioStream{
			Codec: "aac", CodecTag: "mp4a", Profile: "LC", ChannelLayout: "stereo",
			Bitrate: 64000, Language: "und", Channels: 2, SamplingRate: 48000,
			Default: "True", Forced: "False",
		},
	}
}

// 无 <fileinfo> 时应插入到 </movie> 之前，且原有内容（含未建模元素）一字不动。
func TestSaveStreamDetailsInsertsWithoutFileInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.nfo")
	original := "\ufeff<?xml version=\"1.0\" encoding=\"utf-8\" standalone=\"yes\"?>\n" +
		"<movie>\n  <title>T</title>\n  <actor>\n    <name>N</name>\n  </actor>\n  <customtag>x</customtag>\n</movie>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveFileInfo(path, FileInfoMeta{Size: 7700423855, ProbeVersion: 1, ProbeURL: "http://x/a.mp4"}, sampleDetails()); err != nil {
		t.Fatalf("SaveStreamDetails: %v", err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)

	// BOM 与全部原有元素必须保留
	for _, must := range []string{"\ufeff", "<title>T</title>", "<actor>", "<name>N</name>", "<customtag>x</customtag>"} {
		if !strings.Contains(text, must) {
			t.Errorf("原内容丢失: %q", must)
		}
	}
	// 必须插在 </movie> 之前
	if strings.Index(text, "<fileinfo>") > strings.Index(text, "</movie>") {
		t.Error("fileinfo 未插入到 </movie> 之前")
	}
	// 写入后必须能被正常解析回结构体
	meta, err := Read(path)
	if err != nil {
		t.Fatalf("写入后解析失败: %v", err)
	}
	if meta.Title != "T" {
		t.Errorf("title = %q, want T", meta.Title)
	}
	if meta.FileInfo == nil || meta.FileInfo.StreamDetails == nil || meta.FileInfo.StreamDetails.Video == nil {
		t.Fatal("streamdetails 未写回")
	}
	video := meta.FileInfo.StreamDetails.Video
	if video.Codec != "hevc" || video.Width != 1920 || video.Height != 1080 {
		t.Errorf("video = %s %dx%d", video.Codec, video.Width, video.Height)
	}
	if video.Bitrate != 7559545 || video.BitDepth != 10 || video.RefFrames != 1 {
		t.Errorf("video 技术参数丢失: bitrate=%d bitdepth=%d refs=%d", video.Bitrate, video.BitDepth, video.RefFrames)
	}
	if video.Profile != "Main 10" || video.Level != 120 || video.PixelFormat != "yuv420p10le" {
		t.Errorf("video profile/level/pixfmt = %q/%d/%q", video.Profile, video.Level, video.PixelFormat)
	}
	if meta.FileInfo.StreamDetails.Audio == nil || meta.FileInfo.StreamDetails.Audio.ChannelLayout != "stereo" {
		t.Error("audio channellayout 未写回")
	}
	// 片长应能从 streamdetails 读到
	if got := meta.RuntimeSeconds(); got != 8069 {
		t.Errorf("RuntimeSeconds = %d, want 8069", got)
	}
}

// 已有 <fileinfo> 时应整体替换旧块，不残留旧值，也不影响块外内容。
func TestSaveStreamDetailsReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.nfo")
	original := "<movie>\n  <title>T</title>\n" +
		"  <fileinfo>\n    <streamdetails>\n      <video>\n        <codec>h264</codec>\n        <bitrate>999</bitrate>\n      </video>\n    </streamdetails>\n  </fileinfo>\n" +
		"  <tag>keep</tag>\n</movie>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveFileInfo(path, FileInfoMeta{Size: 7700423855, ProbeVersion: 1, ProbeURL: "http://x/a.mp4"}, sampleDetails()); err != nil {
		t.Fatalf("SaveStreamDetails: %v", err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)

	if strings.Contains(text, "<bitrate>999</bitrate>") || strings.Contains(text, "h264</codec>") {
		t.Error("旧 streamdetails 未清除")
	}
	if strings.Count(text, "<fileinfo>") != 1 {
		t.Errorf("fileinfo 块数 = %d, want 1", strings.Count(text, "<fileinfo>"))
	}
	if !strings.Contains(text, "<tag>keep</tag>") {
		t.Error("块外内容丢失")
	}
	meta, err := Read(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if meta.FileInfo.StreamDetails.Video.Width != 1920 {
		t.Errorf("width = %d, want 1920", meta.FileInfo.StreamDetails.Video.Width)
	}
}

// 缺少 </movie> 时报错，且不改动原文件。
func TestSaveStreamDetailsRejectsNonMovie(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.nfo")
	original := "<movie><title>no closing tag</title>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveFileInfo(path, FileInfoMeta{Size: 7700423855, ProbeVersion: 1, ProbeURL: "http://x/a.mp4"}, sampleDetails()); err == nil {
		t.Error("缺少 </movie> 应报错")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != original {
		t.Error("失败时不应改动原文件")
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("失败时不应残留 .tmp 文件")
	}
}

func TestSaveStreamDetailsNilDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.nfo")
	os.WriteFile(path, []byte("<movie></movie>"), 0644)
	if err := SaveFileInfo(path, FileInfoMeta{}, nil); err == nil {
		t.Error("空 streamdetails 应报错")
	}
}

// TestSaveFileInfoSize 覆盖 <fileinfo><size> 的写入与回读：
// 体积是探测产物，NFO 是它的真源，读回后供 MediaSource.Size 使用。
func TestSaveFileInfoSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "size.nfo")
	os.WriteFile(path, []byte("<movie><title>S</title></movie>"), 0644)
	if err := SaveFileInfo(path, FileInfoMeta{Size: 1831088379, ProbeVersion: 1}, sampleDetails()); err != nil {
		t.Fatalf("SaveFileInfo: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "<size>1831088379</size>") {
		t.Errorf("size 未写入:\n%s", raw)
	}
	// size 必须在 <fileinfo> 内、<streamdetails> 之外（Emby/Kodi 的层级约定）
	fileinfo := string(raw)
	start := strings.Index(fileinfo, "<fileinfo>")
	streamStart := strings.Index(fileinfo, "<streamdetails>")
	sizeAt := strings.Index(fileinfo, "<size>")
	if start < 0 || streamStart < 0 || sizeAt < 0 || !(start < sizeAt && sizeAt < streamStart) {
		t.Errorf("size 位置不符（应在 fileinfo 下、streamdetails 之前）:\n%s", raw)
	}
	meta, err := Read(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if meta.FileInfo == nil || meta.FileInfo.Size != 1831088379 {
		t.Errorf("读回 size = %v, want 1831088379", meta.FileInfo)
	}
	// 体积为 0（未探测）时不写该元素，避免伪造
	path2 := filepath.Join(t.TempDir(), "zero.nfo")
	os.WriteFile(path2, []byte("<movie><title>Z</title></movie>"), 0644)
	if err := SaveFileInfo(path2, FileInfoMeta{}, sampleDetails()); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path2)
	if strings.Contains(string(raw2), "<size>") {
		t.Error("size 为 0 时不应写入元素")
	}
}

// TestSaveFileInfoIndent 覆盖缩进：替换已有 <fileinfo> 时不得与其行首缩进叠加。
// 该缺陷会让 <fileinfo> 被顶到 4 空格、内部层级整体错位。
func TestSaveFileInfoIndent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indent.nfo")
	original := "<movie>\n  <title>T</title>\n  <fileinfo>\n    <streamdetails>\n" +
		"      <video>\n        <codec>h264</codec>\n      </video>\n    </streamdetails>\n  </fileinfo>\n</movie>"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveFileInfo(path, FileInfoMeta{Size: 100, ProbeVersion: 1}, sampleDetails()); err != nil {
		t.Fatalf("SaveFileInfo: %v", err)
	}
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, want := range []string{
		"\n  <fileinfo>\n",              // 顶层子元素 → 2 空格
		"\n    <size>100</size>\n",      // fileinfo 的子元素 → 4 空格
		"\n    <streamdetails>\n",       // 同上
		"\n      <video>\n",             // streamdetails 的子元素 → 6 空格
		"\n        <codec>hevc</codec>", // video 的子元素 → 8 空格
	} {
		if !strings.Contains(text, want) {
			t.Errorf("缩进不符，缺少 %q\n实际:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\n    <fileinfo>") {
		t.Errorf("fileinfo 缩进被叠加（出现 4 空格）:\n%s", text)
	}
	// 未探测过的条目（size=0）插入时同样要有正确缩进
	path2 := filepath.Join(t.TempDir(), "insert.nfo")
	os.WriteFile(path2, []byte("<movie>\n  <title>T</title>\n</movie>"), 0644)
	if err := SaveFileInfo(path2, FileInfoMeta{}, sampleDetails()); err != nil {
		t.Fatal(err)
	}
	raw2, _ := os.ReadFile(path2)
	if !strings.Contains(string(raw2), "\n  <fileinfo>\n") {
		t.Errorf("插入时 fileinfo 缩进不符:\n%s", raw2)
	}
}
