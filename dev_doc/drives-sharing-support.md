# Drive Sharing Support in Weka Kubernetes Operator

**Created:** 2025-11-25
**Feature:** Multi-tenant drive sharing for Weka clusters in Kubernetes

---

## Overview

Drive sharing enables multiple independent Weka clusters running on the same Kubernetes nodes to share physical NVMe drives. Each cluster gets virtual drives carved out from the shared physical drives, allowing efficient resource utilization without requiring dedicated hardware per cluster.

### Key Benefits

- **Resource Efficiency**: Multiple clusters share expensive NVMe hardware
- **Flexibility**: Each cluster can request different drive capacities
- **Isolation**: Virtual drives provide logical separation between clusters
- **Dynamic Allocation**: Drive capacity allocated on-demand, not pre-partitioned

---

## Problem Statement

**Before Drive Sharing:**
- Each Weka cluster required dedicated physical NVMe drives
- Drives could only be used by a single cluster (exclusive ownership)
- Multi-tenancy required separate nodes or inefficient drive allocation
- Expensive hardware sat idle when clusters didn't use full capacity

**After Drive Sharing:**
- Single set of physical NVMe drives shared across multiple clusters
- Each cluster gets virtual drives with specified capacity
- Physical drives managed by a proxy container
- Capacity allocated dynamically based on cluster needs

---

## Architecture Components

### 1. Physical Drives
- NVMe SSDs attached to Kubernetes nodes
- Signed for proxy mode using special proxy signature
- Managed by proxy container, not directly by clusters

### 2. Proxy Container
- Special Weka container (mode: `ssdproxy`) that manages physical drives
- One proxy per node (shared by all clusters on that node)
- Lifecycle: Created when first drive-sharing cluster appears, deleted when last one is removed
- Naming: `weka-drives-proxy-{NODE_NAME}` in operator namespace

### 3. Virtual Drives
- Logical drives carved from physical drives
- Each has a unique UUID and allocated capacity (in GiB)
- Mapped to physical drive via PhysicalUUID
- Multiple virtual drives can exist on one physical drive

### 4. Drive Containers
- Regular Weka drive containers with `UseDriveSharing: true`
- Access physical drives through virtual UUIDs
- Mount `/opt/k8s-weka/ssdproxy/` for proxy communication
- Use virtual drive signing instead of exclusive drive ownership

---

## Drive Allocation Modes

Drive sharing supports two allocation modes:

### Mode 1: Fixed Capacity Per Drive (DriveCapacity + NumDrives) - TLC Only

**Configuration:**
```yaml
spec:
  dynamicTemplate:
    numDrives: 6
    driveCapacity: 1000  # 1000 GiB per virtual drive (TLC only)
```

**Behavior:**
- Each drive container requests `numDrives` virtual drives
- Each virtual drive is allocated exactly `driveCapacity` GiB
- **Only TLC drives are used** - QLC drives are ignored in this mode
- Total capacity per container: `numDrives * driveCapacity`
- Simple and predictable allocation

**Use case:** Simple TLC-only configuration with uniform drive sizes

---

### Mode 2: Container Capacity with Drive Types Ratio (ContainerCapacity + DriveTypesRatio)

**Configuration:**
```yaml
spec:
  dynamicTemplate:
    driveCores: 6
    containerCapacity: 12000  # 12000 GiB total per container
    driveTypesRatio:
      tlc: 4  # 80% TLC
      qlc: 1  # 20% QLC
```

**Important:** When `containerCapacity` is set, the operator **always** uses `driveTypesRatio` to determine drive allocation. If no explicit ratio is provided, the global default from Helm values is used (default: `tlc: 1, qlc: 10`).

**TLC-Only with containerCapacity:**
To allocate only TLC drives, set `qlc: 0`:
```yaml
driveTypesRatio:
  tlc: 1
  qlc: 0
```

**Behavior:**
- Container capacity is split between TLC and QLC drives based on the ratio
- For the example above with 4:1 ratio:
  - TLC capacity: `12000 * 4 / (4+1) = 9600 GiB`
  - QLC capacity: `12000 * 1 / (4+1) = 2400 GiB`
- The operator allocates TLC and QLC drives separately from their respective physical drive pools
- The number of virtual drives per type is determined by allocation strategy

**Allocation Strategy:**
The operator generates allocation strategies in order of preference:

