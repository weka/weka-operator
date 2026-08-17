# `weka-capacity` — capacity-planner dry-run CLI

`weka-capacity` is a read-only command-line tool, shipped inside the operator image, that **previews
the operator's capacity-planning decisions without changing anything**. It reproduces the exact inputs
the operator uses — the same node-inventory collector and the same pure planner code — so a dry-run
matches what the running operator would actually do.

Use it to answer, safely and offline:

- What drive/compute containers would the operator **create** or **grow** if I set (or change)
  `clusterCapacity`, `driveTypesRatio`, or the protection scheme?
- On which nodes, with what cores/hugepages, across how many failure domains?
- Is the target **feasible** given real per-node free resources — net of every existing WekaContainer,
  including *other* clusters sharing the same drives — and if not, **why**, and **how do I fix it**?
- What does each backend node actually have free right now, and who is consuming it?

It is the safe alternative to editing a live `WekaCluster` and watching the operator react.

> This is the authoritative reference for the tool. For the capacity model itself (the planner
> algorithm, worked scenarios, events, Helm settings), see
> [Cluster Capacity](../deployment/cluster-capacity.md).

## Installation / how to run

The `weka-capacity` binary runs in the **capacity-planner toolbox pod** — a dedicated,
opt-in workload — and can also be built and run locally against a kubeconfig.

**In-cluster: the capacity-planner toolbox pod.** Enable it via Helm with
`--set deployCapacityPlanner=true`, which installs a `<prefix>-capacity-planner`
Deployment (default prefix `weka-operator`, so `weka-operator-capacity-planner`) with its
own read-only ServiceAccount (`get`/`list`/`watch` on nodes, WekaClusters/WekaContainers,
and Deployments — no write access). Exec into it to run `weka-capacity`:

```bash
kubectl -n weka-operator-system exec deploy/weka-operator-capacity-planner -- \
  /weka-capacity explore-nodes
```

**Locally against a kubeconfig** (`$KUBECONFIG` or `--kubeconfig`):

```bash
make build-weka-capacity            # builds ./bin/weka-capacity
./bin/weka-capacity explore-nodes

# Or run without building:
make run-weka-capacity ARGS="explore-nodes"
```

When no `--kubeconfig` is given and `$KUBECONFIG` is unset, the tool uses the in-cluster config
(so it works unchanged inside the toolbox pod).

> **`plan` and namespaces.** There are **two** independent namespaces. `-n`/`--namespace` is the
> **cluster** namespace — where the target `WekaCluster` lives. `--operator-namespace` is the
> **scrape** namespace — where the manager Deployment is read for the base constraints; it defaults to
> `weka-operator-system` (the operator's home). So a cluster in any namespace works out of the box:
> `plan --cluster <name> -n default` looks the cluster up in `default` and still scrapes the operator
> from `weka-operator-system`. Override the scrape namespace with `--operator-namespace <ns>`, or skip
> the scrape entirely with `--from-operator=false` (built-in defaults only).

## Global options

| Flag | Default | Meaning |
|---|---|---|
| `--kubeconfig` | `$KUBECONFIG` | Path to a kubeconfig; empty ⇒ in-cluster config. |
| `-n`, `--namespace` | `weka-operator-system` | **Cluster namespace**: where `plan` looks up the `WekaCluster`, and the node/selector namespace context. |
| `--operator-namespace` | `weka-operator-system` | **Scrape namespace**: where the operator manager Deployment is read for the base constraints. Independent of `-n`. |
| `--output` | `table` | `table` or `json`. |
| `--out` | *(stdout)* | Write output to this file instead of stdout. |

## Where the constraints come from (and how to override them)

The planner needs the operator's sizing constraints (per-core capacity caps, hugepage ratios, growth
fractions, etc.). The CLI resolves them in **three layers**, and never re-hardcodes a value the operator
reads from its environment:

1. **Base** — the built-in defaults, sourced from the operator's own configuration code
   (`config.LoadCapacityEnv`), so they always match the operator's defaults.
2. **Deployed-operator overlay** — the CLI scrapes the manager Deployment's container env in
   `--operator-namespace` (default `weka-operator-system`) and overlays the operator's *actual* values on
   top of the base. Point it elsewhere with `--operator-namespace <ns>`, or disable the scrape entirely
   with `--from-operator=false` (equivalently `--from-operator false`), which uses the built-in defaults
   only.
3. **Flag overrides** — any constraint flag you pass wins over both layers.

Constraint override flags (each optional; unset ⇒ keep the scraped/base value):

`--tlc-per-core-gib`, `--qlc-per-core-gib`, `--imbalance-factor`, `--deadband-fraction`,
`--max-compute-cores-per-node`, `--min-growth-fraction`, `--max-overprovision-fraction`,
`--enable-dynamic-drive-scaling`, `--allow-single-parity`, `--hugepages-tlc-ratio`,
`--hugepages-qlc-ratio`.

`MinChunkSizeGiB` is a compile-time constant — surfaced in the output but not overridable.

---

## `explore-nodes`

Shows the per-node capacity/resource landscape, independent of any cluster. Every free figure is **net**
of the footprint of every WekaContainer on the node (all clusters, all modes), so `used + free` always
reconciles to the node's allocatable.

It deliberately does **not** subtract non-Weka pods, because it is narrating what Weka holds rather than
making a placement decision. `plan` does charge them. So on a node running foreign workloads,
`explore-nodes` will report more free headroom than `plan` believes it has — use `plan` for feasibility
questions and `explore-nodes` to see where Weka's own capacity went.

