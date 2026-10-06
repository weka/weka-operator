# Act As Daemonset

When you create a `WekaCluster` without telling the operator **how many containers to make**, it acts
as a **daemonset**: one drive container and one compute container on every matching node, with
drives, cores, hugepages and memory derived from each node's own signed full drives.

**There is no flag for this.** The mode is implicit: leave `computeContainers`, `driveContainers` and
all capacity fields unset. An empty `dynamicTemplate: {}`, or no `dynamicTemplate` at all, selects it:

```yaml
spec:
  template: dynamic
  dynamicTemplate: {}       # act as a daemonset
```

The operator does not check whether a node has room for its containers. A pod that does not fit its
node stays **Pending**; see [Troubleshooting](#troubleshooting).

## Mode detection

`spec.dynamicTemplate` has no mode field. The mode follows from which fields are set:

| `dynamicTemplate` | Family | Mode |
|---|---|---|
| absent | full drives | daemonset |
| `{}` | full drives | daemonset |
| `{numDrives: 4}` | full drives | daemonset, 4 largest drives per node |
| `{numDrives: 22, driveCores: 11, computeCores: 16}` | full drives | daemonset, all pins honored |
| `{driveContainers: 6, computeContainers: 6, numDrives: 4, driveCores: 4, computeCores: 8}` | full drives | explicit container counts |
| `{driveContainers: 6}`, `{computeContainers: 6, numDrives: 4}` | — | **rejected**: counts must be set together |
| `{driveContainers: 6, computeContainers: 6, numDrives: 6, driveCapacity: 1000, driveCores: 3, computeCores: 3}` | drive sharing | `numDrives` + `driveCapacity`, see [Drive Sharing](../operations/drive-sharing.md) |
| `{driveContainers: 6, computeContainers: 6, containerCapacity: 8000, driveTypesRatio: {tlc: 1, qlc: 0}, computeCores: 3}` | drive sharing | `containerCapacity`, see [Drive Sharing](../operations/drive-sharing.md) |
| `{clusterCapacity: 300TiB, driveTypesRatio: {tlc: 1, qlc: 10}}` | drive sharing | `clusterCapacity`, see [Cluster Capacity](cluster-capacity.md) |

The daemonset mode is active when all of `computeContainers`, `driveContainers`, `clusterCapacity`,
`containerCapacity` and `driveCapacity` are unset. `numDrives`, `driveCores` and `computeCores` are
**pins**, not mode selectors.

Without a capacity field, `computeContainers` and `driveContainers` must be both set or both unset.
Setting exactly one is rejected by a CEL rule on the CRD at `kubectl apply`.

## Prerequisites

The mode only uses **signed, non-blocked full drives**; it does not sign drives. Sign the backend
nodes before creating the cluster (see [Drive Signing](../operations/drive-signing.md)). Signing writes
the `weka.io/weka-full-drives` node annotation, which is the only inventory the operator reads. A node
signed after the cluster exists is picked up on the next reconcile.

QLC drives get no separate accounting: they will be added to weka along with TLCs. Keep them out with
`signDrivesPayload.driveExclusions`, or use a drive-sharing mode (see
[Cluster Capacity](cluster-capacity.md)).

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-daemonset
  namespace: default
spec:
  template: dynamic
  dynamicTemplate: {}
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  nodeSelector:
    weka.io/dedicated: "cluster-daemonset"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

## Placement

Each reconcile classifies the nodes from the client cache.

- **Drive-role nodes** match `roleNodeSelector.drive`, falling back to the cluster `nodeSelector`.
  **Compute-role nodes** match `roleNodeSelector.compute`, with the same fallback.
- Containers are **node-pinned**: one per node and role.
- The selector therefore sets the cluster size.
- Other clusters' containers are ignored. A drive that another cluster holds is still counted as
  available here; see [Troubleshooting](#troubleshooting).

### Ineligible, unsigned and deleting nodes

| Node state | New container | Existing own container |
|---|---|---|
| Cordoned, `NotReady`, untolerated taint, or missing FD label (FD label mode) | none, reported in `AutoFullDrivesNodeIneligible` | kept, counted, still grows |
| Drive-role node without a parseable `weka.io/weka-full-drives` annotation | no drive or compute container, reported in `AutoFullDrivesUnsignedDriveNodes` | kept, counted, still grows |
| Own container of that role is deleting | none, silently skipped | not counted |
| Lost its selector label | none | kept and counted (Desired and totals); grows only from pins and fleet-wide compute sizing, since the node's annotation is no longer read. Removed instead when `cleanupOnNodeSelectorMismatch` is on |

An unparsable annotation is treated as unsigned; the parse error is named in the event. Pending pods
count too, so a stuck pod never causes a duplicate container. Sizing never deletes a container.

## Sizing

### Drive containers

One per eligible, signed drive-role node.

- `numDrives` = the `numDrives` pin, else every non-blocked drive in the annotation.
- `cores` = the `driveCores` pin, else `min(numDrives, maxCoresPerContainer)`. Drives and cores are
  decoupled: a node with 24 drives gets 24 drives on 19 cores.
- The drive capacity used for hugepages is the sum of the `numDrives` largest drives.

### Compute containers

`ratio` is `capacityPlannerConstraints.fullDrives.computeToDriveCoreRatio` (default **2.0**; must be greater than 0; the chart's values schema rejects anything else; `null` omits the env var). Required
compute cores for `x` drive cores are `max(x, ceil(ratio * x))`, so the 1:1 floor always holds.

- **Paired node** (compute-role node that holds one of this cluster's drive containers): `cores` = the
  `computeCores` pin, else the required cores for that node's drive cores, clamped to 1..19. Hugepages
  come from that node's own drive capacity.
- **Compute-only nodes** (compute-role nodes without our drive container, e.g. a disjoint compute
  selector): the requirement for the fleet's total drive cores, minus what paired nodes supply, is
  split evenly: `cores = ceil(remaining / M)` over `M` nodes, minimum 1. Hugepages use the remaining
  drive capacity divided across the `M` nodes, with a floor of 3000 MiB per core plus DPDK memory.
  With disjoint selectors this is an even spread of the whole requirement.
- A drive-role node with no drive container (unsigned, ineligible or deleting) gets no new compute
  container.

### The 19-core cap

Compute cores per container are capped at `capacityPlannerConstraints.maxCoresPerContainer` (19). Whenever
the cap cuts a desired value, including with an explicit ratio, the operator emits the
`AutoFullDrivesComputeCoresCapped` Warning with the desired and achieved compute:drive core ratio. The
cluster still runs, with less compute than the ratio asks for. A `computeCores` pin is not capped by the
planner; the `cluster_cores_per_container_limit` policy checks it.

## Pins

`numDrives`, `driveCores`, `computeCores`, `driveHugepages`, `computeHugepages` and the two `*Offset`
fields are never ignored and are not checked at runtime.

- `numDrives` below the signed count leaves drives unused, reported in `AutoFullDrivesUnusedDrivesOnNode`
  (Normal), e.g. `3 of 8 signed drives unused on node-a (numDrives pin=5)`.
- `numDrives` or `driveCores` above what a node can supply is caught by admission
  (`cluster_auto_full_drives_pins`). In relaxed mode the container is created anyway and waits on
  `InsufficientDrives`.
- Hugepages and offset never go below **auto**: the value computed without the pin (the drive figure from
  cores and drives, the compute figure the plan computes, and their offsets). Per field:
  - No pin: `max(current, auto)`, applied on every reconcile, so an unpinned container below auto is raised.
  - Pin at or above auto: the pin is written.
  - Pin below auto: the pin is not applied; the value is `max(current, auto)` (auto on create) and
    `AutoFullDrivesHugepagesPinBelowAuto` (Warning) is posted on the cluster and on each affected
    container, listing `container/field pin=X auto=Y current=Z`.

## Pre-formation cap

Before the cluster is formed (`ClusterCreated` not yet set), creation is capped per role at the same
form-cluster maximum a regular cluster uses (10 per role by default). Missing containers are created
**nodes holding both roles first**, then by node name. After formation the rest are created on the next
reconcile.

## Expand-only and pod restart

Sizing only raises. Each reconcile raises existing containers in place:

- `numDrives` and `cores` become `max(existing, target)`. An annotation that loses a drive, or a lowered
  pin, never shrinks a container.
- Compute cores and hugepages are raised the same way; a hugepages pin at or above auto is written as is.
- A new signed node, or newly signed drives on a node, gain containers or drives on the next reconcile.

A raised value changes the `WekaContainer` spec, not the running pod. It applies when the pod is next
recreated (image upgrade, `podConfigVersion` bump, manual delete). Drive-only growth is served at once
by the running weka container, but its hugepages limit is immutable and no longer covers the extra
drives, so restart the pod. Each growth emits `CapacityGrowthApplied` (Warning) on the container and
`AutoFullDrivesGrowth` on the cluster.

## Mode flips

`cluster_sizing_mode_flip` allows both directions on a cluster that already has containers:

| Flip | Result |
|---|---|
| counts to daemonset | Existing containers are matched to their node (a scheduled count-based container is matched through its status node affinity) and grown to the node's full drive set. New nodes get new containers. Never-bound containers without a node are ignored. |
| daemonset to counts | Existing containers and pins are kept and never shrunk. Count mode only creates while it has fewer containers than requested. |
| daemonset to `clusterCapacity` or drive sharing | **Rejected**. |

## What admission checks

| Policy | Strict | Relaxed | Checks |
|---|---|---|---|
| `cluster_auto_full_drives_min_nodes` | Error | Error | Each role selector matches at least the form-cluster minimum (5; 3 with `ALLOW_SINGLE_PARITY`). |
| `cluster_auto_full_drives_pins` | Error | Warn | Per signed drive-role node: `numDrives` pin at most the signed count; `driveCores` pin at most `numDrives` (or the signed count). Unsigned nodes are skipped. |
| `cluster_drives_unsigned_advisory` | Warn | Warn | Lists every drive-role node without the annotation. |
| `cluster_min_drives_feasibility` | Error | Error | `minNumDrives` at most the total claimable drives. Skipped when nothing is signed yet (the advisory covers it). |
| `cluster_sizing_mode_flip` | Error | Error | See [Mode flips](#mode-flips). |
| `cluster_cores_per_container_limit` | Error | Warn | `driveCores` and `computeCores` pins at most 19. |
| `cluster_cores_available`, `cluster_hugepages_available` | Warn | Warn | Advisory checks of node CPU and hugepages. |

The CRD also requires `numDrives >= driveCores` when both are pinned.

## Events

All cluster-level events are in `internal/controllers/wekacluster/planner_events.go`.

| Reason | Type | Throttle | Meaning |
|---|---|---|---|
| `AutoFullDrivesContainersCreated` | Normal | 1m | Containers were created this pass, by role and node. |
| `AutoFullDrivesGrowth` | Normal | 1m | Existing containers were raised; restart their pods. |
| `AutoFullDrivesUnusedDrivesOnNode` | Normal | 3m, aggregated | `numDrives` pin below the signed count. |
| `AutoFullDrivesNodeIneligible` | Normal | 3m, aggregated, keyed by reason | Nodes that get no new containers, with the reason. |
| `AutoFullDrivesUnsignedDriveNodes` | Warning | 1m, aggregated | Drive-role nodes without usable signed drives. |
| `AutoFullDrivesComputeCoresCapped` | Warning | 2m | Compute cores cut by the 19-core cap. |
| `AutoFullDrivesHugepagesPinBelowAuto` | Warning | 5m, aggregated; also on each `WekaContainer`, throttled | A hugepages or offset pin below auto was not applied. |
| `CapacityGrowthApplied` | Warning | none | On the `WekaContainer`: a container was raised. |

## Troubleshooting

**A pod is Pending.** The operator does not fit-check nodes, so the node cannot satisfy the pod. Run
`kubectl describe pod` for the unmet request (CPU, hugepages, `weka.io/drives`). Free the resources, or
narrow `nodeSelector` / `roleNodeSelector` so the node is no longer matched. A Pending container is
never replaced or deleted by the operator; deleting the `WekaContainer` and narrowing the selector
removes it.

**A node has no containers.** Check `kubectl describe wekacluster` for `AutoFullDrivesUnsignedDriveNodes`
(sign drives there; an unparsable annotation is named in the message) and
`AutoFullDrivesNodeIneligible` (uncordon, tolerate the taint, or add the FD label). The `DCT`/`CCT`
columns show fewer created than desired while a node is unsigned.

**`AutoFullDrivesComputeCoresCapped`.** The ratio asks for more than 19 cores per compute container.
Add compute-role nodes, lower `computeToDriveCoreRatio` (never below 1.0 in effect), or accept the lower
ratio shown in the event.

**A drive container waits on `InsufficientDrives`.** Drives come from the node annotation, and another
cluster's container may hold some of them. The container waits until those drives are released. Also
the result of a `numDrives` pin above the signed count.

## Related documentation

- [Drive Signing](../operations/drive-signing.md): the `weka.io/weka-full-drives` annotation this mode reads
- [Cluster Capacity](cluster-capacity.md): the `clusterCapacity` drive-sharing mode, including QLC
- [Drive Sharing](../operations/drive-sharing.md): `containerCapacity`, `driveCapacity` and `ssdproxy`
- [Cluster Provisioning](cluster-provisioning.md): general `WekaCluster` provisioning
