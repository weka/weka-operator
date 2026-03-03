## Environment Variables

The following environment variables are automatically passed to all hook scripts:

| Variable | Description |
|----------|-------------|
| `CLUSTER_NAME` | Name of the Weka cluster |
| `CLUSTER_NAMESPACE` | Kubernetes namespace where resources are deployed |
| `CLIENT_NAME` | Name of the Weka client |
| `INITIAL_VERSION` | Initial version of the Weka software |
| `NEW_VERSION` | Target version for the upgrade |
| `KUBECONFIG` | Path to kubeconfig file |
| `OPERATOR_VERSION` | Version of the Weka operator |
