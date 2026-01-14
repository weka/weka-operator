# Drive Sharing (Multi-Tenant Storage)

Drive sharing enables multiple independent Weka clusters to share the same physical NVMe drives on Kubernetes nodes. Instead of dedicating entire drives to a single cluster, physical drives are partitioned into virtual drives that can be allocated across multiple clusters.

## When to Use Drive Sharing

**Use drive sharing when:**
- Running multiple Weka clusters on the same Kubernetes nodes
- You want flexible capacity allocation per cluster without pre-partitioning drives
- Hardware costs justify multi-tenant resource sharing
- Clusters have varying capacity requirements

**Use exclusive drives (traditional mode) when:**
- Running a single Weka cluster per node
- Maximum performance is critical (eliminates proxy layer overhead)
- Simpler operations are preferred

## Architecture Overview

Drive sharing introduces two key components:

1. **Proxy Container**: A special `ssdproxy` mode container that manages physical drives. One proxy per node, shared by all clusters.

2. **Virtual Drives**: Logical drives carved from physical drives. Each cluster's drive containers use virtual drives instead of exclusive physical drives.

```
Physical Drive (3840 GiB)
├── Virtual Drive 1 → Cluster A (1000 GiB)
├── Virtual Drive 2 → Cluster B (1500 GiB)
└── Available: 1340 GiB
```

## Configuration Guide

### Step 1: Sign Drives for Proxy Mode

Before using drive sharing, physical drives must be signed for proxy mode (instead of exclusive cluster ownership).

**Using WekaPolicy (recommended for ongoing operations):**

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: sign-drives-for-proxy
  namespace: weka-operator-system
spec:
  type: sign-drives
  payload:
    signDrivesPayload:
      shared: true  # Enable proxy mode signing
      type: all-not-root
      nodeSelector:
        weka.io/supports-backends: "true"
```

**Using WekaManualOperation (one-time operation):**

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: sign-drives-for-proxy
  namespace: weka-operator-system
spec:
  action: sign-drives
  payload:
    signDrivesPayload:
      shared: true
      type: all-not-root
      nodeSelector:
        weka.io/supports-backends: "true"
```

**What happens:**
- Drives are signed with proxy system GUID (not a cluster-specific GUID)
- Drive information stored in node annotation: `weka.io/shared-drives`
- Extended resource created: `weka.io/shared-drives-capacity` (total GiB available)

**Important:** Do NOT specify `spec.image` - the operator automatically uses the appropriate signing image.

### Step 2: Configure Cluster with Drive Sharing

Enable drive sharing by setting capacity fields in `spec.dynamicTemplate`. Choose one of two allocation modes:

#### Mode 1: Fixed Capacity Per Virtual Drive (TLC Only)

Specify capacity per individual virtual drive using `driveCapacity` with `numDrives`.

**Important:** This mode allocates **only TLC drives**. It cannot be used with `driveTypesRatio`. For mixed TLC/QLC drives or QLC-only, use `containerCapacity` with `driveTypesRatio` instead (see Mode 2).

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-a
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 3
    numDrives: 6
    driveCapacity: 1000  # Each virtual drive gets 1000 GiB (TLC only)
    computeContainers: 6
    computeCores: 3
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

**Result:** Each drive container requests 6 TLC virtual drives × 1000 GiB = 6000 GiB total per container.

**Use case:** Simple TLC-only configuration with uniform drive sizes.

---

#### Mode 2: Container Capacity with Drive Types Ratio

Specify total capacity per container using `containerCapacity`. The `driveTypesRatio` controls how capacity is split between TLC and QLC drives.

**Important:** When `containerCapacity` is set, the operator **always** uses the `driveTypesRatio` to determine drive allocation. If no explicit ratio is provided, the global default from Helm values is used (default: `tlc: 1, qlc: 10`).

##### TLC-Only with containerCapacity

To allocate only TLC drives while using `containerCapacity`, set `qlc: 0`:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-tlc-only
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 4
    containerCapacity: 8000  # Total capacity per container
    driveTypesRatio:
      tlc: 1  # 100% TLC
      qlc: 0  # No QLC drives
    computeContainers: 6
    computeCores: 3
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

**Result:** Each drive container receives 8000 GiB total from TLC drives only, distributed across multiple virtual drives (typically `driveCores` or more drives).

##### Mixed TLC/QLC Drives

