package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/utils/env"

	"k8s.io/klog/v2"
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

var Config struct {
	Version                    string
	OperatorPodUID             string
	OperatorPodNamespace       string
	BindAddress                BindAddress
	EnableLeaderElection       bool
	EnableClusterApi           bool
	Timeouts                   Timeouts
	Otel                       Otel
	WekaHome                   WekaHome
	DebugSleep                 int
	MaintenanceSaName          string
	MaintenanceImage           string
	MaintenanceImagePullSecret string
	OcpCompatibility           OcpCompatibility
	GkeCompatibility           GkeCompatibility
	WekaAllocZombieDeleteAfter time.Duration
	DevMode                    bool
	Logging                    Logging
	MaxWorkers                 MaxWorkers
	Metrics                    Metrics
	Mode                       OperatorMode
	LocalDataPvc               string
}

type Metrics struct {
	Clusters struct {
		Enabled     bool
		PollingRate time.Duration
		Image       string
	}
	Containers struct {
		Enabled     bool
		PollingRate time.Duration
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
}

func init() {
	Consts.DevModeNamespace = "weka-operator-system"
	Consts.ContainerUpdateAllocationsSleep = 10 * time.Second
	Consts.JoinIpsCacheTTL = 1 * time.Minute
	Consts.NewContainersLimit = 1000 // virtually no limit for now
}

func ConfigureEnv(ctx context.Context) {
	Config.Version = getEnvOrFail("VERSION")
	Config.Mode = OperatorMode(env.GetString("OPERATOR_MODE", string(OperatorModeManager)))
	Config.OperatorPodUID = os.Getenv("POD_UID")
	Config.OperatorPodNamespace = os.Getenv("POD_NAMESPACE")
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
	Config.MaintenanceImage = getEnvOrDefault("WEKA_MAINTENANCE_IMAGE", "busybox")
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
	Config.Metrics.Clusters.Image = env.GetString("METRICS_CLUSTERS_IMAGE", "nginx:1.27.3")
	Config.Metrics.NodeAgentSecretName = env.GetString("METRICS_NODE_AGENT_TOKEN", "weka-node-agent-secret")
	Config.LocalDataPvc = env.GetString("LOCAL_DATA_PVC", "")
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

func getBoolEnvOrDefault(envKey string, defaultVal bool) bool {
	val, found := os.LookupEnv(envKey)
	if !found {
		return defaultVal
	}

	ival, err := strconv.ParseBool(val)
	if err != nil {
		err = fmt.Errorf("failed to parse boolean value %s from env vars", val)
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
		err = fmt.Errorf("failed to parse integer value %s from env vars", val)
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
		err = fmt.Errorf("failed to parse duration value %s from env vars", val)
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
