# Services Navigation

API clients, Kubernetes helpers, node-local services and shared domain types.

| Area | Source | Responsibility |
|---|---|---|
| Weka API | `internal/services/weka.go` | Cluster/container/drive operations and protocol configuration |
| Weka helpers | `internal/services/weka_cluster.go`, `weka_container.go`, `cluster_join_ips.go`, `secrets.go` | Cluster and container operations, join addresses, credentials |
| Kubernetes | `internal/services/kubernetes/` | `kubernetes.go`, `affinities.go`, `metricsservice.go` |
| Pod execution | `internal/services/exec/` | Exec into pods |
| Discovery | `internal/services/discovery/` | Service discovery and container operational checks |
| SSD proxy | `internal/services/ssdproxy/` | Node-agent JSONRPC client for physical/virtual drives and node-agent pod/token lookup |
| Node agent | `internal/node_agent/node_agent.go` | Drive discovery (`/findDrives`), local operations and JRPC forwarding |
| Node metrics | `internal/node_agent/scrapper.go` | Metrics scraping |
| NUMA device plugin | `internal/node_agent/deviceplugin/` | Discovery, kubelet plugin server and restart-aware registration; `NODE_AGENT_DEVICE_PLUGIN_ENABLED` |
| Weka Home reporter | `internal/reporter/` | [Snapshot collection, identity and transport](reporter.md) |
| Domain types | `internal/pkg/domain/` | `resources.go`, `allocations.go`, `auth.go`, `wekahome.go`, `api_extension.go`, `consts.go`, `hashes.go` |
| Utilities | `pkg/util/` | Files, hashes, collections, IPs, tolerations, HTTP helpers; `kubernetes.go` event recording |
| Shared constants | `internal/consts/` | `consts.go`: finalizers, drive annotations, extended resource names; `event_actions.go`: per-controller event action names |
| Optional cluster API | `internal/rest_api/` | `router.go`, `cluster.go`, `password.go`; enabled with `ENABLE_CLUSTER_API` |

For callers, start with [controllers](../controllers/index.md) or
[operations](../operations/index.md). Configuration lives in [config](../config/index.md).
