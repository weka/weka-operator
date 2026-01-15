# Common Development Tasks

Quick reference for common modifications.

> **Note**: After completing any task, update relevant `.ainav` files if your changes affect navigation (new files, changed purposes, new features).

## Adding a Controller Operation

1. Review existing patterns in `internal/controllers/operations/`
2. Create operation file (e.g., `my_operation.go`)
3. Define operation struct and execute method
4. Wire into controller (WekaPolicy or WekaManualOperation)

See [operations/index.md](operations/index.md) for file organization.

## Modifying Container Lifecycle

1. Identify the state: Active, Deleting, Destroying, Paused
2. Find relevant file:
   - State flow: `wekacontainer/flow_{state}.go`
   - Specific logic: `wekacontainer/funcs_{topic}.go`
3. Follow step-based pattern using `go-steps-engine`

See [controllers/wekacontainer.md](controllers/wekacontainer.md) for file map.

## Adding Configuration Options

1. Add env var to `internal/config/env.go`
2. Add default to `charts/weka-operator/values.yaml`
3. Wire env var in `charts/weka-operator/templates/manager.yaml`

See [config/index.md](config/index.md) for config overview.

## Modifying CRD Types

1. Edit types in `pkg/weka-k8s-api/api/v1alpha1/`
2. Run `make manifests` to regenerate CRDs
3. Run `make generate-api-docs` to update docs

## Adding CSI Functionality

1. Review `internal/controllers/operations/csi/`
2. Controller logic: `controller.go`
3. Node daemonset: `daemonset.go`
4. Storage classes: `storageclass.go`
5. Integration: `wekacontainer/csi_steps.go`

See [operations/index.md](operations/index.md) for CSI details.

## Working with Weka API

1. Main client: `internal/services/weka.go`
2. Cluster ops: `internal/services/weka_cluster.go`
3. Container ops: `internal/services/weka_container.go`
4. Node agent endpoints: `internal/node_agent/node_agent.go`

See [services/index.md](services/index.md) for service overview.
