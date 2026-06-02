// lifecycle.go provides shared mode helpers used by compute, drive, and client modes.
package modes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/runtime/agent"
	"github.com/weka/weka-operator/internal/runtime/cmdutil"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/daemon"
	"github.com/weka/weka-operator/internal/runtime/generation"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/resources"
	"github.com/weka/weka-operator/internal/runtime/shutdown"
	"github.com/weka/weka-operator/internal/runtime/syslog"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// forceStopPollInterval is the poll cadence for the allow_force_stop watcher.
// Matches the shutdown-instruction poll interval in shutdown.go.
// A var (not const) so tests can lower it.
var forceStopPollInterval = 5 * time.Second

// Test seams for the force-stop watcher; default to real implementations.
var (
	getShutdownInstructionsFn = shutdown.GetShutdownInstructions
	forceStopFn               = func(cfg *config.Config) error {
		return cmdutil.Run(context.Background(), "sh", "-c",
			fmt.Sprintf("weka local stop %s --force", cfg.Name))
	}
)

// loadResources waits for node resources to become available, loads them, and
// populates cfg with the resource values.
func loadResources(ctx context.Context, cfg *config.Config) (*resources.NodeResources, error) {
	bootID := shutdown.GetBootID()
	abort := func() bool {
		return shutdown.GetShutdownInstructions(cfg.PodID, bootID).AllowStop
	}
	res, err := resources.WaitAndLoad(ctx, abort)
	if err != nil {
		return nil, err
	}
	updateCfgFromResources(cfg, res)
	return res, nil
}

// runGenerationAndLock writes the generation file and acquires the generation lock.
// The caller must defer lock.Close().
//
// L2 ordering note: Python order is write_generation → write_management_ips → obtain_lock
// (weka_runtime.py ~:4140-4143). Go bundles write+lock here and mode files call
// WriteManagementIPs before runGenerationAndLock, giving order:
//
//	write_management_ips → write_generation → obtain_lock.
//
// Both orderings are pre-agent and functionally equivalent. Splitting runGenerationAndLock
// across 7 mode files is invasive for a low-priority ordering difference; left as-is.
func runGenerationAndLock(ctx context.Context, cfg *config.Config) (io.Closer, error) {
	if err := generation.Write(ctx, cfg); err != nil {
		return nil, err
	}
	lock, err := generation.ObtainLock(cfg.Name)
	if err != nil {
		return nil, err
	}
	return lock, nil
}

// runAgent configures the weka agent, starts the agent supervisor, and waits until ready.
//
// L3 ordering note: Python order is configure_agent → start_syslog → override_dependencies_flag →
// ensure_drivers → start_agent. Mode files call agent.EnsureDrivers BEFORE runAgent, meaning
// driver-detection logs are not forwarded via syslog. Reordering would require splitting runAgent
// across 7 mode files (invasive for a syslog-forwarding benefit only). Left unchanged — EnsureDrivers
// uses --without-agent and does not functionally depend on agent.Configure being done first.
func runAgent(ctx context.Context, cfg *config.Config) error {
	if err := agent.Configure(ctx, cfg, false); err != nil {
		return err
	}
	if err := agent.OverrideDependenciesFlag(ctx, cfg); err != nil {
		return err
	}
	startAgentSupervisor(ctx, cfg)
	return agent.AwaitReady(ctx, cfg)
}

// startAndVerifyContainer starts the weka container and verifies it is executing.
func startAndVerifyContainer(ctx context.Context, cfg *config.Config) error {
	if err := weka.StartContainer(ctx, cfg.Name); err != nil {
		return err
	}
	return weka.EnsureContainerExec(ctx, cfg.Name)
}

