package resources

import (
	"context"
	"fmt"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"strconv"
	"strings"
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

func (c ClusterContainer) ContainerId() (int, error) {
	return HostIdToContainerId(c.HostId)
}

type ClusterContainersResponse []ClusterContainer

type ClusterContainersMap map[string]ClusterContainer

func HostIdToContainerId(hostId string) (int, error) {
	wekaId, err := WekaIdToInteger("HostId", hostId)
	if err != nil {
		return 0, err
	}

	return wekaId, nil
}

func NodeIdToProcessId(nodeId string) (int, error) {
	wekaId, err := WekaIdToInteger("NodeId", nodeId)
	if err != nil {
		return 0, err
	}

	return wekaId, nil
}

func WekaIdToInteger(prefix string, wekaId string) (int, error) {
	wekaId = strings.Replace(wekaId, prefix+"<", "", 1)
	wekaId = strings.Replace(wekaId, ">", "", 1)
	id, err := strconv.Atoi(wekaId)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func NodeIdToInt(nodeId string) (int, error) {
	nodeId = strings.Replace(nodeId, "NodeId<", "", 1)
	nodeId = strings.Replace(nodeId, ">", "", 1)
	id, err := strconv.Atoi(nodeId)
	if err != nil {
		return 0, err
	}
	return id, nil
}

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
