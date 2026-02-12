# Dagger CI/CD Pipelines

This directory contains Dagger modules for building and deploying the weka-operator.

## Prerequisites

- [Dagger CLI](https://docs.dagger.io/install) installed
- Docker running locally
- SSH agent with keys for accessing private repositories (if needed)

## Usage

### Deploy Operator


### Local dagger
```bash
dagger call  deploy  --operator=. --sock="$SSH_AUTH_SOCK" \
    --kubeconfig=file:///full/path/to/kubeconfig \
    --operator-repo=quay.io/weka.io/weka-operator-dev \
    --helm-repo=quay.io/weka.io/helm-dev \
    --helm-username=env://QUAY_USERNAME --helm-password=env://QUAY_PASSWORD
```
Modify values for your private repository
Repo username can be omitted if you use private public-write repository, like simplest form of docker registry deployment

### Remote dagger (much quicker cycles if dagger engine, images registry are close one to another, while dev machine is not)
Use `dagger-remote` to run dagger on a remote Kubernetes-hosted dagger engine:

```bash
# Set required environment variables
export DAGGER_RUNNER_POD='your-dagger-pod-name'
export DAGGER_RUNNER_NAMESPACE='infra'  # optional, defaults to 'infra'
export DAGGER_OPERATOR_REPO='your-registry.example.com:5000/weka-operator'
export DAGGER_HELM_REPO='your-registry.example.com:5000/helm'
export KUBECONFIG=/path/to/dagger-runner-cluster.yaml # kubeconfig for the cluster hosting the Dagger runner
# Deploy operator
./dagger-remote call --mod .dagger/src deploy \
    --operator=. \
    --sock="$SSH_AUTH_SOCK" \
    --kubeconfig=file:///path/to/target-cluster-kubeconfig.yaml \ # kubeconfig for the target cluster
    --operator-values=/path/to/operator_values.yaml
```

## Module Structure

```
.dagger/
├── src/
│   ├── operator_flows/     # Main operator CI/CD workflows
│   │   └── main.py
│   ├── containers/         # Container building utilities
│   │   └── builders.py
│   ├── apps/              # Application-specific functions
│   │   └── operator.py
│   └── utils/             # Shared utilities
│       └── github.py
└── README.md
```
