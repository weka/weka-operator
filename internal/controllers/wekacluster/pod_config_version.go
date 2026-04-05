package wekacluster

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// ClusterPodConfigVersionInputs defines the fields tracked for spec version calculation.
// Changing any of these fields produces a different spec version hash,
// which triggers coordinated rolling pod rotation for all containers owned by the cluster.
type ClusterPodConfigVersionInputs struct {
	// Operator-level constants
	PodConfigVersion   string `json:"podConfigVersion"`
	WekaRuntimeVersion string `json:"wekaRuntimeVersion"`

	// WekaClusterSpec fields
	Image string `json:"image"`
}

func CalcClusterPodConfigVersion(spec *weka.WekaClusterSpec) string {
	inputs := ClusterPodConfigVersionInputs{
		PodConfigVersion: config.Config.PodConfigVersion,
		Image:            spec.Image,
	}
	if config.Config.EnablePodConfigCodeVersionRotation {
		inputs.WekaRuntimeVersion = consts.PodConfigCodeVersion
	}
	raw, _ := json.Marshal(inputs)
	hash := sha256.Sum256(raw)
	return fmt.Sprintf("%x", hash)[:8]
}
