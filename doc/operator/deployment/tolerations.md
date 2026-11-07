# Tolerations and taints

Which tolerations the operator puts on each pod it creates, every setting that changes them, and how
the operator reacts when a node gets a taint. Read this when pods do not land on (or do not leave) tainted,
cordoned or NotReady nodes.

## Quick answers

| I want… | Set |
|---|---|
| Clients (and their drivers, discovery, client adhoc ops) to run on every node they select, whatever the taints | configuration `WekaPolicy`: `scheduling.clientsTolerateAll: true` |
| Backends to run on nodes with my taint `example.com/gpu` | `WekaCluster.spec.tolerations: ["example.com/gpu"]` |
| Clients to run on nodes with my taint | `WekaClient.spec.tolerations: ["<key>"]`, or `clientsTolerateAll` |
| Drivers builder/dist to run on tainted nodes | `tolerations` of the `enable-local-drivers-distribution` `WekaPolicy` |
| The operator itself on tainted nodes | helm `tolerations` (or `manager.tolerations`) |
| Pods to be evicted from a NotReady node instead of staying | helm `skipUnhealthyToleration: true` |
| Containers deleted when their node gets a taint they don't tolerate | helm `cleanupContainersOnTolerationsMismatch: true` |

## How Kubernetes uses taints

The scheduler never checks node health, only taints. Each taint has an effect:

```
effect        new pods      running pods
NoSchedule    not placed    stay
NoExecute     not placed    evicted
```

```
node cordoned  →  node.kubernetes.io/unschedulable          NoSchedule
node NotReady  →  node.kubernetes.io/not-ready|unreachable  NoSchedule + NoExecute
```

A toleration with a `key` matches only that key. A toleration **without a key** (`operator: Exists`)
matches every key: `{operator: Exists, effect: NoSchedule}` tolerates every NoSchedule taint, cordon and
NotReady included, and `{operator: Exists}` tolerates everything. Once a NotReady node's NoSchedule taint
is tolerated, the scheduler treats it like a healthy node.

## Where a pod's tolerations come from

Every WekaContainer pod is built from layers, in this order:

```
1. base set               every WekaContainer pod
2. NoSchedule expansion   client and aux modes
3. cordon toleration      client modes, if the policy allows it
4. operator: Exists       client modes, with clientsTolerateAll
5. your tolerations       from the WekaCluster / WekaClient / WekaPolicy
```

**1. Base set**, on every WekaContainer pod:

| Key | Effect | Why |
|---|---|---|
| `weka.io/shutdown-node` | NoExecute | keep running while the operator drains a node |
| `node.kubernetes.io/not-ready` | NoExecute | stay on a NotReady node (not evicted after 5 min) |
| `node.kubernetes.io/unreachable` | NoExecute | same, for an unreachable node |
| `node.kubernetes.io/network-unavailable` | NoExecute | same |
| `node.kubernetes.io/disk-pressure`, `cpu-pressure`, `memory-pressure` | NoSchedule + NoExecute | node pressure does not stop WEKA |

The three NotReady entries are left out with helm `skipUnhealthyToleration: true`. They are NoExecute
only, so a running pod survives a node outage but no new pod is placed on a NotReady node.

**2. NoSchedule expansion**: keyless `{operator: Exists, effect: NoSchedule}` and
`{operator: Exists, effect: PreferNoSchedule}`. Tolerates every NoSchedule taint. Added to:
- `client`, unless helm `skipClientNoScheduleToleration: true`
- `drivers-loader`, `discovery`, `adhoc-op`, `adhoc-op-with-container`, unless helm
  `skipAuxNoScheduleToleration: true`

Not added to backends, `drivers-builder` or `drivers-dist`.

