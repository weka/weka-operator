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

**Path**: `internal/services/ssdproxy/`
- Reusable node-agent JSONRPC client for ssdproxy virtual drives (list physical/virtual drives, remove VID) + node-agent pod/token lookup.

## Node Agent

**Path**: `internal/node_agent/node_agent.go`

HTTP server running on each node (via daemonset or pod):
- Drive discovery endpoint (`/findDrives`)
- Metrics scraping
- Local operations execution
- JRPC call forwarding

Related:
- `scrapper.go` - Metrics scraping logic
- `deviceplugin/` - Kubelet device plugin (gRPC) advertising each NUMA region as extended resource `weka.io/numa-region-<N>`; discovery, plugin server, and restart-aware registration manager.

## Weka Home CR Reporter

**Path**: `internal/reporter/`

Periodically snapshots operator-managed objects (5 weka CRs, operator Deployment,
DaemonSets, Pods, Node projection) to Weka Home as gzipped kind-tagged NDJSON
(`POST /api/v4/operator/deployments/{id}/snapshot`). Enabled via
`wekahome.reporter.enabled` (default on); identity = keypair+GUID Secret, RS256 SRT JWT.

| File | Purpose |
|------|---------|
| `reporter.go` | Report loop, registration latch, `buildSnapshot` |
| `collector.go` | CR-kind registry + Deployment/DaemonSet/Pod collectors |
| `collector_nodes.go` | Node projection (weka.io-scoped labels/annotations) |
| `collector_events.go` | Events List (uncached reader) + per-object `_events` index |
| `serializer.go` | NDJSON envelope, strip, `_events` graft |
| `identity.go` | Deployment identity + registration |
| `transport.go` | TLS/proxy-aware HTTP client, gzipped send |

Each object's JSON embeds its kubectl-describe Events section as a synthetic
top-level `_events` array (all types, describe-like projection, sorted by last-seen).

## Domain Types

**Path**: `internal/pkg/domain/`

| File | Contains |
|------|---------|
| `resources.go` | Resource allocation types |
| `allocations.go` | Allocation structures |
| `consts.go` | Domain constants |
| `hashes.go` | Hash utilities |
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

## Constants Package

**Path**: `internal/consts/consts.go`

Shared constants used across controllers:
- `WekaFinalizer` - Kubernetes finalizer for Weka resources
- Node annotations for drive management (WekaDrives, BlockedDrives, SharedDrives, DriveTypeOverrides)
- Extended resource names (ResourceDrives, ResourceSharedDrivesCapacity)

## REST API Server (Optional)

**Path**: `internal/rest_api/`

Optional HTTP API server for cluster operations (port 8082). Enabled via `ENABLE_CLUSTER_API` env var.

| File | Purpose |
|------|---------|
| `router.go` | API server setup, route registration |
| `cluster.go` | Cluster CRUD operations |
| `password.go` | Password update endpoint |
