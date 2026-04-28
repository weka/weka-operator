# weka-operator

Weka operator for Kubernetes clusters

## Install

Helm does not upgrade CRDs, so apply the chart's CRDs with server-side apply before every install or upgrade.

```bash
VERSION=vX.Y.Z  # the operator release to install

helm show crds oci://quay.io/weka.io/helm/weka-operator --version "$VERSION" | \
  kubectl apply --server-side -f -

helm upgrade --install weka-operator oci://quay.io/weka.io/helm/weka-operator \
  --namespace weka-operator-system --create-namespace \
  --version "$VERSION"
```

## Values

### Admission control

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| admissionPolicies | object | `{"mode":"relaxed"}` | Admission policies. `mode` is the default mode applied to all policies: `relaxed` uses the "relaxed" column of the per-policy mapping, `strict` the "strict" column. Per-policy overrides (`policies`, a map of policy name to `default`, `warn` or `error`, commented out by default) win over `mode`. The available policies and their strict/relaxed defaults are listed in the commented `policies` block in values.yaml. |
| enableAdmissionControl | bool | `true` | Admission control master switch. When true, installs the VWC with failurePolicy=Fail; every operator restart briefly blocks WekaCluster/WekaClient writes. Per-object bypass: set the `weka.io/skip-admission` label on the CR. Emergency recovery: `kubectl delete validatingwebhookconfiguration weka-operator-validating-webhook-configuration`. |

### NFS/SMB

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| allowMultipleProtocolsPerNode | bool | `false` | If true, allows multiple network protocols containers per node, like NFS for multiple weka clusters or S3+NFS on same nodes. In case of multiple clusters, upgrades need to be synchronized / spares kept for per-node eviction. |
| nfs.lockmanagerPort | int | `18001` | Port for the NFS lock manager service. |
| nfs.mountdPort | int | `18000` | Port for the NFS mountd service. |
| nfs.notifyPort | int | `18002` | Port for the NFS notify service. |
| smbw.shmSize | string | `"8Gi"` | Size of /dev/shm for SMB-W containers (for corosync shared memory). |

### Advanced

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| allowRotateNonAnnotatedPodConfigHash | bool | `false` | If true, rotate pods for containers that have no pod-config-version annotation yet (pre-existing containers from before this feature). Default false to avoid mass restarts on upgrade. |
| enableClusterApi | bool | `false` | Experimental: enables a custom HTTP API endpoint as an alternative to the K8s API. |
| enablePodConfigCodeVersionRotation | bool | `false` | If true, include WekaRuntimeVersion in the pod config hash calculation. When enabled, bumping WekaRuntimeVersion triggers coordinated rolling pod rotation. Default false to avoid unexpected rotations on operator upgrade. |
| localDataPvc | string | `""` | Name of a PVC used as the default local data PVC for containers that do not set one in their spec. |
| podConfigVersion | string | `"1"` | Manual version bump to trigger pod rotation when pod config shape changes via helm. |
| podSecurityContext | object | `{}` | Pod-level securityContext applied to every privileged / hostPath pod produced by the operator. Opt-in: leave empty for no override. Accepts any subset of the standard corev1.PodSecurityContext shape; every field you set flows through to the produced pods unchanged. Example to satisfy Kyverno's require-apparmor-on-privileged-or-hostpath (Unconfined is recommended for weka, as its containers rely on DPDK/RDMA/device access that RuntimeDefault may restrict): `{appArmorProfile: {type: Unconfined}}`. |
| priorityClasses | object | `{"defaultValues":{"initial":900000000,"targeted":1000000000},"initial":"weka-initial-no-evict","targeted":"weka-targeted-no-evict"}` | Priority classes. `initial` and `targeted` are the priority class names for initial weka containers and for re-scheduled weka containers, node agents, CSI controller and CSI node server. The operator always creates the default priority classes weka-initial-no-evict and weka-targeted-no-evict; override the names to use your own existing priority classes instead. `defaultValues` holds the values of the default priority classes that are always created. |
| syslogPackage | string | `"auto"` | Syslog package choice: "auto" (go-syslog if available, otherwise syslog-ng), "go-syslog" or "syslog-ng". |

