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

Operator-wide default (`false`), through the configuration WekaPolicy in the operator namespace. The operator accepts at most one: if one already exists (for example for CSI or drivers settings), add `podRotation` to it instead of creating another.

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: weka-operator-config
  namespace: weka-operator-system
spec:
  payload:
    configurationPayload:
      podRotation:
        enabled: true
```

Clusters without the override don't rotate while that configuration can't be resolved (for example, two configuration WekaPolicies); the operator logs a warning.

## What counts as outdated

The container rebuilds the pod the operator would create now, from its current spec, the helm values and the node's info, and compares it with the running pod. Only values the operator writes are compared; everything else on the running pod is ignored.

Compared:

- env vars the operator writes (`CORES`, `MEMORY`, network and CPU-policy settings, ...); a var missing on either side is a difference
- the operator's volumes and mounts, including `additionalSecrets`, except `node-info`
- container resources (cpu, memory, hugepages-2Mi, drives, NICs, NUMA regions, ephemeral-storage requests) and DRA resource claims
- the `otel-packages-installer` init container: its presence, command, cpu and memory resources and mounts, not its image
- `hostNetwork`, `serviceAccountName`, `dnsPolicy`, the container command, `privileged`
- annotation `weka.io/pod-config-hash`, the manual rotate knob below

Not compared:

| Value | Why |
|---|---|
| image | own flow ([upgrade.md](upgrade.md)) |
| `otel-packages-installer` image | admission can pin it to a digest on every create, which would loop rotation; a helm change is picked up at the next pod restart |
| `JOIN_IPS` | the expected pod gets the cluster's current join IPs, which change as backends join or leave; a running container is already connected |
| `PORT`, `WEKA_PORT`, `AGENT_PORT` | 0 on the first pod, allocated later |
| `OTEL_SERVICE_VERSION` | operator version; would flag every pod on every upgrade |
| `DIST_SERVICE`, `DRIVERS_BUILD_ID` | a backend pod never reads them; the drivers-loader gets the current value from the container spec |
| scheduling (affinity, tolerations, topology spread, priority class), labels, other annotations | placement and metadata |
| pod `securityContext`, `imagePullPolicy`, `terminationGracePeriodSeconds` | SCC and admission webhooks rewrite them |
| capacity planner resources (`weka.io/shared-drives-capacity`, `-qlc`) | grown live |
| `extraVolumes`, `extraVolumeMounts` | a change needs a manual pod delete |
| `imagePullSecrets` | matter only at image pull time |
| `node-info` volume and mount, `PRE_RUN_SCRIPT`, `hostPID` | change only by a direct WekaContainer edit |

`weka.io/pod-config-hash` is the manual "rotate everything" knob: a hash of helm `podConfigVersion`, plus the operator's `PodConfigCodeVersion` when helm `enablePodConfigCodeVersionRotation` is `true`.

The check runs every 3 minutes per container, on containers bound to a node. While a matching pod is not ready yet (after a rotation), the check that clears the flag repeats on every reconcile. If the pod cannot be rebuilt (for example node info is unavailable), the failure is logged and the pod is not flagged.

When the pod is flagged, the `PodOutdated` event lists the differing pod values, for example `pod does not match the operator's pod: CORES 2→4, requests.memory 9800Mi→10312Mi`. If the kubelet's full-pcpus-only setting cannot be read, the cpu resource is not compared, unless helm `fullPcpusOnly` is `true`.

A decrease of an increase-only field (for example `numDrives`) does not change the container spec, so nothing is flagged.

## Scope

| Containers | Detect + event | Rotate |
|---|---|---|
| WekaCluster-owned drive, compute, data-services, S3, NFS, SMBW | yes | yes, when enabled |
| ssdproxy | yes | no. Delete the pod by hand, or use [rotate-ssdproxy](rotate-ssdproxy.md) for image changes |
| Clients, envoy, telemetry, other containers | no | no |

## Rotation flow

| Step | `status.podOutdated` | `spec.rotatePod` | Who |
|---|---|---|---|
| spec changes | `true` | `false` | container, event `PodOutdated: pod does not match the operator's pod: CORES 2→4` |
| approved | `true` | `true` | WekaCluster, after the health check |
| pod replaced | `true` | `true` | container deletes the pod, creates it from the current spec |
| Weka ready in the new pod | `false` | `true` | container, after the readiness gates and settle window |
| done | `false` | `false` | WekaCluster clears `rotatePod`, picks the next |

