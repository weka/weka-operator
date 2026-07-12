# Cluster Capacity (clusterCapacity)

`clusterCapacity` expresses a single, human-friendly **target usable capacity** for the whole
cluster (e.g. `"300TiB"`, `"8000GB"`). Instead of sizing each container by hand, the operator
translates that one target into drive containers spread across failure domains, sizes compute to
match, and **reconciles toward the target as it changes**.

Concretely, it lets you **drop the detailed sizing knobs** you would otherwise set in
`dynamicTemplate` — the container counts (`driveContainers`, `computeContainers`), per-container
cores (`driveCores`, `computeCores`), and the per-role hugepages (`driveHugepages` /
`driveHugepagesOffset`, `computeHugepages` / `computeHugepagesOffset`). The operator derives all of
them from the one target. You can still pin any of these by hand when you need to; see
[Explicit drive/compute sizing](#explicit-drivecompute-sizing).

It is a **drive-sharing capacity mode** — an alternative to the per-container `containerCapacity`
and the legacy `driveCapacity`/`numDrives` knobs described in the
[Drive Sharing guide](../operations/drive-sharing.md). Physical drives must be signed for proxy
(shared) mode first; see that guide for signing and the `ssdproxy` architecture. Once this planner
decides a per-container capacity, the underlying virtual drives are carved using the
[Virtual Drive Allocation Strategies](../operations/drive-sharing.md#virtual-drive-allocation-strategies)
(even distribution, with fit-to-physical fallback for heterogeneous physical drives).

This document is organized so each fact lives in exactly one place:

- **[Core concepts](#core-concepts)** — the quantities and caps every branch is built on.
- **[Rules that apply everywhere](#rules-that-apply-everywhere)** — the cross-cutting behaviors
  (CR-based accounting, deferral, applying a grow, never-shrink, even balance). The algorithm and
  examples reference these instead of restating them.
- **[The algorithm](#the-algorithm)** — the pure planning flow.
- **[Worked examples](#worked-examples)** — every case as **Input → Expected output**.

## Key properties

- **One target, not per-container.** You set `clusterCapacity`; the planner derives the drive
  container count (`driveContainers`), per-container capacity, cores (`driveCores`), and compute
  sizing (`computeContainers` / `computeCores`) — none of which need to appear in `dynamicTemplate`.
- **Two independent pools.** TLC and QLC are planned **independently**, so one pool can grow while
  the other is a no-op.
- **Stateless planning.** Every reconcile rebuilds the node inventory and re-derives the plan from
  current cluster state. Nothing is persisted between reconciles.
- **Growth creates new failure domains by default.** With
  `enableDynamicDriveScalingForSharedDrives: false` (the default) existing drive containers are
  **frozen** — a grow is satisfied by **creating new containers on fresh FDs**, never by extending
  what is already there. Extending existing containers in place (and the live/deferred grow behavior
  below) is **opt-in** via `enableDynamicDriveScalingForSharedDrives: true`. See
  [Growth in the default config](#growth-in-the-default-config-dynamic-scaling-disabled).
- **The operator does not own failure-domain identity.** Failure-domain (FD) identity is owned by
  Weka: in the default **AUTO mode** (no `spec.failureDomain`) **each host is its own FD**; in
  **label-based mode** (`spec.failureDomain` set) an FD is a group of hosts sharing a label value
  (it may span several hosts). The operator only places/grows drive capacity so it is spread as
  evenly as possible across **at least `minFdNum` failure domains** and is guaranteed to *land* on
  its target nodes (cores, hugepages, and memory all fit).

**Mutual exclusivity & protection floor (enforced at admission):** `clusterCapacity` cannot be
combined with `containerCapacity`, `numDrives`, or `driveCapacity` (CEL). It also requires
`stripeWidth >= 3`, `redundancyLevel >= 2`, `hotSpare >= 0` (hot spare optional; the
`clusterCapacityProtection` webhook), because the FD spread is derived from these. QLC-only is not allowed
(`driveTypesRatio.tlc > 0` is required).

## Basic example

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: cluster-large
  namespace: default
spec:
  image: quay.io/weka.io/weka-in-container:WEKA_VERSION
  imagePullSecret: quay-io-secret
  driversDistService: https://weka-drivers-dist.weka-operator-system.svc.cluster.local:60002
  template: dynamic
  stripeWidth: 16
  redundancyLevel: 4
  hotSpare: 1
  dynamicTemplate:
    clusterCapacity: "300TiB"   # target usable capacity
    driveTypesRatio:
      tlc: 1
      qlc: 10
  nodeSelector:
    weka.io/supports-backends: "true"
  network:
    deviceSubnets:
    - 10.100.0.0/16
```

### Unit handling

The suffix determines the base; suffixes are case-insensitive. `"8000GB"` and `"8000GiB"` are
**not** equivalent.

| Suffix | Base | Example | Result |
|--------|------|---------|--------|
| `GiB`/`Gi`, `TiB`/`Ti`, … | Binary (1024) | `"8000GiB"` | 8000 GiB |
| `GB`, `TB`, `MB`, … | Decimal (1000) | `"8000GB"` | ≈ 7450 GiB |
| Bare unit (`g`, `t`, …) | Binary (backward compatible) | `"8000g"` | 8000 GiB |

## Core concepts

These quantities are recomputed **statelessly, per pool (TLC and QLC independently)** on every
reconcile. They are referenced throughout the algorithm and examples.

| Quantity | Formula | Notes |
|---|---|---|
| `minFdNum` | `stripeWidth + redundancyLevel + hotSpare` | Minimum failure domains capacity must spread across. |
| `rawCapacity` | `clusterCapacity × minFdNum / stripeWidth ÷ 0.9` | Usable target inflated to raw: protection overhead (`minFdNum / stripeWidth`) **and** a ~10% usable reserve (`÷ 0.9`), so ~90% of raw is usable. |
| `tlcRaw`, `qlcRaw` | `rawCapacity` split by `driveTypesRatio` | Per-pool raw targets, planned independently. |
| `current` | Σ this pool's capacity over this cluster's healthy drive containers | What the cluster already has, this pool. |
| `delta` | `desiredRaw − current` | Drives the planning branch. **Sign and the deadband** select grow / no-op / shrink — a positive `delta` within `capacityDeadbandFraction` (default 5%) of `desiredRaw` is also a no-op. |

Fixed caps and floors:

- **10% usable reserve.** On top of the protection inflation, `rawCapacity` divides by `0.9`, keeping
  ~10% of raw as reserve so the usable target is ~90% of raw (matches `RawCapacityGiB` in the allocator).
- **Per-core capacity caps** (defaults): TLC ≈ 5 TiB/core, QLC ≈ 50 TiB/core. A container's cores =
  `ceil(tlc / tlcPerCore) + ceil(qlc / qlcPerCore)`, at least 1.
- **MinChunk = 384 GiB.** No new drive container (and no per-FD share) is created below this floor.
- **Mixed containers are emergent.** A node selected by both the TLC and QLC passes carries a
  single container with both drive types ("mixed"); a node selected by one pass stays pure. The
  mixed count is the *overlap* of the two pools' node selections — never targeted directly.

## Rules that apply everywhere

Every branch of the algorithm and every example below obeys these. They are stated here **once**;
elsewhere you will see a pointer back to this section rather than a repeat.

- **Accounting is CR-based, not pod-based.** `current` is the sum of this cluster's healthy drive
  **WekaContainer CRs'** recorded capacity, and the existing failure-domain set comes from each CR's
  `Status.NodeAffinity` — neither depends on whether a pod is running. **Deleting a pod is not a
  capacity change:** the CR (its capacity and node affinity) persists, so the plan is unchanged and
  the container controller simply recreates the pod. Only deleting the **CR** changes the plan (a
  CR's `Status.NodeAffinity` is cleared only if its node itself is removed). See
  [Deletion and replanning](#deletion-and-replanning).

- **Deferral guard — `ClusterCapacityDeferred`.** While any drive container is **alive but never
  scheduled** (`Status.NodeAffinity == ""` — typically one the planner just *created* during a grow
  whose pod hasn't landed, or one whose node was removed), it counts toward `current` but is **not
  yet** in the growable FD set. Planning against that incomplete snapshot would grow-only
  **concentrate** the fixed total onto the already-placed FDs, so the operator emits
  `ClusterCapacityDeferred` and returns a no-op, retrying until it lands. Routine pod deletion never
  triggers this.

- **Applying a grow — depends on `enableDynamicDriveScalingForSharedDrives`.** **By default
  (`false`) existing containers are never grown in place** — every grow lands on **new** containers
  on fresh FDs (see [Growth in the default config](#growth-in-the-default-config-dynamic-scaling-disabled)),
  so none of the live/deferred mechanics below apply. **When the flag is opted into (`true`)**,
  growing an existing container adds virtual drives **live** when the increase fits the container's
  current cores/hugepages (a drive *capacity*-only increase → `CapacityGrowthApplied` Normal); a grow
  that needs **more cores or hugepages** is **deferred** — the operator writes the new spec and emits
  `CapacityGrowthApplied` (Warning), but it takes effect only after you **manually terminate the
  pod** (the operator never recreates it automatically). Terminate pods **one at a time**, letting
  each return to `Running`, to keep enough live FDs; when a grow bumps both compute and drive cores,
  terminate the **compute** pod(s) first, then the drive pod(s).

- **Usable capacity per pool is gated by the smallest failure domain.** A failure domain smaller
  than the rest adds raw capacity but **zero usable capacity** — usable is bounded by the minimum
  per-FD size across the pool. Therefore the planner only creates new failure domains at the
  **uniform per-FD chunk `T`** (the smallest existing per-FD capacity, floored at MinChunk). A
  candidate node whose headroom is below `T` is never opened as a new FD, and no sub-`T` FD is ever
  created. The uniform-FD rule is unconditional.

- **Never auto-shrink.** Lowering the target (or shifting `driveTypesRatio` away from a type) is a
  **no-op**; when the resulting over-provision exceeds `maxOverProvisionFraction` it emits
  `ClusterCapacityShrink` telling you to delete WekaContainers manually (a smaller, in-cap overage —
  e.g. the create-new rounding over-provision — stays silent). The operator never deletes or shrinks a
  container on its own.

- **Balance is per FD, always uniform.** The divisor is always the **distinct FD count**, never the
  container count, and every FD is sized to the **same per-FD chunk `T`**. A create/grow lands on the
  **fewest FDs that tile evenly** — `minFdNum` when nodes are uniform, more only when a smaller per-FD
  share is needed to fit every chosen FD's ceiling, or when `driveContainers` pins the count. There is
  no ceiling-capped uneven fill: if no uniform `(N, T)` reaches the target the pool is **infeasible**
  (or, for a growth that a dwarfed legacy FD would cap, the [heterogeneous fallback](#the-algorithm)
  lays a fresh uniform set and flags the old FDs deletable). In **label-based mode** an FD groups
  several hosts: the planner sums their capacity toward one FD target and grows them together
  (best-effort, gated by real headroom).

## The algorithm

### Orchestration

```
planClusterCapacity():                         # stateless — recomputed from scratch every reconcile
    if any drive container is alive-but-unscheduled:  emit ClusterCapacityDeferred; no-op   # §Rules
    inventory   = perNodeHeadroom()            # allocatable − EVERY weka container on the node:
                                               # OTHER clusters' AND this cluster's own, all modes
                                               # (drives + compute + ssdproxy) charged in one pass →
                                               # new drive FDs already avoid compute-saturated nodes
    minFdNum    = stripeWidth + redundancyLevel + hotSpare
    rawCapacity = clusterCapacity * minFdNum / stripeWidth
    tlcRaw, qlcRaw = splitByRatio(rawCapacity, driveTypesRatio)

    planPool(TLC, tlcRaw, inventory)           # the two pools are planned INDEPENDENTLY:
    planPool(QLC, qlcRaw, inventory)           # one may grow while the other is a no-op
    mergeColocatedPoolsIntoMixedContainers()   # a node picked by both passes → one mixed container

    totalTlcDriveCores = sum(cores of planned TLC drive containers)
    planCompute(totalTlcDriveCores, inventory) # compute:drive 1:1, sized on POST-drive headroom

    if plan.infeasible:
        emit ClusterCapacityInfeasible; requeue          # create / grow NOTHING
    else:
        apply(growInPlace + createNew); emit per-decision events   # ClusterCapacityPlanned on success
```

### Per-pool planning

The whole pool plan reduces to one rule — **build uniform failure domains, or declare infeasible** —
expressed with two primitives:

- **`selectUniform`** picks the per-FD chunk `T` and FD count `N`. It is uniform-or-infeasible: if no
  `(N, T)` tiles the candidate FDs evenly up to the target, the pool is infeasible.
- **`placeUniform`** makes each chosen FD hold **exactly `T`**, split evenly across the FD's member
  hosts. Per host it **grows** the existing container (same-pool top-up, or cross-pool TLC→mixed
  conversion) or **creates** a new one. An FD that cannot reach `T` is rolled back and skipped — never
  left sub-`T`.

```
planPool(pool, desiredRaw, inventory):                 # pool is TLC or QLC, planned independently
    current = Σ pool capacity over this cluster's healthy drive containers
    delta   = desiredRaw - current
    if delta < 0:  return  # never auto-shrink; emit ClusterCapacityShrink only if current-desiredRaw > maxOverProvisionFraction*desiredRaw (else silent)
    if delta == 0: return  # nothing to do

    # Heterogeneous fallback (driveContainers NOT pinned): when a fresh per-FD chunk ceil(delta/minFdNum)
    # would DWARF the existing FDs — chunk >= imbalanceFactor(8.0) × existing per-FD average — the tiny
    # FDs would cap the uniform level. Abandon them: lay a fresh uniform set on nodes free of this pool
    # (placeUniform over selectUniform), leave the old containers running, emit ClusterCapacityHeterogeneousGrowth
    # ("delete them once data migrates"). Falls through to the cases below if no fresh set reaches target.

    # Otherwise place ONE uniform set — only the choice of T differs:
    #   driveContainers pinned   → T = ceil(desiredRaw / driveContainers)         # over existing + new FDs
    #   pool already has FDs      → planPoolUniformIncrease (pin T0 = smallest existing chunk; see below)
    #   greenfield (no FD yet)    → selectUniform free-picks the smallest N >= minFdNum that tiles evenly
    # placeUniform realizes T (grow existing below T, create the rest). On a shared node it CONVERTS an
    # existing OTHER-pool container to mixed (e.g. adding QLC to a TLC-only node).
    # No (N, T) tiles uniformly up to the target → ClusterCapacityInfeasible (create/grow nothing).
```

**Uniform-increase policy** (pool already has ≥1 FD, count not pinned). To avoid resizing existing
containers, the planner pins the uniform chunk to the **smallest existing per-FD capacity `T0`** (floored
at MinChunk; over-sized anchors don't raise it) and prefers **creating whole new FDs at `T0`** over
growing. New FDs are always sized at the uniform level — never sub-`T`.

```
planPoolUniformIncrease(pool, desiredRaw, existingDrives, cons):
    T0 = max(MinChunk, smallest existing per-FD capacity)

    # --- Step A: create-new-at-T0 (preferred, no spec edits) ---
    kNeeded = ceil(delta / T0)
    if kNeeded spare nodes each hold T0 AND kNeeded*T0 - delta <= maxOverProvisionFraction*desiredRaw:
        create kNeeded new FDs at T0; emit ClusterCapacityOverProvisioned if kNeeded*T0 > delta; return

    # --- Step B: raise the uniform level (ONLY when enableDynamicDriveScalingForSharedDrives is ON) ---
    # smallest N >= max(minFdNum, numExisting) and level L >= T0 where: kFresh = N-numExisting spare nodes
    # hold L, every existing FD can reach L, and existingReach(L) + kFresh*L covers desiredRaw within the
    # over-provision cap. Grow every below-L existing FD to L and create kFresh new FDs at L.
    # Gate: (L - T0)/T0 >= minGrowthFraction, else the grow is too small → infeasible.

    # --- Step C: infeasible ---
    # No spare node at T0 AND (scaling is OFF, or no (N, L) clears coverage + the minGrowthFraction gate)
    # → ClusterCapacityInfeasible.
```

Existing FDs are never shrunk; over-sized anchors stay at their current level (only below-`L` FDs are
raised in Step B). The grow step edits container specs — see [§Rules](#rules-that-apply-everywhere) for
live-vs-deferred mechanics. With `enableDynamicDriveScalingForSharedDrives: false` (the default) Step B is
unavailable, so a raise is met entirely by Step A (new FDs at `T0`) or is infeasible.

### Node feasibility (the fit gate)

A node can host a new container — or absorb an in-place extension — only if it has enough of **all
of**: drive capacity of that type, CPU cores, **hugepages including the per-core DPDK base**, and
**memory/RSS**. Every figure is **net of** what other clusters and this cluster's own containers
already consume on the node. The per-container virtual-drive count is capped at
`maxVirtualDrivesPerCore × NumCores`. If a node can't fit, the planner records the binding reason
(e.g. "node X: hugepages short by N MiB").

The planner models each drive container with these coefficients (the container controller computes
the authoritative pod values when it builds the pod):

| Coefficient | Value | Used for |
|---|---|---|
| hugepages per drive core | 1600 MiB **+ DPDK base/core** | drive node-fit estimate |
| DPDK base memory per core | 64 MiB default, per-role (overridable via `DpdkBaseMemoryMb`) | added to drive **and** compute hugepage reservation |
| base memory per drive container | 8000 MiB | drive node-fit estimate |
| memory per drive core | 3000 MiB | drive node-fit estimate |

A node's **headroom for a pool** is the **minimum** of: remaining drive capacity; cores × per-core
capacity; hugepages ÷ (1600 + DPDK base/core) × per-core capacity; and (memory − base) ÷ 3000 ×
per-core capacity — each net of other and own consumption. Reserving the same `1600 + DPDK base`
per core that `GetContainerHugepages` requests keeps the fit-gate aligned with the scheduler's real
`hugepages-2Mi` request (otherwise pools would co-locate and the scheduler would reject them with
*Insufficient hugepages-2Mi*).

### Compute sizing

Compute is sized **after drive placement**, from `totalTlcDriveCores`, to honor a compute:drive
**1:1 core ratio**, bounded by the **real post-drive per-node core/hugepage headroom**. Compute
carries its **own** role node selector (`roleNodeSelector.compute`, falling back to the cluster
`nodeSelector`; empty matches all nodes), so the compute pool may differ from the drive nodes and
may include **diskless** nodes. On a node shared by both roles, compute draws from the
cores/hugepages left **after** drives; a diskless compute node contributes its full headroom. Each
new compute container is **node-pinned** (best-fit on post-drive headroom) so it never lands where
it cannot host both drives and compute. A compute core/hugepage change applies per
[§Rules](#rules-that-apply-everywhere) (deferred until you manually terminate the pod).

```
planCompute(totalTlcDriveCores, inventory):           # t = totalTlcDriveCores
    nodes        = compute-selector nodes with post-drive, post-existing-compute headroom
    coreHeadroom = [coresFree(n) for n in nodes]
    floor        = minFdNum

    # ---- (1) Derive the UNIFORM (count, cores) target — IDENTICAL for greenfield and grow ----
    hmin            = min(coreHeadroom)                # smallest node binds: cores is uniform and
                                                       # spreads one-per-node, so it must fit anywhere
    perContainerCap = min(maxComputeCoresPerNode, hmin)   # cap 0 == policy disabled; hmin still binds

    if computeContainers set and computeCores set:     # honored as-is
        count, cores = computeContainers, computeCores
        infeasible if cores > perContainerCap or count*cores < t or count > len(nodes)
    elif computeCores set:
        cores = computeCores;  infeasible if cores > perContainerCap
        count = max(floor, ceil(t / cores));  infeasible if count > len(nodes)
    elif computeContainers set:
        count = computeContainers;  infeasible if count > len(nodes)
        cores = max(1, ceil(t / count))
    else:                                              # both auto → MINIMIZE the container count
        count = max(floor, ceil(t / perContainerCap));  infeasible if count > len(nodes)
        cores = max(1, ceil(t / count))

    # ---- (2) Apply target — grow in place first, freeze where the node is full, then fill ----
    for ec in existingCompute pinned to a node:
        if node has headroom for (cores - ec.cores):  growInPlace(ec -> cores)   # deferred per §Rules
        else:                                         freeze(ec at ec.cores)     # no disruption

    shortfall   = count*cores - sum(cores existing computes now supply)
    fitNodes    = free nodes with headroom >= cores, ordered for FD SPREAD (fresh FDs first)
    nNew        = ceil(shortfall / cores)
    if nNew > len(fitNodes):                  plan.infeasible = "not enough free compute nodes"
    while distinctFDs(covered ∪ fitNodes[:nNew]) < minFdNum and nNew < len(fitNodes): nNew += 1
    if distinctFDs(covered ∪ fitNodes[:nNew]) < minFdNum: plan.infeasible = "compute spans < minFdNum FDs"
    else: place shortfall as nNew uniformly-balanced containers, one per node (each <= cores, >=1 core)
```

| `computeContainers` | `computeCores` | Resulting count | Cores/container |
|---|---|---|---|
| unset | unset | `max(floor, ceil(t / perContainerCap))` — **minimized** | `max(1, ceil(t / count))` |
| unset | set | `max(floor, ceil(t / cores))` | `cores`; **infeasible** if `cores > perContainerCap` |
| set | unset | honored (**infeasible** if `> compute-node count`) | `max(1, ceil(t / count))` |
| set | set | honored | `computeCores`; **infeasible** if `count×cores < t` or exceeds per-node headroom |

- **`floor = minFdNum`** (one above Weka's `stripeWidth + redundancyLevel` minimum) leaves headroom
  to delete/recreate a single compute pod without dropping below Weka's minimum.
- **Why minimize the count.** Sizing each container as wide as headroom allows yields a few wide
  containers instead of many single-core ones. Example: target **200 TiB**, 3/2/1, TLC-only across
  **14 large nodes** ⇒ `t = 84`; with `maxComputeCoresPerNode = 16` ⇒ **6 × 14 cores**, not 84
  single-core containers.
- **FD diversity (label mode).** The layout must span **≥ `minFdNum` distinct FDs**, not just
  nodes; the planner orders free nodes fresh-FD-first and extends the count until the span is met,
  failing fast if it cannot.

### Growth in the default config (dynamic scaling disabled)

`enableDynamicDriveScalingForSharedDrives: false` is **the default**. With this setting, in-place
growth of existing containers is not allowed, so the uniform-increase path (Step B) is unavailable.
The planner covers a capacity increase exclusively via **Step A: creating new FDs at the uniform
per-FD chunk `T`**. If no spare node has headroom `≥ T`, the plan is **infeasible** — `ClusterCapacityInfeasible`
fires and nothing is created or grown. Add a node (or wait for a draining node to free), then the
planner places a clean new FD.

The [heterogeneous fallback](#the-algorithm) still applies in the default config (it only **creates**
fresh FDs — it never grows existing containers): when a fresh per-FD chunk would dwarf the existing FDs
it lays a fresh uniform set on spare nodes and flags the old FDs deletable. Opt into
`enableDynamicDriveScalingForSharedDrives: true` to additionally allow Step B (the in-place uniform grow
on the uniform-increase path).

## Worked examples

Small round numbers for clarity. Unless noted, all use **SW=3, RL=2, HS=1 ⇒ minFdNum = 6**, so
`rawCapacity = 2 × usable`; per-core caps TLC ≈ 5 TiB/core, QLC ≈ 50 TiB/core. *For round numbers the
examples use the simplified `rawCapacity = usable × minFdNum / stripeWidth` and omit the 10% usable
reserve (`÷ 0.9`) from [Core concepts](#core-concepts); real raw is ~11% higher. The reserve does not
change the placement logic these examples illustrate (FD count, uniform tiling, fallback).* Each example states
its **Input** (hardware + spec + current state) and the planner's **Expected output** (the
grow/create plan + events). Behaviors marked "deferred", "live", "no-op shrink", or "deferred
planning" are defined once in [§Rules](#rules-that-apply-everywhere).

### Greenfield (initial create)

**A — Homogeneous, TLC-only.**

*Input:* 6 nodes × 100 TiB TLC. `clusterCapacity: 90TiB`, TLC-only ⇒ rawTLC 180. No existing
containers.

*Expected output:*

| Node | Avail TLC | Plan |
|---|---|---|
| n1…n6 | 100 TiB | **CREATE** TLC **30 TiB** each (180 / 6) |

Uniform nodes ⇒ the even 30 TiB share clears every ceiling at `minFdNum` ⇒ exactly 6 FDs.

**A′ — Heterogeneous, add an FD to stay uniform.**

*Input:* 2 nodes × 100 TiB + 5 nodes × 64 TiB. `clusterCapacity: 210TiB` ⇒ rawTLC 420. Greenfield.

*Expected output:* **CREATE** **7 × 60 TiB** (one per FD).

| Try | Even share `⌈420/N⌉` | Fits every chosen ceiling? | Decision |
|---|---|---|---|
| N = 6 | 70 TiB | ✗ (64 TiB nodes can't hold 70) | add an FD |
| N = 7 | **60 TiB** | ✓ (≤ 64 and ≤ 100) | **CREATE** 7 × **60 TiB** |

`selectUniform` requires **every** chosen FD to hold the even share, so N = 6 (70 TiB share) is
rejected — the 64 TiB nodes can't hold 70. Growing to N = 7 lowers the share to 60 TiB, which clears
every ceiling, so all 7 FDs are equal. (Had no `N` produced a uniform fit, the pool would be
**infeasible** — never a ceiling-capped uneven fill.)

**B — TLC+QLC, fail-fast on too few QLC FDs.**

*Input:* `clusterCapacity: 90TiB`, ratio 1:2 ⇒ tlcRaw 60, qlcRaw 120. 6 TLC-capable nodes (100 TiB
TLC) and only **5** QLC-capable nodes (100 TiB QLC). Greenfield.

*Expected output:*

| Pool | Desired | FDs available | Plan |
|---|---|---|---|
| TLC | 60 (6 × 10 TiB) | 6 | would **CREATE** 6 × 10 TiB |
| QLC | 120 (6 × 20 TiB) | **5** of 6 | **infeasible** ⇒ `ClusterCapacityInfeasible` ("QLC: only 5 of 6 required failure domains have capacity") |

Each pool needs 6 FDs = minFdNum; QLC is short one ⇒ fail-fast, **nothing created**.

**B′ — Mixed as emergent overlap.**

*Input:* `clusterCapacity: 120TiB`, ratio 1:3 ⇒ tlcRaw 60, qlcRaw 180; each pool wants 6 FDs.
Hardware (9 nodes): **3 TLC-only** (100 TiB TLC), **3 QLC-only** (100 TiB QLC), and **3 "combo"
nodes** whose physical drives include *both* types (100 TiB TLC + 100 TiB QLC), so each can host
either drive type. Greenfield.

*Expected output:*

| Pool | Candidate FDs (= minFdNum, so all are forced) | Per-FD |
|---|---|---|
| TLC | 3 TLC-only + 3 combo (QLC-only nodes have no TLC) | 60/6 = **10 TiB** |
| QLC | 3 QLC-only + 3 combo (TLC-only nodes have no QLC) | 180/6 = **30 TiB** |

Each pool has exactly `minFdNum = 6` candidate FDs, so both passes are forced onto **all** of them —
including the 3 combo nodes, which both select. Overlap = those **3 combo nodes** ⇒ **3 mixed**
(TLC 10 + QLC 30). The 3 TLC-only ⇒ **3 pure-TLC** (10); the 3 QLC-only ⇒ **3 pure-QLC** (30). The
mixed count is the *overlap* of the two independent selections — incidental to topology, never
targeted.

### Grow WITHOUT adding failure domains (in place)

> The examples C, D, and H below **assume `enableDynamicDriveScalingForSharedDrives: true`** (the
> opt-in). In the default config (`false`) existing containers are frozen and a raise is covered by
> new FDs instead — see [Grow with dynamic scaling disabled (default)](#grow-with-dynamic-scaling-disabled-default).

**C — Grow in place, no spare nodes (from A).**

*Input:* state from A (6 FDs × 30 TiB, 6 cores each), **no spare nodes** (all 6 nodes already host
a container). Raise `clusterCapacity` 90 → 120 TiB ⇒ rawTLC 180 → 240, delta 60.

Step A (create-new) cannot proceed — no spare node has 30 TiB free. Step B (uniform grow): target
level `L = ceil(240 / 6) = 40 TiB`; grow fraction `(40 − 30) / 30 = 33%` ≥ 20% threshold ✓; each
node has 70 TiB headroom.

*Expected output:*

| Node | Existing | Free | Plan |
|---|---|---|---|
| n1…n6 | 30 TiB (6 cores) | 70 TiB | **GROW in place** → **40 TiB** uniformly; cores `⌈30/5⌉=6 → ⌈40/5⌉=8` ⇒ deferred |

A grow staying within the existing 6 cores (TLC ≤ 30 TiB) would apply **live** — see
[§Rules](#rules-that-apply-everywhere).

**D — Grow with TLC→mixed conversion.**

*Input:* 6 nodes carry both types, each running a **TLC-only** 10 TiB container, 100 TiB QLC free.
QLC target rises: need +60 TiB QLC over 6 FDs = +10/FD. (`enableDynamicDriveScalingForSharedDrives: true`)

*Expected output:*

| Node | Existing | QLC free | Plan |
|---|---|---|---|
| n1…n6 | TLC 10 (TLC-only) | 100 TiB | **GROW + CONVERT to mixed**: TLC 10 + **QLC 10** in place (no new QLC pod); cores `⌈10/5⌉=2 → ⌈10/5⌉+⌈10/50⌉=3` ⇒ deferred |

**H — `driveTypesRatio` change (same target, shift TLC↔QLC).**

*Input:* 6 nodes, each running a **mixed** TLC 10 + QLC 20 (ratio 1:2, target 90 TiB). Keep target
90 TiB but flip ratio **1:2 → 2:1** ⇒ tlcRaw 120, qlcRaw 60.

*Expected output:*

| Pool | Current | New desired | Action |
|---|---|---|---|
| TLC | 60 TiB | 120 TiB | **GROW** each TLC 10 → 20 in place. Cores `3 → 5` ⇒ deferred. |
| QLC | 120 TiB | 60 TiB | **No-op** (shrink); QLC stays 20/node + `ClusterCapacityShrink` |

Result: 6 mixed TLC 20 + QLC 20 — one pool grows while the other is a no-op.

### Grow WITH adding failure domains

> Examples J, J′, and E below involve adding new failure domains to an existing pool.

**J — Spare node available; new FD created, existing specs untouched.**

*Input:* 6 FDs × 10 TiB (rawTLC 60). 1 spare node (100 TiB free). Raise rawTLC 60 → 70, delta 10.

Step A: `kNeeded = ceil(10 / 10) = 1`, spare node has 100 TiB ≥ T = 10 TiB, overshoot = 0.

*Expected output:*

| Node | Existing | Plan |
|---|---|---|
| n1…n6 | 10 TiB | **unchanged** — no spec edits |
| spare | — | **CREATE** TLC **10 TiB** (1 new FD at T) |

Result: **7 FDs all at 10 TiB** = 70 ✓. No existing container is resized.

**J′ — Increase not a clean multiple (over-provision by one chunk).**

*Input:* 6 FDs × 10 TiB (rawTLC 60). 1 spare node (100 TiB free). Raise rawTLC 60 → 66, delta 6.

Step A: `kNeeded = ceil(6 / 10) = 1`, spare node available, overshoot = 1 × 10 − 6 = 4 TiB.
Over-provision cap = 0.20 × 66 ≈ 13.2 TiB; 4 TiB is within cap.

*Expected output:*

| Node | Existing | Plan |
|---|---|---|
| n1…n6 | 10 TiB | **unchanged** |
| spare | — | **CREATE** TLC **10 TiB** |

Total: 70 TiB raw (over-provisioned by 4 TiB). `ClusterCapacityOverProvisioned` fires:

> *"TLC: +6144 GiB covered by adding 1 new failure domain(s), each sized to a uniform 10240 GiB; this
> over-provisions the target by 4096 GiB (within maxOverProvisionFraction=0.20) — intentional rounding to
> keep failure domains uniformly sized, not reclaimable excess (no manual shrink needed)"*

**J″ — Both create AND grow (large increase, partial spare coverage).**

> **Requires `enableDynamicDriveScalingForSharedDrives: true`** for the grow leg.

*Input:* 6 FDs × 10 TiB (rawTLC 60). **2 spare nodes** (100 TiB free each). Raise rawTLC 60 → 120,
delta 60.

Step A: `kNeeded = ceil(60 / 10) = 6` new FDs needed at T = 10 TiB, but only **2 spare nodes** are
available — not enough. Step A cannot fully cover delta.

Step B: search the smallest `N ≥ 6` and level `L ≥ T` where 2 spare nodes + 6 existing FDs grown to
`L` cover 120 GiB (all sized uniformly at `L`). At `N = 8`, `L = ceil(120/8) = 15 TiB`;
grow fraction `(15 − 10) / 10 = 50%` ≥ 20% threshold ✓; each existing node has ≥ 5 TiB headroom.

*Expected output:*

| Node | Existing | Plan |
|---|---|---|
| n1…n6 | 10 TiB | **GROW in place** → **15 TiB** uniformly (cores recalculated ⇒ deferred) |
| spare1…spare2 | — | **CREATE** TLC **15 TiB** each (at the raised uniform level) |

Result: **8 FDs all at 15 TiB** = 120 ✓.

Why both happen: the planner first tries to cover the full delta by adding new FDs at T = 10 TiB
(Step A — no spec edits). With only 2 spare nodes it can place 20 TiB, leaving a 40 TiB shortfall
that no spare node can host. Step B then raises the uniform level to 15 TiB and grows every existing
FD to that level while also creating the 2 new FDs at the same level — keeping all FDs uniform. The
existing FDs are grown only because create-new alone could not cover the delta, and only to the
minimum level required.

**E — Heterogeneous / balanced-fresh fallback (`imbalanceFactor` = 8.0 default).**

*Input:* `clusterCapacity` 30 TiB (rawTLC 60) on 6 small nodes (15 TiB each), each running a TLC
10 TiB container (~5 TiB headroom each). 6 big nodes (120 TiB) just added, empty. Raise to **300 TiB**
(rawTLC 600) ⇒ delta 540. The even per-FD grow chunk 540/6 = ~90 TiB is **≥ 8 × the existing 10 TiB**
⇒ fallback fires.

*Expected output:*

| Node | Existing | Plan |
|---|---|---|
| small1…small6 (15 TiB) | TLC 10 TiB | left running; **`ClusterCapacityHeterogeneousGrowth` (Warning): delete these** |
| big1…big6 (120 TiB) | — | **CREATE** TLC 600/6 = **100 TiB** each (balanced fresh; existing ignored) |

The fresh chunk would be ~90 TiB but the small nodes can grow to at most 15 TiB, so a uniform grow that
*kept* them is infeasible — the heterogeneous fallback abandons them and tiles the 6 big nodes uniformly
at 100 TiB instead. Note the fallback only **creates** (it never grows the small FDs), so it applies
even in the default config (`enableDynamicDriveScalingForSharedDrives: false`).

### Grow with dynamic scaling disabled (default)

These are the **default** config (`enableDynamicDriveScalingForSharedDrives: false`): in-place growth
is not allowed, so a capacity increase is covered by **new FDs at the uniform chunk T** (Step A only).
If no spare node has headroom ≥ T, the plan is infeasible.

**C-off — Spare node available, growth on a new FD.**

*Input:* state from A (6 FDs × 30 TiB). T = 30 TiB. Raise `clusterCapacity` 90 → 120 TiB ⇒
rawTLC 180 → 240, delta 60. **2 spare nodes** (100 TiB) available.

Step A: `kNeeded = ceil(60/30) = 2`, both spare nodes have ≥ 30 TiB, overshoot = 0.

*Expected output:*

| Node | Existing | Plan |
|---|---|---|
| n1…n6 | 30 TiB | **unchanged** |
| spare1…spare2 | — | **CREATE** TLC **30 TiB** each |

Result: **8 FDs all at 30 TiB** = 240 ✓ — uniform.

**Y-off — Infeasible: no spare node at T.**

*Input:* state from A (6 FDs × 30 TiB). T = 30 TiB. Raise 90 → 300 TiB ⇒ rawTLC 600, delta 420.
**No spare nodes** (dynamic scaling `false`, so growing existing is not an option).

*Expected output:* **`ClusterCapacityInfeasible`**, nothing created:

> *"TLC: cannot satisfy clusterCapacity (+430080 GiB) — dynamic drive scaling for shared drives is
> disabled, so existing containers cannot grow, and no spare node has 30720 GiB free to add a failure
> domain. Add nodes or enable enableDynamicDriveScalingForSharedDrives"*

With the flag **on**, the same raise would grow the 6 existing FDs in place to cover the shortfall.

### Deletion and replanning

Accounting is **CR-based**, so a deletion only changes the plan when the **CR** (not just its pod)
goes away — see [§Rules](#rules-that-apply-everywhere).

**R1 — Permanent delete (WekaContainer CR removed).**

*Input:* from A (6 FDs × 30 TiB, target 90 TiB, rawTLC 180). Delete the **CR** on n1.

*Expected output:* next reconcile sees `current = 5 × 30 = 150`, `delta = 180 − 150 = 30 > 0`. The
planner's Step A looks for a spare node with ≥ T = 30 TiB free:

| Node | Existing | Plan |
|---|---|---|
| n2…n6 | 30 TiB | **unchanged** — no spec edits |
| n1 (freed) or other spare | — | **CREATE** TLC **30 TiB** to restore the 6th FD |

If n1 is still **draining** (its CR is deleted but the node hasn't freed its capacity yet), it is not
counted as a spare — so no spare node is available at T. The plan is
**`ClusterCapacityInfeasible`** ("cannot satisfy clusterCapacity — no spare node has 30720 GiB free"),
and the operator retries. Once the drain completes, n1 becomes a spare and Step A places the
replacement FD cleanly. **Delete one container at a time and let each settle** before the next delete
— that ensures a spare node is always available for the replacement.

**R2 — A newly-created container still landing (deferred planning).**

*Input:* a drive container is **alive but never scheduled** (`Status.NodeAffinity == ""`) — typically
one the planner just created during a grow whose pod hasn't landed, or one whose node was removed.

*Expected output:* the operator emits **`ClusterCapacityDeferred`** and returns a no-op, retrying
until it lands (self-healing guard during create/grow churn). This is **not** a response to routine
pod deletion, which leaves the CR's node affinity and the plan untouched
([§Rules](#rules-that-apply-everywhere)).

**R3 — Tiny increase, no spare nodes (below grow threshold).**

> **Requires `enableDynamicDriveScalingForSharedDrives: true`** — with `false`, the same scenario
> emits the "dynamic scaling disabled, no spare" message instead.

*Input:* 6 FDs × 30 TiB (rawTLC 180). **No spare nodes**. Raise rawTLC 180 → 186, delta 6 TiB.

Step A: no spare nodes at T = 30 TiB. Step B: the uniform level needed is
`L = ceil(186/6) = 31 TiB`; grow fraction `(31 − 30) / 30 ≈ 3%` — **below the 20% MinGrowthFraction
threshold**. The grow is skipped, and no spare node can host a new FD.

*Expected output:* **`ClusterCapacityInfeasible`**:

> *"TLC: cannot satisfy clusterCapacity — need +6144 GiB. Adding a failure domain requires a spare
> node with >=30720 GiB free (the uniform per-FD size); none is available. Growing existing
> containers in place would raise each by only 3% (below minGrowthFraction=0.20), so it is skipped.
> Resolve by: adding a node, or raising clusterCapacity by at least one 30720 GiB failure-domain
> chunk, or lowering minGrowthFraction"*

To resolve: add a node (so Step A can place a full T = 30 TiB FD), raise `clusterCapacity` by at
least one 30 TiB chunk (so the grow fraction clears 20%), or lower `minGrowthFraction`.

### Label-based failure domains

**I — FD spanning multiple hosts.**

*Input:* `spec.failureDomain` = rack label; **12 hosts in 6 FDs (2 hosts/rack)**, each host 50 TiB
TLC. `clusterCapacity: 90TiB` ⇒ rawTLC 180 ⇒ **30 TiB per FD**. Greenfield.

*Expected output:*

| FD (rack) | Hosts | Per-host plan |
|---|---|---|
| rack1…rack6 | 2 × 50 TiB | **CREATE** TLC ~**15 TiB** on each host (30 TiB/FD), carrying `spec.failureDomain=rackN` |

6 FDs = minFdNum ✓. Capacity is balanced **per FD** (sum of its hosts), not per host.

**K — Label-based GROW with uneven hosts.**

*Input:* `spec.failureDomain` = rack. **6 FDs**, but **rack1 has 2 hosts** and rack2…rack6 have 1
each; every host runs TLC **10 TiB** (rack1 = 20, others = 10). Raise so rawTLC = 180 ⇒ **30 TiB per
FD**.

*Expected output:*

| FD (rack) | Hosts | Current | Plan |
|---|---|---|---|
| rack1 | 2 | 20 TiB | **GROW** to **30 TiB/FD**, split evenly across its hosts (n1a 10→15, n1b 10→15) |
| rack2…rack6 | 1 each | 10 TiB | **GROW** each 10 → **30 TiB** |

The divisor is the **distinct FD count (6)**, not the container count (7); rack1 reaches the same
30 TiB total as a single-host FD, with its `T = 30` split **evenly** across its 2 hosts (15 each —
`placeUniform` never front-loads one host). Each grown host crosses its core ceiling ⇒ deferred. (This GROW
assumes `enableDynamicDriveScalingForSharedDrives: true`; in the default config the existing hosts
are frozen and the increase would instead create capacity on fresh FDs.)

### Contention with other clusters

**G — Capacity partly taken by other clusters (can't tile uniformly ⇒ infeasible).**

*Input:* 6 nodes × 100 TiB; cluster-B already uses 75 TiB on n4…n6 ⇒ 25 TiB free there.
`clusterCapacity: 90TiB` ⇒ rawTLC 180. Greenfield (for us).

*Expected output:* `minFdNum = 6` and there are exactly 6 candidate FDs, so N = 6 is forced and the
even share is `⌈180/6⌉ = 30 TiB` — but n4…n6 have only 25 TiB free, below that share. No `N` tiles
uniformly (there is no 7th node to lower the share), so the plan is **`ClusterCapacityInfeasible`**:

> *"TLC: cannot place 184320 GiB uniformly across 6 failure domains — the smallest usable FD holds
> 25600 GiB, below the 30720 GiB per-FD share; add capacity or lower clusterCapacity"*

The fix is to free contended capacity (or lower the target to ≤ 75 TiB ⇒ rawTLC 150 ⇒ 25 TiB/FD, which
all 6 nodes hold). The planner never does an uneven 35/25 fill — usable would be gated by the 25 TiB
FDs anyway, so the extra raw on n1…n3 would be wasted.

### Explicit sizing and separate compute pool

**L — Explicit `driveContainers` / `driveCores` (greenfield, TLC-only).**

*Input:* `clusterCapacity: 90TiB` ⇒ rawTLC 180. Pin `driveContainers: 9`, `driveCores: 4`. 9 ≥
minFdNum ✓.

*Expected output:*

| Setting | Result |
|---|---|
| `driveContainers: 9` | spread 180 across **exactly 9 FDs** = **20 TiB each** |
| `driveCores: 4` | each container needs `ceil(20/5) = 4` cores ✓ — honored |
| `driveCores: 3` | **infeasible** — 20 TiB needs 4 cores > 3 (fail fast) |

For **mixed** pools `driveContainers` is the combined total split by raw-capacity ratio; the split
must keep **both** pools ≥ minFdNum (e.g. 12 at ratio 1:2 ⇒ TLC 4 / QLC 8 ⇒ infeasible, TLC < 6).

**M — Diskless / separate compute node pool.**

*Input:* drives on 6 nodes reach rawTLC 180 ⇒ 6 × 30 TiB × 6 cores = `totalTlcDriveCores` 36.
`roleNodeSelector.compute` selects **8 diskless nodes** with `maxComputeCoresPerNode: 16`.

*Expected output:*

| Pool | Plan |
|---|---|
| Drives | n1…n6: **CREATE** TLC 30 TiB each (6 cores) |
| Compute | `count = max(6, ⌈36/16⌉) = 6`, `cores = max(1, ⌈36/6⌉) = 6` ⇒ **6 × 6 cores**, one-per-node on 6 of 8 |

**Compute create-then-grow sequence.**

*Input:* 8 diskless compute nodes × 16 core headroom, `maxComputeCoresPerNode = 16`, 3/2/1 ⇒
floor = 6. The same sizing runs every step (create and grow identical), `t = totalTlcDriveCores`:

*Expected output:*

| Step | Trigger | `t` | `count = max(6, ⌈t/16⌉)` | `cores = ⌈t/count⌉` | Action |
|---|---|---|---|---|---|
| 1 | create | 36 | 6 | 6 | **CREATE** 6 × 6 cores (on 6 of 8 nodes) |
| 2 | cap ↑ | 72 | 6 | 12 | **GROW** all 6 in place 6→12 (deferred) |
| 3 | cap ↑ | 120 | 8 | 15 | **GROW** the 6 existing 12→15, **then CREATE** 2 × 15 on the 2 free nodes |

Step 3: each existing node has `16−12 = 4` free, delta to 15 is `3 ≤ 4` ⇒ all 6 grow to 15
(`existingCores = 90`). `shortfall = 8×15 − 90 = 30` ⇒ 2 new × 15. Total `8×15 = 120` ✓. If a fill
node is hugepage-constrained its container is created smaller (e.g. 8 cores); a later grow **levels
it up first** (8→16) *if* its hugepages freed up, else **freezes** it at 8 and covers the deficit with
a fresh balanced container on a free node.

### Shrink

**F — Shrink.**

*Input:* lower `clusterCapacity` 120 → 60 TiB.

*Expected output:* **no-op**; `ClusterCapacityShrink` event ("delete WekaContainers manually to
shrink") — [§Rules](#rules-that-apply-everywhere).

### Validation (rejected at admission)

**N — QLC-only rejected.**

| Input (rejected) | Rejected by | Message |
|---|---|---|
| `driveTypesRatio: {qlc: 1}` (no TLC) | CEL | *"driveTypesRatio.tlc must be greater than 0 …; QLC-only is not allowed."* |

TLC-only `{tlc:1}` and mixed `{tlc:m, qlc:n}` are supported; only QLC-only is refused.

**O — Mutual exclusion + protection floor.**

| Input (rejected) | Rejected by | Message |
|---|---|---|
| `clusterCapacity` + any of `containerCapacity` / `numDrives` / `driveCapacity` | CEL | *"clusterCapacity is mutually exclusive with containerCapacity, numDrives and driveCapacity."* |
| `clusterCapacity` with `stripeWidth < 3` (or `redundancyLevel < 2`) | `clusterCapacityProtection` | *"clusterCapacity requires stripeWidth >= 3"* (etc.). Hot spare is optional (`hotSpare >= 0`), so it never triggers this rejection. |

The protection floor is required because `minFdNum = stripeWidth + redundancyLevel + hotSpare`.

**P — Per-type chunk infeasibility.**

| Input (rejected) | Rejected by | Message |
|---|---|---|
| `clusterCapacity 20TiB`, ratio `{tlc:50, qlc:1}` ⇒ QLC ≈ 803 GiB raw, below 384 GiB/FD × 6 | `clusterCapacityChunkFeasibility` | *"clusterCapacity QLC share … is below the minimum drive chunk of 384 GiB. … (rule: clusterCapacity × part/(tlc+qlc) >= 384 × stripeWidth)."* |

A **per-type** failure — the TLC pool alone would be fine. Skipped once the cluster already has
drive containers (the planner then only grows).

## Explicit drive/compute sizing

By default the planner derives drive sizing from capacity and compute sizing from the TLC drive
cores. You may instead pin any of the four **planner sizing fields** in `dynamicTemplate`:
`driveContainers`, `driveCores`, `computeContainers`, `computeCores`. **These four are honored
exactly**, and any value that violates a constraint makes the plan **fail fast**
(`ClusterCapacityInfeasible`, nothing created/grown) rather than being clamped:

- **`driveContainers`** — exact total drive-container count (in AUTO mode, container == FD). For
  mixed pools it is the combined total split by raw-capacity ratio. Fails fast below `minFdNum`,
  above available FDs, with a per-container share below MinChunk, or (mixed) when the split drops a
  pool below `minFdNum`. (Example [L](#explicit-sizing-and-separate-compute-pool).)
- **`driveCores`** — fixed per-container core count. Fails fast when a container's capacity needs
  more cores, or a node cannot host the pinned cores. Pinning *more* than needed is allowed if the
  node has room.
- **`computeContainers` / `computeCores`** — per the [compute truth table](#compute-sizing); an
  explicit value exceeding per-node headroom, the compute-node count, or breaking 1:1 fails fast.

**Hugepages are not planner pins.** The per-role hugepages fields — `driveHugepages` /
`driveHugepagesOffset`, `computeHugepages` / `computeHugepagesOffset` — are applied later by the
container allocator, not consulted by the capacity planner. The planner sizes hugepages from cores;
if you set these they apply **as-is** to every container of that role regardless of cores, and they
never cause `ClusterCapacityInfeasible`. Prefer leaving them auto. The same goes for the per-role
`*ExtraCores` fields (`driveExtraCores`, `computeExtraCores`, …): container-level overrides outside
clusterCapacity planning.

## Constraints

**Hard errors (fail fast)** — the planner returns infeasible, the operator emits
`ClusterCapacityInfeasible` and **waits** without creating or growing anything:

| Condition |
|-----------|
| fewer than `minFdNum` reachable failure domains for a needed pool (TLC or QLC) |
| per-FD share would fall below 384 GiB (MinChunk) |
| total node headroom across candidate FDs `< delta` (capacity/cores/hugepages/memory) |
| no spare node has ≥ `T` (uniform per-FD chunk) free AND in-place growth is disabled (or the required grow is below `minGrowthFraction`, or the overshoot would exceed `maxOverProvisionFraction`) |
| compute:drive 1:1 cannot fit one-per-node on the compute nodes' headroom (or `computeContainers` exceeds the compute-node count) |
| explicit `computeCores` exceeds per-node compute headroom, or `computeContainers × computeCores < totalTlcDriveCores` |
| explicit `driveContainers < minFdNum`, `> available FDs`, or per-container share `< 384 GiB`; for mixed pools, the raw-ratio split drops a pool below `minFdNum` |
| explicit `driveCores` smaller than a container's capacity needs, or a node cannot host the pinned `driveCores` |
| `stripeWidth < 3` or `redundancyLevel < 2`; `clusterCapacity <= 0` (hot spare is optional, `hotSpare >= 0`) |

The event names the binding constraint (e.g. "QLC: only 4 of 6 required FDs have capacity",
"node X: hugepages short by N MiB").

**Admission errors** — rejected before any reconcile:
- CEL: `clusterCapacity` XOR `containerCapacity`/`numDrives`/`driveCapacity`; `driveTypesRatio.tlc > 0`.
- `clusterCapacityProtection`: `stripeWidth >= 3`, `redundancyLevel >= 2`, `hotSpare >= 0` (hot spare optional).
- `clusterCapacityChunkFeasibility` (greenfield only): each active pool's per-FD share must clear
  384 GiB — `clusterCapacity × part/(tlc+qlc) >= 384 × stripeWidth` for **both** pools. Skipped once
  the cluster already has drive containers.

## Events

Every planning decision is surfaced as a Kubernetes event on the WekaCluster (throttled to one per
minute per reason). The conditions are defined in [§Rules](#rules-that-apply-everywhere) and the
[constraints](#constraints) above; this table is just the reason→type→trigger index.

An **infeasible** plan is the sole signal: when the plan is infeasible only
`ClusterCapacityInfeasible` is emitted, and the shrink / heterogeneous-growth / over-provision
advisories are suppressed for that reconcile (they describe placement that does not happen).

| Reason | Type | When it fires |
|--------|------|---------------|
| `ClusterCapacityPlanned` | Normal | A feasible plan that actually places capacity (≥1 create or grow) — a positive signal, e.g. after recovering from infeasible by adding a node. Steady-state reconciles stay silent. Example: *"clusterCapacity plan applied: creating 3 drive container(s) [2 mixed, 1 TLC] across 3 node(s) / 3 failure domain(s) @ ~10.7TiB/FD, placing T/Q 24.0/8.0 TiB; growing 1 existing container(s) (+T 2.0TiB, cores 6→8); compute 3 container(s), 24 cores on 3 node(s); minFdNum 11; target raw T/Q 24.0/12.0 TiB (placed 34.0TiB), protection 8+2+1"*. |
| `ClusterCapacityDeferred` | Normal | A drive container is alive but never scheduled — planning deferred and retried (see [§Rules](#rules-that-apply-everywhere)). Routine pod deletion does **not** cause this. |
| `ClusterCapacityShrink` | Normal | A pool's current capacity exceeds desired by **more than `maxOverProvisionFraction` × desired**. **Never auto-applied** — delete WekaContainers manually to shrink. (An in-cap over-provision from create-new rounding stays silent — see `ClusterCapacityOverProvisioned`.) |
| `ClusterCapacityHeterogeneousGrowth` | Warning | The heterogeneous-fallback notice: a fresh balanced (uniform) set was created on spare nodes because a fresh per-FD chunk would dwarf the existing FDs; the old smaller drive containers can be deleted manually once data has migrated. |
| `ClusterCapacityOverProvisioned` | Normal | A pool was realized with uniformly-sized failure domains, and ceiling that uniform size lands up to one chunk above the desired raw (at most `maxOverProvisionFraction` × desired) — an intentional rounding, not reclaimable excess. The message names the pool, states the placement (growing existing FDs, adding new ones, or both), and reports the overshoot: `"<pool>: +N GiB covered by growing K existing failure domain(s), each sized to a uniform T GiB; this over-provisions the target by M GiB (within maxOverProvisionFraction=0.20) — intentional rounding to keep failure domains uniformly sized, not reclaimable excess (no manual shrink needed)"`. |
| `ClusterCapacityInfeasible` | Warning | The plan is infeasible — no spare node has ≥ T free for a new FD, and either in-place growth is disabled, or the required grow is below `minGrowthFraction`, or the overshoot would exceed `maxOverProvisionFraction`. The operator waits (1-minute requeue) and creates/grows nothing. The message states the binding reason (e.g. "no spare node has N GiB free … Growing … would raise each by only X% (below minGrowthFraction=0.20)"). When a pool has **fewer than `minFdNum` candidate failure domains**, the message also appends a **breakdown** naming why each rejected node doesn't qualify — `no <pool> drive capacity`, `already hosts a <pool> container`, or `<drive capacity / cores / hugepages / memory> limits usable <pool> to N GiB (below the 384 GiB minimum chunk)` — with **nodes sharing a reason grouped into one clause** (e.g. `n4, n5, n6: no QLC drive capacity`; capped with `(+N more)` tails). |
| `CapacityGrowthApplied` | Warning **or** Normal | Growth committed to an **existing** container. **Warning** when a core/hugepage change needs a manual pod termination; **Normal** when applied live (drive *capacity*-only increase). Emitted only while `enableDynamicDriveScalingForSharedDrives` is `true` — **never in the default config** (`false`), where no container is grown in place. |

(Explicit compute/drive sizing that can't be satisfied is **not** a warning — it is a Hard error
that fails fast via `ClusterCapacityInfeasible`.)

## Helm constraints

| Setting | Default | Env Var | Meaning |
|---------|---------|---------|---------|
| `maxComputeCoresPerNode` | `16` | `CLUSTER_CAPACITY_MAX_COMPUTE_CORES_PER_NODE` | Policy cap on compute cores per node (0 disables the *policy* cap; real per-node headroom still binds) |
| `tlcCapacityPerCoreGiB` | `5120` (5 TiB) | `CLUSTER_CAPACITY_TLC_CAPACITY_PER_CORE_GIB` | TLC raw capacity per drive core |
| `qlcCapacityPerCoreGiB` | `51200` (50 TiB) | `CLUSTER_CAPACITY_QLC_CAPACITY_PER_CORE_GIB` | QLC raw capacity per drive core |
| `imbalanceFactor` | `8.0` | `CLUSTER_CAPACITY_IMBALANCE_FACTOR` | A fresh per-FD chunk `≥ factor × existing per-FD average` triggers the heterogeneous (balanced-fresh) fallback. `0` (or below) disables the fallback. |
| `driveSharing.minGrowthFraction` | `0.2` | `MIN_GROWTH_FRACTION` | Minimum relative per-container grow required to trigger an in-place grow: `(newCap − cur) / cur >= minGrowthFraction`. A grow below this fraction is skipped; if that is the only option and no spare node is available → `ClusterCapacityInfeasible`. |
| `driveSharing.maxOverProvisionFraction` | `0.2` | `MAX_OVER_PROVISION_FRACTION` | Maximum fraction by which a pool may be over-provisioned (above its raw desired) when creating new FDs rounds up to a full chunk. If the overshoot from a ceil-rounded new-FD count exceeds `maxOverProvisionFraction × desiredRaw`, the create-new step is skipped and the planner falls back to grow or infeasible. |
| `clusterCapacity.capacityDeadbandFraction` | `0.05` | `CLUSTER_CAPACITY_DEADBAND_FRACTION` | Relative shortfall `(desiredRaw − current) / desiredRaw` below which pool growth is ignored (treated as a no-op), avoiding churn from tiny target changes. `0` disables the band (strict `current < desiredRaw`). |

`enableDynamicDriveScalingForSharedDrives` (default `false`) governs whether existing containers may
be extended in place. **In the default config (`false`) existing drive containers are frozen and all
growth is met by creating new containers on fresh FDs** (or reported infeasible if none can hold the
delta); opt into `true` to grow existing containers in place. See
[Growth in the default config](#growth-in-the-default-config-dynamic-scaling-disabled)
and the [Drive Sharing guide](../operations/drive-sharing.md#global-defaults). Per drive container,
`NumCores = ceil(perTypeCapacity / perCoreCapacity)`; the 384 GiB per-virtual-drive minimum still
applies at the allocator level.

## Observability

Per-drive-container TLC/QLC capacity is a `WekaContainer` printer column:

```bash
kubectl get wekacontainer -l weka.io/cluster=<name>
# the Capacity column shows each drive container's TLC/QLC GiB
```

Planning decisions (node inventory net of other/own clusters, desired-vs-current per pool, and each
grow/create with its target node and per-node fit reason) are emitted to the operator log at debug
level — grep the `planClusterCapacity` span to see why capacity did or didn't land on a node.

## Migrating from containerCapacity

An existing `containerCapacity` cluster can migrate to `clusterCapacity` **in place — no containers
are recreated**.

**Prerequisite — the cluster must already have protection.** `stripeWidth`, `redundancyLevel`, and
`hotSpare` must already be set on the WekaCluster (`spec`-level, matching the protection the cluster
was formed with) **before** you migrate. `clusterCapacity` derives `minFdNum` and the failure-domain
spread from them, and the `clusterCapacityProtection` webhook **rejects** the switch unless
`stripeWidth >= 3`, `redundancyLevel >= 2` (`hotSpare >= 0`, optional). These are cluster-formation parameters —
migration **adopts** the protection the cluster already runs with; it does not introduce or change it.

**Steps** (edit the same WekaCluster, keeping its name/namespace):
1. Remove `containerCapacity` (the two fields are mutually exclusive).
2. Add `clusterCapacity` and keep or adjust `driveTypesRatio`. Leave `stripeWidth`,
   `redundancyLevel`, `hotSpare` as already configured — do not change them during migration.

On migration you can **remove** the explicit sizing fields — `driveContainers`, `driveCores`,
`computeContainers`, and `computeCores` — unless you want to keep overriding the auto-calculated
values. With them removed, the operator derives container count, cores, hugepages, and memory
automatically from `clusterCapacity` (drive sizing from the target, compute sizing from the TLC
drive cores). Leave any of them set only to pin that dimension; see
[Explicit drive/compute sizing](#explicit-drivecompute-sizing).

On the first reconcile the operator computes `current` as the sum of the existing drive containers'
capacity and compares it to the new target. If `current` already covers the target, the plan is
**empty** — nothing is resized, recreated, or stamped; the existing containers are adopted as-is. If
the target is higher, the normal grow algorithm applies (grow in place, then create new).

```yaml
# Before — 6 mixed (TLC+QLC) drive containers; protection already set at spec level
# spec.stripeWidth: 3, spec.redundancyLevel: 2, spec.hotSpare: 1 (already set on the cluster)
dynamicTemplate:
  driveContainers: 6
  driveCores: 4
  computeContainers: 6
  computeCores: 3
  containerCapacity: 8000      # per container: TLC 1600 GiB, QLC 6400 GiB at 1:4
  driveTypesRatio: {tlc: 1, qlc: 4}

# After — in-place edit (same name); TLC grows in place, QLC grows where nodes have headroom
# spec.stripeWidth: 3, spec.redundancyLevel: 2, spec.hotSpare: 1 (unchanged — already set) → minFdNum = 6
dynamicTemplate:
  clusterCapacity: "90TiB"     # raised target
  driveTypesRatio: {tlc: 1, qlc: 20}
```

Verify with the `WekaContainer` Capacity column:

```bash
kubectl get wekacontainer -l weka.io/cluster=cluster-capacity-migration -n default
```

## Previewing a plan: the `weka-capacity` CLI

`clusterCapacity` changes are reconciled live, so before editing a real `WekaCluster` you can **preview**
what a target (or a change to `driveTypesRatio` / protection) would do. The operator image ships a
read-only dry-run CLI, `weka-capacity`, that reproduces the operator's exact inputs (the same node
inventory collector and pure planner) and prints the drive/compute containers it would **create** or
**grow**, on which nodes, and whether the plan is **feasible** — with actionable fix tips when it is not.

```bash
# Preview a migration (or a change) without touching the live cluster. Preferred: exec into the
# opt-in capacity-planner toolbox pod (enable via Helm --set deployCapacityPlanner=true):
kubectl -n weka-operator-system exec deploy/weka-operator-capacity-planner -- \
  /weka-capacity plan --cluster my-cluster -n default --cluster-capacity 11022TiB --drive-types-ratio 1:90

# Preview a brand-new cluster that doesn't exist yet (spec synthesized from flags):
weka-capacity plan --new-cluster --node-selector weka.io/supports-backends=true --cluster-capacity 11022TiB --drive-types-ratio 1:90 --stripe-width 16 --redundancy 2 --hot-spare 1

# See the raw per-node capacity/resource landscape:
weka-capacity explore-nodes
```

See **[`weka-capacity` CLI reference](../cli-tools/weka-capacity.md)** for the full command,
flag, output, constraint-sourcing, and toolbox-pod deployment documentation.

## Related documentation

- [Drive Sharing](../operations/drive-sharing.md) — proxy-mode signing, `ssdproxy` architecture,
  the other capacity modes, and virtual drive allocation strategies
- [Drive Sharing Allocation Logic Summary](../operations/drive-sharing-summary.md) — condensed
  flowcharts and decision trees
- [Cluster Provisioning](cluster-provisioning.md) — general cluster configuration
- [WekaCluster API Reference](../../api_dump/wekacluster.md) — complete field reference
