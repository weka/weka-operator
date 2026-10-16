package runtimes

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// TestDriverDistRuntime_Start_Succeeds exercises the full happy path: persistence, agent
// bring-up, stem container setup, trace config, and the supervisorctl RUNNING poll, all
// against fakes/temp roots.
func TestDriverDistRuntime_Start_Succeeds(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("generation.AcquireLock uses abstract-namespace unix sockets, Linux-only")
	}

	runner := &recordingRunner{respond: func(c process.Command) (process.Result, bool, error) {
		script := ""
		if len(c.Args) > 0 {
			script = strings.Join(c.Args, " ")
		}
		switch {
		case strings.Contains(script, "local ps --json"):
			return process.Result{Stdout: []byte("[]")}, true, nil
		case strings.Contains(script, "local resources") && strings.Contains(script, "--json"):
			return process.Result{Stdout: []byte("{}")}, true, nil
		case strings.Contains(script, "supervisorctl status"):
			return process.Result{Stdout: []byte("dist RUNNING")}, true, nil
		}
		return process.Result{}, false, nil
	}}
	launcher := &fakeLauncher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := &Deps{
		Runner:    runner,
		Processes: launcher,
		Clock:     clock.System,
		Paths:     testRoots(t),
		Coord:     testCoordinator(ctx),
	}
	cfg := &config.ContainerConfig{
		Identity: config.Identity{Name: "dist-0"},
	}

	rt := newDriverDist(cfg, deps)
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("driverDistRuntime.Start() error = %v", err)
	}
}

// TestDriverDistRuntime_Shutdown_UsesDistContainerName verifies Shutdown's running-check and
// stop use the fixed "dist" container name, not cfg.Identity.Name. The fake "weka local ps"
// reports the identity-named entry as Running and "dist" as Stopped; if Shutdown filtered on
// the identity name it would treat the container as still running and issue a stop.
func TestDriverDistRuntime_Shutdown_UsesDistContainerName(t *testing.T) {
	runner := &recordingRunner{respond: func(c process.Command) (process.Result, bool, error) {
		if c.Path == "weka" && len(c.Args) >= 2 && c.Args[0] == "local" && c.Args[1] == "ps" {
			return process.Result{Stdout: []byte(
				`[{"name":"dist-0","runStatus":"Running"},{"name":"dist","runStatus":"Stopped"}]`,
			)}, true, nil
		}
		return process.Result{}, false, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := &Deps{Runner: runner, Clock: clock.System, Coord: testCoordinator(ctx)}
	cfg := &config.ContainerConfig{Identity: config.Identity{Name: "dist-0", PodID: "pod-1"}}
	rt := newDriverDist(cfg, deps)
	rt.agentLaunched = true

	if err := rt.Shutdown(ctx); err != nil {
		t.Fatalf("driverDistRuntime.Shutdown() error = %v", err)
	}

	for _, c := range runner.commandsSnapshot() {
		full := c.Path + " " + strings.Join(c.Args, " ")
		if strings.Contains(full, "stop") {
			t.Errorf("unexpected stop command %q: \"dist\" is reported Stopped, so shutdown must not treat cfg.Identity.Name as the running container", full)
		}
	}
}
