# Services Navigation

Weka API clients, Kubernetes utilities, and node agent.

## Weka API Client

**Path**: `internal/services/weka.go`

Main interface to Weka cluster API:
- Container management (add, remove, deactivate)
- Drive operations
- S3 operations
- NFS configuration
- Cluster status queries

Related files:
- `weka_cluster.go` - Cluster-specific operations
- `weka_container.go` - Container-specific operations
- `cluster_join_ips.go` - Join IP management
- `secrets.go` - Credential handling

## Kubernetes Utilities

**Path**: `internal/services/kubernetes/`

| File | Purpose |
|------|---------|
| `kubernetes.go` | Core K8s operations |
| `affinities.go` | Affinity management |
| `metricsservice.go` | Metrics service setup |

**Path**: `internal/services/exec/`
- Pod exec operations

**Path**: `internal/services/discovery/`
- Service discovery logic

## Node Agent

**Path**: `internal/node_agent/node_agent.go`

HTTP server running on each node (via daemonset or pod):
- Drive discovery endpoint (`/findDrives`)
- Metrics scraping
- Local operations execution
- JRPC call forwarding

Related:
- `scrapper.go` - Metrics scraping logic
- `weka_structs_test.go` - Test structures

## Domain Types

**Path**: `internal/pkg/domain/`

| File | Contains |
|------|---------|
| `resources.go` | Resource allocation types |
| `allocations.go` | Allocation structures |
| `consts.go` | Domain constants |
| `hashs.go` | Hash utilities |
| `auth.go` | Auth types |
| `wekahome.go` | Weka home integration |
| `api_extension.go` | API extensions |

## Utility Packages

**Path**: `pkg/util/`

General utilities: files, hashes, maps, slices, kubernetes helpers,
IP handling, tolerations, HTTP client, etc.

## Service Patterns

- Weka API client wraps HTTP calls to Weka cluster
- Node agent provides per-node HTTP endpoints
- K8s utilities abstract controller-runtime operations
- Domain types define shared data structures
