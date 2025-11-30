# WekaCluster Controller

Manages cluster-level resources and post-cluster operations.

**Location**: `internal/controllers/wekacluster/`

## Main Files

| File | Purpose |
|------|---------|
| `controller.go` | Controller setup, watches |
| `reconciler_loop.go` | Main reconciliation loop |

## Steps (steps_*.go)

| File | Purpose |
|------|---------|
| `steps_cluster_creation.go` | Initial cluster creation |
| `steps_post_cluster.go` | Post-creation operations |
| `steps_metrics.go` | Metrics/monitoring setup |
| `steps_deletion.go` | Cluster deletion flow |

## Functions (funcs_*.go)

| File | Purpose |
|------|---------|
| `funcs_clusterization.go` | Cluster formation logic |
| `funcs_credentials.go` | Secret/credential mgmt |
| `funcs_helpers.go` | Utility functions |
| `funcs_upgrade.go` | Upgrade orchestration |
| `funcs_nfs.go` | NFS frontend config |
| `funcs_s3.go` | S3 configuration |
| `funcs_management_proxy.go` | Management proxy service |
| `funcs_management_service.go` | Management k8s service |

## Other

- `gc.go` - Garbage collection for orphaned resources

## Key Interactions

- Creates WekaContainer resources for cluster nodes
- Manages cluster-wide secrets and credentials
- Coordinates NFS/S3 protocol configuration
- Exposes management via K8s services/ingress
