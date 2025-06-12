# Operator-embedded CSI Deployment Flow

## This page explains how the operator deploys embedded CSI plugin components

Starting from version 1.7.0, the Weka Operator features embedded CSI (Container Storage Interface) deployment capabilities. This integration enables automatic deployment and management of CSI components alongside Weka clusters.

The CSI deployment in the operator follows a two-tier architecture:

1. **WekaClient Reconciler** - Deploys:
    - CSI Driver resource
    - StorageClasses
    - CSI Controller Deployment
2. **WekaContainer Reconciler** - Deploys CSI node pods alongside each client WekaContainer on the same Kubernetes node.

## CSI Driver Naming

The CSI driver name follows specific conventions based on deployment scenario:

### No weka cluster

- `csi.weka.io` by default, a suitable default for single cluster
    - To override: `csiGroup` on client level, in form of:
        - `csiGroup: groupName`, which results in `csi.group-name.weka.io`
        - In multiple clusters scenario, the first cluster typically won't have this setting, while subsequent clusters will require a custom csiGroup

### With targetCluster

- Target cluster name is used, in form of:
    - `csi.cluster-name-namespace.weka.io`
- To override:
    - `spec.csiConfig.csiDriverName` (will use the value "as is")
    - For backward compatibility with separate CSI, set this override to `csi.weka.io`

## StorageClass Naming

StorageClass names are derived from the CSI driver name following this pattern:
`storageclass-csi-whatever-weka-io-default`, where default is a filesystem name.

Non-standard mount options are also reflected in the name, for example:
`storageclass-csi-whatever-weka-io-default-mountdirect`

## Enabling the CSI Plugin

To enable the embedded CSI plugin, set `csi.installationEnabled: true` in your Helm values or operator configuration.
In case this method is used - no need to install CSI separately as might be instructed in other documentation.

```yaml
# Example: enabling CSI in Helm values
csi:
  installationEnabled: true # Feature flag to enable/disable embedded CSI
```

## Additional Configuration

The configurable options in the `values.yaml` are as follows, defaults might differ. No need to override anything unless there are specific requirements

```yaml
csi:
  namespace: weka-operator-system  # Namespace where CSI components will be deployed
  driverVersion: "2.7.2"           # Version of the CSI driver
  image: "quay.io/weka.io/csi-wekafs:v2.7.2"  # CSI driver image
  provisionerImage: "registry.k8s.io/sig-storage/csi-provisioner:v5.1.0"
  attacherImage: "registry.k8s.io/sig-storage/csi-attacher:v4.8.0"
  livenessProbeImage: "registry.k8s.io/sig-storage/livenessprobe:v2.15.0"
  resizerImage: "registry.k8s.io/sig-storage/csi-resizer:v1.13.1"
  snapshotterImage: "registry.k8s.io/sig-storage/csi-snapshotter:v8.2.0"
  registrarImage: "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.13.0"
```

When `csi.installationEnabled` is switched from true to false, the operator automatically removes all CSI resources. The reverse process occurs when switching from false to true.

## Deployment Flow

### 1. Client Reconciler - DeployCsiPlugin

The deployment begins with the `DeployCsiPlugin` function in the client reconciler:

1. Determine CSI driver name based on configuration
2. Create a deployment operation
3. Execute the operation, which:
   - Deploys the CSI Driver resource
   - Creates default StorageClasses
   - Deploys the CSI Controller deployment
4. Update container specs with the CSI driver name
5. Mark CSI as deployed in the WekaClient status

### 2. Container Reconciler - DeployCsiNodeServerPod

For each client WekaContainer, the container reconciler:

1. Checks if a CSI node server pod exists for the container's node
2. If not, creates a new CSI node server pod
3. If one exists but is outdated (different driver name or tolerations), recreates it

## Node Selection and Resource Inheritance

- **CSI Driver and StorageClass**: Global resources, not tied to specific nodes
- **CSI Controller**: Inherits NodeSelector and Tolerations from the WekaClient
- **CSI Node Server Pods**: 
  - Automatically deployed alongside client containers on each node
  - Inherit tolerations from the parent WekaClient
  - Does not inherit NodeSelector, as scheduled by specific node affinity rules

When a client container is deleted (due to node tainting, NodeSelector mismatch, etc.), its CSI node server pod is automatically removed.

## Update and Delete Flow

### Configuration Changes

The operator detects CSI configuration changes through the `CheckCsiConfigChanged` function:

1. Computes a hash of the current CSI configuration
2. Compares with the previously recorded hash
3. If the CSI driver name has changed, undeploys and redeploys all CSI components
4. If other configurations have changed, updates only the affected components

### Node Server Updates

The `CheckAndDeleteOutdatedCsiNode` function ensures CSI node servers are up-to-date:

1. Compares CSI driver name and tolerations of existing pod with desired configuration
2. If mismatched, deletes the pod so it can be recreated with updated settings
3. The container reconciler then recreates it with the correct configuration

This handles scenarios like:
- CSI driver name changes
- Toleration updates
- Other pod specification changes

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

### Viewing CSI Node Server Pods

List all CSI node server pods:

```bash
kubectl get pods -n <csi-namespace> | grep "csi-node"
```
