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
- [DistServiceStatus](#distservicestatus)
- [PCIDevices](#pcidevices)
- [SignOptions](#signoptions)

---

## WekaPolicy

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaPolicySpec | WekaPolicy is the Schema for the wekapolicies API |
| status | WekaPolicyStatus | WekaPolicy is the Schema for the wekapolicies API |

---

## WekaPolicySpec

| JSON Field | Type | Description |
|------------|------|-------------|
| type | string | WekaPolicySpec defines the desired state of WekaPolicy |
| payload | PolicyPayload | WekaPolicySpec defines the desired state of WekaPolicy |
| image | *string | WekaPolicySpec defines the desired state of WekaPolicy |
| imagePullSecret | *string | WekaPolicySpec defines the desired state of WekaPolicy |
| tolerations | []v1.Toleration |  |
| serviceAccountName | string |  |

---

## WekaPolicyStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| status | string | WekaPolicyStatus defines the observed state of WekaPolicy |
| result | string | WekaPolicyStatus defines the observed state of WekaPolicy |
| lastRunTime | metav1.Time | WekaPolicyStatus defines the observed state of WekaPolicy |
| progress | string | WekaPolicyStatus defines the observed state of WekaPolicy |
| typedStatus | *TypedPolicyStatus |  |

---

## WekaPolicyList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaPolicy | WekaPolicyList contains a list of WekaPolicy |

---

## PolicyPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| signDrivesPayload | *SignDrivesPayload | WekaPolicyList contains a list of WekaPolicy |
| schedulingConfigPayload | *SchedulingConfigPayload |  |
| discoverDrivesPayload | *DiscoverDrivesPayload |  |
| ensureNICsPayload | *EnsureNICsPayload |  |
| driverDistPayload | *DriverDistPayload |  |
| interval | metav1.Duration |  |
| waitForPolicies | []string |  |

---

## TypedPolicyStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| distService | *DistServiceStatus | TypedPolicyStatus holds status fields specific to a policy type |

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
| type | NICType | default for AWS: `cd01` (NVMe SSD) |
| nodeSelector | map[string]string |  |
| dataNICsNumber | int |  |

---

## DriverDistPayload

| JSON Field | Type | Description |
|------------|------|-------------|
| ensureImages | []string | DriverDistPayload defines the parameters for the enable-local-drivers-distribution policy |
| nodeSelectors | []map[string]string | NodeSelectors is a list of node selectors. Nodes matching any of these selectors will be considered for driver building. |
| kernelLabelKey | *string | KernelLabelKey is the custom label key to use for storing the node's kernel version. |
| architectureLabelKey | *string | ArchitectureLabelKey is the custom label key to use for storing the node's architecture. |
| builderPreRunScript | *string | If not specified, "weka.io/architecture" will be used. |
| distNodeSelector | map[string]string | DistNodeSelector is the node selector for the drivers distribution (dist) container. |

---

## DistServiceStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| serviceUrl | string | Add other policy-specific statuses here in the future |

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