### Drives & capacity

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| allowSingleParity | bool | `false` | Lower the clusterCapacity protection floor from the production 3+2+0 (stripeWidth/data>=3, redundancyLevel/parity>=2, hotSpare>=0 / hot spare optional) to single-parity 2+1+0, enabling QA/test clusters such as 2+1 (minFdNum=3). QA/TEST ONLY: a single parity chunk leaves a stripe unprotected during rebuild. When true, the operator also sets the allow_1_parity weka override at cluster formation (weka rejects parity=1 without it). |
| capacityPlannerConstraints | object | `{"driveSharing":{"computeToQlcDriveCoreRatio":0,"computeToTlcDriveCoreRatio":1},"fullDrives":{"computeToDriveCoreRatio":null},"maxCoresPerContainer":19}` | Capacity planner constraints. `maxCoresPerContainer` is Weka's own per-container core cap (drive or compute). `driveSharing.computeToTlcDriveCoreRatio` and `driveSharing.computeToQlcDriveCoreRatio` set the compute:drive-core ratios for TLC and QLC drives: requiredComputeCores = max(totalDriveCores, ceil(tlcRatio*tlcCores + qlcRatio*qlcCores)), and the drive-core count itself is always a hard 1:1 floor regardless of these ratios. `fullDrives.computeToDriveCoreRatio`: when unset (null), the planner prefers 2:1 and, when the compute nodes cannot host that, relaxes toward 1:1 (infeasible only below 1:1); set a number to enforce that ratio strictly. |
| clusterCapacity.deadbandFraction | float | `0.05` | Relative capacity deadband: a clusterCapacity growth is ignored when the shortfall is smaller than desired capacity x this fraction. Avoids re-planning/thrashing on trivial bumps. Set to 0 to disable the deadband (any positive capacity delta triggers growth). |
| clusterCapacity.imbalanceFactor | float | `8` | Grow imbalance factor: when each new drive container would be >= this factor x the existing containers' average capacity, the planner lays out a fresh balanced set instead. |
| clusterCapacity.qlcCapacityPerCoreGiB | int | `51200` | Max QLC capacity (GiB) one drive core can serve. The default is 50 TiB. |
| clusterCapacity.tlcCapacityPerCoreGiB | int | `5120` | Max TLC capacity (GiB) one drive core can serve. The default is 5 TiB. |
| driveSharing.driveTypesRatio | object | `{"qlc":10,"tlc":1}` | Global default ratio of drive types (TLC vs QLC) when using drive sharing, as integer parts of relative proportions. Used when spec.dynamicTemplate.driveTypesRatio is not set on the WekaCluster; can be overridden per cluster via that field. Drive sharing is enabled by setting containerCapacity in spec.dynamicTemplate of WekaCluster. For example tlc: 1, qlc: 0 = 100% TLC; tlc: 4, qlc: 1 = 80% TLC, 20% QLC; tlc: 1, qlc: 1 = 50% TLC, 50% QLC. NOTE: tlc must be > 0. QLC-only configurations (tlc: 0) are not supported. |
| driveSharing.enableDynamicDriveScalingForSharedDrives | bool | `false` | Enable dynamic drive scaling for shared drives mode (containerCapacity or driveCapacity configured; traditional numDrives-only mode is not affected). When false: containers are never extended in place on a spec change - existing drive containers won't automatically allocate additional drives when containerCapacity increases or driveTypesRatio changes, and clusterCapacity growth is satisfied by CREATING new containers on fresh failure domains only (reported infeasible if no free FDs/nodes can hold the delta). To grow a single container you must delete and recreate it. Initial allocation still works. When true (opt-in): containers automatically allocate additional drives to match the new capacity/ratio, and clusterCapacity may grow existing drive containers in place. Capacity-only grows are applied live (no pod restart); a grow that also needs more cores/hugepages is not applied automatically (the operator only emits a warning) - the pod must be terminated manually to pick up the new spec. |
| driveSharing.enforceMinDrivesPerTypePerCore | bool | `true` | Enforce minimum drives per type per core when using drive sharing with mixed TLC/QLC. When true: per-type constraints (cores <= tlcDrives AND cores <= qlcDrives). When false: combined constraint (cores <= tlcDrives + qlcDrives <= maxDrives). Only affects mixed TLC/QLC configurations; single-type allocations behave the same either way. |
| driveSharing.maxOverProvisionFraction | float | `0.2` | Max fraction by which creating new failure domains may over-provision a drive pool's desired capacity. A create-new that would exceed desiredRaw * (1 + maxOverProvisionFraction) is not allowed (falls back to grow or infeasible). |
| driveSharing.maxVirtualDrivesPerCore | int | `8` | Maximum number of virtual drives per CPU core when using drive sharing. |
| driveSharing.minGrowthFraction | float | `0.2` | Minimum relative per-container growth (target-cur)/cur to grow an existing drive container in place; smaller grows are skipped (prefer creating a new FD at the uniform chunk size T instead, or emit infeasible if no spare node is available). |
| driveSharing.smallBigDiskSizesMaxProportionFactor | int | `100` | Max allowed ratio between the largest and smallest drive in the IndirectionUnitBig pool. Overrides weka's built-in limit (MAX_DRIVE_CAPACITY_RATIO=8). Relevant for drive-sharing clusters that mix very differently sized drives. |
| driveSharing.ssdProxy | object | `{"hugepagesOffsetMiB":null,"imageOverride":""}` | SSD proxy settings (drive sharing mode). `hugepagesOffsetMiB` is the hugepages offset in MiB kept as a buffer from weka's --memory for SSD proxy containers (null uses the operator default of 200 MiB). `imageOverride`, if set, is used as the ssdProxy container image instead of the cluster image; it is also the fallback target image for the `rotate-ssdproxy` WekaManualOperation when payload.rotateSsdProxyPayload.targetImage is left empty. |
| fullPcpusOnly | bool | `false` | Force SMT/full-pcpus-only CPU alignment (round weka pod CPU up to an even number on hyperthreaded nodes). Default false = auto-detect per node from the kubelet's cpuManagerPolicyOptions full-pcpus-only setting (needs nodes/proxy RBAC). Set true to force alignment operator-wide, e.g. when the kubelet configz read is unavailable. |
| protection | object | `{"hotSpare":0,"redundancyLevel":0,"stripeWidth":0}` | Default protection scheme applied to WekaClusters that leave these fields at 0. A non-zero per-cluster spec value wins; a spec value of 0 is treated as "unset" and falls back to the default below (so a cluster cannot force e.g. hotSpare=0 while a non-zero default is configured here). stripeWidth/redundancyLevel: 0 means "no default", so the operator skips the matching `weka cluster update` call and the weka CLI default applies. hotSpare: the operator always runs `weka cluster hot-spare <value>` at formation, so 0 here sets hot spare explicitly to 0 (it is NOT left at a weka CLI default). Note: clusterCapacity clusters are still rejected at admission unless the *effective* protection (spec value, else this default) meets the 3+2+0 floor (see allowSingleParity). |
| removeFailedDrives | bool | `false` | Remove failed drives from Weka once drive containers report added drives that are not aligned with allocations. |