- Order: drive, compute, data-services, S3/NFS/SMBW, then by name. Containers on nodes with no assignment, or marked for deletion or in deleting/destroying state, are skipped.
- Before each approval the cluster must be fully protected, with no data moving, status healthy, all drives active, and drive and compute container counts above the upgrade thresholds. Otherwise the cluster waits and emits `PodRotationWaiting`.
- At most one container is approved at a time. An image change waits for an in-flight rotation to finish.
- `spec.overrides.upgradePaused` stops new approvals. `spec.overrides.upgradeAllAtOnce` approves every outdated container together, with no health gate. Weka is down until the pods are back; use it on dev and test clusters only.
- `spec.rotatePod` is written by the WekaCluster only. Setting it by hand on a container that is not outdated does nothing.
- If the spec is reverted before the pod is deleted, no restart happens; `podOutdated` clears once the pod matches again and passes the readiness gates.
- Disabling rotation mid-roll lets the in-flight container finish and approves nothing new.

`kubectl get wekacontainer` shows the `Outdated` column.

## Rotating every pod by hand

Bump helm `podConfigVersion`; it changes the expected `weka.io/pod-config-hash`. Every in-scope container becomes outdated; with rotation enabled they roll one at a time, otherwise they are only flagged. A bump no longer restarts ssdproxy pods.

## Stuck rotation

If the replacement pod never becomes ready, the rotation waits indefinitely and the container emits the failing readiness gate's event (for example `IoProcessesNotUp`).

- Stop further approvals: set `spec.overrides.podRotation: false` (or `spec.overrides.upgradePaused: true`) on the WekaCluster. The in-flight container keeps waiting.
- Release the stuck container, only after the step above: `kubectl patch wekacontainer <name> --type merge -p '{"spec":{"rotatePod":false}}'`. Clearing `rotatePod` alone does not help: the container is still `podOutdated` and first in order, so the next pass approves it again.
- `status.podOutdated` stays true until the current pod matches the operator's pod and passes the readiness gates; to retry, delete the pod.

## Known limitations

- A pod that is already broken (crash-looping, not joining) is flagged when the spec is fixed, but the cluster health check blocks approval while the cluster is unhealthy. Delete the pod by hand.
- Adding a PVC (`globalPVC`, `localDataPvc`) flags running backends; rotation restarts them onto the PVC.
- An admission webhook that rewrites a compared value makes the rotated pod differ again. The approved container then deletes and recreates its pod in a loop, with no health check on it; `podOutdated` never clears, no other container is approved, and image upgrades wait. Stop the loop as in [Stuck rotation](#stuck-rotation); the fix is to exclude the value from the compare in the operator.

## Upgrading the operator

Pods built by an older operator have no `weka.io/pod-config-hash` annotation. The container adds it to the running pod, without a restart, and does not flag the pod for that.

The new operator then compares the pod as usual: values it renders differently from the older one (for example `MEMORY`, `hugepages-2Mi`) are flagged at the first check. An operator release that renders pods differently flags every backend and, with rotation on, rolls them all, one at a time.

An in-progress `podConfigVersion` roll ends at the upgrade: the remaining pods get the current hash. Bump `podConfigVersion` again afterwards. An in-progress image roll continues.

## For developers

- Changing a formula or constant behind a compared value flags existing pods on the next operator release; add a release note.
- Removing an env name from `pod.go` (the unit tests then make you drop it from the compared list), dropping a volume name from `ReservedVolumeNames`, or changing `weka_runtime.py` is invisible to the compare: bump `consts.PodConfigCodeVersion`. A volume or mount removed from `pod.go` while its name stays reserved is still compared, so every existing pod is flagged at the next release. The bump takes effect only where helm `enablePodConfigCodeVersionRotation` is `true`; toggling that flag itself changes every pod's expected hash and flags every pod.
- A new pod field, env name, resource name or init container must be classified: compared in `resources/pod_compare.go`, or excluded with a reason in `resources/pod_compare_test.go`; the unit tests there fail otherwise.

## Removed

- helm `allowRotateNonAnnotatedPodConfigHash` (setting it has no effect)
- `WekaContainer.spec.podConfigHash` and the `lastAppliedPodConfigHash` status fields
- the `weka.io/pod-config-version` pod annotation (replaced by `weka.io/pod-config-hash`)
- the `CapacityGrowthApplied` event, replaced by `PodOutdated` and `status.podOutdated`
