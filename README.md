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
Set image pull secret name if needed
```shell
export QUAY_USERNAME=<quay username>
export QUAY_PASSWORD=<quay password>

kubectl create namespace weka-operator-system
kubectl create secret docker-registry quay-io-robot-secret \
  --docker-server=quay.io \
  --docker-username=$QUAY_USERNAME \
  --docker-password=$QUAY_PASSWORD \
  --docker-email=$QUAY_USERNAME \
  --namespace=weka-operator-system # operator will be scheduling some containers in own namespace

kubectl create secret docker-registry quay-io-robot-secret \
  --docker-server=quay.io \
  --docker-username=$QUAY_USERNAME \
  --docker-password=$QUAY_PASSWORD \
  --docker-email=$QUAY_USERNAME \
  --namespace=default # wekacluster/wekaclient namespaces, that can be different from operator itself, each namespace needs a copy of secret
```
```shell
helm pull oci://quay.io/weka.io/helm/weka-operator --untar --version v1.9.1
kubectl apply -f weka-operator/crds
helm upgrade \
--install weka-operator oci://quay.io/weka.io/helm/weka-operator \
--namespace weka-operator-system \
--create-namespace --version v1.9.1
```

## Configuration

### Helm Values

Key configuration options in `charts/weka-operator/values.yaml`:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `imagePullSecret` | Image pull secret name | `quay-io-robot-secret` |
| `csi.installationEnabled` | Enable CSI driver | `false` |

See [values](charts/weka-operator/values.yaml) for all configuration options.

### Using Dagger

See [dagger](.dagger/README.md) for deploying the operator with Dagger.

### Unit Tests

```sh
go test ./...
```

## API

The operator api is defined in a separate [api](https://github.com/weka/weka-k8s-api) repository.<br>
Any changes to the api requires updating the [api submodule](pkg/weka-k8s-api).

## License

See [LICENSE](LICENSE) for details.
