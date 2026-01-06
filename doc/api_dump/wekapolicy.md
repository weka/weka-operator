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
- [DistServiceStatus](#distservicestatus)
- [PCIDevices](#pcidevices)
- [SignOptions](#signoptions)
- [ObjectReference](#objectreference)

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
| type | string |  |
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
| pciDevices | *PCIDevices | PCI vendor and device IDs of the drives to sign.<br>To get the values for VendorId and DeviceId:<br>1. Run the following command to list all PCI devices on your system:<br>```bash<br>lspci -nn<br>```<br>2. Find the relevant PCI device in the output, which will display both the<br>vendor and device IDs in square brackets in the format [vendorId:deviceId].<br>For example:<br>```<br>00:1f.0 Non-Volatile memory controller [0108]: Amazon.com, Inc. NVMe SSD Controller [1d0f:cd01]<br>``` |
| options | *SignOptions |  |
| shared | bool | Shared enables shared drive signing for proxy mode (defaults to false).<br>When enabled:<br>- Drives are signed for proxy using 'weka-sign-drive sign proxy' command<br>- Drives are signed with a proxy system GUID<br>- Results are stored in weka.io/shared-drives annotation (instead of weka.io/weka-drives)<br>- Physical UUIDs, serial IDs, and capacities are captured<br>- Enables multi-tenant drive sharing via SSD proxy |

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
| builderPreRunScript | *string | BuilderPreRunScript is an optional script to run on builder containers after kernel validation. |
| distNodeSelector | map[string]string | DistNodeSelector is the node selector for the drivers distribution (dist) container.<br>If not specified, the dist container will be scheduled on any available node. |

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

## ObjectReference

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| namespace | string |  |

---

