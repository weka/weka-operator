# WekaManualOperation

## API Types

- [WekaManualOperation](#wekamanualoperation)
- [WekaManualOperationSpec](#wekamanualoperationspec)
- [WekaManualOperationStatus](#wekamanualoperationstatus)
- [WekaManualOperationList](#wekamanualoperationlist)
- [ManualOperatorPayload](#manualoperatorpayload)
- [SignDrivesPayload](#signdrivespayload)
- [BlockDrivesPayload](#blockdrivespayload)
- [DiscoverDrivesPayload](#discoverdrivespayload)
- [EnsureNICsPayload](#ensurenicspayload)
- [ForceResignDrivesPayload](#forceresigndrivespayload)
- [RemoteTracesSessionConfig](#remotetracessessionconfig)
- [CleanStaleVirtualDrivesPayload](#cleanstalevirtualdrivespayload)
- [PCIDevices](#pcidevices)
- [SignOptions](#signoptions)
- [DriveTypeOverrides](#drivetypeoverrides)
- [ObjectReference](#objectreference)
- [DriveTypeOverrideRule](#drivetypeoverriderule)

---

## WekaManualOperation

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaManualOperationSpec |  |
| status | WekaManualOperationStatus |  |

---

## WekaManualOperationSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| action | WekaManualOperationAction |  |
| payload | ManualOperatorPayload |  |
| image | *string |  |
| imagePullSecret | *string |  |
| tolerations | []v1.Toleration |  |
| serviceAccountName | string |  |
| deletionDelay | *metav1.Duration | DeletionDelay specifies how long to wait after completion before deleting the resource.<br>Defaults to 5m if not specified. |

---

## WekaManualOperationStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| result | string |  |
| status | string |  |
| completedAt | metav1.Time |  |

---

## WekaManualOperationList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaManualOperation |  |

---

## ManualOperatorPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| signDrivesPayload | *SignDrivesPayload |  |
| blockDrivesPayload | *BlockDrivesPayload |  |
| discoverDrivesPayload | *DiscoverDrivesPayload |  |
| ensureNICsPayload | *EnsureNICsPayload |  |
| forceResignDrivesPayload | *ForceResignDrivesPayload |  |
| remoteTracesSessionPayload | *RemoteTracesSessionConfig |  |
| cleanStaleVirtualDrivesPayload | *CleanStaleVirtualDrivesPayload |  |

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
| driveTypeOverrides | *DriveTypeOverrides | DriveTypeOverrides forces the reported TLC/QLC type for matching drives instead of<br>deriving it from the drive's IU size. Only meaningful when Shared is true.<br>Persisted on the node in the weka.io/drive-type-overrides annotation and re-applied<br>on every later sign-drives run. Omit the field to keep whatever is already persisted<br>on the node; set an empty rules list to clear all overrides. |

---

## BlockDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| serialIDs | []string |  |
| physicalUUIDs | []string |  |
| node | string |  |

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

## ForceResignDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| nodeName | NodeName |  |
| deviceSerials | []string |  |
| devicePaths | []string |  |

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

## ObjectReference

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| namespace | string |  |

---

## DriveTypeOverrideRule

| JSON Field | Type | Description |
|------------|------|-------------|
| model | string | Model matches the device model exactly, case-insensitively, ignoring surrounding<br>whitespace. Find it with: lsblk -dno MODEL /dev/nvme0n1<br>Empty means "do not match on model". |
| capacityGiB | int | CapacityGiB matches the drive capacity in GiB exactly, as reported in the<br>weka.io/weka-shared-drives annotation. 0 means "do not match on capacity". |
| type | string | Type is the drive type to report for matching drives. |

---

