package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/klog/v2"
	"k8s.io/utils/env"
)

type BindAddress struct {
	Metrics     string
	HealthProbe string
	NodeAgent   string
}

type CleanupRemovedNodesMode string

const (
	CleanupRemovedNodesOff  CleanupRemovedNodesMode = "false"
	CleanupRemovedNodesOn   CleanupRemovedNodesMode = "true"
	CleanupRemovedNodesAuto CleanupRemovedNodesMode = "auto"
)

// CleansOnNodeRemoval reports whether the mode intends eventual cleanup of a
// removed node's backend container (immediate for On, after a grace period for Auto).
func (m CleanupRemovedNodesMode) CleansOnNodeRemoval() bool {
	return m == CleanupRemovedNodesOn || m == CleanupRemovedNodesAuto
}

type Timeouts struct {
	ReconcileTimeout                  time.Duration // Reconcile timeout
	KubeExecTimeout                   time.Duration // Kubernetes ssh commands executor timeout
	PodTerminationDeactivationTimeout time.Duration // Default timeout for pod termination deactivation
	// WaitSinceIoProcessesUpTimeout is how long to wait, once IO processes are reported up, before
	// considering the container's applied image settled. 0 (default): don't wait.
	WaitSinceIoProcessesUpTimeout time.Duration
}

// OpenTelemetry settings
type Otel struct {
	DeploymentIdentifier         string
	ExporterOtlpEndpoint         string
	PythonPackagesInstallerImage string
}

type WekaHomeReporter struct {
	Enabled            bool
	Interval           time.Duration
	IdentitySecretName string
}

type WekaHome struct {
	Endpoint      string
	AllowInsecure bool
	CacertSecret  string
	EnableStats   bool
	Reporter      WekaHomeReporter
}

type OcpCompatibility struct {
	DriverToolkitSecretName   string
	DriverToolkitImageBaseUrl string
}

type GkeCompatibility struct {
	DisableDriverSigning  bool
	HugepageConfiguration struct {
		Enabled bool
		Size    string
		Count   int
	}
	ServiceAccountSecret string
}

type Logging struct {
	Level    int
	TimeOnly bool
}

type MaxWorkers struct {
	WekaCluster         int
	WekaContainer       int
	WekaClient          int
	WekaManualOperation int
	WekaPolicy          int
}

type OperatorMode string

const (
	OperatorModeManager   OperatorMode = "manager"
	OperatorModeNodeAgent OperatorMode = "node-agent"
)

type MetricsServerEnv struct {
	NodeName string
}

type DNSPolicy struct {
	K8sNetwork  string
	HostNetwork string
}

type TolerationsMismatchSettings struct {
	EnableIgnoredTaints bool
	IgnoredTaints       []string
}

type ResourceRequirements struct {
	Limits   ResourceList `json:"limits,omitempty"`
	Requests ResourceList `json:"requests,omitempty"`
}

type ResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type CsiControllerResources struct {
	Wekafs         ResourceRequirements `json:"wekafs,omitempty"`
	CsiAttacher    ResourceRequirements `json:"csiAttacher,omitempty"`
	CsiProvisioner ResourceRequirements `json:"csiProvisioner,omitempty"`
	CsiResizer     ResourceRequirements `json:"csiResizer,omitempty"`
	CsiSnapshotter ResourceRequirements `json:"csiSnapshotter,omitempty"`
}

type CsiNodeResources struct {
	Wekafs        ResourceRequirements `json:"wekafs,omitempty"`
	LivenessProbe ResourceRequirements `json:"livenessProbe,omitempty"`
	CsiRegistrar  ResourceRequirements `json:"csiRegistrar,omitempty"`
}

// NodeAgentDevicePluginConfig configures the kubelet device plugin run by the node agent
// that advertises each NUMA region on the node as an extended resource
// (weka.io/numa-region-<N>). Disabled by default: node-agent must not expose the device
// plugin unless explicitly enabled.
type NodeAgentDevicePluginConfig struct {
	Enabled     bool
	KubeletPath string
}

type EmbeddedCsiSettings struct {
	Enabled                                       bool
	StorageClassCreationDisabled                  bool
	WekafsImage                                   string
	ProvisionerImage                              string
	AttacherImage                                 string
	LivenessProbeImage                            string
	ResizerImage                                  string
	SnapshotterImage                              string
	RegistrarImage                                string
	PreventNewWorkloadOnClientContainerNotRunning bool
	LogLevel                                      int
	ControllerResources                           CsiControllerResources
	NodeResources                                 CsiNodeResources
	SelinuxSupport                                string // "auto", "enforced", or "off"
	KubeletPath                                   string
	HostNetwork                                   bool
	AllowMountOptionOverrides                     bool
}

type PriorityClasses struct {
	Initial  string
	Targeted string
}

type NfsConfig struct {
	MountdPort      int
	LockmanagerPort int
	NotifyPort      int
}

type HugepagesUpdateConfig struct {
	Compute bool
	Drive   bool
}

type SmbwConfig struct {
	ShmSize string
}

type ClusterCapacityConfig struct {
	MaxComputeCoresPerNode int
	TlcCapacityPerCoreGiB  int
	QlcCapacityPerCoreGiB  int
	// ImbalanceFactor gates the heterogeneous "balanced fresh" growth fallback: when each new drive
	// container would be at least this factor (8.0 = 8.0x) of the existing containers' average
	// capacity, the planner lays out a fresh balanced set instead. 0 falls back to the default (8.0).
	ImbalanceFactor float64
	// CapacityDeadbandFraction is the relative shortfall (desired-current)/desired below which a pool
	// growth is ignored, to avoid re-planning/thrashing on trivial clusterCapacity bumps. 0 disables
	// the deadband (exact-match: any positive delta grows).
	CapacityDeadbandFraction float64
}

type DriveSharingConfig struct {
	DriveTypesRatio                      v1alpha1.DriveTypesRatio
	MaxVirtualDrivesPerCore              int
	EnforceMinDrivesPerTypePerCore       bool
	EnableDynamicDriveScaling            bool
	MinGrowthFraction                    float64
	MaxOverProvisionFraction             float64
	SsdProxyHugepagesOffsetMiB           int
	SsdProxyImageOverride                string
	HugepagesTlcRatio                    int
	HugepagesQlcRatio                    int
	SmallBigDiskSizesMaxProportionFactor int
	// AllowSingleParity lowers the clusterCapacity protection floor from the production
	// 3+2+0 (stripeWidth/data>=3, redundancyLevel/parity>=2, hotSpare>=0 / optional) to
	// single-parity 2+1+0, enabling QA/test clusters such as 2+1. QA/test only: a single
	// parity chunk leaves a stripe unprotected during rebuild. When set, the operator also
	// emits the allow_1_parity weka override at cluster formation (weka rejects parity=1
	// without it).
	AllowSingleParity bool
	// DefaultStripeWidth, DefaultRedundancyLevel, DefaultHotSpare are Helm-level protection
	// defaults applied only when the WekaCluster CR leaves the corresponding field at 0.
	// A non-zero per-cluster spec value takes precedence; a spec value of 0 is treated as
	// "unset" and falls back to the default. Consequence: a cluster cannot force hotSpare=0
	// while a non-zero DefaultHotSpare is configured (0 always resolves to the default).
	DefaultStripeWidth     int
	DefaultRedundancyLevel int
	DefaultHotSpare        int
}

