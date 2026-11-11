//go:build unix

package artwork

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMediaProcessDiagnosticsSafe(t *testing.T) {
	dir := t.TempDir()
	raw := "https://canary-host.invalid/private-canary/movie?token=query-canary&signature=encoded%2Fcanary"
	probe := spriteScript(t, dir, "probe", "printf '%s\\n' \"$@\" >&2; printf '%s\\n' 'query-canary encoded%2Fcanary private-canary Authorization: Bearer standalone-canary' 'Server returned 403 Forbidden' >&2; exit 7")
	_, err := spriteProbe(context.Background(), raw, SpriteOptions{FFprobePath: probe})
	var diagnostic *MediaProcessError
	if !errors.As(err, &diagnostic) || diagnostic.kind != "exit_failed" || diagnostic.exitCode != 7 {
		t.Fatalf("missing classification: %v", err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Warn("failed", "media_process", diagnostic)
	for _, secret := range []string{raw, "canary-host", "private-canary", "query-canary", "encoded%2Fcanary", "standalone-canary", probe} {
		if strings.Contains(logs.String(), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked: %q", secret)
		}
	}
	for _, want := range []string{"server returned 403", "stderr_bytes_seen", "$FFPROBE", "$STREAM_URL", "format=duration", "exit_code\":7"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing safe diagnostic %q: %s", want, logs.String())
		}
	}
	if strings.Contains(err.Error(), "argv") || strings.Contains(err.Error(), "server returned") {
		t.Fatalf("DTO error contains log-only diagnostics: %v", err)
	}

	ff := spriteScript(t, dir, "ffmpeg", "printf '%s\\n' \"$@\" >&2; exit 9")
	seconds := 12.5
	_, err = screenshot(context.Background(), raw, Request{PositionSeconds: &seconds}, Options{FFmpegPath: ff})
	if !errors.As(err, &diagnostic) || diagnostic.exitCode != 9 {
		t.Fatalf("screenshot classification: %v", err)
	}
	logs.Reset()
	slog.New(slog.NewJSONHandler(&logs, nil)).Warn("failed", "media_process", diagnostic)
	for _, want := range []string{"$FFMPEG", "$STREAM_URL", "12.500", "scale=1920:1920"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing screenshot template %q: %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "canary") || strings.Contains(err.Error(), "argv") {
		t.Fatalf("unsafe screenshot error/log: %v / %s", err, logs.String())
	}
}

func TestCappedBufferPartialWriteCount(t *testing.T) {
	buffer := &cappedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("ab")); n != 2 || err != nil {
		t.Fatalf("initial write = %d, %v", n, err)
	}
	if n, err := buffer.Write([]byte("cdef")); n != 2 || err == nil {
		t.Fatalf("partial write = %d, %v", n, err)
	}
	if string(buffer.Bytes()) != "abcd" || buffer.total != 6 || !buffer.overflow {
		t.Fatalf("unexpected bounded prefix: %+v", buffer)
	}
	if n, err := buffer.Write([]byte("gh")); n != 0 || err == nil {
		t.Fatalf("full write = %d, %v", n, err)
	}
}

func TestMediaProcessRejectsUnknownArgv(t *testing.T) {
	fake := spriteScript(t, t.TempDir(), "fail", "printf '%s\\n' \"$@\" >&2; exit 7")
	for _, args := range [][]string{
		{"-c", "echo standalone-canary", "--", "-i", "https://url-canary.invalid/path", "query-canary"},
		{"untrusted-canary", "error", "-protocol_whitelist", protocols, "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", "https://url-canary.invalid/path"},
		{"-nostdin", "-v", "error", "-protocol_whitelist", protocols, "-max_pixels", strconv.Itoa(maxPixels), "-threads", "1", "-ss", "1.000", "-i", "https://url-canary.invalid/path", "-map", "0:v:0", "-frames:v", "1", "-vf", "untrusted-filter-canary", "-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"},
	} {
		_, err := command(context.Background(), fake, args, 64)
		var diagnostic *MediaProcessError
		if !errors.As(err, &diagnostic) {
			t.Fatal(err)
		}
		if len(diagnostic.argv) != 0 {
			t.Fatalf("unsupported argv copied: %v", diagnostic.argv)
		}
		var logs bytes.Buffer
		slog.New(slog.NewJSONHandler(&logs, nil)).Warn("failed", "media_process", diagnostic)
		if strings.Contains(logs.String(), "canary") {
			t.Fatalf("unknown argv leaked: %s", logs.String())
		}
	}
}

