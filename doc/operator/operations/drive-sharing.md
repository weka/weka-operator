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

Enable drive sharing by setting capacity fields in `spec.dynamicTemplate`. Choose one of three allocation modes:

#### Mode 1: Fixed Capacity Per Virtual Drive

Specify capacity per individual virtual drive using `driveCapacity`.

**Important:** `driveCapacity` is for TLC-only mode and cannot be used with `driveTypesRatio`. For mixed TLC/QLC drives, use `containerCapacity` with `driveTypesRatio` instead (see Mode 3).

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-a
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:4.4.10.183
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 3
    numDrives: 6
    driveCapacity: 1000  # Each virtual drive gets 1000 GiB
    computeContainers: 6
    computeCores: 3
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

**Result:** Each drive container requests 6 virtual drives × 1000 GiB = 6000 GiB total per container.

**Use case:** Uniform drive sizes, simple configuration.

---

#### Mode 2: Total Container Capacity

Specify total capacity per container using `containerCapacity`. The operator automatically distributes capacity across virtual drives.

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-b
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:4.4.10.183
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  dynamicTemplate:
    driveContainers: 6
    driveCores: 4
    containerCapacity: 8000  # Total capacity per container
    computeContainers: 6
    computeCores: 3
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

**Result:** Each drive container receives 8000 GiB total, distributed across multiple virtual drives (typically `driveCores` or more drives).

**Allocation strategy:**
1. Operator tries uniform distributions (all drives equal size)
2. Falls back to near-uniform if capacity doesn't divide evenly
3. Ensures minimum drive size (1024 GiB minimum)

**Use case:** Flexible capacity allocation, let operator optimize drive distribution.

**Note:** `containerCapacity` takes precedence over `driveCapacity` when both are set.

---

#### Mode 3: Mixed Drive Types with Ratio

Specify capacity split between TLC (high-performance) and QLC (cost-optimized) drives using `driveTypesRatio`.

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-c
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:4.4.10.183
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

**Minimum Capacity Constraint:**

When using mixed drive types, **each type (TLC and QLC) must get at least `driveCores` virtual drives**. Since each virtual drive requires a minimum of 384 GiB, the capacity requirements are:

- **TLC capacity** ≥ `driveCores × 384 GiB`
- **QLC capacity** ≥ `driveCores × 384 GiB`

**Example validation:**
```yaml
driveCores: 6
containerCapacity: 12000
driveTypesRatio: {tlc: 4, qlc: 1}
```
- TLC capacity: 9600 GiB ≥ 2304 GiB (6 × 384) ✓
- QLC capacity: 2400 GiB ≥ 2304 GiB (6 × 384) ✓
- **Valid configuration**

**Invalid example:**
```yaml
driveCores: 5
containerCapacity: 5000
driveTypesRatio: {tlc: 4, qlc: 1}
```
- TLC capacity: 4000 GiB ≥ 1920 GiB (5 × 384) ✓
- QLC capacity: 1000 GiB < 1920 GiB (5 × 384) ✗
- **Error:** "insufficient QLC capacity: need at least 1920 GiB to allocate 5 QLC drives, but only 1000 GiB available"

**To fix insufficient capacity errors:**
1. Increase `containerCapacity`
2. Decrease `driveCores`
3. Adjust `driveTypesRatio` to allocate more capacity to the constrained type

**Use case:** Mixed drive types for performance/cost balance, tiered storage.

---

### Global Default: driveTypesRatio

The operator supports a global default for mixed drive types via Helm values.

**Helm values.yaml:**

```yaml
driveTypesRatio:
  tlc: 4  # 80% TLC (high-performance)
  qlc: 1  # 20% QLC (cost-optimized)
```

**Behavior:**
- When `containerCapacity` is set **without** per-cluster `driveTypesRatio`, the operator applies the global default automatically
- Per-cluster `spec.dynamicTemplate.driveTypesRatio` always overrides global setting
- Ratio represents relative proportions (total parts = tlc + qlc)

**Example values:**
- `tlc: 1, qlc: 0` = 100% TLC (all high-performance) - DEFAULT
- `tlc: 4, qlc: 1` = 80% TLC, 20% QLC (recommended balance)
- `tlc: 1, qlc: 1` = 50% TLC, 50% QLC
- `tlc: 0, qlc: 1` = 100% QLC (all cost-optimized)

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
  image: quay.io/weka.io/weka-in-container:4.4.10.183
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
  image: quay.io/weka.io/weka-in-container:4.4.10.183
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

**Minimum Drive Count Constraint Error (Mixed Types):**
```
insufficient QLC capacity for default/my-cluster-drive-0: need at least 1920 GiB
to allocate 5 QLC drives (minimum 384 GiB per drive), but only 1000 GiB available
```

**Resolution:**
- **Increase** `containerCapacity` to provide more capacity for the constrained type
- **Decrease** `driveCores` if fewer drives are acceptable
- **Adjust** `driveTypesRatio` to allocate more capacity to the constrained type
  - Example: Change from `{tlc: 4, qlc: 1}` to `{tlc: 3, qlc: 2}` to give more capacity to QLC

**Minimum Drive Count Constraint Error (Single Type):**
```
insufficient capacity for default/my-cluster-drive-0: need at least 1920 GiB
to allocate 5 drives (minimum 384 GiB per drive), but only 1500 GiB available
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