For mixed drive types, specify both `tlc` and `qlc` values:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-mixed
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 6
    containerCapacity: 12000
    driveTypesRatio:
      tlc: 4  # 80% TLC (high-performance)
      qlc: 1  # 20% QLC (cost-optimized)
    computeContainers: 6
    computeCores: 3
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

**Result:**
- TLC capacity: 12000 × 4/(4+1) = 9600 GiB from TLC physical drives
- QLC capacity: 12000 × 1/(4+1) = 2400 GiB from QLC physical drives
- Total: 12000 GiB per container
- **Drive count**: Minimum 6 TLC drives AND 6 QLC drives (equal to `driveCores`)

**Requirements:**
- Physical drives must have type information (TLC or QLC)
- Type information comes from `weka-sign-drive show --json` during proxy signing
- Separate physical drive pools for TLC and QLC must have sufficient capacity

**Allocation strategy:** See [Virtual Drive Allocation Strategies](#virtual-drive-allocation-strategies) for details.

**Minimum Capacity Constraint:**

The minimum drive count constraint behavior is controlled by the `enforceMinDrivesPerTypePerCore` Helm value (default: `true`).

**When `enforceMinDrivesPerTypePerCore: true` (default) - Per-type constraint:**
- Each active type (TLC and/or QLC) must get at least `driveCores` virtual drives
- **TLC capacity** ≥ `driveCores × 384 GiB` (if tlc > 0)
- **QLC capacity** ≥ `driveCores × 384 GiB` (if qlc > 0)

**When `enforceMinDrivesPerTypePerCore: false` - Combined constraint:**
- Total drives across both types must be at least `driveCores`
- **Total capacity** ≥ `driveCores × 384 GiB`
- More flexible, allows asymmetric distribution between types

**Note:** When only one drive type is active (e.g., `qlc: 0`), both modes behave identically.

**Example validation with `enforceMinDrivesPerTypePerCore: true`:**
```yaml
driveCores: 6
containerCapacity: 12000
driveTypesRatio: {tlc: 4, qlc: 1}
```
- TLC capacity: 9600 GiB ≥ 2304 GiB (6 × 384) ✓
- QLC capacity: 2400 GiB ≥ 2304 GiB (6 × 384) ✓
- **Valid configuration**

**Invalid example with `enforceMinDrivesPerTypePerCore: true`:**
```yaml
driveCores: 5
containerCapacity: 5000
driveTypesRatio: {tlc: 4, qlc: 1}
```
- TLC capacity: 4000 GiB ≥ 1920 GiB (5 × 384) ✓
- QLC capacity: 1000 GiB < 1920 GiB (5 × 384) ✗
- **Error:** "insufficient QLC capacity: need at least 1920 GiB to allocate 5 QLC drives, but only 1000 GiB available"

**Same example with `enforceMinDrivesPerTypePerCore: false`:**
- Total capacity: 5000 GiB ≥ 1920 GiB (5 × 384) ✓
- **Valid configuration** - allocation succeeds with asymmetric distribution (e.g., 4 TLC + 1 QLC drives)

**To fix insufficient capacity errors:**
1. Increase `containerCapacity`
2. Decrease `driveCores`
3. Adjust `driveTypesRatio` to allocate more capacity to the constrained type
4. Set `enforceMinDrivesPerTypePerCore: false` in Helm values to use combined constraint

**Use case:** Flexible capacity allocation with control over TLC/QLC distribution.

---

### Virtual Drive Allocation Strategies

The operator uses two strategies to allocate virtual drives from physical capacity. Both strategies ensure each drive meets the **minimum 384 GiB** requirement.

#### Strategy 1: Even Distribution (primary)

Distributes capacity as evenly as possible across virtual drives. When capacity divides evenly, all drives have identical sizes. When it doesn't divide evenly, drives differ by at most 1 GiB (remainder distributed across drives).

**Example:** 7001 GiB with 3 drives → `[2334, 2334, 2333]` GiB

#### Strategy 2: Fit-to-Physical (fallback)

Creates virtual drives matching actual physical drive capacities. Used as a fallback when even distribution fails due to heterogeneous physical drive sizes.

**Example scenario:**
- Physical drives: `[20000, 500, 500]` GiB
- Requested: 21000 GiB with 3 cores
- Even distribution tries `[7000, 7000, 7000]` → **fails** (only one drive can hold 7000 GiB)
- Fit-to-physical creates `[20000, 500, 500]` → **succeeds** (matches physical layout)

**Drive splitting:** If fit-to-physical doesn't create enough drives to meet `numCores`, it splits the largest drives:
- Physical: `[20000, 500, 500]` with 21000 GiB needed and **4 cores**
- Initial: `[20000, 500, 500]` = 3 drives (< numCores)
- After splitting: `[10000, 10000, 500, 500]` = 4 drives ✓

This fallback enables allocation on nodes with mixed drive sizes where even distribution would fail.

#### Drive Count Constraints

The constraint behavior depends on the `enforceMinDrivesPerTypePerCore` Helm setting:

**When `enforceMinDrivesPerTypePerCore: true` (default) - Per-type constraint:**
```
driveCores <= tlcDrives (if tlc > 0)
driveCores <= qlcDrives (if qlc > 0)
tlcDrives + qlcDrives <= driveCores × maxVirtualDrivesPerCore
```

Each active drive type must independently satisfy the minimum drive count.

**When `enforceMinDrivesPerTypePerCore: false` - Combined constraint:**
```
driveCores <= tlcDrives + qlcDrives <= driveCores × maxVirtualDrivesPerCore
```

This allows flexible distribution between drive types. If one type has limited capacity, the other can compensate.

#### Strategy Selection

The operator allocates TLC first, then QLC. The allocation behavior depends on the constraint mode:

**Per-type constraint (`enforceMinDrivesPerTypePerCore: true`):**
1. **TLC allocation:** Start with min=driveCores, find first successful strategy
2. **QLC allocation:** Start with min=driveCores, find first successful strategy
3. No iteration needed - each type has fixed minimum

**Combined constraint (`enforceMinDrivesPerTypePerCore: false`):**
1. **TLC allocation:** Start with min=1 drive, find first successful strategy
2. **QLC allocation:** Set min = max(1, driveCores - tlcDrives) to ensure combined total ≥ driveCores
3. **Iteration:** If QLC can't meet its minimum, increase TLC's minimum and retry

For each allocation, strategies are generated in this order:
1. **Even distribution strategies** (min, min+1, min+2, ... up to max drives)
2. **Fit-to-physical fallback** (if constraints are satisfied)

The first strategy that fits available physical drives is used.

**Early termination:** The search stops as soon as drive sizes fall below 384 GiB, since increasing the drive count would only make sizes smaller.

#### Configuration: maxVirtualDrivesPerCore

The `maxVirtualDrivesPerCore` setting controls the maximum drive count flexibility during allocation. It defines the multiplier applied to `driveCores` when searching for allocation strategies.

**Helm Configuration:**

```yaml
maxVirtualDrivesPerCore: 8  # Default value
```

**Behavior:**
- Maximum virtual drives = `driveCores × maxVirtualDrivesPerCore`

#### Example

**Configuration:**
```yaml
driveCores: 3
containerCapacity: 7000
driveTypesRatio:
  tlc: 1
  qlc: 4
```

**Capacity split:** TLC = 1400 GiB, QLC = 5600 GiB

**TLC allocation (1400 GiB, min=1):**

| Drives | Uniform? | Size | ≥384 GiB? | Result |
|--------|----------|------|-----------|--------|
| 1 | 1400÷1=1400 | Yes | Yes | **use** |

**QLC allocation (5600 GiB, min=max(1, 3-1)=2):**

| Drives | Uniform? | Size | ≥384 GiB? | Result |
|--------|----------|------|-----------|--------|
| 2 | 5600÷2=2800 | Yes | Yes | **use** |

**Final allocation:**
- TLC: 1 drive (1400 GiB)
- QLC: 2 drives (2800 GiB each)
- Total: 3 virtual drives, 7000 GiB

#### Example: Asymmetric Capacity (Combined Constraint)

**Note:** This example applies when `enforceMinDrivesPerTypePerCore: false` is set.

**Configuration:**
```yaml
driveCores: 3
containerCapacity: 1500
driveTypesRatio:
  tlc: 1
  qlc: 2
```

**Capacity split:** TLC = 500 GiB, QLC = 1000 GiB

**Allocation:**

**Iteration 1:** TLC min=1
- TLC: 500 ÷ 1 = 500 GiB ≥ 384 → **1 drive**
- QLC min = max(1, 3-1) = 2
- QLC: 1000 ÷ 2 = 500 GiB ≥ 384 → **2 drives**
- Total: 1 + 2 = 3 ≥ driveCores ✓

**Final allocation:**
- TLC: 1 drive (500 GiB)
- QLC: 2 drives (500 GiB each)
- Total: 3 virtual drives, 1500 GiB

#### Example: Iteration Required (Combined Constraint)

**Note:** This example applies when `enforceMinDrivesPerTypePerCore: false` is set.

**Configuration:**
```yaml
driveCores: 3
containerCapacity: 1500
driveTypesRatio:
  tlc: 2
  qlc: 1
```

**Capacity split:** TLC = 1000 GiB, QLC = 500 GiB

**Iteration 1:** TLC min=1
- TLC: 1000 ÷ 1 = 1000 GiB → **1 drive**
- QLC min = max(1, 3-1) = 2
- QLC: 500 ÷ 2 = 250 GiB < 384 → **fails** (early termination)

**Iteration 2:** TLC min=2
- TLC: 1000 ÷ 2 = 500 GiB → **2 drives**
- QLC min = max(1, 3-2) = 1
- QLC: 500 ÷ 1 = 500 GiB ≥ 384 → **1 drive**
- Total: 2 + 1 = 3 ≥ driveCores ✓

**Final allocation:**
- TLC: 2 drives (500 GiB each)
- QLC: 1 drive (500 GiB)
- Total: 3 virtual drives, 1500 GiB

#### Example: Minimum Viable Configuration (Combined Constraint)

**Note:** This example applies when `enforceMinDrivesPerTypePerCore: false` is set.

**Configuration:**
```yaml
driveCores: 3
containerCapacity: 1152
driveTypesRatio:
  tlc: 1
  qlc: 2
```

**Capacity split:** TLC = 384 GiB, QLC = 768 GiB

**Allocation:**
- TLC: 384 ÷ 1 = 384 GiB → **1 drive** (exactly at minimum)
- QLC min = max(1, 3-1) = 2
- QLC: 768 ÷ 2 = 384 GiB → **2 drives** (exactly at minimum)
- Total: 1 + 2 = 3 ≥ driveCores ✓

**Final allocation:**
- TLC: 1 drive (384 GiB)
- QLC: 2 drives (384 GiB each)
- Total: 3 virtual drives, 1152 GiB

This is the minimum possible capacity for driveCores=3: each drive is exactly 384 GiB.

#### Example: Uneven Distribution (Combined Constraint)

**Note:** This example applies when `enforceMinDrivesPerTypePerCore: false` is set.

**Configuration:**
```yaml
driveCores: 4
containerCapacity: 2100
driveTypesRatio:
  tlc: 1
  qlc: 2
```

**Capacity split:** TLC = 700 GiB, QLC = 1400 GiB

**TLC allocation (700 GiB, min=1):**
- 700 ÷ 1 = 700 GiB ≥ 384 → **1 drive**

**QLC allocation (1400 GiB, min=max(1, 4-1)=3):**

Even distribution strategies:

| Drives | Base Size | Remainder | Distribution | All ≥384? | Result |
|--------|-----------|-----------|--------------|-----------|--------|
| 3 | 466 | 2 | 467, 467, 466 | Yes | **use** |

Even distribution calculation for 3 drives:
- Base size: 1400 ÷ 3 = 466 GiB (integer division)
- Remainder: 1400 - (466 × 3) = 1400 - 1398 = 2 GiB
- Distribution: 2 drives get base + 1 = 467 GiB, 1 drive gets base = 466 GiB
- Verification: 467 + 467 + 466 = 1400 GiB ✓

**Final allocation:**
- TLC: 1 drive (700 GiB)
- QLC: 3 drives (467, 467, 466 GiB)
- Total: 4 virtual drives, 2100 GiB

#### Example: Fit-to-Physical Fallback (Heterogeneous Drives)

**Scenario:** Node has mixed physical drive sizes that don't allow even distribution.

**Physical drives on node:** `[20000, 500, 500]` GiB (one large, two small)

**Configuration:**
```yaml
driveCores: 3
containerCapacity: 21000
driveTypesRatio:
  tlc: 1
  qlc: 0
```

**Allocation attempt:**

Even distribution exhausts all options (tries 3, 4, 5, ... up to 24 drives):

| Drives | Size per drive | Result |
|--------|----------------|--------|
| 3 | 7000 GiB | ❌ 500 GiB drives can't hold 7000 |
| 4 | 5250 GiB | ❌ 500 GiB drives can't hold 5250 |
| 6 | 3500 GiB | ❌ 500 GiB drives can't hold 3500 |
| 12 | 1750 GiB | ❌ 500 GiB drives can't hold 1750 |
| 24 | 875 GiB | ❌ 500 GiB drives can't hold 875 |

Since even the smallest even distribution (875 GiB) exceeds the 500 GiB drives, **all even distribution strategies fail**.

**Fit-to-physical fallback** creates strategy matching physical drives:
- Strategy: `[20000, 500, 500]`
- **Succeeds**: Each virtual drive size ≤ corresponding physical drive capacity

**Final allocation:**
- TLC: 3 drives (20000, 500, 500 GiB)
- Total: 3 virtual drives, 21000 GiB

**Note:** Fit-to-physical is only generated when:
- All even distribution strategies have been exhausted
- The physical drive layout meets the `numDrives >= driveCores` constraint
- Each drive meets the 384 GiB minimum

#### Example: Single Drive Type (TLC Only)

**Configuration:**
```yaml
driveCores: 3
containerCapacity: 3000
driveTypesRatio:
  tlc: 1
  qlc: 0
```

**Capacity split:** TLC = 3000 GiB, QLC = 0 GiB

**Allocation:**
- TLC min = driveCores = 3 (no QLC to compensate)
- TLC: 3000 ÷ 3 = 1000 GiB → **3 drives**
- QLC: skipped (0 capacity)

**Final allocation:**
- TLC: 3 drives (1000 GiB each)
- Total: 3 virtual drives, 3000 GiB

---

### Global Defaults

The operator supports global defaults for drive sharing configuration via Helm values.

#### driveTypesRatio

**Helm values.yaml:**

```yaml
driveTypesRatio:
  tlc: 1   # ~9% TLC (high-performance)
  qlc: 10  # ~91% QLC (cost-optimized)
```

**Behavior:**
- When `containerCapacity` is set **without** per-cluster `driveTypesRatio`, the operator applies the global default automatically
- Per-cluster `spec.dynamicTemplate.driveTypesRatio` always overrides global setting
- Ratio represents relative proportions (total parts = tlc + qlc)

#### maxVirtualDrivesPerCore

**Helm values.yaml:**

```yaml
maxVirtualDrivesPerCore: 8  # Default value
```

**Behavior:**
Limits the number of virtual drives that can be allocated per CPU core assigned to the container (`driveCores`).
Formula: `Total Virtual Drives <= driveCores * maxVirtualDrivesPerCore`

#### enforceMinDrivesPerTypePerCore

**Helm values.yaml:**

```yaml
enforceMinDrivesPerTypePerCore: true  # Default value
```

**Behavior:**
Controls the minimum drive count constraint when using mixed TLC/QLC configurations.

- **When `true` (default):** Per-type constraint - each active drive type (TLC and/or QLC) must have at least `driveCores` virtual drives. More strict, ensures balanced distribution per type.

- **When `false`:** Combined constraint - total drives across both types must be at least `driveCores`. More flexible, allows asymmetric distribution (e.g., 4 TLC + 2 QLC for 6 cores).

**Note:** This setting only affects mixed TLC/QLC configurations. Single-type allocations (TLC-only or QLC-only) behave identically in both modes.

---

## Multiple Clusters Sharing Drives

Multiple clusters can share the same physical drives simultaneously. Each cluster allocates its own virtual drives from available capacity.

**Example: Two clusters on the same nodes**

```yaml
---
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: prod-cluster
  namespace: production
spec:
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 3
    containerCapacity: 6000  # 6TB per container
    computeContainers: 6
    computeCores: 3
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
  ports:
    basePort: 15000

---
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: dev-cluster
  namespace: development
spec:
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 2
    containerCapacity: 3000  # 3TB per container
    computeContainers: 6
    computeCores: 2
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
  ports:
    basePort: 15500  # Different port range
```

**Key points:**
- Each cluster uses different `basePort` to avoid conflicts
- Both clusters share the same physical drives via proxy containers
- Total capacity request: (6000 + 3000) × 6 containers = 54TB must be available
- Virtual drive claims tracked in node annotation: `weka.io/virtual-drive-claims`

---

## Verification and Monitoring

### Check Proxy Container Status

Proxy containers are automatically created when first drive-sharing cluster appears:

```bash
kubectl get pods -n weka-operator-system -l weka.io/container-mode=ssdproxy
```

**Expected output:**
```
NAME                      READY   STATUS    RESTARTS   AGE
weka-drives-proxy-node1   1/1     Running   0          5m
weka-drives-proxy-node2   1/1     Running   0          5m
```

### Check Shared Drive Capacity

View available shared drive capacity on nodes:

```bash
kubectl get nodes -o custom-columns=\
NAME:.metadata.name,\
SHARED_CAPACITY:.status.capacity.weka\.io/shared-drives-capacity
```

### Check Virtual Drive Allocations

View virtual drive claims per node:

```bash
kubectl get node <node-name> -o jsonpath='{.metadata.annotations.weka\.io/virtual-drive-claims}' | jq
```

**Example output:**
```json
{
  "31de939a-...": ["default:prod-cluster-drive-0", "fb05d910-...", 2000],
  "7b3f82cd-...": ["development:dev-cluster-drive-0", "fb05d910-...", 1500]
}
```

Format: `{"virtualUUID": ["namespace:container", "physicalUUID", capacityGiB]}`

### Check Cluster Status

Monitor drive container status:

```bash
kubectl get wekacontainer -n <namespace> -l weka.io/container-type=drive
```

View virtual drive allocations in container status:

```bash
kubectl get wekacontainer <container-name> -n <namespace> -o jsonpath='{.status.allocations.virtualDrives}' | jq
```

---

## Capacity Management

### Understanding Capacity Limits

**Physical capacity:** Total GiB from all physical drives on node
**Claimed capacity:** Sum of all virtual drive allocations
**Available capacity:** Physical - Claimed

**Example scenario:**
- Node has 4 physical drives × 3840 GiB = 15360 GiB total
- Cluster A allocates 6000 GiB
- Cluster B allocates 5000 GiB
- Available: 15360 - 11000 = 4360 GiB

### Allocation Errors

**Minimum Drive Count Constraint Error (Mixed Types with `enforceMinDrivesPerTypePerCore: true`):**
```
insufficient QLC capacity: with 5 drive cores and enforceMinDrivesPerTypePerCore=true, need at least 1920 GiB QLC
(minimum 5 drives × 384 GiB each), but only 1000 GiB configured.
Increase containerCapacity or adjust driveTypesRatio, or set enforceMinDrivesPerTypePerCore=false
```

**Resolution:**
- **Increase** `containerCapacity` to provide more capacity for the constrained type
- **Decrease** `driveCores` if fewer drives are acceptable
- **Adjust** `driveTypesRatio` to allocate more capacity to the constrained type
  - Example: Change from `{tlc: 4, qlc: 1}` to `{tlc: 3, qlc: 2}` to give more capacity to QLC
- **Set** `enforceMinDrivesPerTypePerCore: false` in Helm values to use combined constraint (allows asymmetric distribution)

**Minimum Drive Count Constraint Error (Combined constraint or Single Type):**
```
insufficient total capacity: with 5 drive cores, need at least 1920 GiB total
(minimum 5 drives × 384 GiB each), but only 1500 GiB available.
Increase containerCapacity
```

**Resolution:**
- **Increase** `containerCapacity` to meet minimum requirement
- **Decrease** `driveCores` if fewer drives are acceptable

**InsufficientDriveCapacityError:**
```
Insufficient drive capacity: need 8000 GiB, available 4360 GiB
```

**Resolution:**
- Reduce `containerCapacity` or `driveCapacity` in cluster spec
- Add more physical drives to nodes
- Delete unused clusters to free capacity

**InsufficientDrivesError:**
```
Insufficient TLC drives: need 6000 GiB, available 3000 GiB
```

**Resolution (when using drive type ratio):**
- Check physical drive type distribution matches ratio requirements
- Adjust `driveTypesRatio` to match available drive types
- Add more drives of the required type

---

## Comparison: Drive Sharing vs Exclusive Drives

| Aspect | Drive Sharing | Exclusive Drives |
|--------|---------------|------------------|
| **Multi-tenancy** | Multiple clusters per node | One cluster per node |
| **Capacity allocation** | Flexible, on-demand | Pre-partitioned, fixed |
| **Configuration** | `containerCapacity` or `driveCapacity` | `numDrives` only |
| **Drive signing** | `shared: true` (proxy mode) | Standard signing |
| **Performance** | Small proxy overhead | Direct drive access |
| **Complexity** | Higher (proxy + virtual drives) | Lower (direct mapping) |
| **Use case** | Multi-tenant, cost optimization | Single tenant, maximum performance |

---

## Related Documentation

- [Drive Signing](drive-signing.md) - Standard (exclusive) drive signing for single-cluster deployments
- [Cluster Provisioning](../deployment/cluster-provisioning.md) - General cluster configuration
- [WekaCluster API Reference](../../api_dump/wekacluster.md) - Complete field reference
- [WekaManualOperation API Reference](../../api_dump/wekamanualoperation.md) - Manual operation details
