// Package ssdproxy provides a reusable client for the node-agent JSONRPC contract
// used to enumerate and manage virtual drives (VIDs) on an ssdproxy's physical drives.
//
// It is shared by the wekacontainer reconciler (per-container VID add/remove during a
// container's own lifecycle) and the stale-virtual-drives operation (fleet-level scan and
// stale-VID reconciliation). The transport (POST to the node-agent /jsonrpc endpoint) and
// the wire types are identical for both callers.
package ssdproxy

// PhysicalDrive represents a physical drive returned by ssd_proxy_list_physical_drives JSONRPC.
type PhysicalDrive struct {
	NumVirtualDrives int    `json:"numVirtualDrives"`
	PhysicalUUID     string `json:"physicalUuid"`
	SizeGB           int    `json:"sizeGB"`
	Model            string `json:"model"`
	PCIAddress       string `json:"pciAddress"`
}

// VirtualDrive represents a virtual drive returned by ssd_proxy_list_virtual_drives JSONRPC.
type VirtualDrive struct {
	VirtualUUID  string `json:"uuid"`
	PhysicalUUID string `json:"-"` // Not in JSON response, populated from request context
	ClusterGUID  string `json:"clusterGuid"`
	SizeGB       int    `json:"sizeGB"`
}

// physicalDrivesResponse is the response from ssd_proxy_list_physical_drives.
type physicalDrivesResponse struct {
	Result  []PhysicalDrive `json:"result"`
	ID      int             `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
}

// virtualDrivesResponse is the node-agent wrapper response for ssd_proxy_list_virtual_drives.
type virtualDrivesResponse struct {
	Message string         `json:"message"`
	Result  []VirtualDrive `json:"result"`
}
