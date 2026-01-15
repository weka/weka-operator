# Controllers Navigation

All reconciliation logic lives in `internal/controllers/`.

## Controller Types

| Controller | Path | Reconciles |
|-----------|------|-----------|
| WekaCluster | `wekacluster/controller.go` | Cluster-level resources |
| WekaContainer | `wekacontainer/controller.go` | Per-node containers |
| WekaClient | `wekaclient/client_controller.go` | Client deployments |
| WekaPolicy | `wekapolicy_controller.go` | Automated policies |
| WekaManualOp | `wekamanualoperation_controller.go` | One-time operations |

## Deep Dives

- [wekacontainer.md](wekacontainer.md) - Container lifecycle (most complex)
- [wekacluster.md](wekacluster.md) - Cluster lifecycle
- [wekaclient.md](wekaclient.md) - Client lifecycle

## Shared Components

```
allocator/          # Resource allocation (ports, IPs, drives)
  allocator.go      # Main allocation logic
  templates.go      # Container templates
  ranges.go         # IP/port range management

factory/            # Object construction
  container_factory.go  # Creates container specs
  labels.go             # Label management

resources/          # K8s resource builders
  pod.go            # Pod spec construction
  init_containers.go # Init container logic
  tolerations.go    # Toleration handling
  cluster.go        # Cluster resource helpers

utils/              # Shared utilities
  utils.go          # Common helpers
  k8s_utils.go      # K8s-specific utils
  health.go         # Health check logic
```

## Supporting Packages

### Upgrade Controller

**Path**: `internal/controllers/upgrade/upgrade.go`

Orchestrates container image upgrades across the cluster:
- `RollingUpgrade()` - Upgrades one container at a time (recommended)
- `AllAtOnceUpgrade()` - Upgrades all containers simultaneously
- `AreUpgraded()` - Checks if all containers are at target image

### Metrics Builder

**Path**: `internal/controllers/metrics/metrics.go`

Builds Prometheus-format metrics for cluster monitoring:
- `BuildClusterPrometheusMetrics()` - Creates metric strings from cluster status
- Metrics include: throughput, IOPS, drives count, capacity, status, alerts

## Reconciliation Pattern

All controllers use `go-steps-engine` for step-based reconciliation:
- `pkg/go-steps-engine/lifecycle/` - Core engine
- Steps defined as functions with predicates
- State transitions via status updates
