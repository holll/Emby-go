package logging

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// R4：受限的磁盘运行日志读取。
//
// 只读固定 kind=app 与服务端按日期生成的 app-YYYY-MM-DD.log：
// 不接收客户端路径、拒绝非常规文件/越界符号链接，游标带 HMAC，
// 解码后仍会重新校验日期、offset 与行边界，绝不信任客户端给的定位信息。

// LogKindApp 是当前唯一开放的日志来源；request 日志可能含原始 debug 内容，不暴露。
const LogKindApp = "app"

const (
	diskLogScanBytes  = 1 << 20 // 每请求最多扫描 1 MiB
	diskLogMaxLine    = 64 << 10
	diskLogMaxPayload = 512 << 10
	diskLogDefaultLim = 200
	diskLogMaxLim     = 500
	diskLogChunk      = 64 << 10
	diskLogLayout     = "2006-01-02"
)

var (
	// ErrLogsUnavailable 表示日志目录/文件不可读，调用方应返回稳定 503。
	ErrLogsUnavailable = errors.New("日志目录不可读")
	// ErrLogsCursorInvalid 表示游标伪造、过长或绑定信息不成立，调用方应返回 400。
	ErrLogsCursorInvalid = errors.New("日志游标无效")
)

// DiskLogItem 是一条可安全展示的日志；Message 已经过写入侧同一套脱敏，
// 无法确认安全的旧格式行只显示固定占位文本。
type DiskLogItem struct {
	ID      string `json:"id"`
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// DiskLogPage 是受限读取结果；Items 始终按时间正序（最旧在前）。
type DiskLogPage struct {
	Items      []DiskLogItem
	NextAfter  string
	NextBefore string
	HasMore    bool
	Reset      bool
	Gap        bool
}

// DiskLogQuery 是一次读取请求；After 与 Before 互斥，Limit 已在调用方校验。
type DiskLogQuery struct {
	After  string
	Before string
	Limit  int
}

type diskCursor struct {
	Day    string
	Offset int64
}

type lineRef struct {
	Day   string
	Start int64
	End   int64
}

// cursorKey 每进程随机生成：游标不可跨重启复用，伪造/旧游标会被拒绝而不是被信任。
var cursorKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return []byte("emby-go-disk-log-cursor")
	}
	return key
}()

var (
	overrideDirMu sync.RWMutex
	overrideDir   string
)

// SetAppLogDir 固定日志读取根目录（仅供测试/嵌入式场景）；传空串恢复默认。
func SetAppLogDir(dir string) {
	overrideDirMu.Lock()
	defer overrideDirMu.Unlock()
	if dir == "" {
		overrideDir = ""
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	overrideDir = abs
}

// AppLogDir 返回当前 app 日志目录的绝对路径；未 Setup 时固定为 cwd/log。
func AppLogDir() string {
	overrideDirMu.RLock()
	override := overrideDir
	overrideDirMu.RUnlock()
	if override != "" {
		return override
	}
	currentMu.RLock()
	defer currentMu.RUnlock()
	if current != nil && current.app != nil {
		return current.app.dir
	}
	abs, err := filepath.Abs(Dir)
	if err != nil {
		return Dir
	}
	return abs
}

// ReadDiskLogs 读取 app 日志；无文件返回空页，目录不可读返回 ErrLogsUnavailable。
func ReadDiskLogs(q DiskLogQuery) (DiskLogPage, error) {
	return readDiskLogsAt(AppLogDir(), q)
}

func readDiskLogsAt(dir string, q DiskLogQuery) (DiskLogPage, error) {
	if q.Limit <= 0 {
		q.Limit = diskLogDefaultLim
	}
	if q.Limit > diskLogMaxLim {
		return DiskLogPage{}, ErrLogsCursorInvalid
	}
	if q.After != "" && q.Before != "" {
		return DiskLogPage{}, ErrLogsCursorInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return DiskLogPage{Items: []DiskLogItem{}}, nil
		}
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	defer root.Close()

	files, err := appLogFiles(root, dir)
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	if len(files) == 0 {
		return DiskLogPage{Items: []DiskLogItem{}}, nil
	}
	if q.After != "" {
		cur, err := decodeCursor(q.After)
		if err != nil {
			return DiskLogPage{}, err
		}
		return readAfter(root, files, cur, q.Limit)
	}
	if q.Before != "" {
		cur, err := decodeCursor(q.Before)
		if err != nil {
			return DiskLogPage{}, err
		}
		return readBefore(root, files, cur, q.Limit)
	}
	return readTail(root, files, q.Limit)
}

// appLogFiles 只接受 app-YYYY-MM-DD.log，按日期升序返回（旧→新）。
func appLogFiles(root *os.Root, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "app-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "app-"), ".log")
		if _, err := time.Parse(diskLogLayout, day); err != nil {
			continue
		}
		// 目录项可能是符号链接：必须以只读方式打开并确认为常规文件。
		f, err := root.Open(name)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		_ = f.Close()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func openRegular(root *os.Root, name string) (*os.File, int64, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("非常规日志文件")
	}
	return f, info.Size(), nil
}

func dayOf(name string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "app-"), ".log")
}