// EffectiveProtection returns the protection values to apply, using the per-cluster
// spec value when set (!=0) and falling back to the Helm-level default otherwise.
func (c *DriveSharingConfig) EffectiveProtection(specSW, specRL, specHS int) (sw, rl, hs int) {
	sw, rl, hs = specSW, specRL, specHS
	if sw == 0 {
		sw = c.DefaultStripeWidth
	}
	if rl == 0 {
		rl = c.DefaultRedundancyLevel
	}
	if hs == 0 {
		hs = c.DefaultHotSpare
	}
	return
}

type PortAllocationConfig struct {
	StartingPort int
}

// WebhookConfig holds deployment-level webhook settings (cert paths, VWC
// naming). Policy semantics live in AdmissionControl / AdmissionPolicies.
type WebhookConfig struct {
	CertDir     string
	SecretName  string
	ServiceName string
	WebhookName string
}

// AdmissionControlConfig is the master switch. Enabled=false deletes the
// VWC at startup and skips webhook setup entirely.
type AdmissionControlConfig struct {
	Enabled bool
}

// AdmissionPoliciesConfig carries the per-request policy posture. Mode
// picks the strict/relaxed column from each policy's defaults; Overrides
// pin individual policies regardless of Mode. Override values are
// lowercased at load and must be "default", "warn", or "error".
type AdmissionPoliciesConfig struct {
	Mode      string
	Overrides map[string]string
}

type BuilderImagesConfig struct {
	Default  string
	Ubuntu24 string
}

func (t *TolerationsMismatchSettings) GetIgnoredTaints() []string {
	if t == nil || !t.EnableIgnoredTaints {
		return nil
	}
	return t.IgnoredTaints
}

var Config struct {
	Version                        string
	OperatorPodUID                 string
	OperatorPodName                string
	OperatorPodNamespace           string
	OperatorDeploymentName         string
	OperatorImage                  string
	BindAddress                    BindAddress
	EnableLeaderElection           bool
	EnableClusterApi               bool
	Timeouts                       Timeouts
	Otel                           Otel
	WekaHome                       WekaHome
	DebugSleep                     int
	MaintenanceSaName              string
	OperatorServiceAccountName     string
	MaintenanceImage               string
	EnvoyImage                     string
	MaintenanceImagePullSecret     string
	OcpCompatibility               OcpCompatibility
	GkeCompatibility               GkeCompatibility
	WekaAllocZombieDeleteAfter     time.Duration
	DevMode                        bool
	Logging                        Logging
	MaxWorkers                     MaxWorkers
	Metrics                        Metrics
	Mode                           OperatorMode
	LocalDataPvc                   string
	DNSPolicy                      DNSPolicy
	SignDrivesImage                string
	TaskmonDefaultImage            string
	FullPcpusOnly                  bool
	SkipUnhealthyToleration        bool
	SkipClientNoScheduleToleration bool
	SkipAuxNoScheduleToleration    bool
	// SkipAwsTerminationLifecycleHook disables all operator management of the AWS ASG
	// EC2_INSTANCE_TERMINATING lifecycle hook: the operator neither resolves a backend node's ASG nor
	// creates the hook, and never holds or releases an instance through it. Escape hatch for
	// environments where the operator has no autoscaling IAM authority or hooks are managed out of
	// band. Scale-down drive-drain protection is unavailable while set.
	SkipAwsTerminationLifecycleHook bool
	MetricsServerEnv                MetricsServerEnv
	NodeAgentDevicePlugin           NodeAgentDevicePluginConfig
	Upgrade                         struct {
		ComputeThresholdPercent          int
		DriveThresholdPercent            int
		MaxDeactivatingContainersPercent int
		ImagePrePullEnabled              bool
		ImagePrePullTimeout              time.Duration
	}
	CleanupRemovedNodes                          CleanupRemovedNodesMode
	CleanupBackendsOnNodeSelectorMismatch        bool
	CleanupClientsOnNodeSelectorMismatch         bool
	CleanupContainersOnTolerationsMismatch       bool
	EvictContainerOnDeletion                     bool
	RemovalThrottlingEnabled                     bool
	RecreateUnhealthyEnvoyThrottlingEnabled      bool
	SkipClientsTolerationValidation              bool
	TolerationsMismatchSettings                  TolerationsMismatchSettings
	DeleteEnvoyWithoutS3NeighborTimeout          time.Duration
	DeleteTelemetryWithoutComputeNeighborTimeout time.Duration
	DeleteUnschedulablePodsAfter                 time.Duration
	// How long a clusterCapacity drive container may stay unscheduled before the operator deletes it
	// so the planner can re-place its capacity on a node that can host it.
	UnschedulableDriveContainerGCTimeout time.Duration
	// How long an adhoc-op container's pod may fail to produce a result before the
	// operator deletes the container, so a pod that can never run cannot leak the CR
	// forever. StuckAdhocPodStartingTimeout applies while the pod is still legitimately
	// starting up (image pull / container creation), which can take much longer than a
	// hard failure like ImagePullBackOff or Unschedulable.
	StuckAdhocPodTimeout             time.Duration
	StuckAdhocPodStartingTimeout     time.Duration
	RemoveFailedDrivesFromWeka       bool
	AllowMultipleProtocolsPerNode    bool
	NetnsEnabled                     bool
	ManagementProxyHostNetwork       bool
	ManagementProxyIngressBaseDomain string
	ManagementProxyIngressClass      string
	EvictedPodCleanupEnabled         bool
	EvictedPodCleanupInterval        time.Duration
	// Management proxy tunables, shared by every WekaCluster this operator manages. Documented in
	// the chart's managementProxy values.
	ManagementProxyReplicas              int32
	ManagementProxyHealthyPanicThreshold int32
	ManagementProxyAdminBindAddress      string

	BuilderImages          BuilderImagesConfig
	Csi                    EmbeddedCsiSettings
	SyslogPackage          string
	Proxy                  string
	PriorityClasses        PriorityClasses
	Nfs                    NfsConfig
	Smbw                   SmbwConfig
	ClusterCapacity        ClusterCapacityConfig
	DriveSharing           DriveSharingConfig
	PortAllocation         PortAllocationConfig
	HugepagesUpdate        HugepagesUpdateConfig
	ComputeMaxHugepagesMiB int

	PodConfigVersion                     string
	EnablePodConfigCodeVersionRotation   bool
	AllowRotateNonAnnotatedPodConfigHash bool

	Webhook           WebhookConfig
	AdmissionControl  AdmissionControlConfig
	AdmissionPolicies AdmissionPoliciesConfig
}

