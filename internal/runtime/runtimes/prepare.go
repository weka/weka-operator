package runtimes

import (
	"context"
	"errors"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/agent"
	"github.com/weka/weka-operator/internal/runtime/clock"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/generation"
	"github.com/weka/weka-operator/internal/runtime/lifecycle"
	"github.com/weka/weka-operator/internal/runtime/network"
	"github.com/weka/weka-operator/internal/runtime/persistency"
	"github.com/weka/weka-operator/internal/runtime/resources"
	"github.com/weka/weka-operator/internal/runtime/shutdown"
	"github.com/weka/weka-operator/internal/runtime/syslog"
	"github.com/weka/weka-operator/internal/runtime/weka"
	v1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// generationWatchInterval is the takeover-marker poll cadence.
const generationWatchInterval = time.Second

// agentReadyTimeout bounds how long a persistent mode waits for "weka local ps" to succeed
// after starting the agent daemon.
const agentReadyTimeout = 60 * time.Second

// persistentState is what acquirePersistentState resolved, for the caller to act on.
type persistentState struct {
	FailureDomain *string
	MachineID     string
	Ports         config.Ports
	NetDevice     string
	Drives        []string
	ManagementIPs []string
}

// acquirePersistentState runs the shared persistent-mode preamble, in order: configure
// persistence, resolve operator resources (when domain.NeedsOperatorResources(mode)),
// publish this runtime's generation and arm its takeover watcher, write management IPs,
// then acquire the generation lock (registered last).
func acquirePersistentState(ctx context.Context, deps *Deps, mode string, cc *config.ContainerConfig, ports config.Ports) (persistentState, error) {
	state := persistentState{FailureDomain: cc.Identity.FailureDomain, MachineID: cc.Identity.MachineIdentifier, Ports: ports, NetDevice: cc.Network.Device}

	if err := persistency.Configure(ctx, deps.Runner, cc.Persistence, deps.Paths); err != nil {
		return state, err
	}

	var alloc *v1alpha1.ContainerAllocations
	if domain.NeedsOperatorResources(mode) {
		bootID := generation.ReadBootID()
		shouldAbort := func() bool {
			instr := shutdown.ReadInstructions(deps.Paths, cc.Identity.PodID, bootID)
			return instr.AllowStop || instr.AllowForceStop
		}
		var err error
		alloc, err = resources.WaitAndLoad(ctx, deps.Clock, deps.Paths, shouldAbort)
		if err != nil {
			return state, err
		}
	}

	req := allocation{Ports: ports, FailureDomain: cc.Identity.FailureDomain, MachineID: cc.Identity.MachineIdentifier, NetDevice: cc.Network.Device}
	resolved := resolveAllocations(mode, &req, alloc)
	state.FailureDomain, state.MachineID, state.Ports, state.NetDevice = resolved.FailureDomain, resolved.MachineID, resolved.Ports, resolved.NetDevice
	state.Drives = resolved.Drives

	marker, err := generation.Publish(ctx, deps.Paths)
	if err != nil {
		return state, err
	}
	deps.Coord.Go("generation-watch", func(ctx context.Context) error {
		return generationWatch(ctx, deps, marker)
	})

	ips, err := network.WriteManagementIPs(ctx, deps.Runner, network.ManagementInput{
		Mode:                  mode,
		NetworkDevice:         state.NetDevice,
		ManagementIP:          cc.Network.ManagementIP,
		ManagementIPSelectors: cc.Network.ManagementIPSelectors,
		NetworkSelectors:      cc.Network.Selectors,
		Subnets:               cc.Network.Subnets,
		IsIPv6:                cc.Network.IsIPv6,
		UDPMode:               cc.Network.UDPMode,
	}, deps.Paths)
	if err != nil {
		return state, err
	}
	state.ManagementIPs = ips

	lock, err := generation.AcquireLock(cc.Identity.Name)
	if err != nil {
		return state, err
	}
	if err := deps.Coord.RegisterLast("generation-lock", lock); err != nil {
		_ = lock.Close() //nolint:errcheck // best-effort: we're already returning the registration error
		return state, err
	}

	return state, nil
}

// startAgent runs the shared agent bring-up sequence: configure the agent, start syslog,
// override dependency settings, ensure drivers (when ensureDrivers), launch the agent, then
// wait for it to come up.
func startAgent(ctx context.Context, deps *Deps, mode string, cc *config.ContainerConfig, ports config.Ports, ensureDrivers bool, launched *bool) error {
	cfgIn := agent.ConfigureInput{Mode: mode, Identity: cc.Identity, Agent: cc.Agent, Ports: ports, Persistence: cc.Persistence, Paths: deps.Paths}
	if err := agent.Configure(ctx, deps.Runner, cfgIn, false); err != nil {
		return err
	}

	syslogCmd, err := syslog.Command(cc.Agent.SyslogPackage)
	if err != nil {
		return err
	}
	if !syslog.UseGoSyslog(cc.Agent.SyslogPackage) {
		syslog.StripMongodbModule(ctx)
	}
	if _, err = deps.Processes.StartDaemon(deps.Coord.ServicesContext(), "syslog", syslogCmd); err != nil {
		return err
	}

	err = agent.OverrideDependenciesFlag(ctx, deps.Runner, cc.Agent.ImageName)
	if err != nil {
		return err
	}

	if ensureDrivers {
		err = agent.EnsureDrivers(ctx, deps.Runner, deps.Clock, mode, cc.Agent.ImageName, deps.Paths.OptWeka)
		if err != nil {
			return err
		}
	}

	if launched != nil {
		*launched = true
	}
	if _, err = deps.Processes.StartDaemon(deps.Coord.ServicesContext(), "agent", agent.Command(ports.Agent)); err != nil {
		return err
	}

	return agent.AwaitReady(ctx, deps.Runner, deps.Clock, agentReadyTimeout)
}

// readFeatures reads the release spec's feature flags. Called once per Start, after the
// agent is up, so each family can select its Weka version.
func readFeatures(deps *Deps) (domain.FeatureFlags, error) {
	release, err := weka.ReadReleaseSpec(deps.Paths.OptWeka + "/dist/release")
	if err != nil {
		return domain.FeatureFlags{}, err
	}
	return release.FeatureFlags, nil
}

// generationWatch polls for a takeover on this runtime's generation marker and requests
// shutdown when one is detected. An IsTakenOver I/O error is logged and polling continues:
// a transient read failure must not be mistaken for a permanent watcher failure. Returns nil
// on context cancellation: a background housekeeping task has nothing to report on ordinary
// shutdown, and returning the cancellation error would surface as an unattributed fatal task
// error during cleanup.
func generationWatch(ctx context.Context, deps *Deps, marker string) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "runtimes.generationWatch")
	defer logger.End()

	err := clock.Poll(ctx, deps.Clock, generationWatchInterval, func() (bool, error) {
		takenOver, err := generation.IsTakenOver(deps.Paths, marker)
		if err != nil {
			logger.Warn("check takeover marker failed, will retry", "err", err)
			return false, nil
		}
		if takenOver {
			deps.Coord.RequestShutdown(lifecycle.ReasonTakeover)
		}
		return takenOver, nil
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
