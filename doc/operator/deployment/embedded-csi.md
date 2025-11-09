# Operator-embedded CSI Deployment Flow

## This page explains how the operator deploys embedded CSI plugin components

Starting from version 1.7.0, the Weka Operator features embedded CSI (Container Storage Interface) deployment capabilities. This integration enables automatic deployment and management of CSI components alongside Weka clusters.

The embedded CSI plugin is disabled by default. To enable it, set `csi.installationEnabled: true` in your Helm values or operator configuration.

The embedded CSI plugin will be deployed only when there are running clients.

`NOTE: The embedded CSI plugin is currently supported only for WekaClients with targetCluster.`


The CSI deployment in the operator follows a two-tier architecture:

1. **WekaClient Reconciler** - Deploys:
    - CSI Driver resource
    - StorageClasses (optional)
    - CSI Controller Deployment (optional)
    - CSI Node DaemonSet (runs on all nodes where WekaClient pods would run)
2. **WekaContainer Reconciler** - Manages CSI topology labels on nodes based on container status.

## CSI Driver Naming

The CSI driver name follows specific conventions based on deployment scenario:

### WekaClient With targetCluster

- Target cluster name is used, in form of:
    - `clusterName.clusterNamespace.weka.io`
- To override set on WekaCluster:
    - `spec.csiConfig.csiGroup: groupName` which results in `<groupName>.weka.io`
    - For backward compatibility with separate CSI, set this override to `csi`

### WekaClient with joinIps -- NOT SUPPORTED YET

- `csi.weka.io` by default, a suitable default for single cluster
- To override set on WekaClient:
    - `spec.csiConfig.csiGroup: groupName`, which results in `<groupName>.weka.io`

## StorageClass
2 storage classes are created by default for the CSI driver.
<br>StorageClass names are derived from the CSI group name following this pattern:
- `weka-<groupName>-<fsName>`, where default is a filesystem name.
- Non-standard mount options are also reflected in the name, for example:
`weka-<groupName>-<fsName>-forcedirect`

To disable StorageClass creation, set `csi.storageClassCreationDisabled: true` in your Helm values or operator configuration.


## CSI Controller Deployment
The CSI controller deployment is created by default.
To disable the CSI controller deployment, set in the WekaClient `spec.csiConfig.disableControllerCreation: true`.

## Additional Configuration:
- Operator Values Configuration Options
  - csi plugin namespace: `csi.namespace`
  - csi image: `csi.image`
- Client csiConfig Configuration Options
  - controllerLabels
  - nodeLabels
  - controllerTolerations
  - nodeTolerations

When `csi.installationEnabled` is switched from true to false, the operator automatically removes all CSI resources. The reverse process occurs when switching from false to true.

## Deployment Flow

### 1. Client Reconciler - DeployCsiPlugin

The deployment begins with the `deployCsiPlugin` function in the client reconciler:

1. Execute the operation, which:
   - Deploys the CSI Driver resource
   - Creates default StorageClasses
   - Deploys the CSI Controller deployment
   - Deploys the CSI Node DaemonSet
2. Update container specs with the CSI parameters
3. Mark CSI as deployed in the WekaClient status

### 2. Container Reconciler - CSI Topology Labels

For each client WekaContainer, the container reconciler:

1. Monitors the container status and active mounts
2. Manages CSI topology labels on the node to control volume scheduling
3. Updates labels based on container health and mount state

## Node Selection and Resource Inheritance

- **CSI Driver and StorageClass**: Global resources, not tied to specific nodes
- **CSI Controller**: Inherits NodeSelector and Tolerations from the WekaClient
- **CSI Node DaemonSet**:
  - Runs on all nodes where WekaClient pods would run
  - Inherits NodeSelector and Tolerations from the parent WekaClient
  - Managed as a single DaemonSet per WekaClient (not individual pods)

The DaemonSet ensures that CSI node functionality is available on every node that can run Weka client containers.

## Update and Delete Flow

### Configuration Changes

The operator detects CSI configuration changes through the `CheckCsiConfigChanged` function:

1. Computes a hash of the current CSI configuration
2. Compares with the previously recorded hash
3. If the CSI driver name has changed, undeploy and redeploys all CSI components
4. If other configurations have changed, updates only the affected components

### CSI Node DaemonSet Updates

The `UpdateCsiNodeDaemonSet` function ensures the CSI node DaemonSet is up-to-date:

1. Compares the existing DaemonSet annotation hash with the desired configuration hash
2. If mismatched, updates the DaemonSet with the new configuration
3. Kubernetes automatically rolls out the updated DaemonSet pods

This handles scenarios like:
- CSI driver name changes
- Toleration or NodeSelector updates
- Image version changes
- Other DaemonSet specification changes

## Lifecycle Management

Deleting CSI resources (driver, StorageClass, controller, node server) does not affect existing mounts, PVCs, or PVs. It only prevents the provisioning and mounting of new volumes.

This makes it safe to delete and recreate CSI resources with updated specifications as needed.

## Common Operations

### Monitoring CSI Deployment Status

Check the status of the CSI deployment on a WekaClient:

```bash
kubectl get wekaclient <client-name> -n <namespace> -o jsonpath='{.status.csiDeployed}'
```

### Viewing Deployed StorageClasses

List all StorageClasses created by the operator:

```bash
kubectl get storageclass | grep "weka-io"
```

### Viewing CSI Controller Deployment

Check the status of the CSI controller deployment:

```bash
kubectl get deployment -n <csi-namespace> | grep "csi-controller"
```

### Viewing CSI Node DaemonSet

View the CSI node DaemonSet:

```bash
kubectl get daemonset -n <csi-namespace> | grep "csi-node"
```

List all CSI node DaemonSet pods:

```bash
kubectl get pods -n <csi-namespace> -l component=<csi-group>-weka-csi-node
```


## Migration from Separate CSI to Embedded CSI
- Upgrade the operator to 1.7.0 or later (`csi.installationEnabled: false` by default)
- Undeploy the separate CSI
- Assuming the separate CSI was deployed with the default csi driver name `csi.weka.io`, set the WekaCluster `spec.csiConfig.csiGroup: csi`
- Install the operator with `csi.installationEnabled: true`