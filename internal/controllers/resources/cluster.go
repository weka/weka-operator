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
			// HACK: If already exists, but is dead - skipping it,
			// we would not reach this flow if we would not already delete previous on operator side
			// Proper solution would be to bind WekaContainer with actual system Container on deletion
			// But then need to have special handling of full deletion process, next iteration
			// Also, nothing guarantees order here... It is bizzare we cannot find container ID locally
			if ok.Status == "DOWN" {
				if container.Status != "DOWN" {
					result[container.ContainerName] = container
					goto INSERT
				}
			}
			if container.Status == "DOWN" {
				if ok.Status != "DOWN" {
					continue
				}
			}

			if container.Status == "DOWN" {
				if ok.Status == "DOWN" {
					goto INSERT
				}
			}
			return nil, fmt.Errorf("duplicate container name: %s", container.ContainerName)
		}
	INSERT:
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
