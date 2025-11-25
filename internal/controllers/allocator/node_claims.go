package allocator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

// ClaimKey uniquely identifies a container making a claim
// Format: {clusterName}:{namespace}:{containerName}
type ClaimKey string

func NewClaimKey(clusterName, namespace, containerName string) ClaimKey {
	return ClaimKey(fmt.Sprintf("%s:%s:%s", clusterName, namespace, containerName))
}

// ParseClaimKey parses a ClaimKey into its components (clusterName, namespace, containerName)
func ParseClaimKey(key ClaimKey) (*Owner, error) {
	parts := strings.Split(string(key), ":")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid ClaimKey format: %s (expected clusterName:namespace:containerName)", key)
	}
	return &Owner{
		OwnerCluster: OwnerCluster{
			ClusterName: parts[0],
			Namespace:   parts[1],
		},
		Container: parts[2],
	}, nil
}

func ClaimKeyFromContainer(container *weka.WekaContainer) ClaimKey {
	owners := container.GetOwnerReferences()
	clusterName := ""
	if len(owners) > 0 {
		clusterName = owners[0].Name
	}
	return NewClaimKey(clusterName, container.Namespace, container.Name)
}

// VirtualDriveClaim represents a virtual drive allocation claim (drive sharing mode)
type VirtualDriveClaim struct {
	Container    ClaimKey `json:"container"`    // Container that owns this virtual drive
	PhysicalUUID string   `json:"physicalUUID"` // Physical drive UUID (from proxy signing)
	CapacityGiB  int      `json:"capacityGiB"`  // Allocated capacity in GiB
}

// NodeClaims represents all resource claims on a node
type NodeClaims struct {
	// Drive serial ID -> ClaimKey
	Drives map[string]ClaimKey `json:"drives,omitempty"`
	// Port range (e.g., "15000,100" for base,count) -> ClaimKey
	Ports map[string]ClaimKey `json:"ports,omitempty"`
	// Virtual drive UUID -> VirtualDriveClaim (drive sharing mode)
	VirtualDrives map[string]VirtualDriveClaim `json:"virtualDrives,omitempty"`
}

func NewNodeClaims() *NodeClaims {
	return &NodeClaims{
		Drives:        make(map[string]ClaimKey),
		Ports:         make(map[string]ClaimKey),
		VirtualDrives: make(map[string]VirtualDriveClaim),
	}
}

// ParseNodeClaims reads claims from node annotations
func ParseNodeClaims(node *v1.Node) (*NodeClaims, error) {
	claims := NewNodeClaims()

	// Parse drive claims
	if driveClaimsStr, ok := node.Annotations[consts.AnnotationDriveClaims]; ok && driveClaimsStr != "" {
		if err := json.Unmarshal([]byte(driveClaimsStr), &claims.Drives); err != nil {
			return nil, fmt.Errorf("failed to parse drive claims: %w", err)
		}
	}

	// Parse port claims
	if portClaimsStr, ok := node.Annotations[consts.AnnotationPortClaims]; ok && portClaimsStr != "" {
		if err := json.Unmarshal([]byte(portClaimsStr), &claims.Ports); err != nil {
			return nil, fmt.Errorf("failed to parse port claims: %w", err)
		}
	}

	// Parse virtual drive claims (compact format: array of values)
	if virtualDriveClaimsStr, ok := node.Annotations[consts.AnnotationVirtualDriveClaims]; ok && virtualDriveClaimsStr != "" {
		// Parse compact format: {"virtualUUID": [container, physicalUUID, capacityGiB], ...}
		var compactClaims map[string][]any
		if err := json.Unmarshal([]byte(virtualDriveClaimsStr), &compactClaims); err != nil {
			return nil, fmt.Errorf("failed to parse virtual drive claims: %w", err)
		}

		// Convert from compact format to VirtualDriveClaim structs
		for virtualUUID, arr := range compactClaims {
			if len(arr) != 3 {
				return nil, fmt.Errorf("virtual drive claim %s has %d fields, expected 3 (container, physicalUUID, capacityGiB)", virtualUUID, len(arr))
			}

			container, ok := arr[0].(string)
			if !ok {
				return nil, fmt.Errorf("virtual drive claim %s: container (field 0) is not a string", virtualUUID)
			}

			physicalUUID, ok := arr[1].(string)
			if !ok {
				return nil, fmt.Errorf("virtual drive claim %s: physicalUUID (field 1) is not a string", virtualUUID)
			}

			var capacityGiB int
			switch v := arr[2].(type) {
			case float64:
				capacityGiB = int(v)
			case int:
				capacityGiB = v
			default:
				return nil, fmt.Errorf("virtual drive claim %s: capacityGiB (field 2) is not a number", virtualUUID)
			}

			claims.VirtualDrives[virtualUUID] = VirtualDriveClaim{
				Container:    ClaimKey(container),
				PhysicalUUID: physicalUUID,
				CapacityGiB:  capacityGiB,
			}
		}
	}

	return claims, nil
}

