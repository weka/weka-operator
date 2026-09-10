# Controllers Navigation

Reconciliation lives in `internal/controllers/` and uses `go-steps-engine` steps,
predicates and status transitions.

| Controller | Source entry | Navigation |
|---|---|---|
| WekaCluster | `wekacluster/controller.go` | [Cluster lifecycle](wekacluster.md) |
| WekaContainer | `wekacontainer/controller.go` | [Container lifecycle](wekacontainer.md) |
| WekaClient | `wekaclient/client_controller.go` | [Client lifecycle](wekaclient.md) |
| WekaPolicy | `wekapolicy_controller.go` | [Operations](../operations/index.md) |
| WekaManualOperation | `wekamanualoperation_controller.go` | [Operations](../operations/index.md) |

Focused routes: [capacity planning](wekacluster-drive-planning.md),
[management proxy](management-proxy.md), [admission validation](../config/validation.md).

## Shared components

Paths below are relative to `internal/controllers/`.

| Area | Entry points and responsibility |
|---|---|
| Workload ownership | `owner_details.go`: constructs `WekaOwnerDetails` shared by policy/manual-operation workloads |
| Allocation | `allocator/allocator.go`, `templates.go`, `ranges.go`: container templates, ports and IP ranges |
| Planner adapter | `allocator/cluster_capacity.go`: `CapacityConstraintsFromConfig`, `MinProtectionFloor`; `cluster_capacity_assignment.go`: `ResolveNodeFDValue` |
| Object construction | `factory/container_factory.go`, `labels.go`: container specs and labels |
| Pod resources | `resources/pod.go`, `init_containers.go`, `tolerations.go`, `cluster.go`: Kubernetes resource builders |
| Utilities | `utils/utils.go`, `k8s_utils.go`, `health.go`: shared controller helpers |
| Upgrades | `upgrade/upgrade.go`: `RollingUpgrade`, `AllAtOnceUpgrade`, `AreUpgraded` |
| Metrics | `metrics/metrics.go`: `BuildClusterPrometheusMetrics` |
| Event recording | `util.WrapEventRecorder` wraps each reconciler loop's recorder; actions in `internal/consts/event_actions.go` |

The pure planning algorithm lives in `internal/capacityplanner/`;
Kubernetes inventory collection lives in its `inventory/` subpackage.
