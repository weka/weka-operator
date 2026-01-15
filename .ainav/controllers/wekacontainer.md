# WekaContainer Controller

Most active controller. Manages per-node Weka container lifecycle.

**Location**: `internal/controllers/wekacontainer/`

## State Machine (flow_*.go)

Container states and their flow files:

| State | File | Description |
|-------|------|-------------|
| Active | `flow_active_state.go` | Running container operations |
| Deleting | `flow_deleting_state.go` | Graceful shutdown |
| Destroying | `flow_destroying_state.go` | Force removal |
| Paused | `flow_paused_state.go` | Suspended operations |

## Function Categories (funcs_*.go)

| File | Purpose |
|------|---------|
| `funcs_pod_ensure.go` | Pod creation/updates |
| `funcs_pod_termination.go` | Pod shutdown handling |
| `funcs_drivers.go` | Driver loading state |
| `funcs_drives.go` | Drive management |
| `funcs_cluster_joining.go` | Cluster join process |
| `funcs_active_mounts.go` | Mount management |
| `funcs_active_state_nfs.go` | NFS operations |
| `funcs_active_state_s3.go` | S3 operations |
| `funcs_common_deletion.go` | Shared deletion logic |
| `funcs_image_upgrade.go` | Image upgrade handling |
| `funcs_weka_local_status.go` | Local weka status |
| `funcs_getters.go` | State getters |
| `funcs_get_node_agent.go` | Node agent lookups |
| `funcs_management_ips.go` | Management IP handling |
| `funcs_resources_allocation.go` | Resource allocation |
| `funcs_handle_node_statuses.go` | Node status handling |
| `funcs_status_updates.go` | Status field updates |
| `funcs_events.go` | K8s event emission |
| `funcs_migrations.go` | Data migrations |
| `funcs_oneoff.go` | One-off operations |
| `funcs_not_used.go` | Deprecated/unused |

## CSI Integration

- `csi_steps.go` - CSI lifecycle steps within container
- Coordinates with `operations/csi/` for actual CSI resources

## Metrics

- `metrics_steps.go` - Metrics collection steps

## Key Interactions

- Creates/manages Pods via `resources/pod.go`
- Communicates with Weka via `services/weka.go`
- Coordinates with node-agent for local operations
- Integrates with CSI for storage provisioning