**3. Cordon toleration** `node.kubernetes.io/unschedulable:NoSchedule`, added when the configuration
policy sets `clientsTolerateAll` or the deprecated `tolerateCordon`, for `client`, `drivers-loader`,
`drivers-dist`, `drivers-builder`, `discovery`, `adhoc-op` and `adhoc-op-with-container`. Cordon is
special: a cordon toleration you write yourself is removed, and the keyless entries of layer 2 do not count
as one for the operator's own placement decisions. Backends are never placed on a cordoned node. The adhoc
ops that run on backends (sign drives, force-resign drives, discover drives, ssdproxy kernelize) get it only
from the deprecated `tolerateCordon`, not from `clientsTolerateAll`.

**4. `operator: Exists`**, added with `clientsTolerateAll` to the same modes as layer 3, except the
adhoc ops that run on backends (sign drives, force-resign drives, discover drives, ssdproxy kernelize).
`drivers-loader` and `discovery` pods are per-node helpers, so they get it on backend nodes too; the backend
containers themselves are untouched.

**5. Your tolerations**, copied from the owner:
- `tolerations: ["key"]` (WekaCluster, WekaClient) becomes `{key, Exists, NoSchedule}` and
  `{key, Exists, NoExecute}`
- `rawTolerations` (WekaCluster, WekaClient) and `tolerations` (WekaPolicy, WekaManualOperation) are copied
  as written

## Tolerations per pod

```
                              base  NoSchedule  cordon*  Exists*  your tolerations from
drive, compute, s3, nfs,…      ✔        –         –        –      WekaCluster
ssdproxy                       ✔        –         –        –      WekaCluster
client                         ✔        ✔         ✔        ✔      WekaClient
drivers-loader                 ✔        ✔         ✔        ✔      the container it loads for
discovery                      ✔        ✔         ✔        ✔      the container that needed it
drivers-builder, drivers-dist  ✔        –         ✔        ✔      drivers WekaPolicy
adhoc: umount                  ✔        ✔         ✔        ✔      + always operator: Exists
adhoc: ensure-nics, feat-flags ✔        ✔         ✔        ✔      WekaPolicy / cluster / container
adhoc: sign/resign/discover
       drives, kernelize       ✔        ✔         ✔        –      WekaPolicy / WekaCluster

* only with clientsTolerateAll (cordon also with tolerateCordon)
```

Pods that are not WekaContainers:

| Pod | Tolerations |
|---|---|
| Operator | helm `manager.tolerations`, else `tolerations` (default none) |
| Capacity planner | helm `capacityPlanner.tolerations` (default none) |
| node-agent DaemonSet | configuration policy `nodeAgent.tolerations`, default `[{operator: Exists}]` |
| CSI node DaemonSet | `operator: Exists` + `WekaClient.spec.csiConfig.advanced.nodeTolerations` |
| CSI controller | WekaClient `tolerations`/`rawTolerations` + `csiConfig.advanced.controllerTolerations` |
| Cleanup job (on container delete) | the container's own tolerations + NoSchedule expansion (unless `skipAuxNoScheduleToleration`) + `operator: Exists`, first in the list, for every mode `clientsTolerateAll` covers |
| Image pre-pull | WekaCluster or WekaClient tolerations; for clients also the cordon/`Exists` from the policy |
| Monitoring, management proxy | WekaCluster tolerations |

## Settings reference

**Custom resources**

| Field | Applies to |
|---|---|
| `WekaCluster.spec.tolerations` / `rawTolerations` | all backend pods, ssdproxy, pre-pull, monitoring, management proxy |
| `WekaClient.spec.tolerations` / `rawTolerations` | client pods, their drivers-loader/discovery, CSI controller, pre-pull |
| `WekaClient.spec.csiConfig.advanced.nodeTolerations` / `controllerTolerations` | CSI node / controller, added to the defaults |
| `WekaPolicy.spec.tolerations` | the policy's pods: drivers-builder/dist, sign/discover drives, ensure-nics, traces |
| `WekaManualOperation.spec.tolerations` | the operation's pods |
| configuration `WekaPolicy` `scheduling.clientsTolerateAll` | layers 3 and 4 above; WekaClients create clients on every selected node |
| configuration `WekaPolicy` `scheduling.tolerateCordon` | **deprecated**, layer 3 only |
| configuration `WekaPolicy` `nodeAgent.tolerations` | node-agent DaemonSet, replaces the default |

