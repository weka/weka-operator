package modes

import (
	"context"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/resources"
	"github.com/weka/weka-operator/internal/runtime/shutdown"
)

func TestUpdateCfgFromResources(t *testing.T) {
	t.Run("vf net devices override existing device", func(t *testing.T) {
		cfg := &config.Config{Mode: "compute", NetworkDevice: "eth0"}
		res := &resources.NodeResources{NetDevices: []string{"vf_eth1", "vf_eth2"}}
		updateCfgFromResources(cfg, res)
		if cfg.NetworkDevice != "vf_eth1,vf_eth2" {
			t.Errorf("NetworkDevice = %q, want %q", cfg.NetworkDevice, "vf_eth1,vf_eth2")
		}
	})

	t.Run("non-vf net devices do not override", func(t *testing.T) {
		cfg := &config.Config{NetworkDevice: "eth0"}
		res := &resources.NodeResources{NetDevices: []string{"eth1"}}
		updateCfgFromResources(cfg, res)
		if cfg.NetworkDevice != "eth0" {
			t.Errorf("NetworkDevice = %q, want %q", cfg.NetworkDevice, "eth0")
		}
	})

	t.Run("weka port set for compute", func(t *testing.T) {
		cfg := &config.Config{Mode: "compute", Port: 0}
		res := &resources.NodeResources{WekaPort: 14000}
		updateCfgFromResources(cfg, res)
		if cfg.Port != 14000 {
			t.Errorf("Port = %d, want 14000", cfg.Port)
		}
	})

	t.Run("telemetry mode keeps ports zero", func(t *testing.T) {
		cfg := &config.Config{Mode: "telemetry", Port: 0, AgentPort: 0}
		res := &resources.NodeResources{WekaPort: 14000, AgentPort: 15000}
		updateCfgFromResources(cfg, res)
		if cfg.Port != 0 {
			t.Errorf("Port = %d, want 0", cfg.Port)
		}
		if cfg.AgentPort != 0 {
			t.Errorf("AgentPort = %d, want 0", cfg.AgentPort)
		}
	})

	t.Run("envoy mode keeps weka port zero", func(t *testing.T) {
		cfg := &config.Config{Mode: "envoy", Port: 0}
		res := &resources.NodeResources{WekaPort: 14000}
		updateCfgFromResources(cfg, res)
		if cfg.Port != 0 {
			t.Errorf("Port = %d, want 0", cfg.Port)
		}
	})

	t.Run("failure domain and machine identifier override when non-empty", func(t *testing.T) {
		cfg := &config.Config{Mode: "compute", FailureDomain: "old-fd", MachineIdentifier: "old-mid"}
		res := &resources.NodeResources{FailureDomain: "new-fd", MachineIdentifier: "new-mid"}
		updateCfgFromResources(cfg, res)
		if cfg.FailureDomain != "new-fd" {
			t.Errorf("FailureDomain = %q, want %q", cfg.FailureDomain, "new-fd")
		}
		if cfg.MachineIdentifier != "new-mid" {
			t.Errorf("MachineIdentifier = %q, want %q", cfg.MachineIdentifier, "new-mid")
		}
	})

	t.Run("empty failure domain and machine identifier do not override", func(t *testing.T) {
		cfg := &config.Config{Mode: "compute", FailureDomain: "old-fd", MachineIdentifier: "old-mid"}
		res := &resources.NodeResources{}
		updateCfgFromResources(cfg, res)
		if cfg.FailureDomain != "old-fd" {
			t.Errorf("FailureDomain = %q, want %q", cfg.FailureDomain, "old-fd")
		}
		if cfg.MachineIdentifier != "old-mid" {
			t.Errorf("MachineIdentifier = %q, want %q", cfg.MachineIdentifier, "old-mid")
		}
	})
}

// TestComputeStopFlag pins the Python force_stop decision at weka_runtime.py:4544–4551.
// client always force-stops; the graceful-eligible modes use "-g" only when neither
// allow_force_stop nor a generation mismatch is set.
func TestComputeStopFlag(t *testing.T) {
	modes := []string{"client", "s3", "nfs", "smbw", "drive", "compute", "data-services"}
	for _, mode := range modes {
		for _, allowForceStop := range []bool{false, true} {
			for _, genMismatch := range []bool{false, true} {
				// Graceful "-g" is reachable only by graceful-eligible modes, and only when
				// neither force signal is present. client is not graceful-eligible.
				wantGraceful := gracefulEligibleModes[mode] && !allowForceStop && !genMismatch
				want := "--force"
				if wantGraceful {
					want = "-g"
				}
				got := computeStopFlag(mode, allowForceStop, genMismatch)
				if got != want {
					t.Errorf("computeStopFlag(%q, allowForceStop=%v, genMismatch=%v) = %q, want %q",
						mode, allowForceStop, genMismatch, got, want)
				}
			}
		}
	}

	// Explicit spot-checks of the most load-bearing cases.
	if got := computeStopFlag("client", false, false); got != "--force" {
		t.Errorf("client with no force signals = %q, want --force (client always force-stops)", got)
	}
	if got := computeStopFlag("compute", false, false); got != "-g" {
		t.Errorf("compute with no force signals = %q, want -g", got)
	}
	if got := computeStopFlag("data-services", false, false); got != "-g" {
		t.Errorf("data-services default = %q, want -g", got)
	}
	if got := computeStopFlag("compute", false, true); got != "--force" {
		t.Errorf("compute on generation mismatch = %q, want --force", got)
	}
}

func TestWatchForceStop_Escalates(t *testing.T) {
	origGet := getShutdownInstructionsFn
	origForce := forceStopFn
	defer func() {
		getShutdownInstructionsFn = origGet
		forceStopFn = origForce
	}()

	getShutdownInstructionsFn = func(_, _ string) *shutdown.ShutdownInstructions {
		return &shutdown.ShutdownInstructions{AllowForceStop: true}
	}
	forced := make(chan *config.Config, 1)
	forceStopFn = func(cfg *config.Config) error {
		forced <- cfg
		return nil
	}

	done := make(chan struct{})
	go func() {
		watchForceStop(context.Background(), &config.Config{Name: "c0"}, "boot")
		close(done)
	}()

	select {
	case cfg := <-forced:
		if cfg.Name != "c0" {
			t.Errorf("forceStopFn called with Name = %q, want %q", cfg.Name, "c0")
		}
	case <-time.After(time.Second):
		t.Fatal("forceStopFn not called within 1s")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchForceStop did not return within 1s after escalating")
	}
}

func TestWatchForceStop_ExitsOnCtxCancel(t *testing.T) {
	origGet := getShutdownInstructionsFn
	origForce := forceStopFn
	origInterval := forceStopPollInterval
	defer func() {
		getShutdownInstructionsFn = origGet
		forceStopFn = origForce
		forceStopPollInterval = origInterval
	}()

	forceStopPollInterval = time.Millisecond
	getShutdownInstructionsFn = func(_, _ string) *shutdown.ShutdownInstructions {
		return &shutdown.ShutdownInstructions{} // no force stop
	}
	var called bool
	forceStopFn = func(_ *config.Config) error {
		called = true
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	done := make(chan struct{})
	go func() {
		watchForceStop(ctx, &config.Config{Name: "c0"}, "boot")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchForceStop did not return promptly on cancelled ctx")
	}
	if called {
		t.Error("forceStopFn should not be called when there is no force-stop instruction")
	}
}