// SaveToNode writes claims to node annotations with optimistic locking
func (nc *NodeClaims) SaveToNode(ctx context.Context, k8sClient client.Client, node *v1.Node) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "SaveClaimsToNode")
	defer end()

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	// Serialize drive claims
	drivesJSON, err := json.Marshal(nc.Drives)
	if err != nil {
		return fmt.Errorf("failed to marshal drive claims: %w", err)
	}
	node.Annotations[consts.AnnotationDriveClaims] = string(drivesJSON)

	// Serialize port claims
	portsJSON, err := json.Marshal(nc.Ports)
	if err != nil {
		return fmt.Errorf("failed to marshal port claims: %w", err)
	}
	node.Annotations[consts.AnnotationPortClaims] = string(portsJSON)

	// Serialize virtual drive claims (compact array format: [container, physicalUUID, capacityGiB])
	compactVirtualDrives := make(map[string][]any, len(nc.VirtualDrives))
	for virtualUUID, claim := range nc.VirtualDrives {
		compactVirtualDrives[virtualUUID] = []any{
			string(claim.Container),
			claim.PhysicalUUID,
			claim.CapacityGiB,
		}
	}
	virtualDrivesJSON, err := json.Marshal(compactVirtualDrives)
	if err != nil {
		return fmt.Errorf("failed to marshal virtual drive claims: %w", err)
	}
	node.Annotations[consts.AnnotationVirtualDriveClaims] = string(virtualDrivesJSON)

	// Update node with optimistic locking (ResourceVersion)
	err = k8sClient.Update(ctx, node)
	if err != nil {
		if apierrors.IsConflict(err) {
			logger.Info("Node update conflict, another controller modified the node", "node", node.Name)
		}
		return err
	}

	logger.Debug("Successfully saved claims to node", "node", node.Name,
		"drives", len(nc.Drives), "ports", len(nc.Ports), "virtualDrives", len(nc.VirtualDrives))
	return nil
}

// AddDriveClaim adds a drive claim for a container
func (nc *NodeClaims) AddDriveClaim(driveSerial string, claimKey ClaimKey) error {
	if existingClaim, exists := nc.Drives[driveSerial]; exists {
		if existingClaim != claimKey {
			return fmt.Errorf("drive %s already claimed by %s", driveSerial, existingClaim)
		}
		// Already claimed by us, no-op
		return nil
	}
	nc.Drives[driveSerial] = claimKey
	return nil
}

// AddPortClaim adds a port range claim for a container
func (nc *NodeClaims) AddPortClaim(portRange string, claimKey ClaimKey) error {
	if existingClaim, exists := nc.Ports[portRange]; exists {
		if existingClaim != claimKey {
			return fmt.Errorf("port range %s already claimed by %s", portRange, existingClaim)
		}
		// Already claimed by us, no-op
		return nil
	}
	nc.Ports[portRange] = claimKey
	return nil
}

// AddVirtualDriveClaim adds a virtual drive claim for a container (drive sharing mode)
func (nc *NodeClaims) AddVirtualDriveClaim(virtualUUID string, physicalUUID string, capacityGiB int, claimKey ClaimKey) error {
	if existingClaim, exists := nc.VirtualDrives[virtualUUID]; exists {
		if existingClaim.Container != claimKey {
			return fmt.Errorf("virtual drive %s already claimed by %s", virtualUUID, existingClaim.Container)
		}
		// Already claimed by us, no-op
		return nil
	}
	nc.VirtualDrives[virtualUUID] = VirtualDriveClaim{
		Container:    claimKey,
		PhysicalUUID: physicalUUID,
		CapacityGiB:  capacityGiB,
	}
	return nil
}

// RemoveClaims removes all claims for a specific container
func (nc *NodeClaims) RemoveClaims(claimKey ClaimKey) {
	// Remove drive claims
	for driveSerial, owner := range nc.Drives {
		if owner == claimKey {
			delete(nc.Drives, driveSerial)
		}
	}

	// Remove port claims
	for portRange, owner := range nc.Ports {
		if owner == claimKey {
			delete(nc.Ports, portRange)
		}
	}

	// Remove virtual drive claims
	for virtualUUID, claim := range nc.VirtualDrives {
		if claim.Container == claimKey {
			delete(nc.VirtualDrives, virtualUUID)
		}
	}
}

// RemoveDriveClaims removes specific drive claims for a container
func (nc *NodeClaims) RemoveDriveClaims(claimKey ClaimKey, driveSerials []string) {
	for _, driveSerial := range driveSerials {
		if owner, exists := nc.Drives[driveSerial]; exists && owner == claimKey {
			delete(nc.Drives, driveSerial)
		}
	}
}

// GetClaimedDrives returns all drives claimed by a specific container
func (nc *NodeClaims) GetClaimedDrives(claimKey ClaimKey) []string {
	drives := []string{}
	for driveSerial, owner := range nc.Drives {
		if owner == claimKey {
			drives = append(drives, driveSerial)
		}
	}
	return drives
}

