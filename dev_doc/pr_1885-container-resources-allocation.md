# Hybrid Resource Allocation Architecture

## Overview

The Weka Operator now uses a **hybrid resource allocation approach** that combines global ConfigMap storage with per-container node annotations. This replaces the previous fully centralized ConfigMap-based system.

## The Problem

**Previous Approach:**
- All resource allocations (global cluster ranges AND per-container drives/ports) stored in a single ConfigMap
- Sequential allocation from WekaCluster reconciler meant slow container creation at scale
- No self-healing - lost allocations couldn't be reconstructed
- **ConfigMap size limited to 1MB** - Large clusters with many containers could hit this limit

**Key Issues:**
1. **Size limitation** - ConfigMap 1MB limit restricts maximum cluster size (per-container allocations grow linearly)
2. **Sequential allocation bottleneck** - WekaCluster reconciler allocated resources for all containers sequentially; creating many containers required waiting for each allocation to complete one by one
3. **No self-healing** - If ConfigMap was corrupted, allocations were lost

## The Solution: Hybrid Approach

**New Architecture:**

```
Global Allocations (ConfigMap)
├── Cluster port ranges (base port + range size)
└── Singleton ports (LB, S3, LbAdmin)

Per-Container Allocations (Node Annotations)
├── weka.io/drive-claims: {"drive1": "cluster1:ns:container1", ...}
└── weka.io/port-claims: {"35000,100": "cluster1:ns:container1", "35303,1": "cluster1:ns:container1", ...}

Source of Truth (WekaContainer Status)
└── Status.Allocations: {Drives: [...], WekaPort: 35000, AgentPort: 35303}
```

**Key Principles:**

1. **ConfigMap for global resources only** - Cluster-level port ranges and singleton ports
2. **Node annotations for per-container resources** - Drives and ports for each container
3. **WekaContainer Status as source of truth** - Allocations persist even if node is deleted
4. **Parallel allocation** - Each WekaContainer controller allocates independently
5. **Self-healing** - Allocations rebuilt from Status if node annotations are lost

## Why It's Better

| Aspect | Old Approach | New Approach |
|--------|--------------|--------------|
| **Scalability** | Limited by ConfigMap 1MB size | No limit - annotations distributed across nodes |
| **Allocation Speed** | Sequential - must allocate all containers one by one | Parallel - each WekaContainer controller allocates independently |
| **Recovery** | No self-healing - lost allocations unrecoverable | Self-healing - `ValidateAndRebuildNodeClaims()` runs before each allocation, rebuilds from Status |
| **Data Distribution** | Single ConfigMap for all per-container allocations | Distributed - each node has only its container allocations |
| **Cleanup** | Allocations in ConfigMap, cleaned up with containers | Node annotations, cleaned up automatically with containers |

## What Was Changed

### 1. Resource Allocation Flow

**Old:**
```
WekaCluster reconciler creates all containers
  → For each container sequentially:
    → AllocateContainers() → Read ConfigMap → Allocate → Write ConfigMap
    → Wait for completion
  → All allocations complete
```

**New:**
```
WekaCluster reconciler creates all containers
  → Each WekaContainer controller independently:
    → AllocateResources()
      → ValidateAndRebuildNodeClaims() (self-healing from Status)
      → Allocate resources
      → Update Node Annotations + Status.Allocations
  → All allocations happen in parallel
```

### 2. Allocation Components

**Removed:**
- `NodeAllocations` and `NodeAllocMap` structures from ConfigMap
- `AllocateContainers()` function (240+ lines) - centralized allocation in WekaCluster controller
- Per-container allocation logic from WekaCluster reconciler

**Added:**
- `ContainerResourceAllocator` service for per-container allocation in WekaContainer controller
- `NodeClaims` structure (JSON in node annotations)
- `ClaimKey` format: `{clusterName}:{namespace}:{containerName}` for ownership tracking
- `ValidateAndRebuildNodeClaims()` for self-healing - **runs before each allocation** to rebuild from Status.Allocations
- `CleanupClaimsOnNode()` for finalizer-based cleanup
- `ParseClaimKey()` for parsing ownership from claim keys
- `AggregateNodePortClaims()` for preventing conflicts between global and per-container allocations

