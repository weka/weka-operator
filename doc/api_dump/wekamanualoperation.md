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
- [PCIDevices](#pcidevices)
- [SignOptions](#signoptions)
- [ObjectReference](#objectreference)

---

## WekaManualOperation

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaManualOperationSpec | WekaManualOperation is the Schema for the wekamanualoperations API |
| status | WekaManualOperationStatus | WekaManualOperation is the Schema for the wekamanualoperations API |

---

## WekaManualOperationSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| action | string | WekaManualOperationSpec defines the desired state of WekaManualOperation |
| payload | ManualOperatorPayload |  |
| image | *string |  |
| imagePullSecret | *string |  |
| tolerations | []v1.Toleration |  |
| serviceAccountName | string |  |

---

## WekaManualOperationStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| result | string | WekaManualOperationStatus defines the observed state of WekaManualOperation |
| status | string | WekaManualOperationStatus defines the observed state of WekaManualOperation |
| completedAt | metav1.Time | WekaManualOperationStatus defines the observed state of WekaManualOperation |

---

## WekaManualOperationList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaManualOperation | WekaManualOperationList contains a list of WekaManualOperation |

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

---

## SignDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| type | string |  |
| nodeSelector | map[string]string |  |
| devicePaths | []string |  |
| pciDevices | *PCIDevices | vendor and device IDs in square brackets in the format [vendorId:deviceId]. |
| options | *SignOptions | For example: |

---

## BlockDrivesPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| serialIDs | []string |  |
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
| type | NICType | default for AWS: `cd01` (NVMe SSD) |
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
| duration | metav1.Duration |  |
| wekahomeEndpointOverride | string |  |
| allowHttpWekahomeEndpoint | bool |  |
| allowInsecureWekahomeEndpoint | bool |  |
| wekahomeCaSecret | string |  |

---

## PCIDevices

| JSON Field | Type | Description |
|------------|------|-------------|
| vendorId | string | VendorId is the 4-digit hexadecimal vendor ID |
| deviceId | string | DeviceId is the 4-digit hexadecimal device ID |

---

## SignOptions

| JSON Field | Type | Description |
|------------|------|-------------|
| allowEraseWekaPartitions | bool | 00:1f.0 Non-Volatile memory controller [0108]: Amazon.com, Inc. NVMe SSD Controller [1d0f:cd01] |
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

