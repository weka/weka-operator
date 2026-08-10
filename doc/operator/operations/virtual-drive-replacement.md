# Virtual Drive Replacement

On drive-sharing (composable) deployments, a physical NVMe is carved into several virtual drives
(VIDs), and VIDs on one physical can belong to different clusters. This page is the supported
procedure for replacing a single faulted VID without disturbing anything else — the neighbouring
VIDs on the same physical, and any other tenant sharing it.

## Why not just remove the drive by hand

Removing a VID by hand — from the cluster and from the `ssdproxy` — does not stick. It comes back
within about a minute.

The operator's desired state for VIDs is the owning drive container's allocation record
(`wekacontainer.status.allocations.virtualDrives[]`), which stores the concrete virtual UUID. Two
periodic loops enforce it: one re-signs onto the proxy any allocated VID that's missing there, the
other re-adds to the cluster any allocated VID that's missing from it. Because the UUID is a stored
value rather than something derived from live state, the *identical* VID comes back — nothing ever
reconciles the record against reality.

The record has to be changed instead of the drive. Blocking by `virtualUUIDs` is what changes it —
see [Block Drives](block-drives.md) for the full reference on the identifier and its payload.

## Case 1: a single faulted VID

Identify the faulted virtual UUID (from cluster drive status, or a `VirtualDriveInactive`-style
event/alert), then block it:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: replace-vid
  namespace: weka-operator-system
spec:
  action: block-drives
  payload:
    blockDrivesPayload:
      node: node1
      virtualUUIDs:
        - 31de939a-1111-2222-3333-444455556666
```

What happens next, over the following reconcile passes:

1. The block is recorded against the node.
2. The faulted VID is deactivated and removed from the cluster (`VirtualDriveDeactivated`,
   `VirtualDriveRemoved` events on the container).
3. The VID is erased from the physical drive via the proxy (`VirtualDriveErased`).
4. The VID's entry is dropped from the container's allocation record — the step that makes the
   removal stick.
5. The container is now below its target capacity, so the allocator carves a replacement wherever
   there is room, with a **new** virtual UUID, same size and type as the one removed.
6. The replacement is signed and added to the cluster.

The add steps run before the remove steps within a single reconcile pass, but both enforcement loops
skip virtual UUIDs that appear in the node's blocked list, so a blocked VID is never re-signed or
re-added while its removal is in progress. If the proxy is unreachable the removal simply retries
until it returns, leaving the VID in the cluster in the meantime rather than erasing it on disk and
orphaning it.

Verify the container returned to its target capacity, then unblock the old virtual UUID — this is
a **required final step**, not optional cleanup:

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

## Case 2: a physical drive with multiple faulted VIDs

If the physical drive itself is genuinely bad — not just one VID on it — block by `physicalUUIDs`
instead:

```yaml
    blockDrivesPayload:
      node: node1
      physicalUUIDs:
        - fb05d910-aaaa-bbbb-cccc-ddddeeeeffff
```

**Warning:** this evicts **every** VID on that physical drive, across **every tenant** sharing it —
not just the faulted ones. Only use `physicalUUIDs` when the fault is at the physical-drive level,
not the VID level. For a single faulted VID on a physical that other tenants also use, use
`virtualUUIDs` (Case 1) so those other tenants are untouched.

## Warning: automatic scaling must be enabled for a replacement to appear

Whether step 5 above (carving a replacement) happens at all is gated by the operator config flag
`ENABLE_DYNAMIC_DRIVE_SCALING_FOR_SHARED_DRIVES` (Helm
`driveSharing.enableDynamicDriveScalingForSharedDrives`), which **defaults to `false`**.

> **With the flag `false` (the default): blocking a VID removes the drive and no replacement is
> ever created.** You get the `VirtualDriveReplacementDisabled` warning event and a container
> sitting permanently below its target capacity — not a transient state that clears on its own.

If you intend to use this procedure, confirm the flag is `true` for the affected cluster's nodes
before blocking, or be prepared to size the replacement drive in some other way. This is the most
likely source of a "I blocked the VID and nothing came back" support case — check for
`VirtualDriveReplacementDisabled` first.

## Where the replacement lands

Not guaranteed: the allocator picks freely and may reuse the same physical drive the faulted VID
came from. That is expected behavior, not a bug. If the replacement faults again on that same
physical, the physical is genuinely bad — move to Case 2 and block the physical drive.

## Manual removal is not a shortcut

Do not attempt to remove a VID by editing the allocation record directly, or by acting only on the
cluster or only on the proxy. The two reconcile loops described above re-enforce the recorded VID
independently of each other and within about a minute of each other, so a manual, partial removal
just gets undone. Blocking by `virtualUUIDs` is the only path that changes the allocation record
itself, which is the only thing that makes a removal stick.

## Related documentation

- [Block Drives](block-drives.md) — the `block-drives` / `unblock-drives` reference: all three
  identifiers, side effects, and result format.
- [Drive Sharing](drive-sharing.md) — virtual drives, proxy mode, and capacity allocation.
- [Clean Stale Virtual Drives](clean-stale-virtual-drives.md) — for a VID signed on the proxy but
  claimed by no container. A different problem from a faulted, claimed VID.
