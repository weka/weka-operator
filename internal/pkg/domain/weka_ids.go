package domain

import (
	"strconv"
	"strings"
)

func HostIdToContainerId(hostId string) (int, error) {
	return WekaIdToInteger("HostId", hostId)
}

func NodeIdToProcessId(nodeId string) (int, error) {
	return WekaIdToInteger("NodeId", nodeId)
}

func FsIdToInteger(fsId string) (int, error) {
	return WekaIdToInteger("FSId", fsId)
}

func DriveIdToInteger(driveId string) (int, error) {
	return WekaIdToInteger("DiskId", driveId)
}

func WekaIdToInteger(prefix, wekaId string) (int, error) {
	wekaId = strings.Replace(wekaId, prefix+"<", "", 1)
	wekaId = strings.Replace(wekaId, ">", "", 1)
	id, err := strconv.Atoi(wekaId)
	if err != nil {
		return 0, err
	}
	return id, nil
}
