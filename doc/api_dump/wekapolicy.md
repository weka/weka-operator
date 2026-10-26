# WekaPolicy

## API Types

- [WekaPolicy](#wekapolicy)
- [WekaPolicySpec](#wekapolicyspec)
- [WekaPolicyStatus](#wekapolicystatus)
- [WekaPolicyList](#wekapolicylist)
- [PolicyPayload](#policypayload)
- [TypedPolicyStatus](#typedpolicystatus)
- [SignDrivesPayload](#signdrivespayload)
- [SchedulingConfigPayload](#schedulingconfigpayload)
- [DiscoverDrivesPayload](#discoverdrivespayload)
- [EnsureNICsPayload](#ensurenicspayload)
- [DriverDistPayload](#driverdistpayload)
- [RemoteTracesSessionConfig](#remotetracessessionconfig)
- [CleanStaleVirtualDrivesPayload](#cleanstalevirtualdrivespayload)
- [ConfigurationPayload](#configurationpayload)
- [DistServiceStatus](#distservicestatus)
- [PCIDevices](#pcidevices)
- [SignOptions](#signoptions)
- [DriveTypeOverrides](#drivetypeoverrides)
- [DriveExclusions](#driveexclusions)
- [PodResourcesSpec](#podresourcesspec)
- [ObjectReference](#objectreference)
- [CsiSpec](#csispec)
- [DriversSpec](#driversspec)
- [SchedulingSpec](#schedulingspec)
- [PodRotationSpec](#podrotationspec)
- [NodeAgentSpec](#nodeagentspec)
- [DriveTypeOverrideRule](#drivetypeoverriderule)
- [DriveExclusionRule](#driveexclusionrule)
- [PodResources](#podresources)
- [NodeAgentDevicePluginSpec](#nodeagentdevicepluginspec)

---

## WekaPolicy

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaPolicySpec |  |
| status | WekaPolicyStatus |  |

---

## WekaPolicySpec

| JSON Field | Type | Description |
|------------|------|-------------|
| type | WekaPolicyType |  |
| payload | PolicyPayload |  |
| image | *string |  |
| imagePullSecret | *string |  |
| tolerations | []v1.Toleration |  |
| serviceAccountName | string |  |

---

## WekaPolicyStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| status | string |  |
| result | string |  |
| lastRunTime | metav1.Time |  |
| progress | string |  |
| typedStatus | *TypedPolicyStatus |  |

---

## WekaPolicyList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaPolicy |  |

---

## PolicyPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| signDrivesPayload | *SignDrivesPayload |  |
| schedulingConfigPayload | *SchedulingConfigPayload |  |
| discoverDrivesPayload | *DiscoverDrivesPayload |  |
| ensureNICsPayload | *EnsureNICsPayload |  |
| driverDistPayload | *DriverDistPayload |  |
| remoteTracesSessionPayload | *RemoteTracesSessionConfig |  |
| cleanStaleVirtualDrivesPayload | *CleanStaleVirtualDrivesPayload |  |
| configurationPayload | *ConfigurationPayload |  |
| interval | metav1.Duration |  |
| waitForPolicies | []string |  |

---

## TypedPolicyStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| distService | *DistServiceStatus |  |

---

## SignDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| type | string |  |
| nodeSelector | map[string]string |  |
| devicePaths | []string |  |
| deviceSerials | []string | DeviceSerials sign drives by serial ID. Used with type device-serials. |
| pciDevices | *PCIDevices | PCI vendor and device IDs of the drives to sign.<br>To get the values for VendorId and DeviceId:<br>1. Run the following command to list all PCI devices on your system:<br>```bash<br>lspci -nn<br>```<br>2. Find the relevant PCI device in the output, which will display both the<br>vendor and device IDs in square brackets in the format [vendorId:deviceId].<br>For example:<br>```<br>00:1f.0 Non-Volatile memory controller [0108]: Amazon.com, Inc. NVMe SSD Controller [1d0f:cd01]<br>``` |
| options | *SignOptions |  |
| shared | bool | Shared enables shared drive signing for proxy mode (defaults to false).<br>When enabled:<br>- Drives are signed for proxy using 'weka-sign-drive sign proxy' command<br>- Drives are signed with a proxy system GUID<br>- Results are stored in weka.io/shared-drives annotation (instead of weka.io/weka-drives)<br>- Physical UUIDs, serial IDs, and capacities are captured<br>- Enables multi-tenant drive sharing via SSD proxy |
| driveTypeOverrides | *DriveTypeOverrides | DriveTypeOverrides forces the reported TLC/QLC type for matching drives instead of<br>deriving it from the drive's IU size. Only meaningful when Shared is true.<br>Persisted on the node in the weka.io/drive-type-overrides annotation and re-applied<br>on every later sign-drives run. Omit the field to keep whatever is already persisted<br>on the node; set an empty rules list to clear all overrides. |
| driveExclusions | *DriveExclusions | DriveExclusions skips matching drives when signing, in both full-drives and shared mode.<br>Type is matched against the IU-size-derived type, before DriveTypeOverrides apply. |

---

## SchedulingConfigPayload

---

## DiscoverDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| nodeSelector | map[string]string |  |

---

## EnsureNICsPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| type | NICType |  |
| nodeSelector | map[string]string |  |
| dataNICsNumber | int |  |

---

## DriverDistPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| ensureImages | []string | EnsureImages is a list of Weka images for which drivers should be proactively built. |
| nodeSelectors | []map[string]string | NodeSelectors is a list of node selectors. Nodes matching any of these selectors will be considered for driver building.<br>If empty, all nodes in the cluster are considered. |
| kernelLabelKey | *string | KernelLabelKey is the custom label key to use for storing the node's kernel version.<br>If not specified, "weka.io/kernel" will be used. |
| architectureLabelKey | *string | ArchitectureLabelKey is the custom label key to use for storing the node's architecture.<br>If not specified, "weka.io/architecture" will be used. |
| osLabelKey | *string | OsLabelKey is the custom label key to use for storing the node's os.<br>If not specified, "weka.io/os" will be used. |
| builderImageOverride | string | BuilderImageOverride is an optional image that you can specify for the builder |
| builderPreRunScript | *string | BuilderPreRunScript is an optional script to run on builder containers after kernel validation. |
| distNodeSelector | map[string]string | DistNodeSelector is the node selector for the drivers distribution (dist) container.<br>If not specified, the dist container will be scheduled on any available node. |
| distResources | *PodResourcesSpec | DistResources overrides the pod resources of the drivers distribution (dist) container.<br>Fields left empty keep the built-in sizing. |

---

## RemoteTracesSessionConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| cluster | ObjectReference |  |
| nodeSelector | map[string]string |  |
| hostNetwork | bool |  |
| duration | metav1.Duration | Duration specifies how long the trace session should run.<br>WekaManualOperation: defaults to 1 week if omitted/0. CR auto-deletes after expiration.<br>WekaPolicy: defaults to continuous if omitted/0. Resources cleaned up after expiration.<br>Examples: "30m", "2h", "7d", "168h" |
| wekahomeEndpointOverride | string |  |
| allowHttpWekahomeEndpoint | bool |  |
| allowInsecureWekahomeEndpoint | bool |  |
| wekahomeCaSecret | string |  |

---

## CleanStaleVirtualDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| nodeSelector | map[string]string | NodeSelector limits the scan to ssdproxies on nodes matching these labels.<br>Empty = all nodes that have an ssdproxy. |
| onlyNonExistingClusters | bool | OnlyNonExistingClusters restricts the stale set to VIDs whose owner cluster GUID has NO<br>WekaCluster CR at all (category dead_cluster) — the safe-by-construction subset (no live<br>cluster could be mid-allocating them). Excludes live_cluster_unclaimed. Recommended ON<br>when pairing with deletion. |
| deleteStaleVids | bool | DeleteStaleVids enables ACTUAL removal of detected stale VIDs. DANGEROUS — disabled by<br>default. Even when true, a VID is only removed if it was reported stale and UNCHANGED on<br>the previous cycle (fingerprint match) and is still unclaimed at removal time. Acts as the<br>user's confirmation. |

---

## ConfigurationPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| csi | *CsiSpec | Csi configures the embedded CSI deployment. |
| drivers | *DriversSpec | Drivers configures the drivers build and distribution. |
| scheduling | *SchedulingSpec | Scheduling configures scheduling behavior of operator-managed pods. |
| podRotation | *PodRotationSpec | PodRotation sets the operator-wide default for replacing pods whose pod-affecting spec changed. |
| nodeAgent | *NodeAgentSpec | NodeAgent configures the node-agent DaemonSet the operator deploys on every node. |

---

## DistServiceStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| serviceUrl | string |  |

---

## PCIDevices

| JSON Field | Type | Description |
|------------|------|-------------|
| vendorId | string | VendorId is the 4-digit hexadecimal vendor ID<br>default for AWS: `1d0f` (Amazon.com, Inc.) |
| deviceId | string | DeviceId is the 4-digit hexadecimal device ID<br>default for AWS: `cd01` (NVMe SSD) |

---

## SignOptions

| JSON Field | Type | Description |
|------------|------|-------------|
| allowEraseWekaPartitions | bool |  |
| allowEraseNonWekaPartitions | bool |  |
| allowNonEmptyDevice | bool |  |
| skipTrimFormat | bool |  |

---

## DriveTypeOverrides

| JSON Field | Type | Description |
|------------|------|-------------|
| rules | []DriveTypeOverrideRule | Rules are evaluated in order; the first rule matching a drive wins.<br>An empty list clears all previously persisted overrides on the node. |

---

## DriveExclusions

| JSON Field | Type | Description |
|------------|------|-------------|
| rules | []DriveExclusionRule | A drive is excluded if any rule matches it. |

---

## PodResourcesSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| requests | PodResources |  |
| limits | PodResources |  |

---

## ObjectReference

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| namespace | string |  |

---

## CsiSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| metricsEnabled | *bool | MetricsEnabled controls the Prometheus metrics endpoints of the CSI controller, the CSI node<br>plugin and the controller sidecars. Enabled by default. |
| fsGroupPolicy | *string | FsGroupPolicy sets fsGroupPolicy on the CSIDriver object, which decides whether Kubernetes<br>reapplies a pod's fsGroup to the volume. Default value is File. |
| manageNfsPermissions | *bool | ManageNfsPermissions applies to WekaClients with useNfs only. When true, the CSI plugin creates<br>and removes NFS client groups, rules and permissions (exports) on the WEKA cluster itself.<br>When false (default), the WEKA cluster admin must configure them. |

---

## DriversSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| forceBuilderCli | *bool | ForceBuilderCli makes the drivers-builder init containers take the weka CLI from the builder<br>image instead of the cluster image. False by default. |

---

## SchedulingSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| tolerateCordon | *bool | TolerateCordon makes client containers and the node-local pods they depend on (drivers-loader,<br>drivers-dist, discovery, adhoc-op) tolerate the node.kubernetes.io/unschedulable:NoSchedule<br>taint, so they keep running and keep being created on a cordoned node. Backends never tolerate<br>it. False by default. |

---

## PodRotationSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| enabled | *bool | Enabled makes WekaClusters without spec.overrides.podRotation replace outdated pods one at a time. False by default. |

---

## NodeAgentSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| updateStrategy | *appsv1.DaemonSetUpdateStrategy | UpdateStrategy of the DaemonSet. Default is RollingUpdate with maxUnavailable 100% and maxSurge 0,<br>which restarts all node-agent pods at once. |
| resources | *corev1.ResourceRequirements | Resources of the node-agent container. Replaces the defaults as a whole. |
| tolerations | []corev1.Toleration | Tolerations of the node-agent pods. Default tolerates every taint. |
| nodeSelector | map[string]string | NodeSelector of the node-agent pods. |
| affinity | *corev1.Affinity | Affinity of the node-agent pods. Default requires a linux node with amd64 or arm64 architecture. |
| persistencePath | *string | PersistencePath is the host path used for node-agent persistence. Default is /opt/k8s-weka; use<br>/root/k8s-weka on OpenShift and /mnt/stateful_partition/k8s-weka on Google Container-Optimized OS. |
| kubeletPath | *string | KubeletPath is the kubelet directory of the nodes, used to locate the kubelet device-plugins<br>directory. Default is /var/lib/kubelet. |
| devicePlugin | *NodeAgentDevicePluginSpec | DevicePlugin configures the NUMA region device plugin. |

---

## DriveTypeOverrideRule

| JSON Field | Type | Description |
|------------|------|-------------|
| model | string | Model matches the device model exactly, case-insensitively, ignoring surrounding<br>whitespace. Find it with: lsblk -dno MODEL /dev/nvme0n1<br>Empty means "do not match on model". |
| capacityGiB | int | CapacityGiB matches the drive capacity in GiB exactly, as reported in the<br>weka.io/weka-shared-drives annotation. 0 means "do not match on capacity". |
| type | string | Type is the drive type to report for matching drives. |

---

## DriveExclusionRule

| JSON Field | Type | Description |
|------------|------|-------------|
| model | string | Model matches the device model exactly, case-insensitively, ignoring surrounding<br>whitespace. Find it with: lsblk -dno MODEL /dev/nvme0n1 |
| capacityGiB | int | CapacityGiB matches the drive capacity in GiB exactly. |
| type | string | Type is the drive type the sign tool derives from the IU size. |

---

## PodResources

| JSON Field | Type | Description |
|------------|------|-------------|
| cpu | resource.Quantity |  |
| memory | resource.Quantity |  |
| hugepages-2Mi | resource.Quantity | Hugepages2Mi is requested verbatim as the pod's hugepages-2Mi resource. 1Gi pages are<br>not settable through this field. |

---

## NodeAgentDevicePluginSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| enabled | *bool | Enabled advertises each node's NUMA regions as weka.io/numa-region-<N> extended resources through a<br>kubelet device plugin. Disabled by default. |

---

