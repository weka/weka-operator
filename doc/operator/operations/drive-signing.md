### Sign Drives (backends only)

Drive containers will be scheduled on nodes that have available signed drives.

**Note:** This section covers **exclusive drive signing** for single-cluster deployments. For multi-cluster deployments where physical drives are shared between clusters, see [Drive Sharing](drive-sharing.md).

To scan nodes for drives that can be used for weka and sign them, apply following policy

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: sign-drives
  namespace: weka-operator-system # Replace with your namespace
spec:
  type: sign-drives
  payload:
    signDrivesPayload:
      type: "all-not-root"
```
It is also possible to sign drives using WekaManualOperation with signDrivesPayload
In both cases, manual operation and policy,  - spec.image should not be specified as this is not same image as weka containers
The only cases when spec.image might be specified - is when there is specific need to use different signing image, like local distribution or hotfix-version, in such cases it will be instructed specifically

When drives are signed they are propagated into node annotations and extended resources
Annotation: `weka.io/weka-full-drives` (legacy format: `weka.io/weka-drives: '["233447E40E3C","233447E40CFD","233447E40E5A","19231043BD02","23164A23D27F","233447E40D0F"]'`)
In drive-sharing (proxy) mode the result goes to `weka.io/weka-shared-drives` instead — see [Drive Sharing](drive-sharing.md).
Extended resource example: `weka.io/drives: "6"`
This way, multiple wekaclusters can share same nodes, as long as they use different set of drives, which they are not selecting themselves, but rather Weka Operator is selecting them based on the signed drives available on the node

### Re-running sign-drives and adding new drives

Whether a node is processed at all is gated by the `weka.io/sign-drives-hash` node annotation, **not** by the drive annotations themselves. The hash is a sha256 of the node's **BootID** only (`internal/pkg/domain/hashes.go`) and is written after every successful sign. Without `force`, a node whose stored hash matches the current BootID is skipped before any discovery container is created (`internal/controllers/operations/sign_drives.go`).

Consequence: **hot-adding a physical drive does not trigger signing on its own.** The BootID (and therefore the hash) is unchanged, so re-applying the sign-drives operation skips the node and never sees the new drive. To pick up a new drive, one of the following is required:

- run the operation with `force: true`, or
- delete the `weka.io/sign-drives-hash` annotation from the node, or
- reboot the node (BootID changes, so the hash no longer matches).

#### Incremental behavior: only new drives are signed

When the operation does run on a node that already has drive annotations, it is incremental and safe for existing drives:

- All serials already listed in `weka.io/weka-full-drives`, legacy `weka.io/weka-drives`, and `weka.io/weka-shared-drives` are passed to the signing container as `ExcludedSerialIds` — already-signed drives are never re-signed.
- The result is **merged** into the existing annotation by serial (existing entries are preserved, new drives are appended), not overwritten.
- Annotated drives that are no longer visible to the kernel are added to `weka.io/blocked-drives` (only when the discovery reports a complete kernel view). Excluded-but-present drives are still kernel-visible, so they are not affected.

The exclusion list is derived from the drive annotations, so deleting **only** `weka.io/sign-drives-hash` yields an incremental run (sign new drives, keep old ones). Deleting the drive annotations as well removes the exclusions and causes a full re-sign of every eligible drive on the node — a full reset.

### Drive type overrides (TLC/QLC, shared mode only)

In [drive-sharing](drive-sharing.md) (proxy) mode, each physical drive is classified as `TLC` or
`QLC`. By default the type is inferred from the drive's IU size (`iu_size_to_drive_type()` in
`charts/weka-operator/resources/weka_runtime.py`): `iu_size >= 16384` → `QLC`, otherwise `TLC`. This
value is stored per-drive in the `weka.io/weka-shared-drives` node annotation and drives two node
extended resources, `weka.io/shared-drives-capacity` (TLC) and `weka.io/shared-drives-capacity-qlc`,
which the allocator and capacity planner use when placing drive containers. Both exclude blocked
drives, and any drive whose recorded type is not exactly `QLC` counts as TLC.

The inferred type is sometimes wrong — IU size is missing on some firmware and defaults to 0, which
silently classifies the drive as TLC. Hand-editing `weka.io/weka-shared-drives` does not fix this
because the extended resources are derived separately and would not follow the edit.

`signDrivesPayload.driveTypeOverrides` lets you force the type for matching drives (full example:
[sign-shared-drives-type-override.yaml](../../examples/drive-sharing/sign-shared-drives-type-override.yaml)):

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaManualOperation
metadata:
  name: sign-shared-drives-override
  namespace: weka-operator-system
spec:
  action: sign-drives
  payload:
    signDrivesPayload:
      type: all-not-root
      nodeSelector:
        weka.io/role: backend
      shared: true
      driveTypeOverrides:
        rules:
          - model: "SAMSUNG MZWLO7T6HBLA-00A07"
            type: QLC
          - capacityGiB: 3500
            type: TLC
```

