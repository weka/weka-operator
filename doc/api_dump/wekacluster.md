# WekaCluster

## API Types

- [WekaCluster](#wekacluster)
- [WekaClusterSpec](#wekaclusterspec)
- [WekaClusterStatus](#wekaclusterstatus)
- [WekaClusterList](#wekaclusterlist)
- [RoleNodeSelector](#rolenodeselector)
- [RoleAnnotations](#roleannotations)
- [RoleNetworkSelector](#rolenetworkselector)
- [FailureDomain](#failuredomain)
- [PodConfiguration](#podconfiguration)
- [TracesConfiguration](#tracesconfiguration)
- [WekaHomeConfig](#wekahomeconfig)
- [AdditionalMemory](#additionalmemory)
- [ClusterPorts](#clusterports)
- [WekaConfig](#wekaconfig)
- [Network](#network)
- [StartIoConditions](#startioconditions)
- [WekaClusterSpecOverrides](#wekaclusterspecoverrides)
- [CsiConfig](#csiconfig)
- [PVCConfig](#pvcconfig)
- [RoleCoreIds](#rolecoreids)
- [EncryptionConfig](#encryptionconfig)
- [ClusterMetrics](#clustermetrics)
- [ClusterPrinterColumns](#clusterprintercolumns)
- [RoleTopologySpreadConstraints](#roletopologyspreadconstraints)
- [RoleAffinity](#roleaffinity)
- [NetworkSelector](#networkselector)
- [AdvancedCsiConfig](#advancedcsiconfig)
- [VaultConfig](#vaultconfig)
- [ContainersMetrics](#containersmetrics)
- [IoStats](#iostats)
- [DriveMetrics](#drivemetrics)
- [CapacityMetrics](#capacitymetrics)
- [FilesystemMetrics](#filesystemmetrics)
- [ContainerMetrics](#containermetrics)
- [StatusThroughput](#statusthroughput)
- [StatusIops](#statusiops)
- [EntityStatefulNum](#entitystatefulnum)
- [DriveFailures](#drivefailures)

---

## WekaCluster

| JSON Field | Type | Description |
|------------|------|-------------|
| spec | WekaClusterSpec |  |
| status | WekaClusterStatus |  |

---

## WekaClusterSpec

| JSON Field | Type | Description |
|------------|------|-------------|
| template | string | WekaClusterSpec defines the desired state of WekaCluster |
| image | string | full container image name in format of quay.io/weka.io/weka-in-container:VERSION |
| imagePullSecret | string | full container image name in format of quay.io/weka.io/weka-in-container:VERSION |
| driversDistService | string | endpoint for distribution service, global https://drivers.weka.io or in-k8s-cluster "https://weka-drivers-dist.namespace.svc.cluster.local:60001" |
| nodeSelector | map[string]string | endpoint for distribution service, global https://drivers.weka.io or in-k8s-cluster "https://weka-drivers-dist.namespace.svc.cluster.local:60001" |
| roleNodeSelector | RoleNodeSelector | node selector for the weka containers |
| roleAnnotations | RoleAnnotations | node selector for the weka containers per role, overrides global nodeSelector |
| roleNetworkSelector | RoleNetworkSelector | annotations for the weka containers per role |
| failureDomain | *FailureDomain | network selector for the weka containers per role, overrides global network |
| podConfig | *PodConfiguration | failure domain configuration for weka containers |
| cpuPolicy | CpuPolicy | there is no need to specify siblings in this list, but on the side of other applications like slurm, both weka core and its siblings should be excluded from used cpu set |
| tracesConfiguration | *TracesConfiguration | traces capacities configuration for weka containers |
| tolerations | []string | traces capacities configuration for weka containers |
| rawTolerations | []v1.Toleration | simplified tolerations, checked only by key existence, expanding to NoExecute|NoSchedule tolerations |
| wekaHome | *WekaHomeConfig | simplified tolerations, checked only by key existence, expanding to NoExecute|NoSchedule tolerations |
| ipv6 | bool | weka home configuration |
| additionalMemory | AdditionalMemory | use ipv6 for weka cluster networking configuration |
| ports | ClusterPorts | port allocation for weka containers, if not set, free range will be auto selected. Currently allocated ports can be seen in wekacluster.status.ports |
| operatorSecretRef | string | port allocation for weka containers, if not set, free range will be auto selected. Currently allocated ports can be seen in wekacluster.status.ports |
| expandEndpoints | []string | endpoint of existing weka cluster, containers created for this k8s-driver cluster will join existing weka cluster, used in flow of migration |
| dynamicTemplate | *WekaConfig | endpoint of existing weka cluster, containers created for this k8s-driver cluster will join existing weka cluster, used in flow of migration |
| network | Network | endpoint of existing weka cluster, containers created for this k8s-driver cluster will join existing weka cluster, used in flow of migration |
| hotSpare | int | A hot spare is reserved capacity designed to handle data rebuilds while maintaining the system's net capacity, even in the event of failure domains being lost |
| redundancyLevel | int | storage capacity dedicated to system protection (2/4). https://docs.weka.io/weka-system-overview/ssd-capacity-management#protection-level |
| stripeWidth | int | stripe width is the number of blocks within a common protection set, ranging from 3 to 16 https://docs.weka.io/weka-system-overview/ssd-capacity-management#stripe-width |
| leadershipRaftSize | *int | stripe width is the number of blocks within a common protection set, ranging from 3 to 16 https://docs.weka.io/weka-system-overview/ssd-capacity-management#stripe-width |
| bucketRaftSize | *int | size of raft for leadership, defaults to 5, 5/9 are supported |
| startIoConditions | *StartIoConditions | size of raft for buckets, defaults to 5, 5/9 are supported |
| gracefulDestroyDuration | metav1.Duration | Note: due to discrepancies in validation vs parsing, we use a Pattern instead of `Format=duration`. See |
| overrides | *WekaClusterSpecOverrides | https://github.com/kubernetes/apimachinery/issues/131 |
| csiConfig | CsiConfig | https://github.com/kubernetes/apiextensions-apiserver/issues/56 |
| globalPVC | *PVCConfig |  |
| serviceAccountName | string |  |
| roleCoreIds | RoleCoreIds | Example: |
| encryption | *EncryptionConfig | drive:   [4,5,6,7] |

---

## WekaClusterStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| status | WekaClusterStatusEnum | WekaClusterStatus defines the observed state of WekaCluster |
| conditions | []metav1.Condition | WekaClusterStatus defines the observed state of WekaCluster |
| clusterID | string |  |
| traceId | string |  |
| spanId | string |  |
| lastAppliedImage | string | Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later |
| lastAppliedSpec | string | Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later |
| ports | ClusterPorts | Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later |
| stats | *ClusterMetrics | Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later |
| printer | ClusterPrinterColumns |  |
| timestamps | map[string]metav1.Time |  |

---

## WekaClusterList

| JSON Field | Type | Description |
|------------|------|-------------|
| items | []WekaCluster |  |

---

## RoleNodeSelector

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *map[string]string | nodeSelector for compute weka containers |
| drive | *map[string]string | nodeSelector for compute weka containers |
| s3 | *map[string]string | nodeSelector for compute weka containers |
| nfs | *map[string]string | nodeSelector for drive weka containers |

---

## RoleAnnotations

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *map[string]string | nodeSelector for nfs weka containers |
| drive | *map[string]string | annotations for compute weka containers |
| s3 | *map[string]string | annotations for compute weka containers |
| nfs | *map[string]string | annotations for drive weka containers |

---

## RoleNetworkSelector

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *Network | annotations for nfs weka containers |
| drive | *Network | network selector for compute weka containers |
| s3 | *Network | network selector for compute weka containers |
| nfs | *Network | network selector for drive weka containers |

---

## FailureDomain

| JSON Field | Type | Description |
|------------|------|-------------|
| label | *string | label used for spreading the weka containers across different failure domains (if set) |
| skew | *int | nodes that have the same value for the label will be considered as a single failure domain |
| compositeLabels | []string | If `compositeLabels` is set, `label` and `skew` will be ignored. |

---

## PodConfiguration

| JSON Field | Type | Description |
|------------|------|-------------|
| topologySpreadConstraints | []v1.TopologySpreadConstraint | controls the distribution of weka containers across the failure domainsqq |
| roleTopologySpreadConstraints | *RoleTopologySpreadConstraints | controls the distribution of weka containers across the failure domainsqq |
| affinity | *v1.Affinity | takes precedence over the `topologySpreadConstraints` |
| roleAffinity | *RoleAffinity | advanced scheduling constraints |

---

## TracesConfiguration

| JSON Field | Type | Description |
|------------|------|-------------|
| maxCapacityPerIoNode | int | TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes |
| ensureFreeSpace | int | TraceConfiguration defines the configuration for the traces, accepts parameters in gigabytes |
| dumperConfigMode | DumperConfigMode |  |

---

## WekaHomeConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| endpoint | string | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage offset for NFS frontend |
| allowInsecure | bool | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage offset for NFS frontend |
| cacertSecret | string |  |
| enableStats | *bool |  |

---

## AdditionalMemory

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | int |  |
| drive | int |  |
| s3 | int |  |
| nfs | int |  |
| envoy | int |  |

---

## ClusterPorts

| JSON Field | Type | Description |
|------------|------|-------------|
| basePort | int | We should not be updating Spec, as it's a user interface and we should not break ability to update spec file |
| portRange | int | We should not be updating Spec, as it's a user interface and we should not break ability to update spec file |
| lbPort | int | Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here |
| lbAdminPort | int | Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here |
| s3Port | int | Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here |

---

## WekaConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| computeContainers | *int |  |
| driveContainers | *int |  |
| s3Containers | int |  |
| computeCores | int |  |
| driveCores | int |  |
| s3Cores | int |  |
| numDrives | int |  |
| s3ExtraCores | int |  |
| driveHugepages | int |  |
| driveHugepagesOffset | int |  |
| computeHugepages | int |  |
| computeHugepagesOffset | int |  |
| s3FrontendHugepages | int |  |
| s3FrontendHugepagesOffset | int |  |
| envoyCores | int |  |
| nfsContainers | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS containers |
| nfsCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS containers |
| nfsExtraCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS cores per container |
| nfsFrontendHugepages | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS extra cores per container |
| nfsFrontendHugepagesOffset | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage allocation for NFS frontend |

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

## StartIoConditions

| JSON Field | Type | Description |
|------------|------|-------------|
| minNumDrives | int | takes precedence over the `affinity` field |

---

## WekaClusterSpecOverrides

| JSON Field | Type | Description |
|------------|------|-------------|
| allowS3ClusterDestroy | bool |  |
| disregardRedundancy | bool | disregard redundancy constraints, useful for testing, should not be used in production as misaligns failure domains |
| driversLoaderImage | string | disregard redundancy constraints, useful for testing, should not be used in production as misaligns failure domains |
| forceAio | bool | force weka to use drives in aio mode and not direct nvme (impacts performance, but might serve as a fallback in case of incompatible device) |
| postFormClusterScript | string | force weka to use drives in aio mode and not direct nvme (impacts performance, but might serve as a fallback in case of incompatible device) |
| upgradeForceReplace | bool | unsafe operation, skips graceful stop of weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel |
| upgradeForceReplaceDrives | bool | unsafe operation, skips graceful stop of drive weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel |
| upgradeAllAtOnce | bool | unsafe operation, skips graceful stop of drive weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel |
| upgradePaused | bool | unsafe operation, should not be used unless instructed explicitly by weka personnel |
| upgradePausePreCompute | bool | unsafe operation, should not be used unless instructed explicitly by weka personnel |

---

## CsiConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| endpointsSubnets | []string | When using compositeLabels, weka containers will be spread considering all labels |
| csiGroup | string | with best effort, but even distribution is not guaranteed |
| advanced | *AdvancedCsiConfig |  |

---

## PVCConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string |  |
| path | string |  |

---

## RoleCoreIds

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | []int | omitted, no explicit core pinning will be applied for that role. |
| drive | []int |  |
| s3 | []int |  |
| nfs | []int |  |

---

## EncryptionConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| vault | *VaultConfig | Name of the transit key. defaults to "weka-key" |

---

## ClusterMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| containers | ContainersMetrics |  |
| ioStats | IoStats |  |
| drives | DriveMetrics |  |
| alertsCount | IntMetric |  |
| clusterStatus | StringMetric |  |
| capacity | CapacityMetrics |  |
| numFailures | map[string]FloatMetric |  |
| lastUpdate | metav1.Time |  |
| filesystem | FilesystemMetrics |  |

---

## ClusterPrinterColumns

| JSON Field | Type | Description |
|------------|------|-------------|
| computeContainers | StringMetric |  |
| driveContainers | StringMetric |  |
| drives | StringMetric |  |
| throughput | StringMetric |  |
| iops | StringMetric |  |
| filesystemCapacity | StringMetric | Information about filesystem capacity: Available/Used |

---

## RoleTopologySpreadConstraints

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | []v1.TopologySpreadConstraint |  |
| drive | []v1.TopologySpreadConstraint |  |
| s3 | []v1.TopologySpreadConstraint |  |
| nfs | []v1.TopologySpreadConstraint |  |

---

## RoleAffinity

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *v1.Affinity |  |
| drive | *v1.Affinity |  |
| s3 | *v1.Affinity |  |
| nfs | *v1.Affinity |  |

---

## NetworkSelector

| JSON Field | Type | Description |
|------------|------|-------------|
| subnet | string |  |
| min | int |  |
| max | int |  |
| deviceNames | []string |  |

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

## VaultConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| address | string | Vault address, e.g. "https://vault.example.com:8200". |
| role | string | Vault address, e.g. "https://vault.example.com:8200". |
| authPath | string | Role to authenticate as in Vault. |
| transitPath | string | Path under auth/ that the weka uses for login. defaults to "kubernetes" |
| method | string | Vault Auth method (only “kubernetes” is supported  on operator side.) |
| keyName | string | Vault Auth method (only “kubernetes” is supported  on operator side.) |

---

## ContainersMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| drive | ContainerMetrics |  |
| compute | ContainerMetrics |  |
| s3 | *ContainerMetrics |  |
| nfs | *ContainerMetrics |  |

---

## IoStats

| JSON Field | Type | Description |
|------------|------|-------------|
| throughput | StatusThroughput |  |
| iops | StatusIops |  |

---

## DriveMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| counters | EntityStatefulNum |  |
| failures | []DriveFailures |  |

---

## CapacityMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| totalBytes | IntMetric |  |
| unprovisionedBytes | IntMetric |  |
| unavailableBytes | IntMetric |  |
| hotSpareBytes | IntMetric |  |

---

## FilesystemMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| totalProvisionedCapacity | IntMetric | FilesystemMetrics contains metrics about filesystem usage |
| totalUsedCapacity | IntMetric | TotalProvisionedCapacity is the sum of total_budget for all filesystems |
| totalAvailableCapacity | IntMetric | TotalUsedCapacity is the sum of used_total for all filesystems |
| totalProvisionedSSDCapacity | IntMetric | TotalAvailableCapacity is the difference between TotalProvisionedCapacity and TotalUsedCapacity |
| totalUsedSSDCapacity | IntMetric | TotalAvailableCapacity is the difference between TotalProvisionedCapacity and TotalUsedCapacity |
| totalAvailableSSDCapacity | IntMetric | SSD-specific metrics |
| hasTieredFilesystems | bool | Object Store metrics |
| totalObsCapacity | IntMetric | Object Store metrics |
| obsBucketCount | IntMetric | Object Store metrics |
| activeObsBucketCount | IntMetric |  |

---

## ContainerMetrics

| JSON Field | Type | Description |
|------------|------|-------------|
| numContainers | EntityStatefulNum |  |
| processes | EntityStatefulNum |  |
| cpuUtilization | FloatMetric |  |

---

## StatusThroughput

| JSON Field | Type | Description |
|------------|------|-------------|
| read | IntMetric | Information about filesystem capacity: Available/Used |
| write | IntMetric | Information about filesystem capacity: Available/Used |

---

## StatusIops

| JSON Field | Type | Description |
|------------|------|-------------|
| read | IntMetric |  |
| write | IntMetric |  |
| metadata | IntMetric |  |
| total | IntMetric |  |

---

## EntityStatefulNum

| JSON Field | Type | Description |
|------------|------|-------------|
| active | IntMetric |  |
| created | IntMetric |  |
| desired | IntMetric |  |

---

## DriveFailures

| JSON Field | Type | Description |
|------------|------|-------------|
| serialId | string |  |
| wekaDriveId | string |  |

---

