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
| `steps_cluster_creation.go` | Initial cluster creation; `BuildMissingContainers` + the count-based role path |
| `steps_planner_apply.go` | Build/apply for both planner modes: `plannerSizingMode`, `buildPlannerDriveContainers`, `applyPlannerDriveGrowth`, `applyPlannerComputeGrowth`, `updateContainerWithRetry` |
| `planner_events.go` | Planner event reasons/severities/throttling: `plannerEventSpecs`, `emitPlannerEvent` |
| `steps_post_cluster.go` | Post-creation operations |
| `steps_metrics.go` | Metrics/monitoring setup |
| `steps_deletion.go` | Cluster deletion flow |

## Functions (funcs_*.go)

| File | Purpose |
|------|---------|
| `funcs_clusterization.go` | Cluster formation logic |
| `funcs_fd_planning.go` | Capacity planning entry points: `planClusterCapacity`, `planAutoFullDrives` |
| `funcs_credentials.go` | Secret/credential mgmt |
| `funcs_helpers.go` | Utility functions |
| `funcs_upgrade.go` | Upgrade orchestration; also propagates cluster spec (incl. `Numa`) to containers per-role in `HandleSpecUpdates` |
| `funcs_nfs.go` | NFS interface-group config + teardown (EnsureNfs / ShouldDestroyNfs+DestroyNfs) |
| `funcs_s3.go` | S3 configuration |
| `funcs_management_proxy.go` | Envoy management proxy: ConfigMap + Deployment + Service + Ingress → [management-proxy.md](management-proxy.md) |
| `funcs_management_service.go` | Management k8s service; endpoint selection → [management-proxy.md](management-proxy.md) |

## Drive-container sizing modes

Mode is derived from which `spec.dynamicTemplate` fields are set; `plannerSizingMode`
(`steps_planner_apply.go`) is the single detection site.

| Mode | Family | Planner | Notes |
|------|--------|---------|-------|
| explicit counts (`computeContainers`+`driveContainers`, +`numDrives`/`driveCores`) | exclusive | none (static template) | uniform shape, scheduler-placed |
| `numDrives`+`driveCapacity` | drive-sharing | none (cores derived in `allocator.getDriveCores`) | TLC-only |
| `containerCapacity` | drive-sharing | none (cores derived) | split by `driveTypesRatio` |
| `clusterCapacity` | drive-sharing | `PlanCapacity` | whole-cluster target, FD-aware, grows |
| **daemonset** (auto full drives) | exclusive | `PlanAutoFullDrives` | active iff counts + all 3 capacity fields unset (`UsesAutoFullDrives()`); 1 node-pinned container per node taking all its signed drives, expand-only |

CEL both-or-neither: with no capacity field, `computeContainers`/`driveContainers` must be both set or both unset.

Event reasons/severities/throttling: `planner_events.go` (`plannerEventSpecs` table).

Core sizing formulas: `internal/capacityplanner/{cores,hugepages}.go` — drive/compute core arithmetic
(`FullDriveCores`, `RequiredComputeCores`) and hugepages (`DriveContainerHugepagesMiB`,
`ComputeContainerHugepagesMiB`).

Explicit `dynamicTemplate` overrides (cores, hugepages) are enforced by admission validators, not
auto-calculation.

Validators (`internal/validation/`, severities in `internal/admission/defaults.go`):

- `cluster_auto_full_drives_pin_exceeds_node_drives` — pinned cores/drives exceed node's signed drives
- `cluster_auto_full_drives_compute_hugepages` — projected compute hugepages exceed node headroom
- `cluster_auto_full_drives_min_nodes` — role selector matches fewer nodes than min container counts
- `cluster_sizing_mode_flip` — derived mode changed (UPDATE only) while drive containers exist
- `cluster_compute_drive_cores_floor` / `cluster_drive_compute_core_ratio` — compute:drive core ratio floor/advisory
- `cluster_cores_per_container_limit` — pinned cores above `maxCoresPerContainer`

Docs: `doc/operator/deployment/act-as-daemonset.md`, `cluster-capacity.md`.

## Key Interactions

- Creates WekaContainer resources for cluster nodes
- Manages cluster-wide secrets and credentials
- Coordinates NFS/S3 protocol configuration
- Exposes management via K8s services/ingress
