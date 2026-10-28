# Block Drives

`block-drives` and `unblock-drives` are `WekaManualOperation` actions that tell the operator to stop
using specific drives on a node. Blocking is **asynchronous**: it only records the request. The
drive containers on that node pick it up on their own reconcile loop and converge over the next
`PeriodicDrivesCheckInterval` (1 minute) — it is not instant, and there is no "done" signal beyond
polling the operation result and observing the drive actually leave.

## The three identifiers

`blockDrivesPayload` takes a node plus one or more identifier lists. Pick the list that matches
what actually failed:

| Identifier | Field | Removes | Node annotation |
|---|---|---|---|
| Serial ID | `serialIDs` | The physical drive, by its serial (non-proxy / exclusive-drive mode) | `weka.io/blocked-drives` |
| Physical UUID | `physicalUUIDs` | **Every** virtual drive (VID) carved from that physical, across **all tenants** sharing it | `weka.io/blocked-drives-physical-uuids` |
| Virtual UUID | `virtualUUIDs` | Exactly the named VID(s), nothing else on the physical | `weka.io/blocked-drives-virtual-uuids` |

**Which do I want?**

- A whole physical drive is bad, or you're not in drive-sharing mode at all → `serialIDs`.
- One VID on a shared drive faulted and other tenants are also carved from that physical →
  `virtualUUIDs`. Blocking the physical would evict every other tenant's VID on it too — see
  [Virtual Drive Replacement](virtual-drive-replacement.md) for the full procedure.
- The physical drive itself is bad (not just one VID on it) → `physicalUUIDs`, accepting that
  every VID on it goes, including other tenants'.

## Blocking

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: block-vid
  namespace: weka-operator-system
spec:
  action: block-drives
  payload:
    blockDrivesPayload:
      node: node1
      virtualUUIDs:
        - 31de939a-1111-2222-3333-444455556666
```

`serialIDs` and `physicalUUIDs` follow the same shape — the payload only inspects the list(s) you
set, so a single request can mix identifiers if the situation genuinely needs it.

```yaml
    blockDrivesPayload:
      node: node1
      serialIDs:
        - "233447E40E3C"
      physicalUUIDs:
        - fb05d910-aaaa-bbbb-cccc-ddddeeeeffff
```

## Per-identifier side effects

Serial and physical-UUID blocking are more disruptive than they look, because both also:

- Recompute the node's capacity / allocatable extended resources (`weka.io/drives`,
  `weka.io/shared-drives-capacity[-qlc]`) to exclude the newly-blocked drive.
- Delete the `weka.io/sign-drives-hash` node annotation, forcing a full drive re-scan on the next
  `sign-drives` run.

Virtual-UUID blocking does **neither**. A VID has no physical inventory of its own — the node's
physical drive count and capacity are unchanged by blocking one — so there is nothing to recompute
and nothing new to re-scan.

## Unblocking

Unblocking is a **required final step**, not optional cleanup — see
[Blocked lists are never cleaned automatically](#blocked-lists-are-never-cleaned-automatically)
below. The payload shape is identical, just under `unblock-drives`:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: unblock-vid
  namespace: weka-operator-system
spec:
  action: unblock-drives
  payload:
    blockDrivesPayload:
      node: node1
      virtualUUIDs:
        - 31de939a-1111-2222-3333-444455556666
```

Several identifiers of the same kind can be unblocked in one request — list them all under the
same field:

```yaml
    blockDrivesPayload:
      node: node1
      virtualUUIDs:
        - 31de939a-1111-2222-3333-444455556666
        - 7b3f82cd-7777-8888-9999-aaaabbbbcccc
```

## Reading the result

The operation's outcome lands in `status.status` (`Done` / `Failed`) and `status.result`, a small
JSON object:

```json
{"result": "Successfully blocked 1 drives on node node1"}
```

or, on failure:

```json
{"err": "the following drives were not found in the available drives list: [233447E40E3C]"}
```

Failure is **all-or-nothing per identifier kind**: if any entry in a list is unknown to the node
(not currently present for block, or not currently blocked for unblock), none of that list is
written and the CR lands `Failed`. Each kind of identifier is handled separately, so in a request
that mixes kinds one list can be applied while another is rejected; `status.result` then reports
both, and the first error is the one recorded in `err`. Prefer one identifier kind per request if
you want the simpler guarantee.

```bash
kubectl get wekamanualoperation block-vid -n weka-operator-system -o jsonpath='{.status.status} {.status.result}'
```

## Unsticking drive container deletion when a drive is gone

Deleting a drive container force-resigns every serial it was allocated. If one of them no longer
exists on the host as a block device (a failed NVMe whose controller still enumerates but exposes
no namespace, or a pulled disk), the resign fails without signing any drive, and deletion waits.
The drive container then shows a `ResignDrivesFailed` Warning event, and its
`ContainerDrivesResigned` condition message names the missing serials:

```
resign drives operation failed: drives not found on node node1: ['23164A21ECD1']; ...
```

Block those serials by `serialIDs`. Blocked serials are left out of the resign, so the next
attempt resigns the remaining drives and deletion continues; if every drive of the container is
blocked, the resign is skipped. Unblock the serial once the disk is replaced.
`overrides.skipDrivesForceResign` is not a substitute: it skips the resign of every drive in the
container.

This applies to the deletion flow only. A `force-resign-drives` WekaManualOperation resigns the
serials it was given, blocked or not; re-run it without the missing ones.

## Blocked lists are never cleaned automatically

A blocked identifier stays in its node annotation forever unless explicitly unblocked — the
operator never expires or garbage-collects these lists. For virtual UUIDs this is low-risk: a
retired VID can never be reissued, so a stale entry can never match a real drive again. It is
harmless but it does accumulate, and an unbounded blocked-virtual-uuids list is a sign that
unblocking has been skipped as a step. Always finish the procedure by unblocking once a
replacement is confirmed.

## Related documentation

- [Virtual Drive Replacement](virtual-drive-replacement.md) — the supported procedure for
  replacing a single faulted VID, built on top of `virtualUUIDs` blocking.
- [Drive Sharing](drive-sharing.md) — virtual drives, proxy mode, and capacity allocation.
- [Drive Signing](drive-signing.md) — how `weka.io/sign-drives-hash` and re-scanning work.
- [Clean Stale Virtual Drives](clean-stale-virtual-drives.md) — for a VID that is signed on the
  proxy but claimed by **no** container. That is a different problem from a faulted, claimed VID;
  do not reach for `block-drives` there.