type NodeAgentRequestsTimeouts struct {
	Register         time.Duration
	GetContainerInfo time.Duration
}

type Metrics struct {
	Clusters struct {
		Enabled      bool
		PollingRate  time.Duration
		Image        string
		NodeSelector map[string]string
	}
	Containers struct {
		Enabled          bool
		PollingRate      time.Duration
		RequestsTimeouts NodeAgentRequestsTimeouts
	}
	// PodMetrics scrapes pod cpu/memory from the metrics.k8s.io API and reports it on
	// reconcile spans. Requires metrics-server, turn off on clusters that don't run it.
	PodMetrics struct {
		Enabled bool
	}
	NodeAgentSecretName string
}

var Consts struct {
	DevModeNamespace string
	// sleep between container self-update allocations attempts
	ContainerUpdateAllocationsSleep time.Duration
	// TTL for join ips cache
	JoinIpsCacheTTL time.Duration
	// Limit for the number of containers to be created during one reconcile loop
	NewContainersLimit int
	// Interval for periodic drives check on weka container
	PeriodicDrivesCheckInterval time.Duration
	// Interval for checking drivers on distribution wekacontainer
	CheckDriversInterval time.Duration
	// Min compute containers to be UP before forming a weka cluster
	FormClusterMinComputeContainers int
	// Min drive containers to be UP before forming a weka cluster
	FormClusterMinDriveContainers int
	// Max compute containers to be UP before forming a weka cluster
	FormClusterMaxComputeContainers int
	// Max drive containers to be UP before forming a weka cluster
	FormClusterMaxDriveContainers int
	// Max containers number that will be part of initial s3 cluster
	FormS3ClusterMaxContainerCount int
	// Min containers number required to form an SMB-W cluster
	FormSmbwClusterMinContainerCount int
	// Max containers number that will be part of initial SMB-W cluster
	FormSmbwClusterMaxContainerCount int
	// Interval at which CSI secret with container ips will be updated
	CsiLoginCredentialsUpdateInterval time.Duration
	// Filesystem name for CSI storage class
	CsiFileSystemName string
	// Legacy driver name for CSI, used when can't determine the driver name from config
	CsiLegacyDriverName string
	// Max containers to delete at once on node selector mismatch
	MaxContainersDeletedOnSelectorMismatch int
	// Interval for cleanup of containers on node selector mismatch
	SelectorMismatchCleanupInterval time.Duration
	// Interval for cleanup of containers on tolerations mismatch
	TolerationsMismatchCleanupInterval time.Duration
	// Management service update interval
	ManagementServiceUpdateInterval time.Duration
	// Interval for telemetry exports configuration updates
	TelemetryUpdateInterval time.Duration
	// Interval for weka debug override reconciliation
	WekaOverridesUpdateInterval time.Duration
	// DPDK memory reserved for SSD proxy containers (MiB).
	// Excluded from weka's --memory without SsdProxyIncludesDpdkMemory FF; included with it.
	SsdProxyDpdkMemoryMiB int
}

func init() {
	Consts.DevModeNamespace = "weka-operator-system"
	Consts.ContainerUpdateAllocationsSleep = 10 * time.Second
	Consts.JoinIpsCacheTTL = 1 * time.Minute
	Consts.NewContainersLimit = 1000 // virtually no limit for now
	Consts.PeriodicDrivesCheckInterval = 1 * time.Minute
	Consts.CheckDriversInterval = 7 * time.Minute
	// Default minimum drive/compute containers required to form a cluster. The 5-container default
	// suits production 3+2+1 (minFdNum=6); a single-parity 2+1 cluster (minFdNum=3) legitimately
	// forms with as few as 3, so AllowSingleParity lowers the default. Both remain env-overridable.
	formClusterMinDefault := 5
	if getBoolEnvOrDefault("ALLOW_SINGLE_PARITY", false) {
		formClusterMinDefault = 3
	}
	Consts.FormClusterMinComputeContainers = getIntEnvOrDefault("FORM_CLUSTER_MIN_COMPUTE_CONTAINERS", formClusterMinDefault)
	Consts.FormClusterMinDriveContainers = getIntEnvOrDefault("FORM_CLUSTER_MIN_DRIVE_CONTAINERS", formClusterMinDefault)
	Consts.FormClusterMaxComputeContainers = 10
	Consts.FormClusterMaxDriveContainers = 10
	Consts.FormS3ClusterMaxContainerCount = 3
	Consts.FormSmbwClusterMinContainerCount = 3
	Consts.FormSmbwClusterMaxContainerCount = 8
	Consts.CsiLoginCredentialsUpdateInterval = 1 * time.Minute
	Consts.CsiFileSystemName = "default"
	Consts.CsiLegacyDriverName = "csi.weka.io"
	Consts.MaxContainersDeletedOnSelectorMismatch = 4
	Consts.SelectorMismatchCleanupInterval = 2 * time.Minute
	Consts.TolerationsMismatchCleanupInterval = 1 * time.Minute
	Consts.ManagementServiceUpdateInterval = 1 * time.Minute
	Consts.TelemetryUpdateInterval = 1 * time.Minute
	Consts.WekaOverridesUpdateInterval = 1 * time.Minute
	Consts.SsdProxyDpdkMemoryMiB = 2048
}

