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
# Install CRDs using server-side apply
helm show crds oci://quay.io/weka.io/helm/weka-operator --version v1.9.1 | \
  kubectl apply --server-side -f -

# Install operator
helm upgrade --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
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

# Dev flows

### Building and pushing weka-operator image and helm with docker only

```sh
helm registry login quay.io -p xxx -u xxx \
  && docker login quay.io -p xxx -u xxx \
  && VERSION=1.11.0-$USER-dev.0 REPO=quay.io/weka.io/weka-operator-dev HELM_REPO=quay.io/weka.io/helm-dev"\ 
   ./build-release.sh
```
## installing dev release using helm 
```sh
export REPO=quay.io/weka.io/weka-operator-dev 
export HELM_REPO=quay.io/weka.io/helm-dev
export VERSION=v1.11.0-$USER-dev.0; helm show crds oci://$HELM_REPO/weka-operator --version $VERSION | kubectl apply --server-side -f -
helm upgrade \
--install weka-operator oci://$HELM_REPO/weka-operator \
--namespace weka-operator-system \
--create-namespace --version $VERSION --set image.repository=$REPO
```
### Building, pushing and deploying Using Dagger, a quick dev flow supporting remote building 

See [dagger](.dagger/README.md) for deploying the operator with Dagger.

### Unit Tests

```sh
go test ./...
```

## Examples

See [doc/examples](doc/examples) for YAML configurations including clusters, clients, drive sharing, and policies.

## API

The operator api is defined in a separate [api](https://github.com/weka/weka-k8s-api) repository.<br>
Any changes to the api requires updating the [api submodule](pkg/weka-k8s-api).

## License

See [LICENSE](LICENSE) for details.
