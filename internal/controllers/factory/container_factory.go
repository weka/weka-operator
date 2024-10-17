package factory

import (
	"fmt"
	"os"
	"slices"
	"strings"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/util"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	util2 "github.com/weka/weka-operator/pkg/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewWekaContainerForWekaCluster(cluster *wekav1alpha1.WekaCluster,
	template allocator.ClusterTemplate,
	role, name string,
) (*wekav1alpha1.WekaContainer, error) {
	labels := map[string]string{
		"weka.io/cluster-id": string(cluster.UID),
		"weka.io/mode":       role, // in addition to spec for indexing on k8s side for filtering by mode
	}
	labels = util2.MergeLabels(cluster.ObjectMeta.GetLabels(), labels)

	var hugePagesNum int
	var numCores int
	if role == "drive" {
		hugePagesNum = template.DriveHugepages
		numCores = template.DriveCores
	} else if role == "compute" {
		hugePagesNum = template.ComputeHugepages
		numCores = template.ComputeCores
	} else if role == "s3" {
		hugePagesNum = template.S3FrontendHugepages
		numCores = template.S3Cores
	} else if role == "envoy" {
		numCores = template.EnvoyCores
	}

	network, err := resources.GetContainerNetwork(cluster.Spec.NetworkSelector)
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

	serviceAccountName := os.Getenv("WEKA_OPERATOR_BACKEND_POD_SA_NAME")
	if serviceAccountName == "" {
		return nil, fmt.Errorf("cannot create %s container, WEKA_OPERATOR_BACKEND_POD_SA_NAME is not defined", role)
	}

	nodeSelector := cluster.Spec.NodeSelector
	if len(cluster.Spec.RoleNodeSelector.ForRole(role)) != 0 {
		nodeSelector = cluster.Spec.RoleNodeSelector.ForRole(role)
	}
	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", cluster.Name, name),
			Namespace: cluster.Namespace,
			Labels:    labels,
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
			HugepagesSize:         template.HugePageSize,
			HugepagesOverride:     template.HugePagesOverride,
			NumDrives:             numDrives,
			WekaSecretRef:         v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretKey}},
			DriversDistService:    cluster.Spec.DriversDistService,
			CpuPolicy:             cluster.Spec.CpuPolicy,
			TracesConfiguration:   cluster.Spec.TracesConfiguration,
			Tolerations:           util.ExpandTolerations([]v1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations),
			Ipv6:                  cluster.Spec.Ipv6,
			AdditionalMemory:      additionalMemory,
			Group:                 containerGroup,
			AdditionalSecrets:     additionalSecrets,
			NoAffinityConstraints: cluster.Spec.DisregardRedundancy,
			NodeSelector:          nodeSelector,
			ServiceAccountName:    serviceAccountName,
			FailureDomainLabel:    cluster.Spec.FailureDomainLabel,
		},
	}

	topologySpreadConstraints := preparePodTopologySpreadConstraints(cluster, role)
	container.Spec.TopologySpreadConstraints = topologySpreadConstraints

	var affinity *v1.Affinity

	if cluster.Spec.PodConfig != nil {
		affinity = cluster.Spec.PodConfig.Affinity
		// if role specific affinity is set, we don't need to set general affinity
		if cluster.Spec.PodConfig.RoleAffinity != nil && cluster.Spec.PodConfig.RoleAffinity.ForRole(role) != nil {
			affinity = cluster.Spec.PodConfig.RoleAffinity.ForRole(role)
		}
	}

	if affinity != nil {
		container.Spec.Affinity = affinity
	}

	return container, nil
}

func preparePodTopologySpreadConstraints(cluster *wekav1alpha1.WekaCluster, role string) []v1.TopologySpreadConstraint {
	defaultConstraints := getDefaultRoleTopologySpreadConstraints(cluster, role)
	podConstraints := make([]v1.TopologySpreadConstraint, 0)
	podConstraints = append(podConstraints, defaultConstraints.ForRole(role)...)

	if cluster.Spec.PodConfig == nil {
		return podConstraints
	}

	if cluster.Spec.PodConfig.RoleTopologySpreadConstraints == nil && cluster.Spec.PodConfig.TopologySpreadConstraints == nil {
		return podConstraints
	}

	if cluster.Spec.PodConfig.RoleTopologySpreadConstraints != nil && cluster.Spec.PodConfig.RoleTopologySpreadConstraints.ForRole(role) != nil {
		podConstraints = append(podConstraints, cluster.Spec.PodConfig.RoleTopologySpreadConstraints.ForRole(role)...)
		// if role specific constraints are set, we don't need to add general constraints
		return podConstraints
	}

	if cluster.Spec.PodConfig.TopologySpreadConstraints != nil {
		podConstraints = append(podConstraints, cluster.Spec.PodConfig.TopologySpreadConstraints...)
	}

	return podConstraints
}

func getDefaultRoleTopologySpreadConstraints(cluster *wekav1alpha1.WekaCluster, role string) *wekav1alpha1.RoleTopologySpreadConstraints {
	constraints := &wekav1alpha1.RoleTopologySpreadConstraints{}

	if cluster.Spec.FailureDomainLabel == nil {
		return constraints
	}

	if !slices.Contains([]string{"compute", "drive", "s3"}, role) {
		return constraints
	}

	constraint := v1.TopologySpreadConstraint{
		MaxSkew:           1,
		TopologyKey:       *cluster.Spec.FailureDomainLabel,
		WhenUnsatisfiable: v1.DoNotSchedule,
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"weka.io/cluster-id": string(cluster.UID),
				"weka.io/mode":       role,
			},
		},
	}

	switch role {
	case "compute":
		constraints.Compute = append(constraints.Compute, constraint)
	case "drive":
		constraints.Drive = append(constraints.Drive, constraint)
	case "s3":
		constraints.S3 = append(constraints.S3, constraint)
	}
	return constraints
}