// LoadCapacityEnv populates the drive-sharing, cluster-capacity and compute-hugepages configuration
// from environment variables, with the built-in defaults. It is the single source of these defaults,
// shared by ConfigureEnv (the operator) and standalone callers such as the weka-capacity dry-run CLI,
// which need the capacity constraints without the full operator env (VERSION, bind addresses, ...).
func LoadCapacityEnv() {
	// Drive sharing configuration
	Config.DriveSharing.DriveTypesRatio.Tlc = getIntEnvOrDefault("DRIVE_TYPES_RATIO_TLC", 1)
	Config.DriveSharing.DriveTypesRatio.Qlc = getIntEnvOrDefault("DRIVE_TYPES_RATIO_QLC", 0)
	Config.DriveSharing.MaxVirtualDrivesPerCore = getIntEnvOrDefault("MAX_VIRTUAL_DRIVES_PER_CORE", 8)
	Config.DriveSharing.EnforceMinDrivesPerTypePerCore = getBoolEnvOrDefault("ENFORCE_MIN_DRIVES_PER_TYPE_PER_CORE", true)
	Config.DriveSharing.EnableDynamicDriveScaling = getBoolEnvOrDefault("ENABLE_DYNAMIC_DRIVE_SCALING_FOR_SHARED_DRIVES", false)
	Config.DriveSharing.MinGrowthFraction = getFloatEnvOrDefault("MIN_GROWTH_FRACTION", 0.2)
	Config.DriveSharing.MaxOverProvisionFraction = getFloatEnvOrDefault("MAX_OVER_PROVISION_FRACTION", 0.2)
	Config.DriveSharing.SsdProxyHugepagesOffsetMiB = getIntEnvOrDefault("SSD_PROXY_HUGEPAGES_OFFSET_MIB", 200)
	Config.DriveSharing.SsdProxyImageOverride = getEnvOrDefault("SSD_PROXY_IMAGE_OVERRIDE", "")
	Config.DriveSharing.HugepagesTlcRatio = getIntEnvOrDefault("HUGEPAGES_TLC_RATIO", 1000)
	Config.DriveSharing.HugepagesQlcRatio = getIntEnvOrDefault("HUGEPAGES_QLC_RATIO", 6000)
	Config.DriveSharing.SmallBigDiskSizesMaxProportionFactor = getIntEnvOrDefault("SMALL_BIG_DISK_SIZES_MAX_PROPORTION_FACTOR", 10)
	Config.DriveSharing.AllowSingleParity = getBoolEnvOrDefault("ALLOW_SINGLE_PARITY", false)
	Config.DriveSharing.DefaultStripeWidth = getIntEnvOrDefault("PROTECTION_STRIPE_WIDTH", 0)
	Config.DriveSharing.DefaultRedundancyLevel = getIntEnvOrDefault("PROTECTION_REDUNDANCY_LEVEL", 0)
	Config.DriveSharing.DefaultHotSpare = getIntEnvOrDefault("PROTECTION_HOT_SPARE", 0)

	// Cluster capacity configuration
	Config.ClusterCapacity.MaxComputeCoresPerNode = getIntEnvOrDefault("CLUSTER_CAPACITY_MAX_COMPUTE_CORES_PER_NODE", 16)
	Config.ClusterCapacity.TlcCapacityPerCoreGiB = getIntEnvOrDefault("CLUSTER_CAPACITY_TLC_CAPACITY_PER_CORE_GIB", 5*1024)
	Config.ClusterCapacity.QlcCapacityPerCoreGiB = getIntEnvOrDefault("CLUSTER_CAPACITY_QLC_CAPACITY_PER_CORE_GIB", 50*1024)
	Config.ClusterCapacity.ImbalanceFactor = getFloatEnvOrDefault("CLUSTER_CAPACITY_IMBALANCE_FACTOR", 8.0)
	Config.ClusterCapacity.CapacityDeadbandFraction = getFloatEnvOrDefault("CLUSTER_CAPACITY_DEADBAND_FRACTION", 0.05)

	// Compute hugepages cap
	Config.ComputeMaxHugepagesMiB = getIntEnvOrDefault("COMPUTE_MAX_HUGEPAGES_MIB", 360000)
}