```
weka-capacity explore-nodes [--selector k=v[,k=v...]] [--fd-label <label>]
                            [--detail <node>] [--output table|json] [--out f.json]
```

| Flag | Default | Meaning |
|---|---|---|
| `--selector` | `weka.io/supports-backends=true` | Node label selector (comma-separated `k=v`). |
| `--fd-label` | *(AUTO)* | Failure-domain label key (label-based FD mode); default AUTO ⇒ one FD per host. |
| `--detail` | | Show the WekaContainers consuming this single node. |

**Table columns:** `NODE`, `FD`, `MODE` (the drive-capacity model the node is signed under — `shared`,
`full`, or `-` for unsigned), `DRIVES(free/phys)`, `FREE SIZES` (the free full drives, grouped by
capacity), `TLC(free/phys)`, `QLC(free/phys)`, `CPU(free/alloc)`, `HP2Mi(free/alloc)`,
`MEM MiB(free/alloc)`, `WC` (# containers on the node), `BLOCKED` (# drives excluded by the
`weka.io/blocked-drives` annotation), `DEL` (hosts a deleting drive container?), `INELIGIBLE`, plus a
`TOTAL` row.

`INELIGIBLE` is why the node cannot receive a **new** container — `cordoned`, `not ready`,
`untolerated taint`, or `-` when it can. Nodes are listed either way: hiding one would defeat the point
of asking why it is not being used. Two things it does not mean:

- **It never bars an existing container.** A container already on the node keeps running, keeps its
  drives and resources charged, and can still grow in place.
- **Taints are judged cluster-agnostically.** `explore-nodes` has no cluster to read
  `spec.tolerations`/`spec.rawTolerations` from, so it compares against the tolerations every Weka pod
  carries. A node whose taint a particular cluster *does* tolerate still shows `untolerated taint` here
  while that cluster places on it happily. `plan --cluster <name>` uses the real tolerations.

A node showing `MODE -` with `0B` capacity while its `weka.io/weka-full-drives` annotation is populated
means an annotation failed to parse — `weka.io/blocked-drives` is read first, so a malformed value there
zeroes every drive field for the node. Re-sign the node rather than debugging the planner.

The **CPU** column is **physical CPUs**, not weka data cores. Each container's charge is the CPU its pod
actually requests: under `cpuPolicy: auto` (the default) a container reserves `numCores*2 + 1` on a
hyper-threaded node (`dedicated_ht`) or `numCores + 1` on a non-HT node (`dedicated`). Node HT / full-pcpus
topology is read from the `weka.io/discovery.json` annotation. So a 2-data-core drive container shows `5`
CPUs on an HT node, matching `kubectl`'s pod request — the same figure the `plan` feasibility gate uses.

`--detail <node>` lists each consuming WekaContainer with its cluster, role, TLC/QLC, physical CPU
(`CORES`) and hugepages charge, and flags any drive container with a **nil `driveTypesRatio`**
(attributed 100% to TLC) — a common source of skew between the reported and realized capacity split. It
also breaks the node's drives into free and claimed with their individual capacities, and prints an
`INELIGIBLE: <reason>` line when the node cannot take a new container.

## `plan`

Dry-runs the planner for either a **live** `WekaCluster` (`--cluster`) or a **hypothetical,
not-yet-created** one (`--new-cluster`), applying any flag overrides on top of its `dynamicTemplate`,
and prints what the operator would do.

```
weka-capacity plan (--cluster <name> | --new-cluster) [-n <cluster-ns>]
    [--cluster-capacity 11022TiB] [--drive-types-ratio 1:90]
    [--stripe-width 16] [--redundancy 2] [--hot-spare 1]
    [--drive-containers N] [--compute-containers N] [--compute-cores N] [--drive-cores N]
    [--node-selector k=v[,k=v...]] [--fd-label <label>]
    [<constraint override flags>] [--output table|json] [--out plan.json]
```

> `--cluster` is **no longer required**. Exactly **one** of `--cluster` / `--new-cluster` must be
> given — neither ⇒ error, both ⇒ error.

Spec-override flags (all optional unless noted; with `--cluster` they **override** the cluster's live
value, with `--new-cluster` they **define** the synthetic spec from scratch — see below):

| Flag | Overrides / defines |
|---|---|
| `--cluster-capacity` | `dynamicTemplate.clusterCapacity` (e.g. `11022TiB`) — **required** with `--new-cluster`, unless `--auto-full-drives` is given |
| `--drive-types-ratio` | `driveTypesRatio`, as `tlc:qlc` (e.g. `1:90`) |
| `--stripe-width`, `--redundancy`, `--hot-spare` | `stripeWidth` / `redundancyLevel` / `hotSpare` |
| `--drive-containers`, `--drive-cores` | explicit drive sizing. Outside a capacity mode `--drive-containers` must be set together with `--compute-containers`, mirroring the CRD's both-or-neither rule |
| `--compute-containers`, `--compute-cores` | explicit compute sizing |
| `--num-drives` | `dynamicTemplate.numDrives`. In the daemonset mode this is a **per-node** override: every eligible node takes exactly this many of its **largest** signed full drives instead of all of them |

`--new-cluster`-specific flags:

