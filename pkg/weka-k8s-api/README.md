# weka-k8s-api

Weka k8s Custom Resource Definitions (CRDs) and corresponding Go type definitions.

This is a nested Go module (`github.com/weka/weka-operator/pkg/weka-k8s-api`) inside the weka-operator repository. The root module consumes it through a `replace` directive.

## Development

- `make generate manifests` (from the repository root) regenerates deepcopy code, CRDs in `crds/v1alpha1/` and the chart CRDs.
- `make fmt vet` in this directory formats and vets the module.
- `scripts/preview-docs.sh` builds and serves the CRD reference site locally.
