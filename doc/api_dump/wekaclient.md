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
| spec | WekaClientSpec |  |
| status | WekaClientStatus |  |

---

## WekaClientSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| image | string | full container image in format of quay.io/weka.io/weka-in-container:VERSION |
| imagePullSecret | string |  |
| port | int | if not set (0), weka will find a free port from the portRange |
| agentPort | int | if not set (0), weka will find a free port from the portRange |
| portRange | *PortRange | used for dynamic port allocation |
| nodeSelector | map[string]string |  |
| wekaSecretRef | string |  |
| network | Network |  |
| driversDistService | string |  |
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
| resources | *PodResourcesSpec | experimental: pod resources to be proxied as-is to the pod spec |
| hugepages | int | hugepages, value in megabytes |
| hugepagesOffset | *int | value in megabytes to offset |
| wekaHomeConfig | WekahomeClientConfig | DEPRECATED, kept for compatibility with old API clients, not taking any action, to be removed on new API version |
| wekaHome | *WekahomeClientConfig |  |
| upgradePolicy | UpgradePolicy |  |
| allowHotUpgrade | bool |  |
| overrides | *WekaClientSpecOverrides |  |
| autoRemoveTimeout | metav1.Duration | sets weka cluster-side timeout, if client is not coming back in specified duration it will be auto removed from cluster config |
| globalPVC | *PVCConfig |  |
| csiConfig | *ClientCsiConfig | EXPERIMENTAL, ALPHA STATE, should not be used in production: if set, allows to reuse the same csi resources for multiple clients |

---

## WekaClientStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| conditions | []metav1.Condition |  |
| lastAppliedSpec | string |  |
| status | WekaClientStatusEnum |  |
| stats | *ClientMetrics |  |
| printer | ClientPrinterColumns |  |

---

## WekaClientList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaClient |  |

---

## PortRange

| JSON Field | Type | Description |
|------------|------|-------------|
| basePort | int |  |
| portRange | int | number of ports to check for availability<br>if 0 - will check all ports from basePort to 65535 |

---

## Network

| JSON Field | Type | Description |
|------------|------|-------------|
| ethDevice | string | The name of a single network interface (for example, eth1) to be used by every backend container.<br>This is for clusters that use only one dedicated NIC for the data path.<br>You cannot use this field with ethDevices.<br>If you leave this empty, the system automatically uses the node’s interface associated with the first subnet defined in deviceSubnets. |
| ethDevices | []string | A list of network interface names to be used by backend containers when you have multiple dedicated NICs.<br>The order of interfaces in this list is important, as it maps directly to the ethSlots index (the first interface maps to slot-0, the second to slot-1, and so on).<br>You cannot use this field with ethDevice. Ensure that every interface listed here exists on all nodes that are part of the cluster. |
| gateway | string | The default gateway IPv4 address for the backend containers’ data-path network.<br>This is only necessary if backend subnets need to communicate with destinations outside of their local network (L2 segment).<br>If you have a flat, non-routed backend network, you can leave this field empty. |
| udpMode | bool | A setting that enables or disables UDP encapsulation for backend traffic.<br>- false (default): Uses standard raw Ethernet frames. true: Wraps data-path traffic in UDP packets.<br>This is required if your network infrastructure or CNI (Container Network Interface) blocks traffic that isn’t IP-based. |
| deviceSubnets | []string | A list of backend subnets in CIDR notation (for example, 192.168.10.0/24).<br>The operator assigns IP addresses from these subnets to the backend containers for their data path network |
| selectors | []NetworkSelector |  |
| managementIpsSelectors | []NetworkSelector |  |
| bindManagementAll | bool | BindManagementAll controls whether Weka containers bind to all network interfaces or only to specific management interfaces.<br>When set to false (default), containers will only listen on the management ips interfaces (restrict_listen mode).<br>When set to true, containers will listen on all ips (0.0.0.0) instead of specific IP addresses. |

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
| maxCapacityPerIoNode | int |  |
| ensureFreeSpace | int |  |
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
| type | UpgradePolicyType |  |

---

## WekaClientSpecOverrides

| JSON Field | Type | Description |
|------------|------|-------------|
| driversBuildId | *string | can be used to specify a build_id for a driver in the distributor service, keep empty for auto detection default |
| driversLoaderImage | string |  |
| machineIdentifierNodeRef | string | used to override machine identifier node reference for client containers |
| forceDrain | bool | unsafe operation, forces drain on the node where the container is running, should not be used unless instructed explicitly by weka personnel, the effect of drain is throwing away all IOs and acknowledging all umounts in unsafe manner |
| skipActiveMountsCheck | bool | option to skip active mounts check before deleting client containers |
| umountOnHost | bool | unsafe operation, runs nsenter in root namespace to umount all wekafs mounts visible on host |
| dropAffinityConstraints | bool | unsafe parameter, disables anti-affinities on client pods, allowing to schedule more than one client pod per node.<br>Running multiple clients for multiple clusters on the same node is not fully supported yet, and this flag should not be used in production. |
| wekaContainerName | string | override name used in weka local setup for the container<br>this can be used for integration with external client on the host |

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
| cpu | resource.Quantity |  |
| memory | resource.Quantity |  |

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

