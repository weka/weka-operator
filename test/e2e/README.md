# End to End Tests

This directory contains end to end tests for the project. These tests are
written in Python and use `kubectl` and the Kubernetes Python client to
interact with the Kubernetes cluster.

To better simulate intended use, cluster objects are defined using YAML
manifest files.

## Prerequisites

### Python Dependencies

The tests require the following tools to be installed:

- python3 (>=3.12)
- poetry

To install the required dependencies, run the following command:

```bash
poetry install
```

### Kubernetes Cluster

The suite expects a running Kubernetes cluster.
At this time, the cluster must:

- Run in OCI
- Have an image pull secret named `quay-cred` in the `weka-operator-e2e-system` namespace
- Have 6 total nodes
- Have 5 nodes with the label `weka.io/role=backend`
- Have 1 node with the label `weka.io/role=builder`
- ... Probably more ...

## Running the Tests

To run the tests, you will need to have a Kubernetes cluster running.
The KUBECONFIG environment variable must be set to the path of the kubeconfig
for this cluster.

```bash
export KUBECONFIG=/path/to/kubeconfig
```

To run the tests, execute the following command:

```bash
make test-e2e
```

These tests will attempt to re-use existing resources if they exist.
If you want to start from a clean slate, you can delete the resources using this command:

```bash
make clean-e2e
```
