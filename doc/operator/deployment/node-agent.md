# Node-agent DaemonSet

The operator creates and updates the node-agent DaemonSet itself; the Helm chart does not render it.
Its settings live in the configuration WekaPolicy (the one policy of this kind in the operator namespace),
under `spec.payload.configurationPayload.nodeAgent`. Every field is optional and an unset field keeps its default.

```yaml
apiVersion: weka.weka.io/v1alpha1
kind: WekaPolicy
metadata:
  name: weka-configuration
  namespace: weka-operator-system
spec:
  payload:
    configurationPayload:
      nodeAgent:
        updateStrategy:
          type: RollingUpdate
          rollingUpdate:
            maxUnavailable: 1
        persistencePath: /root/k8s-weka
```

## Fields

| Field | Default |
|---|---|
| `updateStrategy` | `RollingUpdate`, `maxUnavailable: 100%`, `maxSurge: 0` (all pods restart at once). `maxSurge` must stay 0 while `devicePlugin.enabled` is true: two agents on one node would fight over the plugin sockets |
| `resources` | limits `cpu: 1000m`, `memory: 1024Mi`, `ephemeral-storage: 400Mi`; requests `cpu: 50m`, `memory: 64Mi`, `ephemeral-storage: 100Mi`. Replaced as a whole |
| `tolerations` | `[{operator: Exists}]` |
| `nodeSelector` | none |
| `affinity` | required node affinity: `kubernetes.io/os` in `linux`, `kubernetes.io/arch` in `amd64`, `arm64` |
| `persistencePath` | `/opt/k8s-weka`; the agent mounts `<persistencePath>/shared`. Use `/root/k8s-weka` on OpenShift and `/mnt/stateful_partition/k8s-weka` on Google Container-Optimized OS |
| `kubeletPath` | `/var/lib/kubelet`; locates the kubelet `device-plugins` directory |
| `devicePlugin.enabled` | `false`; see [NUMA alignment](../concepts/numa-alignment.md) |

On VM nodes using the native containerd snapshotter (for example KubeVirt guests without overlayfs support) the
whole image counts as ephemeral storage, so raise `resources.limits.ephemeral-storage` if node-agent pods are evicted.

## Migrating from Helm values

The chart fails when `nodeAgent` is set. `helm upgrade --reuse-values` carries an old `nodeAgent` block over and hits
the guard, so drop `--reuse-values` or pass `--set nodeAgent=null`. Move the values as follows:

| Helm value | Policy field |
|---|---|
| `nodeAgent.resources` | `nodeAgent.resources` |
| `nodeAgent.tolerations` | `nodeAgent.tolerations` |
| `nodeAgent.nodeSelector` | `nodeAgent.nodeSelector` |
| `nodeAgent.affinity` | `nodeAgent.affinity` |
| `nodeAgent.persistencePaths` | `nodeAgent.persistencePath` |
| `nodeAgent.kubeletPath` | `nodeAgent.kubeletPath` |
| `nodeAgent.devicePlugin.enabled` | `nodeAgent.devicePlugin.enabled` |

## Behavior

- The operator polls and reconciles the DaemonSet every 30 seconds, so policy changes are picked up within 30
  seconds. It applies policy and operator-version changes; a manual edit of the DaemonSet spec (for example
  `kubectl edit` or `set image`) stays until the next such change. An `updateStrategy`-only change is patched onto the
  DaemonSet without restarting pods; any pod-template change rolls out per the strategy.
- The DaemonSet is named `<prefix>-node-agent` and owned by the operator Deployment, so uninstalling the chart removes it.
- Upgrading from a chart that rendered the DaemonSet: Helm removes the old object and the new operator recreates it
  under the same name. Node-agent pods are down until the new operator is running.
- To roll back to a chart version from before this change, first scale the operator down so it cannot recreate the
  DaemonSet (`kubectl -n <namespace> scale deploy <prefix>-controller-manager --replicas=0`), delete the DaemonSet
  (`kubectl -n <namespace> delete ds <prefix>-node-agent`), then run `helm rollback`, which restores the replica count.
  Otherwise Helm fails with `no DaemonSet with the name "<prefix>-node-agent" found`, because the live object is not
  in the release manifest. Node-agent is briefly down, as on upgrade.
- Node-agent pods must run the operator image; until a pod does, the operator rejects its requests. With a slow
  `updateStrategy` this mismatch lasts as long as the rollout.
- If the configuration cannot be resolved (for example two configuration policies exist) the operator skips the
  reconcile and the existing DaemonSet keeps running unchanged.
- Existing node-agent pods keep running while the operator is down; only updates wait for it.
