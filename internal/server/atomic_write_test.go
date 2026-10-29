package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWriteFileAtomicReaderSeesOldOrNew 卡在「写了一半」和「写完待改名」两个窗口，
// 断言读者在任何时刻看到的都只能是旧的完整内容或新的完整内容，绝不会是半截文件。
// 这正是固定 ".tmp" 名出问题的场景：临时文件和目标文件同名共用，写入者互相截断。
func TestWriteFileAtomicReaderSeesOldOrNew(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "cover.webp")
	oldContent := []byte("old-cover-content")
	if err := os.WriteFile(dest, oldContent, 0o644); err != nil {
		t.Fatalf("准备旧文件: %v", err)
	}
	newContent := []byte("new-cover-content-much-longer-than-old")

	writeHalf := make(chan struct{})
	halfWritten := make(chan struct{})
	renamed := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := writeFileAtomic(dest, func(tmpPath string) error {
			file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err = file.Write(newContent[:5]); err != nil {
				file.Close()
				return err
			}
			close(halfWritten)
			<-writeHalf
			if _, err = file.Write(newContent[5:]); err != nil {
				file.Close()
				return err
			}
			return file.Close()
		}, func(tmpPath, destination string) {
			// 写完、改名前：此刻目标文件必须仍是旧内容（改名是原子的）。
			data, err := os.ReadFile(destination)
			if err != nil {
				t.Errorf("改名窗口读取目标文件: %v", err)
				return
			}
			if string(data) != string(oldContent) {
				t.Errorf("改名窗口目标文件内容 = %q，应为旧内容 %q", data, oldContent)
			}
			close(renamed)
		})
		if err != nil {
			t.Errorf("writeFileAtomic: %v", err)
		}
	}()

	<-halfWritten
	// 写了一半：此时临时文件只有 5 字节，但目标文件必须完好。
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("半写窗口读取目标文件: %v", err)
	}
	if string(data) != string(oldContent) {
		t.Errorf("半写窗口目标文件内容 = %q，应为旧内容 %q（说明写入者直接写到了目标文件）", data, oldContent)
	}
	close(writeHalf)
	<-renamed
	wg.Wait()

	data, err = os.ReadFile(dest)
	if err != nil {
		t.Fatalf("改名后读取目标文件: %v", err)
	}
	if string(data) != string(newContent) {
		t.Errorf("改名后内容 = %q，应为 %q", data, newContent)
	}
	assertNoTempLeftovers(t, dir)
}

// TestWriteFileAtomicKeepsPermissions 沿用改造前「os.Create 后改名」的 0644。
// os.CreateTemp 建的是 0600，若忘了 chmod，改名后封面/缓存的权限会悄悄收紧。
func TestWriteFileAtomicKeepsPermissions(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "poster.webp")
	if err := writeFileAtomic(dest, func(tmpPath string) error {
		return os.WriteFile(tmpPath, []byte("webp"), 0o600)
	}, nil); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("权限 = %o，应为 644", mode)
	}
}

// TestWriteFileAtomicCleansTempOnError 写入或改名失败时不能留下临时文件。
func TestWriteFileAtomicCleansTempOnError(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "poster.webp")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatalf("准备旧文件: %v", err)
	}
	wantErr := fmt.Errorf("注入的写入失败")
	err := writeFileAtomic(dest, func(tmpPath string) error {
		if err := os.WriteFile(tmpPath, []byte("half"), 0o644); err != nil {
			return err
		}
		return wantErr
	}, nil)
	if err == nil {
		t.Fatal("write 返回错误时 writeFileAtomic 必须报错")
	}
	data, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("读取目标文件: %v", readErr)
	}
	if string(data) != "old" {
		t.Errorf("失败写入改动了目标文件：%q", data)
	}
	assertNoTempLeftovers(t, dir)
}

