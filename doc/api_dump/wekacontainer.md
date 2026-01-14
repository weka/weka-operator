# WekaContainer

## API Types

- [WekaContainer](#wekacontainer)
- [WekaContainerSpec](#wekacontainerspec)
- [WekaContainerStatus](#wekacontainerstatus)
- [WekaContainerList](#wekacontainerlist)
- [FailureDomain](#failuredomain)
- [PortRange](#portrange)
- [Network](#network)
- [TracesConfiguration](#tracesconfiguration)
- [Instructions](#instructions)
- [DriveTypesRatio](#drivetypesratio)
- [WekaContainerSpecOverrides](#wekacontainerspecoverrides)
- [PodResourcesSpec](#podresourcesspec)
- [PVCConfig](#pvcconfig)
- [ContainerAllocations](#containerallocations)
- [Drive](#drive)
- [WekaContainerMetrics](#wekacontainermetrics)
- [ContainerPrinterColumns](#containerprintercolumns)
- [NetworkSelector](#networkselector)
- [PodResources](#podresources)
- [VirtualDrive](#virtualdrive)
- [EntityStatefulNum](#entitystatefulnum)
- [DriveMetrics](#drivemetrics)
- [DriveFailures](#drivefailures)

---

## WekaContainer

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaContainerSpec |  |
| status | WekaContainerStatus |  |

---

## WekaContainerSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| nodeAffinity | NodeName | name of the node where the container should run on |
| failureDomain | *FailureDomain | failure domain configuration |
| topologySpreadConstraints | []v1.TopologySpreadConstraint | controls the distribution of weka containers across the failure domains |
| affinity | *v1.Affinity | advanced scheduling constraints |
| nodeSelector | map[string]string |  |
| port | int |  |
| exposePorts | []int | deprecated, use ExposedPorts instead |
| exposedPorts | []v1.ContainerPort | ports to be exposed on the container, proxied to pod |
| agentPort | int |  |
| portRange | *PortRange |  |
| image | string |  |
| imagePullSecret | string |  |
| name | string |  |
| mode | string |  |
| numCores | int | numCores is weka-specific cores |
| extraCores | int | extraCores is temporary solution for S3 containers, cores allocation on top of weka cores |
| coreIds | []int |  |
| cpuPolicy | CpuPolicy |  |
| network | Network |  |
| hugepages | int |  |
| hugepagesOffset | int |  |
| hugepagesSize | string |  |
| hugepagesSizeOverride | string |  |
| numDrives | int |  |
| driversDistService | string |  |
| driversLoaderImage | string |  |
| driversBuildId | *string |  |
| wekaSecretRef | v1.EnvVarSource |  |
| joinIpPorts | []string |  |
| tracesConfiguration | *TracesConfiguration |  |
| tolerations | []v1.Toleration |  |
| nodeInfoConfigMap | string |  |
| ipv6 | bool |  |
| additionalMemory | int |  |
| group | string |  |
| serviceAccountName | string |  |
| additionalSecrets | map[string]string |  |
| instructions | *Instructions |  |
| dropAffinityConstraints | bool |  |
| uploadResultsTo | string |  |
| upgradePolicyType | UpgradePolicyType |  |
| state | ContainerState |  |
| allowHotUpgrade | bool |  |
| driveCapacity | int | DriveCapacity specifies the capacity (in GiB) per virtual drive, indicates this container uses shared drives via SSD proxy.<br>When enabled, the container will:<br>- Use virtual UUIDs instead of device paths for drives<br>- Allocate capacity from shared drives rather than exclusive drives<br>- Require an SSD proxy container to be running on the same node<br>This value is copied from the cluster's DriveSharing.DriveCapacity configuration.<br>Used to calculate total capacity request: NumDrives * DriveCapacity |
| containerCapacity | int | ContainerCapacity specifies the total capacity (in GiB) requested by this container when using shared drives via SSD proxy.<br>This value takes precedence over DriveCapacity when both are set. It allows more flexible capacity allocation. |
| driveTypesRatio | *DriveTypesRatio | DriveTypesRatio specifies the desired ratio of drive types (TLC vs QLC) when allocating drives for the container. |
| autoRemoveTimeout | metav1.Duration | sets weka cluster-side timeout, if client is not coming back in specified duration it will be auto removed from cluster config |
| overrides | *WekaContainerSpecOverrides |  |
| hostPID | bool |  |
| resources | *PodResourcesSpec | resources to be proxied as-is to the pod spec |
| pvc | *PVCConfig |  |

---

## WekaContainerStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| status | ContainerStatus |  |
| internalStatus | string | weka local container internal status |
| managementIP | string |  |
| managementIPs | []string |  |
| containerID | *int |  |
| clusterID | string |  |
| conditions | []metav1.Condition |  |
| lastAppliedImage | string | Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later |
| lastAppliedSpec | string | set by weka cluster or client or other higher level controller, to track if higher level spec was propagated |
| nodeAffinity | NodeName | active nodeAffinity, copied from spec and populated if nodeSelector was used instead of direct nodeAffinity |
| result | *string |  |
| allocations | *ContainerAllocations |  |
| addedDrives | []Drive | drives that were added to the weka cluster |
| stats | *WekaContainerMetrics |  |
| printer | *ContainerPrinterColumns |  |
| timestamps | map[string]metav1.Time |  |
| notToleratedOnReschedule | bool |  |

---

## WekaContainerList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaContainer |  |

---

## FailureDomain

| JSON Field | Type | Description |
|------------|------|-------------|
| label | *string | label used for spreading the weka containers across different failure domains (if set)<br>nodes that have the same value for the label will be considered as a single failure domain |
| skew | *int | skew for the failure domain, if set, the weka containers will be spread with the skew in mind<br>(only applicable if `label` is set) |
| compositeLabels | []string | If multiple labels are specified, the failure domain will be the combination of the labels.<br>If `compositeLabels` is set, `label` and `skew` will be ignored.<br>When using compositeLabels, weka containers will be spread considering all labels<br>with best effort, but even distribution is not guaranteed |

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
| nvidiaVfSingleIp | *bool | NvidiaVfSingleIp indicates whether NVIDIA virtual functions (VFs) should be configured to use a single-ip weka mode, where multiple weka processes can share same VF<br>When not set defaults to false, in future releases, when auto-discovery of capabilities will be implemented not set might translate to true on supported setups |

---

## TracesConfiguration

| JSON Field | Type | Description |
|------------|------|-------------|
| maxCapacityPerIoNode | int |  |
| ensureFreeSpace | int |  |
| dumperConfigMode | DumperConfigMode |  |

---

## Instructions

| JSON Field | Type | Description |
|------------|------|-------------|
| type | string |  |
| payload | string |  |

---

## DriveTypesRatio

| JSON Field | Type | Description |
|------------|------|-------------|
| tlc | int |  |
| qlc | int |  |

---

## WekaContainerSpecOverrides

| JSON Field | Type | Description |
|------------|------|-------------|
| skipDeactivate | bool | skips deactivation of container, this is unsafe operation that should be used only when this container will never be back into cluster |
| skipDrivesForceResign | bool | skips resign of drives, if we did not resign drives on removal of drive container we will not be able to reuse them, and manual operation with force resign will be required |
| skipCleanupPersistentDir | bool | skips cleanup of persistent directory, if this operation was omit local data of container will remain in persistent location(/opt/k8s-weka on vanilla OS/k8s distributions) |
| upgradeForceReplace | bool | unsafe operation, skips graceful stop of weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel |
| upgradePreventEviction | bool |  |
| podDeleteForceReplace | bool |  |
| machineIdentifierNodeRef | string |  |
| preRunScript | string | script to be executed post initial persistency(if needed) configuration, before running actual workload |
| forceDrain | bool | unsafe operation, forces drain on the node where the container is running, should not be used unless instructed explicitly by weka personnel, the effect of drain is throwing away all IOs and acknowledging all umounts in unsafe manner |
| skipActiveMountsCheck | bool | option to skip active mounts check before deleting client containers |
| umountOnHost | bool | unsafe operation, runs nsenter in root namespace to umount all wekafs mounts visible on host |
| debugSleepOnTerminate | int | DebugSleepOnTerminate specifies the number of seconds to sleep on container abnormal exit for debugging purposes |
| migrateOutFromPvc | bool | MigrateOutFromPvc specifies that the container should be migrated out from PVC into local storage, this will be done prior to starting pod |

---

## PodResourcesSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| requests | PodResources |  |
| limits | PodResources |  |

---

## PVCConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| path | string |  |

---

## ContainerAllocations

| JSON Field | Type | Description |
|------------|------|-------------|
| drives | []string |  |
| ethSlots | []string |  |
| lbPort | int |  |
| wekaPort | int |  |
| agentPort | int |  |
| failureDomain | *string | value of the failure domain label of the node where the container is running |
| machineIdentifier | string |  |
| netDevices | []string |  |
| virtualDrives | []VirtualDrive | VirtualDrives contains virtual drive allocations for drive sharing mode.<br>Each VirtualDrive maps a virtual UUID to a physical drive UUID with allocated capacity. |

---

## Drive

| JSON Field | Type | Description |
|------------|------|-------------|
| uuid | string |  |
| added_time | string |  |
| device_path | string |  |
| serial_number | string |  |
| status | string |  |

---

## WekaContainerMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| processes | EntityStatefulNum |  |
| cpuUtilization | FloatMetric |  |
| drives | DriveMetrics |  |
| activeMounts | IntMetric |  |
| lastUpdate | metav1.Time |  |

---

## ContainerPrinterColumns

| JSON Field | Type | Description |
|------------|------|-------------|
| processes | StringMetric |  |
| drives | StringMetric |  |
| activeMounts | StringMetric |  |
| managementIPs | string | pretty-printed management IPs |
| nodeAffinity | string | node name where the container is running |

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

## VirtualDrive

| JSON Field | Type | Description |
|------------|------|-------------|
| virtualUUID | string | VirtualUUID is the virtual drive identifier |
| physicalUUID | string | PhysicalUUID is the physical drive UUID obtained from proxy signing |
| capacityGiB | int | CapacityGiB is the allocated capacity in GiB |
| serial | string | Serial is the serial number of the physical drive |
| type | string | Type is the type of the drive (e.g., TLC, QLC) |

---

## EntityStatefulNum

| JSON Field | Type | Description |
|------------|------|-------------|
| active | IntMetric |  |
| created | IntMetric |  |
| desired | IntMetric |  |

---

## DriveMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| counters | EntityStatefulNum |  |
| failures | []DriveFailures |  |

---

## DriveFailures

| JSON Field | Type | Description |
|------------|------|-------------|
| serialId | string |  |
| wekaDriveId | string |  |

---