func ConfigureEnv(ctx context.Context) {
	Config.Version = getEnvOrFail("VERSION")
	Config.Mode = OperatorMode(env.GetString("OPERATOR_MODE", string(OperatorModeManager)))
	Config.OperatorPodUID = os.Getenv("POD_UID")
	Config.OperatorPodName = os.Getenv("POD_NAME")
	Config.OperatorPodNamespace = os.Getenv("POD_NAMESPACE")
	Config.OperatorDeploymentName = os.Getenv("OPERATOR_DEPLOYMENT_NAME")
	Config.OperatorImage = os.Getenv("OPERATOR_IMAGE")
	if Config.Mode == OperatorModeManager {
		Config.BindAddress.Metrics = getEnvOrFail("OPERATOR_METRICS_BIND_ADDRESS")
		Config.BindAddress.HealthProbe = getEnvOrFail("HEALTH_PROBE_BIND_ADDRESS")
		Config.MaintenanceSaName = getEnvOrFail("WEKA_OPERATOR_MAINTENANCE_SA_NAME")
		Config.OperatorServiceAccountName = getEnvOrFail("WEKA_OPERATOR_SERVICE_ACCOUNT_NAME")
		Config.OcpCompatibility.DriverToolkitSecretName = getEnvOrFail("WEKA_OCP_PULL_SECRET")
	}
	Config.BindAddress.NodeAgent = getEnvOrDefault("NODE_AGENT_BIND_ADDRESS", ":8090")
	Config.EnableLeaderElection = getBoolEnvOrDefault("ENABLE_LEADER_ELECTION", false)
	Config.EnableClusterApi = getBoolEnvOrDefault("ENABLE_CLUSTER_API", false)
	Config.Timeouts.KubeExecTimeout = getDurationEnvOrDefault("KUBE_EXEC_TIMEOUT", 5*time.Minute)
	// Default 0 = never auto-deactivate a terminating backend pod. On managed cloud nodes (AWS/EKS,
	// OCI/OKE) the operator overrides this to 30m (see resolveDeactivationTimeout) so managed-nodegroup
	// drains don't hang.
	Config.Timeouts.PodTerminationDeactivationTimeout = getDurationEnvOrDefault("POD_TERMINATION_DEACTIVATION_TIMEOUT", 0)
	Config.Timeouts.WaitSinceIoProcessesUpTimeout = getDurationEnvOrDefault("WAIT_SINCE_IO_PROCESSES_UP_TIMEOUT", 0)
	Config.Timeouts.ReconcileTimeout = getDurationEnvOrDefault("RECONCILE_TIMEOUT", 30*time.Minute)
	Config.Otel.DeploymentIdentifier = os.Getenv("OTEL_DEPLOYMENT_IDENTIFIER")
	Config.Otel.ExporterOtlpEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	// Weka Home configuration
	Config.WekaHome.Endpoint = getEnvOrDefault("WEKA_OPERATOR_WEKA_HOME_ENDPOINT", "https://api.home.weka.io")
	Config.WekaHome.AllowInsecure = getBoolEnvOrDefault("WEKA_OPERATOR_WEKA_HOME_INSECURE", false)
	Config.WekaHome.CacertSecret = os.Getenv("WEKA_OPERATOR_WEKA_HOME_CACERT_SECRET")
	Config.WekaHome.EnableStats = getBoolEnvOrDefault("WEKA_OPERATOR_WEKA_HOME_ENABLE_STATS", true)
	Config.WekaHome.Reporter.Enabled = getBoolEnvOrDefault("WEKA_OPERATOR_REPORTER_ENABLED", true)
	Config.WekaHome.Reporter.Interval = getDurationEnvOrDefault("WEKA_OPERATOR_REPORTER_INTERVAL", 60*time.Second)
	Config.WekaHome.Reporter.IdentitySecretName = getEnvOrDefault("WEKA_OPERATOR_REPORTER_IDENTITY_SECRET", "weka-operator-wekahome-identity")
	Config.DebugSleep = getIntEnvOrDefault("WEKA_OPERATOR_DEBUG_SLEEP", 3)
	Config.MaintenanceImage = getEnvOrDefault("WEKA_MAINTENANCE_IMAGE", "quay.io/weka.io/busybox:1.37.0")
	Config.EnvoyImage = getEnvOrDefault("ENVOY_IMAGE", "docker.io/envoyproxy/envoy:v1.31-latest")
	Config.Upgrade.ComputeThresholdPercent = getIntEnvOrDefault("UPGRADE_COMPUTE_THRESHOLD_PERCENT", 90)
	Config.Upgrade.DriveThresholdPercent = getIntEnvOrDefault("UPGRADE_DRIVE_THRESHOLD_PERCENT", 90)
	Config.Upgrade.MaxDeactivatingContainersPercent = getIntEnvOrDefault("UPGRADE_MAX_DEACTIVATING_CONTAINERS_PERCENT", 10)
	Config.Upgrade.ImagePrePullEnabled = getBoolEnvOrDefault("UPGRADE_IMAGE_PRE_PULL_ENABLED", true)
	Config.Upgrade.ImagePrePullTimeout = getDurationEnvOrDefault("UPGRADE_IMAGE_PRE_PULL_TIMEOUT", 20*time.Minute)
	Config.MaintenanceImagePullSecret = os.Getenv("WEKA_MAINTENANCE_IMAGE_PULL_SECRET")
	Config.Otel.PythonPackagesInstallerImage = os.Getenv("WEKA_OTEL_PACKAGES_INSTALLER_IMAGE") // No default - opt-in only
	Config.OcpCompatibility.DriverToolkitImageBaseUrl = getEnvOrDefault("WEKA_OCP_TOOLKIT_IMAGE_BASE_URL", "quay.io/openshift-release-dev/ocp-v4.0-art-dev")
	Config.GkeCompatibility.DisableDriverSigning = getBoolEnvOrDefault("WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING", false)
	Config.GkeCompatibility.HugepageConfiguration.Enabled = getBoolEnvOrDefault("WEKA_COS_ALLOW_HUGEPAGE_CONFIG", false)
	Config.GkeCompatibility.HugepageConfiguration.Size = getEnvOrDefault("WEKA_COS_GLOBAL_HUGEPAGE_SIZE", "2m")
	Config.GkeCompatibility.HugepageConfiguration.Count = getIntEnvOrDefault("WEKA_COS_GLOBAL_HUGEPAGE_COUNT", 4000)
	Config.GkeCompatibility.ServiceAccountSecret = os.Getenv("WEKA_COS_SERVICE_ACCOUNT_SECRET")
	Config.WekaAllocZombieDeleteAfter = getDurationEnvOrDefault("WEKA_ALLOC_ZOMBIE_DELETE_AFTER", 5*time.Minute)
	Config.DevMode = getBoolEnvOrDefault("OPERATOR_DEV_MODE", false)
	// logging configuration
	Config.Logging.Level = getIntEnvOrDefault("LOG_LEVEL", 0)
	Config.Logging.TimeOnly = getBoolEnvOrDefault("LOG_TIME_ONLY", true)
	// max workers configuration
	Config.MaxWorkers.WekaCluster = getIntEnvOrDefault("MAX_WORKERS_WEKACLUSTER", 1)
	Config.MaxWorkers.WekaContainer = getIntEnvOrDefault("MAX_WORKERS_WEKACONTAINER", 10)
	Config.MaxWorkers.WekaClient = getIntEnvOrDefault("MAX_WORKERS_WEKACLIENT", 10)
	Config.MaxWorkers.WekaManualOperation = getIntEnvOrDefault("MAX_WORKERS_WEKAMANUALOPERATION", 1)
	Config.MaxWorkers.WekaPolicy = getIntEnvOrDefault("MAX_WORKERS_WEKAPOLICY", 1)

	Config.Metrics.Clusters.Enabled = getBoolEnvOrDefault("METRICS_CLUSTERS_ENABLED", true)
	Config.Metrics.Clusters.PollingRate = getDurationEnvOrDefault("METRICS_CLUSTERS_POLLING_RATE", time.Second*60)
	Config.Metrics.Clusters.Image = env.GetString("METRICS_CLUSTERS_IMAGE", "docker.io/library/nginx:1.27.3")
	Config.Metrics.Clusters.NodeSelector = getMapEnvOrDefault("METRICS_CLUSTERS_NODE_SELECTOR", nil)
	Config.Metrics.Containers.Enabled = getBoolEnvOrDefault("METRICS_CONTAINERS_ENABLED", true)
	Config.Metrics.Containers.PollingRate = getDurationEnvOrDefault("METRICS_CONTAINERS_POLLING_RATE", time.Second*60)
	Config.Metrics.Containers.RequestsTimeouts.Register = getDurationEnvOrDefault("METRICS_CONTAINERS_REQUEST_TIMEOUT_REGISTER", time.Second*3)
	Config.Metrics.Containers.RequestsTimeouts.GetContainerInfo = getDurationEnvOrDefault("METRICS_CONTAINERS_REQUEST_TIMEOUT_GET_CONTAINER_INFO", time.Second*10)
	Config.Metrics.PodMetrics.Enabled = getBoolEnvOrDefault("METRICS_POD_METRICS_ENABLED", true)
	Config.Metrics.NodeAgentSecretName = env.GetString("METRICS_NODE_AGENT_TOKEN", "weka-node-agent-secret")
	Config.LocalDataPvc = env.GetString("LOCAL_DATA_PVC", "")
	Config.DNSPolicy.K8sNetwork = env.GetString("DNS_POLICY_K8S_NETWORK", "")
	Config.DNSPolicy.HostNetwork = env.GetString("DNS_POLICY_HOST_NETWORK", "")
	Config.SignDrivesImage = env.GetString("SIGN_DRIVES_IMAGE", "")
	Config.TaskmonDefaultImage = env.GetString("TASKMON_DEFAULT_IMAGE", "")
	Config.FullPcpusOnly = getBoolEnvOrDefault("FULL_PCPUS_ONLY", false)
	Config.SkipUnhealthyToleration = getBoolEnvOrDefault("SKIP_UNHEALTHY_TOLERATION", false)
	Config.SkipClientNoScheduleToleration = getBoolEnvOrDefault("SKIP_CLIENT_NO_SCHEDULE_TOLERATION", false)
	Config.SkipAuxNoScheduleToleration = getBoolEnvOrDefault("SKIP_AUX_NO_SCHEDULE_TOLERATION", false)
	Config.SkipAwsTerminationLifecycleHook = getBoolEnvOrDefault("SKIP_AWS_TERMINATION_LIFECYCLE_HOOK", false)
	Config.CleanupRemovedNodes = getCleanupRemovedNodesMode()
	Config.CleanupBackendsOnNodeSelectorMismatch = getBoolEnvOrDefault("CLEANUP_BACKENDS_ON_NODE_SELECTOR_MISMATCH", false)
	Config.CleanupClientsOnNodeSelectorMismatch = getBoolEnvOrDefault("CLEANUP_CLIENTS_ON_NODE_SELECTOR_MISMATCH", false)
	Config.CleanupContainersOnTolerationsMismatch = getBoolEnvOrDefault("CLEANUP_CONTAINERS_ON_TOLERATIONS_MISMATCH", false)
	Config.EvictContainerOnDeletion = getBoolEnvOrDefault("EVICT_CONTAINER_ON_DELETION", false)
	Config.RemovalThrottlingEnabled = getBoolEnvOrDefault("REMOVAL_THROTTLING_ENABLED", false)
	Config.RecreateUnhealthyEnvoyThrottlingEnabled = getBoolEnvOrDefault("RECREATE_UNHEALTHY_ENVOY_THROTTLING_ENABLED", true)
	Config.SkipClientsTolerationValidation = getBoolEnvOrDefault("SKIP_CLIENTS_TOLERATION_VALIDATION", false)
	Config.TolerationsMismatchSettings.EnableIgnoredTaints = getBoolEnvOrDefault("TOLERATIONS_MISMATCH_SETTINGS_ENABLE_IGNORED_TAINTS", true)
	Config.TolerationsMismatchSettings.IgnoredTaints = getStringSlice("TOLERATIONS_MISMATCH_SETTINGS_IGNORED_TAINTS")
	Config.DeleteEnvoyWithoutS3NeighborTimeout = getDurationEnvOrDefault("DELETE_ENVOY_WITHOUT_S3_NEIGHBOR_TIMEOUT", 5*time.Minute)
	Config.DeleteTelemetryWithoutComputeNeighborTimeout = getDurationEnvOrDefault("DELETE_TELEMETRY_WITHOUT_COMPUTE_NEIGHBOR_TIMEOUT", 5*time.Minute)
	Config.DeleteUnschedulablePodsAfter = getDurationEnvOrDefault("DELETE_UNSCHEDULABLE_PODS_AFTER", 1*time.Minute)
	Config.UnschedulableDriveContainerGCTimeout = getDurationEnvOrDefault("UNSCHEDULABLE_DRIVE_CONTAINER_GC_TIMEOUT", 2*time.Minute)
	Config.StuckAdhocPodTimeout = getDurationEnvOrDefault("STUCK_ADHOC_POD_TIMEOUT", 10*time.Minute)
	Config.StuckAdhocPodStartingTimeout = getDurationEnvOrDefault("STUCK_ADHOC_POD_STARTING_TIMEOUT", 30*time.Minute)
	Config.RemoveFailedDrivesFromWeka = getBoolEnvOrDefault("REMOVE_FAILED_DRIVES_FROM_WEKA", false)
	Config.AllowMultipleProtocolsPerNode = getBoolEnvOrDefault("ALLOW_MULTIPLE_PROTOCOLS_PER_NODE", false)
	Config.PodConfigVersion = env.GetString("POD_CONFIG_VERSION", "1")
	Config.EnablePodConfigCodeVersionRotation = getBoolEnvOrDefault("ENABLE_POD_CONFIG_CODE_VERSION_ROTATION", false)
	Config.AllowRotateNonAnnotatedPodConfigHash = getBoolEnvOrDefault("ALLOW_ROTATE_NON_ANNOTATED_POD_CONFIG_HASH", false)
	Config.ManagementProxyHostNetwork = getBoolEnvOrDefault("MANAGEMENT_PROXY_HOST_NETWORK", false)
	Config.ManagementProxyIngressBaseDomain = env.GetString("MANAGEMENT_PROXY_INGRESS_BASE_DOMAIN", "")
	Config.ManagementProxyIngressClass = env.GetString("MANAGEMENT_PROXY_INGRESS_CLASS", "")
	Config.ManagementProxyReplicas = getInt32EnvInRange("MANAGEMENT_PROXY_REPLICAS", 2, 0, 100, "managementProxy.replicas")
	// Default 50 is Envoy's own default, and what installs ran before this was configurable: the
	// config used to emit no common_lb_config at all, so exposing the value must not change it.
	Config.ManagementProxyHealthyPanicThreshold = getInt32EnvInRange("MANAGEMENT_PROXY_HEALTHY_PANIC_THRESHOLD", 50, 0, 100, "managementProxy.healthyPanicThreshold")
	// Depends on ManagementProxyHostNetwork being set above: the loopback default is derived from it,
	// and reading it before it's populated would silently default the unauthenticated admin API to
	// 0.0.0.0 under hostNetwork.
	Config.ManagementProxyAdminBindAddress = getIPEnvOrDefault("MANAGEMENT_PROXY_ADMIN_BIND_ADDRESS",
		defaultManagementProxyAdminBindAddress(Config.ManagementProxyHostNetwork), "managementProxy.adminBindAddress")

	// Metrics server environment configuration
	Config.MetricsServerEnv.NodeName = env.GetString("NODE_NAME", "")

	// Node agent device plugin configuration (NUMA region extended resources). Off by
	// default; the chart always emits NODE_AGENT_DEVICE_PLUGIN_ENABLED explicitly so
	// nodeAgent.devicePlugin.enabled=true still turns it on.
	Config.NodeAgentDevicePlugin.Enabled = getBoolEnvOrDefault("NODE_AGENT_DEVICE_PLUGIN_ENABLED", false)
	Config.NodeAgentDevicePlugin.KubeletPath = strings.TrimRight(getEnvOrDefault("NODE_AGENT_KUBELET_PATH", "/var/lib/kubelet"), "/")

	Config.NetnsEnabled = getBoolEnvOrDefault("NETNS_ENABLED", true)

	// CSI configuration
	Config.Csi.Enabled = getBoolEnvOrDefault("CSI_INSTALLATION_ENABLED", false)
	Config.Csi.StorageClassCreationDisabled = getBoolEnvOrDefault("CSI_STORAGE_CLASS_CREATION_DISABLED", false)
	Config.Csi.SelinuxSupport = getEnvOrDefault("CSI_SELINUX_SUPPORT", "auto")
	Config.Csi.KubeletPath = strings.TrimRight(getEnvOrDefault("CSI_KUBELET_PATH", "/var/lib/kubelet"), "/")
	Config.Csi.WekafsImage = env.GetString("CSI_IMAGE", "")
	Config.Csi.ProvisionerImage = env.GetString("CSI_PROVISIONER_IMAGE", "")
	Config.Csi.AttacherImage = env.GetString("CSI_ATTACHER_IMAGE", "")
	Config.Csi.LivenessProbeImage = env.GetString("CSI_LIVENESSPROBE_IMAGE", "")
	Config.Csi.ResizerImage = env.GetString("CSI_RESIZER_IMAGE", "")
	Config.Csi.SnapshotterImage = env.GetString("CSI_SNAPSHOTTER_IMAGE", "")
	Config.Csi.RegistrarImage = env.GetString("CSI_REGISTRAR_IMAGE", "")
	Config.Csi.PreventNewWorkloadOnClientContainerNotRunning = getBoolEnvOrDefault("CSI_PREVENT_NEW_WORKLOAD_ON_CLIENT_CONTAINER_NOT_RUNNING", true)
	Config.Csi.LogLevel = getIntEnvOrDefault("CSI_LOG_LEVEL", 5)
	Config.Csi.HostNetwork = getBoolEnvOrDefault("CSI_HOST_NETWORK", false)
	Config.Csi.AllowMountOptionOverrides = getBoolEnvOrDefault("CSI_ALLOW_MOUNT_OPTION_OVERRIDES", false)
	Config.Csi.ControllerResources = parseCsiControllerResources()
	Config.Csi.NodeResources = parseCsiNodeResources()
	Config.SyslogPackage = getEnvOrDefault("SYSLOG_PACKAGE", "auto")
	Config.Proxy = getEnvOrDefault("PROXY", "")

	// Priority classes configuration
	Config.PriorityClasses.Initial = getEnvOrDefault("PRIORITY_CLASS_INITIAL", "weka-initial-no-evict")
	Config.PriorityClasses.Targeted = getEnvOrDefault("PRIORITY_CLASS_TARGETED", "weka-targeted-no-evict")

	// NFS configuration
	Config.Nfs.MountdPort = getIntEnvOrDefault("NFS_MOUNTD_PORT", 0)
	Config.Nfs.LockmanagerPort = getIntEnvOrDefault("NFS_LOCKMANAGER_PORT", 0)
	Config.Nfs.NotifyPort = getIntEnvOrDefault("NFS_NOTIFY_PORT", 0)

	// SMBW configuration
	Config.Smbw.ShmSize = getEnvOrDefault("SMBW_SHM_SIZE", "8Gi")

	// Drive-sharing, cluster-capacity and compute-hugepages config (shared with the weka-capacity CLI).
	LoadCapacityEnv()

	// Builder images configuration
	Config.BuilderImages.Default = getEnvOrDefault("BUILDER_IMAGE_DEFAULT", "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22")
	Config.BuilderImages.Ubuntu24 = getEnvOrDefault("BUILDER_IMAGE_UBUNTU24", "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24")

	// Port allocation configuration
	Config.PortAllocation.StartingPort = getIntEnvOrDefault("PORT_ALLOCATION_STARTING_PORT", 35000)

	// Hugepages update propagation configuration
	Config.HugepagesUpdate.Compute = getBoolEnvOrDefault("HUGEPAGES_UPDATE_COMPUTE", false)
	Config.HugepagesUpdate.Drive = getBoolEnvOrDefault("HUGEPAGES_UPDATE_DRIVE", false)

	// Evicted pod cleanup configuration
	Config.EvictedPodCleanupEnabled = getBoolEnvOrDefault("EVICTED_POD_CLEANUP_ENABLED", true)
	Config.EvictedPodCleanupInterval = getDurationEnvOrDefault("EVICTED_POD_CLEANUP_INTERVAL", 2*time.Minute)

	// Webhook deployment configuration
	Config.Webhook.CertDir = getEnvOrDefault("WEBHOOK_CERT_DIR", "/tmp/k8s-webhook-server/serving-certs")
	Config.Webhook.SecretName = getEnvOrDefault("WEBHOOK_SECRET_NAME", "weka-operator-webhook-server-cert")
	Config.Webhook.ServiceName = getEnvOrDefault("WEBHOOK_SERVICE_NAME", "weka-operator-webhook-service")
	Config.Webhook.WebhookName = getEnvOrDefault("WEBHOOK_NAME", "weka-operator-validating-webhook-configuration")

	// Admission control: master switch. When false the operator removes
	// the ValidatingWebhookConfiguration on startup and skips the webhook
	// server entirely.
	Config.AdmissionControl.Enabled = getBoolEnvOrDefault("ADMISSION_CONTROL_ENABLED", true)

	// Admission policies: global posture + per-policy overrides.
	Config.AdmissionPolicies.Mode = strings.ToLower(getEnvOrDefault("ADMISSION_POLICIES_MODE", "relaxed"))
	if Config.AdmissionPolicies.Mode != "strict" && Config.AdmissionPolicies.Mode != "relaxed" {
		klog.Fatalf("invalid ADMISSION_POLICIES_MODE %q: must be 'strict' or 'relaxed'", Config.AdmissionPolicies.Mode)
	}
	Config.AdmissionPolicies.Overrides = loadAdmissionPolicyOverrides(
		getEnvOrDefault("ADMISSION_POLICIES_OVERRIDES", ""),
	)
}

