# Configuration Navigation

Environment variables, Helm values, and API types.

## Environment Configuration

**Path**: `internal/config/env.go`

All operator configuration via environment variables.
Set in Helm chart: `charts/weka-operator/templates/manager.yaml`

Key config categories:
- Operator identity (namespace, pod UID, deployment name)
- Image references (operator, CSI, drivers, node-agent)
- Feature flags and timeouts
- OTEL/observability settings
- Priority class names
- Proxy settings (HTTP/HTTPS)

## Helm Chart

**Path**: `charts/weka-operator/`

| File | Purpose |
|------|---------|
| `values.yaml` | Default configuration |
| `templates/manager.yaml` | Operator deployment |
| `templates/role.yaml` | RBAC permissions |
| `templates/priority_classes.yaml` | Priority classes |
| `templates/metrics_daemonset.yaml` | Metrics collection |
| `resources/weka_runtime.py` | Python runtime for pods |
| `resources/run-weka-cli.sh` | CLI wrapper script |

## API Types (CRDs)

**Path**: `pkg/weka-k8s-api/api/v1alpha1/`

| File | Defines |
|------|---------|
| `wekacluster_types.go` | WekaCluster spec/status |
| `container_types.go` | WekaContainer spec/status |
| `client_types.go` | WekaClient spec/status |
| `wekapolicy_types.go` | WekaPolicy spec/status |
| `wekamanualoperation_types.go` | WekaManualOp spec/status |
| `driveclaims_types.go` | Drive claim types |
| `instructions_type.go` | Stop/start instructions |
| `metrics.go` | Metrics types |
| `condition/conditions.go` | Status conditions |

Generated docs: `doc/api_dump/*.md`

## Adding Configuration

1. Add field to `internal/config/env.go`
2. Set default in `charts/weka-operator/values.yaml`
3. Wire into `templates/manager.yaml`
4. See [tasks.md](../tasks.md) for detailed steps
