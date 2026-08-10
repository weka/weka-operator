package consts

// Kubernetes finalizer
const (
	// WekaFinalizer is added to Weka resources — both the CRs (WekaContainer/Cluster/Client) and the
	// pods they own — to protect them from being force-removed out from under the operator, whether
	// manually (e.g. kubectl delete) or automatically (e.g. by a controller during a scale-down).
	// Such a delete only sets the object's deletionTimestamp; the object persists until the operator
	// finishes its cleanup safely and removes the finalizer itself. On a drive pod this keeps the pod
	// object present (Terminating) so the drive can drain gracefully instead of vanishing mid-rebuild.
	WekaFinalizer = "weka.weka.io/do-not-force-delete-unsafe"

	// WekaFinalizerDeprecated is the previous name of WekaFinalizer. It is no longer added, but the
	// operator still removes it everywhere it removes WekaFinalizer, so resources created by an older
	// operator (which carry this name) are never stranded in Terminating on deletion. Retire it in a
	// later release once no live object carries it.
	WekaFinalizerDeprecated = "weka.weka.io/finalizer"
)

// WekaContainerName is the name of the main weka container within a weka pod (as opposed to init
// containers or any injected sidecars). Resource requests must be read from this container by name.
const WekaContainerName = "weka-container"

// Node annotation keys for drive management
const (
	// AnnotationWekaDrives stores drive serial IDs for non-proxy mode.
	// Format: ["SERIAL1", "SERIAL2", ...]
	// Deprecated for writing: use AnnotationWekaFullDrives instead. Kept for backward compatibility reading.
	AnnotationWekaDrives = "weka.io/weka-drives"

	// AnnotationWekaFullDrives stores drive entries with full metadata (serial + capacity_gib) for non-proxy mode.
	// Format: [{"serial":"SERIAL1","capacity_gib":14307},...]
	// This supersedes AnnotationWekaDrives which is deprecated for writing but still supported for reading (fallback).
	AnnotationWekaFullDrives = "weka.io/weka-full-drives"

	// AnnotationBlockedDrives stores blocked drive serial IDs (non-proxy mode)
	// Format: ["SERIAL1", "SERIAL2", ...]
	AnnotationBlockedDrives = "weka.io/blocked-drives"

	// AnnotationSharedDrives stores shared drive information for proxy mode
	// Format: JSON array of objects matching domain.SharedDriveInfo: physical_uuid, serial,
	// capacity_gib, type, model.
	// Example: [{"physical_uuid":"e30cbc29-1b0d-47af-b775-b8d43d2d2e72","serial":"22184A1DEC21",
	// "capacity_gib":7153,"type":"TLC","model":"Micron_7450_MTFDKCC7T6TFR"}]
	AnnotationSharedDrives = "weka.io/weka-shared-drives"

	// AnnotationDriveTypeOverrides stores drive type (TLC/QLC) override rules persisted from
	// the last sign-drives operation that specified them. Re-applied on every sign-drives run.
	// capacity_gib is the truncated GiB value from the annotation above, not the vendor's
	// marketing capacity — a 7.68 TB drive appears as 7153, so rules must use that.
	// Format: [{"model":"Micron_7450_MTFDKCC7T6TFR","type":"QLC"},{"capacityGiB":7153,"type":"TLC"}]
	AnnotationDriveTypeOverrides = "weka.io/drive-type-overrides"

	// AnnotationBlockedDrivesPhysicalUuids stores blocked drive physical UUIDs
	// Format: ["uuid1", "uuid2", ...]
	AnnotationBlockedDrivesPhysicalUuids = "weka.io/blocked-drives-physical-uuids"

	// AnnotationBlockedDrivesVirtualUuids stores blocked virtual drive UUIDs (VIDs), drive-sharing
	// mode only. Unlike the two blocked lists above it does not affect node capacity resources or
	// allocator filtering: virtual UUIDs are random and never reused, so a new allocation can never
	// collide with a blocked one, and the node's physical drive inventory is unchanged.
	// Format: ["uuid1", "uuid2", ...]
	AnnotationBlockedDrivesVirtualUuids = "weka.io/blocked-drives-virtual-uuids"

	// AnnotationSignDrivesHash stores hash of signed drives to track changes
	// Used to determine if drives need to be re-signed
	AnnotationSignDrivesHash = "weka.io/sign-drives-hash"
)

// PodConfigVersionAnnotation is the annotation key set on pods at creation time
// to record which pod config version the pod was created with.
const PodConfigVersionAnnotation = "weka.io/pod-config-version"

// PodConfigCodeVersion should be bumped when the pod spec shape changes in code
// (new env vars, volume mounts, container args, etc.) to trigger pod rotation.
const PodConfigCodeVersion = "1"

// Kubernetes extended resource names
const (
	// ResourceDrives is the extended resource name for tracking available drives (non-proxy mode)
	ResourceDrives = "weka.io/drives"

	// ResourceSharedDrivesCapacity is the extended resource name for tracking shared drive capacity (proxy mode)
	ResourceSharedDrivesCapacity = "weka.io/shared-drives-capacity"

	// ResourceSharedDrivesCapacityTLC is the extended resource name for tracking shared drive capacity of QLC drives (proxy mode)
	ResourcesSharedDrivesCapacityQLC = "weka.io/shared-drives-capacity-qlc"
)