// loadAdmissionPolicyOverrides parses ADMISSION_POLICIES_OVERRIDES (a JSON
// object keyed by policy ID, e.g. `{"cluster_min_drives_feasibility":"warn"}`).
// Unknown keys / invalid values are caught later by ValidateRegistry().
func loadAdmissionPolicyOverrides(raw string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		klog.Fatalf("invalid ADMISSION_POLICIES_OVERRIDES: %v", err)
	}
	for k, v := range out {
		out[k] = strings.ToLower(v)
	}
	return out
}

func getEnvOrFail(envKey string) string {
	val, found := os.LookupEnv(envKey)
	if !found {
		err := fmt.Errorf("failed to find value %s in env vars", envKey)
		klog.Error(err)
		os.Exit(1)
	}
	return val
}

func getEnvOrDefault(envKey, defaultVal string) string {
	val, found := os.LookupEnv(envKey)
	if !found {
		return defaultVal
	}
	return val
}

func getStringSlice(envKey string) []string {
	val, found := os.LookupEnv(envKey)
	if !found || val == "" {
		return nil
	}

	val = env.GetString(envKey, "")

	slice := make([]string, 0)
	for c := range strings.SplitSeq(val, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		slice = append(slice, c)
	}

	return slice
}

func getBoolEnvOrDefault(envKey string, defaultVal bool) bool {
	val, found := os.LookupEnv(envKey)
	if !found {
		return defaultVal
	}

	ival, err := strconv.ParseBool(val)
	if err != nil {
		err = fmt.Errorf("failed to parse boolean value %s from env var %s", val, envKey)
		klog.Error(err)
		os.Exit(1)
	}
	return ival
}

