package resources

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/util"
)

const PodConfigHashAnnotation = "weka.io/pod-config-hash"

// podConfigHashInput contains curated WekaContainerSpec fields that affect pod creation.
// Maps are replaced with hashable equivalents for util.HashStruct compatibility.
type podConfigHashInput struct {
	Image               string
	Mode                string
	WekaContainerName   string
	NumCores            int
	ExtraCores          int
	CoreIds             []int
	CpuPolicy           string
	Hugepages           int
	HugepagesOffset     int
	HugepagesSize       string
	AdditionalMemory    int
	Network             weka.Network
	TracesConfiguration *weka.TracesConfiguration
	// Tolerations are excluded — handled by in-place updatePodTolerationsOnChange
	// Affinity and TSC contain maps (LabelSelector.MatchLabels), so serialize to JSON strings
	AffinityJSON                  string
	TopologySpreadConstraintsJSON string
	HostPID                       bool
	ExposePorts                   []int
	ExposedPorts                  []corev1.ContainerPort
	ImagePullSecret               string
	ServiceAccountName            string
	PVC                           *weka.PVCConfig
	AdditionalSecrets             *util.HashableMap
	NodeInfoConfigMap             string
	WekaSecretRef                 corev1.EnvVarSource
	DriversDistService            string
	DriversLoaderImage            string
	DriversBuildId                *string
	Ipv6                          bool
	PortRange                     *weka.PortRange
	Port                          int
	AgentPort                     int
	Resources                     *weka.PodResourcesSpec
	Overrides                     podConfigOverridesHash
}

type podConfigOverridesHash struct {
	PreRunScript          string
	DebugSleepOnTerminate int
}

func ComputePodConfigHash(spec *weka.WekaContainerSpec) (string, error) {
	overrides := podConfigOverridesHash{}
	if spec.Overrides != nil {
		overrides.PreRunScript = spec.Overrides.PreRunScript
		overrides.DebugSleepOnTerminate = spec.Overrides.DebugSleepOnTerminate
	}

	tracesConfig := spec.TracesConfiguration
	if tracesConfig == nil {
		tracesConfig = weka.GetDefaultTracesConfiguration()
	}

	// Serialize Affinity and TopologySpreadConstraints to JSON to avoid map issues with HashStruct
	var affinityJSON string
	if spec.Affinity != nil {
		b, err := json.Marshal(spec.Affinity)
		if err != nil {
			return "", err
		}
		affinityJSON = string(b)
	}

	var tscJSON string
	if len(spec.TopologySpreadConstraints) > 0 {
		b, err := json.Marshal(spec.TopologySpreadConstraints)
		if err != nil {
			return "", err
		}
		tscJSON = string(b)
	}

	input := podConfigHashInput{
		Image:                         spec.Image,
		Mode:                          spec.Mode,
		NumCores:                      spec.NumCores,
		ExtraCores:                    spec.ExtraCores,
		CoreIds:                       spec.CoreIds,
		CpuPolicy:                     string(spec.CpuPolicy),
		Hugepages:                     spec.Hugepages,
		HugepagesOffset:               spec.HugepagesOffset,
		HugepagesSize:                 spec.HugepagesSize,
		AdditionalMemory:              spec.AdditionalMemory,
		Network:                       spec.Network,
		TracesConfiguration:           tracesConfig,
		AffinityJSON:                  affinityJSON,
		TopologySpreadConstraintsJSON: tscJSON,
		HostPID:                       spec.HostPID,
		ExposePorts:                   spec.ExposePorts,
		ExposedPorts:                  spec.ExposedPorts,
		ImagePullSecret:               spec.ImagePullSecret,
		ServiceAccountName:            spec.ServiceAccountName,
		PVC:                           spec.PVC,
		AdditionalSecrets:             util.NewHashableMap(spec.AdditionalSecrets),
		NodeInfoConfigMap:             spec.NodeInfoConfigMap,
		WekaSecretRef:                 spec.WekaSecretRef,
		DriversDistService:            spec.DriversDistService,
		DriversLoaderImage:            spec.DriversLoaderImage,
		DriversBuildId:                spec.DriversBuildId,
		Ipv6:                          spec.Ipv6,
		PortRange:                     spec.PortRange,
		Port:                          spec.Port,
		AgentPort:                     spec.AgentPort,
		Resources:                     spec.Resources,
		Overrides:                     overrides,
	}
	return util.HashStruct(input)
}