**Matching:** each rule must set `model`, `capacityGiB`, or both (the API rejects neither); rules are
evaluated in order and the **first match wins**.
- `model` is compared exactly, case-insensitively, whitespace-trimmed, against the model recorded in
  `weka.io/weka-shared-drives` — not against a live device query. Drives signed before that field
  existed have no model recorded, so model rules cannot match them until the node is re-signed. On
  such a node the "no match" reporting below is suppressed, since every model rule would look
  unmatched.
- `capacityGiB` is compared **exactly** against the capacity recorded in the same annotation. That
  value is truncated (`int(size_bytes / 1024**3)`), so drives of one model can differ by a few GiB —
  an exact-capacity rule is unforgiving of that variance.
- Both set ⇒ AND. Useful when one SKU ships in several capacities and only some of them are QLC.
- A rule matching zero drives emits one `DriveTypeOverrideNoMatch` Warning event for that rule,
  aggregated across nodes. A rule that is shadowed on every drive by an earlier rule counts as
  matched and is **not** reported.
- `DriveTypeOverrideNoMatch` re-fires on **every evaluated pass**, including passes that write
  nothing — on a recurring `WekaPolicy` its count keeps climbing every interval while the rule stays
  dead, so a rising count means "still dead", not "failed again". `DriveTypeOverridesApplied` /
  `DriveTypeOverridesCleared` fire only when the rule set actually **changes** and the write lands —
  once per change, not once per pass.

**Scope:** shared (proxy) mode only — exclusive-drive mode has no TLC/QLC concept and the API rejects
`driveTypeOverrides` unless `shared: true`. A node not signed yet has no drives to rewrite, so it just
stores the rules; its first sign already publishes the overridden types. Such nodes are reported by
`DriveTypeOverridesPersisted` rather than `DriveTypeOverridesApplied`, because no drive was re-typed
in that pass — on a first-ever sign, where every selected node is unsigned, `Persisted` is the only
override Event you will see even though the types are forced as the drives are signed. `rules: []`
clears such a node too, and does so without an Event: there is no forced type yet to report undoing.

**Persistence:** rules are stored per-node in `weka.io/drive-type-overrides` and re-applied on every
later sign-drives run against the full merged drive set, not just newly-discovered drives.
**Omitting** `driveTypeOverrides` keeps whatever is already persisted on the node; clearing requires
an explicit `driveTypeOverrides: {rules: []}`. This holds for `WekaPolicy` too — dropping the field
from a recurring policy spec does not remove the rules from nodes.

**Effect is immediate:** any change to the rule set — new rules, narrowing, or `rules: []` — is
written to the node right away: the operator rewrites `weka.io/drive-type-overrides` and both
extended resources without waiting for a pod run, emits `DriveTypeOverridesApplied` /
`DriveTypeOverridesCleared`, clears `weka.io/sign-drives-hash` so the node re-signs on the next pass,
and parks the operation for one reconcile cycle to let the node cache observe the write. That re-sign
is what actually reverts drives when rules are narrowed or cleared: an override overwrites the
IU-derived type in place and the base value is not kept anywhere else, so the annotation rewrite
alone cannot restore it.

**Verification:** the Events are the record of what each pass did — `status.result` carries no
override summary (a status field can only describe the last writing pass, so it under-reports when
the overrides land across more than one reconcile). The event counts below are not comparable —
see the cadence note in the matching-rules bullets above.
```bash
kubectl get events --field-selector reason=DriveTypeOverridesApplied   --sort-by=.lastTimestamp
kubectl get events --field-selector reason=DriveTypeOverridesPersisted --sort-by=.lastTimestamp # not-yet-signed nodes
kubectl get events --field-selector reason=DriveTypeOverrideNoMatch    --sort-by=.lastTimestamp # dead rules
kubectl get events --field-selector reason=DriveTypeOverrideFailed     --sort-by=.lastTimestamp # write failures
kubectl get node <node> -o jsonpath='{.metadata.annotations.weka\.io/drive-type-overrides}' | jq .
kubectl get node <node> -o jsonpath='{.metadata.annotations.weka\.io/weka-shared-drives}' | jq .
kubectl get node <node> -o jsonpath='{.status.allocatable}' | jq '."weka.io/shared-drives-capacity", ."weka.io/shared-drives-capacity-qlc"'
```

A failed write does **not** fail the operation — one unreachable or misannotated node must not stall
the whole selector, so the operation keeps retrying and stays `Running`. `DriveTypeOverrideFailed` is
therefore the only place the reason appears: an operation stuck in `Running` with no `Applied` event
is what this event explains. A pass can also emit both, when the write succeeds on some nodes and
fails on others.

**Caveat — apply before drives are claimed:** `VirtualDrive.Type` is recorded at virtual-drive
allocation time and determines which weka pool the virtual drive joins (`iubig` for QLC, `iu4k`
otherwise). Changing a physical drive's type does **not** retroactively change virtual drives already
carved from it, so overriding a claimed drive leaves the node's per-type allocatable capacity
disagreeing with the pools of already-running containers until those containers are reallocated. The
per-drive warning naming each changed drive is logged only on the deferred re-sign, not on the
immediate annotation rewrite.
