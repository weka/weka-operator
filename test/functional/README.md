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

1. Stand-up your development cluster.
2. Set the `KUBECONFIG` environment variable to point to your development cluster.

- Verify with `kubectl get nodes`

3. (Optional) Ensure that your cluster is in a clean state.

- See `./script/clean-testing.sh` script.
- This script will attempt to clean up all resources and namespaces that will be touched by the test suite.

4. Run the test suite: `make test-functional`

## Caveats

Functional test suites are, by their nature, less reliable than unit tests.
Occasionally, pods will get into an unexpected state and cause the test suite to stall.
If this happens, you can usually kill the pod manually and allow the suite to progress.

Sometimes, custom resource instances get stuck deleting.
You can force them to be deleted by editing (keyboard `e`) the resouce in k9s and removing the finalizer.
