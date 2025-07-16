package factory

import (
	"fmt"
	"slices"
	"strings"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	util2 "github.com/weka/weka-operator/pkg/util"
)

func NewWekaContainerForWekaCluster(cluster *wekav1alpha1.WekaCluster,
	template allocator.ClusterTemplate,
	role, name string,
) (*wekav1alpha1.WekaContainer, error) {
	labels := RequiredWekaContainerLabels(cluster.UID, cluster.Name, role)
	labels = util2.MergeMaps(cluster.ObjectMeta.GetLabels(), labels)

	annotations := cluster.GetAnnotationsForRole(role)

	var hugePagesNum int
	var hugePagesOffset int
	var numCores int
	if role == "drive" {
		hugePagesNum = template.DriveHugepages
		hugePagesOffset = template.DriveHugepagesOffset
		numCores = template.DriveCores
	} else if role == "compute" {
		hugePagesNum = template.ComputeHugepages
		hugePagesOffset = template.ComputeHugepagesOffset
		numCores = template.ComputeCores
	} else if role == "s3" {
		hugePagesNum = template.S3FrontendHugepages
		hugePagesOffset = template.S3FrontendHugepagesOffset
		numCores = template.S3Cores
	} else if role == "envoy" {
		numCores = template.EnvoyCores
	} else if role == "nfs" {
		numCores = template.NfsCores
		hugePagesNum = template.NfsFrontendHugepages
		hugePagesOffset = template.NfsFrontendHugepagesOffset
	} else {
		return nil, fmt.Errorf("unsupported role %s", role)
	}

	networkSelector := cluster.GetNetworkSelectorForRole(role)

	network, err := resources.GetContainerNetwork(networkSelector)
	if err != nil {
		return nil, err
	}

	secretKey := cluster.GetOperatorSecretName()

	additionalMemory := 0
	extraCores := 0
	numDrives := 0

	switch role {
	case "compute":
		additionalMemory = cluster.Spec.AdditionalMemory.Compute
	case "drive":
		additionalMemory = cluster.Spec.AdditionalMemory.Drive
		numDrives = template.NumDrives
	case "s3":
		additionalMemory = cluster.Spec.AdditionalMemory.S3
		extraCores = template.S3ExtraCores
	case "nfs":
		additionalMemory = cluster.Spec.AdditionalMemory.Nfs
		extraCores = template.NfsExtraCores
	case "envoy":
		additionalMemory = cluster.Spec.AdditionalMemory.Envoy
	}

	containerGroup := ""
	if slices.Contains([]string{"s3", "envoy"}, role) {
		containerGroup = "s3"
	}

	wekahomeConfig, err := domain.GetWekahomeConfig(cluster)
	if err != nil {
		return nil, err
	}

	additionalSecrets := make(map[string]string)
	if domain.GetWekaHomeSecretRef(wekahomeConfig) != nil {
		secret := domain.GetWekaHomeSecretRef(wekahomeConfig)
		if secret != nil {
			additionalSecrets["wekahome-cacert"] = *secret
		}
	}

	nodeSelector := cluster.GetNodeSelectorForRole(role)
	if role != wekav1alpha1.WekaContainerModeEnvoy { // envoy sticks to s3, so does not need explicit node selector
		nodeSelector = map[string]string{}
	}

	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-%s", cluster.Name, name),
			Namespace:   cluster.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			Image:                 cluster.Spec.Image,
			ImagePullSecret:       cluster.Spec.ImagePullSecret,
			WekaContainerName:     strings.Replace(name, "-", "x", -1),
			Mode:                  role,
			NumCores:              numCores,
			ExtraCores:            extraCores,
			Network:               network,
			Hugepages:             hugePagesNum,
			HugepagesOffset:       hugePagesOffset,
			HugepagesSize:         template.HugePageSize,
			HugepagesOverride:     template.HugePagesOverride,
			NumDrives:             numDrives,
			WekaSecretRef:         v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretKey}},
			DriversDistService:    cluster.Spec.DriversDistService,
			CpuPolicy:             cluster.Spec.CpuPolicy,
			CoreIds:               cluster.GetCoreIdsForRole(role),
			TracesConfiguration:   cluster.Spec.TracesConfiguration,
			Tolerations:           util.ExpandTolerations([]v1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations),
			Ipv6:                  cluster.Spec.Ipv6,
			AdditionalMemory:      additionalMemory,
			Group:                 containerGroup,
			AdditionalSecrets:     additionalSecrets,
			NoAffinityConstraints: cluster.Spec.GetOverrides().DisregardRedundancy,
			NodeSelector:          nodeSelector,
			FailureDomain:         cluster.Spec.FailureDomain,
			DriversLoaderImage:    cluster.Spec.GetOverrides().DriversLoaderImage,
			PVC:                   resources.GetPvcConfig(cluster.Spec.GlobalPVC),
		},
	}

	if role == wekav1alpha1.WekaContainerModeEnvoy {
		container.Spec.ExposePorts = []int{cluster.Status.Ports.LbPort, cluster.Status.Ports.LbAdminPort}
	}

	topologySpreadConstraints := preparePodTopologySpreadConstraints(cluster, role)
	container.Spec.TopologySpreadConstraints = topologySpreadConstraints

	affinity := cluster.GetAffinityForRole(role)

	if affinity != nil {
		container.Spec.Affinity = affinity
	}

	if cluster.Spec.ServiceAccountName != "" {
		container.Spec.ServiceAccountName = cluster.Spec.ServiceAccountName
	}

	return container, nil
}