| Flag | Default | Meaning |
|---|---|---|
| `--new-cluster` | *(off)* | Boolean flag (takes no value). Plan for a hypothetical, not-yet-created cluster synthesized from flags; shown as `new-cluster` in the output. Mutually exclusive with `--cluster`. |
| `--auto-full-drives` | *(off)* | Boolean flag. Build a hypothetical **daemonset** cluster (one pinned drive container per eligible node, taking all its full drives) instead of a `clusterCapacity` one. There is no spec field for this mode — it is what an empty `dynamicTemplate` means — so a live `--cluster`'s mode is always derived from its own spec and this flag does not apply there. |
| `--node-selector` | *(all nodes)* | Node label selector (`k=v[,k=v...]`) for `--new-cluster`; which nodes the hypothetical cluster could land on. Optional — empty ⇒ all nodes. |
| `--fd-label` | *(AUTO)* | Failure-domain label key for `--new-cluster` (label-based FD mode); default AUTO ⇒ one FD per host. |

There is **no `--role-node-selector`**, so a cluster that splits the drive and compute roles across
different labels cannot be dry-run; plan it with a single selector or validate it by applying.

`plan --cluster <name>` only works on a **planner-managed** cluster — one sized by `clusterCapacity` or
acting as a daemonset. A cluster sized by explicit `computeContainers` + `driveContainers` has nothing
for the planner to decide, and `plan` says so and points you at `explore-nodes`.

**Output sections:**

- **TARGET** — the requested end state: `usable capacity`, `drive ratio`, `protection`
  (`stripe+redundancy+hotSpare → minFdNum N`), and `min chunk`.
- **RAW CAPACITY** — a `TLC / QLC / total` table with three rows: `current` (this cluster's existing
  drive-container capacity), `target` (the derived raw TLC/QLC target), and `delta` (target − current
  per column, signed) — i.e. what must change.
- **FEASIBILITY** — `OK` or `INFEASIBLE`.
- **INFEASIBLE** (only when infeasible) — the reason, the binding cause (`pool` / `binding` /
  `shortfall`), a `RejectedNodes` table (`NODE  BINDING  FREE  NEEDED`), and a numbered **FIXES** list.
  The fixes come from the planner itself, so they are identical to the operator's
  `ClusterCapacityInfeasible` event.
- **DRIVE** — drive containers to place, split into `create` and `grow` sub-groups (each printed only
  when non-empty). `create` rows are keyed by node (`NODE FD TYPE TLC QLC CORES`); `grow` rows by
  container and also show the container's `NODE` (`CONTAINER NODE TLC QLC CORES`), with each changed
  column shown as a `from→to` transition (a column that doesn't change shows the single value). Raising
  capacity also raises cores (pod hash), so a grow applies on the next pod (re)creation.
- **COMPUTE** — compute containers, same `create` / `grow` sub-groups. `create` rows are keyed by node
  (`NODE CORES HUGEPAGES`, absolute); `grow` rows by container plus its `NODE`
  (`CONTAINER NODE CORES HUGEPAGES`), with both the core and hugepages changes shown as `from→to`
  transitions. A **create** is a brand-new compute container (applied at creation); a **grow** changes an
  existing container's cores/hugepages, which live in the pod hash, so it applies on the next pod
  (re)creation. `HUGEPAGES` is in MiB; drive rows omit hugepages.
  When the plan is **infeasible**, the `create` / `grow` sub-headers are relabeled
  `create (PARTIAL — NOT applied; plan is infeasible)` — they show only the partial placement the planner
  reached before the binding pool, which the controller (and this dry-run) never applies.
- **WARNINGS / OVER-PROVISION / SHRINK** — the planner's advisories.
- **SUMMARY** — on a feasible plan: raw delta placed, new nodes used, idle inventory, target. On an
  infeasible plan it instead leads with `INFEASIBLE — no containers will be created or grown`, names the
  blocking pool, and flags any placement shown as diagnostic-only (not applied) — never the feasible
  `create raw +…` phrasing.

**Exit code:** non-zero when the plan is infeasible, so `plan` is usable as a CI / pre-flight gate.
(The output is still written first.)

### Output for the daemonset mode

A daemonset cluster is sized per node rather than from a capacity target, so `plan` prints a different
shape — headed `CLUSTER <name> (daemonset / auto full drives)`. `TARGET` and `RAW CAPACITY` are absent
(there is no target), and in their place:

- **DRIVE SIZING** — the fleet totals: `drives: <taken>/<available>`, `TLC: <taken>/<available>`, the
  resulting `drive cores`, the `compute cores required` by the ratio, and the compute shape
  (`N container(s), C cores/container, H MiB hugepages`). Then a one-line `rationale` spelling the
  derivation out in words, including any pin in force and, on an infeasible plan, the binding reason.

  The **denominator counts every signed drive the selector matches**, including drives on nodes that
  cannot take a container. So `42/48` means six drives exist that this plan will not claim — check the
  `NODES` table for why.
- **NODES** — one row per matched node: `NODE  FD  DRIVES(used/avail)  TLC  CORES  STATE  NOTE`. `STATE`
  is `create` (a new container), `grow` (an existing one expanding), or `not-planned`. A `not-planned`
  row is **not** in itself a sign of failure — a node that is cordoned, `NotReady` or carrying an
  untolerated taint is skipped on a perfectly feasible plan — so read the `NOTE`, which names the reason.
- **COMPUTE** — the compute containers, in the same `create` / `grow` sub-groups as the capacity mode.
- **WARNINGS** — the planner's advisories, one line each, in the same wording the operator emits as
  events on the `WekaCluster`.

An infeasible daemonset plan adds the usual **INFEASIBLE** section with its numbered **FIXES**, and the
`NODES`/`COMPUTE` placements shown are diagnostic only — nothing is created, not even the drive
containers that would have fitted.

See [Act As Daemonset](../deployment/act-as-daemonset.md) for what the mode does with these numbers.

### Infeasibility fix tips

When a plan can't be satisfied, the tool names the tightest **binding** dimension and prints ordered,
actionable fixes. The binding is one of: `drive capacity`, `failure domains`, `cores`, `hugepages`,
`memory`, `protection`, `driveContainers`, `driveCores`. Typical fixes:

- **capacity-bound** → shift `driveTypesRatio` toward the abundant type; add drives/nodes of the short
  type; enable `enableDynamicDriveScalingForSharedDrives` to grow existing containers in place.
- **failure-domains-bound** → add nodes/FDs that can host the pool's drive container (need
  `minFdNum = stripeWidth + redundancyLevel + hotSpare`).
