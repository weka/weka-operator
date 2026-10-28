Installed with command as
```bash
# Install CRDs using server-side apply
helm show crds oci://quay.io/weka.io/helm/weka-operator --version v1.5.0 | \
  kubectl apply --server-side -f -

# Install operator
helm upgrade --create-namespace \
    --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
    --namespace weka-operator-system \
    --version v1.5.0 --values operator_values.yaml
```
Always make sure to update CRDs before upgrading helm using server-side apply to handle large CRD definitions.

Operator pod can be found with label `app=weka-operator` after installation

notable helm-level values:

`driveSharing.driveTypesRatio` - Global default ratio for drive types (TLC vs QLC) when using drive sharing (default: `tlc: 1, qlc: 10`)

`localDataPvc` - global PVC to use for container's local data
Helm-level configure of PVC is deprecated and should be used only when explicitly instructed
If just asked to "use pvc for cluster", use cluster spec field instead. `globalPVC` object field on cluster spec with, with `name` field on it, which references PVC

`portAllocation.startingPort` - Starting port for Weka container port allocation (default: `35000`). This is the base port from which the operator allocates port ranges for Weka clusters. Only modify if you have specific firewall or port conflict requirements.

`manager.extraVolumes` / `manager.extraVolumeMounts` - extra volumes/mounts on the operator's own Deployment pod (e.g. a corporate CA for an outbound proxy). The reserved volume name is `tmpdir`, the reserved mount path is `/tmp`. See `operator/deployment/extra-volumes.md` for this and the separate, per-CR `extraVolumes` fields on WekaCluster/WekaClient.

`cleanupRemovedNodes` - Controls what happens to a backend WekaContainer when its target node leaves the K8s cluster (default: `"auto"`). `"false"` never deletes the container; `"true"` deletes it immediately; `"auto"` holds the container in `Stale` status for a grace period (30m on managed cloud AWS/OCI, 24h otherwise) and deletes it only if the node is still gone at the end, so a transient node loss (reboot, ASG churn, brief API-server blip) no longer forces an unnecessary teardown. Quote the value. **Note:** the default changed from the previous effective `"false"` (never delete) to `"auto"` — deployments that do not set this value explicitly will now delete removed-node backends after the grace period.

## RBAC namespace scoping

`rbac.namespaces` (default `[]`) limits where the operator may change namespaced objects and where the CSI controller may read secrets.

The operator's permissions are split into two ClusterRoles (names below use the default `prefix`, `weka-operator`):
- `weka-operator-manager-role` - always bound cluster-wide: cluster-scoped kinds (nodes, namespaces, storage classes, CSI drivers, the validating webhook configuration), all `weka.weka.io` resources, events, and get/list/watch on pods, configmaps, services, endpoints, deployments, daemonsets, jobs, leases, ingresses and resourceclaims. Pod reads stay cluster-wide because the capacity planner charges pods it does not own against node resources.
- `weka-operator-namespaced-role` - pod, secret, configmap, service, endpoint, deployment, daemonset, job, lease, ingress and resourceclaim writes, `pods/exec`, and all secret access.

With the list empty, `weka-operator-namespaced-role` is bound with a ClusterRoleBinding, so the effective permissions are the same as before the split. With the list set, it is bound with one RoleBinding per listed namespace plus the release namespace, and the operator's secret cache watches only those namespaces.

```
weka-operator-manager-role (ClusterRole) ── ClusterRoleBinding ──► operator SA, everywhere
  nodes, namespaces, weka CRs, events, reads, ...

weka-operator-namespaced-role (ClusterRole)
  pods, pods/exec, secrets, configmaps, deployments, jobs, ...
    rbac.namespaces: []       ── ClusterRoleBinding ─────────► operator SA, all namespaces
    rbac.namespaces: [a, b]   ── RoleBinding in a ───────────► operator SA, only in a
                              ── RoleBinding in b ───────────► operator SA, only in b
                              ── RoleBinding in release ns ──► operator SA, only there
```

The binding sets the scope, not the role: a RoleBinding that references a ClusterRole grants its rules only inside the RoleBinding's namespace. The namespaced rules are a ClusterRole so that one definition is reused by every RoleBinding instead of copying a Role into each namespace; `rbac.namespaces` changes only the bindings, never the rules. Cluster-scoped kinds such as nodes stay in `weka-operator-manager-role`, because through a RoleBinding they would match nothing.

The list must contain every namespace that holds a weka.weka.io resource (WekaCluster, WekaClient, WekaContainer, WekaManualOperation, WekaPolicy), including the namespace of a WekaClient's target cluster. Namespaces that only hold applications and PVCs do not need to be listed: the CSI secret `weka-csi-<cluster>` lives in the WekaCluster or WekaClient namespace.

```yaml
rbac:
  namespaces:
    - weka
```

Helm does not create the listed namespaces: each must exist before `helm install` or `helm upgrade`, or the release fails with `namespaces "<name>" not found`. To onboard a new namespace, create it, add it to `rbac.namespaces` and run `helm upgrade`.

For a Weka CR in a namespace that is not listed, any reconcile step that writes a namespaced object or reads a secret fails. The operator logs and events show `Forbidden` errors for pod, secret or configmap writes, or `unknown namespace for the cache` for secret reads. Steps that only touch cluster-scoped objects such as nodes still succeed: the list limits the operator's own write access, while who may create Weka CRs in which namespace is governed by users' RBAC on `weka.weka.io` resources. Add the namespace to `rbac.namespaces` and run `helm upgrade`.

CSI secrets follow the same list: `csi-wekafs-controller-secrets` (secret `get`) is bound to `csi-wekafs-controller-sa` with a ClusterRoleBinding when the list is empty, or with a RoleBinding in each namespace above when it is set.

CSI changes that apply to every install, including the default:
- `csi-wekafs-node-cr` no longer has any secret permissions. Kubelet reads node-stage and node-publish secrets with its own credentials.
- The CSI controller can no longer `list` secrets, only `get` them by name, and that permission moved from `csi-wekafs-controller-cr` to `csi-wekafs-controller-secrets`.
