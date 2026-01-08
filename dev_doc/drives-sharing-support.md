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

Drive sharing supports three allocation modes, each suitable for different use cases:

### Mode 1: Fixed Capacity Per Drive (DriveCapacity)

**Configuration:**
```yaml
spec:
  dynamicTemplate:
    numDrives: 6
    driveCapacity: 1000  # 1000 GiB per virtual drive
```

**Behavior:**
- Each drive container requests `numDrives` virtual drives
- Each virtual drive is allocated exactly `driveCapacity` GiB
- Total capacity per container: `numDrives * driveCapacity`
- Simple and predictable allocation

**Use case:** When you want uniform drive sizes across all containers

---

### Mode 2: Total Container Capacity (ContainerCapacity)

**Configuration:**
```yaml
spec:
  dynamicTemplate:
    driveCores: 4
    containerCapacity: 8000  # 8000 GiB total per container
```

**Behavior:**
- Each drive container requests a total of `containerCapacity` GiB
- The operator intelligently divides capacity across virtual drives
- The number of virtual drives is determined by the allocation strategy and is typically >= `driveCores`
- The allocator tries multiple strategies to find optimal drive sizes
- **ContainerCapacity takes precedence over DriveCapacity when both are set**

**Allocation Strategy:**
The operator generates allocation strategies in order of preference:

1. **Uniform strategies** (preferred):
   - Tries creating `numCores`, `numCores+1`, ..., up to `numCores*3` drives of equal size
   - Only yields strategies where capacity divides evenly
   - Each drive must meet minimum chunk size (1024 GiB)
   - Example: 8000 GiB with 4 cores → tries 4 drives of 2000 GiB, then 5 drives of 1600 GiB, etc.

2. **Non-uniform strategies**:
   - When capacity doesn't divide evenly, distributes as evenly as possible
   - Some drives get slightly larger sizes to account for remainder
   - Example: 9000 GiB with 4 cores → 4 drives: [2251, 2250, 2250, 2249] GiB

For each strategy, the allocator:
- Verifies sufficient physical drive capacity is available
- Simulates allocation across physical drives using round-robin distribution
- Ensures no over-allocation occurs

**Use case:** When you want to specify total capacity per container and let the operator optimize drive distribution

---

### Mode 3: Container Capacity with Drive Type Ratio (ContainerCapacity + DriveTypesRatio)

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

**Behavior:**
- Container capacity is split between TLC and QLC drives based on the ratio
- For the example above with 4:1 ratio:
  - TLC capacity: `12000 * 4 / (4+1) = 9600 GiB`
  - QLC capacity: `12000 * 1 / (4+1) = 2400 GiB`
- The operator allocates TLC and QLC drives separately from their respective physical drive pools
- Each type uses the same allocation strategy logic as Mode 2 (uniform first, then non-uniform)
- The number of virtual drives per type is determined by allocation strategy

**Drive Type Detection:**
- Physical drives are categorized by their `Type` field (TLC or QLC) - fetched from node annotation (weka.io/weka-shared-drives)
- Type information comes from the `weka-sign-drive show --json` output during proxy signing
- If physical drives don't have type information, drive type ratio allocation will fail

**Global Drive Types Ratio:**
If per-cluster `driveTypesRatio` is not explicitly set and `containerCapacity` > 0, the operator automatically applies the global default ratio from Helm values. For example, `driveTypesRatio: {tlc: 4, qlc: 1}` (80% TLC, 20% QLC) in Helm values is directly used as the cluster's `driveTypesRatio`. Default is TLC-only (`tlc: 1, qlc: 0`).

**Minimum Drive Count Constraint:**
When using mixed drive types, each type (TLC and QLC) must receive **at least `driveCores` virtual drives**. This constraint is enforced at allocation time in the allocator.

- **Implementation**: `internal/controllers/allocator/container_allocator.go`
  - Function: `allocateSharedDrivesByCapacityWithTypes()` (mixed types)
  - Function: `allocateSharedDrivesByCapacity()` (single type)
- **Constant**: `MinChunkSizeGiB = 384 GiB` (128 GiB × 3)
- **Validation**: Each type's capacity must be ≥ `driveCores × 384 GiB`
- **Behavior**:
  - For mixed types: `tlcCores = numCores`, `qlcCores = numCores` (if capacity needed)
  - The `driveTypesRatio` is used ONLY for capacity distribution, not drive count distribution
- **Error handling**: Returns clear error message if capacity is insufficient for minimum drive count

**Example:**
- `driveCores: 5`, `containerCapacity: 5000`, `driveTypesRatio: {tlc: 4, qlc: 1}`
- TLC capacity: 4000 GiB (80%) → needs 1920 GiB (5 × 384) ✓
- QLC capacity: 1000 GiB (20%) → needs 1920 GiB (5 × 384) ✗
- Result: Allocation fails with error message

**Use case:** When you have mixed TLC/QLC drives and want to control the ratio of drive types per container

---

### Allocation Process Overview

**High-Level Steps:**
1. Operator reads physical drive information from node annotation (`weka.io/shared-drives`)
2. Builds capacity map for each physical drive (total, claimed, available)
3. Filters drives by minimum capacity (MinChunkSizeGiB = 384 GiB = 128 GiB × 3)
4. Validates minimum drive count constraint (capacity ≥ `driveCores × 384 GiB` per type)
5. Sorts drives by available capacity (most available first)
6. For drive type ratio mode:
   - Separates physical drives by type (TLC vs QLC)
   - Allocates TLC drives first from TLC physical drives
   - Allocates QLC drives from QLC physical drives
   - Fails if either type doesn't have sufficient capacity
6. For each allocation strategy (in order):
   - Simulates allocation across physical drives
   - Uses round-robin distribution for even wear
   - Re-sorts after each allocation to maintain balance
   - If strategy succeeds, stops and uses that strategy
7. On success, creates VirtualDrive entries with:
   - Random VirtualUUID (generated fresh)
   - PhysicalUUID mapping
   - CapacityGiB
   - Serial number
   - Type (TLC/QLC if applicable)

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
- Added `DriveCapacity` (int) to `WekaConfig` - capacity per virtual drive in GiB
- Added `ContainerCapacity` (int) to `WekaConfig` - total capacity per drive container in GiB (takes precedence over DriveCapacity * NumDrives)
- Added `DriveTypesRatio` (object) to `WekaConfig` - specifies the desired ratio of TLC vs QLC drives when allocating drives
  - `Tlc` (int) - TLC drive ratio part (e.g., 4 for 4:1 ratio)
  - `Qlc` (int) - QLC drive ratio part (e.g., 1 for 4:1 ratio)
- If `DriveTypesRatio` is not set and `ContainerCapacity` > 0, the operator automatically applies the global `driveTypesRatio` from Helm values (default: tlc=1, qlc=0)

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
