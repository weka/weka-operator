package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
)

func testAuxiliaryConfig(name string) *config.ContainerConfig {
	return &config.ContainerConfig{
		Identity: config.Identity{Name: name},
		CPU:      config.CPU{Cores: 2, CoreIDs: config.CoreSelection{IDs: []int{0, 1}}},
		Memory:   config.Memory{Request: "1GiB"},
		Network:  config.Network{UDPMode: true},
		Ports:    config.Ports{Weka: 14000, Agent: 15000},
	}
}

// findManagedContainerSetupCommand returns the "weka local setup ..." command issued for
// name, or "" if none was recorded.
func findManagedContainerSetupCommand(runner *recordingRunner, name string) string {
	for _, c := range runner.commands {
		script := strings.Join(c.Args, " ")
		if strings.Contains(script, "local setup") && strings.Contains(script, name) {
			return script
		}
	}
	return ""
}

// indexOfCommand returns the position of the first command whose args contain substr, or -1.
func indexOfCommand(runner *recordingRunner, substr string) int {
	for i, c := range runner.commands {
		if strings.Contains(strings.Join(c.Args, " "), substr) {
			return i
		}
	}
	return -1
}

func TestAuxiliaryRuntime_Envoy_SetupFlags(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)

	cfg := testAuxiliaryConfig("envoy-0")
	rt := newAuxiliary("envoy", cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	script := findManagedContainerSetupCommand(runner, "envoy")
	if script == "" {
		t.Fatal("no envoy setup command recorded")
	}
	if !strings.Contains(script, "--no-start") || !strings.Contains(script, "--disable") {
		t.Errorf("envoy setup command = %q, want --no-start and --disable", script)
	}
	if strings.Contains(script, "--not-dependent") {
		t.Errorf("envoy setup command = %q, want no --not-dependent", script)
	}

	if indexOfCommand(runner, "local start") == -1 {
		t.Error("no \"weka local start\" command recorded after envoy setup")
	}
}

func TestAuxiliaryRuntime_Telemetry_SetupFlagsAndOverride(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)
	traceDir := filepath.Join(deps.Paths.OptWeka, "external-mounts", "shared_boot_level", "audit-traces")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := testAuxiliaryConfig("telemetry-0")
	rt := newAuxiliary("telemetry", cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	script := findManagedContainerSetupCommand(runner, "telemetry")
	if script == "" {
		t.Fatal("no telemetry setup command recorded")
	}
	for _, flag := range []string{"--not-dependent", "--no-start", "--disable"} {
		if !strings.Contains(script, flag) {
			t.Errorf("telemetry setup command = %q, want to contain %q", script, flag)
		}
	}

	setupIdx := indexOfCommand(runner, "local setup")
	startIdx := indexOfCommand(runner, "local start")
	if startIdx == -1 {
		t.Error("no \"weka local start\" command recorded after telemetry setup")
	} else if startIdx < setupIdx {
		t.Errorf("start command (index %d) ran before setup command (index %d)", startIdx, setupIdx)
	}

	if _, err := os.Stat(filepath.Join(traceDir, "override.config.json")); err != nil {
		t.Errorf("telemetry override not written: %v", err)
	}
}
