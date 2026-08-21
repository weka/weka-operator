# WekaClient Controller

Manages client container deployments connecting to existing Weka clusters.

**Location**: `internal/controllers/wekaclient/`

## Main Files

| File | Purpose |
|------|---------|
| `client_controller.go` | Controller setup |
| `client_reconciler_loop.go` | Main reconciliation loop |

## Key Responsibilities

1. **Client Container Deployment**
   - Creates WekaContainer resources for clients
   - Manages selector/toleration matching
   - Handles node scheduling decisions

2. **CSI Ownership**
   - One client owns CSI deployment
   - Manages CSI controller deployment
   - Handles CSI topology labels

3. **Driver Management**
   - Coordinates driver loading
   - New drivers API compatibility

4. **Network Propagation**
   - Propagates network updates to containers

## Interaction with WekaContainer

WekaClient creates WekaContainer resources that are then
reconciled by the WekaContainer controller. The client
controller handles high-level client decisions.

## Key Interactions

- Creates WekaContainer for each client node
- Manages CSI deployment lifecycle
- Propagates network config to containers
- Handles node selector/toleration matching

## Spec Propagation and the Hash Trap

Fields reach existing containers only if they are BOTH on `UpdatableClientSpec` and compared in
`updateContainerIfChanged` (`client_reconciler_loop.go`). Creation-time assignment in
`buildClientWekaContainer` alone is not enough — that path runs once, so a field wired only there
is silently dead on update.

`NewUpdatableClientSpec` is fingerprinted with `util.HashStruct`, which uses **gob**. gob only
encodes exported fields, and `resource.Quantity` keeps its value in unexported ones — so a
`Quantity` on that struct hashes identically no matter its value. `spec.resources` therefore
travels as `ResourcesDigest`, a text rendering, alongside the pointer. `normalizePodResources`
folds an all-zero spec to nil so `resources: {}` and unset hash the same instead of churning the
spec hash every reconcile. Any future `Quantity`-valued field needs the same treatment.

`spec.resources` is a deliberate escape hatch: `applyResourcesOverride` (resources/pod.go) applies
it for EVERY mode and EVERY cpuPolicy, last, after the computed sizing. Setting `resources.cpu` on
an `auto`/`dedicated`/`dedicated_ht` container therefore replaces the `CPURequestCores` value the
capacity planner charges and the DRA claim is sized from — and if only one side of a pair is named
the other follows it, so QoS stays Guaranteed but the planner's node accounting drifts. That is
accepted: naming a resource here means taking responsibility for it.
