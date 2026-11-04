# Configuration Navigation

Use this map for operator settings, deployment templates and CRD definitions.

| Area | Source | Detail |
|---|---|---|
| Environment and defaults | `internal/config/env.go` | Operator identity, images, feature flags, timeouts, observability and proxy settings |
| Helm defaults | `charts/weka-operator/values.yaml` | User-facing configuration |
| Environment wiring | `charts/weka-operator/templates/manager.yaml` | Operator deployment |
| RBAC and priorities | markers → `make rbac` → `charts/weka-operator/files/rbac/role.yaml`; `templates/role.yaml`, `role_binding.yaml`, `csi/*-rbac.yaml`, `priority_classes.yaml` | Permissions and scheduling; [rbac.namespaces](../../doc/operator/deployment/helm-install.md#rbac-namespace-scoping) |
| Runtime scripts | `charts/weka-operator/resources/` | `weka_runtime.py`, `run-weka-cli.sh` |
| CRD types | `pkg/weka-k8s-api/api/v1alpha1/` | Cluster, container, client, policy and manual-operation specs/status |
| Admission | [validation.md](validation.md) | Rule registry, shared helpers and severity defaults |

## Focused routes

- Capacity constraints: `CapacityPlannerConfig` is shared by both planners; `ClusterCapacityConfig` applies to clusterCapacity. `allocator.CapacityConstraintsFromConfig` wires them to the planner. See the [Helm constraints table](../../doc/operator/deployment/cluster-capacity.md) and [planner map](../controllers/wekacluster-drive-planning.md). `capacityPlannerConstraints` configures the algorithm; `capacityPlanner` configures the optional toolbox pod.
- Pod security: `resources/security_context.go` (`ApplySecurityProfile`, `mergePodSecurityContext`) applies `WEKA_POD_SECURITY_CONTEXT` / Helm `podSecurityContext`; only supported fields merge.
- Management proxy: [management-proxy.md](../controllers/management-proxy.md).
- Generated API reference: `doc/api_dump/` (do not edit); shared conditions: `api/v1alpha1/condition/conditions.go`.
- Extra volumes: Helm `manager.extraVolumes`/`extraVolumeMounts` for the operator pod (`tmpdir` / `/tmp` reserved); WekaCluster `spec.podConfig`, WekaClient and WekaContainer `spec` carry their own. See [extra-volumes.md](../../doc/operator/deployment/extra-volumes.md).
- Removed-node backend cleanup: `CLEANUP_REMOVED_NODES` / Helm `cleanupRemovedNodes`, tri-state `false`/`true`/`auto` (default `auto`), grace-period logic in `wekacontainer/funcs_handle_node_statuses.go`. See [helm-install.md](../../doc/operator/deployment/helm-install.md).
- Node-agent DaemonSet: built by `internal/node_agent/daemonset.go` from `NodeAgentSettings` (`internal/services/configuration_settings.go`); see [node-agent.md](../../doc/operator/deployment/node-agent.md).
- CSI accessible label settings: `CsiSettings` in `internal/services/configuration_settings.go`; see [embedded-csi.md](../../doc/operator/deployment/embedded-csi.md).

To add configuration, update `env.go`, Helm defaults and deployment wiring together; see [tasks.md](../tasks.md).