// watchForceStop mirrors Python watch_for_force_shutdown() at weka_runtime.py:4497.
// During a graceful stop it polls for allow_force_stop and escalates to a force stop.
func watchForceStop(ctx context.Context, cfg *config.Config, bootID string) {
	_, logger := instrumentation.CreateLogSpan(ctx, "modes.watchForceStop")
	defer logger.End()
	for {
		if getShutdownInstructionsFn(cfg.PodID, bootID).AllowForceStop {
			logger.Info("received allow-force-stop instruction, escalating to force stop")
			if err := forceStopFn(cfg); err != nil {
				logger.Warn("force stop command failed", "err", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(forceStopPollInterval):
		}
	}
}

// runShutdownLoop is shared by all backend/client modes that own a weka container
// (compute, drive, client, s3, nfs, smbw, data-services).
// It watches for generation mismatch or ctx cancellation, then orchestrates graceful/force stop.
// Mirrors Python shutdown() at weka_runtime.py:4524.
func runShutdownLoop(ctx context.Context, cfg *config.Config) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "modes.runShutdownLoop", "mode", cfg.Mode)
	defer logger.End()

	bootID := shutdown.GetBootID()

	// Watch for generation mismatch; cancel watchCtx when detected.
	// generationMismatch is atomic: the watcher goroutine may still be live (and may
	// write it) when the main goroutine reads it after a parent-ctx cancellation, so the
	// two accesses are otherwise unsynchronized.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	var generationMismatch atomic.Bool
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-t.C:
				if generation.IsWrongGeneration(cfg) {
					logger.Info("generation mismatch detected, initiating shutdown")
					generationMismatch.Store(true)
					watchCancel()
				}
			}
		}
	}()

	// Block until watchCtx is cancelled (generation mismatch or parent ctx done).
	<-watchCtx.Done()

	logger.Warn("shutdown initiated")

	// Determine stop flag, mirroring Python weka_runtime.py:4544–4551.
	//
	// Compute allow_force_stop via the same I/O Python does, then delegate the flag
	// choice to computeStopFlag (the pure mirror of the Python branch logic):
	//   - generation mismatch → force path; no instruction read.
	//   - data-services → non-blocking AllowForceStop check (no instruction wait).
	//   - instruction-gated modes → block on PollShutdownInstructions; allow_force_stop = !graceful.
	wrongGeneration := generationMismatch.Load()
	allowForceStop := false
	switch {
	case wrongGeneration:
		// Force path; instruction read skipped. allowForceStop is unused by computeStopFlag here.
	case cfg.Mode == "data-services":
		// Non-blocking read, matching Python data-services teardown.
		allowForceStop = getShutdownInstructionsFn(cfg.PodID, bootID).AllowForceStop
	case modesNeedShutdownInstruction[cfg.Mode]:
		// Blocks until the operator permits a stop; graceful=true → allow_stop, false → allow_force_stop.
		graceful := shutdown.PollShutdownInstructions(cfg.PodID, bootID)
		allowForceStop = !graceful
	}

	stopFlag := computeStopFlag(cfg.Mode, allowForceStop, wrongGeneration)
	forceStop := stopFlag == "--force"

	stopWatchCtx, stopWatchCancel := context.WithCancel(context.Background())
	defer stopWatchCancel()
	if !forceStop {
		go watchForceStop(stopWatchCtx, cfg, bootID)
	}

	for isContainerRunning(cfg.Name, forceStop) {
		if err := cmdutil.Run(context.Background(), "sh", "-c",
			fmt.Sprintf("timeout 180 weka local stop %s %s", cfg.Name, stopFlag)); err != nil {
			logger.Warn("weka local stop failed, will retry", "flag", stopFlag, "err", err)
		}
		time.Sleep(3 * time.Second)
	}
	stopWatchCancel()

	return nil
}

// updateCfgFromResources copies NodeResources fields into cfg.
// Mirrors the global assignments in Python wait_for_resources() at weka_runtime.py:3636–3643.
func updateCfgFromResources(cfg *config.Config, res *resources.NodeResources) {
	// Mirror Python: if parse_port(PORT)==0 and MODE not in ['envoy','telemetry']: PORT = data["wekaPort"]
	if cfg.Port == 0 && res.WekaPort != 0 && cfg.Mode != "envoy" && cfg.Mode != "telemetry" {
		cfg.Port = res.WekaPort
	}
	// Mirror Python: if parse_port(AGENT_PORT)==0 and MODE != 'telemetry': AGENT_PORT = data["agentPort"]
	if cfg.AgentPort == 0 && res.AgentPort != 0 && cfg.Mode != "telemetry" {
		cfg.AgentPort = res.AgentPort
	}
	if res.FailureDomain != "" {
		cfg.FailureDomain = res.FailureDomain
	}
	if res.MachineIdentifier != "" {
		cfg.MachineIdentifier = res.MachineIdentifier
	}
	// Mirror Python wait_for_resources (weka_runtime.py:3624-3626):
	//   net_devices = ",".join(data.get("netDevices", []))
	//   if net_devices and should_allocate_vf_per_ionode(net_devices):
	//       NETWORK_DEVICE = net_devices
	if len(res.NetDevices) > 0 {
		netDevices := strings.Join(res.NetDevices, ",")
		if network.ShouldAllocateVFPerIoNode(netDevices) {
			cfg.NetworkDevice = netDevices
		}
	}
	cfg.Drives = res.Drives
}