// GetClaimedPorts returns all port ranges claimed by a specific container
func (nc *NodeClaims) GetClaimedPorts(claimKey ClaimKey) []string {
	ports := []string{}
	for portRange, owner := range nc.Ports {
		if owner == claimKey {
			ports = append(ports, portRange)
		}
	}
	return ports
}

// GetClaimedVirtualDrives returns all virtual drives claimed by a specific container
func (nc *NodeClaims) GetClaimedVirtualDrives(claimKey ClaimKey) map[string]VirtualDriveClaim {
	virtualDrives := make(map[string]VirtualDriveClaim)
	for virtualUUID, claim := range nc.VirtualDrives {
		if claim.Container == claimKey {
			virtualDrives[virtualUUID] = claim
		}
	}
	return virtualDrives
}

// Equals checks if two NodeClaims are identical
func (nc *NodeClaims) Equals(other *NodeClaims) bool {
	if nc == nil && other == nil {
		return true
	}
	if nc == nil || other == nil {
		return false
	}

	if len(nc.Drives) != len(other.Drives) || len(nc.Ports) != len(other.Ports) || len(nc.VirtualDrives) != len(other.VirtualDrives) {
		return false
	}

	for drive, owner := range nc.Drives {
		if other.Drives[drive] != owner {
			return false
		}
	}

	for portRange, owner := range nc.Ports {
		if other.Ports[portRange] != owner {
			return false
		}
	}

	for virtualUUID, claim := range nc.VirtualDrives {
		otherClaim, exists := other.VirtualDrives[virtualUUID]
		if !exists || otherClaim.Container != claim.Container ||
			otherClaim.PhysicalUUID != claim.PhysicalUUID ||
			otherClaim.CapacityGiB != claim.CapacityGiB {
			return false
		}
	}

	return true
}

// BuildClaimsFromContainers reconstructs node claims from WekaContainer status (source of truth)
func BuildClaimsFromContainers(containers []weka.WekaContainer) *NodeClaims {
	claims := NewNodeClaims()

	for _, container := range containers {
		if container.Status.Allocations == nil {
			continue
		}

		claimKey := ClaimKeyFromContainer(&container)

		// Add drive claims
		for _, drive := range container.Status.Allocations.Drives {
			claims.Drives[drive] = claimKey
		}

		// Add port claims
		if container.Status.Allocations.WekaPort > 0 {
			// Weka port range (100 ports)
			wekaPortRange := fmt.Sprintf("%d,%d",
				container.Status.Allocations.WekaPort,
				WekaPortRangeSize)
			claims.Ports[wekaPortRange] = claimKey

			// Agent port (single port)
			if container.Status.Allocations.AgentPort > 0 {
				agentPortRange := fmt.Sprintf("%d,1",
					container.Status.Allocations.AgentPort)
				claims.Ports[agentPortRange] = claimKey
			}
		}

		// Add virtual drive claims (drive sharing mode)
		for _, vd := range container.Status.Allocations.VirtualDrives {
			claims.VirtualDrives[vd.VirtualUUID] = VirtualDriveClaim{
				Container:    claimKey,
				PhysicalUUID: vd.PhysicalUUID,
				CapacityGiB:  vd.CapacityGiB,
			}
		}
	}

	return claims
}

// ValidateAndRebuildNodeClaims verifies claims match reality and rebuilds if needed
// Returns true if claims were rebuilt
func ValidateAndRebuildNodeClaims(ctx context.Context, k8sClient client.Client, node *v1.Node, namespace string) (bool, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "ValidateAndRebuildNodeClaims")
	defer end()

	nodeName := node.Name

	// Parse current claims from node annotation
	currentClaims, err := ParseNodeClaims(node)
	if err != nil {
		logger.Warn("Failed to parse current node claims, will rebuild from containers", "error", err)
		currentClaims = nil
	}

	// Use KubeService to get containers on this node
	kubeService := kubernetes.NewKubeService(k8sClient)
	containers, err := kubeService.GetWekaContainersSimple(ctx, namespace, nodeName, nil)
	if err != nil {
		return false, fmt.Errorf("failed to list containers on node %s: %w", nodeName, err)
	}

	// Build claims from ground truth (container status)
	actualClaims := BuildClaimsFromContainers(containers)

	// Check if claims match
	if currentClaims != nil && currentClaims.Equals(actualClaims) {
		logger.Debug("Node claims are in sync with container status", "node", nodeName)
		return false, nil
	}

	// Claims are out of sync, rebuild
	logger.Info("Node claims out of sync with container status, rebuilding",
		"node", nodeName,
		"currentDrives", len(currentClaims.Drives),
		"actualDrives", len(actualClaims.Drives),
		"currentPorts", len(currentClaims.Ports),
		"actualPorts", len(actualClaims.Ports))

	// Save actual claims to node
	err = actualClaims.SaveToNode(ctx, k8sClient, node)
	if err != nil {
		return false, fmt.Errorf("failed to rebuild claims on node %s: %w", nodeName, err)
	}

	logger.Info("Successfully rebuilt node claims from container status", "node", nodeName)
	return true, nil
}