1. **Even distribution** (primary):
   - Tries creating `numCores`, `numCores+1`, ..., up to `maxDrives` drives
   - Distributes capacity as evenly as possible across drives
   - When capacity divides evenly, all drives have identical sizes
   - When it doesn't divide evenly, drives differ by at most 1 GiB (remainder distributed)
   - Each drive must meet minimum chunk size (384 GiB)
   - Example: 8000 GiB with 4 cores → 4 drives of 2000 GiB each
   - Example: 9000 GiB with 4 cores → 4 drives: [2250, 2250, 2250, 2250] GiB
   - Example: 7001 GiB with 3 cores → 3 drives: [2334, 2334, 2333] GiB

2. **Fit-to-physical** (fallback):
   - Creates virtual drives matching actual physical drive capacities
   - Used when ALL even distribution strategies fail due to heterogeneous physical drive sizes
   - Example: Physical drives `[20000, 500, 500]` GiB with 21000 GiB needed, 3 cores, maxDrives=24:
     - Even distribution tries 3, 4, 5, ..., 24 drives → all fail (smallest drives can't hold equal shares)
     - Fit-to-physical creates `[20000, 500, 500]` → succeeds (matches physical layout)
   - Enables allocation on nodes with mixed drive sizes

For each strategy, the allocator:
- Verifies sufficient physical drive capacity is available
- Simulates allocation across physical drives using round-robin distribution
- Ensures no over-allocation occurs

**Drive Type Detection:**
- Physical drives are categorized by their `Type` field (TLC or QLC) - fetched from node annotation (weka.io/weka-shared-drives)
- Type information comes from the `weka-sign-drive show --json` output during proxy signing
- If physical drives don't have type information, drive type ratio allocation will fail

**Minimum Drive Count Constraint:**
The minimum drive count constraint behavior is controlled by the `enforceMinDrivesPerTypePerCore` Helm value (default: `true`).

- **When `enforceMinDrivesPerTypePerCore: true` (default)**: Per-type constraint
  - Each active type (TLC and/or QLC) must receive **at least `driveCores` virtual drives**
  - Constraint: `cores <= tlcDrives` AND `cores <= qlcDrives`
  - More strict, ensures balanced distribution per type

- **When `enforceMinDrivesPerTypePerCore: false`**: Combined constraint
  - Total drives across both types must be at least `driveCores`
  - Constraint: `cores <= tlcDrives + qlcDrives <= maxDrives`
  - More flexible, allows asymmetric distribution between types

- **Implementation**: `internal/controllers/allocator/container_allocator.go`
  - Function: `allocateSharedDrivesByCapacityWithTypes()` (for containerCapacity mode)
  - Function: `allocateSharedDrivesByDrivesNum()` (for driveCapacity + numDrives mode, TLC only)
- **Constant**: `MinChunkSizeGiB = 384 GiB` (128 GiB × 3)
- **Validation**: Capacity must be ≥ `driveCores × 384 GiB` (per type or combined, based on setting)
- **Note**: When only one drive type is active (e.g., `qlc: 0`), both modes behave identically

**Example with `enforceMinDrivesPerTypePerCore: true` (default):**
- `driveCores: 5`, `containerCapacity: 5000`, `driveTypesRatio: {tlc: 4, qlc: 1}`
- TLC capacity: 4000 GiB (80%) → needs 1920 GiB (5 × 384) ✓
- QLC capacity: 1000 GiB (20%) → needs 1920 GiB (5 × 384) ✗
- Result: Allocation fails with error message

**Example with `enforceMinDrivesPerTypePerCore: false`:**
- Same configuration as above
- Total capacity: 5000 GiB → needs 1920 GiB (5 × 384) ✓
- Result: Allocation succeeds with asymmetric distribution (e.g., 4 TLC + 1 QLC drives)

**Use case:** Flexible capacity allocation with control over TLC/QLC distribution

---

### Allocation Process Overview

**High-Level Steps:**
1. Operator reads physical drive information from node annotation (`weka.io/shared-drives`)
2. Builds capacity map for each physical drive (total, claimed, available)
3. Filters drives by minimum capacity (MinChunkSizeGiB = 384 GiB = 128 GiB × 3)
4. Validates minimum drive count constraint (capacity ≥ `driveCores × 384 GiB` per type)
5. Sorts drives by available capacity (most available first)
6. For Mode 1 (driveCapacity + numDrives):
   - Filters for TLC drives only
   - Allocates specified number of drives with fixed capacity
7. For Mode 2 (containerCapacity + driveTypesRatio):
   - Separates physical drives by type (TLC vs QLC)
   - Allocates TLC drives first from TLC physical drives
   - Allocates QLC drives from QLC physical drives (if qlc > 0)
   - Fails if any active type doesn't have sufficient capacity
8. For each allocation strategy (in order):
   - Simulates allocation across physical drives
   - Uses round-robin distribution for even wear
   - Re-sorts after each allocation to maintain balance
   - If strategy succeeds, stops and uses that strategy
9. On success, creates VirtualDrive entries with:
   - Random VirtualUUID (generated fresh)
   - PhysicalUUID mapping
   - CapacityGiB
   - Serial number
   - Type (TLC/QLC)

**Error Handling:**
- `InsufficientDriveCapacityError`: Not enough total capacity available
  - Reports needed vs available capacity
  - Includes drive type if using drive type ratio
- `InsufficientDrivesError`: Not enough drives matching criteria

---

## Complete Flow: From Physical Drives to Running Cluster

### Step 1: Proxy Container Creation
**When:** First drive container with `UseDriveSharing: true` is created on a node
**What happens:**
- Operator creates proxy container on the node
- Proxy container name: `weka-drives-proxy-{NODE_NAME}`
- Multiple drive containers can share the same proxy

**Annotation:** None yet (proxy just created, not configured)

---

### Step 2: Physical Drive Signing for Proxy
**When:** Manual operation executes (WekaManualOperation with `Shared: true`)
**What happens:**
- Python runtime executes on proxy container
- Uses `weka-sign-drive sign proxy` command on each physical device
- Extracts drive information (UUID, serial, capacity, device path) using `weka-sign-drive show --json`

**Weka CLI:**
- `weka-sign-drive sign proxy {device}` - Signs physical drive for proxy mode
- `weka-sign-drive show --json {device}` - Extracts signed drive metadata

**Result stored in node annotation:**
- Annotation: `weka.io/shared-drives`
- Format: `[[uuid, serial, capacityGiB, devicePath], ...]` (compact array)
- Example: `[["fb05d910-...", "S5XYNS0T...", 3840, "/dev/nvme0n1"], ...]`

**Extended resource created:**
- Resource: `weka.io/shared-drives-capacity`
- Value: Total capacity from all shared drives in GiB
- Used by scheduler to prevent over-allocation

---

### Step 3: Drive Container Creation & Virtual Drive Allocation
**When:** User creates drive container with drive sharing enabled (via `DriveCapacity` or `ContainerCapacity`)
**What happens:**
- Operator reads `weka.io/shared-drives` annotation
- Calculates available capacity (total - already claimed by other containers)
- Determines allocation mode:
  - Mode 1: `NumDrives` * `DriveCapacity` (fixed capacity per drive)
  - Mode 2: `ContainerCapacity` (total capacity with intelligent distribution)
  - Mode 3: `ContainerCapacity` + `DriveTypesRatio` (capacity split by drive type)
- Generates random UUIDs for virtual drives
- Maps virtual drives to physical drives using round-robin distribution
- Creates claim in node annotation

**Allocation logic:**
- Total capacity = sum of all physical drives on node
- Claimed capacity = sum of all virtual drives from existing containers
- Available = total - claimed
- If insufficient capacity → allocation fails with clear error
- See **Drive Allocation Modes** section above for detailed strategy information

**Result stored:**
1. **Container Status:**
   - `Status.Allocations.VirtualDrives`: List of allocated virtual drives
   - Each entry: `{VirtualUUID, PhysicalUUID, CapacityGiB, Serial, Type}`

2. **Node Annotation:**
   - Annotation: `weka.io/virtual-drive-claims`
   - Format: `{"virtualUUID": [container, physicalUUID, capacityGiB], ...}` (compact map)
   - Prevents double-allocation of capacity
   - Example: `{"31de939a-...": ["ns:default:container-abc", "fb05d910-...", 4000]}`

---

### Step 4: Cluster Creation & Virtual Drive Signing
**When:** Drive container joins cluster (ClusterID becomes available)
**What happens:**
- Operator executes `AddVirtualDrives()` step on drive container
- For each virtual drive, signs the virtual UUID onto the physical device
- Tracks signed UUIDs in container status

**Weka CLI (executed on drive container):**
- `weka-sign-drive virtual add {devicePath} --virtual-uuid {uuid} --owner-cluster-guid {clusterID} --size {capacityGiB}`
- Modifies physical device header to include virtual UUID metadata
- Associates virtual drive with specific cluster

**Result stored in container status:**
- `Status.AddedVirtualDrives`: List of virtual UUIDs that were signed with `weka-sign-drive virtual add` and subsequently added to the Weka cluster
- Used for idempotency during reconciliation

**Why on drive container?**
- Drive containers have direct device access
- `weka-sign-drive` tool available in container
- Virtual UUID signing modifies device headers

---

### Step 5: Adding Virtual Drives to Cluster
**When:** Virtual drives are signed and container is ready
**What happens:**
- Operator executes `EnsureDrives()` step
- Calls Weka API to add each virtual drive to the cluster
- Uses virtual UUID that was previously signed on the physical device
- Updates `Status.AddedVirtualDrives` after successful addition

**Weka API:**
- `weka cluster drive add {containerID} {virtualUUID}`
- Uses the virtual UUID that was previously signed on the device
- Drive becomes available for cluster use

**Result stored in container status:**
- `Status.AddedDrives`: List of drives added to cluster (Weka's internal tracking)
- `Status.AddedVirtualDrives`: List of virtual UUIDs that completed full lifecycle (allocated → signed → added to cluster)

---

### Step 6: Container Deletion & Virtual Drive Cleanup
**When:** Drive container is deleted
**What happens:**
1. **Remove from Cluster**: Drives removed from Weka cluster via API
2. **Unsign Virtual Drives**: `RemoveVirtualDrives()` executes on drive container
3. **Deallocate Claims**: Virtual drive claims removed from node annotation
4. **Release Capacity**: Capacity becomes available for other containers
5. **Proxy Cleanup**: If last drive container on node, proxy container is deleted

**Weka CLI (executed on drive container):**
- `weka-sign-drive virtual remove {devicePath} --virtual-uuid {uuid} --owner-cluster-guid {clusterID}`
- Removes virtual UUID signature from physical device
- Makes capacity available for reuse

**Result:**
- Container status cleared
- Node annotation updated (claims removed)
- Capacity tracking updated
- Proxy deleted if no more drive containers remain

---

## Operator Changes

### CRD Schema Changes

**WekaCluster:**
- Added `DriveCapacity` (int) to `WekaClusterTemplate` - capacity per virtual drive in GiB
- Added `ContainerCapacity` (int) to `WekaClusterTemplate` - total capacity per drive container in GiB (takes precedence over DriveCapacity * NumDrives)
- Added `DriveTypesRatio` (object) to `WekaClusterTemplate` - specifies the desired ratio of TLC vs QLC drives when allocating drives
  - `Tlc` (int) - TLC drive ratio part (e.g., 4 for 4:1 ratio)
  - `Qlc` (int) - QLC drive ratio part (e.g., 1 for 4:1 ratio)
- If `DriveTypesRatio` is not set and `ContainerCapacity` > 0, the operator automatically applies the global `driveTypesRatio` from Helm values (default: tlc=1, qlc=10)

**WekaContainer:**
- Added `DriveCapacity` (int) to spec - capacity per virtual drive in GiB
- Added `ContainerCapacity` (int) to spec - total capacity per drive container in GiB (takes precedence over DriveCapacity * NumDrives)
- Added `DriveTypesRatio` (object) to spec - specifies the desired ratio of TLC vs QLC drives
  - `Tlc` (int) - TLC drive ratio part
  - `Qlc` (int) - QLC drive ratio part
- Added `VirtualDrives` list to `Status.Allocations` - stores allocated virtual drives with capacity and type information
- Added `AddedVirtualDrives` list to status - tracks virtual drives that completed full lifecycle (allocated → signed with virtual add → added to cluster)

**VirtualDrive:**
- `VirtualUUID` (string) - unique identifier for the virtual drive
- `PhysicalUUID` (string) - physical drive UUID this virtual drive is mapped to
- `CapacityGiB` (int) - allocated capacity in GiB
- `Serial` (string) - physical drive serial number
- `Type` (string) - drive type (TLC or QLC)

**WekaManualOperation:**
- Added `Shared` (bool) to `SignDrivesPayload` - triggers proxy-mode signing

### New Files Created

**Allocator Package:**
- `internal/controllers/allocator/annotations.go` - Annotation constants
- `internal/controllers/allocator/extended_resources.go` - Extended resource management

**Container Controller:**
- `internal/controllers/wekacontainer/funcs_proxy.go` - Proxy container lifecycle

### Modified Files

**Python Runtime (`charts/weka-operator/resources/weka_runtime.py`):**
- Added proxy drive signing functions (all signing types: aws-all, gcp-all, device-paths, etc.)
- Added JSON parsing for proxy drive information
- Updated `sign_drives()` to detect and route proxy mode
- Updated `configure_persistency()` to mount ssdproxy directory

**Node Info (`internal/controllers/allocator/node_info.go`):**
- Added `SharedDriveInfo` struct for proxy drives
- Added parsing for `weka.io/shared-drives` annotation
- Updated `NodeInfoGetter` to return shared drives

**Node Claims (`internal/controllers/allocator/node_claims.go`):**
- Added `VirtualDriveClaim` struct
- Added virtual drive claim parsing and serialization (compact format)
- Updated `BuildClaimsFromContainers()` to include virtual drives
- Updated `RemoveClaims()` to handle virtual drives

**Container Allocator (`internal/controllers/allocator/container_allocator.go`):**
- Added `AllocateSharedDrives()` function for virtual drive allocation
- Updated `AllocateResources()` to branch on drive sharing mode
- Added virtual drive tracking in allocation results

**Drive Functions (`internal/controllers/wekacontainer/funcs_drives.go`):**
- Added `AddVirtualDrives()` - signs virtual UUIDs on drive container
- Added `RemoveVirtualDrives()` - removes virtual UUIDs during deletion
- Updated `EnsureDrives()` - handles both virtual and exclusive drives

**Resource Allocation (`internal/controllers/wekacontainer/funcs_resources_allocation.go`):**
- Added `deallocateVirtualDrives()` - handles blocked drive cleanup
- Updated `deallocateRemovedDrives()` - routes to virtual drive deallocation

**Pod Factory (`internal/controllers/resources/pod.go`):**
- Added ssdproxy volume mount for proxy and drive-sharing containers
- Node path: `/opt/k8s-weka/ssdproxy/` (shared across containers)
- Container mount: `/host-binds/ssdproxy`

**Reconciliation Flows:**
- `flow_active_state.go`: Added `AddVirtualDrives` step after cluster creation
- `flow_deleting_state.go`: Added `RemoveVirtualDrives` step before drive cleanup

---

## Weka APIs & Tools Used

### weka-sign-drive Tool

**Proxy Mode Signing:**
- `weka-sign-drive sign proxy {device}` - Signs physical drive for proxy use
- `weka-sign-drive show --json {device}` - Extracts drive metadata after signing

**Virtual Drive Management:**
- `weka-sign-drive virtual add {device} --virtual-uuid {uuid} --owner-cluster-guid {clusterID} --size {capacityGiB}` - Signs virtual UUID
- `weka-sign-drive virtual remove {device} --virtual-uuid {uuid} --owner-cluster-guid {clusterID}` - Removes virtual UUID

### Weka Cluster API

**Drive Management:**
- `weka cluster drive add {containerID} {virtualUUID}` - Adds virtual drive to cluster using virtual UUID
- `weka cluster drive list {containerID}` - Lists drives attached to container
- `weka cluster drive remove {driveUUID}` - Removes drive from cluster

---

## Key Concepts

### Capacity Tracking

**Three levels of tracking:**

1. **Physical Level** - `weka.io/shared-drives` annotation
   - Lists all physical drives available for sharing
   - Includes total capacity per drive
   - Set during proxy signing

2. **Allocation Level** - `weka.io/virtual-drive-claims` annotation
   - Maps virtual UUIDs to containers
   - Tracks claimed capacity
   - Prevents double-allocation

3. **Extended Resource** - `weka.io/shared-drives-capacity`
   - Kubernetes extended resource on node
   - Total shared capacity in GiB
   - Used by scheduler for pod placement

**Capacity Calculation:**
- Total capacity = sum of all physical drives
- Claimed capacity = sum of all virtual drive allocations
- Available capacity = total - claimed

### Idempotency & Self-Healing

**Virtual Drive Lifecycle Tracking:**
- `Status.Allocations.VirtualDrives` contains allocated virtual drives (random UUIDs generated)
- `Status.AddedVirtualDrives` contains only virtual drives that completed the full lifecycle:
  - Allocated (in Status.Allocations.VirtualDrives)
  - Signed with `weka-sign-drive virtual add`
  - Successfully added to Weka cluster
- On reconciliation, operator skips drives already in `Status.AddedVirtualDrives`
- Ensures drives are not re-signed or re-added on pod restarts

**Allocation:**
- Node annotations rebuilt from container status if corrupted
- Source of truth: `WekaContainer.Status.Allocations`
- Validation step runs before each allocation

### Proxy Container Lifecycle

**Creation:**
- Triggered by first drive container with `UseDriveSharing: true`
- One proxy per node, shared by all clusters

**Ownership:**
- Owned by operator (shared resource across multiple clusters)
- Deletion managed by reference counting drive containers
- Survives individual container deletions
