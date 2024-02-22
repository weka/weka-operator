package resources

import (
	"slices"
	"testing"

	wekav1alpha1 "github.com/weka/weka-operator/internal/app/manager/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestAgentResource(t *testing.T) {
	// Create a new agent resource
	image := "weka/weka-agent"
	client := &wekav1alpha1.Client{
		Spec: wekav1alpha1.ClientSpec{
			Image:               image,
			Version:             "0.0.1",
			BackendIP:           "10.1.2.3",
			ManagementPortBase:  14000,
			ImagePullSecretName: "weka-registry",
		},
	}
	key := types.NamespacedName{
		Name:      "weka-client",
		Namespace: "weka",
	}
	agent, err := AgentResource(client, key)
	if err != nil {
		t.Errorf("Error creating agent resource: %s", err)
	}

	// Container volume mounts should exist in volume definition
	volumes := agent.Spec.Template.Spec.Volumes
	volumeMounts := wekaAgentContainer(client, image).VolumeMounts
	for _, vm := range volumeMounts {
		idx := slices.IndexFunc[[]corev1.Volume, corev1.Volume](volumes, func(v corev1.Volume) bool {
			return v.Name == vm.Name
		})
		if idx == -1 {
			t.Errorf("Expected volume mount %s not found volumes", vm.Name)
		}
	}
}
