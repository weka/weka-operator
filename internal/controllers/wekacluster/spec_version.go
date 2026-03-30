package wekacluster

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// ClusterSpecVersionInputs defines the fields tracked for spec version calculation.
// Changing any of these fields produces a different spec version hash,
// which triggers coordinated rolling pod rotation for all containers owned by the cluster.
type ClusterSpecVersionInputs struct {
	// Operator-level constants
	PodConfigVersion   string `json:"podConfigVersion"`
	WekaRuntimeVersion string `json:"wekaRuntimeVersion"`

	// WekaClusterSpec fields
	Image            string                    `json:"image"`
	CpuPolicy        weka.CpuPolicy            `json:"cpuPolicy"`
	Ipv6             bool                      `json:"ipv6"`
	Network          weka.Network              `json:"network"`
	Dynamic          *weka.WekaClusterTemplate `json:"dynamic"`
	AdditionalMemory weka.AdditionalMemory     `json:"additionalMemory"`
	Encryption       *weka.EncryptionConfig    `json:"encryption"`
}

func CalcClusterSpecVersion(spec *weka.WekaClusterSpec) string {
	inputs := ClusterSpecVersionInputs{
		PodConfigVersion:   config.Config.PodConfigVersion,
		WekaRuntimeVersion: consts.WekaRuntimeVersion,
		Image:              spec.Image,
		CpuPolicy:          spec.CpuPolicy,
		Ipv6:               spec.Ipv6,
		Network:            spec.Network,
		Dynamic:            spec.Dynamic,
		Encryption:         spec.Encryption,
	}
	raw, _ := json.Marshal(inputs)
	hash := sha256.Sum256(raw)
	return fmt.Sprintf("%x", hash)[:8]
}
