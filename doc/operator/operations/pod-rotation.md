# Pod Rotation (config-change pod replacement)

## Overview

A WekaContainer's pod is built from the container spec when it is created. When a pod-affecting field changes later (cores, hugepages, drives, traces, ...), the running pod keeps its old shape. The operator detects this per container, reports it, and, where pod rotation is enabled, replaces the pods one container at a time.

- Detection is always on. It sets `status.podOutdated` and emits one `PodOutdated` event naming what changed. It never replaces a pod by itself.
- Rotation is opt-in (default off). When enabled, the WekaCluster approves one outdated container at a time, after a cluster health check. The container replaces its pod and reports done only when Weka in the new pod is ready.
- Image upgrades are a separate flow ([upgrade.md](upgrade.md)); they run on the image alone and also clear `podOutdated`.

## Enabling rotation

Per cluster (wins when set):

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaCluster
spec:
  overrides:
    podRotation: true   # false forces event-only mode; unset follows the operator-wide default
```

Operator-wide default (`false`), through the configuration WekaPolicy:

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
spec:
  payload:
    configurationPayload:
      podRotation:
        enabled: true
```

## What counts as outdated

The container stamps the values of its pod-affecting fields on its pod (annotation `weka.io/pod-spec`, a JSON object) when it creates the pod, and compares them to the current spec on every reconcile.

| Tracked key | Source |
|---|---|
| `numCores`, `extraCores`, `hugepages`, `hugepagesOffset`, `numDrives`, `additionalMemory`, `dpdkBaseMemoryMb`, `resources`, `tracesConfiguration` | WekaContainer spec |
| `podConfigVersion` | helm `podConfigVersion` (always compared) |
| `podConfigCodeVersion` | operator code constant; compared only when helm `enablePodConfigCodeVersionRotation` is `true` |

Not tracked: image (own flow), scheduling fields (nodeSelector, tolerations, affinity), `joinIps`, labels and annotations, `imagePullSecret`, planner-managed capacity fields (`driveCapacity`, `containerCapacity`).

A decrease of an increase-only field (for example `numDrives`) does not change the container spec, so nothing is flagged. A key missing from an older stamp is skipped, so adding tracked keys in a later release does not flag existing pods.

## Scope

| Containers | Detect + event | Rotate |
|---|---|---|
| WekaCluster-owned drive, compute, data-services, S3, NFS, SMBW | yes | yes, when enabled |
| ssdproxy | yes | no. Delete the pod by hand, or use [rotate-ssdproxy](rotate-ssdproxy.md) for image changes |
| Clients, envoy, telemetry, other containers | no | no |

## Rotation flow

| Step | `status.podOutdated` | `spec.rotatePod` | Who |
|---|---|---|---|
| spec changes | `true` | `false` | container, event `PodOutdated: numCores 2→4` |
| approved | `true` | `true` | WekaCluster, after the health check |
| pod replaced | `true` | `true` | container deletes the pod, creates it with the new stamp |
| Weka ready in the new pod | `false` | `true` | container, after the readiness gates and settle window |
| done | `false` | `false` | WekaCluster clears `rotatePod`, picks the next |

- Order: drive, compute, data-services, S3/NFS/SMBW, then by name. Containers on nodes with no assignment, or marked for deletion or in deleting/destroying state, are skipped.
- Before each approval the cluster must be fully protected, with no data moving, status healthy, all drives active, and drive and compute container counts above the upgrade thresholds. Otherwise the cluster waits and emits `PodRotationWaiting`.
- At most one container is approved at a time. An image change waits for an in-flight rotation to finish.
- `spec.overrides.upgradePaused` stops new approvals. `spec.overrides.upgradeAllAtOnce` approves every outdated container together, with no health gate. Weka is down until the pods are back; use it on dev and test clusters only.
- `spec.rotatePod` is written by the WekaCluster only. Setting it by hand on a container that is not outdated does nothing.
- If the spec is reverted before the pod is deleted, no restart happens; `podOutdated` clears once the pod passes the readiness gates.
- Disabling rotation mid-roll lets the in-flight container finish and approves nothing new.

`kubectl get wekacontainer` shows the `Outdated` column.

## Rotating every pod by hand

Bump helm `podConfigVersion`. Every in-scope container becomes outdated; with rotation enabled they roll one at a time, otherwise they are only flagged. A bump no longer restarts ssdproxy pods.

## Stuck rotation

If the replacement pod never becomes ready, the rotation waits indefinitely and the container emits the failing readiness gate's event (for example `IoProcessesNotUp`).

- Stop further approvals: set `spec.overrides.podRotation: false` (or `spec.overrides.upgradePaused: true`) on the WekaCluster. The in-flight container keeps waiting.
- Release the stuck container, only after the step above: `kubectl patch wekacontainer <name> --type merge -p '{"spec":{"rotatePod":false}}'`. Clearing `rotatePod` alone does not help: the container is still `podOutdated` and first in order, so the next pass approves it again.
- `status.podOutdated` stays true until the current pod passes the readiness gates; to retry, delete the pod.

## Upgrading the operator

Pods created by an older operator have no `weka.io/pod-spec` stamp. On the first reconcile the container adopts them: it stamps the running pod with the current spec values, without a restart, and nothing is flagged.

Adoption trusts the spec. A pod that was already behind its spec before the upgrade (for example spec `numCores: 2`, pod built with 1) is stamped as current and never flagged. Delete that pod, or bump `podConfigVersion`, to bring it up to date.

An in-progress `podConfigVersion` roll ends at the upgrade: the remaining pods are adopted. Finish the roll first, or bump `podConfigVersion` again afterwards. An in-progress image roll continues.

## Removed

- helm `allowRotateNonAnnotatedPodConfigHash` (setting it has no effect)
- `WekaContainer.spec.podConfigHash` and the `lastAppliedPodConfigHash` status fields
- the `weka.io/pod-config-version` pod annotation (ignored on running pods)
- the `CapacityGrowthApplied` event, replaced by `PodOutdated` and `status.podOutdated`