### 3. Concurrency Protection

**Old:** Kubernetes optimistic locking on ConfigMap (ResourceVersion).  
**New:** Kubernetes optimistic locking on Node (ResourceVersion) - per-node, not cluster-wide

### 4. Cleanup Mechanism

**Old:**
```
Container deleted → ConfigMap updated separately (if needed)
```

**New:**
```
Container deleted → Finalizer runs → CleanupClaimsOnNode() → Node annotations updated → Done
```

### 5. Self-Healing

**New capability:**
Self-healing happens automatically during resource allocation via `ValidateAndRebuildNodeClaims()`:

```
Before each allocation:
  → ValidateAndRebuildNodeClaims() runs
  → Reads Status.Allocations from all containers in namespace
  → Rebuilds missing/corrupted node annotations
  → Ensures consistency before allocating new resources

Example scenarios:
  - Node deleted → Container recreated on new node → ValidateAndRebuildNodeClaims() rebuilds claims
  - Manual annotation edit → Next allocation → ValidateAndRebuildNodeClaims() restores from Status
  - Annotation corruption → Next allocation → ValidateAndRebuildNodeClaims() rebuilds from Status
```

**Key point:** Node annotations are only used during allocation to prevent conflicts. The WekaContainer Status.Allocations is the authoritative source of truth.

### 6. Global Range Allocation Enhancement

**Critical Fix:**
Global port allocation (LB, S3, LbAdmin) now checks per-container allocations to prevent conflicts:

```
AllocateClusterRange() → AggregateNodePortClaims() → Combine with global ranges
→ EnsureGlobalRangeWithOffset() → Allocate avoiding all conflicts
```

This prevents global singleton ports from conflicting with container agent ports (both use offset range `base+300` to `base+500`).

## Migration Path

**No migration required** - the operator handles both approaches:

1. **Existing containers**: ConfigMap allocations remain until container is recreated
2. **New containers**: Use hybrid approach automatically
3. **Gradual transition**: As containers are recreated (upgrades, failures), they adopt the new approach
4. **ConfigMap cleanup**: Old NodeMap data is ignored, eventually removed when all containers migrate

## Key Architectural Decisions

### Why Node Annotations?

1. **Natural scoping** - Resources are node-specific (drives, ports)
2. **Built-in locking** - Kubernetes provides optimistic locking per node
3. **Automatic lifecycle** - Annotations deleted when node is deleted
4. **Easy inspection** - `kubectl get node <name> -o yaml` shows all claims

### Why Keep ConfigMap?

1. **Cluster-level resources** - Port ranges span multiple nodes
2. **Singleton ports** - LB, S3 ports need cluster-wide uniqueness
3. **Existing investment** - ConfigMapStore already handles compression, locking
4. **Migration path** - Allows gradual transition from old approach

### Why WekaContainer Status as Source of Truth?

1. **Survives node deletion** - Status is cluster-level, not node-specific
2. **Kubernetes native** - Built-in persistence and versioning
3. **Visible in kubectl** - Easy to inspect and debug
4. **Enables self-healing** - Can reconstruct node annotations from Status

## Benefits Summary

✅ **Unlimited Scalability** - No ConfigMap size limit, supports large clusters.  
✅ **Faster Allocation** - Parallel allocation by WekaContainer controllers instead of sequential by WekaCluster.  
✅ **Self-Healing** - Node annotations automatically rebuilt from Status.Allocations before each allocation.
✅ **Better Distribution** - Node-level locking instead of single ConfigMap for all allocations.  
✅ **Improved Resilience** - Allocations survive node deletion via WekaContainer Status.  
✅ **Simpler Code** - Cleaner architecture with distributed allocation.  
✅ **Zero Downtime** - No migration needed, gradual adoption
