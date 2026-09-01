# Act As Daemonset

When you create a `WekaCluster` without telling the operator **how many containers to make**, it acts
as a **daemonset over its drive-role node selector**: one drive container per eligible node,
consuming **every eligible full drive on that node**, with drive cores, hugepages and memory
auto-calculated from the node's own drives.

**There is no flag for this.** The mode is *implicit* — you get it by leaving `computeContainers` and
`driveContainers` (and all three capacity fields) unset. An empty `dynamicTemplate: {}`, or no
`dynamicTemplate` at all, is the shortest way to ask for it:

```yaml
spec:
  template: dynamic
  dynamicTemplate: {}       # act as a daemonset
```

The defining guarantee is: **every signed full drive on every eligible node is claimed.** The
operator never quietly uses fewer drives than a node has in order to make a container fit. If a node
cannot host a container sized for all of its drives, the *whole plan* is infeasible and nothing is
created — see [When a node cannot fit its drives](#when-a-node-cannot-fit-its-drives).

## How the operator picks a sizing mode

`spec.dynamicTemplate` has no mode field. The operator derives the mode from **which fields you set**:

| `dynamicTemplate` | Mode |
|---|---|
| absent, or `{}` | daemonset |
| `{numDrives: 4}` | daemonset, 4 largest drives per node |
| `{numDrives: 4, driveCores: 3}` | daemonset, both pins honored |
| `{computeContainers: 6, driveContainers: 6, numDrives: 4}` | manual container counts (unchanged) |
| `{driveContainers: 6}` / `{computeContainers: 6, numDrives: 4}` | **rejected** |
| `{clusterCapacity: 500TiB}` (± one count) | clusterCapacity (unchanged) |
| `{numDrives: 4, driveCapacity: 3500}` | drive sharing (unchanged) |

Read as a rule: the daemonset mode is active **if all** of `computeContainers`, `driveContainers`,
`clusterCapacity`, `containerCapacity` and `driveCapacity` are unset (or zero). `numDrives`,
`driveCores` and `computeCores` are **pins, not mode selectors** — setting any of them does not take
you out of the mode.

### The both-or-neither rule

When **no** capacity field (`clusterCapacity`, `containerCapacity`, `driveCapacity`) is set,
`computeContainers` and `driveContainers` must be **both set or both unset**. Setting exactly one is
rejected by a CEL rule on the CRD, so it fails at `kubectl apply` with:

> *"computeContainers and driveContainers must be set together: setting both sizes the cluster by
> container counts, while leaving both unset makes the operator act as a daemonset over its
> drive-role nodeSelector (one drive container per eligible node, sized from that node's own full
> drives). numDrives, driveCores and computeCores may be pinned either way. For capacity-based
> sizing, use clusterCapacity, containerCapacity or driveCapacity instead"*

## When to use this

Daemonset is one of several ways to size drive containers. They fall into two families: **exclusive
full drives** (each cluster owns whole physical drives) and **drive sharing** (physical drives are
partitioned into virtual drives shared across clusters via `ssdproxy`; see the
[Drive Sharing guide](../operations/drive-sharing.md)).

| Mode | Family | You set | Operator derives | Heterogeneous nodes |
|---|---|---|---|---|
| Manual container counts (`computeContainers` + `driveContainers`, with `numDrives`/`driveCores`) | Exclusive | Both counts, `numDrives`, `driveCores` (uniform across all containers) | Nothing — you size everything | No — one uniform shape for every container |
| `numDrives` + `driveCapacity` | Drive sharing | `numDrives`, target capacity per drive | `driveCores` (unless pinned) | No |
| `containerCapacity` | Drive sharing | Target capacity per container | `driveCores` (unless pinned) | No |
| `clusterCapacity` | Drive sharing | One whole-cluster capacity target | Container count, per-container capacity, cores, compute — see [Cluster Capacity](cluster-capacity.md) | Yes, within its own planner |
| **Daemonset** | **Exclusive** | Nothing (`numDrives`/`driveCores`/`computeCores` are optional pins) | Container placement (1 per eligible node), `numDrives`, `driveCores`, hugepages, memory, compute sizing | **Yes** — sized per node from that node's own drives |

Use the daemonset mode when you want to hand the operator every signed drive on your backend nodes
without computing cores by hand, especially when nodes don't all have the same drive count. Use
`clusterCapacity` instead when you want a single target usable-capacity number and are running (or
willing to run) in drive-sharing mode. Set both container counts when you want full control over a
uniform shape.

## Prerequisites

The mode only ever picks up **signed, non-blocked full drives** — it does not sign drives itself.
Sign drives on your backend nodes before creating the cluster (see
[Drive Signing](../operations/drive-signing.md)); signing writes the `weka.io/weka-full-drives`
node annotation (and the `weka.io/drives` extended resource) that the planner reads. A node with no
signed full drives gets no drive container.

Sign drives, *then* create the `WekaCluster` — or, if the cluster already exists, sign drives on
new nodes and the operator will pick them up on its own (see
[Expand-only reconciliation](#expand-only-reconciliation)).

## Basic example

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-daemonset
  namespace: default
spec:
  template: dynamic
  gracefulDestroyDuration: 0s
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

No container counts, no `numDrives`, no `driveCores` — the operator creates one drive container per
node matching the drive-role `nodeSelector` (falling back to the cluster `nodeSelector` when no
role-specific one is set) that has signed full drives, and sizes each from that node's own drives.
Omitting `dynamicTemplate` entirely has the same effect.

Because the mode takes **every** signed full drive on **every** node the selector matches, the node
set is the primary control over the resulting cluster's size and shape — a broad selector can pull in
nodes with very different drive counts than intended, so pick your `nodeSelector` deliberately rather
than reusing a fleet-wide label.

## What gets auto-calculated

For each eligible node, the operator counts its signed non-blocked full drives, then derives:

- **`numDrives`** for that node's container — **all** of them, unless you pinned `numDrives`
  (see [numDrives as a per-node override](#numdrives-as-a-per-node-override)).
- **Drive cores** — `min(drives taken, 19)`, unless you pinned `driveCores`. See
  [Drives and cores](#drives-and-cores).
- **Hugepages and memory** for the drive container — recomputed from the derived core count (see
  [Hugepages budget](#hugepages-budget) below for the exact per-core figures).
- **Compute sizing** — derived from the total drive cores across the cluster at a configurable
  ratio, **2:1 by default** in full-drives mode. See [Compute sizing](#compute-sizing).

## Drives and cores

A container's drive count and its core count are **independent numbers**:

```
drives     = every signed non-blocked full drive on the node        # or the numDrives pin
driveCores = driveCores pin, else min(drives, maxCoresPerContainer) # maxCoresPerContainer = 19
```

In full-drives mode weka still requires **at least one physical drive per drive core**, so cores can
never *exceed* drives — but they may be **fewer**:

| Node | Spec | Result |
|---|---|---|
| 30 drives | — | 30 drives, 19 cores |
| 30 drives | `driveCores: 5` | 30 drives, 5 cores |
| 30 drives | `numDrives: 5` | 5 largest drives, 5 cores (stranding expected, Normal event) |
| 4 drives | `driveCores: 8` | **infeasible** — weka needs ≥1 physical drive per core |
| 4 drives | `numDrives: 10` | **infeasible** — explicit pin cannot be honored |

Consequences worth knowing:

- **A `driveCores` pin below the drive count is lossless and supported.** You keep every drive and
  run them on fewer cores. Useful when a node is short on CPU or hugepages, though it shrinks only the
  per-core part of the footprint: the `200` MiB per drive stays, since every drive is still claimed
  (see [Hugepages budget](#hugepages-budget)).
- **A `driveCores` pin above a node's effective drive count is infeasible** — the plan is rejected and
  **nothing is created**, on the whole cluster, not just that node. Full drives cannot express more
  than one core per device. Lower `driveCores`, or switch to a **drive-sharing** mode
  (`clusterCapacity` or `containerCapacity`), where each physical drive is partitioned into several
  virtual drives so a container can hold more drives — and therefore more cores — than the node has
  physical devices. See [Cluster Capacity](cluster-capacity.md) and the
  [Drive Sharing guide](../operations/drive-sharing.md).
- **Drive *size* does not affect the core count.** A node with 4 × 3.84 TB drives and a node with
  4 × 15.36 TB drives both derive **4** drive cores. Capacity-per-core ratios
  (`tlcCapacityPerCoreGiB`) belong to the drive-sharing modes and are not consulted here.
- **A node with more than 19 drives keeps all of them.** It runs them on 19 cores. Only cores are
  capped; no drive is dropped.

## numDrives as a per-node override

Pinning `numDrives` sets a **per-node drive count** rather than disabling the mode. Every eligible
node takes exactly that many of its **largest** drives:

```yaml
  dynamicTemplate:
    numDrives: 4        # still daemonset — 4 largest drives on every eligible node
```

- **`numDrives` above a node's signed drive count is infeasible.** An explicit pin is never silently
  reduced. Nothing is created until every eligible node has at least that many signed drives (sign
  more, or narrow the drive-role selector). Admission catches this too, via the
  `cluster_auto_full_drives_pin_exceeds_node_drives` policy — an **error** in strict mode, a
  **warning** in relaxed. The same policy covers a `driveCores` pin above a node's effective drive
  count; it deliberately stays silent on a pin *below* it, which is lossless.
- **`numDrives` below a node's drive count strands the rest, and that is expected.** The drives you
  did not ask for are left unused. Because you asked for it explicitly, this is reported as a
  **Normal** `AutoFullDrivesDrivesStranded` event on the `WekaCluster` — one aggregated message naming
  each affected node as *used of signed* — not as a Warning.
- **On a node whose drives are not all the same size, the reported capacity can overstate.** The pin
  names the node's largest drives, but a container already holding smaller ones keeps them, so the
  `X of Y GiB TLC` figure in `AutoFullDrivesPlanned` — and the claimed capacity the compute-hugepages
  term derives from — can read high. Drive counts, cores and the container spec are unaffected.
- `numDrives` is also the lever for reducing **claimed capacity**, which is what the compute-hugepages
  ceiling scales with. See
  [Compute hugepages are the practical ceiling](#compute-hugepages-are-the-practical-ceiling).

`numDrives` and `driveCores` combine freely as long as `numDrives >= driveCores`, which is a hard CEL
rule on the CRD. `{numDrives: 5, driveCores: 3}` gives every node 5 drives on 3 cores.

## Per-container core limit

A single weka container may hold at most **19 cores**
(`capacityPlannerConstraints.maxCoresPerContainer`, Helm-tunable). The operator enforces this in two places, both
reading that same value:

- **Admission.** A pinned `spec.dynamicTemplate.driveCores` or `computeCores` above the limit is
  rejected by the `cluster_cores_per_container_limit` policy — an **error** in strict mode, a
  **warning** in relaxed mode — for every sizing mode. Setting `maxCoresPerContainer: 0` disables
  the check, matching how the planners treat 0.
- **Auto-calculation.** Both capacity planners cap the cores they *derive* at the same limit. In
  daemonset mode this is the `min(...)` in the formula [above](#drives-and-cores); it caps **cores
  only** and leaves the drive count untouched. It does not clamp an explicit `driveCores` pin, which is
  honored verbatim — admission owns pins, via `cluster_cores_per_container_limit`. For `clusterCapacity` a per-container capacity needing
  more than the cap makes the plan infeasible rather than silently over-sizing a container (raise
  `driveContainers` or lower `clusterCapacity`).

The limit is enforced only for the drive and compute roles the planners manage. The protocol roles
(`s3Cores`, `nfsCores`, `smbwCores`, `dataServicesCores`) are **not** checked.

### Hugepages budget

Drive and compute containers each reserve hugepages per core, using the same default coefficients
the operator applies everywhere else in the capacity planner:

- **Drive side**: `1400` MiB per drive **core**, plus `200` MiB per **drive**, plus the per-core DPDK
  reservation (`64` MiB/core by default, overridable via
  `spec.dynamicTemplate.overrides.dpdkBaseMemoryMb.drive`):

  ```
  driveHugepagesMiB = 1400 × cores + 200 × drives + 64 × cores
  ```

  It scales with **both** axes. A node with as many drives as cores reserves `1664` MiB per core
  (`1400 + 200 + 64`), but the two terms are otherwise independent of each other: a 30-drive
  node running 19 cores reserves `1400 × 19 + 200 × 30 + 64 × 19 = 33,816` MiB, and a `driveCores: 5`
  pin brings that to `1400 × 5 + 200 × 30 + 64 × 5 = 13,320` MiB — lower, but not `5 × 1664`, because
  the per-drive part does not move when you lower cores.

  The per-drive `200` MiB is the out-of-band reservation weka takes off its own `--memory`, so a
  container's weka heap stays a function of cores alone; only the total pod request grows with drives.
- **Compute side**: a per-core floor of `3000` MiB/core, **or** `1700` MiB/core plus a
  **capacity-based share** (the cluster's total claimed drive capacity converted to hugepages and
  divided across the number of compute containers), whichever is larger — plus the same per-core
  DPDK reservation (`64` MiB/core by default, overridable via `.overrides.dpdkBaseMemoryMb.compute`).
  At defaults, when the per-core floor dominates this is **3064 MiB per compute core**; the
  capacity-based share grows with total cluster capacity divided by compute container count, and on
  a drive-dense fleet it dominates by a wide margin — see
  [Compute hugepages are the practical ceiling](#compute-hugepages-are-the-practical-ceiling).

Budget node headroom (CPU, hugepages, memory) for both figures together on any node that hosts both
a drive and a compute container. Node headroom is computed against **all** scheduled, non-terminal
pods on the node, not just Weka's own — a foreign workload's CPU/hugepages/memory requests reduce
believed availability the same way a `WekaContainer`'s do. This cluster's own drive and compute
containers, however, are charged from their **spec** the moment the `WekaContainer` exists — pod or
no pod — because daemonset-mode containers are pinned to their node via `spec.nodeAffinity` at
creation, so a container with no pod yet still occupies its share of the node's headroom.

> **`driveHugepages` / `computeHugepages` bypass all of this.** Setting either on
> `spec.dynamicTemplate` replaces the derived figure on the container outright, but the planner still
> decides node fit against the figure it *computed*. Pin one above a node's allocatable hugepages and
> the plan is declared feasible, the container is created and pinned to that node, and its pod then
> never schedules. Admission warns on the clearest symptom — a pin the **weakest** matched node could
> not host — via `cluster_hugepages_available`, a **warning** in both modes. It is an advisory, not a
> verdict, in both directions: the planner picks nodes by fit, so a fleet with one small node can warn
> and still plan cleanly, while a pin that fits every node but not the ones the planner picks passes
> silently. These are escape hatches for a sizing you have worked out yourself; in this mode the
> derived values are the supported path, and if you use the overrides, check them against
> `weka-capacity explore-nodes` first.

### Compute sizing

Compute is sized from the cluster's drive cores at a **configurable ratio** with a **hard 1:1
floor**:

```
requiredComputeCores = max(
    totalDriveCores,                                            # hard floor — never less than 1:1
    ceil(ratio × driveCores)                                    # the configured ratio
)
```

| Mode | Ratio setting | Default |
|---|---|---|
| Full drives (daemonset, manual container counts) | `capacityPlannerConstraints.fullDrives.computeToDriveCoreRatio` | **2.0** |
| Drive sharing — TLC drive cores | `capacityPlannerConstraints.driveSharing.computeToTlcDriveCoreRatio` | **1.0** |
| Drive sharing — QLC drive cores | `capacityPlannerConstraints.driveSharing.computeToQlcDriveCoreRatio` | **0.0** |

The floor is the **total** drive-core count across the cluster, TLC **and** QLC. A ratio below 1.0
(such as the QLC default of 0.0) therefore reduces how much compute the *ratio term* asks for, but
can never take the plan below one compute core per drive core. Both planners enforce the same floor
when assessing feasibility, and admission validates it too for explicitly-sized clusters.

Because compute scales with **drive cores**, a `driveCores` pin is also a compute lever: pinning
drive cores lower reduces the compute the ratio demands, at no cost in claimed drives.

The requirement is a **hard feasibility gate**: if the fleet cannot host enough compute to satisfy
it, the whole plan is infeasible and **nothing is created** — not even the drive containers — until
enough compute capacity is available.

#### How a compute shortfall is covered

When drive cores grow (newly signed drives, or a new drive node), the extra compute cores the ratio
demands are found in this order:

1. **A new compute container on a node that has none yet**, if any compute-eligible node is free.
   This is preferred because it disturbs nothing already running.
2. **Growing an existing compute container in place**, for whatever new containers cannot carry. Each
   container can grow up to `capacityPlannerConstraints.maxCoresPerContainer` — or up to a pinned
   `computeCores`, whichever is lower — and only as far as its node's own spare CPU, hugepages and
   memory allow.

The two combine: the operator uses the least in-place growth that lets the rest fit on free nodes, so a
free node too small to absorb the whole shortfall is still used for as much as it can take. Only if both
levers together cannot cover it is the plan infeasible.

Only compute containers on nodes the **compute-role selector matches** count toward the cluster's
compute total. One running outside it is not credited, so the operator provisions those cores afresh
instead — keep `roleNodeSelector.compute` wide enough to cover every node your compute containers
actually run on.

Growing compute raises a container's cores and hugepages, so the same
[pod-restart caveat](#pod-restart-caveat) as drive growth applies: the operator updates the spec and
emits a Warning `CapacityGrowthApplied`, and the new sizing takes effect when the pod is next
recreated. Compute container **cores** are never reduced and no compute container is ever removed by
sizing logic.

Their **hugepages** are re-derived on every plan, at the cluster's current claimed capacity and its
final compute container count — a container whose own cores never changed can still owe a new figure,
because the capacity-based term is a share of the cluster total divided across the containers. That
computed figure can fall as well as rise (adding compute containers divides the term further; claiming
more drives raises it), but only a **rise** is ever written to the container's spec. A fall is not: the
running pod's hugepages limit is immutable, so it keeps the larger value regardless of what a later plan
computes, and the operator's own headroom accounting charges hugepages from the spec — writing the lower
figure would make it believe capacity was freed that the pod has not actually released. A node that
cannot absorb a rise makes the plan infeasible, the same as any other reservation this mode cannot
satisfy. Every applied rise emits `CapacityGrowthApplied` and owes a pod recreation.

## Compute hugepages are the practical ceiling

This is the constraint most likely to stop a daemonset cluster from forming, and it is worth working
through once.

Because every drive is claimed, the cluster's **total claimed capacity is fixed** by your node
selector and drive signing. The capacity-based term of a compute container's hugepages is derived
from that total, so **sizing cores down would not shrink it — and the operator does not try.** Drive
cores are whatever [Drives and cores](#drives-and-cores) derives; they are never lowered to make
compute fit. The only dials are the number of compute containers the total is divided across, the
coefficients, and how much capacity you claim in the first place — and all three are yours to set,
not the operator's to guess at.

The formula the planner uses for one compute container, at defaults:

```
capacityBased = totalClaimedTlcGiB × 1024 / hugepagesTlcRatio / computeContainerCount
hugepagesMiB  = max(capacityBased + 1700 × cores, 3000 × cores)     # rounded up to even
                capped at computeMaxHugepagesMiB
              + 64 × cores                                          # DPDK, added after the cap
```

### Worked example: an 8-node fleet

Fleet: **8 hyperconverged nodes**, each with **6 signed full drives of 14,307 GiB**, 63 CPUs and
**60,000 MiB** of free hugepages. Defaults throughout (`hugepagesTlcRatio: 1000`,
`computeMaxHugepagesMiB: 360000`, `fullDrives.computeToDriveCoreRatio: 2.0`).

1. **Claimed capacity.** Every drive is taken: `8 × 6 × 14,307 = 686,736 GiB`, which is
   `686,736 × 1024 / 1000 = 703,217 MiB` of cluster-wide compute hugepages to divide up.
2. **Drive containers.** 6 drives per node → `min(6, 19) = 6` drive cores each; 48 drive cores across
   the fleet. Each drive container reserves `1400 × 6 + 200 × 6 + 64 × 6 = 9,984` MiB — this fleet has
   as many drives as cores, so it is also `6 × 1664` — leaving `60,000 − 9,984 = 50,016` MiB of
   hugepages per node for compute.
3. **Compute requirement.** `2.0 × 48 = 96` compute cores.
4. **Compute hugepages at 8 containers** (one per node), `ceil(96 / 8) = 12` cores each:

   ```
   capacityBased = 703,217 / 8                 = 87,902 MiB
   hugepages     = 87,902 + 1700 × 12          = 108,302 MiB
                 + 64 × 12 (DPDK)              = 109,070 MiB
   ```

   against **50,016 MiB** available. The plan is **infeasible**.

5. **Smaller compute containers do not rescue it.** Even at one core each, the capacity-based term
   alone gives `87,902 + 1,700 + 64 = 89,666` MiB — still above the node's *entire* 60,000 MiB, before
   the drive container takes its share. The binding term is claimed capacity, not cores, which is why
   there is nothing for the operator to trade: only the container **count** in the denominator moves
   this number.

6. **How many compute nodes would be enough?** The planner scans upward for the smallest container
   count whose per-container hugepages fit on that many nodes. At **18** containers of
   `ceil(96 / 18) = 6` cores each:

   ```
   capacityBased = 703,217 / 18                = 39,067 MiB
   hugepages     = 39,067 + 1700 × 6           =  49,268 MiB   (rounded up to even)
                 + 64 × 6 (DPDK)               =  49,652 MiB   ≤ 50,016 ✓
   ```

   17 containers would need 51,950 MiB and misses. So this fleet needs **18 compute-eligible nodes —
   10 more than it has** — to run all 48 drives at the 2:1 ratio.

**The operator does not bargain its way out of this.** It keeps all 48 drives at 6 drive cores per
node and refuses to form the cluster until the fleet has the compute those cores require. Drive cores
are never traded away to fit the compute budget — there is no cap, no descent, no negotiated middle
ground. Either the requested sizing fits, or the plan is infeasible from the start.

### Remedies

Six, in rough order of preference:

1. **Add compute-eligible nodes.** The capacity term is divided by the container count, so more
   compute nodes shrink it directly (the worked example above quantifies it: 10 more nodes).
   Compute-eligible nodes may be **diskless** — they need hugepages and CPU, not drives — and are
   selected by `roleNodeSelector.compute`, falling back to the cluster `nodeSelector`.
2. **Raise `hugepagesTlcRatio`** (Helm `hugepagesTlcRatio`, env `HUGEPAGES_TLC_RATIO`, default
   `1000`). It is the divisor on the capacity term, so doubling it halves that term. This changes how
   much memory weka gets per TiB of capacity — tune it deliberately, not to make a number pass.
3. **Lower `computeMaxHugepagesMiB`** (default `360000`). It hard-caps the per-container base before
   DPDK is added, so it will make an arbitrarily large capacity term fit. That is a deliberate
   under-provision of compute memory relative to what the capacity formula asks for; use it only when
   you know the workload tolerates it.
4. **Pin `driveCores` lower.** This does not touch the capacity term, but it lowers everything around
   it: fewer drive cores means fewer compute cores required by the ratio (so a smaller `1700 × cores`
   term) *and* a smaller per-core hugepages reservation for each node's own drive container, freeing
   room for compute. It frees `1464` MiB per core dropped, not `1664` — the `200` MiB per drive stays,
   because every drive is still claimed. It is the one remedy that costs no capacity at all — but see
   the caveat below.
5. **Pin `numDrives` lower.** Claiming fewer drives per node reduces total claimed capacity, which is
   the numerator. `numDrives: 3` on the fleet above claims 24 of the 48 drives — explicitly, in the
   spec, with the stranded drives reported.
6. **Unpin or lower `computeCores`,** if you pinned it. A pin is honored verbatim, and the container
   count follows from it as `ceil(requiredComputeCores / computeCores)` — so a small pin demands *more*
   containers than there are compute-eligible nodes, while a large one demands more hugepages per
   container than any node has. Leaving it unset lets the planner search for a count that fits.

Remedy 4 has a limit worth knowing: when the capacity term **alone** already exceeds what a node can
give — as in the worked example, where 87,902 MiB busts a 50,016 MiB budget before a single core is
charged — lowering `driveCores` cannot rescue the plan, because cores are not what is binding. Only
remedies 1, 2, 3, 5 and 6 move that number.

### When cores, not memory, are what binds

The compute layout can also fail for a reason no amount of hugepages fixes. Compute spreads one
container per eligible node and a container holds at most
`capacityPlannerConstraints.maxCoresPerContainer` cores, so a fleet of *N* compute-eligible nodes tops
out at `N × maxCoresPerContainer` compute cores no matter how much memory those nodes have. When the
ratio asks for more than that, the plan is infeasible on **cores**, and the report says so rather than
quoting a memory shortfall — the remedy is to add compute-eligible nodes, lower the drive cores driving
the requirement, or raise `maxCoresPerContainer`.

A pinned `computeCores` is checked the same way and against the same two walls, separately: too small a
pin needs more containers than you have compute-eligible nodes; too large a pin needs more hugepages per
container than the nodes can give. Both are reported as themselves.

**This is checked at admission.** The `cluster_auto_full_drives_compute_hugepages` policy projects
claimed capacity from the signed-drive annotations and rejects the `WekaCluster` at `kubectl apply`
(an **error** in strict mode, a **warning** in relaxed mode), naming the needed-vs-available
hugepages, the sufficient compute-node count, and these remedies — rather than letting you discover it
after a failed cluster formation. It is precise about remedy 4: it offers the `driveCores` lever only
when lowering cores would actually help, and otherwise says so explicitly rather than sending you down
a dead end.

**Treat it as best-effort, not a guarantee.** The check projects from the drives signed **at the time
you apply**, and it skips nodes carrying no `weka.io/weka-full-drives` annotation yet — deliberately,
so that creating a cluster before any drives are signed is not blocked. The gap is the partial case: a
cluster applied while signing is still in progress is measured against only the nodes annotated so
far, so a claim that will not fit the finished fleet can pass admission and surface later as
`AutoFullDrivesInfeasible` at runtime. Sign the whole fleet before applying if you want admission to
see the real number.

## Heterogeneous nodes

Because sizing is computed **per node**, nodes with different drive counts naturally get
differently-sized containers — there is no uniform shape to satisfy. A node with 8 signed drives gets
an 8-drive/8-core container; a neighboring node with 4 gets a 4-drive/4-core one; a node with 30 gets
a 30-drive/19-core one. This is the main reason to prefer the daemonset mode over explicit container
counts, which force every drive container to the same shape.

## The node selector sets the container count

Because the operator places exactly one container per eligible node, **the matched node count *is* the
container count** — and no sizing field can raise it. That makes the node selector load-bearing in a
way it is not in the other modes, where a count field can compensate for a narrow selector.

Weka needs a minimum number of each container type to form a cluster at all: **5** by default, **3**
under `ALLOW_SINGLE_PARITY`, tunable via `FORM_CLUSTER_MIN_DRIVE_CONTAINERS` and
`FORM_CLUSTER_MIN_COMPUTE_CONTAINERS` (a floor of 0 disables that side of the check). A selector
matching fewer nodes than its floor can never reach it.

The `cluster_auto_full_drives_min_nodes` policy rejects that at `kubectl apply` — an **error** in
strict mode, a **warning** in relaxed — checking the drive and compute roles independently against
`roleNodeSelector.drive` and `roleNodeSelector.compute` (each falling back to the cluster
`nodeSelector`). In relaxed mode, where the policy only warns, the two roles fail very differently:

- **Too few drive nodes is silent.** The planner has no drive-container floor — it plans one container
  per signed node quite happily — so the plan is feasible, the pods run healthy, and the cluster then
  loops on `MinContainersNotReady` forever with nothing reporting why.
- **Too few compute nodes is visible**, because the planner does floor compute and reports
  `AutoFullDrivesInfeasible` — but only after you have applied and waited, not at apply time.

Unlike the compute-hugepages check, this one is a **real guarantee, not best-effort**: it counts nodes
the selector *matches*, not nodes with signed drives. Labelling and drive signing are independent, so
there is no partial-rollout state for it to be fooled by — a labelled-but-unsigned node still gets a
container once signing runs, and is counted accordingly.

## When a node cannot fit its drives

If **any** eligible node lacks the CPU, hugepages, or memory headroom to host a container sized for
**all** of its signed drives, the **whole plan is infeasible**: zero drive containers, zero compute
containers, on every node, not just the offending one. Nothing is created, and an
`AutoFullDrivesInfeasible` Warning event fires on the `WekaCluster`.

Drives are **never** dropped to make a container fit. The same rule applies on the growth path: an
existing container that cannot grow onto drives its node has gained makes the plan infeasible rather
than absorbing a subset.

The infeasibility report names the offending nodes — not just the first — with the binding dimension
(physical CPU, hugepages, or memory) and the needed-versus-available figures for each, so one
`kubectl describe wekacluster` tells you the story. The message spells out up to ten nodes and then
appends `(+N more)`; the structured report behind it always carries every one. On the **growth** path
specifically, the message adds a clause explaining that the missing headroom may be held by this
cluster's own compute container on that node — since compute reservations only ever rise, a compute
container placed earlier can end up holding room a later drive-container growth needs. See
[Relaxing a pin in stages can strand capacity](#relaxing-a-pin-in-stages-can-strand-capacity).

Remedies:

- **Delete the co-located compute container**, when the report names it as the cause of a growth
  failure — the operator re-places it once the drive container has grown into the freed headroom. If
  weka refuses the deactivation because active compute would drop too low, add compute capacity
  elsewhere first. Only applies to a growth blocked this way, not to a fresh create. See
  [Relaxing a pin in stages can strand capacity](#relaxing-a-pin-in-stages-can-strand-capacity).
- **Free resources on the node** — evict or resize whatever else is holding its CPU, hugepages or
  memory.
- **Pin `driveCores` lower.** This is usually the right answer: drives are decoupled from cores, so
  you keep **every drive** and run them on fewer cores and the container's footprint shrinks with no
  capacity loss. It frees CPU, memory and `1464` MiB of hugepages per core
  dropped; the per-drive hugepages stay. See [Drives and cores](#drives-and-cores).
- **Take the node out of the drive role** — remove it from `roleNodeSelector.drive` (or the cluster
  `nodeSelector`), or unsign its drives.
- **Switch to a drive-sharing mode**, where a container's drive count is not bounded by the node's
  physical devices at all.

> **One bad node blocks the whole cluster.** A single busy or degraded node in the drive-role
> selector stops cluster formation entirely rather than being skipped — a silently smaller cluster is
> worse than a loud failure. It does mean the drive-role selector needs to be curated, and that
> removing a node from it is a legitimate, supported response.

The one exception is transient: a node still hosting a this-cluster drive container that is being
deleted is **skipped** for this reconcile. It clears itself. The Normal
`AutoFullDrivesPlacementDeferred` event accompanies the skip only when the node has signed drives
beyond the ones that container holds; when it holds them all, that one node is skipped silently. If
*every* node is in that state there is nothing left to plan, and the whole pass defers under that same
event rather than reporting the fleet as unsigned.

## Changing sizing mode on a live cluster

Because the mode is implicit, adding or removing `computeContainers`/`driveContainers` on an existing
cluster silently changes what the operator is trying to build. An update whose **derived mode changes
while drive containers exist** is therefore **rejected** by the `cluster_sizing_mode_flip` policy — an
**error in both strict and relaxed mode** — unless the operator can carry the running containers over
into the new mode. Exactly two switches can be:

| Transition | On a live cluster |
|---|---|
| explicit container counts → daemonset | **allowed** — containers are adopted and grown in place |
| drive-sharing → `clusterCapacity` | **allowed** — see [Cluster Capacity](cluster-capacity.md#migrating-from-containercapacity) |
| daemonset → explicit container counts | rejected |
| `clusterCapacity` / `containerCapacity` / `driveCapacity` ↔ daemonset | rejected, both directions |
| explicit container counts ↔ any capacity mode | rejected, both directions |

Before any drive container exists the mode is still free to change however you like — that is what
makes fixing a mistyped spec possible.

### Adopting the daemonset mode (the allowed direction)

Unset **both** `computeContainers` and `driveContainers` in the same update — the
[both-or-neither rule](#the-both-or-neither-rule) still applies, so unsetting one is rejected by the
CRD before this policy is ever consulted:

```yaml
  dynamicTemplate: {}       # was {computeContainers: 6, driveContainers: 6}
```

The operator then **adopts the drive containers you already have** rather than building a second set
beside them. Each existing drive container is matched to the node its **pod** is running on and grown
in place to that node's full signed drive set, through exactly the same growth path as
[Expand-only reconciliation](#expand-only-reconciliation): `AutoFullDrivesGrowthDetected` on the
cluster, `CapacityGrowthApplied` on each container. Nothing is deleted and nothing shrinks. Nodes in
the drive-role selector that had no container get a new node-pinned one.

Drive cores are recomputed as part of that growth and will usually **rise** — a count-based container
sized for `numDrives` drives becomes one sized for `min(node drives, 19)` cores — so a **pod
recreation is owed** on every container whose cores changed. See the
[Pod-restart caveat](#pod-restart-caveat); the containers keep running correctly at their old size
until then.

The switch **is** checked against the daemonset admission gates. Every policy runs on any update that
changes the spec, evaluated against the new spec, so `cluster_auto_full_drives_min_nodes`,
`cluster_auto_full_drives_compute_hugepages` and `cluster_auto_full_drives_pin_exceeds_node_drives`
all apply to the switch exactly as they would to a fresh daemonset cluster. Expect
`compute_hugepages` to be the one that rejects: claimed capacity typically jumps on the switch, since
every drive on every eligible node is now claimed rather than the `numDrives` your counts asked for —
work through [Compute hugepages are the practical ceiling](#compute-hugepages-are-the-practical-ceiling)
before you switch. Those gates remain best-effort, though, projecting from node state as it looks at
apply time, so `AutoFullDrivesInfeasible` can still surface at runtime.

### Why the other transitions stay rejected

Adding `driveContainers: 6` (and the `computeContainers` its partner rule requires) to a live
daemonset cluster would start creating count-based, single-drive, *unpinned* containers alongside the
node-pinned ones already running. There is no adoption available in that direction — the two
populations would simply coexist and plan the same nodes under different rules. The rejection stops
that.

Transitions into or out of `clusterCapacity`, `containerCapacity` or `driveCapacity` are rejected for
a different reason: those are **drive-sharing** modes whose containers hold virtual drives, which the
full-drives planner cannot account for on either side — it can neither see them as occupied nor grow
them. Adoption is only sound between two exclusive full-drives modes.

The same policy also governs the transitions that never touch this mode at all — `counts ↔
clusterCapacity`, `counts ↔ containerCapacity`/`driveCapacity`, and `clusterCapacity → drive-sharing`
— and rejects them for the same underlying reason: neither planner can adopt the other's containers,
and nothing in the operator ever removes a surplus one. `counts → clusterCapacity` is the sharpest
case: the running full-drives containers report no capacity to the capacity planner, so it plans a
fresh set covering the whole target on top of them. The one capacity transition that *is* supported
is `containerCapacity`/`driveCapacity` → `clusterCapacity`, the in-place migration documented in
[Cluster Capacity](cluster-capacity.md#migrating-from-containercapacity).

**The remedy for a rejected update is to revert it** — restore the sizing fields the update changed
and the cluster continues under the mode it was created with.

## Expand-only reconciliation

The mode reconciles **continuously** and **only ever grows** — it never reduces a container's drive
count, cores, or the number of containers, no matter what changes on the cluster or its nodes.

- **A new node starts matching the drive-role selector and has signed drives** → the operator
  creates a new node-pinned drive container for it on the next reconcile. No action needed beyond
  signing drives on the new node.
- **More drives are signed on a node that already has a container** → the operator grows that
  container **in place** to the node's new total — the drives the container already holds plus
  every drive still free on that node, not merely how many *additional* drives got signed since the
  last reconcile. Cores are recomputed too, and rise whenever the derived count — `min(drives, 19)`,
  or the `driveCores` pin — comes out above where they already are. Growth that is actually applied
  is reported on the
  `WekaCluster` as `AutoFullDrivesGrowthDetected`, naming each grown container and its new size (see
  [Events](#events)); growth the planner proposes but does not commit is deliberately not announced.
- **Drive-only growth is cheap, but not free.** CPU and memory are functions of a container's **cores**,
  so absorbing more drives at the same core count costs neither. Hugepages do grow, by `200` MiB per
  added drive (see [Hugepages budget](#hugepages-budget)), so a drives-only growth is charged against
  the node like any other. A node without that headroom blocks it — and, since drives are never dropped
  to make a container fit, that makes the whole plan infeasible (see
  [When a node cannot fit its drives](#when-a-node-cannot-fit-its-drives)).
- **Cores can grow on their own**, with no change in drive count, whenever a container sits *below*
  its derived core count — most often a container carried over from before drives and cores were
  decoupled (see [the upgrade note](#upgrade-note-existing-clusters-gain-drives-and-cores-unless-pinned)),
  or one whose `driveCores` pin was raised. Node headroom is the **gate** on such a growth, not its
  trigger: the operator is closing the gap to the derived count, and a reconcile where the delta does
  not fit makes the plan infeasible rather than deferring it.
- **Drive cores grow past the compute the cluster has** → the operator adds a compute container on a
  free compute-eligible node, or grows an existing one in place when there is no free node (see
  [How a compute shortfall is covered](#how-a-compute-shortfall-is-covered)). Compute containers are
  only ever added, and their cores and hugepages only ever raised — the figure the planner computes
  each pass can dip, but only a rise is ever written to a container's spec (see
  [Compute sizing](#compute-sizing)).
- **Drives are removed, or a node disappears** → nothing shrinks. The existing container keeps its
  current `numDrives`/cores; removal is handled by the normal container-deactivation flows, not by
  this mode's sizing logic.

### Relaxing a pin in stages can strand capacity

**Relax `driveCores` and `numDrives` together, in one patch.** Relaxing them in separate patches can
strand a node permanently: a compute container is sized against what its co-located drive container
needs **right now**, and compute cores and hugepages only ever rise (see
[Compute sizing](#compute-sizing)) — so one placed or grown while the drive container is still pinned
small takes headroom that never comes back, and the drive container's later growth then fails
`AutoFullDrivesInfeasible` for good, even though every reconcile along the way was locally correct.
Dropping both pins at once leaves no intermediate reconcile: the drive walk charges the container's
fully-grown footprint against the node before compute is ever sized against the remainder.

For example, on a node with 60,000 MiB of hugepages and 6 signed drives: a drive container pinned at
`numDrives: 4, driveCores: 2` (4 drives / 2 cores / 3,728 MiB) shares the node with no compute container
yet. Dropping only the `driveCores` pin grows it to 4/4/6,656 MiB and, in the same reconcile, sizes a
*new* compute container against that 4-drive state — as large as the remaining headroom allows (17
cores / 52,088 MiB, leaving 1,256 MiB free). Dropping the `numDrives` pin next asks the drive container
to grow again, to the node's full 6 signed drives — which needs 9,984 MiB — but only 1,256 MiB is free,
and nothing ever shrinks to make room. The plan is `AutoFullDrivesInfeasible` from then on.

**Recovery:** delete the `WekaContainer` for this cluster's compute container on the affected node —
one node at a time — and let the operator re-place it. The drive walk runs before compute sizing
within a single reconcile, so with the compute container gone the drive container grows first and the
replacement is sized against what is left over.

Weka gates the deactivation on how much **active** compute the cluster keeps: if removing this
container would leave too few compute processes for the buckets already allocated, weka refuses it and
the `WekaContainer` sits in `Deleting` while the operator retries. Whether it refuses depends on the
bucket count fixed when the cluster started IO, so it varies between otherwise identical clusters. When
it does refuse, add compute capacity elsewhere first, then retry:

- **a new compute container on a spare compute-eligible node** — widen `roleNodeSelector.compute` to a
  node that has none. A new container gets a new pod, so its cores are active as soon as it joins.
- **or grow an existing compute container and recreate its pod.** Growth alone is not enough: raising
  cores changes the spec but not the running pod, so the extra cores are not active until the pod is
  recreated (see [Pod-restart caveat](#pod-restart-caveat)).

The node whose drive growth is blocked is **deferred**, not failed, while its compute container is
being deleted — so the rest of the fleet keeps growing and compute keeps being planned meanwhile,
which is what makes the added capacity available at all.

### Pod-restart caveat

Raising an existing container's core count changes the `WekaContainer` **spec**, but it does **not**
by itself recreate the running **pod** — the new core count (and the hugepages/memory that go with
it) only take effect the next time the pod is recreated (e.g. an image upgrade, a Helm
`podConfigVersion` bump, or a manual pod delete).

A **drive-only** growth starts serving capacity immediately — weka adds the new drives to the running
container — but it still owes a restart, for two reasons. The pod's declared `weka.io/drives` request
is not updated, since Kubernetes extended-resource requests are immutable once a pod is created, so
any node-level accounting reading the declared request under-counts this container's drives until
then. More importantly, the drives raise the container's **hugepages** reservation by `200` MiB each,
and the pod's hugepages limit is immutable *and enforced* — so until it is recreated the container is
serving those drives on a budget that no longer covers them.

The operator surfaces this distinction as an event on the affected `WekaContainer`:
`CapacityGrowthApplied` fires as a **Warning** for any applied growth, with a message saying which
kind it was: a cores bump needs the restart before the new sizing takes effect at all, while a
drives-only growth is already serving capacity but on a hugepages limit that no longer covers it.
Treat either as an action item. The cluster-level
`AutoFullDrivesGrowthDetected` event says the same thing in aggregate, so a restart owed on any grown
container is visible from `kubectl describe wekacluster` without inspecting each container.

## Upgrade note: existing clusters gain drives, and cores unless pinned

Before drives were decoupled from cores, a drive container's drive count always equalled its core
count, so a node with more drives than cores left the surplus unused — and a node short on headroom
was made to fit by lowering *both* numbers together. On the first reconcile after upgrading the
operator, every such container is **re-derived against its node's full drive set**, exactly as
[Drives and cores](#drives-and-cores) describes for a fresh one:

```
drives     = every signed non-blocked full drive on the node   # or the numDrives pin
driveCores = driveCores pin, else min(drives, 19)
```

Sizing only ever ratchets **up**, so a container never comes out of this smaller than it went in. But
it can come out **wider on both axes**, not just on drives:

| Before upgrade | Node | After first reconcile | Pod restart owed |
|---|---|---|---|
| 19 drives / 19 cores | 30 drives | 30 drives / 19 cores — already at the [19-core cap](#per-container-core-limit) | No |
| 3 drives / 3 cores (old headroom-driven shrink) | 6 drives | 6 drives / **6 cores** | **Yes** |
| any, with `driveCores: N` pinned | any | every drive / N cores | No |

The first row is the common case on drive-dense nodes; it holds only because `min(30, 19)` is
already 19. Any container that sat
**below** its derived core count gains cores too.

**Every growth is charged, on both axes.** CPU and memory follow a container's **cores** alone, so a
drives-only growth costs neither. Hugepages follow both, at `200` MiB per added **drive** and
`1464` MiB per added **core** at defaults (`1400` + `64` DPDK; see
[Hugepages budget](#hugepages-budget)). A growth that raises cores is additionally charged **physical
CPU** per the node's threading topology and CPU policy, and **memory**. If **any** node cannot absorb
its delta on **any** of those dimensions, the **whole plan is infeasible**: nothing is created or grown
anywhere, and `AutoFullDrivesInfeasible` names the offending nodes and the binding dimension (see
[When a node cannot fit its drives](#when-a-node-cannot-fit-its-drives)).

**Hugepages is the dimension most likely to bind here**, and on an upgrade it moves on both axes at
once: a node whose drive container was previously held down to 3 cores gains cores *and* picks up the
per-drive term for every drive it was not counting. **Precondition:** check that every drive node has
headroom for `1400 × min(drives, 19) + 200 × drives + 64 × min(drives, 19)` on top of whatever its
compute container already holds — otherwise the plan goes infeasible on the first reconcile, and a
container that *did* grow would fail to start the next time its pod is recreated.

Expect a fleet-wide growth wave on that first reconcile: every affected drive container is written in
one pass, aggregated into `AutoFullDrivesGrowthDetected` on the cluster. Per container,
`CapacityGrowthApplied` fires as a **Warning** either way — a cores bump changes the pod spec, and a
drives-only growth raises the hugepages reservation, which a running pod's immutable limit does not
cover. Both are action items until the pod is recreated. See the
[Pod-restart caveat](#pod-restart-caveat).

What *can* also change under you is the cluster's total claimed capacity, which roughly doubles on a
fleet that was previously core-limited — and with it the capacity-based share of compute hugepages. A
fleet that formed under the old partial-claim behaviour may now be rejected. See
[Compute hugepages are the practical ceiling](#compute-hugepages-are-the-practical-ceiling).

**To keep the old core count:** pin `driveCores` to its current value before upgrading. This is the
lever that avoids both the restart wave and the hugepages risk at **no cost in capacity** — every
drive is still claimed. (It is the same precaution the
[`numDrives` + `driveCapacity` note below](#upgrade-note-numdrives--drivecapacity-clusters-can-gain-cores-on-operator-upgrade)
calls for, for the same reason.)

**To keep the old drive count:** pin `numDrives` to it before upgrading. Note that this leaves the
surplus drives stranded on purpose, reported as a Normal event.

## Raising `numDrives` on a live cluster

This applies to the modes where `numDrives` sizes the containers directly — explicit container counts,
and `numDrives` + `driveCapacity` — not to the daemonset mode, where `numDrives` is a pin the planner
owns and a change is carried by [Expand-only reconciliation](#expand-only-reconciliation) instead. In
the daemonset mode, relaxing that pin in stages — rather than in the same patch as `driveCores` — can
carry only partially, and permanently so; see
[Relaxing a pin in stages can strand capacity](#relaxing-a-pin-in-stages-can-strand-capacity).

Raising `numDrives` propagates to the drive containers that already exist. Their drive count, cores
and hugepages are rewritten together, and the change is **increase-only**: lowering `numDrives` leaves
running containers alone, because weka cannot hand a drive back without a rebuild. Each affected
container gets a Warning `CapacityGrowthApplied` naming what moved.

**Two pod recreations are owed, and the second one is not optional.** Neither is performed by the
operator — the spec changes immediately and the pods keep running as they were:

- **The drive pods**, so the new hugepages limit and `weka.io/drives` request take effect. This is the
  usual [pod-restart caveat](#pod-restart-caveat).
- **The compute pods**, because weka refuses to activate a drive unless the cluster has the RAM to
  maintain its RAM-to-disk-space ratio — and the RAM it counts is what the **running** compute pods
  hold, not what the operator has reserved for them. Compute hugepages are re-derived when claimed
  capacity grows, but that figure reaches weka only when the compute pods are recreated.

Skip the compute restart and the cluster looks healthy while quietly holding less capacity than you
asked for: the drives are allocated to their containers, but only some are attached, and each drive
container repeats a Warning `DrivesAddingError` carrying weka's own diagnosis —
*"There is not enough memory in the cluster to activate a new drive (N GiB RAM will be necessary to
maintain the RAM to disk-space ratio, but only a total of M GiB is available)"*. The cluster stays
`Ready` throughout; nothing fails, the capacity simply never arrives. Recreating the compute pods
clears it and the remaining drives attach on the next reconcile.

Raising `numDrives` above what the nodes can supply is caught at apply time by admission, which
reports the arithmetic — `driveContainers × numDrives` against the total signed non-blocked drives on
the matched nodes — as an error in strict mode and a warning in relaxed. Past that point the excess is
simply never allocated: the containers hold what their nodes could give and no runtime event reports
the shortfall, so treat the admission warning as the signal.

## Upgrade note: `numDrives` + `driveCapacity` clusters can gain cores on operator upgrade

This applies to the `numDrives` + `driveCapacity` **drive-sharing** mode from the table above, not to
the daemonset mode.

If an existing cluster sets `numDrives` + `driveCapacity` and does **not** pin `driveCores`, upgrading
the operator binary can raise the derived core count on its own, with no spec change from you. Drive
cores for this mode are derived from total capacity (`driveCapacity * numDrives`) using the same
capacity-per-core formula as `clusterCapacity`, governed by the `TlcCapacityPerCoreGiB` constraint
(default **5120** GiB/core). For example, `numDrives: 6, driveCapacity: 3500` previously derived to
**1** core; it now derives to `min(ceil(21000 / 5120), 6)` = **5** cores. Because `driveCores` isn't
pinned, the operator raises the core count (and hugepages) on the already-running drive
containers to match — a plain operator upgrade can resize live containers roughly 5x.

**Precondition:** the node must have enough hugepages headroom for the new core count, or the drive
pod will fail to start the next time it is recreated.

**To avoid this:** pin `driveCores` explicitly to its current value before upgrading the operator.

## QLC drives are not used in this mode

The daemonset mode is **full-drives-only** and has no QLC accounting: capacity, drive cores and
hugepages are all computed as if every drive were TLC. QLC drives are therefore **excluded from
full-drives signing** — the signing path skips them, so they never enter the
`weka.io/weka-full-drives` annotation and are never picked up. Drive type is derived from the
device's IU size (large IU size → QLC), queried through the drive-signing tool. Proxy/shared signing
is unaffected: drive sharing supports QLC.

As a second line of defence, the operator also filters QLC entries out of discovery results before
writing the annotation, emitting a `QLCDrivesSkipped` Warning event on the `WekaContainer` that did
the discovery.

Entries written by older operator versions carry no drive type. Those are left alone and are still
charged as TLC — re-run drive signing (see [Drive Signing](../operations/drive-signing.md)) to
refresh a node's annotation if you suspect it contains QLC devices.

If your deployment needs QLC capacity, use a drive-sharing mode — `clusterCapacity` (with
`driveTypesRatio`) or `containerCapacity` — described in [Cluster Capacity](cluster-capacity.md) and
[Drive Sharing](../operations/drive-sharing.md).

## What admission checks

Seven policies apply to this mode. All of them run both when you create a cluster **and** on any update
that changes its spec, evaluated against the new spec — `cluster_sizing_mode_flip` is simply the only
one that also needs the old spec, which is why it can catch a transition at all. Severities are
`{strict, relaxed}`; the mode is set per policy in the operator's Helm values.

| Policy | Severity | When | Catches |
|---|---|---|---|
| CRD CEL rule (not a policy) | rejection | create + update | Exactly one of `computeContainers`/`driveContainers` set — see [the both-or-neither rule](#the-both-or-neither-rule) |
| `cluster_auto_full_drives_min_nodes` | Error / Warn | create + update | A role selector matching fewer nodes than the form-cluster floor — see [above](#the-node-selector-sets-the-container-count) |
| `cluster_auto_full_drives_compute_hugepages` | Error / Warn | create + update | No compute layout fits: not enough hugepages for the claimed capacity ([the ceiling](#compute-hugepages-are-the-practical-ceiling)), more cores required than the compute-eligible nodes can hold ([cores, not memory](#when-cores-not-memory-are-what-binds)), or a pinned `computeCores` that fits neither |
| `cluster_auto_full_drives_pin_exceeds_node_drives` | Error / Warn | create + update | `driveCores` above a node's effective drive count, or `numDrives` above its signed count |
| `cluster_cores_per_container_limit` | Error / Warn | create + update | A pinned `driveCores`/`computeCores` above [19](#per-container-core-limit) |
| `cluster_cores_available` | Warn / Warn | create + update | A pinned `driveCores`/`computeCores` larger than the smallest matched node's allocatable CPU |
| `cluster_hugepages_available` | Warn / Warn | create + update | A pinned `driveHugepages`/`computeHugepages` larger than the smallest matched node's allocatable hugepages-2Mi — see [the override caveat](#hugepages-budget) |
| `cluster_sizing_mode_flip` | Error / Error | **update** | A change that flips the derived sizing mode while drive containers exist, other than the two supported switches; see [above](#changing-sizing-mode-on-a-live-cluster) |

Only `min_nodes` and the CEL rule are guarantees. The rest project from node state that can still be
changing underneath them — most importantly the drive annotations, which the
[hugepages check reads at apply time](#compute-hugepages-are-the-practical-ceiling). Passing admission
means nothing obviously wrong was visible then, not that the cluster is certain to form.

## Events

Every reason below lands on the **`WekaCluster`** except `UnschedulableDriveContainer`,
`UnschedulableComputeContainer`, and `CapacityGrowthApplied`, which land on the affected
**`WekaContainer`** — so `kubectl describe wekacluster <name>` alone will not show them. Check
`kubectl describe wekacontainer <name>` when you need per-container detail.

Each planner warning has a **kind**, which selects its reason, and within a reason a bounded
**cause**, which says which of that reason's conditions this particular event describes. Events are
throttled on reason plus cause — not on the full message, which for the fleet-wide aggregates below
varies with the affected node set and is deliberately not part of the key. A repeat of the same
reason *and* cause within the window is dropped; a *different* cause under the same reason is posted
at once, since it is a different key. The advisories that describe a **converged** state use a
15-minute window, because a permanently compute-limited cluster is healthy and re-posting a Warning
every minute forever trips alerting. The three fleet-wide aggregates — `DrivesStranded`,
`PlacementDeferred`, `NodeIneligible` — instead use 3 minutes: each names every node hit by one
cause in one message. The node set is not part of the key, so a node joining or leaving that set
under a cause that already fired still waits out the window before it is named — deliberately:
keying on the set too would make a node flapping `Ready`/`NotReady` re-fire the event on every flip,
which is the worst moment for extra event volume. That is added latency, not lost information —
whenever the event does fire, it names the complete current set.

Planner warnings are split into **one reason per kind**, so
`kubectl get events --field-selector reason=AutoFullDrivesInfeasible` isolates the actionable ones
without matching message text, and the kinds that are not problems are Normal rather than Warning.

| Reason | Type | Object | Throttle | When it fires |
|--------|------|--------|----------|---------------|
| `AutoFullDrivesPlanned` | Normal | Cluster | 1 min | A feasible plan that creates ≥1 drive container. Steady-state reconciles stay silent. The message summarizes the create leg. |
| `AutoFullDrivesGrowthDetected` | Normal | Cluster | 1 min | Growth was **applied** to ≥1 existing drive container — `numDrives`/cores actually written to the spec. It names each container, its node, and its new drives/cores, and says when a pod recreation is owed. It does not report growth the planner proposed but did not commit. |
| `AutoFullDrivesGrowthDeferred` | Warning | Cluster | 15 min | Growth was planned but **none** of it could be applied because an update failed. The operator retries on the next reconcile, but a later plan may no longer offer the same growth (node headroom changes as pods schedule). |
| `AutoFullDrivesDrivesStranded` | Normal | Cluster | 3 min | A pinned `numDrives` leaves signed drives unused. One aggregated message covering the whole fleet, listing each node as *used of signed*. **Expected** whenever the pin is in force — it is Normal precisely because you asked for it. Raise or drop `numDrives` to use them. |
| `AutoFullDrivesPlacementDeferred` | Normal | Cluster | 3 min | Placement is waiting this pass, for one of four causes, each its own message and its own throttle key so one cause firing never silences another: an existing container's growth is deferred because its pod is not yet scheduled; a node still hosts a this-cluster drive container that is being deleted; a node hosts a this-cluster compute container that is being deleted and holds what the pending placement needs — a create as readily as a growth, and the message names the binding dimension (cores, hugepages or memory) only when every node hit by this cause is short of the same one; or, fleet-wide, *every* signed drive is still held by containers being deleted so planning cannot start at all — there the drives are signed, merely not released yet. A pass can therefore emit up to three of these events for the per-node causes, each naming every node hit by that one cause, plus the fleet-wide one as a fourth. Clears itself. |
| `AutoFullDrivesNodeIneligible` | Normal | Cluster | 3 min | A node matching the drive-role selector is cordoned, `NotReady`, or carries an untolerated taint, so it gets no **new** container. Normal rather than Warning because on its own it costs nothing — the plan proceeds on the remaining nodes, and if the loss actually matters the plan goes infeasible and `AutoFullDrivesInfeasible` says so. All currently-ineligible nodes arrive in one message, each still carrying its own reason inline (e.g. `cordoned`), but the throttle cause is the *set* of distinct reasons present this pass — so a node going `NotReady` changes the cause and is reported at once even while a separately cordoned node's event is still inside its own window, instead of one reason masking the other. Anything already running there keeps running and still grows. See [Troubleshooting](#troubleshooting). |
| `AutoFullDrivesComputeLayout` | Warning | Cluster | 15 min | Every compute-sizing advisory from the shared compute layout step, joined into one message per pass. |
| `AutoFullDrivesWarning` | Warning | Cluster | 15 min | Fallback only: a planner warning whose kind has no dedicated reason yet. |
| `AutoFullDrivesInfeasible` | Warning | Cluster | 1 min | The plan can't proceed and **nothing is created**. Triggers: a node that cannot fit a container sized for all its drives (named, with the binding dimension and needed-vs-available), `driveCores` pinned above a node's drive count, `numDrives` pinned above a node's signed count, not enough compute capacity for the ratio, or a growth blocked by headroom a co-located compute container holds (see [Relaxing a pin in stages can strand capacity](#relaxing-a-pin-in-stages-can-strand-capacity)). The message names the binding reason and, for a growth blocked this way, the remedy. The full remedy catalog travels in the structured report rather than the event — `weka-capacity plan` renders it. |
| `AutoFullDrivesNoSignedDrives` | Normal | Cluster | 1 min | No node matching the drive-role selector has a signed, non-blocked full drive yet. Planning is deferred; sign drives and the operator picks them up on its own. Drives held by a container being deleted are *not* this case — see `AutoFullDrivesPlacementDeferred`. |
| `UnschedulableDriveContainer` | Warning | **Container** | none | A node-pinned drive container **whose pod never bound** was deleted, so its capacity can be re-placed, after the scheduler had been reporting `PodScheduled=False`/`Reason=Unschedulable` for longer than the GC timeout. The message carries the scheduler's own explanation. A pod still `Pending` for another reason (e.g. a slow DKMS build) is left alone. See [Troubleshooting](#troubleshooting). |
| `UnschedulableComputeContainer` | Warning | **Container** | none | The same, for a compute container. See [Troubleshooting](#troubleshooting). |
| `CapacityGrowthApplied` | Warning | **Container** | none | Growth committed to this container; a pod recreation is owed either way — see [Pod-restart caveat](#pod-restart-caveat). The message distinguishes a cores bump (new sizing does not apply until the restart) from a drives-only growth (capacity is already served, but the pod's hugepages limit has not caught up). |

An **infeasible** plan is the sole signal: when the plan is infeasible only `AutoFullDrivesInfeasible`
is emitted and the advisories above are suppressed for that reconcile, since they would describe
placement that does not happen.

## Troubleshooting

**No drive containers are created.**
Check that your backend nodes have signed full drives — the operator never signs drives itself.
Confirm the `weka.io/weka-full-drives` node annotation is present (see
[Drive Signing](../operations/drive-signing.md)) and that the node matches the drive-role
`nodeSelector` (or the cluster-wide `nodeSelector` if no role-specific selector is set). If the
node's devices are QLC, they are deliberately not signed for full drives — see
[QLC drives are not used in this mode](#qlc-drives-are-not-used-in-this-mode). If drives *are* signed
and nothing is created anyway, the plan is infeasible: look for `AutoFullDrivesInfeasible`.

**A node ends up with fewer drives than you signed on it.**
There is exactly one cause: **`numDrives` is pinned** below the node's signed count, so each node
takes only that many of its largest drives. It is reported as a Normal `AutoFullDrivesDrivesStranded`
event naming the node and the count. Raise or drop the pin to use the rest. Neither the 19-core limit
nor a shortage of node headroom reduces a node's drive count — the first caps cores only, and the
second makes the whole plan infeasible instead.

**A drive container has fewer cores than the node has drives.**
This is normal and lossless, and there are exactly two causes: `driveCores` is pinned below the drive
count, or the node has more than 19 drives and hit the
[per-container core limit](#per-container-core-limit). Every drive is still claimed either way. Note
that a compute shortage is **not** a cause — the operator never lowers drive cores to fit compute; it
reports the plan infeasible instead. See [Drives and cores](#drives-and-cores).

**The `WekaCluster` is rejected at creation with a message about `computeContainers` and
`driveContainers`.**
You set exactly one of the two container counts with no capacity field. Set both (to size by container
counts) or neither (to act as a daemonset) — see
[The both-or-neither rule](#the-both-or-neither-rule).

**The `WekaCluster` is rejected at creation with a message about compute hugepages.**
The `cluster_auto_full_drives_compute_hugepages` policy projected the claimed capacity of your node
selector and found that no compute layout fits. The message quantifies the shortfall and the
compute-node count that would clear it; the remedies are in
[Compute hugepages are the practical ceiling](#compute-hugepages-are-the-practical-ceiling). If it
names **cores** rather than memory as what binds, see
[When cores, not memory, are what binds](#when-cores-not-memory-are-what-binds) — more hugepages will
not help.

**An update to a live cluster is rejected as a mode flip.**
Expected for every mode change but two: adding `computeContainers`/`driveContainers` to a live
daemonset cluster, or setting or unsetting `clusterCapacity`/`containerCapacity`/`driveCapacity` in
any other combination, is rejected once drive containers exist. Revert the change. The two
transitions that are **accepted** are unsetting *both* container counts to adopt the daemonset mode,
and moving a drive-sharing cluster to `clusterCapacity`. Note that unsetting only one count
fails earlier, on the CRD's [both-or-neither rule](#the-both-or-neither-rule), with a different
message. See [Changing sizing mode on a live cluster](#changing-sizing-mode-on-a-live-cluster).

**`AutoFullDrivesInfeasible` fires and nothing is created.**
The message names the binding cause. The five to expect: a node that cannot fit a container sized for
all of its drives ([details and remedies](#when-a-node-cannot-fit-its-drives)); `driveCores` pinned
above a node's drive count; `numDrives` pinned above a node's signed count
([details](#numdrives-as-a-per-node-override)); not enough compute capacity for the
[ratio](#compute-sizing); or a drive-container growth blocked by headroom a co-located compute
container holds ([details](#relaxing-a-pin-in-stages-can-strand-capacity)). Remember that **one** bad
node is enough to block the whole cluster.

**A newly added node isn't getting a drive container.**
Confirm the node matches the drive-role `nodeSelector` and has signed full drives. Reconciliation is
continuous, so once those are true the container is created on the next reconcile without any further
action — unless the node cannot fit a container for all its drives, in which case the whole plan goes
infeasible rather than the node being quietly skipped.

**A drive container never finishes adding its drives, or weka rejects an add with a resource error.**
Check the container's cores against the drives actually present on its node: weka needs at least one
physical drive per drive core. A `driveCores` pin above the effective drive count is caught at plan
time, so the case that reaches runtime is drives **disappearing** under a container that keeps its
cores — reconciliation never shrinks, so a failed or pulled device leaves cores stranded above drives.
Note also that a core change only reaches the running pod once it is recreated — see the
[Pod-restart caveat](#pod-restart-caveat).

**A drive container's pod stays `Pending` and never schedules at all.**
The node it was pinned to can no longer fit it — usually because another workload took the headroom
after planning. Once the scheduler has been reporting `PodScheduled=False` / `Reason=Unschedulable` for
longer than the GC timeout, the operator deletes the container and re-plans that capacity on another
node; the event carries the scheduler's own message, naming the resource that did not fit. A pod merely
`Pending` for some other reason — a slow DKMS kernel-module build, for instance — is left alone. Look
for a Warning `UnschedulableDriveContainer` event on the container.

Three conditions have to hold together, which is what keeps the reap narrow:

- the container is **node-pinned** (`spec.nodeAffinity` set, as the planner sets it on everything it
  places). An unpinned container is not this rule's business: its replacement pod can land anywhere, so
  the operator deletes just the **pod** and lets the scheduler re-place it;
- its pod has **never bound** (`status.nodeAffinity` still empty). A container that ran and only later
  went unschedulable carries cluster state and leaves through deactivation, never a reap;
- the **scheduler has actually ruled**, for longer than the timeout. The clock runs from the scheduler's
  verdict, not from when the container was created, so a long-lived container whose pod has just now
  become unschedulable still gets its full grace period.

**A compute container's pod stays `Pending` and never schedules at all.**
Same cause and same handling: it is deleted on the same terms, and its cores are re-planned onto a node
that can take them. This matters more than it looks — a compute container that never schedules still
counts toward the cluster's compute total, so leaving it in place would hold cores the cluster believes
it has but never gets. Look for a Warning `UnschedulableComputeContainer` event on the container.

**A node is cordoned, `NotReady`, or carries a taint the Weka pods don't tolerate.**
No new container is placed on that node while the condition lasts, and a Normal
`AutoFullDrivesNodeIneligible` event on the `WekaCluster` names every ineligible node together with
its own reason. Nothing is taken away either: a container already there keeps running, its drives
and resources still count as used, and it can still grow in place — cordoning does not evict, so a
node briefly down for maintenance is not treated as lost capacity. Its unclaimed drives still count
toward the fleet total the plan reports, so a summary reading *"40 of 48 drive(s) would be
claimed"* is telling you eight drives are out of reach, not that they vanished.

This is a **skip, not an infeasibility** — the plan proceeds on the remaining nodes. It only becomes
fatal indirectly, when so many nodes are ineligible that what is left cannot satisfy the form-cluster
minimum or the compute ratio. When that happens the binding message describes the shortfall on the
nodes that *remain* (it will say the compute ratio cannot be met across *N* nodes, not that a node was
cordoned), so read the per-node rejection breakdown — which lists an excluded node as
`ineligible (cordoned)`; the `AutoFullDrivesNodeIneligible` advisory itself is suppressed on an
infeasible plan. A plan that went infeasible right after a maintenance cordon is usually short exactly
that node.

Run `weka-capacity explore-nodes` and read the `INELIGIBLE` column for the reason — `cordoned`,
`not ready`, or `untolerated taint`. Clear the condition (uncordon the node, remove the taint, get it
back to `Ready`) and it becomes a placement candidate again.

One caveat on that column: `explore-nodes` is cluster-agnostic, so it judges taints against the
tolerations **every** Weka pod carries and cannot know about a particular cluster's
`spec.tolerations`/`spec.rawTolerations`. A node whose taint your cluster *does* tolerate therefore
still prints `untolerated taint` there while the planner places on it happily. Before removing a taint
on the strength of that column, check whether the cluster already tolerates it.

## Related documentation

- [Drive Signing](../operations/drive-signing.md) — how physical drives get signed and the
  `weka.io/weka-full-drives` annotation this mode reads
- [Cluster Capacity](cluster-capacity.md) — the whole-cluster `clusterCapacity` drive-sharing mode,
  including QLC support
- [Drive Sharing](../operations/drive-sharing.md) — the other drive-sharing capacity modes
  (`containerCapacity`, `driveCapacity`) and the `ssdproxy` architecture
- [Cluster Provisioning](cluster-provisioning.md) — general `WekaCluster` provisioning, including
  the explicit container-count full-drives baseline
