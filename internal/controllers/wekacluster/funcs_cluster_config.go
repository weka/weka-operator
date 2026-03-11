package wekacluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/weka/go-weka-observability/instrumentation"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// clusterConfigData contains fields that should trigger pod rotation when changed.
type clusterConfigData struct {
	PodConfigVersion   int
	WekaRuntimeVersion int
}

// clusterConfigHash returns a hex-encoded SHA-256 hash of the clusterConfigData struct.
func clusterConfigHash() string {
	data := clusterConfigData{
		PodConfigVersion:   config.Config.PodConfigVersion,
		WekaRuntimeVersion: consts.WekaRuntimeVersion,
	}
	b, _ := json.Marshal(data)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

func (r *wekaClusterReconcilerLoop) handleClusterConfigChange(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "handleClusterConfigChange")
	defer end()

	cluster := r.cluster
	currentHash := clusterConfigHash()

	if cluster.Status.LastAppliedConfig == currentHash {
		return nil
	}

	logger.Info("Cluster config hash changed", "previous", cluster.Status.LastAppliedConfig, "current", currentHash)
	cluster.Status.LastAppliedConfig = currentHash
	return r.getClient().Status().Update(ctx, cluster)
}
