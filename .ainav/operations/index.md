# Operations Navigation

Manual operations, policies, CSI, and driver management.

**Location**: `internal/controllers/operations/`

## CSI Operations

**Path**: `operations/csi/`

| File | Purpose |
|------|---------|
| `controller.go` | CSI controller deployment |
| `nodeserver.go` | CSI node server pods |
| `daemonset.go` | CSI daemonset management |
| `driver.go` | CSI driver registration |
| `storageclass.go` | StorageClass creation |
| `utils.go` | Shared utilities |

## Drive Operations

| File | Purpose |
|------|---------|
| `sign_drives.go` | Drive signing for weka use |
| `block_drives.go` | Block drives operation |
| `discover_drives.go` | Drive discovery via node-agent |
| `resign_drives.go` | Force drive re-signing |

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
