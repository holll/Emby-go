package logging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRawLogsPreserveTextAndResume(t *testing.T) {
	dir := t.TempDir()
	app := "2026-11-12 INFO original token=RAW_FIXTURE\r\n{\"time\":\"now\",\"level\":\"DEBUG\",\"message\":\"json\"}\n  continuation\n\npartial"
	request := "[GIN] original request\n{\n  \"body\": true\n}\n"
	for kind, text := range map[string]string{"app": app, "request": request} {
		path := filepath.Join(dir, kind+"-2026-11-12.log")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		page, err := readRawLogsAt(dir, kind, "")
		if err != nil || page.Text != text || page.Cursor == "" || page.Reset || page.HasMore {
			t.Fatalf("raw text/cursor mismatch: kind=%s err=%v", kind, err)
		}
		idle, err := readRawLogsAt(dir, kind, page.Cursor)
		if err != nil || idle.Text != "" || idle.Cursor != page.Cursor {
			t.Fatalf("idle mismatch: %v", err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(" more\n新行\n")
		f.Close()
		next, err := readRawLogsAt(dir, kind, page.Cursor)
		if err != nil || next.Text != " more\n新行\n" || next.Reset {
			t.Fatalf("resume mismatch: %v", err)
		}
		other := "app"
		if kind == other {
			other = "request"
		}
		if _, err := readRawLogsAt(dir, other, page.Cursor); !errors.Is(err, ErrLogsCursorInvalid) {
			t.Fatal("cursor must bind kind")
		}
	}
	if _, err := readRawLogsAt(dir, "app", "forged"); !errors.Is(err, ErrLogsCursorInvalid) {
		t.Fatal("accepted forged cursor")
	}
}

func TestRawLogsRotationAndTruncation(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "app-2026-11-11.log")
	os.WriteFile(first, []byte("old\n"), 0o600)
	page, err := readRawLogsAt(dir, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "app-2026-11-12.log")
	os.WriteFile(second, []byte("new day\n"), 0o600)
	next, err := readRawLogsAt(dir, "app", page.Cursor)
	if err != nil || next.Text != "new day\n" || next.Reset {
		t.Fatalf("rotation mismatch: %v", err)
	}
	os.WriteFile(second, []byte("x\n"), 0o600)
	reset, err := readRawLogsAt(dir, "app", next.Cursor)
	if err != nil || !reset.Reset || !reset.Gap || reset.Text != "x\n" {
		t.Fatalf("truncation not reported: %v", err)
	}
	os.Remove(first)
	reset, err = readRawLogsAt(dir, "app", page.Cursor)
	if err != nil || !reset.Reset || !reset.Gap || reset.Text != "x\n" {
		t.Fatalf("cleanup not reported: %v", err)
	}
	os.WriteFile(second, []byte("same size replacement\n"), 0o600)
	reset, err = readRawLogsAt(dir, "app", reset.Cursor)
	if err != nil || !reset.Reset {
		t.Fatalf("replacement not reported: %v", err)
	}
}

func TestRawLogsBoundedUnicodeCatchup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-2026-11-12.log")
	os.WriteFile(path, nil, 0o600)
	initial, err := readRawLogsAt(dir, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("原始长行", RawLogChunkBytes/3) + "\nend\n"
	os.WriteFile(path, []byte(text), 0o600)
	var restored strings.Builder
	cursor := initial.Cursor
	for i := 0; i < 10; i++ {
		page, err := readRawLogsAt(dir, "app", cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Text) > RawLogChunkBytes || !utf8.ValidString(page.Text) || page.Reset {
			t.Fatal("invalid bounded chunk")
		}
		restored.WriteString(page.Text)
		cursor = page.Cursor
		if !page.HasMore {
			break
		}
	}
	if restored.String() != text {
		t.Fatal("catch-up changed raw text")
	}
	tail, err := readRawLogsAt(dir, "app", "")
	if err != nil || len(tail.Text) > RawLogChunkBytes || !tail.Gap || !strings.HasSuffix(text, tail.Text) {
		t.Fatal("tail not bounded or not a raw suffix")
	}
}

func TestRawLogsInitialTailLongUnterminatedUnicodeLine(t *testing.T) {
	dir := t.TempDir()
	text := strings.Repeat("字", RawLogChunkBytes/3+10)
	if err := os.WriteFile(filepath.Join(dir, "app-2026-11-12.log"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := readRawLogsAt(dir, "app", "")
	if err != nil || !page.Gap || page.Reset || !utf8.ValidString(page.Text) || strings.ContainsRune(page.Text, utf8.RuneError) || !strings.HasSuffix(text, page.Text) || len(page.Text) > RawLogChunkBytes {
		t.Fatalf("initial Unicode tail is not a bounded original suffix: %v", err)
	}
}

func TestRawLogsRejectOutsideSymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "outside.log")
	os.WriteFile(secret, []byte("outside"), 0o600)
	if err := os.Symlink(secret, filepath.Join(dir, "request-2026-11-12.log")); err != nil {
		t.Skip(err)
	}
	if _, err := readRawLogsAt(dir, "request", ""); !errors.Is(err, ErrLogsUnavailable) {
		t.Fatal("outside symlink was readable")
	}
}
