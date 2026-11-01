# Scaling Up Drives and Cores

## Overview

This page covers raising `numDrives`, `driveCores` or `computeCores` on a running WekaCluster whose container counts are set explicitly (`driveContainers`, `computeContainers`) and that uses exclusive full drives. Other sizing modes grow differently: see [cluster-capacity.md](../deployment/cluster-capacity.md), [drive-sharing.md](drive-sharing.md) and [act-as-daemonset.md](../deployment/act-as-daemonset.md).

- The operator writes the new values to every drive or compute WekaContainer within seconds. A running pod keeps its old cores, hugepages, memory and drive count until it is recreated.
- A new drive is reserved at once, but is added to Weka only after its drive pod is recreated.
- Pods are recreated by [pod rotation](pod-rotation.md), one at a time after a cluster health check, or by deleting them by hand.
- These fields are increase-only. Admission rejects a cores decrease; a `numDrives` decrease is accepted and ignored. Check the target before you edit.

## What each raise changes

Per container, for each step of +1:

| Raise | Pods recreated | Pod resources |
|---|---|---|
| `numDrives` | drive | `weka.io/drives` +1, hugepages +200 MiB, memory +700 MiB |
| `driveCores` | drive | CPU +2, hugepages +1464 MiB, memory +3000 MiB |
| `computeCores` | compute | CPU +2, memory +5200 MiB, hugepages recalculated (below) |

- The hugepages figures are the operator's defaults. An explicit `driveHugepages` or `computeHugepages` in `dynamicTemplate` is used as given and does not grow with the raise.
- CPU is +2 per core on hyperthreaded nodes and +1 on nodes without hyperthreading (`cpuPolicy: auto`, see [cpu-policy.md](../concepts/cpu-policy.md)).
- A `computeCores` raise recalculates compute hugepages from the cluster's current raw drive capacity: about 1 MiB per GiB of raw capacity, split across the compute containers, plus 1700 MiB per core ([details](../../dev/compute-hugepages.md)). After drives were added, the jump can be large: 1→2 compute cores after `numDrives` 1→3 took compute hugepages from 16414 MiB to 47480 MiB.
- A `numDrives` raise does not change compute containers; compute pods are not recreated.
- Several fields can change in one edit. The compute part of a combined `computeCores` + `numDrives` edit waits until a drive container has reserved its new drive.

## Before you start

