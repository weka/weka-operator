package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
	"k8s.io/utils/env"
)

type BindAddress struct {
	Metrics     string
	HealthProbe string
	NodeAgent   string
}

type Timeouts struct {
	ReconcileTimeout time.Duration // Reconcile timeout
	KubeExecTimeout  time.Duration // Kubernetes ssh commands executor timeout
}

// OpenTelemetry settings
type Otel struct {
	DeploymentIdentifier string
	ExporterOtlpEndpoint string
}

type WekaHome struct {
	Endpoint      string
	AllowInsecure bool
	CacertSecret  string
	EnableStats   bool
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

type OkeCompatibility struct {
	EnableNicsAllocation bool
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

func (t *TolerationsMismatchSettings) GetIgnoredTaints() []string {
	if t == nil || !t.EnableIgnoredTaints {
		return nil
	}
	return t.IgnoredTaints
}

var Config struct {
	Version                        string
	OperatorPodUID                 string
	OperatorPodNamespace           string
	OperatorDeploymentName         string
	BindAddress                    BindAddress
	EnableLeaderElection           bool
	EnableClusterApi               bool
	Timeouts                       Timeouts
	Otel                           Otel
	WekaHome                       WekaHome
	DebugSleep                     int
	MaintenanceSaName              string
	MaintenanceImage               string
	MaintenanceImagePullSecret     string
	OcpCompatibility               OcpCompatibility
	GkeCompatibility               GkeCompatibility
	OkeCompatibility               OkeCompatibility
	WekaAllocZombieDeleteAfter     time.Duration
	DevMode                        bool
	Logging                        Logging
	MaxWorkers                     MaxWorkers
	Metrics                        Metrics
	Mode                           OperatorMode
	LocalDataPvc                   string
	DNSPolicy                      DNSPolicy
	SignDrivesImage                string
	SkipUnhealthyToleration        bool
	SkipClientNoScheduleToleration bool
	SkipAuxNoScheduleToleration    bool
	MetricsServerEnv               MetricsServerEnv
	Upgrade                        struct {
		ComputeThresholdPercent          int
		DriveThresholdPercent            int
		MaxDeactivatingContainersPercent int
	}
	CleanupRemovedNodes                    bool
	CleanupBackendsOnNodeSelectorMismatch  bool
	CleanupClientsOnNodeSelectorMismatch   bool
	CleanupContainersOnTolerationsMismatch bool
	EvictContainerOnDeletion               bool
	RemovalThrottlingEnabled               bool
	SkipClientsTolerationValidation        bool
	TolerationsMismatchSettings            TolerationsMismatchSettings
	DeleteEnvoyWithoutS3NeighborTimeout    time.Duration
	DeleteUnschedulablePodsAfter           time.Duration

	CsiInstallationEnabled          bool
	CsiStorageClassCreationDisabled bool
	CsiImage                        string
	CsiProvisionerImage             string
	CsiAttacherImage                string
	CsiLivenessProbeImage           string
	CsiResizerImage                 string
	CsiSnapshotterImage             string
	CsiRegistrarImage               string
	SyslogPackage                   string
	Proxy                           string
}

type NodeAgentRequestsTimeouts struct {
	Register         time.Duration
	GetContainerInfo time.Duration
}

type Metrics struct {
	Clusters struct {
		Enabled     bool
		PollingRate time.Duration
		Image       string
	}
	Containers struct {
		Enabled          bool
		PollingRate      time.Duration
		RequestsTimeouts NodeAgentRequestsTimeouts
	}
	NodeAgentSecretName string
}

var Consts struct {
	DevModeNamespace string
	// sleep between container self-update allocations attempts
	ContainerUpdateAllocationsSleep time.Duration
	// TTL for join ips cache
	JoinIpsCacheTTL time.Duration
	// Limit for the number of contianers to be created during one reconcile loop
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
}

func init() {
	Consts.DevModeNamespace = "weka-operator-system"
	Consts.ContainerUpdateAllocationsSleep = 10 * time.Second
	Consts.JoinIpsCacheTTL = 1 * time.Minute
	Consts.NewContainersLimit = 1000 // virtually no limit for now
	Consts.PeriodicDrivesCheckInterval = 10 * time.Minute
	Consts.CheckDriversInterval = 7 * time.Minute
	Consts.FormClusterMinComputeContainers = 5
	Consts.FormClusterMinDriveContainers = 5
	Consts.FormClusterMaxComputeContainers = 10
	Consts.FormClusterMaxDriveContainers = 10
	Consts.FormS3ClusterMaxContainerCount = 3
	Consts.CsiLoginCredentialsUpdateInterval = 1 * time.Minute
	Consts.CsiFileSystemName = "default"
	Consts.CsiLegacyDriverName = "csi.weka.io"
	Consts.MaxContainersDeletedOnSelectorMismatch = 4
	Consts.SelectorMismatchCleanupInterval = 2 * time.Minute
	Consts.TolerationsMismatchCleanupInterval = 1 * time.Minute
}

func ConfigureEnv(ctx context.Context) {
	Config.Version = getEnvOrFail("VERSION")
	Config.Mode = OperatorMode(env.GetString("OPERATOR_MODE", string(OperatorModeManager)))
	Config.OperatorPodUID = os.Getenv("POD_UID")
	Config.OperatorPodNamespace = os.Getenv("POD_NAMESPACE")
	Config.OperatorDeploymentName = os.Getenv("OPERATOR_DEPLOYMENT_NAME")
	if Config.Mode == OperatorModeManager {
		Config.BindAddress.Metrics = getEnvOrFail("OPERATOR_METRICS_BIND_ADDRESS")
		Config.BindAddress.HealthProbe = getEnvOrFail("HEALTH_PROBE_BIND_ADDRESS")
		Config.MaintenanceSaName = getEnvOrFail("WEKA_OPERATOR_MAINTENANCE_SA_NAME")
		Config.OcpCompatibility.DriverToolkitSecretName = getEnvOrFail("WEKA_OCP_PULL_SECRET")
	}
	Config.BindAddress.NodeAgent = getEnvOrDefault("NODE_AGENT_BIND_ADDRESS", ":8090")
	Config.EnableLeaderElection = getBoolEnvOrDefault("ENABLE_LEADER_ELECTION", false)
	Config.EnableClusterApi = getBoolEnvOrDefault("ENABLE_CLUSTER_API", false)
	Config.Timeouts.KubeExecTimeout = getDurationEnvOrDefault("KUBE_EXEC_TIMEOUT", 5*time.Minute)
	Config.Timeouts.ReconcileTimeout = getDurationEnvOrDefault("RECONCILE_TIMEOUT", 30*time.Minute)
	Config.Otel.DeploymentIdentifier = os.Getenv("OTEL_DEPLOYMENT_IDENTIFIER")
	Config.Otel.ExporterOtlpEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	// Weka Home configuration
	Config.WekaHome.Endpoint = getEnvOrDefault("WEKA_OPERATOR_WEKA_HOME_ENDPOINT", "https://api.home.weka.io")
	Config.WekaHome.AllowInsecure = getBoolEnvOrDefault("WEKA_OPERATOR_WEKA_HOME_INSECURE", false)
	Config.WekaHome.CacertSecret = os.Getenv("WEKA_OPERATOR_WEKA_HOME_CACERT_SECRET")
	Config.WekaHome.EnableStats = getBoolEnvOrDefault("WEKA_OPERATOR_WEKA_HOME_ENABLE_STATS", true)
	Config.DebugSleep = getIntEnvOrDefault("WEKA_OPERATOR_DEBUG_SLEEP", 3)
	Config.MaintenanceImage = getEnvOrDefault("WEKA_MAINTENANCE_IMAGE", "quay.io/weka.io/busybox")
	Config.Upgrade.ComputeThresholdPercent = getIntEnvOrDefault("UPGRADE_COMPUTE_THRESHOLD_PERCENT", 90)
	Config.Upgrade.DriveThresholdPercent = getIntEnvOrDefault("UPGRADE_DRIVE_THRESHOLD_PERCENT", 90)
	Config.Upgrade.MaxDeactivatingContainersPercent = getIntEnvOrDefault("UPGRADE_MAX_DEACTIVATING_CONTAINERS_PERCENT", 10)
	Config.MaintenanceImagePullSecret = os.Getenv("WEKA_MAINTENANCE_IMAGE_PULL_SECRET")
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
	Config.Metrics.Containers.Enabled = getBoolEnvOrDefault("METRICS_CONTAINERS_ENABLED", true)
	Config.Metrics.Containers.PollingRate = getDurationEnvOrDefault("METRICS_CONTAINERS_POLLING_RATE", time.Second*60)
	Config.Metrics.Containers.RequestsTimeouts.Register = getDurationEnvOrDefault("METRICS_CONTAINERS_REQUEST_TIMEOUT_REGISTER", time.Second*3)
	Config.Metrics.Containers.RequestsTimeouts.GetContainerInfo = getDurationEnvOrDefault("METRICS_CONTAINERS_REQUEST_TIMEOUT_GET_CONTAINER_INFO", time.Second*10)
	Config.Metrics.Clusters.Image = env.GetString("METRICS_CLUSTERS_IMAGE", "nginx:1.27.3")
	Config.Metrics.NodeAgentSecretName = env.GetString("METRICS_NODE_AGENT_TOKEN", "weka-node-agent-secret")
	Config.LocalDataPvc = env.GetString("LOCAL_DATA_PVC", "")
	Config.DNSPolicy.K8sNetwork = env.GetString("DNS_POLICY_K8S_NETWORK", "")
	Config.DNSPolicy.HostNetwork = env.GetString("DNS_POLICY_HOST_NETWORK", "")
	Config.SignDrivesImage = env.GetString("SIGN_DRIVES_IMAGE", "")
	Config.SkipUnhealthyToleration = getBoolEnvOrDefault("SKIP_UNHEALTHY_TOLERATION", false)
	Config.SkipClientNoScheduleToleration = getBoolEnvOrDefault("SKIP_CLIENT_NO_SCHEDULE_TOLERATION", false)
	Config.SkipAuxNoScheduleToleration = getBoolEnvOrDefault("SKIP_AUX_NO_SCHEDULE_TOLERATION", false)
	Config.CleanupRemovedNodes = getBoolEnvOrDefault("CLEANUP_REMOVED_NODES", false)
	Config.CleanupBackendsOnNodeSelectorMismatch = getBoolEnvOrDefault("CLEANUP_BACKENDS_ON_NODE_SELECTOR_MISMATCH", false)
	Config.CleanupClientsOnNodeSelectorMismatch = getBoolEnvOrDefault("CLEANUP_CLIENTS_ON_NODE_SELECTOR_MISMATCH", false)
	Config.CleanupContainersOnTolerationsMismatch = getBoolEnvOrDefault("CLEANUP_CONTAINERS_ON_TOLERATIONS_MISMATCH", false)
	Config.EvictContainerOnDeletion = getBoolEnvOrDefault("EVICT_CONTAINER_ON_DELETION", false)
	Config.RemovalThrottlingEnabled = getBoolEnvOrDefault("REMOVAL_THROTTLING_ENABLED", false)
	Config.SkipClientsTolerationValidation = getBoolEnvOrDefault("SKIP_CLIENTS_TOLERATION_VALIDATION", false)
	Config.TolerationsMismatchSettings.EnableIgnoredTaints = getBoolEnvOrDefault("TOLERATIONS_MISMATCH_SETTINGS_ENABLE_IGNORED_TAINTS", true)
	Config.TolerationsMismatchSettings.IgnoredTaints = getStringSlice("TOLERATIONS_MISMATCH_SETTINGS_IGNORED_TAINTS")
	Config.DeleteEnvoyWithoutS3NeighborTimeout = getDurationEnvOrDefault("DELETE_ENVOY_WITHOUT_S3_NEIGHBOR_TIMEOUT", 5*time.Minute)
	Config.DeleteUnschedulablePodsAfter = getDurationEnvOrDefault("DELETE_UNSCHEDULABLE_PODS_AFTER", 1*time.Minute)

	// Metrics server environment configuration
	Config.MetricsServerEnv.NodeName = env.GetString("NODE_NAME", "")

	// CSI configuration
	Config.CsiInstallationEnabled = getBoolEnvOrDefault("CSI_INSTALLATION_ENABLED", false)
	Config.CsiStorageClassCreationDisabled = getBoolEnvOrDefault("CSI_STORAGE_CLASS_CREATION_DISABLED", false)
	Config.CsiImage = env.GetString("CSI_IMAGE", "")
	Config.CsiProvisionerImage = env.GetString("CSI_PROVISIONER_IMAGE", "")
	Config.CsiAttacherImage = env.GetString("CSI_ATTACHER_IMAGE", "")
	Config.CsiLivenessProbeImage = env.GetString("CSI_LIVENESSPROBE_IMAGE", "")
	Config.CsiResizerImage = env.GetString("CSI_RESIZER_IMAGE", "")
	Config.CsiSnapshotterImage = env.GetString("CSI_SNAPSHOTTER_IMAGE", "")
	Config.CsiRegistrarImage = env.GetString("CSI_REGISTRAR_IMAGE", "")
	Config.SyslogPackage = getEnvOrDefault("SYSLOG_PACKAGE", "auto")
	Config.Proxy = getEnvOrDefault("PROXY", "")

	Config.OkeCompatibility.EnableNicsAllocation = getBoolEnvOrDefault("OKE_ENABLE_NICS_ALLOCATION", false)
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

func getEnvOrDefault(envKey string, defaultVal string) string {
	val, found := os.LookupEnv(envKey)
	if !found {
		return defaultVal
	}
	return val
}

func getBoolEnv(envKey string) bool {
	val, found := os.LookupEnv(envKey)
	if !found {
		err := fmt.Errorf("failed to find value %s in env vars", envKey)
		klog.Error(err)
		os.Exit(1)
	}
	ival, err := strconv.ParseBool(val)
	if err != nil {
		err = fmt.Errorf("failed to parse boolean value %s from env vars", val)
		klog.Error(err)
		os.Exit(1)
	}
	return ival
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

func getDurationEnv(envKey string) time.Duration {
	val, found := os.LookupEnv(envKey)
	if !found {
		err := fmt.Errorf("failed to find value %s in env vars", envKey)
		klog.Error(err)
		os.Exit(1)
	}

	duration, err := time.ParseDuration(val)
	if err != nil {
		err = fmt.Errorf("failed to parse duration value %s from env var %s", val, envKey)
		klog.Error(err)
		os.Exit(1)
	}

	return duration
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