func getCleanupRemovedNodesMode() CleanupRemovedNodesMode {
	val := strings.ToLower(strings.TrimSpace(env.GetString("CLEANUP_REMOVED_NODES", string(CleanupRemovedNodesAuto))))

	switch CleanupRemovedNodesMode(val) {
	case CleanupRemovedNodesOff:
		return CleanupRemovedNodesOff
	case CleanupRemovedNodesOn:
		return CleanupRemovedNodesOn
	case CleanupRemovedNodesAuto, "": // "" == set-but-empty, treat as default
		return CleanupRemovedNodesAuto
	default:
		// Unrecognized value: fail closed to Off rather than silently enabling cleanup on a typo.
		klog.Warningf("invalid CLEANUP_REMOVED_NODES value %q, disabling removed-node cleanup; set one of false/true/auto", val)
		return CleanupRemovedNodesOff
	}
}

func getIntEnvOrDefault(envKey string, defaultVal int) int {
	val, found := os.LookupEnv(envKey)
	if !found || val == "" {
		return defaultVal
	}

	ival, err := strconv.Atoi(val)
	if err != nil {
		err = fmt.Errorf("failed to parse integer value %s from env var %s", val, envKey)
		klog.Error(err)
		os.Exit(1)
	}

	return ival
}

// getInt32EnvInRange parses an integer env var and exits at load time when it falls outside
// [minVal, maxVal], naming the chart value that produced it rather than surfacing far away (e.g. a
// crash-looping proxy from a bad healthy_panic_threshold).
func getInt32EnvInRange(envKey string, defaultVal, minVal, maxVal int32, chartValue string) int32 {
	raw, found := os.LookupEnv(envKey)
	if !found || raw == "" {
		return defaultVal
	}

	// bitSize 32 makes ParseInt reject anything the conversion below would truncate.
	val, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil {
		klog.Exitf("%s=%q is not an integer (set by chart value %s)", envKey, raw, chartValue)
	}

	if val < int64(minVal) || val > int64(maxVal) {
		klog.Exitf("%s=%d is out of range [%d, %d] (set by chart value %s)",
			envKey, val, minVal, maxVal, chartValue)
	}

	return int32(val)
}

