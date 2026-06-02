// Package cmdutil provides helpers for running external commands with context propagation.
package cmdutil

import (
	"bytes"
	"context"
	"errors"
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
// Logs the full command before execution and its exit code after, matching Python
// run_command (weka_runtime.py:2224-2235).
// Stderr is captured: logged as a warning when non-empty and appended to any error.
func Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	fullCmd := name + " " + strings.Join(args, " ")
	ctx, logger := instrumentation.CreateLogSpan(ctx, "cmd", "command", fullCmd)
	defer logger.End()

	logger.Info("Running command", "command", fullCmd)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // args are controlled by internal callers
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if stderr.Len() > 0 {
		logger.Warn("stderr output", "stderr", stderr.String())
	}
	logger.Info("Command finished", "command", fullCmd, "code", exitCodeOf(err))
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%s: %w\nstderr: %s", fullCmd, err, stderr.String())
		}
		return nil, fmt.Errorf("%s: %w", fullCmd, err)
	}
	return out, nil
}

// Run is Output with stdout discarded.
func Run(ctx context.Context, name string, args ...string) error {
	_, err := Output(ctx, name, args...)
	return err
}

// exitCodeOf returns 0 for nil, the process exit code for an *exec.ExitError, or -1 otherwise.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
