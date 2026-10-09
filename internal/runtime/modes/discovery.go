package modes

import (
	"context"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/config"
	"github.com/weka/weka-operator/internal/runtime/cos"
	"github.com/weka/weka-operator/internal/runtime/results"
)

func init() {
	register("discovery", runDiscovery)
}

type discoveryResult struct {
	IsHT       bool   `json:"is_ht"`
	KubeDistro string `json:"kubernetes_distro"`
	OS         string `json:"os"`
	OSBuildID  string `json:"os_build_id"`
	Schema     int    `json:"schema"`
}

func runDiscovery(ctx context.Context, cfg *config.Config) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "discovery")
	defer logger.End()

	if err := cos.ConfigureHugepages(ctx, cfg); err != nil {
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

	logger.Info("Discovery result", "is_ht", isHT, "os", nodeInfo.Os, "distro", nodeInfo.KubernetesDistro)

	return results.Write(discoveryResult{
		IsHT:       isHT,
		KubeDistro: nodeInfo.KubernetesDistro,
		OS:         nodeInfo.Os,
		OSBuildID:  nodeInfo.OsBuildId,
		Schema:     1,
	})
}
