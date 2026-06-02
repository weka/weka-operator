// Package cmdutil provides helpers for running external commands with context propagation.
package cmdutil

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
)

// PollUntil calls fn every interval until it returns true, or ctx is done.
// fn should perform any per-iteration logging/side effects itself before returning false.
func PollUntil(ctx context.Context, interval time.Duration, fn func() bool) error {
	for {
		if fn() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// Output runs the named command with args under ctx and returns its stdout.
// Stderr is captured: logged as a warning when non-empty and appended to any error.
func Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cmdutil.output", "cmd", name)
	defer logger.End()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // args are controlled by internal callers
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if stderr.Len() > 0 {
		logger.Warn("stderr output", "stderr", stderr.String())
	}
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%s %s: %w\nstderr: %s", name, strings.Join(args, " "), err, stderr.String())
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// Run runs the named command with args under ctx, discarding stdout.
// Stderr is captured: logged as a warning when non-empty and appended to any error.
func Run(ctx context.Context, name string, args ...string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cmdutil.run", "cmd", name)
	defer logger.End()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // args are controlled by internal callers
	cmd.Stderr = &stderr

	err := cmd.Run()
	if stderr.Len() > 0 {
		logger.Warn("stderr output", "stderr", stderr.String())
	}
	if err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%s %s: %w\nstderr: %s", name, strings.Join(args, " "), err, stderr.String())
		}
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
