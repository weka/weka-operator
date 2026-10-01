package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// newTestDeps builds a Deps wired to a recordingRunner/fakeLauncher pair over a fresh
// testRoots(t), with the runner scripted to satisfy the "weka local ps --json", "weka
// local resources ... --json", and "ip route show default" (management IP discovery,
// since test configs use UDPMode with no explicit NetworkDevice) calls every
// ensureContainer/WriteManagementIPs path makes.
func newTestDeps(t *testing.T) (*Deps, *recordingRunner, *fakeLauncher) {
	t.Helper()
	runner := &recordingRunner{respond: func(c process.Command) (process.Result, bool, error) {
		script := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(script, "local ps --json"):
			return process.Result{Stdout: []byte("[]")}, true, nil
		case strings.Contains(script, "local resources") && strings.Contains(script, "--json"):
			return process.Result{Stdout: []byte("{}")}, true, nil
		case strings.Contains(script, "ip route show default"):
			return process.Result{Stdout: []byte("192.168.1.10")}, true, nil
		}
		return process.Result{}, false, nil
	}}
	launcher := &fakeLauncher{}
	ctx := context.Background()
	deps := &Deps{
		Runner:    runner,
		Processes: launcher,
		Clock:     clock.System,
		Paths:     testRoots(t),
		Coord:     testCoordinator(ctx),
	}
	return deps, runner, launcher
}

// writeResourcesFixture copies internal/runtime/testdata/resources.json into
// <K8sRuntime>/resources.json, unblocking resources.WaitAndLoad for modes that need
// operator resources.
func writeResourcesFixture(t *testing.T, k8sRuntimeDir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(k8sRuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k8sRuntimeDir, "resources.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// testBackendConfig builds a minimal, valid ContainerConfig for mode, avoiding real
// /proc and network reconciliation: explicit core IDs skip the /proc/1/status probe,
// and UDP mode skips ReconcileNetDevices.
func testBackendConfig(mode, name string) *config.ContainerConfig {
	return &config.ContainerConfig{
		Identity: config.Identity{Name: name},
		CPU:      config.CPU{Cores: 2, CoreIDs: config.CoreSelection{IDs: []int{0, 1}}},
		Memory:   config.Memory{Request: "1GiB"},
		Network:  config.Network{UDPMode: true},
		Ports:    config.Ports{Weka: 14000, Agent: 15000},
	}
}

// runStartWithTimeout runs Start in a goroutine and fails the test if it doesn't return
// within 10s, guarding against a hang leaving the test suite stuck.
func runStartWithTimeout(t *testing.T, ctx context.Context, start func(context.Context) error) error { //nolint:revive // ctx-after-t matches call-site readability here
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- start(ctx) }()
	select {
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Start() did not return within 10s")
		return nil
	}
}

func skipUnlessLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("generation.AcquireLock requires Linux abstract-namespace unix sockets")
	}
}

func TestBackendRuntime_Compute_WritesTelemetryOverride_NoDriveOps(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)
	traceDir := filepath.Join(deps.Paths.OptWeka, "external-mounts", "shared_boot_level", "audit-traces")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := testBackendConfig("compute", "compute-0")
	rt := newBackend("compute", cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(traceDir, "override.config.json")); err != nil {
		t.Errorf("telemetry override not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deps.Paths.K8sRuntime, "drives.json")); !os.IsNotExist(err) {
		t.Errorf("compute mode must not write drives.json, stat err = %v", err)
	}
	for _, c := range runner.commands {
		if strings.Contains(strings.Join(c.Args, " "), "local rm") {
			t.Errorf("backend mode issued %q, want no local rm", strings.Join(c.Args, " "))
		}
	}
}

func TestBackendRuntime_Drive_EnsuresDrives(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)

	cfg := testBackendConfig("drive", "drive-0")
	rt := newBackend("drive", cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(deps.Paths.K8sRuntime, "drives.json")); err != nil {
		t.Errorf("drives.json not written: %v", err)
	}
	for _, c := range runner.commands {
		if strings.Contains(strings.Join(c.Args, " "), "local rm") {
			t.Errorf("backend mode issued %q, want no local rm", strings.Join(c.Args, " "))
		}
	}
}

func TestBackendRuntime_S3_SetsAllowProtocols(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)

	cfg := testBackendConfig("s3", "s3-0")
	rt := newBackend("s3", cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	assertResourceDocAllowProtocols(t, deps.Paths.OptWeka, "s3-0", true)
	for _, c := range runner.commands {
		script := strings.Join(c.Args, " ")
		if strings.Contains(script, "local setup container") && strings.Contains(script, "--allow-mix-setting") {
			t.Errorf("s3 setup command must not contain --allow-mix-setting: %q", script)
		}
	}
}

func TestBackendRuntime_DataServices_SetsAllowMixSetting(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)

	cfg := testBackendConfig("data-services", "ds-0")
	rt := newBackend("data-services", cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	assertResourceDocAllowProtocols(t, deps.Paths.OptWeka, "ds-0", false)
	found := false
	for _, c := range runner.commands {
		script := strings.Join(c.Args, " ")
		if strings.Contains(script, "local setup container") && strings.Contains(script, "--allow-mix-setting") {
			found = true
		}
	}
	if !found {
		t.Error("data-services setup command must contain --allow-mix-setting")
	}
}

// assertResourceDocAllowProtocols finds the single weka-resources.*.json written under
// <optWeka>/data/<name>/container and checks its top-level allow_protocols field.
func assertResourceDocAllowProtocols(t *testing.T, optWeka, name string, want bool) {
	t.Helper()
	dir := filepath.Join(optWeka, "data", name, "container")
	matches, err := filepath.Glob(filepath.Join(dir, "weka-resources.*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("glob %s/weka-resources.*.json: matches=%v err=%v", dir, matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	got, _ := doc["allow_protocols"].(bool)
	if got != want {
		t.Errorf("allow_protocols = %v, want %v (doc: %s)", doc["allow_protocols"], want, data)
	}
}
