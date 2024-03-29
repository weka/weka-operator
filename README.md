# weka-operator

*Caution* This is still a prototype/demonstration

## Description

## Getting Started

### Building

Build the operator:
```sh
make
```

#### Trouble Shooting

##### `docker build failed: docker buildx is not set to default context - please switch with 'docker context use default'`

This error occurs when the Docker build context is not set to the default context.
```sh
docker context use default
```

##### `docker build failed: failed to build quay.io/weka.io/weka-operator:latest: exit status 1: ERROR: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?`

In this case, the Docker daemon is running, but the docker socket is on the wrong page.
This occurs in some versions of Docker Desktop for MacOS because the Docker socket is under `$HOME`.

To fix this, create a symlink from the default location to the actual location:
```sh
sudo ln -s -f /Users/$USER/.docker/run/docker.sock /var/run/docker.sock
```

### Creating a Development Cluster

Create a cluster in OCI:
```sh
./teka lab provision --size 5 SYSNAME --os ubuntu22 --env oci --oci-cpus 4 --oci-mem 30 --force && ./teka install SYSNAME --dont-clusterize
```

And setup Kubernetes:
```sh
./teka kube explore SYSNAME
cmd('weka local stop; systemctl disable weka-agent').L
setup_k3s_cluster()
```

SCP the kubeconfig to your local machine:
```sh
scp root@SYSTEMNAME: /tmp/kube-SYSTEMNAME ~/.kube/config-SYSTEMNAME
export KUBECONFIG=~/.kube/config-SYSTEMNAME
```

### Running on the cluster in development mode

Build and run the operator:
```sh
make run DEPLOY_CONTROLLER=false
```

Deploy the cluster CRD:
```sh
kubectl apply -f examples/oci_cluster.yaml
```

## Releasing

This project uses semantic release and GitHub Actions to automate releases.
To create a new release, simply merge your changes to the `main` branch.
Once the actions workflows complete, a new release will be created under the releases tab in GitHub.
The Helm chart and Docker image will be published to Quay.io.
See the [Helm Repository](https://quay.io/repository/weka.io/helm/weka-operator) and the [Docker Image](https://quay.io/repository/weka.io/weka-operator).

## Deployment

### Prerequisites

#### Operator Namespace

The operator should be deployed in the `weka-operator-system` namespace.

```yaml
---
apiVersion: v1
kind: namespace
metadata:
    name: weka-operator-system
```

#### Image Pull Secret

Create a secret with the Quay.io credentials.
These credentials are used to pull the operator image.

Using YAML:
```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: quay-cred
  namespace: weka-operator-system
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: ${BASE64_ENCODED_DOCKER_CONFIG_JSON}
```

Using `kubectl`:
```sh
kubectl -n weka-operator-system create secret docker-registry quay-cred \
  --docker-server=quay.io \
  --docker-username=${QUAY_USERNAME} \
  --docker-password=${QUAY_PASSWORD}
```

#### Weka CLI Credentials

Create a secret with the Weka CLI credentials.
These are used by operator containers that invoke the Weka CLI.
The credentials must match the credentials set on the Weka cluster.

Using YAML:
```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: weka-cli
  namespace: weka-operator-system
type: kubernetes.io/basic-auth
stringData:
  username: ${WEKA_CLI_USERNAME}
  password: ${WEKA_CLI_PASSWORD}
```

### Installing

#### Install from Helm Repository (quay.io)

```sh
helm upgrade --create-namespace \
    --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
    --version ${VERSION}
```

#### Local Installation

Install using Helm:
```sh
helm upgrade --install weka-operator charts/weka-operator \
    --namespace $(NAMESPACE) \
    --values charts/weka-operator/values.yaml \
    --create-namespace \
    --set "prefix=weka-operator,image.repository=quay.io/weka.io/weka-operator,image.tag=$(VERSION)" \
    --set deployController=true
```

Deploy a Weka cluster using the CRD in the same manner as the example above.
