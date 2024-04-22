package factories

import (
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
	NewForCluster(cluster *wekav1alpha1.WekaCluster,
		ownedResources domain.OwnedResources,
		template domain.ClusterTemplate,
		topology domain.Topology,
		role string, i int) (*wekav1alpha1.WekaContainer, error)
}

func NewWekaContainerFactory(scheme *runtime.Scheme) WekaContainerFactory {
	return &wekaContainerFactory{
		Scheme: scheme,
	}
}

type wekaContainerFactory struct {
	Scheme *runtime.Scheme
}

func (r *wekaContainerFactory) NewForCluster(cluster *wekav1alpha1.WekaCluster,
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
	if role == "drive" {
		hugePagesNum = template.DriveHugepages
		appendSetupCommand = cluster.Spec.DriveAppendSetupCommand
	} else {
		// TODO: FIX THIS for other roles
		hugePagesNum = template.ComputeHugepages
		appendSetupCommand = cluster.Spec.ComputeAppendSetupCommand
	}

	network, err := resources.GetContainerNetwork(topology.Network)
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

	coreIds := ownedResources.CoreIds
	if !slices.Contains([]wekav1alpha1.CpuPolicy{wekav1alpha1.CpuPolicyShared}, cluster.Spec.CpuPolicy) {
		coreIds = []int{}
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
			NodeAffinity:        ownedResources.Node,
			Port:                ownedResources.Port,
			AgentPort:           ownedResources.AgentPort,
			Image:               cluster.Spec.Image,
			ImagePullSecret:     cluster.Spec.ImagePullSecret,
			WekaContainerName:   fmt.Sprintf("%s%ss%d", containerPrefix, role, i),
			Mode:                role,
			NumCores:            len(ownedResources.CoreIds),
			CoreIds:             coreIds,
			Network:             network,
			Hugepages:           hugePagesNum,
			HugepagesSize:       template.HugePageSize,
			HugepagesOverride:   template.HugePagesOverride,
			NumDrives:           len(ownedResources.Drives),
			PotentialDrives:     potentialDrives,
			WekaSecretRef:       v1.EnvVarSource{SecretKeyRef: &v1.SecretKeySelector{Key: secretKey}},
			DriversDistService:  cluster.Spec.DriversDistService,
			CpuPolicy:           cluster.Spec.CpuPolicy,
			AppendSetupCommand:  appendSetupCommand,
			TracesConfiguration: cluster.Spec.TracesConfiguration,
		},
	}

	if err := ctrl.SetControllerReference(cluster, container, r.Scheme); err != nil {
		return nil, err
	}

	return container, nil
}
