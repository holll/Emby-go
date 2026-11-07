package probe

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const (
	MaxTimeout    = 60 * time.Second
	MaxStdoutSize = 4 << 20
	MaxStderrSize = 64 << 10
)

var (
	ErrStdoutTooLarge = errors.New("ffprobe stdout exceeds 4 MiB limit")
	ErrStderrTooLarge = errors.New("ffprobe stderr exceeds 64 KiB limit")
	processSlots      = make(chan struct{}, 2)
)

// boundedOutput is owned by one exec output-copy goroutine. Only cancellation
// is shared with the other stream, via context's concurrency-safe cancel func.
type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
	err    error
	cancel context.CancelCauseFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) <= remaining {
		return b.buffer.Write(p)
	}
	n, _ := b.buffer.Write(p[:remaining])
	b.cancel(b.err)
	return n, b.err
}

// execute shares admission and resource limits between Run and Verify. The
// caller establishes its deadline before admission, so waiting uses that budget.
func execute(ctx context.Context, path string, args ...string) ([]byte, []byte, error) {
	select {
	case processSlots <- struct{}{}:
		defer func() { <-processSlots }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stdout := boundedOutput{limit: MaxStdoutSize, err: ErrStdoutTooLarge, cancel: cancel}
	stderr := boundedOutput{limit: MaxStderrSize, err: ErrStderrTooLarge, cancel: cancel}
	cmd := exec.CommandContext(runCtx, path, args...)
	configureProcess(cmd)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// This is an additional pipe-drain bound, not a promise that all cleanup is
	// inside the caller's budget. It also handles descendants retaining pipes
	// after the direct child has exited.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	err := cmd.Wait()
	// A direct child may exit before its descendants; clean up its group even
	// on normal exit. Non-Unix can only kill the direct child (see fallback).
	_ = killProcess(cmd)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, stderr.buffer.Bytes(), ctxErr
	}
	if cause := context.Cause(runCtx); cause != nil {
		return nil, stderr.buffer.Bytes(), cause
	}
	return stdout.buffer.Bytes(), stderr.buffer.Bytes(), err
}
