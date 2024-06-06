package factory

import (
	"errors"
	"fmt"
	"slices"

	"github.com/weka/weka-operator/internal/app/manager/controllers/resources"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

type WekaContainerFactory interface {
	NewWekaContainerForWekaCluster(cluster *wekav1alpha1.WekaCluster,
		ownedResources domain.OwnedResources,
		template domain.ClusterTemplate,
		topology domain.Topology,
		role string, i int,
	) (*wekav1alpha1.WekaContainer, error)
}

func NewWekaContainerFactory(scheme *runtime.Scheme) WekaContainerFactory {
	return &wekaContainerFactory{
		Scheme: scheme,
	}
}

type wekaContainerFactory struct {
	Scheme *runtime.Scheme
}

func (r *wekaContainerFactory) NewWekaContainerForWekaCluster(cluster *wekav1alpha1.WekaCluster,
	ownedResources domain.OwnedResources,
	template domain.ClusterTemplate,
	topology domain.Topology,
	role string, i int,
) (*wekav1alpha1.WekaContainer, error) {
	labels := map[string]string{
		"weka.io/mode": role, // in addition to spec for indexing on k8s side for filtering by mode
	}

	var hugePagesNum int
	var appendSetupCommand string
	var numCores int
	if role == "drive" {
		hugePagesNum = template.DriveHugepages
		appendSetupCommand = cluster.Spec.DriveAppendSetupCommand
		numCores = template.DriveCores
	} else if role == "compute" {
		hugePagesNum = template.ComputeHugepages
		appendSetupCommand = cluster.Spec.ComputeAppendSetupCommand
		numCores = template.ComputeCores
	} else if role == "s3" {
		hugePagesNum = template.S3FrontendHugepages
		numCores = template.S3Cores
	}

	network, err := resources.GetContainerNetwork(topology.Network, &ownedResources)
	if err != nil {
		return nil, err
	}

	potentialDrives := ownedResources.Drives[:]
	availableDrives := topology.GetAllNodesDrives(ownedResources.Node)
	for i := 0; i < len(availableDrives); i++ {
		if slices.Contains(potentialDrives, availableDrives[i]) {
			continue
		}
		potentialDrives = append(potentialDrives, availableDrives[i])
	}
	// Selected by ownership drives are first in the list and will be attempted first, granting happy flow

	secretKey := cluster.GetOperatorSecretName()
	containerPrefix := cluster.GetLastGuidPart()

	if topology.ForcedCpuPolicy != "" {
		cluster.Spec.CpuPolicy = topology.ForcedCpuPolicy
	}

	if cluster.Spec.CpuPolicy == wekav1alpha1.CpuPolicyAuto {
		return nil, errors.New("CpuPolicyAuto is not supported on cluster level. Should be set explicitly or defined by topology")
	}

	coreIds := ownedResources.CoreIds
	if !slices.Contains([]wekav1alpha1.CpuPolicy{wekav1alpha1.CpuPolicyShared}, cluster.Spec.CpuPolicy) {
		coreIds = []int{}
	}

	var s3Params *wekav1alpha1.S3Params
	if role == "s3" {
		s3Params = &wekav1alpha1.S3Params{
			EnvoyPort:      ownedResources.EnvoyPort,
			EnvoyAdminPort: ownedResources.EnvoyAdminPort,
			S3Port:         ownedResources.S3Port,
		}
	}

	nodeConfigMap := ""
	if topology.NodeConfigMapPattern != "" {
		nodeConfigMap = fmt.Sprintf(topology.NodeConfigMapPattern, ownedResources.Node)
	}
	container := &wekav1alpha1.WekaContainer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "weka.weka.io/v1alpha1",
			Kind:       "WekaContainer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.GetContainerName(cluster, role, i),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			NodeAffinity:            ownedResources.Node,
			Port:                    ownedResources.Port,
			AgentPort:               ownedResources.AgentPort,
			Image:                   cluster.Spec.Image,
			ImagePullSecret:         cluster.Spec.ImagePullSecret,
			BuildkitImage:           cluster.Spec.BuildkitImage,
			BuildkitImagePullSecret: cluster.Spec.BuildkitImagePullSecret,
			WekaContainerName:       fmt.Sprintf("%s%ss%d", containerPrefix, role, i),
			Mode:                    role,
			NumCores:                numCores,
			CoreIds:                 coreIds,
			Network:                 network,
			Hugepages:               hugePagesNum,
			HugepagesSize:           template.HugePageSize,
			HugepagesOverride:       template.HugePagesOverride,
			NumDrives:               len(ownedResources.Drives),
			PotentialDrives:         potentialDrives,
			WekaSecretRef:           v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretKey}},
			DriversDistService:      cluster.Spec.DriversDistService,
			CpuPolicy:               cluster.Spec.CpuPolicy,
			AppendSetupCommand:      appendSetupCommand,
			TracesConfiguration:     cluster.Spec.TracesConfiguration,
			S3Params:                s3Params,
			Tolerations:             resources.ExpandTolerations([]v1.Toleration{}, cluster.Spec.Tolerations, cluster.Spec.RawTolerations),
			NodeInfoConfigMap:       nodeConfigMap,
		},
	}

	if err := ctrl.SetControllerReference(cluster, container, r.Scheme); err != nil {
		return nil, err
	}

	return container, nil
}
