# Drive Sharing Support in Weka Kubernetes Operator

**Status:** Implemented (Phase 1-8 complete, testing pending)
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
**When:** Manual operation executes (WekaManualOperation with `ForProxy: true`)
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
**When:** User creates drive container with `UseDriveSharing: true` and `DriveCapacity: 4000`
**What happens:**
- Operator reads `weka.io/shared-drives` annotation
- Calculates available capacity (total - already claimed by other containers)
- Generates random UUIDs for virtual drives
- Maps virtual drives to physical drives using round-robin distribution
- Creates claim in node annotation

**Allocation logic:**
- Total capacity = sum of all physical drives on node
- Claimed capacity = sum of all virtual drives from existing containers
- Available = total - claimed
- If insufficient capacity → allocation fails with clear error

**Result stored:**
1. **Container Status:**
   - `Status.Allocations.VirtualDrives`: List of allocated virtual drives
   - Each entry: `{VirtualUUID, PhysicalUUID, CapacityGiB, DevicePath}`

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
- Added `DriveSharing` configuration section
- Fields: `Enabled` (bool), `DriveSize` (minimum capacity in GiB)

**WekaContainer:**
- Added `UseDriveSharing` (bool) to spec - enables sharing mode for this container
- Added `DriveCapacity` (int) to spec - capacity per virtual drive in GiB (minimum 1024)
- Added `VirtualDrives` list to `Status.Allocations` - stores allocated virtual drives
- Added `AddedVirtualDrives` list to status - tracks virtual drives that completed full lifecycle (allocated → signed with virtual add → added to cluster)

**WekaManualOperation:**
- Added `ForProxy` (bool) to `SignDrivesPayload` - triggers proxy-mode signing

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

**Deletion:**
- Only when last drive container on node is deleted
- `cleanupProxyIfNeeded()` counts remaining drive containers
- Cleanup step runs at end of deletion flow