func TestMediaProcessFailureKinds(t *testing.T) {
	for _, kind := range []string{"deadline_exceeded", "cancelled", "stdout_limit", "stderr_limit", "start_failed"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			body := "sleep 20"
			if kind == "stdout_limit" {
				body = "exec yes private-token-canary"
			}
			if kind == "stderr_limit" {
				body = "exec 1>&2; exec yes private-token-canary"
			}
			path := spriteScript(t, dir, "fake", body)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if kind == "deadline_exceeded" {
				var shortCancel context.CancelFunc
				ctx, shortCancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer shortCancel()
			}
			if kind == "cancelled" {
				cancel()
			}
			if kind == "start_failed" {
				path = filepath.Join(dir, "absent-canary")
			}
			_, err := command(ctx, path, nil, 64)
			var diagnostic *MediaProcessError
			if !errors.As(err, &diagnostic) || diagnostic.kind != kind {
				t.Fatalf("want %s, got %v", kind, err)
			}
			if kind == "deadline_exceeded" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if kind == "stderr_limit" && (!diagnostic.stderrTruncated || diagnostic.stderrBytes <= 32<<10) {
				t.Fatalf("missing bounded stderr evidence: %+v", diagnostic)
			}
			var logs bytes.Buffer
			slog.New(slog.NewJSONHandler(&logs, nil)).Warn("failed", "media_process", diagnostic)
			if strings.Contains(logs.String(), "canary") || strings.Contains(err.Error(), "canary") || logs.Len() > 2048 {
				t.Fatalf("unsafe/unbounded diagnostics: %v / %s", err, logs.String())
			}
		})
	}
}

func TestSpriteStageDeadlineDiagnostics(t *testing.T) {
	for _, stage := range []string{"probe", "frame"} {
		t.Run(stage, func(t *testing.T) {
			path := spriteScript(t, t.TempDir(), "slow", "sleep 20")
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			opts := SpriteOptions{FFprobePath: path, FFmpegPath: path}
			var err error
			if stage == "probe" {
				_, err = spriteProbe(ctx, "https://trusted.invalid/token-canary", opts)
			} else {
				_, err = spriteFrame(ctx, "https://trusted.invalid/token-canary", 1234, opts)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			for _, want := range []string{"stage=" + stage, "stage_elapsed_ms=", "stage_budget_ms=", "deadline_exceeded"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %s: %v", want, err)
				}
			}
			if stage == "frame" && !strings.Contains(err.Error(), "seek_ms=1234") {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "canary") {
				t.Fatal("source leaked")
			}
		})
	}
}

func TestSpriteGenerationDiagnosticContext(t *testing.T) {
	for _, stage := range []string{"probe", "frame"} {
		t.Run(stage, func(t *testing.T) {
			db, movie, dir := fixture(t)
			opts := spriteTestOptions(t, dir, "2")
			bad := spriteScript(t, dir, "fail", "printf '%s\\n' 'private-canary Invalid data found when processing input' >&2; exit 3")
			if stage == "probe" {
				opts.FFprobePath = bad
			} else {
				opts.FFmpegPath = bad
			}
			_, err := GenerateSprite(context.Background(), db, SpriteRequest{MovieID: movie.ID, IntervalMs: 10000}, opts)
			if err == nil {
				t.Fatal("expected failure")
			}
			for _, want := range []string{"stage=" + stage, "total_budget_ms=60000", "total_elapsed_ms=", "remaining_ms="} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %s: %v", want, err)
				}
			}
			if stage == "frame" {
				for _, want := range []string{"frame_index=0", "frames_total=1", "seek_ms=0", "effective_interval_ms=10000"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("missing %s: %v", want, err)
					}
				}
			}
			if strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "argv") {
				t.Fatal("log-only detail leaked into Error")
			}
		})
	}
}
