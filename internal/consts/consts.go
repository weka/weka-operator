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
	// AnnotationWekaDrives stores drive serial IDs for non-proxy mode: ["SERIAL1", "SERIAL2", ...].
	// Deprecated for writing: use AnnotationWekaFullDrives; kept for backward-compat reading.
	AnnotationWekaDrives = "weka.io/weka-drives"

	// AnnotationWekaFullDrives stores drive entries with full metadata for non-proxy mode:
	// [{"serial":"SERIAL1","capacity_gib":14307},...]. Supersedes AnnotationWekaDrives (still read
	// as fallback). TLC drives only: full-drives mode has no QLC accounting, so discovery excludes
	// QLC drives here and every consumer charges these entries as TLC.
	AnnotationWekaFullDrives = "weka.io/weka-full-drives"

	// AnnotationBlockedDrives stores blocked drive serial IDs (non-proxy mode): ["SERIAL1", ...].
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

	// AnnotationSignDrivesHash stores a hash of signed drives, used to detect when re-signing is needed.
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
	// ResourceDrives tracks available drives (non-proxy mode). TLC only: it counts the non-blocked
	// entries of AnnotationWekaFullDrives, which excludes QLC.
	ResourceDrives = "weka.io/drives"

	// ResourceSharedDrivesCapacity tracks shared drive capacity (proxy mode).
	ResourceSharedDrivesCapacity = "weka.io/shared-drives-capacity"

	// ResourcesSharedDrivesCapacityQLC tracks shared drive capacity of QLC drives (proxy mode).
	ResourcesSharedDrivesCapacityQLC = "weka.io/shared-drives-capacity-qlc"

	// WekaNumaRegionResourcePrefix is the extended resource name prefix for NUMA region confinement
	// via the device-plugin method; the region index is appended (e.g. "weka.io/numa-region-1").
	WekaNumaRegionResourcePrefix = "weka.io/numa-region-"
)

// DRA (Dynamic Resource Allocation) NUMA confinement, via kubernetes-sigs/dra-driver-cpu.
//
// WekaDraDriverName and WekaDraDeviceClassName share the same value today ("dra.cpu") but are
// distinct concepts owned by different parts of dra-driver-cpu: the driver name is published on
// the ResourceSlices dra-driver-cpu's kubelet plugin advertises (and is therefore the attribute
// domain CEL selectors key into, e.g. device.attributes["dra.cpu"]), while the DeviceClass is a
// separate cluster-scoped object dra-driver-cpu installs that groups those devices. Keep them as
// separate constants so a future dra-driver-cpu release that diverges the two doesn't require
// hunting down which "dra.cpu" string means which thing.
const (
	// WekaDraDriverName is dra-driver-cpu's DRA driver name — the attribute domain used in CEL
	// device.attributes[...] selectors. Verified against a live cluster (kubernetes-sigs/
	// dra-driver-cpu, docs/user/device-attributes.md); not pinnable in unit tests since it's an
	// external project's naming convention, not something this repo defines.
	WekaDraDriverName = "dra.cpu"

	// WekaDraDeviceClassName is the DeviceClass dra-driver-cpu publishes CPU devices under
	// (grouped by NUMA node), used when NUMA confinement uses the "dra" method. Installed by
	// dra-driver-cpu itself, not the weka operator — see checkNumaDeviceClassInstalled in
	// internal/controllers/wekacontainer/funcs_numa_dra.go.
	WekaDraDeviceClassName = "dra.cpu"

	// WekaDraCPUCapacity is dra-driver-cpu's partitionable capacity name, used to request N CPU
	// cores from within the single NUMA-region device a claim allocates. Same external-naming
	// caveat as WekaDraDriverName.
	WekaDraCPUCapacity = "dra.cpu/cpu"

	// WekaNumaClaimName is the pod-local resource claim name referenced by pod.spec.resourceClaims
	// and the weka container's resources.claims when NUMA confinement uses the "dra" method.
	WekaNumaClaimName = "numa"
)
