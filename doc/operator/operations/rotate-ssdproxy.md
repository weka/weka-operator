# Rotate SSD Proxy

## Overview

On [drive-sharing](drive-sharing.md) deployments, each node runs a single `ssdproxy` `WekaContainer`
that carves physical drives into virtual drives (VIDs) for **every tenant cluster** with drives on
that node. The operator never auto-upgrades it. Restarting a proxy is therefore not a routine rolling
update — it's a multi-tenant disruption, and it must not happen on a node while any dependent cluster
would be hurt by it.

This operation rolls a new proxy image across the fleet **one node at a time**, gating each node on
the health of every cluster that depends on it. It is **`WekaManualOperation`-only** — a recurring
`WekaPolicy` form is intentionally not offered; this is a one-shot campaign you start on purpose for
a specific image.

## API

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: rotate-proxies-641
  namespace: weka-operator-system   # operator namespace
spec:
  action: rotate-ssdproxy
  payload:
    rotateSsdProxyPayload:
      targetImage: ""          # "" -> falls back to helm driveSharing.ssdProxy.imageOverride
      nodeSelector: {}          # {} -> all nodes that have an ssdproxy
      paused: false
```

### Payload fields (`RotateSsdProxyPayload`)

| Field | Default | Meaning |
|---|---|---|
| `targetImage` | `""` | Image to roll out. If empty, falls back to the helm-configured `driveSharing.ssdProxy.imageOverride` (env `SSD_PROXY_IMAGE_OVERRIDE`). If **both** are empty the operation fails immediately without touching anything: `status.status` becomes `Failed`. The resolved value is also **immutable once a campaign has planned** — this includes a campaign whose nodes are all still `Pending`, since the check fires from the first successful `Plan`, not the first patch: because an empty `targetImage` falls back to the helm override, the resolved image can change under a running campaign with no CR edit at all, and a node already patched under the old value can't be safely re-targeted mid-campaign — so this fails the campaign too rather than parking it. These are the **only two hard failures** — everything else parks (see [Parking, not failing](#parking-not-failing)). Recover from either by deleting the campaign and creating a new one with the correct image. |
| `nodeSelector` | `{}` (all) | Restrict the campaign to ssdproxies on nodes matching these labels. Matched against the **node's** labels, not the proxy pod's. A **non-empty** selector matching no ssdproxy parks the campaign and retries every ~15s, so a mistyped label starts on its own once matching nodes are labelled. An **empty** selector matching nothing (a fleet with no drive sharing) is a legitimate no-op and completes normally. |
| `paused` | `false` | Stop starting new nodes. A node already in flight is left to finish. |

There is deliberately **no `force` flag, no `dryRun`, and no per-node timeout.** The cross-cluster gate
*is* the operation — a way to bypass or time it out would defeat the reason it exists.

## What each run does

1. **Resolve the target image** — payload `targetImage` wins, else the helm/env fallback, else fail.
2. **Refuse to double-run** — if another `rotate-ssdproxy` operation is already `Running`, this one
   refuses to start, so two campaigns can't race the same nodes.
3. **Resolve target proxies** — `ssdproxy`-mode `WekaContainer`s in the operator namespace, optionally
   filtered by `nodeSelector`. Any node already on the target image is marked `Skipped` and not
   touched — this makes re-applying the same CR, or recovering after an operator restart, safe.
   `Skipped` is re-evaluated every cycle, not decided once: if the proxy later moves off the target
   image (e.g. its CR is recreated on the operator's configured image), the node re-enters the
   campaign as `Pending` and is rotated like any other.
4. **Pick one node** — the node in flight if there is one, otherwise the first `Pending` node in
   sorted node-name order. **Never more than one node in flight at a time.**
5. **Gate, then patch** — the [cross-cluster gate](#the-cross-cluster-gate) must pass first. If it
   does, only `spec.image` is patched on that node's ssdproxy; the existing image-drift machinery
   recreates the pod on the new image, exactly as if you'd patched it by hand.
6. **Wait for recovery, then move on** — waits for the proxy to report `Running` / `READY`, re-runs
   the gate checks plus a node-scoped drive-`ACTIVE` check, marks the node `Done`, and picks the next.
   When every node is `Done` or `Skipped`, the campaign emits a completion event and stops.

## The cross-cluster gate

Before rotating a node — and again after, to confirm recovery — the operation identifies **every
WekaCluster with drives on that node** and checks that disrupting the proxy is safe for all of them.

**Discovery** unions two independent sources:

- **Node-locality** — every drive-sharing `WekaContainer` with node affinity to this node, resolved
  back to its parent cluster.
- **Proxy-side truth** — the virtual drives actually registered on the node's proxy, matched by owner
  cluster GUID against known `WekaCluster`s.

If **either** source fails to resolve, the check fails closed for that node.

**Per cluster**, all of the following must hold, or the gate blocks:

- An operational container is reachable to query cluster status, and the status fetch succeeds.
- The cluster's rebuild status is fully protected.
- Overall cluster status is `OK` or `REDISTRIBUTING`.
- No data is currently moving.
- **Every drive is `ACTIVE`** — `INACTIVE`, `PHASING_IN`, or anything else blocks. No partial-failure
  allowance.
- Drive and compute container availability meet the standard upgrade thresholds
  (`Upgrade.DriveThresholdPercent` / `Upgrade.ComputeThresholdPercent`, both default **90%**).

The gate is **fail-closed by construction** — an unevaluated or errored verdict is treated as "not
allowed." Example blocked reasons in `status.result` or a `SsdProxyRotationBlocked` event:

```
cluster prod-nvme-1: rebuild not fully protected
cluster prod-nvme-1: 2 drive(s) not ACTIVE: a1b2c3d4 (INACTIVE), e5f6a7b8 (PHASING_IN)
cluster prod-nvme-1: no operational container available to query status
cluster prod-nvme-1: drive availability 84% below threshold 90%
```

Long serial lists are truncated (`, … and N more`) to keep event messages readable.

The **post-rotation** check re-runs the same per-cluster checks and additionally confirms that
cluster's drives *on that node* are back to `ACTIVE` — a cluster can look healthy overall while the
node that just came back is still recovering. Two messages are normal here and clear on their own
within the restart window:

```
cannot verify drives on node X yet: N drive container(s) not yet joined to the cluster
cannot verify drives on node X yet: expected drive containers but observed none
```

## What a tenant looks like while its node is being rotated

**Expect each dependent tenant to report `REBUILDING` and degraded protection for roughly a minute
while its node's proxy restarts. This is normal and is not the gate failing.** Restarting the proxy
takes that node's virtual drives away from every tenant on the node, and weka reports that the only
way it can:

```
tenant-a OK          moving=False protected=True   drv=12/12    <- before
tenant-a REBUILDING  moving=False protected=False  drv=10/12    <- proxy restarting
tenant-a OK          moving=False protected=True   drv=12/12    <- recovered, before the next node starts
```

The guarantee is not the absence of that dip, but that it is **safe and bounded**:

- a tenant is only disrupted while it is fully protected with **zero** non-`ACTIVE` drives, so losing
  one node's drives cannot cross into data loss;
- no data is *evacuated* — drives are never deactivated or removed, so no data-moving rebuild is induced;
- the dip is limited to **one node's drives**, and the tenant is verified back to fully protected
  before the next node is touched (which is why `SsdProxyRotationNodeComplete` always arrives before
  the next `SsdProxyRotationStarted`).

So the thing to watch is not "did a tenant go `REBUILDING`" — it will — but **whether it comes back
before the next node starts.** A tenant that stays degraded, or two nodes' worth of drives missing at
once, is the real anomaly.

## Reading `status.result`

Campaign state is persisted as JSON in `status.result`:

```bash
kubectl get wekamanualoperation rotate-proxies-641 -n weka-operator-system -o json \
  | jq -r '.status.result | fromjson'
