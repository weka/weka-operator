# Functional Test Suite

This is a functional test suite for the Weka Operator.
It is intended as a developer tool to be used against local development builds of the operator.

## Architecture

The suite is written using the Go STDLB testing framework.
It uses `envtest` to run the tests against a Kubernetes cluster.
The `TestWekaCluster` test case is the entrypoint.
All test cases are `t.Run` style sub-tests.

This suite will attempt to re-use any existing resources.
If this is undesirable, you can remove individual resources as needed.

## Usage

### Stand-up your development cluster.

Bliss is now the preferred way to build a development cluster in AWS.
The provided script `./script/bliss` is a wrapper around this tool.

Create a cluster:

```bash
./script/bliss create
```

Destroy a cluster:

```bash
./script/bliss destroy
```

### Set KUBECONFIG

Set the `KUBECONFIG` environment variable to point to your development cluster.
If the cluster was created with bliss, the command's output will provide the correct `KUBECONFIG` value.

Example:

```bash
export KUBECONFIG=/Users/$USER/<cluster>-34.245.232.162.yaml
```

Verify with `kubectl get nodes`.
This should return a list of 6 nodes in your cluster (1 builder and 5 backends).

### Clean cluster

(Optional) Ensure that your cluster is in a clean state.

- See `./script/clean-testing.sh` script.
- This script will attempt to clean up all resources and namespaces that will be touched by the test suite.

_WARNING_: If you provide the `--remove-namespaces` flag, it will delete all namespaces in the cluster.
This includes the generated configmaps describing the node.
This will break the operator.

### Run the test suite

Run the test suite: `make test-functional RUN=TestWekaCluster`

## Caveats

Functional test suites are, by their nature, less reliable than unit tests.
Occasionally, pods will get into an unexpected state and cause the test suite to stall.
If this happens, you can usually kill the pod manually and allow the suite to progress.

Sometimes, custom resource instances get stuck deleting.
You can force them to be deleted by editing (keyboard `e`) the resouce in k9s and removing the finalizer.
