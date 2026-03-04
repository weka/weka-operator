package resources

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/pkg/util"
)

type UpdatableClusterSpec struct {
	AdditionalMemory          weka.AdditionalMemory
	Tolerations               []string
	RawTolerations            []v1.Toleration
	DriversDistService        string
	ImagePullSecret           string
	Labels                    *util.HashableMap
	Annotations               *util.HashableMap
	NodeSelector              *util.HashableMap
	S3NodeSelector            *util.HashableMap
	NfsNodeSelector           *util.HashableMap
	ComputeNodeSelector       *util.HashableMap
	DriveNodeSelector         *util.HashableMap
	DataServicesNodeSelector  *util.HashableMap
	S3Annotations             *util.HashableMap
	NfsAnnotations            *util.HashableMap
	ComputeAnnotations        *util.HashableMap
	DriveAnnotations          *util.HashableMap
	DataServicesAnnotations   *util.HashableMap
	UpgradeForceReplace       bool
	UpgradeForceReplaceDrives bool
	Network                   weka.Network
	RoleNetworkSelector       weka.RoleNetworkSelector
	PvcConfig                 *weka.PVCConfig
	TracesConfiguration       *weka.TracesConfiguration
	RoleCoreIds               weka.RoleCoreIds
	CpuPolicy                 weka.CpuPolicy
	ComputeExtraCores         int
	DriveExtraCores           int
	S3ExtraCores              int
	NfsExtraCores             int
	DataServicesExtraCores    int
	DriversLoaderImage        string
	DriversBuildId            *string
}

func NewUpdatableClusterSpec(spec *weka.WekaClusterSpec, meta *metav1.ObjectMeta) *UpdatableClusterSpec {
	// Helper function to safely convert pointer-to-map to HashableMap
	safeHashableMap := func(ptr *map[string]string) *util.HashableMap {
		if ptr == nil {
			return nil
		}
		return util.NewHashableMap(*ptr)
	}

	// Get extra cores values from dynamic config if available
	computeExtraCores := 0
	driveExtraCores := 0
	s3ExtraCores := 0
	nfsExtraCores := 0
	dataServicesExtraCores := 0
	if spec.Dynamic != nil {
		computeExtraCores = spec.Dynamic.ComputeExtraCores
		driveExtraCores = spec.Dynamic.DriveExtraCores
		s3ExtraCores = spec.Dynamic.S3ExtraCores
		nfsExtraCores = spec.Dynamic.NfsExtraCores
		dataServicesExtraCores = spec.Dynamic.DataServicesExtraCores
	}

	return &UpdatableClusterSpec{
		AdditionalMemory:          spec.AdditionalMemory,
		Tolerations:               spec.Tolerations,
		RawTolerations:            spec.RawTolerations,
		DriversDistService:        spec.DriversDistService,
		ImagePullSecret:           spec.ImagePullSecret,
		Labels:                    util.NewHashableMap(meta.Labels),
		Annotations:               util.NewHashableMap(util.RemoveKeysStartingWithPrefix(meta.Annotations, "weka.io/prepull-")),
		NodeSelector:              util.NewHashableMap(spec.NodeSelector),
		S3NodeSelector:            safeHashableMap(spec.RoleNodeSelector.S3),
		NfsNodeSelector:           safeHashableMap(spec.RoleNodeSelector.Nfs),
		ComputeNodeSelector:       safeHashableMap(spec.RoleNodeSelector.Compute),
		DriveNodeSelector:         safeHashableMap(spec.RoleNodeSelector.Drive),
		DataServicesNodeSelector:  safeHashableMap(spec.RoleNodeSelector.DataServices),
		S3Annotations:             safeHashableMap(spec.RoleAnnotations.S3),
		NfsAnnotations:            safeHashableMap(spec.RoleAnnotations.Nfs),
		ComputeAnnotations:        safeHashableMap(spec.RoleAnnotations.Compute),
		DriveAnnotations:          safeHashableMap(spec.RoleAnnotations.Drive),
		DataServicesAnnotations:   safeHashableMap(spec.RoleAnnotations.DataServices),
		UpgradeForceReplace:       spec.GetOverrides().UpgradeForceReplace,
		UpgradeForceReplaceDrives: spec.GetOverrides().UpgradeForceReplaceDrives,
		Network:                   spec.Network,
		RoleNetworkSelector:       spec.RoleNetworkSelector,
		PvcConfig:                 GetPvcConfig(spec.GlobalPVC),
		TracesConfiguration:       spec.TracesConfiguration,
		RoleCoreIds:               spec.RoleCoreIds,
		CpuPolicy:                 spec.CpuPolicy,
		ComputeExtraCores:         computeExtraCores,
		DriveExtraCores:           driveExtraCores,
		S3ExtraCores:              s3ExtraCores,
		NfsExtraCores:             nfsExtraCores,
		DataServicesExtraCores:    dataServicesExtraCores,
		DriversLoaderImage:        spec.GetOverrides().DriversLoaderImage,
		DriversBuildId:            spec.GetOverrides().DriversBuildId,
	}
}