// defaultManagementProxyAdminBindAddress defaults the admin bind address when the chart leaves it
// unset. Envoy's admin API is unauthenticated, so under hostNetwork loopback is the only safe
// choice; the chart can't compute this, which is why the default lives here.
func defaultManagementProxyAdminBindAddress(hostNetwork bool) string {
	if hostNetwork {
		return "127.0.0.1"
	}

	return "0.0.0.0"
}

// getIPEnvOrDefault reads an env var that must hold a bare IP address. An unparsable address would
// otherwise only fail once it reaches the component that binds it.
func getIPEnvOrDefault(envKey, defaultVal, chartValue string) string {
	val := strings.TrimSpace(env.GetString(envKey, defaultVal))
	if val == "" {
		return defaultVal
	}

	if net.ParseIP(val) == nil {
		klog.Exitf("%s=%q is not a valid IP address (set by chart value %s)", envKey, val, chartValue)
	}

	return val
}

func getFloatEnvOrDefault(envKey string, defaultVal float64) float64 {
	val, found := os.LookupEnv(envKey)
	if !found || val == "" {
		return defaultVal
	}

	fval, err := strconv.ParseFloat(val, 64)
	if err != nil {
		err = fmt.Errorf("failed to parse float value %s from env var %s", val, envKey)
		klog.Error(err)
		os.Exit(1)
	}

	return fval
}

func getDurationEnvOrDefault(envKey string, defaultVal time.Duration) time.Duration {
	val, found := os.LookupEnv(envKey)
	if !found {
		return defaultVal
	}

	duration, err := time.ParseDuration(val)
	if err != nil {
		klog.Error(err, "failed to parse duration value from env vars")
		os.Exit(1)
	}

	return duration
}

func getMapEnvOrDefault(envKey string, defaultVal map[string]string) map[string]string {
	val, found := os.LookupEnv(envKey)
	if !found || val == "" {
		return defaultVal
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(val), &result); err != nil {
		klog.Error(err, "failed to parse JSON map from env var", "key", envKey, "value", val)
		os.Exit(1)
	}

	return result
}

func parseCsiControllerResources() CsiControllerResources {
	return CsiControllerResources{
		Wekafs:         parseResourceRequirements("CSI_CONTROLLER_WEKAFS"),
		CsiAttacher:    parseResourceRequirements("CSI_CONTROLLER_ATTACHER"),
		CsiProvisioner: parseResourceRequirements("CSI_CONTROLLER_PROVISIONER"),
		CsiResizer:     parseResourceRequirements("CSI_CONTROLLER_RESIZER"),
		CsiSnapshotter: parseResourceRequirements("CSI_CONTROLLER_SNAPSHOTTER"),
	}
}

func parseCsiNodeResources() CsiNodeResources {
	return CsiNodeResources{
		Wekafs:        parseResourceRequirements("CSI_NODE_WEKAFS"),
		LivenessProbe: parseResourceRequirements("CSI_NODE_LIVENESS_PROBE"),
		CsiRegistrar:  parseResourceRequirements("CSI_NODE_REGISTRAR"),
	}
}

func parseResourceRequirements(envPrefix string) ResourceRequirements {
	return ResourceRequirements{
		Limits: ResourceList{
			CPU:    env.GetString(envPrefix+"_LIMITS_CPU", ""),
			Memory: env.GetString(envPrefix+"_LIMITS_MEMORY", ""),
		},
		Requests: ResourceList{
			CPU:    env.GetString(envPrefix+"_REQUESTS_CPU", ""),
			Memory: env.GetString(envPrefix+"_REQUESTS_MEMORY", ""),
		},
	}
}
