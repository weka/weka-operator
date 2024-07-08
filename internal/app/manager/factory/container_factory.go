package factory

import (
	"errors"
	"fmt"
	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"slices"
	"strings"
)

func NewContainerName(role string) string {
	guid := string(uuid.NewUUID())
	return fmt.Sprintf("%s-%s", role, guid)
}

func BuildMissingContainers(cluster *wekav1alpha1.WekaCluster, template domain.ClusterTemplate, topology domain.Topology, existingContainers []*wekav1alpha1.WekaContainer) ([]*wekav1alpha1.WekaContainer, error) {
	containers := make([]*wekav1alpha1.WekaContainer, 0)

	for _, role := range []string{"compute", "drive", "s3", "envoy"} {
		var numContainers int

		switch role {
		case "compute":
			numContainers = template.ComputeContainers
		case "drive":
			numContainers = template.DriveContainers
		case "s3":
			numContainers = template.S3Containers
		case "envoy":
			numContainers = template.S3Containers
		}

		currentCount := 0
		for _, container := range existingContainers {
			if container.Spec.Mode == role {
				currentCount++
			}
		}

		for i := currentCount; i < numContainers; i++ {
			container, err := NewWekaContainerForWekaCluster(cluster, template, topology, role, NewContainerName(role))
			if err != nil {
				return nil, err
			}
			containers = append(containers, container)
		}
	}
	return containers, nil
}

func NewWekaContainerForWekaCluster(cluster *wekav1alpha1.WekaCluster,
	template domain.ClusterTemplate,
	topology domain.Topology,
	role, name string,
) (*wekav1alpha1.WekaContainer, error) {
	labels := map[string]string{
		"weka.io/mode": role, // in addition to spec for indexing on k8s side for filtering by mode
	}

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

	network, err := resources.GetContainerNetwork(topology.Network)
	if err != nil {
		return nil, err
	}

	secretKey := cluster.GetOperatorSecretName()

	cpuPolicy := cluster.Spec.CpuPolicy
	if topology.ForcedCpuPolicy != "" {
		cpuPolicy = topology.ForcedCpuPolicy
	}

	if cpuPolicy == wekav1alpha1.CpuPolicyAuto {
		return nil, errors.New("CpuPolicyAuto is not supported on cluster level. Should be set explicitly or defined by topology")
	}

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
			Image:               cluster.Spec.Image,
			ImagePullSecret:     cluster.Spec.ImagePullSecret,
			CoreOSBuildSpec:     cluster.Spec.CoreOSBuildSpec,
			WekaContainerName:   strings.Replace(name, "-", "x", -1),
			COSBuildSpec:        cluster.Spec.COSBuildSpec,
			Mode:                role,
			NumCores:            numCores,
			ExtraCores:          extraCores,
			Network:             network,
			Hugepages:           hugePagesNum,
			HugepagesSize:       template.HugePageSize,
			HugepagesOverride:   template.HugePagesOverride,
			NumDrives:           numDrives,
			WekaSecretRef:       v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretKey}},
			DriversDistService:  cluster.Spec.DriversDistService,
			CpuPolicy:           cpuPolicy,
			TracesConfiguration: cluster.Spec.TracesConfiguration,
			Tolerations:         resources.ExpandTolerations([]v1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations),
			Ipv6:                cluster.Spec.Ipv6,
			AdditionalMemory:    additionalMemory,
			ForceAllowDriveSign: topology.ForceSignDrives,
			Group:               containerGroup,
			AdditionalSecrets:   additionalSecrets,
		},
	}

	return container, nil
}
