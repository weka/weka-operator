package domain

const WEKANICs = "weka.io/weka-nics"
const WEKAAllocations = "weka.io/allocations"

type Allocation struct {
	NICs []NIC `json:"nics,omitempty"`
}

type AllocationIdentifier struct {
	Namespace     string `json:"namespace,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
}

func GetAllocationIdentifier(namespace, containerName string) string {
	return namespace + "/" + containerName
}

type Allocations map[string]Allocation