// readTail 从最新文件尾部逆向取完整行；最新在前端展示由 UI 反转。
func readTail(root *os.Root, files []string, limit int) (DiskLogPage, error) {
	newest := files[len(files)-1]
	f, size, err := openRegular(root, newest)
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	defer f.Close()
	refs, endOffset, _, err := tailRefs(f, dayOf(newest), size, limit)
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	items, err := readItems(root, refs)
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	page := DiskLogPage{Items: items}
	if len(refs) > 0 {
		page.NextBefore = encodeCursor(diskCursor{Day: refs[0].Day, Offset: refs[0].Start})
	}
	page.NextAfter = encodeCursor(diskCursor{Day: dayOf(newest), Offset: endOffset})
	page.HasMore = moreEarlier(files, refs)
	return page, nil
}

func readBefore(root *os.Root, files []string, cur diskCursor, limit int) (DiskLogPage, error) {
	idx := fileIndex(files, cur.Day)
	if idx < 0 {
		return resetTail(root, files, limit)
	}
	f, size, err := openRegular(root, files[idx])
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	defer f.Close()
	if cur.Offset < 0 || cur.Offset > size {
		return resetTail(root, files, limit)
	}
	// before 的 offset 是「已加载的最早一条」的起点，允许为 0（继续往前翻文件）。
	if !atBoundary(f, cur.Offset) {
		return resetTail(root, files, limit)
	}
	refs, err := gatherBefore(root, files, idx, cur.Offset, limit)
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	items, err := readItems(root, refs)
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	page := DiskLogPage{Items: items, NextAfter: encodeCursor(cur)}
	if len(refs) > 0 {
		page.NextBefore = encodeCursor(diskCursor{Day: refs[0].Day, Offset: refs[0].Start})
	}
	page.HasMore = moreEarlier(files, refs)
	return page, nil
}

func gatherBefore(root *os.Root, files []string, idx int, offset int64, limit int) ([]lineRef, error) {
	var collected []lineRef
	curIdx, end := idx, offset
	for curIdx >= 0 && len(collected) < limit {
		f, size, err := openRegular(root, files[curIdx])
		if err != nil {
			return nil, err
		}
		if end > size {
			end = size
		}
		refs, _, _, err := tailRefs(f, dayOf(files[curIdx]), end, limit-len(collected))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		collected = append(refs, collected...)
		if len(refs) >= limit || len(collected) >= limit {
			break
		}
		curIdx--
		if curIdx >= 0 {
			if _, sz, err := openRegular(root, files[curIdx]); err == nil {
				end = sz
			}
		}
	}
	if len(collected) > limit {
		collected = collected[len(collected)-limit:]
	}
	return collected, nil
}

