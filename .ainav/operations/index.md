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
| `sign_drives.go` | Drive signing for weka use; TLC/QLC type overrides |
| `block_drives.go` | block-drives/unblock-drives: serial, physical UUID (evicts every VID on that physical), and virtual UUID (evicts one VID only, no capacity recompute) |
| `discover_drives.go` | Drive discovery. Runs on `SIGN_DRIVES_IMAGE` (needs `weka-sign-drive` for TLC/QLC typing) |
| `resign_drives.go` | Force drive re-signing |
| `stale_virtual_drives.go` | Stale virtual drives detection + gated cleanup. |
| `rotate_ssdproxy.go` | Rolling ssdproxy image rotation, one node at a time |
| `proxy_disruption_gate.go` | Health gate before disrupting a shared proxy node |

## Driver Operations

| File | Purpose |
|------|---------|
| `load_drivers.go` | Driver loading orchestration |
| `enable_local_drivers_distribution.go` | Local driver dist |

## Other Operations

| File | Purpose |
|------|---------|
| `discover_node.go` | Node discovery |
| `ensure_nics.go` | NIC configuration |
| `trace_session.go` | Remote trace collection |
| `cleanup_persistent_dir.go` | Cleanup operations |
| `deploy_csi.go` | CSI deployment coordination |
| `operations.go` | Shared operation types |

## Container Sizing

See [drivers-dist-sizing.md](drivers-dist-sizing.md) for how `spec.resources`,
`driverDistPayload.distResources` and `additionalMemory` reach the pod, and what
overriding them costs.

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
