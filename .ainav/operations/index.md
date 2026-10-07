# Operations Navigation

Manual operations, policies, CSI, and driver management.

**Location**: `internal/controllers/operations/`

## CSI Operations

**Path**: `operations/csi/`

| File | Purpose |
|------|---------|
| `controller.go` | CSI controller deployment |
| `daemonset.go` | CSI node server daemonset |
| `driver.go` | CSI driver registration |
| `storageclass.go` | StorageClass creation |
| `utils.go` | Shared utilities |

## Drive Operations

| File | Purpose |
|------|---------|
| `sign_drives.go` | Drive signing; TLC/QLC overrides; `driveExclusions` |
| `block_drives.go` | block-drives/unblock-drives by serial, physical UUID, or virtual UUID |
| `discover_drives.go` | Drive discovery on `SIGN_DRIVES_IMAGE`; skips proxy-signed drives |
| `resign_drives.go` | Force drive re-signing |
| `stale_virtual_drives.go` | Stale virtual drive detection + gated cleanup |
| `rotate_ssdproxy.go` | Rolling ssdproxy image rotation, node by node |
| `kernelize.go` | Ad-hoc hostPID kernelize container before proxy pod (re)creation. See [doc](../../doc/operator/operations/drive-sharing.md) |
| `proxy_disruption_gate.go` | Proxy-node disruption gate; exports `EvaluateClusterHealth` |
| `migrate_drive_sharing.go` | `migrate-to-drive-sharing`: per-cluster full-drives → drive-sharing campaign, one drive container at a time. See [doc](../../doc/operator/operations/migrate-to-drive-sharing.md) |
| `migrate_drive_sharing_types.go` | `migrate-to-drive-sharing` campaign state (phases, sub-phases, `status.result` shape) |
| `full_drives.go` | `RemoveFullDrivesFromNode`: drops serials from `weka.io/weka-full-drives` on a node |

## Driver Operations

| File | Purpose |
|------|---------|
| `load_drivers.go` | Driver loading orchestration |
| `enable_local_drivers_distribution.go` | Local driver dist |

See [driver-distribution.md](../../doc/operator/deployment/driver-distribution.md) for NixOS node support.

## Other Operations

| File | Purpose |
|------|---------|
| `discover_node.go` | Node discovery; recreates discovery container on owner-spec drift (image/tolerations/pullSecret/serviceAccount) |
| `ensure_nics.go` | NIC configuration |
| `trace_session.go` | Remote trace collection |
| `cleanup_persistent_dir.go` | Cleanup |
| `deploy_csi.go` | CSI deployment |
| `operations.go` | Shared operation types |

## Container Sizing

See [drivers-dist-sizing.md](drivers-dist-sizing.md): how `spec.resources`,
`distResources` and `additionalMemory` reach the pod.

## Policies vs Manual Operations

- **WekaPolicy**: Recurring/scheduled operations
  - Controller: `wekapolicy_controller.go`
  - Runs on intervals

- **WekaManualOperation**: One-time operations
  - Controller: `wekamanualoperation_controller.go`
  - Runs once, reports result

## Adding New Operations

1. Create operation file in `operations/`
2. Define struct with `Execute` method
3. Register in WekaPolicy or WekaManualOperation controller
4. See [tasks.md](../tasks.md) for detailed steps
