package resources

import (
	"fmt"
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

func MapByContainerName(containers ClusterContainersResponse) (ClusterContainersMap, error) {
	result := ClusterContainersMap{}
	for _, container := range containers {
		if ok := result[container.ContainerName]; ok.ContainerName != "" {
			return nil, fmt.Errorf("duplicate container name: %s", container.ContainerName)
		}
		result[container.ContainerName] = container
	}
	return result, nil
}

func HostIdToContainerId(hostId string) (int, error) {
	hostId = strings.Replace(hostId, "HostId<", "", 1)
	hostId = strings.Replace(hostId, ">", "", 1)
	id, err := strconv.Atoi(hostId)
	if err != nil {
		return 0, err
	}
	return id, nil
}