func readAfter(root *os.Root, files []string, cur diskCursor, limit int) (DiskLogPage, error) {
	idx := fileIndex(files, cur.Day)
	if idx < 0 {
		return resetTail(root, files, limit)
	}
	f, size, err := openRegular(root, files[idx])
	if err != nil {
		return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
	}
	if cur.Offset < 0 || cur.Offset > size || !atBoundary(f, cur.Offset) {
		_ = f.Close()
		return resetTail(root, files, limit)
	}
	_ = f.Close()

	items := make([]DiskLogItem, 0, limit)
	scanned := 0
	itemBytes := 0
	last := cur
	hasMore := false
	for ; idx < len(files) && len(items) < limit; idx++ {
		name := files[idx]
		day := dayOf(name)
		f, size, err := openRegular(root, name)
		if err != nil {
			return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
		}
		start := int64(0)
		if day == cur.Day {
			start = cur.Offset
		}
		for start < size && len(items) < limit {
			remain := int64(diskLogScanBytes - scanned)
			if remain <= 0 {
				hasMore = true
				break
			}
			n := int64(diskLogChunk)
			if n > size-start {
				n = size - start
			}
			if n > remain {
				n = int64(remain)
			}
			buf := make([]byte, n)
			if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
				_ = f.Close()
				return DiskLogPage{}, fmt.Errorf("%w: %v", ErrLogsUnavailable, err)
			}
			scanned += len(buf)
			consumed := 0
			pos := 0
			for pos < len(buf) && len(items) < limit {
				nl := indexByte(buf[pos:], '\n')
				if nl < 0 {
					break
				}
				lineStart := start + int64(pos)
				line := buf[pos : pos+nl]
				ref := lineRef{Day: day, Start: lineStart, End: lineStart + int64(nl)}
				item := parseLogLine(ref, line)
				if itemBytes+len(item.ID)+len(item.Time)+len(item.Level)+len(item.Message) > diskLogMaxPayload {
					hasMore = true
					break
				}
				items = append(items, item)
				itemBytes += len(item.ID) + len(item.Time) + len(item.Level) + len(item.Message)
				pos += nl + 1
				consumed = pos
			}
			start += int64(consumed)
			last = diskCursor{Day: day, Offset: start}
			if pos >= len(buf) || len(items) >= limit {
				if start < size {
					hasMore = true
				}
				break
			}
			if consumed == 0 {
				// 缓冲区里没有完整行：把当前 offset 停在半行开头，等下次写入后再读。
				hasMore = true
				break
			}
		}
		_ = f.Close()
		if len(items) >= limit || scanned >= diskLogScanBytes {
			hasMore = true
		}
		if idx+1 < len(files) && len(items) < limit && scanned < diskLogScanBytes {
			hasMore = true
		}
	}
	if last.Day == "" {
		last = cur
	}
	return DiskLogPage{Items: items, NextAfter: encodeCursor(last), HasMore: hasMore}, nil
}

// resetTail 在游标失效（文件被清理/截断/替换）时返回当前 tail，并显式标记缺口。
func resetTail(root *os.Root, files []string, limit int) (DiskLogPage, error) {
	page, err := readTail(root, files, limit)
	if err != nil {
		return page, err
	}
	page.Reset = true
	page.Gap = true
	return page, nil
}

func fileIndex(files []string, day string) int {
	for i, name := range files {
		if dayOf(name) == day {
			return i
		}
	}
	return -1
}

// atBoundary 校验 offset 是一条完整行的起点：0，或前一个字节是换行。
func atBoundary(f *os.File, offset int64) bool {
	if offset <= 0 {
		return offset == 0
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], offset-1); err != nil {
		return false
	}
	return b[0] == '\n'
}

// moreEarlier 判断是否还有更早的行（当前 chunk 之前，或更早的日期文件）。
func moreEarlier(files []string, refs []lineRef) bool {
	if len(refs) == 0 {
		return false
	}
	if refs[0].Start > 0 {
		return true
	}
	idx := fileIndex(files, refs[0].Day)
	return idx > 0
}

// tailRefs 读取 [.., end) 尾部的完整行（最多 1 MiB），返回 refs、完整行结束 offset。
// 未写完的半行不返回，completeEnd 停在最后一条完整行之后，等下次写入再读。
func tailRefs(f *os.File, day string, end int64, limit int) ([]lineRef, int64, bool, error) {
	if end < 0 {
		end = 0
	}
	windowStart := end
	total := 0
	newlines := 0
	data := make([]byte, 0, diskLogChunk)
	for windowStart > 0 && newlines <= limit && total < diskLogScanBytes {
		n := int64(diskLogChunk)
		if n > windowStart {
			n = windowStart
		}
		if total+int(n) > diskLogScanBytes {
			n = int64(diskLogScanBytes - total)
		}
		if n <= 0 {
			break
		}
		windowStart -= n
		chunk := make([]byte, n)
		if _, err := f.ReadAt(chunk, windowStart); err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, false, err
		}
		data = append(chunk, data...)
		total += len(chunk)
		newlines += strings.Count(string(chunk), "\n")
	}
	lastNL := strings.LastIndexByte(string(data), '\n')
	if lastNL < 0 {
		return nil, windowStart, false, nil
	}
	completeEnd := windowStart + int64(lastNL) + 1
	refs := make([]lineRef, 0, limit)
	start := windowStart
	for _, segment := range strings.Split(string(data[:lastNL]), "\n") {
		length := int64(len(segment))
		if length > 0 {
			refs = append(refs, lineRef{Day: day, Start: start, End: start + length})
		}
		start += length + 1
	}
	if len(refs) > limit {
		refs = refs[len(refs)-limit:]
	}
	more := len(refs) > 0 && refs[0].Start > 0
	return refs, completeEnd, more, nil
}

