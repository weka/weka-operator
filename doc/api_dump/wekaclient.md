# WekaClient

## API Types

- [WekaClient](#wekaclient)
- [WekaClientSpec](#wekaclientspec)
- [WekaClientStatus](#wekaclientstatus)
- [WekaClientList](#wekaclientlist)
- [PortRange](#portrange)
- [Network](#network)
- [ObjectReference](#objectreference)
- [TracesConfiguration](#tracesconfiguration)
- [PodResourcesSpec](#podresourcesspec)
- [WekahomeClientConfig](#wekahomeclientconfig)
- [UpgradePolicy](#upgradepolicy)
- [WekaClientSpecOverrides](#wekaclientspecoverrides)
- [PVCConfig](#pvcconfig)
- [ClientCsiConfig](#clientcsiconfig)
- [ClientMetrics](#clientmetrics)
- [ClientPrinterColumns](#clientprintercolumns)
- [NetworkSelector](#networkselector)
- [PodResources](#podresources)
- [AdvancedCsiConfig](#advancedcsiconfig)
- [EntityStatefulNum](#entitystatefulnum)

---

## WekaClient

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaClientSpec | WekaClient is the Schema for the clients API |
| status | WekaClientStatus | WekaClient is the Schema for the clients API |

---

## WekaClientSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| image | string | WekaClientSpec defines the desired state of WekaClient |
| imagePullSecret | string | full container image in format of quay.io/weka.io/weka-in-container:VERSION |
| port | int | if not set (0), weka will find a free port from the portRange |
| agentPort | int | if not set (0), weka will find a free port from the portRange |
| portRange | *PortRange | if not set (0), weka will find a free port from the portRange |
| nodeSelector | map[string]string | if not set (0), weka will find a free port from the portRange |
| wekaSecretRef | string | used for dynamic port allocation |
| network | Network |  |
| driversDistService | string |  |
| driversLoaderImage | string |  |
| joinIpPorts | []string |  |
| targetCluster | ObjectReference |  |
| cpuPolicy | CpuPolicy |  |
| cpuRequest | string |  |
| coresNum | int |  |
| coreIds | []int |  |
| tracesConfiguration | *TracesConfiguration |  |
| tolerations | []string |  |
| rawTolerations | []v1.Toleration |  |
| serviceAccountName | string |  |
| additionalMemory | int | memory to add/decrease from "auto-calculated" memory |
| resources | *PodResourcesSpec | memory to add/decrease from "auto-calculated" memory |
| hugepages | int | experimental: pod resources to be proxied as-is to the pod spec |
| hugepagesOffset | *int | experimental: pod resources to be proxied as-is to the pod spec |
| wekaHomeConfig | WekahomeClientConfig | value in megabytes to offset |
| wekaHome | *WekahomeClientConfig | DEPRECATED, kept for compatibility with old API clients, not taking any action, to be removed on new API version |
| upgradePolicy | UpgradePolicy | DEPRECATED, kept for compatibility with old API clients, not taking any action, to be removed on new API version |
| allowHotUpgrade | bool |  |
| overrides | *WekaClientSpecOverrides |  |
| autoRemoveTimeout | metav1.Duration | sets weka cluster-side timeout, if client is not coming back in specified duration it will be auto removed from cluster config |
| globalPVC | *PVCConfig | sets weka cluster-side timeout, if client is not coming back in specified duration it will be auto removed from cluster config |
| csiConfig | *ClientCsiConfig | EXPERIMENTAL, ALPHA STATE, should not be used in production: if set, allows to reuse the same csi resources for multiple clients |

---

## WekaClientStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| conditions | []metav1.Condition | WekaClientStatus defines the observed state of WekaClient |
| lastAppliedSpec | string | WekaClientStatus defines the observed state of WekaClient |
| status | WekaClientStatusEnum |  |
| stats | *ClientMetrics |  |
| printer | ClientPrinterColumns |  |

---

## WekaClientList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaClient | WekaClientList contains a list of WekaClient |

---

## PortRange

| JSON Field | Type | Description |
|------------|------|-------------|
| basePort | int |  |
| portRange | int | number of ports to check for availability |

---

## Network

| JSON Field | Type | Description |
|------------|------|-------------|
| ethDevice | string |  |
| ethDevices | []string |  |
| gateway | string |  |
| udpMode | bool |  |
| deviceSubnets | []string | subnet that is used for devices auto-discovery |
| selectors | []NetworkSelector | subnet that is used for devices auto-discovery |
| managementIpsSelectors | []NetworkSelector |  |

---

## ObjectReference

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| namespace | string |  |

---

## TracesConfiguration

| JSON Field | Type | Description |
|------------|------|-------------|
| maxCapacityPerIoNode | int | TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes |
| ensureFreeSpace | int | TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes |
| dumperConfigMode | DumperConfigMode |  |

---

## PodResourcesSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| requests | PodResources |  |
| limits | PodResources |  |

---

## WekahomeClientConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| cacertSecret | string |  |

---

## UpgradePolicy

| JSON Field | Type | Description |
|------------|------|-------------|
| type | UpgradePolicyType | unsafe operation, runs nsenter in root namespace to umount all wekafs mounts visible on host |

---

## WekaClientSpecOverrides

| JSON Field | Type | Description |
|------------|------|-------------|
| machineIdentifierNodeRef | string | used to override machine identifier node reference for client containers |
| forceDrain | bool | unsafe operation, forces drain on the node where the container is running, should not be used unless instructed explicitly by weka personnel, the effect of drain is throwing away all IOs and acknowledging all umounts in unsafe manner |
| skipActiveMountsCheck | bool | unsafe operation, forces drain on the node where the container is running, should not be used unless instructed explicitly by weka personnel, the effect of drain is throwing away all IOs and acknowledging all umounts in unsafe manner |
| umountOnHost | bool | option to skip active mounts check before deleting client containers |

---

## PVCConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| path | string |  |

---

## ClientCsiConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| csiGroup | string |  |
| disableControllerCreation | bool |  |
| advanced | *AdvancedCsiConfig |  |

---

## ClientMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| containers | EntityStatefulNum |  |

---

## ClientPrinterColumns

| JSON Field | Type | Description |
|------------|------|-------------|
| containers | StringMetric |  |

---

## NetworkSelector

| JSON Field | Type | Description |
|------------|------|-------------|
| subnet | string |  |
| min | int |  |
| max | int |  |
| deviceNames | []string |  |

---

## PodResources

| JSON Field | Type | Description |
|------------|------|-------------|
| cpu | resource.Quantity | number of ports to check for availability |
| memory | resource.Quantity | number of ports to check for availability |

---

## AdvancedCsiConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| enforceTrustedHttps | bool |  |
| nodeLabels | map[string]string |  |
| nodeTolerations | []v1.Toleration |  |
| controllerLabels | map[string]string |  |
| controllerTolerations | []v1.Toleration |  |
| skipGarbageCollection | bool |  |

---

## EntityStatefulNum

| JSON Field | Type | Description |
|------------|------|-------------|
| active | IntMetric |  |
| created | IntMetric |  |
| desired | IntMetric |  |

---

