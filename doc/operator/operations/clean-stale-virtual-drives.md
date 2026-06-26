# Clean Stale Virtual Drives

## Overview

On [drive-sharing](drive-sharing.md) deployments, physical drives are carved into **virtual drives
(VIDs)** registered on each node's `ssdproxy` container. A VID is owned by a cluster GUID and is
normally reclaimed by the per-container teardown path when its container/cluster goes away.

That path is **per-container and self-referential** — it only iterates the deleting container's own
allocations. If a container or the operator dies mid-teardown (notably the pre‑v1.10.7
silent-removal bug, or a prior cluster incarnation), the VID stays registered on the proxy with no
live owner and is **never reclaimed**. On Weka versions that disallow over-provisioning, these
leaked VIDs consume capacity and block expansion.

This operation adds **fleet-level reconciliation**: it enumerates every VID on every targeted
ssdproxy, diffs them against all live allocations, and surfaces — and optionally removes — the stale
ones. **Detection always runs and reports; deletion is opt-in and double-gated** (see
[Safety model](#safety-model)).

## Two ways to run

Both share one payload (`cleanStaleVirtualDrivesPayload`) and the same logic:

| CR | Cadence | Use |
|---|---|---|
| `WekaManualOperation` (`action: clean-stale-virtual-drives`) | one-shot | ad-hoc audit / cleanup |
| `WekaPolicy` (`type: clean-stale-virtual-drives`) | periodic, on `interval` | continuous detection / GC |

## API

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation        # or WekaPolicy
metadata:
  name: clean-stale-vids
  namespace: weka-operator-system   # operator namespace
spec:
  action: clean-stale-virtual-drives   # WekaPolicy uses `type:` instead of `action:`
  payload:
    # interval: 1m                     # WekaPolicy only
    cleanStaleVirtualDrivesPayload:
      nodeSelector: {}
      onlyNonExistingClusters: false
      deleteStaleVids: false
```

> Do **not** set `spec.image` for this operation (unlike `sign-drives`) — it uses the node-agent
> JSONRPC path, not a signing image.

### Payload fields (`CleanStaleVirtualDrivesPayload`)

| Field | Default | Meaning |
|---|---|---|
| `nodeSelector` | `{}` (all) | Limit the scan to ssdproxies on nodes matching these labels. |
| `onlyNonExistingClusters` | `false` | Restrict the stale set to the `dead_cluster` subset — VIDs whose owner GUID has **no** `WekaCluster` CR at all. **Recommended `true` when pairing with deletion.** |
| `deleteStaleVids` | `false` | Enable actual removal. **Dangerous** — even when `true`, a VID is removed only after passing the two-cycle gate and a final re-validation. |

## What each run does

1. **Resolve target proxies** — `ssdproxy`-mode `WekaContainer`s in the operator namespace
   (optionally filtered by `nodeSelector`).
2. **Scan** — list each proxy's VIDs via node-agent JSONRPC.
3. **Build the claimed set** — union of `Status.Allocations.VirtualDrives[].VirtualUUID` over **all**
   `WekaContainer`s in **all** states, read **uncached** so a just-written allocation is never missed.
4. **Diff & categorize** — a scanned VID not in the claimed set is *stale*, categorized by owner GUID
   against all `WekaCluster.Status.ClusterID`:
   - `dead_cluster` — no `WekaCluster` CR has that GUID (safe-by-construction subset).
   - `live_cluster_unclaimed` — a `WekaCluster` has the GUID, but no container claims the VID.

   `onlyNonExistingClusters: true` keeps only the `dead_cluster` subset.
5. **Stability-gated deletion** (only if `deleteStaleVids: true`) — see below.
6. **Report** — write the result JSON to status, log per-VID WARNs, emit events.

## Safety model

Deletion is **double-gated** and never acts on a single observation:

- **Opt-in** — nothing is removed unless `deleteStaleVids: true`.
- **Two-cycle fingerprint stability** — a hash over the sorted `(node | virtualUuid | ownerGUID)` of
  the stale set must match the previous cycle's; a VID is removed only when the **identical**
  non-empty set is seen twice in a row. Any membership/ownership change resets the gate.
  - *Manual op*: cycle 1 persists the result and requeues (~15s); cycle 2 confirms and deletes.
  - *Policy*: the gate spans `interval` runs via `Status.LastResult` — run 1 detects, run 2 deletes.
- **Final uncached re-validation** — immediately before each removal the claimed set is re-read; a
  VID that became claimed since the scan is spared.
- **Partial scans never delete** — if any proxy fails to scan, the gate stays closed for that run.
- **Per-VID failures are non-fatal** — recorded and retried on the next scan.

Because allocation is persisted to `Status.Allocations` **before** the VID is registered on the
proxy (and `AddVirtualDrives` guards on a populated `ClusterID`), a forming cluster's in-flight VID
is always already in the claimed set — "on proxy but not yet claimed" cannot occur.

## Result & surfacing

The full result (`StaleVirtualDrivesResult`) is written as JSON to `Status.Result`
(`WekaManualOperation`) / `Status.LastResult` (`WekaPolicy`). Clean environment:

```json
{"scannedNodes":14,"staleCount":0,"staleTiB":0,"fingerprint":"","deletionEligible":false}
```

Report-only run against a 14-node lab with two orphan VIDs and one live cluster
(`848376a4-…`) running — the cluster's real VIDs are excluded, only the two orphans report:

```json
{
  "scannedNodes": 14,
  "staleCount": 2,
  "staleTiB": 0.1953125,
  "fingerprint": "38c729f929b39e73a63769e7b43f0de25db798c6bd8464e404adff80a9651b03",
  "deletionEligible": false,
  "staleVids": [
    {
      "node": "h6-8-d",
      "physicalUUID": "ed04ded0-2420-437e-83e5-ae72ac3186d9",
      "virtualUUID": "11111111-2222-3333-4444-555555555502",
      "ownerClusterGUID": "848376a4-f017-48bc-9376-a9c3d60d7e30",
      "sizeGB": 100,
      "category": "live_cluster_unclaimed"
    },
    {
      "node": "h6-8-d",
      "physicalUUID": "af1e11dc-8538-40b3-bac4-a99db486eacd",
      "virtualUUID": "dead0000-0000-0000-0000-000000000001",
      "ownerClusterGUID": "deadbeef-dead-dead-dead-deadbeef0001",
      "sizeGB": 100,
      "category": "dead_cluster"
    }
  ]
}
```

With `onlyNonExistingClusters: true` the `live_cluster_unclaimed` VID is dropped (`staleCount:1`,
only `dead_cluster`). With `deleteStaleVids: true` the result reports `"deletionEligible": true` and
the removed UUIDs in `"deleted"` once the two-cycle gate passes.

**Events** on the owner CR (real output, detection + a gated-delete run):

```text
Warning   StaleVirtualDrivesDetected   Detected 2 stale virtual drive(s) (0.20 TiB) across 14 node(s); owner cluster GUIDs: 848376a4-f017-48bc-9376-a9c3d60d7e30, deadbeef-dead-dead-dead-deadbeef0001
Warning   StaleVirtualDriveRemoved     Removed stale virtual drive 11111111-2222-3333-4444-555555555502 (owner cluster 848376a4-f017-48bc-9376-a9c3d60d7e30, category live_cluster_unclaimed) on node h6-8-d
Warning   StaleVirtualDriveRemoved     Removed stale virtual drive dead0000-0000-0000-0000-000000000001 (owner cluster deadbeef-dead-dead-dead-deadbeef0001, category dead_cluster) on node h6-8-d
```

`StaleVirtualDrivesDetected` fires when `staleCount > 0`; one `StaleVirtualDriveRemoved` fires per
removed VID. Each stale VID is also logged as a structured WARN (`virtual_uuid`,
`owner_cluster_guid`, `category`, `physical_uuid`, `size_gb`, `node`). There is no Prometheus
gauge — events, logs, and status cover detection and alerting.

## Recommended usage

- **Audit first** — run report-only (`deleteStaleVids: false`) and inspect `staleVids` / events
  before enabling deletion.
- **Safest GC** — pair `deleteStaleVids: true` with `onlyNonExistingClusters: true` so only VIDs of
  clusters that no longer exist are removed.
- **Continuous reclamation** — a `WekaPolicy` with a modest `interval` keeps leaked VIDs from
  accumulating; the cross-interval gate prevents deletion on a single transient observation.
