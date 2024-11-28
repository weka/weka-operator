package config

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"k8s.io/klog/v2"
)

type BindAddress struct {
	Metrics     string
	HealthProbe string
}

type TombstoneGC struct {
	Interval            time.Duration
	Expiration          time.Duration
	DeleteOnNodeMissing bool // Allow deletion of tombstones when node is not anymore a part of the cluster
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
	Tombstone           int
}

var Config struct {
	Version                    string
	PodUID                     string
	BindAddress                BindAddress
	EnableLeaderElection       bool
	EnableClusterApi           bool
	EnableTombstoneGC          bool
	TombstoneGC                *TombstoneGC
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
}

type Metrics struct {
	Clusters struct {
		Enabled     bool
		PollingRate time.Duration
	}
	Containers struct {
		Enabled     bool
		PollingRate time.Duration
	}
}

var Consts struct {
	DevModeNamespace string
}

func init() {
	Consts.DevModeNamespace = "weka-operator-system"
}

func ConfigureEnv(ctx context.Context) {
	Config.Version = getEnvOrFail("VERSION")
	Config.PodUID = os.Getenv("POD_UID")
	Config.BindAddress.Metrics = getEnvOrFail("OPERATOR_METRICS_BIND_ADDRESS")
	Config.BindAddress.HealthProbe = getEnvOrFail("HEALTH_PROBE_BIND_ADDRESS")
	Config.EnableLeaderElection = getBoolEnvOrDefault("ENABLE_LEADER_ELECTION", false)
	Config.EnableClusterApi = getBoolEnvOrDefault("ENABLE_CLUSTER_API", false)
	Config.EnableTombstoneGC = getBoolEnvOrDefault("ENABLE_TOMBSTONE_GC", true)
	if Config.EnableTombstoneGC {
		Config.TombstoneGC = &TombstoneGC{
			Interval:            getDurationEnvOrDefault("TOMBSTONE_GC_INTERVAL", 5*time.Minute),
			Expiration:          getDurationEnv("TOMBSTONE_EXPIRATION"),
			DeleteOnNodeMissing: getBoolEnvOrDefault("ALLOW_TOMBSTONE_DELETE_ON_NODE_MISSING", true),
		}
	}
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
	Config.MaintenanceSaName = getEnvOrFail("WEKA_OPERATOR_MAINTENANCE_SA_NAME")
	Config.MaintenanceImage = getEnvOrDefault("WEKA_MAINTENANCE_IMAGE", "busybox")
	Config.MaintenanceImagePullSecret = os.Getenv("WEKA_MAINTENANCE_IMAGE_PULL_SECRET")
	Config.OcpCompatibility.DriverToolkitSecretName = getEnvOrFail("WEKA_OCP_PULL_SECRET")
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
	Config.MaxWorkers.Tombstone = getIntEnvOrDefault("MAX_WORKERS_TOMBSTONE", 5)

	Config.Metrics.Clusters.Enabled = getBoolEnvOrDefault("METRICS_CLUSTERS_ENABLED", true)
	Config.Metrics.Clusters.PollingRate = getDurationEnvOrDefault("METRICS_CLUSTERS_POLLING_RATE", time.Second*60)
	Config.Metrics.Containers.Enabled = getBoolEnvOrDefault("METRICS_CONTAINERS_ENABLED", true)
	Config.Metrics.Containers.PollingRate = getDurationEnvOrDefault("METRICS_CONTAINERS_POLLING_RATE", time.Second*60)
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