```

```json
{
  "targetImage": "quay.io/weka/ssdproxy:4.4.1.100",
  "total": 44,
  "done": 12,
  "currentNode": "h12-3-b",
  "nodes": [
    {
      "node": "h12-3-b",
      "proxyName": "ssdproxy-h12-3-b",
      "phase": "InFlight",
      "previousImage": "quay.io/weka/ssdproxy:4.4.0.87",
      "image": "quay.io/weka/ssdproxy:4.4.1.100",
      "startedAt": "2026-08-06T09:14:02Z"
    },
    {
      "node": "h12-4-a",
      "proxyName": "ssdproxy-h12-4-a",
      "phase": "Pending",
      "blockedSince": "2026-08-06T09:05:11Z",
      "reason": "cluster prod-nvme-1: 1 drive(s) not ACTIVE: 9c8d7e6f (INACTIVE)"
    },
    {
      "node": "h12-2-c",
      "proxyName": "ssdproxy-h12-2-c",
      "phase": "Done",
      "previousImage": "quay.io/weka/ssdproxy:4.4.0.87",
      "image": "quay.io/weka/ssdproxy:4.4.1.100"
    },
    {
      "node": "h12-1-a",
      "proxyName": "ssdproxy-h12-1-a",
      "phase": "Skipped",
      "image": "quay.io/weka/ssdproxy:4.4.1.100"
    }
  ],
  "blocked": [
    {
      "namespace": "weka-operator-system",
      "name": "prod-nvme-1",
      "clusterGUID": "848376a4-f017-48bc-9376-a9c3d60d7e30",
      "allowed": false,
      "reason": "1 drive(s) not ACTIVE: 9c8d7e6f (INACTIVE)"
    }
  ]
}
```

- **`phase`** — `Pending`, `InFlight`, `Done`, or `Skipped`. **There is no `Failed` phase**; every
  per-node failure mode parks instead. Both phases below count as complete, but only one of them is
  terminal:
  - `Done` — this campaign rotated the node and verified it afterwards. **Terminal**: a record of
    what this campaign already did, so it is never re-entered even if the proxy drifts off the
    target image later (re-entering would loop on external drift).
  - `Skipped` — the proxy was **already** on `targetImage`; nothing was touched. **Not terminal**:
    it is a claim about the proxy's *current* image, re-evaluated every cycle, so if the proxy moves
    off the target image the node reverts to `Pending` and gets rotated.
- **`previousImage`** — set only when this campaign patched the node, and never cleared. It's both the
  marker for "we touched this node" and your rollback target ([escape hatch #2](#escape-hatches)).
- **`blockedSince`** / **`startedAt`** — stamped when a node first parks or starts; see
  [Parking, not failing](#parking-not-failing).
- **`blocked`** — gate verdicts for the current node, refreshed continuously. Contains **all**
  verdicts, passing ones included (`"allowed": true`, no reason), not only the blocking ones.
- **`err`** — a campaign-level error from `Plan`. Usually this fires before any node is picked for the
  cycle, but `Plan` runs before `AdvanceOne`, so it can also fire while a node from a previous cycle is
  still `InFlight` and unverified — see the campaign-scoped event note below:
  - **Terminal** — `status.status` becomes `Failed` and the campaign will not resume on its own
    (see [Escape hatches](#escape-hatches)). Two causes:
    1. the target image couldn't be resolved at all — fix `targetImage` (or the helm override) and
       re-apply;
    2. the resolved target image changed out from under an already-started campaign (see the
       `targetImage` row above) — editing the CR doesn't help here; delete this `WekaManualOperation`
       and create a new one with the correct image.
  - **Transient** — another campaign is already `Running`, proxy listing failed, or a non-empty
    `nodeSelector` matched nothing. `status.status` stays `Running`, retries every ~15s, and `err`
    disappears once the condition lifts. From the first cycle a transient park like this happens, a
    top-level **`blockedSince`** (a sibling of `err`, **distinct** from the per-node `blockedSince`
    inside `nodes[]`) is stamped too, and clears the moment planning succeeds. It drives a throttled,
    campaign-scoped `SsdProxyRotationBlocked` Warning event — see [Events](#events) — so a plan-scope
    stall (queued behind another campaign, a typo'd `nodeSelector`, a listing blip) is visible as an
    event too, not only in `status.result.err`.

## Parking, not failing

**If a node cannot be safely rotated, the campaign parks on that exact node and does not advance to
any other node.** It re-checks that same node roughly every 15 seconds until it clears. No other
node's rotation proceeds in the meantime, even if they would pass the gate right now. This is
deliberate: there is no timeout and no automatic skip-ahead.

*(This covers node-scoped parking. A campaign can also park before any node is even picked — see the
Transient `err` case above, which stamps the campaign-level `blockedSince` instead of a per-node one.)*

Two parked states are surfaced differently:

| Signal | When | Timed from | Warn after | Repeat every | Event reason |
|---|---|---|---|---|---|
| **Blocked** | Node is `Pending`, gate refuses to let it start | `blockedSince` | 15 minutes | 30 minutes | `SsdProxyRotationBlocked` |
| **Stuck** | Node is `InFlight`, hasn't reached ready+recovered | `startedAt` (patch time) | 5 minutes | 10 minutes | `SsdProxyRotationStalled` |

A proxy that's been down 5+ minutes is more urgent than a tenant that's been rebuilding for 15, hence
the different thresholds. Both warnings are **pure observability — neither changes what the campaign
does.**

## Events

| Reason | Level | When | Message format |
|---|---|---|---|
| `SsdProxyRotationStarted` | Normal | A node's proxy is successfully patched to the target image | `Started ssdproxy rotation on node %s (%d/%d nodes complete): %s -> %s` |
| `SsdProxyRotationNodeComplete` | Normal | A node finishes recovering after rotation | `Rotated ssdproxy on node %s (%d/%d nodes complete)` |
| `SsdProxyRotationCampaignComplete` | Normal | Every targeted node is `Done` or `Skipped` | `ssdproxy rotation complete: %d nodes on image %s` |
| `SsdProxyRotationBlocked` | Warning | A `Pending` node has been blocked at the gate past the threshold above | `Node %s has been blocked at the pre-restart gate for %s: %s` |
| `SsdProxyRotationStalled` | Warning | An `InFlight` node hasn't recovered past the threshold above | `Node %s has been stuck in-flight for %s: %s` |

Two of these reasons carry more than one meaning:

- **`SsdProxyRotationBlocked`** also fires, throttled the same way, when the *campaign itself* parks
  before any node is even picked — the plan-scope `blockedSince` case in
  [Reading `status.result`](#reading-statusresult). This is reserved for the case where no node has
  been targeted at all; see the third `SsdProxyRotationStalled` case below for the same park with a
  node already `InFlight`.
- **`SsdProxyRotationStalled`** covers three more cases besides the timeout above, all meaning
  "this node's rotation status is now unknown":
  1. the campaign **fails terminally** while a node is still `InFlight` — that node is left patched
     but unverified, and the event names the abandoned node plus the image it was patched to and the
     one it ran before;
  2. a node that had already progressed past `Pending` is **dropped from the campaign** because its
     proxy is no longer targeted (proxy deleted, node relabelled out of the `nodeSelector`, node
     removed). Losing an `InFlight` or `Done` node would otherwise shift `total`/`done` with no
     trace. A dropped `Pending` node is logged but raises no event — the campaign never touched it;
  3. the same campaign-scope park that drives `SsdProxyRotationBlocked` above catches a node already
     `InFlight` (e.g. a second campaign CR applied mid-rotation) — the throttled event fires on the
     Stuck thresholds instead of the Blocked ones, naming that node as patched but unverified while
     the campaign is blocked.

Events are recorded on the `WekaManualOperation` object itself.

## Escape hatches

A parked campaign resolves in one of four ways:

1. **Fix the cause** — restore protection on the blocked tenant, replace the failing drive. The
   campaign resumes on its own; nothing needs restarting or re-applying.
2. **Roll the node back** — patch `spec.image` on that one proxy `WekaContainer` back to the
   `previousImage` from `status.result`, then delete the campaign CR.
3. **Pause** — set `payload.paused: true` while you investigate. A node already `InFlight` finishes.
4. **Abort** — delete the `WekaManualOperation`. A node mid-restart is unaffected (the container
   controller owns the pod from there); no further nodes are touched.

These four apply to a campaign that is still **parked and `Running`**. A campaign that has gone
**terminally `Failed`** (see [Reading `status.result`](#reading-statusresult)) is inert — it doesn't
re-enter the state machine every ~15s waiting for something to change, it just sits. Only #2 (if a
node was already patched before the failure) applies; #1 and #3 do nothing. Fix it by deleting the
`WekaManualOperation` and creating a new one with the corrected `targetImage`. And unlike a completed
(`Done`) campaign, a `Failed` one is **not** auto-deleted after the completion delay — delete it by
hand once you're done with it.

## Recommended usage

- **Start unpaused with a wide (or no) `nodeSelector`, and watch the first few nodes complete** before walking away — confirms the image and the gate behave on real traffic.
- **Healthy progress** is a steady march through `status.result.done`, with `currentNode` changing and `SsdProxyRotationStarted` / `SsdProxyRotationNodeComplete` a few minutes apart.
- **A node parked for a few minutes is normal.** Don't intervene on the first `SsdProxyRotationBlocked` or `SsdProxyRotationStalled` — they're tuned to fire before anything is actually wrong. Do intervene if a node stays parked well past the repeat interval, or the same blocking reason keeps recurring.
- **`status.result` is the durable record, not events** — events expire (~1 hour default TTL). For anything beyond "did it just happen," read `status.result`.

## Design note: cross-campaign exclusion

When a second `rotate-ssdproxy` campaign is detected, **both** refuse rather than one winning and
the other stepping aside. Electing a winner needs an ordering signal, and none is safe:
`creationTimestamp` is only second-granular (two campaigns applied together can tie), and
`status.status` lags the very first reconcile of a brand-new campaign — exactly the window where
getting it wrong means two campaigns both believe they're the winner and start patching proxies at
once. Refusing both costs a stall a human clears by deleting one CR; the reason is persisted into
`status.result.err` on both, each naming the other, so it's obvious which two to look at.
