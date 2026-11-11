package artwork

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MediaProcessError keeps untrusted subprocess output out of Error (and thus
// task DTOs). Only its bounded, allowlisted diagnostics are available to logs.
// No raw URL, executable path, stderr or exec error is retained in this value.
type MediaProcessError struct {
	kind            string
	exitCode        int
	elapsed         time.Duration
	stderr          string
	stderrBytes     int
	stderrTruncated bool
	argv            []string
	cause           error
}

func (e *MediaProcessError) Error() string {
	return fmt.Sprintf("media subprocess failed: %s (exit_code=%d)", e.kind, e.exitCode)
}

func (e *MediaProcessError) Unwrap() error { return e.cause }

func (e *MediaProcessError) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("kind", e.kind), slog.Int("exit_code", e.exitCode),
		slog.Int64("elapsed_ms", e.elapsed.Milliseconds()),
		slog.String("stderr_summary", e.stderr), slog.Int("stderr_bytes_seen", e.stderrBytes),
		slog.Bool("stderr_truncated", e.stderrTruncated), slog.Any("argv_template", e.argv),
	)
}

func command(ctx context.Context, path string, args []string, limit int) ([]byte, error) {
	started := time.Now()
	processCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(processCtx, path, args...)
	containProcess(cmd)
	out := &cappedBuffer{limit: limit, onOverflow: stop}
	diagnostic := &cappedBuffer{limit: 32 << 10, onOverflow: stop}
	cmd.Stdout = out
	cmd.Stderr = diagnostic
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		failure := &MediaProcessError{
			kind: "exit_failed", exitCode: -1, elapsed: time.Since(started),
			stderrBytes: diagnostic.total, stderrTruncated: diagnostic.overflow,
		}
		if cmd.ProcessState != nil {
			failure.exitCode = cmd.ProcessState.ExitCode()
		}
		switch {
		case ctx.Err() != nil:
			failure.cause = ctx.Err()
			failure.kind = "cancelled"
			if errors.Is(failure.cause, context.DeadlineExceeded) {
				failure.kind = "deadline_exceeded"
			}
		case out.overflow:
			failure.kind = "stdout_limit"
		case diagnostic.overflow:
			failure.kind = "stderr_limit"
		case cmd.Process == nil:
			failure.kind = "start_failed"
		case errors.Is(err, exec.ErrWaitDelay):
			failure.kind = "wait_delay"
		}

		// FFmpeg may echo URLs, individual query/path tokens or response content.
		// Redacting URL-shaped strings alone cannot make this safe. Extract only
		// known diagnostic phrases; never return any portion of an unknown line.
		text := strings.ToLower(string(diagnostic.Bytes()))
		var summaries []string
		for _, phrase := range []string{
			"connection refused", "connection timed out", "network is unreachable",
			"no route to host", "name or service not known", "temporary failure in name resolution",
			"server returned 401", "server returned 403", "server returned 404", "server returned 5",
			"http error 401", "http error 403", "http error 404", "http error 5",
			"invalid data found when processing input", "moov atom not found",
			"decoder not found", "unknown decoder", "permission denied", "protocol not found",
			"invalid argument", "output file is empty",
		} {
			if strings.Contains(text, phrase) {
				summaries = append(summaries, phrase)
			}
		}
		failure.stderr = strings.Join(summaries, "; ")
		if len(summaries) == 0 && diagnostic.total > 0 {
			failure.stderr = "[unrecognized subprocess stderr redacted]"
		}

		// All production non-input arguments are static options or numeric seek
		// values. Replace the source position, not just URL-shaped substrings.
		// Unsupported command shapes (including test shell wrappers) expose none
		// of their arguments. Templates are argv, not shell commands to eval.
		if len(args) == 11 && slices.Equal(args[:5], []string{"-v", "error", "-protocol_whitelist", protocols, "-user_agent"}) && args[5] == spriteUserAgent &&
			slices.Equal(args[6:10], []string{"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1"}) {
			failure.argv = append([]string{"$FFPROBE"}, args...)
			failure.argv[11] = "$STREAM_URL"
		} else if len(args) == 28 &&
			slices.Equal(args[:7], []string{"-nostdin", "-v", "error", "-protocol_whitelist", protocols, "-user_agent", spriteUserAgent}) &&
			slices.Equal(args[7:12], []string{"-max_pixels", strconv.Itoa(maxPixels), "-threads", "1", "-ss"}) &&
			args[13] == "-i" && slices.Equal(args[15:20], []string{"-map", "0:v:0", "-frames:v", "1", "-vf"}) &&
			(args[20] == "scale=1920:1920:force_original_aspect_ratio=decrease" || args[20] == "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2") &&
			slices.Equal(args[21:], []string{"-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"}) {
			if _, parseErr := strconv.ParseFloat(args[12], 64); parseErr == nil {
				failure.argv = append([]string{"$FFMPEG"}, args...)
				failure.argv[15] = "$STREAM_URL"
			}
		} else if len(args) == 26 &&
			slices.Equal(args[:10], []string{"-nostdin", "-v", "error", "-protocol_whitelist", protocols, "-max_pixels", strconv.Itoa(maxPixels), "-threads", "1", "-ss"}) &&
			args[11] == "-i" && slices.Equal(args[13:18], []string{"-map", "0:v:0", "-frames:v", "1", "-vf"}) &&
			args[18] == "scale=1920:1920:force_original_aspect_ratio=decrease" &&
			slices.Equal(args[19:], []string{"-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1"}) {
			if _, parseErr := strconv.ParseFloat(args[10], 64); parseErr == nil {
				failure.argv = append([]string{"$FFMPEG"}, args...)
				failure.argv[13] = "$STREAM_URL"
			}
		}
		return nil, failure
	}
	return out.Bytes(), nil
}
