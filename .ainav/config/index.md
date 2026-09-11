# Configuration Navigation

Use this map for operator settings, deployment templates and CRD definitions.

| Area | Source | Detail |
|---|---|---|
| Environment and defaults | `internal/config/env.go` | Operator identity, images, feature flags, timeouts, observability and proxy settings |
| Helm defaults | `charts/weka-operator/values.yaml` | User-facing configuration |
| Environment wiring | `charts/weka-operator/templates/manager.yaml` | Operator deployment |
| RBAC and priorities | `charts/weka-operator/templates/role.yaml`, `priority_classes.yaml` | Permissions and scheduling |
| Runtime scripts | `charts/weka-operator/resources/` | `weka_runtime.py`, `run-weka-cli.sh` |
| CRD types | `pkg/weka-k8s-api/api/v1alpha1/` | Cluster, container, client, policy and manual-operation specs/status |
| Admission | [validation.md](validation.md) | Rule registry, shared helpers and severity defaults |

## Focused routes

- Capacity constraints: `CapacityPlannerConfig` is shared by both planners; `ClusterCapacityConfig` applies to clusterCapacity. `allocator.CapacityConstraintsFromConfig` wires them to the planner. See the [Helm constraints table](../../doc/operator/deployment/cluster-capacity.md) and [planner map](../controllers/wekacluster-drive-planning.md). `capacityPlannerConstraints` configures the algorithm; `capacityPlanner` configures the optional toolbox pod.
- Pod security: `internal/controllers/resources/security_context.go` (`ApplySecurityProfile`, `mergePodSecurityContext`) applies `WEKA_POD_SECURITY_CONTEXT` / Helm `podSecurityContext`. Only supported fields are merged; inspect the helper before adding a field.
- Management proxy: [management-proxy.md](../controllers/management-proxy.md).
- Generated API reference: `doc/api_dump/` (do not edit); shared conditions live in `pkg/weka-k8s-api/api/v1alpha1/condition/conditions.go`.
- Extra volumes: `manager.extraVolumes`/`extraVolumeMounts` (Helm) add volumes to the operator's
  own pod; the `tmpdir` volume and `/tmp` mount are reserved and rejected via Helm `fail`.
  WekaCluster (`spec.podConfig`), WekaClient (`spec`) and WekaContainer (`spec`, propagated only,
  not admission-validated) carry their own `extraVolumes`/`extraVolumeMounts` — e.g. a CA bundle.
  See [extra-volumes.md](../../doc/operator/deployment/extra-volumes.md).

To add configuration, update `env.go`, Helm defaults and deployment wiring together; see [tasks.md](../tasks.md).
