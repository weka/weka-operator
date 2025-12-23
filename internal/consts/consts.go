package consts

// Kubernetes finalizer
const (
	// WekaFinalizer is the finalizer added to Weka resources to ensure proper cleanup
	WekaFinalizer = "weka.weka.io/finalizer"
)

// Node annotation keys for drive management
const (
	// AnnotationWekaDrives stores drive serial IDs for non-proxy mode
	// Format: ["SERIAL1", "SERIAL2", ...]
	AnnotationWekaDrives = "weka.io/weka-drives"

	// AnnotationBlockedDrives stores blocked drive serial IDs (non-proxy mode)
	// Format: ["SERIAL1", "SERIAL2", ...]
	AnnotationBlockedDrives = "weka.io/blocked-drives"

	// AnnotationSharedDrives stores shared drive information for proxy mode
	// Format: [[uuid, serial, capacityGiB, devicePath], ...]
	// Example: [["550e8400-e29b-41d4-a716-446655440000", "SERIAL123", 7000, "/dev/nvme0n1"]]
	AnnotationSharedDrives = "weka.io/weka-shared-drives"

	// AnnotationBlockedDrivesPhysicalUuids stores blocked drive physical UUIDs
	// Format: ["uuid1", "uuid2", ...]
	AnnotationBlockedDrivesPhysicalUuids = "weka.io/blocked-drives-physical-uuids"

	// AnnotationSignDrivesHash stores hash of signed drives to track changes
	// Used to determine if drives need to be re-signed
	AnnotationSignDrivesHash = "weka.io/sign-drives-hash"

	// AnnotationDriveClaims stores drive allocation claims per container (hybrid allocation model)
	// Format: {"serial1": "clusterName:namespace:containerName", ...}
	AnnotationDriveClaims = "weka.io/drive-claims"

	// AnnotationPortClaims stores port allocation claims per container (hybrid allocation model)
	// Format: {"basePort,count": "clusterName:namespace:containerName", ...}
	AnnotationPortClaims = "weka.io/port-claims"

	// AnnotationVirtualDriveClaims stores virtual drive allocation claims (drive sharing mode)
	// Format: {"virtualUUID": {"container": "clusterName:namespace:containerName", "physicalUUID": "550e8400-...", "capacityGiB": 2048}, ...}
	AnnotationVirtualDriveClaims = "weka.io/virtual-drive-claims"
)

// Kubernetes extended resource names
const (
	// ResourceDrives is the extended resource name for tracking available drives (non-proxy mode)
	ResourceDrives = "weka.io/drives"

	// ResourceSharedDrivesCapacity is the extended resource name for tracking shared drive capacity (proxy mode)
	ResourceSharedDrivesCapacity = "weka.io/shared-drives-capacity"
)
