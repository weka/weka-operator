package runtimes

import (
	"context"
	"time"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/cpuaffinity"
	"github.com/weka/weka-operator/internal/runtime/weka"
)

// buildContainerInput assembles the weka.ContainerInput fields shared by every container mode
// (backend and client) from name, config, and preparation results.
func buildContainerInput(deps *Deps, mode, name string, cc *config.ContainerConfig, pp *persistentState, features domain.FeatureFlags) (weka.ContainerInput, error) {
	memBytes, err := weka.ParseSize(cc.Memory.Request)
	if err != nil {
		return weka.ContainerInput{}, err
	}
	var failureDomain string
	if pp.FailureDomain != nil {
		failureDomain = *pp.FailureDomain
	}
	return weka.ContainerInput{
		Runner: deps.Runner, Roots: deps.Paths,
		Name: name, Mode: mode, Port: pp.Ports.Weka,
		Cores: cc.CPU.Cores, CoreIDs: cc.CPU.CoreIDs, NonDatapath: cc.CPU.NonDatapathCores, CPUPolicy: cc.CPU.Policy,
		MemoryBytes: memBytes, DPDKBaseMiB: cc.Memory.DPDKBaseMiB,
		JoinIPs: cc.Network.JoinIPs, ManagementIPs: pp.ManagementIPs,
		NetDevice: pp.NetDevice, NetSelectors: cc.Network.Selectors, NetSubnets: cc.Network.Subnets,
		UDPMode: cc.Network.UDPMode, Gateway: cc.Network.Gateway, Netmask: cc.Network.Netmask,
		BindManagementAll: cc.Network.BindManagementAll, NvidiaVFSingleIP: cc.Network.NvidiaVFSingleIP,
		AutoRemoveTimeout: cc.Runtime.AutoRemoveTimeout, FailureDomain: failureDomain,
		Features: features,
	}, nil
}

// startAndPublish starts the container, waits for exec readiness, and writes feature flags.
// Shared by every weka container mode once the container itself has been created.
func startAndPublish(ctx context.Context, deps *Deps, name string, features domain.FeatureFlags) error {
	if err := weka.StartContainer(ctx, deps.Runner, name); err != nil {
		return err
	}
	if err := weka.EnsureContainerExec(ctx, deps.Runner, deps.Clock, name); err != nil {
		return err
	}
	return weka.WriteFeatureFlags(ctx, &deps.Paths, features)
}

// registerCPUAffinity arms periodic CPU affinity management, unless Weka itself manages
// non-ionode affinity for this release.
func registerCPUAffinity(deps *Deps, features domain.FeatureFlags) {
	if features.WekaManagesNonIonodeAffinity {
		return
	}
	deps.Coord.GoPeriodic("cpu-affinity", deps.Clock, 30*time.Second, 60*time.Second, func(ctx context.Context) error {
		return cpuaffinity.Manage(ctx, deps.Runner)
	})
}