### Images

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| builderImages | object | `{"default":"quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22-v1","nixos":{"gcc15":"quay.io/weka.io/weka-drivers-build-images:builder-nixos-gcc15-v2"},"ubuntu24":"quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24-v1"}` | Driver builder images. `default` is the Ubuntu 22 builder, `ubuntu24` is used for Ubuntu 24 nodes, and `nixos` holds NixOS builders keyed by the gcc major of the NixOS kernel they are built for (a node's OS is reported by discovery as "nixos-gcc<major>"). |
| envoyImage | string | `"docker.io/envoyproxy/envoy:v1.31-latest"` | Management proxy (envoy) image. |
| image.repository | string | `"quay.io/weka.io/weka-operator"` | Operator image repository. |
| image.tag | string | the chart version | Operator image tag. Override only. |
| imagePullSecret | string | `""` | Name of an existing imagePullSecret to attach to operator pods (optional). |
| kubeProxyImage | string | `"quay.io/brancz/kube-rbac-proxy:v0.21.0"` | kube-rbac-proxy sidecar image, used in front of the operator metrics endpoint. |
| maintenanceImage | string | `"quay.io/weka.io/busybox:1.37.0"` | Image used for maintenance pods; can be overridden with any other basic linux image. If required, a pull secret for it can be set with `maintenanceImagePullSecret` (commented out by default). |
| signDrivesImage | string | `"quay.io/weka.io/weka-sign-tool:cfbc60804cb627bcd3b7fde8e9c5e82f6f24c0be-multiarch"` | Image of the drive signing tool. |
| taskmon | object | `{"defaultImage":"quay.io/weka.io/taskmon:92b35aa657e9aba4782b4530b81107fff4c19847_x86_64"}` | Taskmon settings; `defaultImage` is the default taskmon image. |
| wekaPodRuntime.image.pullPolicy | string | `"IfNotPresent"` | Go pod runtime image pull policy. |
| wekaPodRuntime.image.repository | string | `"quay.io/weka.io/weka-pod-runtime"` | Go pod runtime image repository. |
| wekaPodRuntime.image.tag | string | the chart version | Go pod runtime image tag. |
| wekaPodRuntime.usePythonFallback | bool | `false` | Run weka_runtime.py instead of the Go pod runtime. |

### Capacity planner tool

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| capacityPlanner.nodeSelector | object | `{}` | nodeSelector of the capacity-planner toolbox pod. |
| capacityPlanner.resources | object | `{"limits":{"cpu":2,"memory":"4Gi"},"requests":{"cpu":"50m","memory":"64Mi"}}` | Resources of the capacity-planner toolbox pod. Requests are small so it schedules anywhere; limits are deliberately loose, since the pod idles until someone execs a plan run and admission policies commonly reject a container with no limits at all. Raise the memory limit if a large plan run gets OOM-killed. |
| capacityPlanner.tolerations | list | `[]` | Tolerations of the capacity-planner toolbox pod. |
| deployCapacityPlanner | bool | `false` | Deploy a standalone capacity-planner toolbox pod (runs `weka-capacity` from the operator image; exec into it to preview capacity plans without touching the operator pod). |

### Node lifecycle & cleanup

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| cleanupClientsOnNodeSelectorMismatch | bool | `false` | Delete client WekaContainers when their nodeSelector no longer matches the assigned node. |
| cleanupContainersOnTolerationsMismatch | bool | `false` | Delete WekaContainers (except aux) when node taints are no longer tolerated. |
| cleanupOnNodeSelectorMismatch | bool | `false` | Delete backend WekaContainers when their nodeSelector no longer matches the assigned node. Aux containers are unaffected. |
| cleanupRemovedNodes | string | `"auto"` | Removed-node backend cleanup mode when a backend WekaContainer's target node leaves the K8s cluster. Tri-state (quote the value):   "false" - never delete the container (previous default).   "true"  - delete the container immediately.   "auto"  - (default) hold the container in Stale status for a grace period, then             delete only if the node is still gone. Grace is 30m on managed cloud             (AWS/OCI) and 24h otherwise; if the node returns within the window the             container recovers instead of being torn down. An unset/empty value uses "auto"; an unrecognized value fails closed to "false". |
| evictContainerOnDeletion | bool | `false` | When a backend pod begins terminating (DeletionTimestamp set, including API-driven evictions / drains), transition the container to graceful deactivation rather than force-stopping. |
| evictedPodCleanup | object | `{"enabled":true,"interval":"2m"}` | Evicted-pod cleanup: a background goroutine periodically deletes operator-managed pods stuck in Phase=Failed, Reason=Evicted. `enabled` runs the cleanup goroutine; `interval` is its tick interval. |
| recreateUnhealthyEnvoyThrottlingEnabled | bool | `true` | Throttle envoy container recreation to one per cluster per minute when the envoy process is missing. |
| removalThrottlingEnabled | bool | `true` | Throttle container deactivation to one per role per cluster per minute, preventing cascading removals. |
| skipAwsTerminationLifecycleHook | bool | `false` | Disable all operator management of the AWS ASG termination lifecycle hook (detection and creation). Scale-down drive-drain protection is unavailable while set. Do not combine with a manually registered weka-drive-drain hook: the operator will not release held instances, so they will sit in Terminating:Wait until the hook's HeartbeatTimeout expires. |

### Hugepages

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| computeMaxHugepagesMiB | int | `360000` | Maximum hugepages (MiB) for compute containers. 0 = no cap. Applied during both initial container creation and hugepages updates. |
| hugepagesQlcRatio | int | `6000` | Hugepages capacity ratio for QLC drives, see `hugepagesTlcRatio`. |
| hugepagesTlcRatio | int | `1000` | Hugepages capacity ratio for TLC drives, used for compute containers. Hugepages are allocated from raw drive capacity: clusterHugepages = tlcCapacityGiB / hugepagesTlcRatio + qlcCapacityGiB / hugepagesQlcRatio. TLC drives require more hugepages per GiB than QLC drives. |
| hugepagesUpdate | object | `{"compute":false,"drive":false}` | Hugepages update propagation to existing compute/drive containers. User-set hugepages values (spec.dynamicTemplate.computeHugepages / driveHugepages) are always propagated to existing containers regardless of these flags. `compute` and `drive` control auto-calculation from cluster capacity for containers where no explicit value is set in the spec. S3/NFS/DataServices hugepages are always propagated using per-role defaults unless overridden in the spec. |

### CSI

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| csi.allowMountOptionOverrides | bool | `false` | Allow the CSI node driver to override mount options from PVC/Pod annotations during NodePublishVolume. Security-gated capability. TRUST IMPLICATION: when enabled, mount options are no longer fixed by the StorageClass alone. They can be influenced by annotations on the PVC/Pod, which any principal able to create a Pod/PVC in a namespace can set. This widens who can affect on-host mount behavior (e.g. caching/coherency, read-only vs read-write). Leave false unless you trust all such principals. The exact annotation keys honored are defined by the csi-wekafs driver version in use (see the csi-wekafs docs for the release pinned in `image`). Note: toggling this on an existing cluster rolls the CSI node DaemonSet. |
| csi.attacherImage | string | `"registry.k8s.io/sig-storage/csi-attacher:v4.9.0"` | csi-attacher sidecar image. |
| csi.controller | object | `{"resources":{"csiAttacher":{"limits":{"cpu":1,"memory":"1Gi"},"requests":{"cpu":"4m","memory":"48Mi"}},"csiProvisioner":{"limits":{"cpu":1,"memory":"3Gi"},"requests":{"cpu":"128m","memory":"128Mi"}},"csiResizer":{"limits":{"cpu":1,"memory":"2Gi"},"requests":{"cpu":"4m","memory":"48Mi"}},"csiSnapshotter":{"limits":{"cpu":1,"memory":"1Gi"},"requests":{"cpu":"4m","memory":"48Mi"}},"livenessProbe":{"limits":{"cpu":1,"memory":"1Gi"},"requests":{"cpu":"12m","memory":"48Mi"}},"wekafs":{"limits":{"cpu":1,"memory":"3Gi"},"requests":{"cpu":"128m","memory":"128Mi"}}}}` | CSI controller container resources, per container (wekafs, csiAttacher, csiProvisioner, csiResizer, csiSnapshotter, livenessProbe), each with `limits` and `requests`. |
| csi.hostNetwork | bool | `false` | Run the CSI pods with hostNetwork. |
| csi.image | string | `"quay.io/weka.io/csi-wekafs:v2.9.4"` | CSI driver (csi-wekafs) image. |
| csi.installationEnabled | bool | `false` | Install the embedded CSI driver. |
| csi.kubeletPath | string | `"/var/lib/kubelet"` | Kubelet path, in cases Kubernetes is installed not in the default folder. |
| csi.livenessProbeImage | string | `"registry.k8s.io/sig-storage/livenessprobe:v2.16.0"` | livenessprobe sidecar image. |
| csi.logLevel | int | `5` | CSI driver log level. |
| csi.node | object | `{"resources":{"csiRegistrar":{"limits":{"cpu":1,"memory":"1Gi"},"requests":{"cpu":"8m","memory":"52Mi"}},"livenessProbe":{"limits":{"cpu":1,"memory":"1Gi"},"requests":{"cpu":"12m","memory":"44Mi"}},"wekafs":{"limits":{"cpu":1,"memory":"2Gi"},"requests":{"cpu":"128m","memory":"128Mi"}}}}` | CSI node container resources, per container (wekafs, livenessProbe, csiRegistrar), each with `limits` and `requests`. |
| csi.preventNewWorkloadOnClientContainerNotRunning | bool | `true` | Prevent new workloads from being scheduled on a node while its client container is not running. |
| csi.provisionerImage | string | `"registry.k8s.io/sig-storage/csi-provisioner:v5.3.0"` | csi-provisioner sidecar image. |
| csi.registrarImage | string | `"registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.14.0"` | csi-node-driver-registrar sidecar image. |
| csi.resizerImage | string | `"registry.k8s.io/sig-storage/csi-resizer:v1.14.0"` | csi-resizer sidecar image. |
| csi.selinuxSupport | string | `"auto"` | SELinux support mode for the embedded CSI driver: "auto" (auto-detect from host /etc/selinux/config), "enforced" (always apply SELinux contexts to volume mounts) or "off" (never apply SELinux contexts). |
| csi.snapshotterImage | string | `"registry.k8s.io/sig-storage/csi-snapshotter:v8.3.0"` | csi-snapshotter sidecar image. |
| csi.storageClassCreationDisabled | bool | `false` | Do not create the CSI StorageClass. |

### Timeouts & intervals

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| defaultIntervals | object | `{"deleteEnvoyWithoutS3NeighborTimeout":"5m","deleteUnschedulablePodsAfter":"1m","stuckAdhocPodStartingTimeout":"30m","stuckAdhocPodTimeout":"10m","unschedulableDriveContainerGCTimeout":"2m"}` | Default timeouts and intervals of operator garbage collection: `deleteEnvoyWithoutS3NeighborTimeout` and `deleteUnschedulablePodsAfter`; `unschedulableDriveContainerGCTimeout` is how long a clusterCapacity drive container may stay unscheduled before the operator deletes it so the planner can re-place its capacity on a node that can host it; `stuckAdhocPodTimeout` is how long an adhoc-op container's pod may fail to produce a result before the operator deletes the container, and `stuckAdhocPodStartingTimeout` is the longer variant that applies while the pod is still pulling its image / creating containers. |
| kubeExecTimeout | string | `"5m"` | Timeout of commands the operator executes inside pods. |
| podTerminationDeactivationTimeout | int | `0` | How long a terminating backend pod may sit before the operator forces graceful deactivation. 0 = never (default). On managed cloud nodes (AWS/EKS, OCI/OKE) the operator overrides this to 30m so managed-nodegroup drains don't hang. Per-cluster override: spec.overrides.podTerminationDeactivationTimeout. |
| reconcileTimeout | string | `"30m"` | Timeout of a single reconcile run. |
| waitSinceIoProcessesUpTimeout | int | `0` | How long to wait, once IO processes are reported up, before considering the container's applied image settled. 0 = don't wait (default). Per-object override: spec.overrides.waitSinceIoProcessesUpTimeout on the WekaCluster (backend containers) or on the WekaClient (client containers). |

### Operator runtime & logging

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| deployController | bool | `true` | Deploy the operator controller. When false, the controller Deployment is scaled to 0 replicas. |
| enableLeaderElection | bool | `true` | Enable leader election for the controller manager. Ensures only one controller manager is active. |
| logging.level | int | `0` | Operator log level. |
| logging.timeOnly | bool | `true` | Log only the time (without the date) in log lines. |
| manager.extraVolumeMounts | list | `[]` | Extra volumeMounts for the `manager` container. The reserved path is `/tmp` (on, under, or above it) -- it holds the webhook serving certs. A mount at `/` is rejected for the same reason. |
| manager.extraVolumes | list | `[]` | Extra volumes added to the operator Deployment pod spec (standard PodSpec `volumes` shape). The reserved name is `tmpdir`. |
| manager.labels | object | `{}` | Extra labels added to the operator pod. |
| manager.loggerSettings | object | `{"callerDirLvl":1,"format":"raw","level":0}` | Operator logger settings: verbosity `level`, output `format` and `callerDirLvl` (how many caller directory levels to include in log lines). |
| manager.nodeSelector | object | `{}` | nodeSelector of the operator pod. |
| manager.resources | object | `{"limits":{"cpu":"1000m","memory":"4096Mi"},"requests":{"cpu":"250m","memory":"64Mi"}}` | Resources of the operator manager container. |
| maxWorkers | object | `{"wekaClient":5,"wekaCluster":5,"wekaContainer":50,"wekaManualOperation":5,"wekaPolicy":5}` | Maximum number of workers for each reconciler (for each resource type). |
| prefix | string | `"weka-operator"` | Prefix for all resources created by the operator (for example the controller Deployment and its service accounts). |

### Observability

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| deploymentIdentifier | string | `""` | Identifier attached to the OTEL telemetry emitted by the operator and node agent to tell deployments apart. |
| metrics.clusters | object | `{"enabled":true,"image":"docker.io/library/nginx:1.27.3","pollingRate":"60s"}` | Cluster-level metrics service. `enabled` toggles it, `pollingRate` is the polling interval and `image` is the image of the metrics service pods. Optionally set `nodeSelector` (commented out by default, for example `{weka.io/monitoring: "true"}`) to pin the monitoring service pods; if unset, it inherits the WekaCluster nodeSelector. |
| metrics.containers | object | `{"enabled":true,"pollingRate":"60s","requestsTimeouts":{"getContainerInfo":"10s","register":"3s"}}` | Container-level metrics. `enabled` toggles it, `pollingRate` is the polling interval and `requestsTimeouts` holds the timeouts of the `register` and `getContainerInfo` requests. |
| metrics.podMetrics | object | `{"enabled":true}` | Scrape pod cpu/memory from the Kubernetes metrics.k8s.io API and report it on reconcile spans. Requires metrics-server to be installed in the cluster; set `enabled` to false if it is not, to avoid a per-container "Error getting pod metrics" warning. Only the cpu/memory attributes are affected: reconcile spans still carry the weka-native process and drive counters, and metrics.containers.* is independent. |
| otelExporterOtlpEndpoint | string | `"https://otelcollector.prod.weka.io:4317"` | Custom endpoint to send OTLP traces and logs to (GRPC). If left empty, traces are sent to stdout in json format, and logs use standard Python logging. Optionally, set `otelPackagesInstallerImage` (commented out by default, for example `python:3.10-slim`, chosen for glibc compatibility with the weka-container image) to enable OTEL logging through an init container that installs the OpenTelemetry packages. Leave it unset to disable OTEL logging. |
| podMonitor | object | `{"enabled":true}` | Create PodMonitors for the node-agent (`app: weka-node-agent`) and cluster-monitoring (`app: weka-cluster-monitoring`) pods. Created only when the monitoring.coreos.com/v1 API is available in the cluster. |
| wekahome.allowInsecureTLS | bool | `false` | Allow insecure TLS connection to the wekahome endpoint (no server certificate validation). |
| wekahome.cacertSecret | string | `""` | Name of the secret specifying the CA certificate chain. It is assumed that every target namespace will have such a secret. |
| wekahome.enableStats | bool | `true` | Send performance statistics to Weka Home. Connectivity and event data are sent regardless of this setting. |
| wekahome.endpoint | string | `"https://api.home.weka.io"` | Weka Home API endpoint. |
| wekahome.report.enabled | bool | `true` | Periodically report operator CRs to Weka Home. |
| wekahome.report.interval | string | `"60s"` | Reporting interval. |

### Networking & ports

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| dnsPolicy | object | `{"hostNetwork":"","k8sNetwork":""}` | DNS policy of operator-created pods: `k8sNetwork` for pods without hostNetwork: true, `hostNetwork` for pods with hostNetwork: true. Empty leaves the operator default. |
| healthProbeBindAddress | string | `":8081"` | Bind address of the operator health probe endpoint. |
| managementProxy | object | `{"hostNetwork":false,"ingressBaseDomain":"","ingressClass":""}` | Management proxy (envoy) settings. Applies to every WekaCluster; no per-WekaCluster override. `hostNetwork`: deploy Envoy proxy on host network. `ingressBaseDomain`: base domain for ingress hostname generation (format: namespace--clustername.basedomain); if empty, ingress creation is disabled. `ingressClass`: ingress class of the ingress resource; if empty, the default ingress class of the cluster is used. The tunables below are commented out so the operator's defaults apply: `replicas` (default 2; 0 parks the proxy without uninstalling it; under hostNetwork all replicas bind the same node ports and nothing keeps two of them, or two clusters' proxies which share admin port 9901, off one node, so replicas beyond the number of usable nodes crash-loop; avoid 1 with hostNetwork, since host ports force in-place replacement and the proxy goes down on every config change); `healthyPanicThreshold` (percentage of healthy upstreams below which Envoy load balances to all hosts including unhealthy ones; default 50, Envoy's own default; 0 always honours health checks, so a fully failed backend set returns 503); `adminBindAddress` (address the Envoy admin endpoint binds to; must be bindable by the pod: 0.0.0.0, ::, 127.0.0.1 or ::1, other values crash-loop every replica; defaults to 0.0.0.0, or 127.0.0.1 under hostNetwork, since the admin API is unauthenticated and includes /quitquitquit; a loopback address with hostNetwork false makes kubelet probes degrade to TCP, which cannot detect a wedged Envoy). |
| netnsEnabled | bool | `true` | Bind-mount the host's /run/weka/ephemeral/shared-netns into weka runtime containers with bidirectional mount propagation, so network namespaces created on either side propagate to the other. Set to false only if you need to isolate the weka container from host netns changes. |
| nodeAgentMetricsBindAddress | string | `":8090"` | Bind address of the node-agent metrics endpoint. |
| operatorMetricsBindAddress | string | `"127.0.0.1:8080"` | Bind address of the operator metrics endpoint. |
| portAllocation.startingPort | int | `35000` | Starting port for Weka container port allocation: the base port from which the operator allocates port ranges for Weka clusters. |
| proxy | string | `""` | Proxy configuration for the drivers loader pod and weka home. |

### Platform compatibility

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| gkeCompatibility | object | `{"disableDriverSigning":false,"gkeServiceAccountSecret":"","hugepageConfiguration":{"enabled":false,"hugepageCount":4000,"hugepageSize":"2M"}}` | Google Kubernetes Engine (GKE) settings. `hugepageConfiguration.enabled` automatically configures hugepages on the GKE nodes, and `disableDriverSigning` disables driver signing enforcement on them (WARNING: both reboot nodes forcefully!). |
| ocpCompatibility | object | `{"csi":{"machineConfigLabels":["worker","master"]},"driverToolkitImageBaseUrl":"quay.io/openshift-release-dev/ocp-v4.0-art-dev","driverToolkitSecretName":null,"hugepageConfiguration":{"enabled":false,"hugepageSize":"2M","hugepagesCount":4000,"machineConfigNodeLabel":"worker","nodeSelector":{"matchLabels":[{"node-role.kubernetes.io/worker":""}]}},"retainMachineConfig":true}` | OpenShift (OCP) settings: driver toolkit access, hugepages provisioning through a Tuned profile, and CSI SELinux policy placement. `hugepageConfiguration.enabled` automatically creates a Tuned profile on the matching nodes to apply hugepages (WARNING: do not enable it if you already use custom Tuned profiles for hugepages). `retainMachineConfig`: if true, the machine configurations are not removed when uninstalling the operator, so an operator reinstall does not cause a machine config pool update. `csi.machineConfigLabels`: which machineconfig pools to install the Weka SELinux policy on (by default both workers and control plane nodes). |

### Tolerations

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| manager.tolerations | list | `[]` | Tolerations of the operator pod. When empty, the top-level `tolerations` are used. |
| skipAuxNoScheduleToleration | bool | `false` | Aux pods (drivers, adhoc ops, telemetry) omit auto-generated NoSchedule tolerations. |
| skipClientNoScheduleToleration | bool | `false` | Client pods omit auto-generated NoSchedule tolerations. |
| skipClientsTolerationValidation | bool | `true` | Skip taint/toleration validation for client containers, assuming all nodes tolerate all clients. |
| skipUnhealthyToleration | bool | `false` | Pods omit standard "unhealthy node" tolerations (unschedulable, not-ready, unreachable). |
| tolerations | list | `[]` | Tolerations of the operator pod, used when `manager.tolerations` is empty. |
| tolerationsMismatchSettings | object | `{"enableIgnoredTaints":true,"ignoredTaints":["node.kubernetes.io/unschedulable","node.kubernetes.io/not-ready","node.kubernetes.io/unreachable"]}` | Tolerations-mismatch cleanup tuning (paired with cleanupContainersOnTolerationsMismatch). When `enableIgnoredTaints` is set, the taints listed in `ignoredTaints` are ignored when checking for toleration mismatch. |

### Node agent

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| nodeAgent.affinity | object | `{}` | Affinity of the node-agent DaemonSet. |
| nodeAgent.devicePlugin | object | `{"enabled":false}` | NUMA region device plugin. When `enabled`, each node's NUMA regions are advertised as weka.io/numa-region-<N> extended resources via a kubelet device plugin. Requires access to the host's kubelet device-plugins directory. Off by default. |
| nodeAgent.kubeletPath | string | `"/var/lib/kubelet"` | Kubelet path, for clusters where Kubernetes is not installed in the default folder. Used to locate the host's kubelet device-plugins directory for the NUMA region device plugin. |
| nodeAgent.nodeSelector | object | `{}` | nodeSelector of the node-agent DaemonSet. |
| nodeAgent.persistencePaths | string | `"/opt/k8s-weka"` | Host path used for node-agent persistence. The default suits generic OSes; override with /root/k8s-weka for OpenShift, or /mnt/stateful_partition/k8s-weka for Google Container-Optimized OS. |
| nodeAgent.resources | object | `{"limits":{"cpu":"1000m","ephemeral-storage":"400Mi","memory":"1024Mi"},"requests":{"cpu":"50m","ephemeral-storage":"100Mi","memory":"64Mi"}}` | Node-agent DaemonSet container resources. On VM nodes using the native containerd snapshotter (e.g. KubeVirt guests without overlayfs support), the entire container image is copied into the writable snapshot and counted as ephemeral storage. On such nodes rootfs usage equals the image size (~300Mi for weka-operator), regardless of any actual writes by the process. Increase `ephemeral-storage` if node-agent pods are evicted on such nodes. |
| nodeAgent.tolerations | list | `[{"operator":"Exists"}]` | Tolerations of the node-agent DaemonSet. |

### Upgrade

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| upgrade.computeThresholdPercent | int | `90` | Minimum percentage of the cluster's compute containers that must be active before the upgrade proceeds. |
| upgrade.driveThresholdPercent | int | `90` | Minimum percentage of the cluster's drive containers that must be active before the upgrade proceeds. |
| upgrade.imagePrePull | object | `{"enabled":true,"timeout":"20m"}` | Image pre-pull before an upgrade. When `enabled`, images are pre-pulled on the nodes before the upgrade, and `timeout` is the maximum time to wait for all nodes to pull the image. |
| upgrade.maxDeactivatingContainersPercent | int | `10` | Rolling upgrade aborts when more than this percentage of containers are already marked for deletion. |
