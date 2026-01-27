# weka-operator

Kubernetes operator for managing WEKA clusters on Kubernetes.

## Overview

The weka-operator automates the deployment and management of WEKA distributed storage clusters on Kubernetes. It provides:

- Automated WEKA cluster provisioning and lifecycle management
- Node discovery and container orchestration
- Drive management and cluster scaling
- CSI driver integration for persistent volumes

## Prerequisites

- Kubernetes cluster (v1.26+)
- Helm 3.x
- Container registry access (Quay.io or your own registry)
- WEKA license and credentials

## Installation

### Quick Start (From Quay.io)

1. Create the operator namespace:

```sh
kubectl create namespace weka-operator-system
```

2. Create image pull secret (if using private registry):

```sh
kubectl -n weka-operator-system create secret docker-registry quay-cred \
  --docker-server=quay.io \
  --docker-username=${QUAY_USERNAME} \
  --docker-password=${QUAY_PASSWORD}
```

3. Create WEKA CLI credentials secret:

```sh
kubectl -n weka-operator-system create secret generic weka-cli \
  --from-literal=username=${WEKA_CLI_USERNAME} \
  --from-literal=password=${WEKA_CLI_PASSWORD}
```

4. Install the operator:

```sh
helm upgrade --create-namespace \
    --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
    --namespace weka-operator-system \
    --version ${VERSION}
```

### Installation from Source

Build and deploy from source code:

```sh
# Build the operator
make build

# Deploy to your cluster
make deploy VERSION=dev-$(git rev-parse --short HEAD) REGISTRY_ENDPOINT=your-registry.io/your-namespace
```

## Configuration

### Helm Values

Key configuration options in `charts/weka-operator/values.yaml`:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Operator image repository | `quay.io/weka.io/weka-operator` |
| `image.tag` | Operator image tag | Chart version |
| `imagePullSecret` | Image pull secret name | `quay-io-robot-secret` |
| `deployController` | Deploy the controller | `true` |
| `csi.installationEnabled` | Enable CSI driver | `false` |
| `wekahome.endpoint` | WEKA Home telemetry endpoint | `""` |

See `charts/weka-operator/values.yaml` for all configuration options.

## Development

### Building

```sh
# Build operator binary
make build

# Run tests
make test

# Generate CRDs and manifests
make manifests
```

### Local Development

Run the operator locally against a Kubernetes cluster:

```sh
make run DEPLOY_CONTROLLER=false
```

### Using Dagger

For CI/CD pipelines, the operator includes Dagger modules:

```sh
# Build operator using Dagger
./dagger call --mod .dagger/src/operator_flows build --operator . --sock "$SSH_AUTH_SOCK"
```

See `.dagger/README.md` for more details.

### Docker Socket Issues (macOS)

If you encounter Docker socket errors:

```sh
# Create symlink for Docker socket
sudo ln -s -f /Users/$USER/.docker/run/docker.sock /var/run/docker.sock
```

## Testing

### Unit Tests

```sh
go test ./...
```

### End-to-End Tests

```sh
# Requires a Kubernetes cluster with WEKA environment
go test -v ./test -run TestHappyPath -weka-image "${WEKA_IMAGE}" -cluster-name ${CLUSTER_NAME} -timeout 30m
```

See `test/e2e/README.md` for detailed E2E testing instructions.

## Deploying a WEKA Cluster

After installing the operator, deploy a WEKA cluster using a custom resource:

```yaml
apiVersion: weka.io/v1alpha1
kind: WekaCluster
metadata:
  name: my-weka-cluster
  namespace: weka-operator-system
spec:
  # Cluster configuration
  # See examples/ directory for sample configurations
```

Check the `examples/` directory for sample cluster configurations.

## Releasing

This project uses semantic release and GitHub Actions:

- **Beta Releases**: Merge to `release/v1-beta` branch
- **Production Releases**: Merge to `release/v1` branch

Helm charts and Docker images are published to Quay.io:
- [Helm Repository](https://quay.io/repository/weka.io/helm/weka-operator)
- [Docker Image](https://quay.io/repository/weka.io/weka-operator)

## Documentation

- API Reference: `doc/api_dump/`
- Operator Deployment: `doc/operator/deployment/`
- Architecture: `doc/weka/architecture/`

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Run tests (`make test`)
5. Submit a pull request

## License

See [LICENSE](LICENSE) for details.
