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
