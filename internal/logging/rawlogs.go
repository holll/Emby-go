package logging

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// RawLogChunkBytes bounds each SSE batch, not individual lines. Oversized lines
// continue in the next batch; consumers concatenate Text without adding newlines.
const RawLogChunkBytes = 256 << 10

// RawLogPage deliberately preserves on-disk text, unlike the legacy redacted API.
type RawLogPage struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Cursor  string `json:"cursor"`
	Reset   bool   `json:"reset"`
	Gap     bool   `json:"gap"`
	HasMore bool   `json:"has_more"`
}

type rawLogCursor struct {
	Kind   string `json:"k"`
	Day    string `json:"d"`
	Offset int64  `json:"o"`
	Anchor string `json:"a"`
}

// ReadRawLogs reads fixed app/request sources only. Kind is validated by the
// HTTP boundary. A signed cursor binds the source, day, position and preceding
// bytes so truncation/replacement cannot silently skip to a stale position.
func ReadRawLogs(kind, after string) (RawLogPage, error) {
	return readRawLogsAt(AppLogDir(), kind, after)
}

func readRawLogsAt(dir, kind, after string) (RawLogPage, error) {
	page := RawLogPage{Kind: kind}
	var cur rawLogCursor
	if after != "" {
		var err error
		cur, err = decodeRawLogCursor(after, kind)
		if err != nil {
			return page, err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if os.IsNotExist(err) {
			page.Reset = after != ""
			page.Gap = page.Reset
			return page, nil
		}
		return page, ErrLogsUnavailable
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return page, ErrLogsUnavailable
	}
	var days []string
	for _, entry := range entries {
		name := entry.Name()
		day, ok := strings.CutPrefix(name, kind+"-")
		if !ok || !strings.HasSuffix(day, ".log") {
			continue
		}
		day = strings.TrimSuffix(day, ".log")
		if _, err := time.Parse(dailyLayout, day); err != nil {
			continue
		}
		days = append(days, day)
	}
	slices.Sort(days)
	if len(days) == 0 {
		page.Reset = after != ""
		page.Gap = page.Reset
		return page, nil
	}
	idx := len(days) - 1
	if after != "" {
		idx = slices.Index(days, cur.Day)
		if idx < 0 {
			idx = len(days) - 1
			page.Reset = true
			page.Gap = true
		}
	}
	for {
		f, size, err := openRegular(root, kind+"-"+days[idx]+".log")
		if err != nil {
			return page, ErrLogsUnavailable
		}
		offset := cur.Offset
		if after != "" && !page.Reset {
			anchor, err := rawLogAnchor(f, min(offset, size))
			if err != nil {
				f.Close()
				return page, ErrLogsUnavailable
			}
			if offset > size || anchor != cur.Anchor {
				f.Close()
				page.Reset = true
				page.Gap = true
				idx = len(days) - 1
				continue
			}
		}
		if after == "" || page.Reset {
			offset = max(int64(0), size-RawLogChunkBytes)
		}
		data := make([]byte, min(int64(RawLogChunkBytes), size-offset))
		n, err := f.ReadAt(data, offset)
		if err != nil && !errors.Is(err, io.EOF) {
			f.Close()
			return page, ErrLogsUnavailable
		}
		data = data[:n]
		if (after == "" || page.Reset) && offset > 0 {
			// Begin at a whole line when the bounded initial tail starts mid-line.
			// If a single line exceeds the batch, keep its suffix and report the gap.
			if newline := bytes.IndexByte(data, '\n'); !atBoundary(f, offset) && newline >= 0 {
				offset += int64(newline + 1)
				data = data[newline+1:]
			}
			// A bounded tail of one unterminated long line may begin inside a
			// UTF-8 character. Omit only that incomplete leading character.
			for len(data) > 0 && !utf8.RuneStart(data[0]) {
				offset++
				data = data[1:]
			}
			page.Gap = true
		}
		if offset+int64(len(data)) < size {
			// Do not split a UTF-8 code point across JSON strings.
			end := len(data)
			for end > 0 && !utf8.RuneStart(data[end-1]) {
				end--
			}
			if end > 0 && !utf8.FullRune(data[end-1:]) {
				data = data[:end-1]
			}
		}
		offset += int64(len(data))
		anchor, err := rawLogAnchor(f, offset)
		f.Close()
		if err != nil {
			return page, ErrLogsUnavailable
		}
		page.Text = string(data)
		page.Cursor = encodeRawLogCursor(rawLogCursor{Kind: kind, Day: days[idx], Offset: offset, Anchor: anchor})
		page.HasMore = offset < size || idx < len(days)-1
		if len(data) == 0 && idx < len(days)-1 {
			idx++
			// Move to the next day's beginning, not its tail.
			cur = rawLogCursor{Offset: 0, Anchor: hex.EncodeToString(sha256.New().Sum(nil))}
			after = page.Cursor
			continue
		}
		return page, nil
	}
}

func rawLogAnchor(f *os.File, offset int64) (string, error) {
	start := max(int64(0), offset-64)
	data := make([]byte, offset-start)
	if _, err := f.ReadAt(data, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func encodeRawLogCursor(cur rawLogCursor) string {
	payload, _ := json.Marshal(cur)
	mac := hmac.New(sha256.New, cursorKey)
	mac.Write([]byte("raw-v1:"))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
}

func decodeRawLogCursor(value, kind string) (rawLogCursor, error) {
	var cur rawLogCursor
	if len(value) > 512 {
		return cur, ErrLogsCursorInvalid
	}
	body, sig, ok := strings.Cut(value, ".")
	if !ok {
		return cur, ErrLogsCursorInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return cur, ErrLogsCursorInvalid
	}
	signature, err := hex.DecodeString(sig)
	if err != nil {
		return cur, ErrLogsCursorInvalid
	}
	mac := hmac.New(sha256.New, cursorKey)
	mac.Write([]byte("raw-v1:"))
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cur, ErrLogsCursorInvalid
	}
	if err := json.Unmarshal(payload, &cur); err != nil {
		return cur, ErrLogsCursorInvalid
	}
	if _, err := time.Parse(dailyLayout, cur.Day); err != nil || cur.Kind != kind || cur.Offset < 0 || len(cur.Anchor) != 64 {
		return cur, ErrLogsCursorInvalid
	}
	return cur, nil
}
