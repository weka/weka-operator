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
- [NfsConfig](#nfsconfig)
- [S3Config](#s3config)
- [SmbwConfig](#smbwconfig)
- [TelemetryConfig](#telemetryconfig)
- [ClusterMetrics](#clustermetrics)
- [ClusterPrinterColumns](#clusterprintercolumns)
- [RoleTopologySpreadConstraints](#roletopologyspreadconstraints)
- [RoleAffinity](#roleaffinity)
- [DriveTypesRatio](#drivetypesratio)
- [NetworkSelector](#networkselector)
- [AdvancedCsiConfig](#advancedcsiconfig)
- [VaultConfig](#vaultconfig)
- [InternalEncryptionConfig](#internalencryptionconfig)
- [TelemetryExport](#telemetryexport)
- [ContainersMetrics](#containersmetrics)
- [IoStats](#iostats)
- [DriveMetrics](#drivemetrics)
- [CapacityMetrics](#capacitymetrics)
- [FilesystemMetrics](#filesystemmetrics)
- [SplunkExportConfig](#splunkexportconfig)
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
| template | string | A template/strategy of how to build a cluster, right now only "dynamic" supported, explicitly specifying config of a cluster |
| image | string | full container image name in format of quay.io/weka.io/weka-in-container:VERSION |
| imagePullSecret | string | image pull secret to use for pulling the image |
| driversDistService | string | endpoint for distribution service, global https://drivers.weka.io or in-k8s-cluster "https://weka-drivers-dist.namespace.svc.cluster.local:60001" |
| nodeSelector | map[string]string | node selector for the weka containers |
| roleNodeSelector | RoleNodeSelector | node selector for the weka containers per role, overrides global nodeSelector |
| roleAnnotations | RoleAnnotations | annotations for the weka containers per role |
| roleNetworkSelector | RoleNetworkSelector | network selector for the weka containers per role, overrides global network |
| failureDomain | *FailureDomain | failure domain configuration for weka containers |
| podConfig | *PodConfiguration | advanced pod affinities configuration |
| cpuPolicy | CpuPolicy | cpu policy to use for scheduling cores for weka, unless instructed by weka team, keep default of auto<br>manual and shared are same, with shared being deprecated<br>when manual is used - no exclusive cores will be allocaated on k8s/cgroup level, assuming good alignment of cores usage across different applications, like weka and slurm<br>there is no need to specify siblings in this list, but on the side of other applications like slurm, both weka core and its siblings should be excluded from used cpu set |
| tracesConfiguration | *TracesConfiguration | traces capacities configuration for weka containers |
| tolerations | []string | simplified tolerations, checked only by key existence, expanding to NoExecute\|NoSchedule tolerations |
| rawTolerations | []v1.Toleration | tolerations in standard k8s format |
| wekaHome | *WekaHomeConfig | weka home configuration |
| ipv6 | bool | use ipv6 for weka cluster networking configuration |
| additionalMemory | AdditionalMemory | additional memory to allocate for weka containers |
| ports | ClusterPorts | port allocation for weka containers, if not set, free range will be auto selected. Currently allocated ports can be seen in wekacluster.status.ports |
| operatorSecretRef | string | reference to the secret containing the weka system credentials used by operator, used in flow of migration |
| expandEndpoints | []string | endpoint of existing weka cluster, containers created for this k8s-driver cluster will join existing weka cluster, used in flow of migration |
| dynamicTemplate | *WekaConfig | weka cluster topology configuration |
| network | Network | weka cluster network configuration |
| hotSpare | int | A hot spare is reserved capacity designed to handle data rebuilds while maintaining the system's net capacity, even in the event of failure domains being lost<br>See: https://docs.weka.io/weka-system-overview/ssd-capacity-management#hot-spare |
| redundancyLevel | int | storage capacity dedicated to system protection (2/4). https://docs.weka.io/weka-system-overview/ssd-capacity-management#protection-level |
| stripeWidth | int | stripe width is the number of blocks within a common protection set, ranging from 3 to 16 https://docs.weka.io/weka-system-overview/ssd-capacity-management#stripe-width |
| leadershipRaftSize | *int | size of raft for leadership, defaults to 5, 5/9 are supported |
| bucketRaftSize | *int | size of raft for buckets, defaults to 5, 5/9 are supported |
| startIoConditions | *StartIoConditions | conditions that must be met before starting IO |
| gracefulDestroyDuration | metav1.Duration | During this period the cluster will not be destroyed (protection from accidental deletion)<br>Note: due to discrepancies in validation vs parsing, we use a Pattern instead of `Format=duration`. See<br>https://bugzilla.redhat.com/show_bug.cgi?id=2050332<br>https://github.com/kubernetes/apimachinery/issues/131<br>https://github.com/kubernetes/apiextensions-apiserver/issues/56 |
| overrides | *WekaClusterSpecOverrides |  |
| csiConfig | CsiConfig |  |
| globalPVC | *PVCConfig |  |
| serviceAccountName | string |  |
| roleCoreIds | RoleCoreIds | RoleCoreIds defines a list of CPU core IDs (as seen by the host) that should<br>be assigned to containers of the specific role when CpuPolicy is set to<br>"manual". If the slice for the given role is empty, core ids will not be<br>set for that role, and the manual policy will fail validation on pod start.<br>NOTE: The semantics are the same as for NodeSelector/Annotations structures –<br>a single list per role which will be copied to every container of that role.<br>Users are responsible to provide a set that makes sense for their topology.<br>Example:<br>roleCoreIds:<br>compute: [0,1,2,3]<br>drive:   [4,5,6,7]<br>will result in every compute container getting coreIds [0,1,2,3] and every<br>drive container getting [4,5,6,7]. |
| encryption | *EncryptionConfig |  |
| nfs | *NfsConfig |  |
| s3 | *S3Config |  |
| smbw | *SmbwConfig |  |
| telemetry | *TelemetryConfig | Telemetry configuration for exporting audit logs and other telemetry data |

---

## WekaClusterStatus

| JSON Field | Type | Description |
|------------|------|-------------|
| status | WekaClusterStatusEnum |  |
| conditions | []metav1.Condition |  |
| clusterID | string |  |
| traceId | string |  |
| spanId | string |  |
| lastAppliedImage | string | Explicit field for upgrade tracking, more generic lastAppliedSpec might be introduced later |
| lastAppliedSpec | string |  |
| ports | ClusterPorts |  |
| stats | *ClusterMetrics |  |
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
| drive | *map[string]string | nodeSelector for drive weka containers |
| s3 | *map[string]string | nodeSelector for s3 weka containers |
| nfs | *map[string]string | nodeSelector for nfs weka containers |
| smbw | *map[string]string | nodeSelector for smbw weka containers |
| dataServices | *map[string]string | nodeSelector for data services weka containers |

---

## RoleAnnotations

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *map[string]string | annotations for compute weka containers |
| drive | *map[string]string | annotations for drive weka containers |
| s3 | *map[string]string | annotations for s3 weka containers |
| nfs | *map[string]string | annotations for nfs weka containers |
| smbw | *map[string]string | annotations for smbw weka containers |
| dataServices | *map[string]string | annotations for data services weka containers |

---

## RoleNetworkSelector

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *Network | network selector for compute weka containers |
| drive | *Network | network selector for drive weka containers |
| s3 | *Network | network selector for s3 weka containers |
| nfs | *Network | network selector for nfs weka containers |
| smbw | *Network | network selector for smbw weka containers |
| dataServices | *Network | network selector for data services weka containers |

---

## FailureDomain

| JSON Field | Type | Description |
|------------|------|-------------|
| label | *string | label used for spreading the weka containers across different failure domains (if set)<br>nodes that have the same value for the label will be considered as a single failure domain |
| skew | *int | skew for the failure domain, if set, the weka containers will be spread with the skew in mind<br>(only applicable if `label` is set) |
| compositeLabels | []string | If multiple labels are specified, the failure domain will be the combination of the labels.<br>If `compositeLabels` is set, `label` and `skew` will be ignored.<br>When using compositeLabels, weka containers will be spread considering all labels<br>with best effort, but even distribution is not guaranteed |

---

## PodConfiguration

| JSON Field | Type | Description |
|------------|------|-------------|
| topologySpreadConstraints | *runtime.RawExtension | controls the distribution of weka containers across the failure domains |
| roleTopologySpreadConstraints | *RoleTopologySpreadConstraints | takes precedence over the `topologySpreadConstraints` |
| affinity | *runtime.RawExtension | advanced scheduling constraints |
| roleAffinity | *RoleAffinity | affinity per container role<br>takes precedence over the `affinity` field |

---

## TracesConfiguration

| JSON Field | Type | Description |
|------------|------|-------------|
| maxCapacityPerIoNode | int |  |
| ensureFreeSpace | int |  |
| dumperConfigMode | DumperConfigMode |  |

---

## WekaHomeConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| endpoint | string |  |
| allowInsecure | bool |  |
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
| smbw | int |  |
| dataServices | int |  |

---

## ClusterPorts

| JSON Field | Type | Description |
|------------|------|-------------|
| basePort | int | We should not be updating Spec, as it's a user interface and we should not break ability to update spec file<br>Therefore, when BasePort is 0, and Range as 0, we have application level defaults that will be written in here |
| portRange | int |  |
| lbPort | int |  |
| lbAdminPort | int |  |
| s3Port | int |  |
| managementProxyPort | int |  |

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
| computeExtraCores | int |  |
| driveExtraCores | int |  |
| s3ExtraCores | int |  |
| driveHugepages | int |  |
| driveHugepagesOffset | int |  |
| computeHugepages | int |  |
| computeHugepagesOffset | int |  |
| s3FrontendHugepages | int |  |
| s3FrontendHugepagesOffset | int |  |
| envoyCores | int |  |
| nfsContainers | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS containers |
| nfsCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS cores per container |
| nfsExtraCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of NFS extra cores per container |
| nfsFrontendHugepages | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage allocation for NFS frontend |
| nfsFrontendHugepagesOffset | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage offset for NFS frontend |
| smbwContainers | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of SMB-W containers (3-8) |
| smbwCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of SMB-W cores per container |
| smbwExtraCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of SMB-W extra cores per container |
| smbwFrontendHugepages | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage allocation for SMB-W frontend |
| smbwFrontendHugepagesOffset | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage offset for SMB-W frontend |
| driveCapacity | int | DriveCapacity is the capacity in GiB to allocate per single virtual drive.<br>NumDrives multiplied by DriveCapacity gives the total capacity requested by each drive container.<br>This value determines how much capacity each container receives from shared drives. |
| containerCapacity | int | ContainerCapacity specifies the total capacity (in GiB) requested by each container when using shared drives via SSD proxy.<br>This value takes precedence over DriveCapacity when both are set. It allows more flexible capacity allocation. |
| driveTypesRatio | *DriveTypesRatio | DriveTypesRatio specifies the desired ratio of drive types (TLC vs QLC) when allocating drives for the cluster. |
| dataServicesContainers | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of data services containers |
| dataServicesCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of data services cores per container |
| dataServicesExtraCores | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: number of data services extra cores per container |
| dataServicesHugepages | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage allocation for data services frontend |
| dataServicesHugepagesOffset | int | EXPERIMENTAL, ALPHA STATE, should not be used in production: hugepage offset for data services frontend |

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

## StartIoConditions

| JSON Field | Type | Description |
|------------|------|-------------|
| minNumDrives | int | minumum number of drives that should be added to the cluster before starting IO |

---

## WekaClusterSpecOverrides

| JSON Field | Type | Description |
|------------|------|-------------|
| allowS3ClusterDestroy | bool |  |
| allowSmbwClusterDestroy | bool |  |
| disregardRedundancy | bool | disregard redundancy constraints, useful for testing, should not be used in production as misaligns failure domains |
| driversBuildId | *string | can be used to specify a build_id for a driver in the distributor service, keep empty for auto detection default |
| driversLoaderImage | string | image to be used for loading drivers, do not use unless explicitly instructed by Weka team |
| forceAio | bool | force weka to use drives in aio mode and not direct nvme (impacts performance, but might serve as a fallback in case of incompatible device) |
| postFormClusterScript | string | script to run post cluster create (i.e before starting io) |
| upgradeForceReplace | bool | unsafe operation, skips graceful stop of weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel |
| upgradeForceReplaceDrives | bool | unsafe operation, skips graceful stop of drive weka container for a quick replacement to a new image, should not be used unless instructed explicitly by weka personnel |
| upgradeAllAtOnce | bool | unsafe operation, should not be used unless instructed explicitly by weka personnel |
| upgradePaused | bool | Pause upgrade |
| upgradePausePreCompute | bool | Prevent from moving into compute phase |
| podTerminationDeactivationTimeout | *metav1.Duration | Timeout duration for deactivating pods that are terminating longer than this duration.<br>When nil (default), the default timeout of 5 minutes is used.<br>When set to 0, deactivation of terminating pods is disabled.<br>Otherwise, the specified duration is used. |
| cancelDeletion | bool | Cancel deletion of the cluster if it is in graceful destroy period, a disaster recovery mechanism |

---

## CsiConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| endpointsSubnets | []string |  |
| csiGroup | string |  |
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
| compute | []int |  |
| drive | []int |  |
| s3 | []int |  |
| nfs | []int |  |
| smbw | []int |  |
| dataServices | []int |  |

---

## EncryptionConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| vault | *VaultConfig |  |
| internal | *InternalEncryptionConfig | InternalConfig defines internal encryption settings, encryption key stored in weka configuration, for production systems use real KMS, however this mode can be useful to evaluate performance of encrypted filesystems |

---

## NfsConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| interfaces | []string |  |
| ipRanges | []string |  |

---

## S3Config

| JSON Field | Type | Description |
|------------|------|-------------|
| clusterCreateArgs | []string | No overlap validation, only appended to the cluster create command as-is<br>Useful for settings such as: `--envoy-max-requests 1150 --envoy-max-connections 1300 --envoy-max-pending-requests 1450`<br>Not propagated to already created cluster, and direct weka control should be used for that |

---

## SmbwConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| clusterName | string | ClusterName is the SMB-W cluster name, defaults to "default" |
| domainName | string | DomainName is the domain name for SMB-W, required for SMB-W cluster creation |
| domainJoinSecret | string |  |
| userName | string |  |
| ipRanges | []string | IpRanges specifies floating IP ranges for SMB-W high availability |

---

## TelemetryConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| exports | []TelemetryExport | List of telemetry exports to configure |

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
| compute | *runtime.RawExtension |  |
| drive | *runtime.RawExtension |  |
| s3 | *runtime.RawExtension |  |
| nfs | *runtime.RawExtension |  |
| smbw | *runtime.RawExtension |  |

---

## RoleAffinity

| JSON Field | Type | Description |
|------------|------|-------------|
| compute | *runtime.RawExtension |  |
| drive | *runtime.RawExtension |  |
| s3 | *runtime.RawExtension |  |
| nfs | *runtime.RawExtension |  |
| smbw | *runtime.RawExtension |  |

---

## DriveTypesRatio

| JSON Field | Type | Description |
|------------|------|-------------|
| tlc | int |  |
| qlc | int |  |

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
| role | string | Role to authenticate as in Vault. |
| authPath | string | Path under auth/ that the weka uses for login. defaults to "kubernetes" |
| transitPath | string | Transit engine mount path, defaults "transit". |
| method | string | Vault Auth method (only “kubernetes” is supported  on operator side.) |
| keyName | string | Name of the transit key. defaults to "weka-key" |

---

## InternalEncryptionConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| enabled | bool |  |

---

## TelemetryExport

| JSON Field | Type | Description |
|------------|------|-------------|
| name | string | Name is the unique identifier for this export |
| sources | []string | Sources specifies which telemetry sources to export (e.g., "audit") |
| splunk | *SplunkExportConfig | Splunk configuration for Splunk HEC export |

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
| totalProvisionedCapacity | IntMetric | TotalProvisionedCapacity is the sum of total_budget for all filesystems |
| totalUsedCapacity | IntMetric | TotalUsedCapacity is the sum of used_total for all filesystems |
| totalAvailableCapacity | IntMetric | TotalAvailableCapacity is the difference between TotalProvisionedCapacity and TotalUsedCapacity |
| totalProvisionedSSDCapacity | IntMetric | SSD-specific metrics |
| totalUsedSSDCapacity | IntMetric |  |
| totalAvailableSSDCapacity | IntMetric |  |
| hasTieredFilesystems | bool | Object Store metrics |
| totalObsCapacity | IntMetric |  |
| obsBucketCount | IntMetric |  |
| activeObsBucketCount | IntMetric |  |

---

## SplunkExportConfig

| JSON Field | Type | Description |
|------------|------|-------------|
| authTokenSecretRef | string | AuthTokenSecretRef references a secret containing the Splunk HEC authentication token.<br>Format: "secretName.keyName" where secretName is the name of the secret in the same namespace<br>and keyName is the key within the secret's data that contains the token. |
| endpoint | string | Endpoint is the Splunk HEC endpoint URL (maps to --target in weka CLI) |
| caCertSecretRef | *string | CACertSecretRef optionally references a secret containing a user-provided CA certificate PEM file.<br>Format: "secretName.keyName" where secretName is the name of the secret in the same namespace<br>and keyName is the key within the secret's data that contains the certificate.<br>Maps to --ca-cert in weka CLI. Empty string is treated same as nil (de-configures if was configured).<br>Mutually exclusive with VerifyWithClusterCACert. |
| allowUnverifiedCertificate | bool | AllowUnverifiedCertificate allows accessing without verifying the target certificate.<br>Maps to --allow-unverified-certificate in weka CLI. |
| verifyWithClusterCACert | bool | VerifyWithClusterCACert uses the Weka cluster's internal CA certificate to verify.<br>Maps to --verify-with-cluster-cacert in weka CLI.<br>Mutually exclusive with CACertSecretRef. |

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
| read | IntMetric |  |
| write | IntMetric |  |

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

