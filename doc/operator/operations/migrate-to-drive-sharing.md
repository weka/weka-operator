# Migrate to Drive Sharing

## Overview

Converts an existing full-drives (exclusive) `WekaCluster` to [drive sharing](drive-sharing.md)
in place, one drive container at a time: each container's drives are gracefully phased out of the
cluster, re-signed for shared use, and a replacement drive-sharing container is created on the same
node while the rest of the cluster keeps serving. No rebuild from object storage is needed.

It migrates **one `WekaCluster` at a time**. Other full-drives clusters on the same nodes are not
touched; run the operation once per cluster (see [After migration](#after-migration)).

Like [rotate-ssdproxy](rotate-ssdproxy.md), it is **`WekaManualOperation`-only** — a one-shot
campaign for one cluster, not a recurring `WekaPolicy`.

## Prerequisites

- Weka release with drive sharing support.
- **Spare hugepages and memory on every target node** for the `ssdproxy` container that drive
  sharing requires. `ssdproxy` hugepages are increase-only and its pod is not auto-restarted on a
  spec change alone — see the [Proxy sub-phase](#3-signing-and-4-proxy) below.
- **The cluster is healthy**: overall status `OK`, fully protected, and every drive `ACTIVE`. The
  same health bar the [per-container gate](#1-gate) re-checks before every container it touches.
- **Enough unprovisioned capacity to phase out one drive container's drives at a time.** The
  cluster keeps running on its remaining containers while one is drained; there must be headroom
  to absorb that container's drives being briefly gone.
- **The cluster's new total capacity, once sized for drive sharing, must be at least what's
  currently provisioned.** Undersizing the sharing-mode fields below is caught before anything is
  touched (see [capacity sanity](#what-each-run-does)), but only if it isn't below what's actually
  in use today.

## Procedure

### 1. Clear the exclusive-signing policies for this cluster's nodes

Delete, or narrow the `nodeSelector` on, every exclusive (non-`shared`) `sign-drives` `WekaPolicy` whose `nodeSelector`
matches any node this cluster's drive containers run on. A recurring exclusive-sign policy racing
the migration's re-sign would keep re-claiming drives for exclusive use right after this operation
frees them for sharing. The operation itself checks for this and **parks until no such policy
matches** — see the policy guard in [What each run does](#what-each-run-does) — so this step is
enforced, not just advised.

### 2. Flip the WekaCluster's sizing fields, with the migration annotation

In one `kubectl apply`, remove the exclusive sizing field and add the drive-sharing ones, and set
the annotation that opts into this specific switch:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: tenant1-prod
  namespace: tenant1
  annotations:
    weka.io/sizing-mode-migration: "drive-sharing"   # required — see below
spec:
  dynamicTemplate:
    # numDrives: 6             # removed — exclusive sizing field
    containerCapacity: 15360   # added — GiB per drive container (or numDrives + driveCapacity, see cluster-capacity.md)
```

Without the annotation, this edit is **rejected** by the admission validator: switching a cluster's
sizing mode while drive containers already exist is blocked unless the switch is one of the three
it explicitly allows (see [the sizing-mode note](../deployment/act-as-daemonset.md#changing-sizing-mode-on-a-live-cluster)).
Setting the annotation is what tells the validator this switch is intentional and is about to be
carried out by this operation — **applying it alone does nothing to the running containers.** They
keep running exclusively until the campaign below actually touches them.

### 3. Start the campaign

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: migrate-tenant1-drive-sharing
  namespace: weka-operator-system   # operator namespace
spec:
  action: migrate-to-drive-sharing
  payload:
    migrateToDriveSharingPayload:
      cluster:
        namespace: tenant1
        name: tenant1-prod
      nodeSelector: {}      # {} -> every exclusive drive container of this cluster
      paused: false
      signOptions: {}       # allowEraseWekaPartitions is always forced on for the re-sign
      driveTypeOverrides: {} # optional TLC/QLC overrides, see drive-signing.md
```

### Payload fields (`MigrateToDriveSharingPayload`)

| Field | Default | Meaning |
|---|---|---|
| `cluster` | — (required) | `{namespace, name}` of the `WekaCluster` to migrate. Exactly one cluster per campaign. |
| `nodeSelector` | `{}` (all) | Restrict the campaign to this cluster's drive containers on nodes matching these labels. The campaign still removes the migration annotation when it finishes; re-apply it before migrating the remaining nodes (see [What each run does](#what-each-run-does), step 8). |
| `paused` | `false` | Stop starting the next container. A container already `InFlight` finishes. |
| `signOptions` | `{}` | Passed through to the child sign-drives operation for the shared re-sign. `allowEraseWekaPartitions` is forced `true` regardless of what's set here — the exclusive signature on these drives must be erased for the shared sign to take. |
| `driveTypeOverrides` | unset | Passed through to the child sign-drives operation; see [Drive type overrides](drive-signing.md#drive-type-overrides-tlcqlc-shared-mode-only). |

### 4. Watch it run

Watch `status.result` and events, as described in [Reading status.result](#reading-statusresult)
and [Events](#events) below.

### After it completes

See [After migration](#after-migration): create the shared `sign-drives` policy, then repeat steps
1–4 for the next tenant on the same nodes.

## What each run does

1. **Resolve the target cluster** — from `payload.cluster`. Not found parks (it may not exist yet
   on a stale campaign, or may have just been deleted).
2. **Refuse to double-run** — if another `migrate-to-drive-sharing` operation is already `Running`
   anywhere in the cluster, **both** park (same reasoning as
   [rotate-ssdproxy's cross-campaign exclusion](rotate-ssdproxy.md#design-note-cross-campaign-exclusion)).
3. **Require the spec flip to already be applied** — the cluster must derive to drive-sharing mode
   (`containerCapacity`/`driveCapacity` set) with `weka.io/sizing-mode-migration: "drive-sharing"`
   still on it. If not, the campaign parks with a reason telling you to apply
   [step 2 of the procedure](#2-flip-the-wekaclusters-sizing-fields-with-the-migration-annotation).
4. **Discover exclusive drive containers** — every drive-mode `WekaContainer` of this cluster that
   is not already drive-sharing. Containers matching `nodeSelector` are queued `Pending`; those
   already drive-sharing are `Skipped`. A container whose node can't be resolved parks the campaign.
   If `nodeSelector` matches no exclusive drive container at all, the campaign parks too, with a
   reason telling you to fix the selector or delete the operation.
5. **Policy guard** — if any exclusive (non-`shared`) `sign-drives` `WekaPolicy`'s `nodeSelector` still matches a target
   node, the campaign parks until it's deleted or narrowed (see
   [procedure step 1](#1-clear-the-exclusive-signing-policies-for-this-clusters-nodes)).
6. **Capacity sanity** — compares the cluster's new total capacity under the drive-sharing sizing
   fields against currently provisioned bytes (`GetWekaStatus()`). Sizing fields are raw capacity
   while weka reports usable bytes, so the provisioned figure is converted to raw with the cluster's
   own stripe width, protection level and hot-spare count before comparing (the headroom check in
   the Gate sub-phase does the same). Parks if the new total would be
   *less* than what's provisioned; emits a one-off Warning if the new total is merely less than the
   *old* total (smaller cluster, still enough headroom).
7. **Migrate one container at a time** through [five sub-phases](#the-per-container-sub-phases) —
   **never more than one container `InFlight`.**
8. **Move to the next, then finish** — once a container's replacement is verified `Done`, the next
   `Pending` container starts. When every container is `Done` or `Skipped`, the campaign emits
   `DriveSharingMigrationCampaignComplete` and removes the migration annotation from the
   `WekaCluster`. With a narrowed `nodeSelector`, "every container" means every container the
   selector matched: the annotation is removed even though exclusive drive containers remain on
   the other nodes. Before starting a campaign for those nodes, re-apply
   `weka.io/sizing-mode-migration: "drive-sharing"` on the `WekaCluster`; without it the new
   campaign parks at the spec-flip check.

## The per-container sub-phases

Each `InFlight` container moves through five sub-phases, persisted in `status.result` so the state
survives an operator restart.

#### 1. Gate

The same per-cluster health checks [rotate-ssdproxy's cross-cluster gate](rotate-ssdproxy.md#the-cross-cluster-gate)
uses — fully protected, `MovingData: false`, healthy overall status, every drive `ACTIVE`, drive and
compute availability above the upgrade thresholds — plus a **headroom check**: unprovisioned
capacity must cover this container's own drives being taken offline. **Fail-closed**: an
unevaluated or errored verdict blocks. Before the first mutation happens, the container's node,
name, drive serials, drive UUIDs, and total drive capacity are recorded into `status.result` — the
container object itself is deleted moments later and is the only other place that information
lives.

#### 2. Draining

`spec.overrides.skipDrivesForceResign: true` is patched onto the container, then it is deleted.
This is the same graceful phase-out every drive container goes through on deletion — deactivate,
wait `INACTIVE`, `weka cluster drive remove`, then normally an exclusive force-resign of the freed
drives. **The override skips that last force-resign**, because it would re-stamp the drives with
the exclusive signature that the next sub-phase needs to erase; without it, the Signing sub-phase's
shared re-sign would be fighting a exclusive signature written moments earlier.

**This can take hours.** Deactivating and moving data off a large drive container (tens of TiB) is
a real rebuild, not a quick unmount — `Stalled` events (see [Events](#events)) fire on the normal
in-flight timers and do **not** indicate a problem by themselves; check the drain's actual progress
against the cluster's rebuild status before assuming something is stuck.

#### 3. Signing (and 4. Proxy)

Once the container is gone:

- **Node annotation cleanup** — the recorded serials are removed from `weka.io/weka-full-drives`
  (and the legacy `weka.io/weka-drives`) on that node, and `weka.io/drives` is recomputed from what
  remains. **Only this cluster's serials are removed** — a node hosting drives for several
  full-drives tenants keeps every other tenant's serials in place and untouched; nothing else on
  the node is affected by this step.
- **Shared re-sign** — a child `WekaManualOperation` is created (`sign-drives`, owned by this
  campaign) targeting exactly the recorded serials on that node:
  ```yaml
  spec:
    action: sign-drives
    payload:
      signDrivesPayload:
        shared: true
        type: device-serials
        deviceSerials: ["233447E40E3C", "233447E40CFD", "..."]   # this container's own serials
        nodeSelector:
          kubernetes.io/hostname: <node>
        options:
          allowEraseWekaPartitions: true   # always forced, regardless of payload.signOptions
        driveTypeOverrides: {}             # passed through from payload.driveTypeOverrides
  ```
  Because the node annotation was already cleared, the shared sign is free to erase and re-claim
  exactly these drives without touching any other tenant's. The node's `weka.io/weka-shared-drives`
  annotation is the source of truth for this sub-phase: once every recorded serial appears there,
  the container moves on and the child operation is never consulted again, so a child that has
  already self-deleted after completing can never trigger a second erase. A failed child parks
  this container with the child's own result attached; after a backoff the child is deleted and
  recreated (`signAttempts` counts the retries in `status.result`). A child that reports `Done`
  while serials are still missing parks permanently for inspection; that park is latched in
  `status.result` (`signDone`), so the child self-deleting later never triggers a recreate.
- **Proxy recovery** — if the node already runs an `ssdproxy` (because another tenant already
  shares drives there), the newly-signed drives are picked up without necessarily needing a
  restart; if the proxy's reported physical drives don't yet include the new UUIDs, or its
  hugepages are behind the container's `spec.hugepages`, the proxy pod is restarted (deleted; the
  container controller recreates it) **behind the same [cross-cluster gate](rotate-ssdproxy.md#the-cross-cluster-gate)
  rotate-ssdproxy uses** — every other tenant sharing that node must be healthy first. If no
  `ssdproxy` exists yet on the node, nothing is done here: the replacement drive-sharing container
  in the next sub-phase creates it.

#### 5. Rejoining

The cluster controller has already started a replacement: since the cluster's spec now derives to
drive-sharing, and this node lost its exclusive container, the planner grows a new drive-sharing
container onto the same node (matched via `GetNodeAffinity()`, same mechanism as
[rotate-ssdproxy's node-locality discovery](rotate-ssdproxy.md#the-cross-cluster-gate)). This
sub-phase waits for that replacement's pod to be `Running`, its allocated virtual drives to be
non-empty, and those drives to read back `ACTIVE` on the cluster — then re-runs the per-cluster gate
once more before marking the container `Done`. A `DriveSharingMigrationNodeComplete` event fires on
both the operation and the `WekaCluster`. The campaign prefers a replacement on the drained node.
Count-based sizing pins no node to a drive container, and the old pod's anti-affinity keeps the
drained node blocked while it phases out, so on a fleet with spare eligible nodes that already hold
shared capacity the replacement can land elsewhere; such a container is accepted only if it was
created after this container's turn started, and a `DriveSharingMigrationReplacementElsewhere`
Warning names both nodes. The drained node's freed capacity then stays available to other tenants.
An unscheduled container is never adopted.

`paused: true` stops a **new** container from starting at the end of this sub-phase; a container
already partway through its own five sub-phases keeps going to `Done`.

## Reading `status.result`

```bash
kubectl get wekamanualoperation migrate-tenant1-drive-sharing -n weka-operator-system -o json \
  | jq -r '.status.result | fromjson'
```

```json
{
  "cluster": "tenant1/tenant1-prod",
  "total": 12,
  "done": 3,
  "current": "weka04",
  "oldTotalBytes": 490000000000000,
  "newTotalBytes": 720000000000000,
  "provisionedBytes": 410000000000000,
  "containers": [
    {
      "node": "weka04",
      "container": "tenant1-prod-drive-3",
      "phase": "InFlight",
      "subPhase": "Draining",
      "serials": ["233447E40E3C", "233447E40CFD", "233447E40E5A"],
      "driveUuids": ["a1b2c3d4-...", "e5f6a7b8-..."],
      "capacityBytes": 45000000000000,
      "capacitySource": "weka",
      "startedAt": "2026-09-18T09:14:02Z"
    },
    {
      "node": "weka01",
      "container": "tenant1-prod-drive-0",
      "phase": "Done",
      "replacement": "tenant1-prod-drive-12"
    },
    {
      "node": "weka09",
      "container": "tenant1-prod-drive-9",
      "phase": "Skipped"
    }
  ],
  "blocked": [],
  "err": ""
}
```

- **`phase`** — `Pending`, `InFlight`, `Done`, or `Skipped`. No `Failed` phase: a container that
  cannot proceed parks instead (see [Parking, not failing](#parking-not-failing)).
  - `Done` is **terminal** — recorded once verified, never re-entered.
  - `Skipped` means the container was already drive-sharing when discovered; **not terminal** in
    principle, but there is no path back to exclusive, so in practice it stays.
- **`subPhase`** — one of `Gate`, `Draining`, `Signing`, `Proxy`, `Rejoining`, set only while
  `InFlight`. This is what a `DriveSharingMigrationStalled` event's message names.
- **`serials` / `driveUuids` / `capacityBytes`** — captured once, in the [Gate sub-phase](#1-gate),
  before the container is deleted. `capacitySource` records whether `capacityBytes` came from a
  live weka query or a fallback to the node annotation.
- **`replacement`** — the drive-sharing container's name once [Rejoining](#5-rejoining) finds and
  verifies it.
- **`signOp` / `signAttempts` / `signDone`** — the child sign-drives operation's name, how many
  times it has been retried after a failure, and whether it reported `Done` while serials were
  still missing (latches the park permanently; see [Signing](#3-signing-and-4-proxy)).
- **`blockedSince`** (per container) / top-level **`blockedSince`** — stamped the same way as
  [rotate-ssdproxy's two `blockedSince` fields](rotate-ssdproxy.md#reading-statusresult): one for a
  container parked before its turn, one for the campaign parking before any container is even
  picked (double-run, spec flip not applied yet, policy guard, capacity sanity).
- **`err`** — campaign-level, **terminal**: `status.status` becomes `Failed`. In v1 this fires only
  for a malformed `payload.cluster` reference; every other block is a park, not a failure.

## Parking, not failing

If a container cannot proceed, the campaign parks on that container and does not advance to any
other one — the same discipline as [rotate-ssdproxy](rotate-ssdproxy.md#parking-not-failing).

| Signal | When | Timed from | Warn after | Repeat every | Event reason |
|---|---|---|---|---|---|
| **Blocked** | Container is `Pending`, gate/policy-guard/capacity check refuses to start it | `blockedSince` | 15 minutes | 30 minutes | `DriveSharingMigrationBlocked` |
| **Stalled** | Container is `InFlight`, hasn't reached `Done` | `startedAt` | 5 minutes | 10 minutes | `DriveSharingMigrationStalled` |

**A long `Draining` sub-phase is expected, not a stall in the ordinary sense.** Phasing out tens of
TiB is a real rebuild and can legitimately run for hours; the `Stalled` event still fires on its
normal timer (it has no way to distinguish "draining normally, just slow" from "actually stuck"),
so check the drain's real progress via cluster rebuild status before treating it as a problem.

## Events

| Reason | Level | When | Also recorded on |
|---|---|---|---|
| `DriveSharingMigrationStarted` | Normal | A container begins its five sub-phases | Operation |
| `DriveSharingMigrationNodeComplete` | Normal | A container's replacement is verified `Done` | Operation and `WekaCluster` |
| `DriveSharingMigrationCampaignComplete` | Normal | Every container is `Done` or `Skipped` | Operation and `WekaCluster` |
| `DriveSharingMigrationBlocked` | Warning | A `Pending` container blocked past the threshold above | Operation |
| `DriveSharingMigrationStalled` | Warning | An `InFlight` container hasn't finished past the threshold above; message names the current `subPhase` | Operation |
| `DriveSharingMigrationReplacementElsewhere` | Warning | The verified replacement landed on a different node than the one drained; message names both nodes | Operation and `WekaCluster` |
| `DriveSharingMigrationCapacityShrinks` | Warning | Once, from the [capacity sanity](#what-each-run-does) check: the new total capacity is less than the old one but still covers what is provisioned. Informational; the campaign proceeds | Operation |

## Escape hatches

1. **Fix the cause** — restore cluster health, delete/narrow the offending `sign-drives` policy,
   free up capacity. The campaign resumes on its own.
2. **Pause** — set `payload.paused: true`. A container already `InFlight` finishes its current
   sub-phase sequence to `Done`.
3. **Abort** — delete the `WekaManualOperation`. **Safe even mid-`Draining`**: the drive container's
   own deletion flow (already in progress, owned by the container controller, not this operation)
   keeps deleting the drives regardless, and once the cluster's spec still derives to drive-sharing,
   the cluster controller recreates that node's container as a drive-sharing one anyway. Aborting
   does not leave the node stuck exclusive-only; it just stops this campaign from tracking the rest
   of the fleet's containers and from doing the node annotation cleanup / re-sign for containers
   not yet started.

## After migration

Once the campaign completes, exclusive-mode drives on the migrated nodes have been re-signed into
`weka.io/weka-shared-drives`; the cluster's own containers are now drive-sharing. Create the shared
policy so *other* tenants (and future capacity) on those nodes can also draw from the shared pool:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: sign-drives
  namespace: weka-operator-system
spec:
  type: sign-drives
  payload:
    signDrivesPayload:
      type: "all-not-root"
      shared: true
```

This policy naturally **excludes** any serials still recorded under `weka.io/weka-full-drives` —
other full-drives tenants still exclusively owning drives on the same nodes are untouched and keep
working exactly as before.

The policy runs without `force`, so it skips any node whose `weka.io/sign-drives-hash` annotation is
already current, which includes every migrated node. In the rare case a migrated node still has
unsigned drives that should join the shared pool, first remove the `weka.io/sign-drives-hash`
annotation from that node, or run a one-off `sign-drives` `WekaManualOperation` with `force: true` (see
[Re-running sign-drives](drive-signing.md#re-running-sign-drives-and-adding-new-drives)). Repeat the whole procedure — [clear policies](#1-clear-the-exclusive-signing-policies-for-this-clusters-nodes),
[flip the spec](#2-flip-the-wekaclusters-sizing-fields-with-the-migration-annotation),
[run the campaign](#3-start-the-campaign) — once per remaining full-drives tenant on those nodes.

A cluster migrated this way can later move again, from `containerCapacity`/`driveCapacity` to
`clusterCapacity`, using the existing in-place migration in
[Cluster Capacity: Migrating from containerCapacity](../deployment/cluster-capacity.md#migrating-from-containercapacity)
— that switch needs no annotation and no drain, since both modes already hold virtual drives.

## Limitations

- **v1 only migrates from explicit container counts** (`numDrives` on the source spec). A cluster
  already in `auto-full-drives` (daemonset) mode cannot use this operation — that switch stays
  rejected by the validator regardless of the annotation.
- **The target must be `containerCapacity`/`driveCapacity` (drive-sharing), not `clusterCapacity`
  directly.** Land on drive-sharing first, then use the existing
  [drive-sharing → clusterCapacity](../deployment/cluster-capacity.md#migrating-from-containercapacity)
  migration afterward if you want `clusterCapacity`.
- **One `WekaCluster` per campaign.** A fleet with several full-drives tenants on the same shared
  nodes runs this once per tenant, in sequence — see [After migration](#after-migration).
