package shutdown

import (
	"context"
	"encoding/json"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// stopRetryInterval is the pause between StopLoop attempts.
const stopRetryInterval = 3 * time.Second

// StopLoop issues `timeout 180 weka local stop [--force|-g]` until the container is
// no longer running, pausing three seconds between attempts.
//
// TODO: the 180s `timeout` wrapper is the only bound on a stuck stop attempt; there is no
// SIGKILL escalation if the container process ignores it, so a hung stop retries forever.
func StopLoop(ctx context.Context, runner process.CommandRunner, c clock.Clock, name string, force bool) error {
	flag := "-g"
	if force {
		flag = "--force"
	}
	for isContainerRunning(ctx, runner, name, force) {
		if _, err := runner.Run(ctx, process.Command{
			Path: "timeout",
			Args: []string{"180", "weka", "local", "stop", flag},
			Log:  process.LogAll,
		}); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if err := clock.Sleep(ctx, c, stopRetryInterval); err != nil {
			return err
		}
	}
	return nil
}

// isContainerRunning reports run status; an agent-query failure means "still running" for a
// graceful loop and "not running" for an initially forced loop.
func isContainerRunning(ctx context.Context, runner process.CommandRunner, name string, forced bool) bool {
	res, err := runner.Run(ctx, process.Command{
		Path:   "weka",
		Args:   []string{"local", "ps", "--json"},
		Output: process.Capture,
		Log:    process.LogExecution,
	})
	if err != nil {
		return !forced
	}
	var containers []map[string]interface{}
	if json.Unmarshal(res.Stdout, &containers) != nil {
		return !forced
	}
	for _, c := range containers {
		cName, ok := c["name"].(string)
		if !ok || cName != name {
			continue
		}
		status, ok := c["runStatus"].(string)
		return !ok || status != "Stopped"
	}
	return false
}

// ForceWatchInput carries the force watcher's dependencies.
type ForceWatchInput struct {
	Runner process.CommandRunner
	Clock  clock.Clock
	Paths  paths.Roots
	Name   string
	PodID  string
	BootID string
}

// WatchForce polls for allow_force_stop during a graceful stop and issues one force command
// when it fires. It is owned and joined by the caller; it never spawns a detached goroutine.
func WatchForce(ctx context.Context, in ForceWatchInput) error { //nolint:gocritic // value semantics preferred over pointer churn for this cold-path config struct
	_, logger := instrumentation.CreateLogSpan(ctx, "shutdown.WatchForce")
	defer logger.End()
	for {
		if ReadInstructions(in.Paths, in.PodID, in.BootID).AllowForceStop {
			logger.Info("received allow-force-stop instruction, escalating to force stop")
			return ForceStop(ctx, in.Runner, in.Name)
		}
		if err := clock.Sleep(ctx, in.Clock, approvalPollInterval); err != nil {
			return nil // caller cancelling us (stop loop finished) is expected, not an error
		}
	}
}

// ForceStop issues a single unconditional force stop, used by the takeover path.
func ForceStop(ctx context.Context, runner process.CommandRunner, name string) error {
	_, err := runner.Run(ctx, process.Command{
		Path: "weka",
		Args: []string{"local", "stop", "--force"},
		Log:  process.LogAll,
	})
	return err
}
