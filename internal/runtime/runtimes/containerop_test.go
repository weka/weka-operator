package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// TestContainerOpRuntime_Start_UsesAdhocContainerName verifies containerOpRuntime always
// operates against the hardcoded "adhoc" stem container name, regardless of cfg.Identity.Name.
func TestContainerOpRuntime_Start_UsesAdhocContainerName(t *testing.T) {
	runner := &recordingRunner{}
	launcher := &fakeLauncher{}
	resultsPath := filepath.Join(t.TempDir(), "results.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := &Deps{
		Runner:    runner,
		Processes: launcher,
		Clock:     clock.System,
		Paths:     testRoots(t),
		Coord:     testCoordinator(ctx),
	}
	cfg := &config.ContainerOpConfig{
		ContainerConfig: config.ContainerConfig{
			Identity: config.Identity{Name: "my-pod-identity"},
			Results:  config.Results{Path: resultsPath},
		},
		Operation: config.Operation{Raw: "{}", Type: "feature-flags-update"},
	}

	rt := newContainerOp(cfg, deps)
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("containerOpRuntime.Start() error = %v", err)
	}

	// Every "weka local ..." command issued must reference the "adhoc" containerName
	// constant, never cfg.Identity.Name.
	for _, c := range runner.commands {
		full := c.Path + " " + strings.Join(c.Args, " ")
		if strings.Contains(full, "--container") && !strings.Contains(full, "adhoc") {
			t.Errorf("command %q: want container name %q, not identity name", full, "adhoc")
		}
		if strings.Contains(full, cfg.Identity.Name) {
			t.Errorf("command %q: unexpectedly references cfg.Identity.Name %q", full, cfg.Identity.Name)
		}
	}
	// startAgent launches its own "syslog"/"agent" daemons under cfg.Identity.Name-
	// independent names; only the stem container's StartProcess call must use "adhoc".
	foundStemLaunch := false
	for _, call := range launcher.calls {
		if call.kind == "process" && call.name == "adhoc" {
			foundStemLaunch = true
		}
		if call.name == cfg.Identity.Name {
			t.Errorf("launch call name = %q: unexpectedly used cfg.Identity.Name", call.name)
		}
	}
	if !foundStemLaunch {
		t.Error("expected a StartProcess launch call named \"adhoc\" for the stem container, found none")
	}

	// cfg.Identity.Name must still be preserved on the runtime (used for PodID/logging elsewhere,
	// not for the container name Shutdown operates on).
	if rt.cfg.Identity.Name != "my-pod-identity" {
		t.Errorf("cfg.Identity.Name = %q, want %q", rt.cfg.Identity.Name, "my-pod-identity")
	}

	data, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	var res map[string]interface{}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
}

// TestContainerOpRuntime_Shutdown_UsesAdhocContainerName verifies Shutdown's running-check and
// stop use the fixed "adhoc" container name, not cfg.Identity.Name. The fake "weka local ps"
// reports the identity-named entry as Running and "adhoc" as Stopped; if Shutdown filtered on
// the identity name it would treat the container as still running and issue a stop.
func TestContainerOpRuntime_Shutdown_UsesAdhocContainerName(t *testing.T) {
	runner := &recordingRunner{respond: func(c process.Command) (process.Result, bool, error) {
		if c.Path == "weka" && len(c.Args) >= 2 && c.Args[0] == "local" && c.Args[1] == "ps" {
			return process.Result{Stdout: []byte(
				`[{"name":"my-pod-identity","runStatus":"Running"},{"name":"adhoc","runStatus":"Stopped"}]`,
			)}, true, nil
		}
		return process.Result{}, false, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := &Deps{Runner: runner, Clock: clock.System, Coord: testCoordinator(ctx)}
	cfg := &config.ContainerOpConfig{
		ContainerConfig: config.ContainerConfig{Identity: config.Identity{Name: "my-pod-identity", PodID: "pod-1"}},
	}
	rt := newContainerOp(cfg, deps)
	rt.agentLaunched = true

	if err := rt.Shutdown(ctx); err != nil {
		t.Fatalf("containerOpRuntime.Shutdown() error = %v", err)
	}

	for _, c := range runner.commandsSnapshot() {
		full := c.Path + " " + strings.Join(c.Args, " ")
		if strings.Contains(full, "stop") {
			t.Errorf("unexpected stop command %q: \"adhoc\" is reported Stopped, so shutdown must not treat cfg.Identity.Name as the running container", full)
		}
	}
}
