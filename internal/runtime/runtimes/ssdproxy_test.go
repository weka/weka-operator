package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/lifecycle"
	"github.com/weka/weka-operator/internal/runtime/paths"
	"github.com/weka/weka-operator/internal/runtime/process"
)

func TestRunSSDProxy_RequiresMemory(t *testing.T) {
	rt := newSSDProxy(&config.ContainerConfig{}, &Deps{})
	if err := rt.Start(context.Background()); err == nil {
		t.Fatal("ssdProxyRuntime.Start() with empty MEMORY: want error, got nil")
	}
}

// specFixture writes a minimal *.spec release file under <optWeka>/dist/release so
// weka.ReadReleaseSpec succeeds.
func specFixture(t *testing.T, optWeka string) {
	t.Helper()
	dir := filepath.Join(optWeka, "dist", "release")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `{"version":"4.4.0","feature_flags":""}`
	if err := os.WriteFile(filepath.Join(dir, "release.spec"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testRoots builds a paths.Roots rooted entirely under t.TempDir(), with the release spec
// fixture in place and an empty Sys dir so weka.CheckIOMMUCompatible no-ops.
func testRoots(t *testing.T) paths.Roots {
	t.Helper()
	root := t.TempDir()
	optWeka := filepath.Join(root, "opt-weka")
	specFixture(t, optWeka)
	sys := filepath.Join(root, "sys")
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	return paths.Roots{
		OptWeka:    optWeka,
		K8sRuntime: filepath.Join(optWeka, "k8s-runtime"),
		HostBinds:  filepath.Join(root, "host-binds"),
		Proc:       filepath.Join(root, "proc"),
		Sys:        sys,
		Tmp:        filepath.Join(root, "tmp"),
	}
}

// testCoordinator builds a real *lifecycle.Coordinator backed by a real process.Manager,
// matching the construction used by lifecycle's own tests.
func testCoordinator(ctx context.Context) *lifecycle.Coordinator {
	return lifecycle.New(ctx, process.NewManager(context.Background()))
}

// TestSSDProxyRuntime_Start_Succeeds exercises the full happy path against fakes: the
// generation lock (Linux-only abstract-namespace unix socket) requires skipping elsewhere.
func TestSSDProxyRuntime_Start_Succeeds(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("generation.AcquireLock uses abstract-namespace unix sockets, Linux-only")
	}

	runner := &recordingRunner{respond: func(c process.Command) (process.Result, bool, error) {
		script := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(script, "local ps --json"):
			return process.Result{Stdout: []byte("[]")}, true, nil
		case strings.Contains(script, "local resources") && strings.Contains(script, "--json"):
			return process.Result{Stdout: []byte("{}")}, true, nil
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
		Identity: config.Identity{Name: "ssdproxy-0"},
		Memory:   config.Memory{Request: "1GiB"},
	}

	rt := newSSDProxy(cfg, deps)
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("ssdProxyRuntime.Start() error = %v", err)
	}
}

// TestSSDProxyRuntime_Start_IOMMUIncompatible_FailsBeforeVersionSet verifies the IOMMU check
// runs (and fails) before ForceSetWekaVersion issues its "weka version set" command: on an
// IOMMU-enabled host without SsdProxyIommuSupport, Start must error out having never attempted
// to pin a Weka version.
func TestSSDProxyRuntime_Start_IOMMUIncompatible_FailsBeforeVersionSet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("generation.AcquireLock uses abstract-namespace unix sockets, Linux-only")
	}

	roots := testRoots(t)
	iommuDir := filepath.Join(roots.Sys, "kernel", "iommu_groups", "0")
	if err := os.MkdirAll(iommuDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{respond: func(c process.Command) (process.Result, bool, error) {
		script := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(script, "local ps --json"):
			return process.Result{Stdout: []byte("[]")}, true, nil
		case strings.Contains(script, "local resources") && strings.Contains(script, "--json"):
			return process.Result{Stdout: []byte("{}")}, true, nil
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
		Paths:     roots,
		Coord:     testCoordinator(ctx),
	}
	cfg := &config.ContainerConfig{
		Identity: config.Identity{Name: "ssdproxy-0"},
		Memory:   config.Memory{Request: "1GiB"},
	}

	rt := newSSDProxy(cfg, deps)
	if err := rt.Start(ctx); err == nil {
		t.Fatal("ssdProxyRuntime.Start() with IOMMU groups present and no SsdProxyIommuSupport: want error, got nil")
	}

	for _, c := range runner.commandsSnapshot() {
		if strings.Contains(strings.Join(c.Args, " "), "version set") {
			t.Errorf("command %q: ForceSetWekaVersion must not run after a failed IOMMU check", strings.Join(c.Args, " "))
		}
	}
}