// modesNeedShutdownInstruction is the set of modes that must wait for the operator
// shutdown-instruction gate before stopping. Mirrors Python logic at weka_runtime.py:4350.
// data-services is intentionally absent: it stops without an instruction gate.
var modesNeedShutdownInstruction = map[string]bool{
	"client":  true,
	"s3":      true,
	"nfs":     true,
	"smbw":    true,
	"drive":   true,
	"compute": true,
}

// gracefulEligibleModes is the set of modes eligible for a graceful ("-g") stop.
// Verbatim list from weka_runtime.py:4549. Notably client is absent: it always force-stops.
var gracefulEligibleModes = map[string]bool{
	"s3":            true,
	"drive":         true,
	"compute":       true,
	"nfs":           true,
	"smbw":          true,
	"data-services": true,
}

// computeStopFlag returns the weka local stop flag ("--force" or "-g"),
// mirroring the force_stop decision in Python shutdown() at weka_runtime.py:4544–4551:
//
//	force_stop = False
//	if allow_force_stop:    force_stop = True
//	if wrong_generation:    force_stop = True
//	if MODE not in [...]:    force_stop = True   # client is absent → always force
//
// It is pure: the caller resolves allowForceStop (blocking/non-blocking instruction
// read) and generationMismatch beforehand.
func computeStopFlag(mode string, allowForceStop, generationMismatch bool) string {
	forceStop := allowForceStop || generationMismatch || !gracefulEligibleModes[mode]
	if forceStop {
		return "--force"
	}
	return "-g"
}

// isContainerRunning checks weka local ps for the named container's run status.
// If noAgentAsNotRunning=true, treats an agent error as "not running".
// Mirrors Python is_container_running() at weka_runtime.py:4506.
func isContainerRunning(name string, noAgentAsNotRunning bool) bool {
	out, err := cmdutil.Output(context.Background(), "weka", "local", "ps", "--json")
	if err != nil {
		return !noAgentAsNotRunning
	}
	var containers []map[string]interface{}
	if err := json.Unmarshal(out, &containers); err != nil {
		return !noAgentAsNotRunning
	}
	for _, c := range containers {
		cName, ok := c["name"].(string)
		if !ok || cName != name {
			continue
		}
		status, ok := c["runStatus"].(string)
		if ok && status == "Stopped" {
			return false
		}
		return true
	}
	return false
}

// startAgentSupervisor creates a Supervisor with syslog and agent processes and starts it.
func startAgentSupervisor(ctx context.Context, cfg *config.Config) {
	sup := daemon.NewSupervisor()
	syslog.AddToDaemon(sup, cfg)
	agentCmd := agent.GetCmd(cfg)
	sup.Add("agent", func() *exec.Cmd {
		return exec.Command("sh", "-c", agentCmd) //nolint:gosec // agentCmd is operator-controlled, not user input
	})
	go func() { _ = sup.Run(ctx) }() //nolint:errcheck // supervisor run error is logged internally; goroutine exit is non-fatal
}

// waitForFrontendDisconnect polls /proc/wekafs/interface until the named container's
// frontend is no longer connected (up to 120s).
// Mirrors Python wait for frontend disconnect in client shutdown flow.
func waitForFrontendDisconnect(ctx context.Context, containerName string) error {
	_, logger := instrumentation.CreateLogSpan(ctx, "modes.waitForFrontendDisconnect")
	defer logger.End()

	deadline := time.Now().Add(120 * time.Second)
	for {
		data, err := os.ReadFile("/proc/wekafs/interface")
		if err != nil {
			return nil // driver not loaded → no frontend connected
		}
		connected := false
		for _, line := range strings.Split(string(data), "\n") {
			// Mirror Python line.startswith("Container=" + name) at weka_runtime.py:4425.
			if strings.HasPrefix(line, "Container="+containerName) && strings.Contains(line, "Connected frontend") {
				connected = true
				break
			}
		}
		if !connected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("frontend %q still connected after 120s", containerName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
