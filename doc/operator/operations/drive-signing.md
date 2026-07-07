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