- **protection-bound** → raise `stripeWidth`/`redundancyLevel` to the floor.
- **pinned `driveContainers`** → unset it (auto) or set it to what the plan resolves to.

### Planning a not-yet-existing cluster

`--new-cluster` (a boolean flag — it takes no value) plans capacity for a cluster that **doesn't exist
yet**; it is labelled `new-cluster` in the output. Instead of fetching a
live `WekaCluster`, the CLI **synthesizes** a `WekaCluster` spec entirely from the flags you pass, then
runs it through the exact same node-inventory collector and the exact same pure planner as the
`--cluster` path — so the dry-run stays just as faithful.

- **Inputs:** `--cluster-capacity` is **required** (there's no live `clusterCapacity` to fall back to).
  `--drive-types-ratio`, `--stripe-width`/`--redundancy`/`--hot-spare`, and the explicit
  drive/compute sizing flags populate the synthetic spec the same way they would override a live one.
- **`--node-selector`** (optional) picks which nodes the hypothetical cluster could land on
  (`spec.nodeSelector`); omit it and the empty selector considers **all** nodes.
- **`--fd-label`** (optional) sets label-based failure domains, equivalent to `explore-nodes`'
  `--fd-label`; unset ⇒ AUTO, one failure domain per host.
- **Everything is CREATE.** Since there's no live cluster, there are no owned containers, so `RAW
  CAPACITY` `current` is `0B` and the entire plan — drive and compute — shows up under the `create`
  sub-groups (a greenfield plan).
- **DPDK base memory** defaults to the operator's fresh-dynamic-cluster default (64 MiB/core), since the
  synthetic spec sets no overrides.

The rest of the output (TARGET, RAW CAPACITY, FEASIBILITY, DRIVE/COMPUTE tables, SUMMARY, exit code) is
identical in shape to the `--cluster` path.

## Fidelity: how it stays in sync with the operator

The CLI is intentionally the *same code* as the operator, not a reimplementation:

- It registers the **same API scheme** and pins the same vendored `weka-k8s-api`, so it reads clusters
  and containers identically.
- It uses the **same inventory collector** (`internal/capacityplanner/inventory`) the controller uses to
  build per-node headroom and the existing-container views.
- It runs the **same pure planner** (`internal/capacityplanner.PlanCapacity`).
- It sources constraints from the **operator's own env defaults** and the deployed operator's config.

So a `plan` dry-run reproduces the controller's create/grow/compute decisions and feasibility verdict
for the same inputs.

---

## Worked examples

- **Operator namespace:** `weka-operator-system`. Manager Deployment: `weka-operator-controller-manager`
  (container `manager`).
- **Backend fleet:** 14 nodes, all `weka.io/supports-backends=true`, all signed in **shared-drives**
  (drive-sharing) mode. 8 workers (`node07`–`node14`) are TLC-only; 6 control-plane nodes
  (`node01`–`node06`) carry both TLC and QLC. (Node names below are anonymized placeholders.)
- **Test selectors** (pre-labelled for capacity testing): `weka.io/test-cluster-capacity-tlc=true` = the
  6 TLC drive nodes used below; `weka.io/test-cluster-capacity-qlc=true` = the 6 QLC nodes;
  `weka.io/test-cluster-capacity=true` = all 14.
- **Example cluster:** `cap-test` — `clusterCapacity: 30TiB`, TLC-only (`driveTypesRatio {tlc:1}`),
  protection `3+2+1` (⇒ `minFdNum 6`), `nodeSelector weka.io/test-cluster-capacity-tlc=true`. It sits in
  `weka-operator-system`, so `-n weka-operator-system` (the cluster namespace) coincides with the default
  `--operator-namespace weka-operator-system` (the scrape namespace); a cluster in another namespace just
  needs its own `-n <ns>` and inherits the same scrape default.

### `explore-nodes` — the fleet

With no WekaContainers yet, free == phys everywhere:

```bash
kubectl -n weka-operator-system exec deploy/weka-operator-capacity-planner -- \
  /weka-capacity explore-nodes
```
```
NODE    FD      TLC(free/phys)     QLC(free/phys)     CPU(free/alloc)  HP2Mi(free/alloc)  MEM MiB(free/alloc)  WC  DEL
node01  node01  42.8TiB/42.8TiB    55.9TiB/55.9TiB    63/63            60000/60000        197600/197600        0
node03  node03  63.7TiB/63.7TiB    55.9TiB/55.9TiB    63/63            60000/60000        197600/197600        0
node07  node07  62.9TiB/62.9TiB    0B/0B              63/63            60000/60000        197600/197600        0
node12  node12  69.9TiB/69.9TiB    0B/0B              63/63            20698/20698        236902/236902        0
...
TOTAL           855.8TiB/855.8TiB  335.3TiB/335.3TiB                                                           14
```
(14 rows trimmed to 4; QLC is present only on the control-plane nodes (`node01`–`node06`). Note `node12`
has a much smaller hugepage budget — 20698 MiB vs 60000 — which becomes the binding constraint in the
plans below.)

Narrow to a subset with `--selector` (comma-separated `k=v`):

```bash
./bin/weka-capacity explore-nodes --selector weka.io/test-cluster-capacity-tlc=true -n weka-operator-system
```

### `explore-nodes` — with consumers (after `cap-test` is running)

Every free figure is **net** of the WekaContainers on the node. On the 6-worker TLC fleet, each node now
carries a drive + compute + ssdproxy container:

```
NODE    FD      TLC(free/phys)     QLC(free/phys)  CPU(free/alloc)  HP2Mi(free/alloc)  MEM MiB(free/alloc)  WC  DEL
node07  node07  51.8TiB/62.9TiB    0B/0B           48/63            43102/60000        163600/197600        3
node08  node08  65.7TiB/76.8TiB    0B/0B           48/63            43046/60000        163600/197600        3
node11  node11  44.8TiB/55.9TiB    0B/0B           48/63            43102/60000        163600/197600        3
node12  node12  58.7TiB/69.9TiB    0B/0B           48/63             3744/20698        202902/236902        3
...
TOTAL           338.5TiB/405.2TiB  0B/0B                                                                    6
```

`--detail <node>` lists the consumers (here the hugepage-tight `node12`):

```bash
./bin/weka-capacity explore-nodes --detail node12 -n weka-operator-system
```
```
Node node12 (FD node12, hyper-threaded)
  TLC free/phys: 58.7TiB/69.9TiB   QLC free/phys: 0B/0B
  CPU free/alloc: 48/63   HP2Mi free/alloc: 3744/20698 MiB   MEM free/alloc: 202902/236902 MiB
  CONTAINER                                              CLUSTER   ROLE      TLC      QLC  CORES  HP(MiB)  NILRATIO  DEL
  cap-test-compute-0a23c834-7eaa-486c-96ef-4f1fb27bd27f  cap-test  compute   0B       0B   7      9192
  cap-test-drive-afc2ae5f-b7ad-481d-9345-b38cdc8af971    cap-test  drive     11.1TiB  0B   7      4800
  weka-drives-proxy-node12                               -         ssdproxy  0B       0B   1      2962
```

`CORES` is the **physical CPU** each pod reserves: on this HT node a 3-data-core `dedicated_ht` container
requests `3*2+1 = 7`, and the ssdproxy `0+1 = 1`.

**Reconciliation (verified).** The free figures equal `kubectl` node allocatable minus the summed
container charges. For an HT `node07` (allocatable cpu 63, hugepages-2Mi 60000, phys TLC 64381 GiB), with a
drive (3 data cores → 7 physical CPU / 4800 MiB HP / 11378 GiB), compute (3 data cores → 7 CPU / 9192 MiB),
and ssdproxy (0 data cores → 1 CPU / 2906 MiB):

| Dim | allocatable − Σ charges | explore-nodes free |
|---|---|---|
| CPU | 63 − (7+7+1) = **48** | 48 |
| hugepages MiB | 60000 − (4800+9192+2906) = **43102** | 43102 |
| TLC GiB | 64381 − 11378 = 53003 = **51.8 TiB** | 51.8 TiB |

### JSON

```bash
./bin/weka-capacity explore-nodes -n weka-operator-system --output json
```
Top level is an array of node objects:
```json
[
  {
    "Node": "node07", "FDValue": "node07",
    "PhysTlcGiB": 64381, "PhysQlcGiB": 0,
    "UsedTlcGiB": 0, "UsedCores": 0, "UsedHugepagesMiB": 0,
    "AllocatableCores": 63, "AllocatableHugepagesMiB": 60000, "AllocatableMemoryMiB": 197600,
    "FreeTlcGiB": 64381, "FreeCores": 63, "FreeHugepagesMiB": 60000,
    "HasDeletingDriveContainer": false, "IsDriveCandidate": true, "Consumers": null
  }
]
```

## `plan` worked examples

Run against `cap-test` (60TiB, 1:1 TLC/QLC, 3+2+1) on the lab's 14-node backend fleet. All `plan`
invocations are read-only dry-runs — raising `--cluster-capacity` here previews a grow without touching
the live cluster.

### Feasible — at target (steady state)

```bash
kubectl -n weka-operator-system exec deploy/weka-operator-capacity-planner -- \
  /weka-capacity plan --cluster cap-test -n weka-operator-system
```
```
CLUSTER cap-test

TARGET
  usable capacity  60TiB
  drive ratio      1:1
  protection       3+2+1  (stripe+redundancy+hotSpare → minFdNum 6)
  min chunk        384.0GiB

RAW CAPACITY  TLC      QLC      total
  current     66.7TiB  66.7TiB  133.3TiB
  target      66.7TiB  66.7TiB  133.3TiB
  delta       -2.0GiB  -1.0GiB  -3.0GiB

FEASIBILITY  OK

SUMMARY
  create raw +0B across 0 new node(s); 14 other inventory node(s) not used by creates; target raw 133.3TiB (TLC 66.7TiB + QLC 66.7TiB)
```
`current` already covers `target` (the `delta` is a sub-GiB rounding artifact) ⇒ nothing to do. No
`DRIVE` or `COMPUTE` section prints when the plan is a no-op.

### Feasible — grow (raise the target)

Raising the target (here to 120TiB, from a cluster already at ~80TiB) grows existing containers in place,
adds fresh TLC failure domains, and grows compute to match the higher drive-core count. This shows every
sub-group — `DRIVE` create/grow and `COMPUTE` grow — with the `NODE` each container lives on:

```bash
./bin/weka-capacity plan --cluster cap-test -n weka-operator-system --cluster-capacity 120TiB
```
```
CLUSTER cap-test

TARGET
  usable capacity  120TiB
  drive ratio      1:1
  protection       3+2+1  (stripe+redundancy+hotSpare → minFdNum 6)
  min chunk        384.0GiB

RAW CAPACITY  TLC       QLC       total
  current     88.9TiB   88.9TiB   177.8TiB
  target      133.3TiB  133.3TiB  266.7TiB
  delta       +44.4TiB  +44.4TiB  +88.9TiB

FEASIBILITY  OK

DRIVE
  create
    NODE    FD      TYPE  TLC      QLC  CORES
    node14  node14  tlc   14.8TiB  0B   3
  grow
    CONTAINER                                            NODE    TLC         QLC              CORES
    cap-test-drive-53303458-02e5-4413-8f96-00330080ac1d  node01  0B          14.8TiB→22.2TiB  1
    cap-test-drive-90b0216c-2313-4adb-8a46-470c02162d12  node03  11.1TiB     14.8TiB→22.2TiB  4
    cap-test-drive-9ca20637-931a-4778-a5b8-fae091040402  node04  0B→14.8TiB  14.8TiB→22.2TiB  1→4
    cap-test-drive-9f9b3e08-b3a1-46fd-af62-0b3ef12b2bf2  node02  0B          14.8TiB→22.2TiB  1
    cap-test-drive-f2af27dc-f47d-4f28-9d4d-64ce28c9aa72  node05  0B          14.8TiB→22.2TiB  1
    cap-test-drive-f2d7f6b7-05b7-438e-99f6-b4a6e68bf5f1  node06  0B→14.8TiB  14.8TiB→22.2TiB  1→4

COMPUTE
  grow
    CONTAINER                                              NODE    CORES  HUGEPAGES
    cap-test-compute-25460a8f-323a-4de8-b1db-25a03109087e  node11  4→6    13080→19620
    cap-test-compute-3b582ffc-b6ac-4b7d-9491-4a4e4b7e0992  node09  4→6    13080→19620
    cap-test-compute-430d92b7-8752-40d3-a5d0-a900c7557d80  node07  4→6    13080→19620
    cap-test-compute-6f56c351-57d6-4c15-bf72-14f32523bd59  node10  4→6    13080→19620
    cap-test-compute-72992a2c-6f84-4aa4-a53b-d60dd47e0fa0  node14  1→6    7788→19620
    cap-test-compute-84e15864-3f9a-4697-8ff2-5507e49220ba  node08  4→6    13080→19620

OVER-PROVISION
  - QLC: +45507 GiB covered by growing 6 existing failure domain(s), each sized to a uniform 22756 GiB; this over-provisions the target by 3 GiB (within maxOverProvisionFraction=0.20) — intentional rounding to keep failure domains uniformly sized, not reclaimable excess (no manual shrink needed)
  - TLC: +45511 GiB covered by adding 3 new failure domain(s), each sized to a uniform 15171 GiB; this over-provisions the target by 2 GiB (within maxOverProvisionFraction=0.20) — intentional rounding to keep failure domains uniformly sized, not reclaimable excess (no manual shrink needed)
```
Existing drive containers grow in place. `grow` rows are keyed by container and carry the `NODE` the
container runs on; both `DRIVE grow` and `COMPUTE grow` cells show the per-container `from→to` transition
for any column that changes — capacity, cores, and hugepages (`0B→14.8TiB`, `14.8TiB→22.2TiB`, `1→4`,
`4→6`, `13080→19620`); a column that does not change shows the single value (e.g. TLC stays `0B` on a
QLC-only container). Drive/compute grows change cores (pod hash), so they apply on the next pod
(re)creation. The two `OVER-PROVISION` lines show the two placement shapes: QLC covered by **growing**
existing FDs, TLC by **adding** new ones — each rounding up to a uniform per-FD size.
(`enableDynamicDriveScalingForSharedDrives=true` is set on this lab's operator, so a grow is satisfied in
place rather than only by new FDs.)

JSON for the same plan (`--output json`, trimmed):
```json
{
  "cluster": "cap-test",
  "current": { "tlcGiB": 91022, "qlcGiB": 91026 },
  "desired": { "clusterCapacity": "120TiB", "driveTypesRatio": "1:1", "tlcRawGiB": 136533, "qlcRawGiB": 136533,
               "minChunkGiB": 384, "protection": { "stripeWidth": 3, "redundancyLevel": 2, "hotSpare": 1 } },
  "feasible": true,
  "growDrive": [ { "Name": "cap-test-drive-53303458-...", "NewTlcGiB": 0, "NewQlcGiB": 22756, "NewCores": 1 }, ... ],
  "growCompute": [ { "name": "cap-test-compute-25460a8f-...", "node": "node11", "fromCores": 4, "toCores": 6, "fromHugepagesMiB": 13080, "hugepagesMiB": 19620, "deferred": true }, ... ],
  "overProvisions": [ "QLC: +45507 GiB covered by growing 6 existing failure domain(s), each sized to a uniform 22756 GiB; this over-provisions the target by 3 GiB ...", "TLC: +45511 GiB covered by adding 3 new failure domain(s), each sized to a uniform 15171 GiB; this over-provisions the target by 2 GiB ..." ],
  "summary": "create raw +14.8TiB across 1 new node(s); 13 other inventory node(s) not used by creates; target raw 266.7TiB (TLC 133.3TiB + QLC 133.3TiB)"
}
```

### Infeasible — capacity-bound (fix tips + non-zero exit)

```bash
./bin/weka-capacity plan --cluster cap-test -n weka-operator-system --cluster-capacity 1000TiB ; echo "exit=$?"
```
```
CLUSTER cap-test

TARGET
  usable capacity  1000TiB
  drive ratio      1:1
  protection       3+2+1  (stripe+redundancy+hotSpare → minFdNum 6)
  min chunk        384.0GiB

RAW CAPACITY  TLC         QLC         total
  current     66.7TiB     66.7TiB     133.3TiB
  target      1111.1TiB   1111.1TiB   2222.2TiB
  delta       +1044.4TiB  +1044.4TiB  +2088.9TiB

FEASIBILITY  INFEASIBLE

INFEASIBLE
  reason: TLC: cannot satisfy clusterCapacity (+1069509 GiB) at the uniform per-failure-domain size of 11378 GiB. Even after growing the 6 existing failure domain(s) to their nodes' limits and adding failure domains on all 8 candidate node(s) (nodes not already running a TLC drive container, with enough free capacity/cores/hugepages/memory), the target is still out of reach. Add more nodes (or nodes with more free resources), or lower clusterCapacity.
  pool: tlc   binding: drive capacity   shortfall: 1044.4TiB
  FIXES:
    1. add more nodes (or nodes with more free capacity/cores/hugepages/memory) that can host a TLC drive container
    2. or lower clusterCapacity

SUMMARY
  INFEASIBLE — no containers will be created or grown. Blocking pool: tlc. No placement could be made. target raw 2222.2TiB (TLC 1111.1TiB + QLC 1111.1TiB).
exit=2
```
This is the same feasibility logic the controller applies — the `reason` and `FIXES` are the planner's
own, so they match the operator's `ClusterCapacityInfeasible` event byte-for-byte. The `SUMMARY` leads
with `INFEASIBLE` because, exactly like the controller, an infeasible plan creates and grows nothing.

### Infeasible — a pool with no failure domains (rejected-nodes table)

Forcing a QLC pool (`--drive-types-ratio 1:1`) onto the TLC-only worker subset
(`weka.io/test-cluster-capacity-tlc=true` — those nodes carry no QLC drives) — the QLC pool can't reach
`minFdNum`, so every candidate node is rejected:

```bash
./bin/weka-capacity plan --new-cluster --node-selector weka.io/test-cluster-capacity-tlc=true \
  --cluster-capacity 20TiB --drive-types-ratio 1:1 --stripe-width 3 --redundancy 2 --hot-spare 1
```
```
FEASIBILITY  INFEASIBLE

INFEASIBLE
  reason: QLC: only 0 of 6 required failure domains have capacity (need at least stripeWidth+redundancyLevel+hotSpare) — 6 node(s) cannot host a QLC failure domain: node07, node08, node09, node10, node11, node12: no QLC drive capacity
  pool: qlc   binding: failure domains   shortfall: 0B
  NODE    BINDING         FREE  NEEDED
  node07  drive capacity  0B    384.0GiB
  node08  drive capacity  0B    384.0GiB
  ... (6 rows)
  FIXES:
    1. add nodes / failure domains that can host a QLC drive container (need minFdNum = stripeWidth+redundancyLevel+hotSpare = 6)

DRIVE
  create (PARTIAL — NOT applied; plan is infeasible)
    NODE    FD      TYPE  TLC     QLC  CORES
    node07  node07  tlc   3.7TiB  0B   1
    ... (6 rows)

SUMMARY
  INFEASIBLE — no containers will be created or grown. Blocking pool: qlc. Partial placement shown below (TLC +22.2TiB across 6 node(s)) is diagnostic only and will NOT be applied. target raw 44.4TiB (TLC 22.2TiB + QLC 22.2TiB).
```
The TLC pool got as far as laying out 6 failure domains, but the QLC pool has no eligible failure
domain, so the plan as a whole is infeasible (exit `2`). Those TLC rows are the *partial* placement the
planner reached before hitting the binding pool — the header and `SUMMARY` both mark them **NOT
applied**, mirroring the controller, which discards the whole plan when infeasible. They are kept only as
diagnostics: they show that TLC is fine and QLC is the blocker.

### Constraint override — `--allow-single-parity`

By default `clusterCapacity` requires `stripeWidth>=3, redundancyLevel>=2`. Overriding protection below
the floor is rejected — unless single-parity is allowed:

```bash
# Without the flag → protection-bound infeasible
./bin/weka-capacity plan --cluster cap-test -n weka-operator-system --stripe-width 2 --redundancy 1
#   reason: clusterCapacity requires stripeWidth>=3, redundancyLevel>=2, hotSpare>=0 (got sw=2 rl=1 hs=1)
#   pool: -   binding: protection

# With --allow-single-parity → floor relaxed to 2+1, plan proceeds (FEASIBILITY OK)
./bin/weka-capacity plan --cluster cap-test -n weka-operator-system --stripe-width 2 --redundancy 1 --allow-single-parity
```

### Greenfield — a not-yet-existing cluster

`--new-cluster` plans capacity for a cluster that doesn't exist yet, synthesizing the spec from flags
instead of fetching a live `WekaCluster`. With no `--node-selector`, every backend node is a candidate;
`RAW CAPACITY` `current` is `0B` (no owned containers), so the whole plan lands under the `create`
sub-groups (greenfield):

```bash
./bin/weka-capacity plan --new-cluster --cluster-capacity 20TiB \
  --stripe-width 3 --redundancy 2 --hot-spare 1
```
```
CLUSTER new-cluster

TARGET
  usable capacity  20TiB
  drive ratio      1:0 (TLC-only default)
  protection       3+2+1  (stripe+redundancy+hotSpare → minFdNum 6)
  min chunk        384.0GiB

RAW CAPACITY  TLC       QLC  total
  current     0B        0B   0B
  target      44.4TiB   0B   44.4TiB
  delta       +44.4TiB  +0B  +44.4TiB

FEASIBILITY  OK

DRIVE
  create
    NODE    FD      TYPE  TLC     QLC  CORES
    node03  node03  tlc   7.4TiB  0B   2
    node04  node04  tlc   7.4TiB  0B   2
    node06  node06  tlc   7.4TiB  0B   2
    node08  node08  tlc   7.4TiB  0B   2
    node09  node09  tlc   7.4TiB  0B   2
    node13  node13  tlc   7.4TiB  0B   2

COMPUTE
  create
    NODE    CORES  HUGEPAGES
    node01  2      6128
    node02  2      6128
    node03  2      6128
    node05  2      6128
    node13  2      6128
    node14  2      6128

SUMMARY
  create raw +44.4TiB across 6 new node(s); 8 other inventory node(s) not used by creates; target raw 44.4TiB (TLC 44.4TiB + QLC 0B)
```

Everything is under `create` — 6 new drive containers and 6 new compute containers placed across the
fleet, `FEASIBILITY OK`, the whole raw target created on new nodes.

**Restricting to a busy subset stays faithful.** The collector charges *every* existing WekaContainer
against node headroom — including *other* clusters' — so planning onto the 6-node TLC subset that
`cap-test` already occupies (`--node-selector weka.io/test-cluster-capacity-tlc=true`) is charged against
those consumers. The drive pool still lays out its 6 `create` rows, but the compute pool can't fit 6
containers on the occupied nodes, so the plan is infeasible (exit `2`):

```bash
./bin/weka-capacity plan --new-cluster --node-selector weka.io/test-cluster-capacity-tlc=true \
  --cluster-capacity 20TiB --stripe-width 3 --redundancy 2 --hot-spare 1
```
```
FEASIBILITY  INFEASIBLE

INFEASIBLE
  reason: compute: cannot place 6 new compute container(s) to cover the 12-core shortfall — only 5 free fitting compute node(s) (each holds up to 2 cores + 6128 MiB hugepages)
  pool: compute   binding: cores   shortfall: 0B
  FIXES:
    1. add compute-eligible nodes (matching the cluster's compute role selector) with free cores + hugepages
    2. or lower computeContainers / computeCores, or reduce clusterCapacity so fewer TLC drive cores are needed

DRIVE
  create (PARTIAL — NOT applied; plan is infeasible)
    NODE    FD      TYPE  TLC     QLC  CORES
    node07  node07  tlc   7.4TiB  0B   2
    ... (6 rows)

SUMMARY
  INFEASIBLE — no containers will be created or grown. Blocking pool: compute. Partial placement shown below (TLC +44.4TiB across 6 node(s)) is diagnostic only and will NOT be applied. target raw 44.4TiB (TLC 44.4TiB + QLC 0B).
```

This is the same feasibility logic the controller applies — a greenfield cluster only lands where real
free resources exist. Here the drive pool fits, but compute can't, so the whole plan is infeasible: the
drive `create` rows are shown for diagnostics only and marked **NOT applied**. Omitting `--node-selector` (as in the first run) widens the candidate set to all
nodes.

### Fidelity summary (verified on this lab)

- **explore-nodes** free/phys reconcile exactly against `kubectl` node allocatable minus summed
  WekaContainer charges (table above).
- **plan create** — the CLI's `RAW CAPACITY` `current` equals the sum of the drive containers the
  controller actually created, and the greenfield plan's `DRIVE`/`COMPUTE` `create` rows match the
  controller's `ClusterCapacityPlanned` placement for the same inputs.
- **plan grow** — the CLI's `DRIVE`/`COMPUTE` `grow` and `OVER-PROVISION` output reproduces what the
  controller applies and emits for the same raised `clusterCapacity` (in-place grow, deferred core change).
- **plan infeasible** — the CLI's `reason` is byte-identical to the controller's `ClusterCapacityInfeasible`
  event for the same spec, and both create/grow nothing.

> **Cross-namespace clusters (OP-346).** The cluster namespace (`-n`) and the scrape namespace
> (`--operator-namespace`) are independent, so a cluster **outside** the operator's namespace still
> picks up the operator's real config: `plan --cluster <name> -n default` looks the cluster up in
> `default` and scrapes the operator from `weka-operator-system` (the `--operator-namespace` default).
> `--from-operator=false` (or `--from-operator false`) correctly disables the scrape and falls back to
> the built-in defaults.

## Related documentation

- [Cluster Capacity](../deployment/cluster-capacity.md) — the capacity model, planner algorithm, worked
  scenarios, events, and Helm settings.
- [Drive Sharing](../operations/drive-sharing.md) — proxy-mode signing and the `ssdproxy` architecture
  that `clusterCapacity` builds on.
