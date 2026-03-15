package wekacluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
)

// clusterConfigData contains fields that should trigger pod rotation when changed.
type clusterConfigData struct {
	Image              string
	PodConfigVersion   int
	WekaRuntimeVersion int
	TrackedSpecFields  clusterConfigTrackedFields
}

// clusterConfigTrackedFields mirrors UpdatableClusterSpec — a curated list of properties
// that should trigger upgrade when changed. Remove fields that should NOT trigger upgrade.
type clusterConfigTrackedFields struct {
	AdditionalMemory weka.AdditionalMemory
	Tolerations      []string
	RawTolerations   []v1.Toleration
	NodeSelector     map[string]string
	RoleNodeSelector weka.RoleNodeSelector

	Network                     weka.Network
	TracesConfiguration         *weka.TracesConfiguration
	RoleCoreIds                 weka.RoleCoreIds
	CpuPolicy                   weka.CpuPolicy
	ComputeExtraCores           int
	DriveExtraCores             int
	S3ExtraCores                int
	NfsExtraCores               int
	DataServicesExtraCores      int
	ComputeHugepages            int
	ComputeHugepagesOffset      int
	DriveHugepages              int
	DriveHugepagesOffset        int
	S3Hugepages                 int
	S3HugepagesOffset           int
	NfsHugepages                int
	NfsHugepagesOffset          int
	DataServicesHugepages       int
	DataServicesHugepagesOffset int
}

func trackedFieldsFromSpec(spec *weka.WekaClusterSpec) clusterConfigTrackedFields {
	fields := clusterConfigTrackedFields{
		AdditionalMemory: spec.AdditionalMemory,
		Tolerations:      spec.Tolerations,
		RawTolerations:   spec.RawTolerations,

		NodeSelector:     spec.NodeSelector,
		RoleNodeSelector: spec.RoleNodeSelector,

		Network:             spec.Network,
		TracesConfiguration: spec.TracesConfiguration,
		RoleCoreIds:         spec.RoleCoreIds,
		CpuPolicy:           spec.CpuPolicy,
	}
	if spec.Dynamic != nil {
		fields.ComputeExtraCores = spec.Dynamic.ComputeExtraCores
		fields.DriveExtraCores = spec.Dynamic.DriveExtraCores
		fields.S3ExtraCores = spec.Dynamic.S3ExtraCores
		fields.NfsExtraCores = spec.Dynamic.NfsExtraCores
		fields.DataServicesExtraCores = spec.Dynamic.DataServicesExtraCores
		fields.ComputeHugepages = spec.Dynamic.ComputeHugepages
		fields.ComputeHugepagesOffset = spec.Dynamic.ComputeHugepagesOffset
		fields.DriveHugepages = spec.Dynamic.DriveHugepages
		fields.DriveHugepagesOffset = spec.Dynamic.DriveHugepagesOffset
		fields.S3Hugepages = spec.Dynamic.S3FrontendHugepages
		fields.S3HugepagesOffset = spec.Dynamic.S3FrontendHugepagesOffset
		fields.NfsHugepages = spec.Dynamic.NfsFrontendHugepages
		fields.NfsHugepagesOffset = spec.Dynamic.NfsFrontendHugepagesOffset
		fields.DataServicesHugepages = spec.Dynamic.DataServicesHugepages
		fields.DataServicesHugepagesOffset = spec.Dynamic.DataServicesHugepagesOffset
	}
	return fields
}

func (r *wekaClusterReconcilerLoop) isImageChanged() bool {
	return r.cluster.Spec.Image != r.cluster.Status.LastAppliedImage
}

// anyContainerConfigApplied returns true if any container has already been upgraded to the target config hash.
func anyContainerConfigApplied(containers []*weka.WekaContainer, targetConfigHash string) bool {
	for _, container := range containers {
		if container.Status.LastAppliedSpec == targetConfigHash && container.Status.ClusterContainerID != nil {
			return true
		}
	}
	return false
}

// clusterConfigHash returns a hex-encoded SHA-256 hash of the clusterConfigData struct,
// incorporating the cluster image, runtime/pod-config versions, and updatable fields.
func clusterConfigHash(spec *weka.WekaClusterSpec) string {
	data := clusterConfigData{
		Image:              spec.Image,
		PodConfigVersion:   config.Config.PodConfigVersion,
		WekaRuntimeVersion: consts.WekaRuntimeVersion,
		TrackedSpecFields:  trackedFieldsFromSpec(spec),
	}
	b, _ := json.Marshal(data)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}
