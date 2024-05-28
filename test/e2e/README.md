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

You will need to create a cluster and run the tests as described in the [Operator Dev Flow](https://www.notion.so/wekaio/Operator-Dev-Flow-df4e43f07ad14618beff57bbcd80e0c9) documents in Notion.
