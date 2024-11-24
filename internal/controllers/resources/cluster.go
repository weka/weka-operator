package resources

import (
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