// TestSaveMediaInfoConcurrentSameTarget 复现固定 ".tmp" 名的截断场景：多个写入者
// 同时写同一个 .strm 的 mediainfo 缓存，读者必须始终能读到完整 JSON。
func TestSaveMediaInfoConcurrentSameTarget(t *testing.T) {
	dir := t.TempDir()
	strm := filepath.Join(dir, "movie.strm")
	if err := os.WriteFile(strm, []byte("http://example.invalid/movie.mp4\n"), 0o644); err != nil {
		t.Fatalf("准备 .strm: %v", err)
	}
	path := mediaInfoPath(strm)

	// 先写一份完整的旧缓存，保证读者一开始就有东西可读。
	if err := saveMediaInfo(strm, "http://example.invalid/movie.mp4", probeFixtureJSON("old")); err != nil {
		t.Fatalf("初始 saveMediaInfo: %v", err)
	}

	const writers, rounds = 6, 12
	var stop atomic.Bool
	var bad atomic.Int64

	var readerWg sync.WaitGroup
	readerWg.Add(1)
	go func() {
		defer readerWg.Done()
		for !stop.Load() {
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					bad.Add(1)
					t.Errorf("读者看到缓存文件消失")
					return
				}
				continue
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				bad.Add(1)
				t.Errorf("读者读到半截 JSON（%d 字节）: %v\n%s", len(data), err, data)
				return
			}
			if raw, ok := doc["ffprobe"]; !ok || raw == nil {
				bad.Add(1)
				t.Errorf("读者读到缺 ffprobe 段的缓存: %s", data)
				return
			}
		}
	}()

	var writerWg sync.WaitGroup
	for i := 0; i < writers; i++ {
		writerWg.Add(1)
		go func(id int) {
			defer writerWg.Done()
			for r := 0; r < rounds; r++ {
				tag := fmt.Sprintf("w%d-r%d", id, r)
				if err := saveMediaInfo(strm, "http://example.invalid/movie.mp4", probeFixtureJSON(tag)); err != nil {
					t.Errorf("并发 saveMediaInfo: %v", err)
					return
				}
			}
		}(i)
	}
	writerWg.Wait()
	time.Sleep(10 * time.Millisecond)
	stop.Store(true)
	readerWg.Wait()

	if bad.Load() != 0 {
		t.Fatalf("读者观察到 %d 次损坏读取", bad.Load())
	}
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取最终缓存: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(final, &doc); err != nil {
		t.Fatalf("最终缓存不是完整 JSON: %v\n%s", err, final)
	}
	assertNoTempLeftovers(t, dir)
}

// TestSaveMediaInfoKeepsEveryField 走一遍公开入口，确认改写路径没丢字段。
func TestSaveMediaInfoKeepsEveryField(t *testing.T) {
	dir := t.TempDir()
	strm := filepath.Join(dir, "clip.strm")
	if err := os.WriteFile(strm, []byte("http://example.invalid/clip.mp4\n"), 0o644); err != nil {
		t.Fatalf("准备 .strm: %v", err)
	}
	source := "http://example.invalid/clip.mp4"
	if err := saveMediaInfo(strm, source, probeFixtureJSON("keep")); err != nil {
		t.Fatalf("saveMediaInfo: %v", err)
	}
	got, ok := loadMediaInfo(strm, source)
	if !ok {
		t.Fatal("刚写入的缓存应能被 loadMediaInfo 接受")
	}
	if got.Source != source || got.ProbeVersion != probeVersion || got.MediaFile != "clip.mp4" {
		t.Errorf("元数据不符：source=%q version=%d media=%q", got.Source, got.ProbeVersion, got.MediaFile)
	}
	if len(got.FFProbe) == 0 {
		t.Error("ffprobe 段为空")
	}
}

func assertNoTempLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", entry.Name())
		}
	}
}

// probeFixtureJSON 造一份结构合法的 ffprobe 输出，tag 写进 format.filename 便于区分写入者。
func probeFixtureJSON(tag string) []byte {
	return []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],"format":{"filename":"` + tag + `","duration":"120.000000"}}`)
}
