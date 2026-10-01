package runtimes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/process"
)

// testClientConfig builds a minimal, valid ClientConfig, reusing the same real-filesystem
// hazard avoidance as testBackendConfig (explicit CoreIDs, UDP mode), plus non-zero ports so
// ports.AllocateClient just persists them instead of probing /proc/net/tcp.
func testClientConfig(name string) *config.ClientConfig {
	return &config.ClientConfig{
		ContainerConfig: config.ContainerConfig{
			Identity: config.Identity{Name: name},
			CPU:      config.CPU{Cores: 2, CoreIDs: config.CoreSelection{IDs: []int{0, 1}}},
			Memory:   config.Memory{Request: "1GiB"},
			Network:  config.Network{UDPMode: true},
			Ports:    config.Ports{Weka: 14000, Agent: 15000},
		},
	}
}

func TestClientRuntime_AllocatesAndPersistsPorts(t *testing.T) {
	skipUnlessLinux(t)
	deps, _, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)

	cfg := testClientConfig("client-0")
	rt := newClient(cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	port, err := os.ReadFile(filepath.Join(deps.Paths.K8sRuntime, "vars", "port"))
	if err != nil {
		t.Fatalf("vars/port not written: %v", err)
	}
	if string(port) != "14000" {
		t.Errorf("vars/port = %q, want %q", port, "14000")
	}
	agentPort, err := os.ReadFile(filepath.Join(deps.Paths.K8sRuntime, "vars", "agent_port"))
	if err != nil {
		t.Fatalf("vars/agent_port not written: %v", err)
	}
	if string(agentPort) != "15000" {
		t.Errorf("vars/agent_port = %q, want %q", agentPort, "15000")
	}
}

// TestClientRuntime_NoRmWhenBasePortMatches pins the deliberate deviation in
// shouldRecreateClientContainer: with base_port and restricted_client already matching the
// requested port/image, the container must not be torn down and recreated.
func TestClientRuntime_NoRmWhenBasePortMatches(t *testing.T) {
	skipUnlessLinux(t)
	deps, runner, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)
	runner.respond = func(c process.Command) (process.Result, bool, error) {
		script := strings.Join(c.Args, " ")
		switch {
		case strings.Contains(script, "local ps --json"):
			return process.Result{Stdout: []byte("[]")}, true, nil
		case strings.Contains(script, "local resources") && strings.Contains(script, "--json"):
			// ImageName is "" in testClientConfig, so expectedRestricted = true.
			return process.Result{Stdout: []byte(`{"base_port":14000,"restricted_client":true}`)}, true, nil
		case strings.Contains(script, "ip route show default"):
			return process.Result{Stdout: []byte("192.168.1.10")}, true, nil
		}
		return process.Result{}, false, nil
	}

	cfg := testClientConfig("client-1")
	rt := newClient(cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	for _, c := range runner.commands {
		if strings.Contains(strings.Join(c.Args, " "), "local rm") {
			t.Errorf("issued %q, want no local rm when base_port and restricted_client already match", strings.Join(c.Args, " "))
		}
	}
}

func TestClientRuntime_FrontendDisconnect_NoInterfaceFile_ReturnsImmediately(t *testing.T) {
	skipUnlessLinux(t)
	deps, _, _ := newTestDeps(t)
	writeResourcesFixture(t, deps.Paths.K8sRuntime)
	// deps.Paths.Proc is a fresh temp dir with no wekafs/interface file: WaitFrontendDisconnect
	// must treat that as "already disconnected" and return nil instantly rather than blocking.
	if _, err := os.Stat(filepath.Join(deps.Paths.Proc, "wekafs", "interface")); !os.IsNotExist(err) {
		t.Fatalf("test setup: expected no wekafs/interface file, stat err = %v", err)
	}

	cfg := testClientConfig("client-2")
	rt := newClient(cfg, deps)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runStartWithTimeout(t, ctx, rt.Start); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}
