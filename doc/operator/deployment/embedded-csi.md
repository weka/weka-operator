# Operator-embedded CSI Deployment Flow

## This page explains how the operator deploys embedded CSI plugin components

Starting from version 1.7.0, the Weka Operator features embedded CSI (Container Storage Interface) deployment capabilities. This integration enables automatic deployment and management of CSI components alongside Weka clusters.

The embedded CSI plugin is enabled by default. To disable it, set `csi.installationEnabled: false` in your Helm values or operator configuration.

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

### Controller placement and NotReady nodes

The controller (2 replicas) runs on nodes matching the WekaClient's `nodeSelector`. It leaves a node that goes
NotReady or unreachable after 10 seconds, because it always carries `not-ready` / `unreachable` NoExecute
tolerations with `tolerationSeconds: 10`; the Deployment starts a replacement elsewhere.

With `scheduling.clientsTolerateAll` the controller also tolerates every taint (`operator: Exists`), and a required
`weka.io/not-ready DoesNotExist` node requirement keeps it off nodes the operator's Node controller labelled NotReady
(see [Tolerations: NotReady nodes](tolerations.md#notready-nodes-and-cluster-unique-pods)).

To run the controller on other nodes than the clients, for example when two WekaClients with different
`nodeSelector`s share one CSI group, set `disableControllerCreation: true` on one of them.

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

The `topology.<csi-driver>/accessible` label is evaluated for every client container on each reconcile. The
first matching row decides:

| # | Signal | Result |
|---|--------|--------|
| 0 | The node's `weka.io/drivers-loaded` boot ID is missing or differs from the node's boot ID | remove |
| 1 | Node NotReady, or this client's own pod is missing, terminating or not `Running` | remove |
| 2 | The CSI node pod of this WekaClient is missing on the node or not `Ready` | remove |
| 3 | The WekaContainer status says WEKA reports the container not serving: `status.internalStatus` (written only from a `weka local ps` reply) is set and not `READY`, or (only when `CSI_PREVENT_NEW_WORKLOAD_ON_CLIENT_CONTAINER_NOT_RUNNING` is on) `status.status` is not `Running` | remove |
| 4 | Usable stats report processes or driver frontends not all connected | remove |
| 5 | Usable stats report all processes active and all driver frontends connected | set, no node-agent call |
| 6 | No usable stats (stale, disabled, from another boot, or from before the node last became Ready): node-agent `/getActiveMounts` reports `frontend_connected` | set |
| 7 | node-agent reports the frontend not connected, or no driver interface | remove |
| 8 | node-agent unreachable or unable to tell, for less than `unknownTimeout` since the first failure | keep the current label, never add it |
| 9 | Unknown for at least `unknownTimeout`, and at least `systemicFailureThreshold` percent of the WekaClient's nodes evaluated in the last 2 × `unknownTimeout` are unknown, whatever their streak length (at least 3 such nodes; fewer are never systemic) | keep the current label: the operator, its network or the Kubernetes API is the likely culprit |
| 10 | Unknown for at least `unknownTimeout`, below the threshold | remove |

Notes:

- Stats are usable only when they are younger than `statsFreshness`, were sampled under the node's current boot (the node-agent stamps them with the
  node boot ID, `bootID`) and are newer than the node's last Ready transition. Stats from before a reboot or outage
  are ignored; a node-agent that does not report `bootID` is treated the same way.
- The process counts in stats are trusted only if the node-agent pulled them from the container within
  `statsFreshness` and after the node's last Ready transition (`processesPulledAt`). An older pull, or a node-agent
  that does not report it, makes them unknown, so usable stats then neither set nor remove on them and the
  node-agent check decides. The driver frontend
  counts are read live and are not gated.
- Stats are an optimisation: with container metrics disabled the label is decided by rows 0-3 and 6-10.
- Driver frontend state is counted per WEKA client container. The node-agent reads the global
  `/proc/wekafs/interface` and counts only the `Container=<that container's WEKA name> FE <n>: ...` lines, so several
  WekaClients on one node (for example from different clusters) are judged independently. A container is connected
  when it has at least one such line and every one reads `Connected frontend`. A container with no frontend lines is
  "unknown" in stats and "not connected" from the node-agent's `/getActiveMounts`. Stats from a node-agent that does
  not send the driver frontend count are treated as unknown for that part and row 6 applies.
- Row 3 reads the WekaContainer status. `status.internalStatus` changes only on a real `weka local ps` reply, so a
  failed attempt (timeout, Kubernetes API error, refused exec) leaves it as it was. `status.status` of `Error` is
  written by the operator when a step fails and never by WEKA, and `Unknown` means WEKA cannot read its own state;
  neither removes the label by itself, and the evaluation falls through to the stats and node-agent rows. Any other
  status (`Stopped`, `Starting`, `Unhealthy`, `PodNotRunning`, ...) removes it.
- Healthy nodes count in row 9 only if they were reconciled within 2 × `unknownTimeout`, so raising the requeue
  interval past that weakens the systemic check.
- Row 9 compares two counts kept in the failure tracker, one entry per node of the WekaClient: nodes whose latest
  evaluation was unknown, and all nodes evaluated (set or unknown). A node decided by a reliable signal (rows 0-3, or
  the node-agent reporting the frontend not connected) is dropped from the tracker, so excluded nodes never make a
  failure look systemic. Containers without a WekaClient are never correlated.
- The CSI node pod is looked up through the DaemonSet's `app` label from the operator's own pod cache (the same
  watch the container controller already runs), so it makes no API call and adds no staleness. A missing pod, a pod
  that is not `Ready`, or one that is terminating is unhealthy; a failed lookup is no signal.
- The failure tracker (rows 8-10) is kept in memory. An operator restart only delays a removal. A node's streak start
  lasts until something proves it healthy (a success, a reliable signal, or the container's cleanup), however far apart
  its evaluations are, so a node that was unknown long ago is past the timeout at once when it fails again.
- During an upgrade, until the node-agents run the new version their stats carry no `bootID`, so every evaluation
  asks the node-agent over HTTP instead of using stats. That is correct, and only adds node-agent traffic until the
  rollout finishes.
- The mount count is best effort when publishing stats: if it cannot be read, the sample (process and frontend
  counts) is still published and stamped, the previous mount count is kept, and the error is reported.
- When driver loading finds the boot ID changed, it also removes every `topology.*.weka.io/accessible` label on the
  node (all CSI groups) before it continues. An image change alone does not.
- A CSI node pod that is terminating counts as not ready.
- The operator never deletes CSI node pods. The only exception is the upgrade cleanup of pre-DaemonSet pods, which
  never touches DaemonSet pods.

The thresholds are set in the configuration WekaPolicy under `csi`:

| Setting | Default | Meaning |
|---------|---------|---------|
| `statsFreshness` | `2m` (minimum `30s`) | Age up to which container stats decide the label |
| `unknownTimeout` | `5m` (minimum `30s`) | How long an unreachable node keeps its label as it is |
| `systemicFailureThreshold` | `80` (1-100) | Percentage of failing nodes at which failures count as systemic |

Failed stats polls on stale stats emit a throttled `StatusMetricsFailed` warning event on the WekaContainer.

## Node Selection and Resource Inheritance

- **CSI Driver and StorageClass**: Global resources, not tied to specific nodes
- **CSI Controller**: Inherits NodeSelector and Tolerations from the WekaClient, plus the placement rules above
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
- Upgrade the operator to 1.7.0 or later, setting `csi.installationEnabled: false` so the embedded CSI does not come up alongside the separate one
- Undeploy the separate CSI
- Assuming the separate CSI was deployed with the default csi driver name `csi.weka.io`, set the WekaCluster `spec.csiConfig.csiGroup: csi`
- Install the operator with `csi.installationEnabled: true`