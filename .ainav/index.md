# Weka Operator Navigation Index

Entry point for AI navigation. Max 3 hops to any information.

## Quick Reference

| Area | Entry File | When to Use |
|------|-----------|-------------|
| Controllers | [controllers/index.md](controllers/index.md) | Reconciliation logic, lifecycle management |
| Operations | [operations/index.md](operations/index.md) | Manual ops, policies, CSI, drivers |
| Config | [config/index.md](config/index.md) | Helm values, env vars, API types |
| Services | [services/index.md](services/index.md) | Weka API, K8s utils, node agent |
| Examples | [examples/index.md](examples/index.md) | YAML examples for clusters, clients, policies |
| **Tasks** | [tasks.md](tasks.md) | How to add/modify features |

## Repository Structure (Key Paths)

```
cmd/manager/main.go          # Operator entry point
internal/controllers/        # All reconciliation logic
  wekacluster/              # Cluster lifecycle
  wekacontainer/            # Container lifecycle (MOST ACTIVE)
  wekaclient/               # Client lifecycle
  operations/               # Manual/policy operations
  upgrade/                  # Container upgrade orchestration
  metrics/                  # Prometheus metrics building
internal/services/           # Weka API, K8s abstractions
internal/config/             # Environment configuration
internal/consts/             # Shared constants, annotations, resources
internal/rest_api/           # Optional REST API server (cluster CRUD)
internal/node_agent/         # Per-node agent server
pkg/weka-k8s-api/           # CRD type definitions
charts/weka-operator/        # Helm chart and Python runtime
```

## Key Areas by Functionality

### Container Lifecycle (Core)
- `wekacontainer/flow_*.go` - State machine flows
- `wekacontainer/funcs_*.go` - Specific operations

### CSI Integration
- `operations/csi/*.go` - CSI controller/nodeserver
- `wekacontainer/csi_steps.go` - CSI lifecycle in container

### Drivers & Resources
- `operations/load_drivers.go` - Driver loading
- `wekacontainer/funcs_drivers.go` - Driver state

### NFS/S3 Protocols
- `wekacluster/funcs_nfs.go` - NFS config
- `wekacontainer/funcs_active_state_nfs.go` - NFS runtime

### Configuration
- `charts/weka-operator/values.yaml` - Helm defaults
- `internal/config/env.go` - Environment config

## CRD Types Quick Lookup

- **WekaCluster**: `pkg/weka-k8s-api/api/v1alpha1/wekacluster_types.go`
- **WekaContainer**: `pkg/weka-k8s-api/api/v1alpha1/container_types.go`
- **WekaClient**: `pkg/weka-k8s-api/api/v1alpha1/client_types.go`
- **WekaPolicy**: `pkg/weka-k8s-api/api/v1alpha1/wekapolicy_types.go`
- **WekaManualOperation**: `pkg/weka-k8s-api/api/v1alpha1/wekamanualoperation_types.go`