**Helm values**

| Value | Default | Effect |
|---|---|---|
| `tolerations`, `manager.tolerations` | `[]` | operator pod |
| `capacityPlanner.tolerations` | `[]` | capacity planner pod |
| `skipUnhealthyToleration` | `false` | `true` drops the not-ready / unreachable / network-unavailable NoExecute tolerations from every WekaContainer pod, except pods that get `clientsTolerateAll`'s `operator: Exists`, which tolerates them regardless |
| `skipClientNoScheduleToleration` | `false` | `true` drops the NoSchedule expansion from client pods |
| `skipAuxNoScheduleToleration` | `false` | `true` drops it from drivers-loader, discovery, adhoc ops and the cleanup job |
| `skipClientsTolerationValidation` | `true` | `true`: WekaClients create clients on every node their `nodeSelector` matches, ignoring taints and cordon. `false`: only on nodes the client tolerates and that are not cordoned (unless the policy allows the cordon) |
| `cleanupContainersOnTolerationsMismatch` | `false` | `true` deletes a running container whose node gets a taint it does not tolerate (see below) |
| `tolerationsMismatchSettings.enableIgnoredTaints` | `true` | use `ignoredTaints` in that check |
| `tolerationsMismatchSettings.ignoredTaints` | `unschedulable`, `not-ready`, `unreachable` | taint keys that never cause that deletion |

## When a node gets a new taint

```
taint added to a node with a running WEKA pod
│
├─ Kubernetes
│    NoSchedule  → pod keeps running
│    NoExecute   → pod evicted unless tolerated; the operator recreates it, and it stays
│                  Pending until the taint goes away (Pending pods are deleted after
│                  deleteUnschedulablePodsAfter)
│
└─ operator, only with cleanupContainersOnTolerationsMismatch: true
     taint key in ignoredTaints              → nothing
     tolerated by the desired tolerations    → nothing
     otherwise                               → the WekaContainer is deleted
                                               (all modes, backends included)
```

What counts as "not tolerated" follows from the layers: backends and drivers-builder/dist have no
NoSchedule expansion, so any NoSchedule taint they don't tolerate deletes them; client and aux pods are
deleted only for an untolerated NoExecute taint; pods with `clientsTolerateAll` never are. A deleted
backend container takes its drives out of the cluster, so taint backend nodes carefully with this setting.

The check uses the container's desired tolerations, rebuilt from its spec and the current policy settings, not
the tolerations visible on the running pod. Removing a toleration, or turning `clientsTolerateAll` off, therefore
deletes containers on nodes with a matching taint even though their running pods still show the toleration.

## Changing tolerations on a running system

- Tolerations you add to a WekaCluster, WekaClient or WekaPolicy are added to the running pods without a
  restart. Removed ones stay until the pod is recreated.
- `clientsTolerateAll` reaches running pods too: the operator adds its `operator: Exists` toleration to them on
  the next reconcile, without a restart. Kubernetes only lets a live pod's tolerations be appended, so it lands
  at the end of the list, after any toleration the pod already has. A pod that has a finite NoExecute
  `tolerationSeconds` for a taint keeps being evicted on that schedule until it is recreated, because the first
  matching toleration wins. Turning the setting off does not remove the added toleration from a running pod; it
  stays until the pod is recreated.
- `tolerateCordon` and the helm `skip*` values apply only to pods created after the change. Existing pods keep
  their tolerations until they are recreated (for example after an eviction).

## See also

- [Driver distribution: cordoned and tainted nodes](driver-distribution.md#cordoned-and-tainted-nodes)
- [Helm install](helm-install.md)
- [Node agent](node-agent.md)
