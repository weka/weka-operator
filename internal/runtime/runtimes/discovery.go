package runtimes

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/cos"
	"github.com/weka/weka-operator/internal/runtime/results"
)

type discoveryResult struct {
	IsHT        bool   `json:"is_ht"`
	KubeDistro  string `json:"kubernetes_distro"`
	OS          string `json:"os"`
	OSBuildID   string `json:"os_build_id"`
	ProcVersion string `json:"proc_version"`
	Schema      int    `json:"schema"`
}

// runDiscovery reports host OS/hyperthreading facts and configures hugepages on COS nodes.
// Mirrors Python discovery() at weka_runtime.py.
func runDiscovery(ctx context.Context, cfg *config.DiscoveryConfig, deps *Deps) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "discovery")
	defer logger.End()

	if err := cos.ConfigureHugepages(ctx, deps.Runner, cfg.Host); err != nil {
		return err
	}

	nodeInfo, err := osinfo.Load()
	if err != nil {
		logger.Info("Could not load OS info, using defaults", "err", err.Error())
		nodeInfo = &osinfo.NodeInfo{KubernetesDistro: osinfo.KubeDistroK8s}
	}

	isHT := false
	if ht, err := osinfo.IsHT(); err != nil {
		logger.Info("Could not determine HT status, defaulting to false", "err", err.Error())
	} else {
		isHT = ht
	}

	// Reported like the HT status: best-effort, the operator only needs it to diagnose NixOS kernels.
	procVersion, pvErr := osinfo.ReadProcVersion()
	if pvErr != nil {
		logger.Info("Could not read /proc/version, reporting empty", "err", pvErr.Error())
	}

	logger.Info("Discovery result", "is_ht", isHT, "os", nodeInfo.Os, "distro", nodeInfo.KubernetesDistro)

	return results.Write(ctx, cfg.Results.Path, discoveryResult{
		IsHT:        isHT,
		KubeDistro:  nodeInfo.KubernetesDistro,
		OS:          nodeInfo.Os,
		OSBuildID:   nodeInfo.OsBuildId,
		ProcVersion: procVersion,
		Schema:      1,
	})
}
