package allocator

import (
	"slices"
)

func (n *NodeAllocations) AllocatePort(owner Owner, basePort int, portStep int, portMap map[Owner]int) {
	allocatedList := []int{}
	for _, allocatedPort := range portMap {
		allocatedList = append(allocatedList, allocatedPort)
	}
	slices.Sort(allocatedList)

	scanPosition := 0
OUTER:
	for targetPort := basePort; targetPort < 60000; targetPort += portStep {
		for i, allocatedPort := range allocatedList[scanPosition:] {
			if targetPort == allocatedPort {
				scanPosition = i
				continue OUTER
			}
			if targetPort < allocatedPort {
				break
			}
		}
		portMap[owner] = targetPort
		return
	}
}

func (n *NodeAllocations) GetFreeEthSlots(ethSlots []string) []string {
	usedEthSlots := 0
	for _, slots := range n.EthSlots {
		usedEthSlots += len(slots)
	}
	return ethSlots[usedEthSlots:]
}

func (n *NodeAllocations) AllocateClusterWideRolePort(owner OwnerRole, port int, step int, ports AllocRoleMap) {
	if _, ok := ports[owner]; ok {
		return
	}
	allocatedList := []int{}
	for _, allocatedPort := range ports {
		allocatedList = append(allocatedList, allocatedPort)
	}
	slices.Sort(allocatedList)

	scanPosition := 0
OUTER:
	for targetPort := port; targetPort < 60000; targetPort += step {
		for i, allocatedPort := range allocatedList[scanPosition:] {
			if targetPort == allocatedPort {
				scanPosition = i
				continue OUTER
			}
			if targetPort < allocatedPort {
				break
			}
		}
		ports[owner] = targetPort
		return

	}
}

func (n *NodeAllocations) DeallocateNodeRange(owner Owner, port int) {
	for i, r := range n.AllocatedRanges[owner] {
		if r.Base == port {
			delete(n.AllocatedRanges[owner], i)
			return
		}
	}
}