1. The cluster is healthy: `weka status` shows `OK`, fully protected, all drives active. Rotation waits for this before each pod.
2. Every node that runs a drive container has at least the new `numDrives` signed, non-blocked drives. Admission (`cluster_signed_drives`, an error in strict mode and a warning in relaxed mode) checks only the cluster-wide total, so a single short node passes it. The `weka.io/drives` allocatable of a node is its signed, non-blocked drive count:

   ```bash
   kubectl get nodes -o custom-columns='NAME:.metadata.name,DRIVES:.status.allocatable.weka\.io/drives,HUGEPAGES:.status.allocatable.hugepages-2Mi,CPU:.status.allocatable.cpu'
   ```

   To sign more drives on a node, see [drive-signing.md](drive-signing.md#re-running-sign-drives-and-adding-new-drives).
3. Every node has free CPU, memory and hugepages for the increase in the table above. `weka-capacity explore-nodes` shows the free figures per node, net of every WekaContainer ([weka-capacity.md](../cli-tools/weka-capacity.md#explore-nodes)).

## Raising the values

Edit `spec.dynamicTemplate` on the WekaCluster, for example:

```bash
kubectl patch wekacluster CLUSTER_NAME -n NAMESPACE --type merge \
  -p '{"spec":{"dynamicTemplate":{"numDrives":2}}}'
```

Rejected and ignored edits:

```
Error from server (Forbidden): admission webhook "validate.wekacluster.weka.io" denied the request: spec.dynamicTemplate.driveCores: Forbidden: decreasing driveCores from 2 to 1 is not allowed; reducing cores can destabilize a running cluster
```

A `numDrives` decrease is admitted, but the containers keep their current `numDrives`.

### Reviewing before rotation

Unless rotation is already enabled, no pod restarts after the edit. Check the new container values, then recreate the pods:

```bash
kubectl get wekacontainer -n NAMESPACE -l weka.io/cluster-name=CLUSTER_NAME \
  -o custom-columns=NAME:.metadata.name,MODE:.spec.mode,CORES:.spec.numCores,DRIVES:.spec.numDrives,HUGEPAGES:.spec.hugepages,OUTDATED:.status.podOutdated
```

## What you see after the edit

| When | What happens |
|---|---|
| Within seconds | The drive or compute WekaContainers get the new cores, `numDrives` and hugepages. |
| At once, on a `numDrives` raise | Each drive container reserves the new drive. Until its pod is recreated, it reports status `DrivesAdding` and, every minute, a `DriveCapacityResourceShortfall` warning with a matching `DrivesAddingError`: `deferring drive add: pod requests 1 drives, allocated 2; recreate the pod`. |
| Within about 3 minutes | Each affected container sets `status.podOutdated` (the `Outdated` column) and emits `PodOutdated`, for example `pod does not match the operator's pod: limits.hugepages-2Mi 1664Mi→1864Mi, limits.memory 11700Mi→12400Mi, limits.weka.io/drives 1→2, ...`. |
| Until the pods are recreated | Nothing else changes. Weka keeps the old drive count and cores. |

## Recreating the pods

### With pod rotation

Enable rotation on the cluster (see [pod-rotation.md](pod-rotation.md#enabling-rotation) for the operator-wide setting):

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
spec:
  overrides:
    podRotation: true
```

- The cluster recreates one outdated pod at a time: drive pods first, then compute ([order](pod-rotation.md#rotation-flow)).
- For each drive pod: one `DrivesAddingError` `Failed connecting to https://...:15000/api/v1. Make sure weka is running on this host` while Weka starts in the new pod, then `DriveAdded`.
- While Weka moves data onto the new drive, the cluster waits and emits `PodRotationWaiting`: `pod rotation waiting for cluster health: CLUSTER_NAME: rebuild is moving data`.
- On a 6 + 6 container test cluster, each pod took about 1.5 minutes; raising both core counts and `numDrives` in one edit rolled all 12 pods in about 20 minutes.

Leave rotation on, or set the field back to its previous value when the roll finishes.

### By hand

Delete one outdated pod at a time, drive pods first. The pod has the WekaContainer's name.

```bash
kubectl get wekacontainer -n NAMESPACE -l weka.io/cluster-name=CLUSTER_NAME \
  -o custom-columns=NAME:.metadata.name,MODE:.spec.mode,OUTDATED:.status.podOutdated
kubectl delete pod -n NAMESPACE CONTAINER_NAME
```

Before deleting the next pod, wait until the container's `Outdated` clears and `weka status` shows `OK` with all drives active and no data moving. The new pod starts in about 30 seconds and its drive is added about a minute after the delete.

## Verifying

- `kubectl get wekacontainer` shows no container with `Outdated` `true` (the column is empty when the pod matches).
- `weka status` shows `OK` with `driveContainers × numDrives` drives, all active. The WekaCluster `DRVS(A/C/D)` column can lag behind Weka.
- `weka local resources` inside a recreated pod shows the new core count.

## Known limitations

- A drive node with fewer spare signed drives than the increase: its container emits `AllocateContainerDrivesError` `not enough drives available: need 2, have 1` and keeps its current drives, but its pod is still flagged with the new `weka.io/drives` count. A recreated pod requests more drives than the node has and cannot be scheduled; rotation waits on it. Sign more drives on that node ([drive-signing.md](drive-signing.md)), or stop rotation before that pod is reached ([Stuck rotation](pod-rotation.md#stuck-rotation)). `numDrives` cannot be lowered back.
- Without a spare signed drive on any drive node, the compute part of a combined `computeCores` + `numDrives` edit never applies.
- `DriveCapacityResourceShortfall` and `DrivesAddingError` repeat every minute until each drive pod is recreated. They are expected and stop on their own.

## Related documentation

- [pod-rotation.md](pod-rotation.md): rotation flow, health check, pausing and releasing a stuck rotation
- [cluster-provisioning.md](../deployment/cluster-provisioning.md): `dynamicTemplate` fields
- [admission-control.md](admission-control.md): `cluster_signed_drives` and other policies
- [drive-signing.md](drive-signing.md): signing new drives on a node