// readItems 按 refs 打开对应日期文件读取行内容（跨文件时逐个打开）。
func readItems(root *os.Root, refs []lineRef) ([]DiskLogItem, error) {
	items := make([]DiskLogItem, 0, len(refs))
	current := ""
	var f *os.File
	closeCurrent := func() {
		if f != nil {
			_ = f.Close()
			f = nil
		}
	}
	defer closeCurrent()
	for _, ref := range refs {
		if ref.Day != current {
			closeCurrent()
			name := "app-" + ref.Day + ".log"
			nf, _, err := openRegular(root, name)
			if err != nil {
				return nil, err
			}
			f, current = nf, ref.Day
		}
		line, err := readLine(f, ref)
		if err != nil {
			return nil, err
		}
		items = append(items, parseLogLine(ref, line))
	}
	return items, nil
}

func readLine(f *os.File, ref lineRef) ([]byte, error) {
	length := ref.End - ref.Start
	truncated := false
	if length > diskLogMaxLine {
		length = diskLogMaxLine
		truncated = true
	}
	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, ref.Start); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if truncated {
		buf = append(buf, []byte(" …[已截断]")...)
	}
	return buf, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// parseLogLine 把一行文本解析成安全条目。旧的非 slog 行无法证明不含秘密，
// 默认只展示隐藏占位符与可安全解析的时间/级别，不提供「显示原文」。
func parseLogLine(ref lineRef, raw []byte) DiskLogItem {
	line := strings.TrimSuffix(string(raw), "\r")
	item := DiskLogItem{ID: encodeLineID(ref)}
	if t, ok := slogTextField(line, "time"); ok {
		item.Time = t
	} else {
		item.Time = ref.Day
	}
	if l, ok := slogTextField(line, "level"); ok {
		item.Level = l
	} else {
		item.Level = "UNKNOWN"
	}
	msg, ok := slogTextField(line, "msg")
	if !ok {
		item.Message = "[历史非结构化日志已隐藏：无法证明其中不含凭据]"
		return item
	}
	msg = strings.ToValidUTF8(msg, "\uFFFD")
	item.Message = SanitizeText(msg)
	return item
}

// slogTextField 解析 text handler 的 key=value；带引号的值按 Go 转义规则解码。
func slogTextField(line, key string) (string, bool) {
	needle := key + "="
	for i := 0; i+len(needle) <= len(line); i++ {
		if i > 0 && line[i-1] != ' ' {
			continue
		}
		if !strings.HasPrefix(line[i:], needle) {
			continue
		}
		rest := line[i+len(needle):]
		if rest == "" {
			return "", true
		}
		if rest[0] == '"' {
			for j := 1; j < len(rest); j++ {
				if rest[j] == '\\' {
					j++
					continue
				}
				if rest[j] == '"' {
					if value, err := strconv.Unquote(rest[:j+1]); err == nil {
						return value, true
					}
					return rest[1:j], true
				}
			}
			return rest[1:], true
		}
		if end := strings.IndexByte(rest, ' '); end >= 0 {
			return rest[:end], true
		}
		return rest, true
	}
	return "", false
}

func encodeLineID(ref lineRef) string {
	raw := "line|" + ref.Day + "|" + strconv.FormatInt(ref.Start, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func encodeCursor(cur diskCursor) string {
	payload := "v1|" + cur.Day + "|" + strconv.FormatInt(cur.Offset, 10)
	mac := hmac.New(sha256.New, cursorKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

func decodeCursor(value string) (diskCursor, error) {
	if len(value) == 0 || len(value) > 256 {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	payloadB64, sigHex, ok := strings.Cut(value, ".")
	if !ok || len(sigHex) != 64 {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	want, err := hex.DecodeString(sigHex)
	if err != nil {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	mac := hmac.New(sha256.New, cursorKey)
	mac.Write(payload)
	if !hmac.Equal(want, mac.Sum(nil)) {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 || parts[0] != "v1" {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	if _, err := time.Parse(diskLogLayout, parts[1]); err != nil {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	offset, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || offset < 0 {
		return diskCursor{}, ErrLogsCursorInvalid
	}
	return diskCursor{Day: parts[1], Offset: offset}, nil
}
