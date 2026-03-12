package wekacluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// clusterConfigData contains fields that should trigger pod rotation when changed.
type clusterConfigData struct {
	Image              string
	PodConfigVersion   int
	WekaRuntimeVersion int
}

// clusterConfigHash returns a hex-encoded SHA-256 hash of the clusterConfigData struct,
// incorporating the cluster image and runtime/pod-config versions.
func clusterConfigHash(spec *weka.WekaClusterSpec) string {
	data := clusterConfigData{
		Image:              spec.Image,
		PodConfigVersion:   config.Config.PodConfigVersion,
		WekaRuntimeVersion: consts.WekaRuntimeVersion,
	}
	b, _ := json.Marshal(data)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}