func preparePodTopologySpreadConstraints(cluster *wekav1alpha1.WekaCluster, role string) []v1.TopologySpreadConstraint {
	defaultConstraints := getDefaultRoleTopologySpreadConstraints(cluster, role)
	podConstraints := make([]v1.TopologySpreadConstraint, 0)
	podConstraints = append(podConstraints, defaultConstraints.ForRole(role)...)

	definedPodConstraints := cluster.GetTopologySpreadConstraintsForRole(role)
	if definedPodConstraints != nil {
		podConstraints = append(podConstraints, definedPodConstraints...)
	}

	return podConstraints
}

func getDefaultRoleTopologySpreadConstraints(cluster *wekav1alpha1.WekaCluster, role string) *wekav1alpha1.RoleTopologySpreadConstraints {
	constraints := &wekav1alpha1.RoleTopologySpreadConstraints{}

	if cluster.Spec.FailureDomain == nil {
		return constraints
	}

	if !slices.Contains([]string{"compute", "drive", "s3"}, role) {
		return constraints
	}

	var roleConstraints []v1.TopologySpreadConstraint

	if cluster.Spec.FailureDomain.Label != nil {
		// if label is set, use it as topology key and check for skew
		skew := 1
		if cluster.Spec.FailureDomain.Skew != nil {
			skew = *cluster.Spec.FailureDomain.Skew
		}
		// if single label is set for FD, use strict scheduling with "DoNotSchedule"
		constraint := v1.TopologySpreadConstraint{
			MaxSkew:           int32(skew),
			TopologyKey:       *cluster.Spec.FailureDomain.Label,
			WhenUnsatisfiable: v1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"weka.io/cluster-id": string(cluster.UID),
					"weka.io/mode":       role,
				},
			},
		}
		roleConstraints = append(roleConstraints, constraint)

	} else if len(cluster.Spec.FailureDomain.CompositeLabels) > 0 {
		for _, label := range cluster.Spec.FailureDomain.CompositeLabels {
			// if composite labels are set, use them as topology keys
			// and scheule with the best effort - "ScheduleAnyway"
			constraint := v1.TopologySpreadConstraint{
				MaxSkew:           1,
				TopologyKey:       label,
				WhenUnsatisfiable: v1.ScheduleAnyway,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"weka.io/cluster-id": string(cluster.UID),
						"weka.io/mode":       role,
					},
				},
			}
			roleConstraints = append(roleConstraints, constraint)
		}
	}

	switch role {
	case "compute":
		constraints.Compute = append(constraints.Compute, roleConstraints...)
	case "drive":
		constraints.Drive = append(constraints.Drive, roleConstraints...)
	case "s3":
		constraints.S3 = append(constraints.S3, roleConstraints...)
	}
	return constraints
}
