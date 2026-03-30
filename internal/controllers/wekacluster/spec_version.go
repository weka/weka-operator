package wekacluster

import (
	"crypto/sha256"
	"fmt"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// ClusterSpecVersionInputs defines the fields tracked for spec version calculation.
// Changing any of these fields produces a different spec version hash,
// which triggers pod rotation for all containers owned by the cluster.
type ClusterSpecVersionInputs struct {
	Image                string
	PodConfigVersion     string
	WekaRuntimeVersion   string
	DriveCores           int
}

func CalcClusterSpecVersion(image string, driveCores int) string {
	inputs := ClusterSpecVersionInputs{
		Image:                image,
		PodConfigVersion:     config.Config.PodConfigVersion,
		WekaRuntimeVersion:   consts.WekaRuntimeVersion,
		DriveCores:           driveCores,
	}
	raw := fmt.Sprintf("%s|%s|%s|%d", inputs.Image, inputs.PodConfigVersion, inputs.WekaRuntimeVersion, inputs.DriveCores)
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", hash)[:8]
}
