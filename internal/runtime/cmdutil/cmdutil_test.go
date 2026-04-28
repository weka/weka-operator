package cmdutil

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutput_success(t *testing.T) {
	out, err := Output(context.Background(), "echo", "hello")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(out))
}

func TestOutput_commandNotFound(t *testing.T) {
	_, err := Output(context.Background(), "nonexistent-cmd-xyz-cmdutil")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-cmd-xyz-cmdutil")
}

func TestOutput_stderrInError(t *testing.T) {
	_, err := Output(context.Background(), "sh", "-c", "echo myerr >&2; exit 1")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "stderr:")
	assert.Contains(t, msg, "myerr")
}

func TestOutput_stderrErrorFormat(t *testing.T) {
	_, err := Output(context.Background(), "sh", "-c", "echo myerr >&2; exit 2")
	require.Error(t, err)
	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "sh"), "error should start with the command name")
}

func TestOutput_stderrOnSuccess(t *testing.T) {
	// stderr is logged but must not appear in the error or corrupt stdout
	out, err := Output(context.Background(), "sh", "-c", "echo stdout; echo noise >&2")
	require.NoError(t, err)
	assert.Equal(t, "stdout\n", string(out))
}

func TestOutput_cancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := Output(ctx, "sleep", "10")
	require.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "sleep 10")
	assert.Contains(t, err.Error(), "context canceled")
}

func TestRun_success(t *testing.T) {
	err := Run(context.Background(), "true")
	require.NoError(t, err)
}

func TestRun_commandNotFound(t *testing.T) {
	err := Run(context.Background(), "nonexistent-cmd-xyz-cmdutil")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-cmd-xyz-cmdutil")
}

func TestRun_stderrInError(t *testing.T) {
	err := Run(context.Background(), "sh", "-c", "echo runerr >&2; exit 1")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "stderr:")
	assert.Contains(t, msg, "runerr")
}

func TestRun_noStderrInErrorWhenClean(t *testing.T) {
	err := Run(context.Background(), "sh", "-c", "exit 1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "stderr:")
}

func TestRun_cancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, "sleep", "10")
	require.Error(t, err)
}
