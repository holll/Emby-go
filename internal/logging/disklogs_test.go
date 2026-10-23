package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, dir, day string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "app-"+day+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestDiskLogsTailBeforeAfterAndSanitize(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "2026-10-20",
		`time=2026-10-20T23:00:00+08:00 level=INFO msg="旧日志 1"`,
		`time=2026-10-20T23:01:00+08:00 level=WARN msg="旧日志 2 token=CANARY_OLD"`)
	writeLog(t, dir, "2026-10-21",
		`time=2026-10-21T00:00:00+08:00 level=INFO msg="新日志 1"`,
		`time=2026-10-21T00:01:00+08:00 level=INFO msg="新日志 2"`,
		`time=2026-10-21T00:02:00+08:00 level=ERROR msg="新日志 3"`,
		`garbage no structure CANARY_LEGACY`)

	page, err := readDiskLogsAt(dir, DiskLogQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !strings.Contains(page.Items[0].Message, "新日志 3") || !strings.Contains(page.Items[1].Message, "历史非结构化日志已隐藏") {
		t.Fatalf("tail items: %+v", page.Items)
	}
	if !page.HasMore || page.NextBefore == "" || page.NextAfter == "" {
		t.Fatalf("tail cursors: %+v", page)
	}
	// 非结构化行默认隐藏，不展示原文。
	if strings.Contains(page.Items[1].Message, "CANARY") {
		t.Fatalf("legacy line leaked: %+v", page.Items[1])
	}

	older, err := readDiskLogsAt(dir, DiskLogQuery{Before: page.NextBefore, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Items) != 4 {
		t.Fatalf("before items: %+v", older.Items)
	}
	if !strings.Contains(older.Items[0].Message, "旧日志 1") {
		t.Fatalf("cross-day order: %+v", older.Items)
	}
	if strings.Contains(older.Items[1].Message, "CANARY_OLD") {
		t.Fatalf("historical credential leaked: %+v", older.Items[1])
	}
	if older.HasMore {
		t.Fatalf("first file should be exhausted: %+v", older)
	}

	// after：从 tail 的 next_after 追新；先追加一行再读。
	path := filepath.Join(dir, "app-2026-10-21.log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`time=2026-10-21T00:03:00+08:00 level=INFO msg="追加日志"` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	newer, err := readDiskLogsAt(dir, DiskLogQuery{After: page.NextAfter, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(newer.Items) != 1 || newer.Items[0].Message != "追加日志" {
		t.Fatalf("after items: %+v", newer.Items)
	}
}

func TestDiskLogsPartialLineAndReset(t *testing.T) {
	dir := t.TempDir()
	path := writeLog(t, dir, "2026-10-22", `time=2026-10-22T10:00:00+08:00 level=INFO msg="完整"`)
	page, err := readDiskLogsAt(dir, DiskLogQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 追加半行：进度游标不能越过它，也不能把半行当完整日志。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`time=2026-10-22T10:00:01+08:00 level=INFO msg="半行`)
	f.Close()
	again, err := readDiskLogsAt(dir, DiskLogQuery{After: page.NextAfter, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 0 || !again.HasMore {
		t.Fatalf("partial line must be withheld: %+v", again)
	}
	if again.NextAfter != page.NextAfter {
		t.Fatalf("cursor advanced past partial line: %s -> %s", page.NextAfter, again.NextAfter)
	}

	if _, err := readDiskLogsAt(dir, DiskLogQuery{After: "forged"}); err != ErrLogsCursorInvalid {
		t.Fatalf("forged cursor accepted: %v", err)
	}
	if _, err := readDiskLogsAt(dir, DiskLogQuery{After: encodeCursor(diskCursor{Day: "2020-01-01", Offset: 0})}); err != nil {
		t.Fatalf("missing day should reset: %v", err)
	} else {
		// reset page must be flagged, not silently pretend continuity
	}
	reset, err := readDiskLogsAt(dir, DiskLogQuery{After: encodeCursor(diskCursor{Day: "2020-01-01", Offset: 0})})
	if err != nil || !reset.Reset || !reset.Gap || len(reset.Items) != 1 {
		t.Fatalf("missing day not reset: %+v err=%v", reset, err)
	}
	mid, err := readDiskLogsAt(dir, DiskLogQuery{After: encodeCursor(diskCursor{Day: "2026-10-22", Offset: 5})})
	if err != nil || !mid.Reset || !mid.Gap {
		t.Fatalf("mid-line cursor not reset: %+v err=%v", mid, err)
	}
}

func TestDiskLogsEmptyAndOversizedLine(t *testing.T) {
	dir := t.TempDir()
	page, err := readDiskLogsAt(dir, DiskLogQuery{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("empty dir: %+v %v", page, err)
	}
	long := strings.Repeat("x", 70<<10)
	writeLog(t, dir, "2026-10-23", `time=2026-10-23T10:00:00+08:00 level=INFO msg="`+long+`"`)
	page, err = readDiskLogsAt(dir, DiskLogQuery{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("oversized line: %+v %v", page, err)
	}
	if !strings.Contains(page.Items[0].Message, "已截断") {
		t.Fatalf("oversized line not truncated: %.60s", page.Items[0].Message)
	}
}
