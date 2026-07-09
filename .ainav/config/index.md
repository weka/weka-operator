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
- Port allocation settings (starting port for cluster port ranges)
- `ClusterCapacityConfig` — clusterCapacity pod-resource constraints (`TlcCapacityPerCoreGiB`, `QlcCapacityPerCoreGiB`, `MaxComputeCoresPerNode`, `ImbalanceFactor`); Helm names in `cluster-capacity.md` Helm constraints table. (`Consts.UnschedulableDriveContainerGCTimeout` gates GC of long-unscheduled drive containers.)
- Pod-level securityContext injection (`WEKA_POD_SECURITY_CONTEXT` — JSON-encoded `corev1.PodSecurityContext`) — applied to every privileged/hostPath pod produced by the operator: WekaContainer pods (`pod.go`), CSI node DaemonSet, CSI controller, and prepull/trace/cleanup/pvc-migrate Jobs. Mgmt-proxy and metrics pods are excluded (non-privileged, no hostPath). Today only `appArmorProfile` is propagated (used to satisfy Kyverno `require-apparmor-on-privileged-or-hostpath`); other PodSecurityContext fields parse but `mergePodSecurityContext` ignores them — add a line there to support more. Helper: `internal/controllers/resources/security_context.go` (`ApplySecurityProfile` + `mergePodSecurityContext`). Helm value: `podSecurityContext` (default `{}`).

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

`clusterCapacity` operator constraints (`maxComputeCoresPerNode`, `tlcCapacityPerCoreGiB`, `qlcCapacityPerCoreGiB`) are documented in `doc/operator/deployment/cluster-capacity.md` (Helm constraints table).

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

## Validation & Admission

**Path**: `internal/validation/` + `internal/admission/`

Admission-webhook validators implement the `Validator` interface (`validator.go`), are
listed per-CRD in `registry.go`, and get a default severity in `admission/defaults.go`.
Add a rule = implement + register + add to the defaults table. clusterCapacity validators:
`cluster_capacity_chunk_feasibility.go` (greenfield per-FD TLC share ≥ 384 GiB; skipped once the
cluster has TLC-bearing drive containers) and `cluster_capacity_protection.go` (min SW≥3, RL≥2, HS≥0 /
hotSpare optional — the `3+2+0` floor from `allocator.MinProtectionFloor`).
Protection values are resolved via `DriveSharingConfig.EffectiveProtection` (env.go): a per-cluster
spec field wins when non-zero (0 is treated as unset), else the Helm-level default (`PROTECTION_STRIPE_WIDTH` /
`PROTECTION_REDUNDANCY_LEVEL` / `PROTECTION_HOT_SPARE`, values `protection.*`) fills it. Same helper
is used in `FormCluster` so validation and formation agree.

## Adding Configuration

1. Add field to `internal/config/env.go`
2. Set default in `charts/weka-operator/values.yaml`
3. Wire into `templates/manager.yaml`
4. See [tasks.md](../tasks.md) for detailed steps
