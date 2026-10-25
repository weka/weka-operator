package resources

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/config"
)

type ClusterContainer struct {
	State          string   `json:"state"`
	Status         string   `json:"status"`
	ManagementPort int      `json:"mgmt_port"`
	HostIp         string   `json:"host_ip"`
	Ips            []string `json:"ips"`
	ContainerName  string   `json:"container_name"`
	HostId         string   `json:"host_id"`
}

type ClusterContainersResponse []ClusterContainer

type ClusterContainersMap map[string]ClusterContainer

func SelectActiveContainerWithRole(ctx context.Context, containers []*wekav1alpha1.WekaContainer, role string) (*wekav1alpha1.WekaContainer, error) {
	for _, container := range containers {
		if container.Spec.Mode != role {
			continue
		}
		if container.Status.ClusterContainerID == nil {
			continue
		}
		return container, nil
	}

	err := fmt.Errorf("no container with role %s found", role)
	return nil, err
}

// GetPvcConfig determines the PVC configuration to use based on the provided spec-level config and the global default.
// It returns nil if neither is specified.
func GetPvcConfig(specPvcConfig *wekav1alpha1.PVCConfig) *wekav1alpha1.PVCConfig {
	if specPvcConfig != nil {
		return specPvcConfig
	}
	if config.Config.LocalDataPvc != "" {
		return &wekav1alpha1.PVCConfig{
			Name: config.Config.LocalDataPvc,
			Path: "",
		}
	}
	return nil
}
