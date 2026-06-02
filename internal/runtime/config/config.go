package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	v1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

var version = "dev" // set via -ldflags at build time

type Config struct {
	// Identity
	Mode              string
	Name              string
	NodeName          string
	PodName           string
	PodNamespace      string
	PodID             string
	FailureDomain     string
	MachineIdentifier string
	Version           string
	Drives            []string // populated at runtime from NodeResources

	// Resource allocation
	Cores         []int
	CoreIDs       []int
	CPUPolicy     string
	Memory        string
	DPDKBaseMemMB int

	// Network
	NetworkDevice         string
	Subnets               []string
	NetworkSelectors      []string
	ManagementIPSelectors []string
	Port                  int
	AgentPort             int
	BasePort              int
	PortRange             int
	JoinIPs               []string
	IsIPv6                bool
	UDPMode               bool
	BindManagementAll     bool
	ManagementIP          string
	NetGateway            string
	NvidiaVFSingleIP      bool

	// Runtime behaviour
	DistService         string
	DriversBuildID      string
	DumperConfigMode    string
	WekaContainerID     string
	WekaPersistenceMode string
	AutoRemoveTimeout   int
	PreRunScript        string
	ImageName           string
	TargetImageName     string
	SyslogPackage       string

	// Hugepages
	COSAllowHugepageConfig    bool
	COSAllowDisableDriverSign bool
	COSGlobalHugepageSize     string
	COSGlobalHugepageCount    int

	// Observability
	OtelEndpoint       string
	OtelLogsEndpoint   string
	OtelHeaders        string
	OtelLogsHeaders    string
	OtelServiceName    string
	OtelServiceVersion string
	OtelLogsEnabled    bool
	MaxTraceCapacityGB int
	EnsureFreeSpaceGB  int
	DebugSleep         int

	// Ad-hoc operations
	Instructions *v1alpha1.Instructions

	// Feature flags (parsed from release spec JSON in env RELEASE_SPEC)
	Features domain.FeatureFlags
}

func Load() *Config {
	cfg := &Config{
		Version: version,
	}
	cfg.Mode = os.Getenv("MODE")
	cfg.Name = os.Getenv("NAME")
	cfg.NodeName = os.Getenv("NODE_NAME")
	cfg.PodName = os.Getenv("POD_NAME")
	cfg.PodNamespace = os.Getenv("POD_NAMESPACE")
	cfg.PodID = os.Getenv("POD_ID")
	cfg.FailureDomain = os.Getenv("FAILURE_DOMAIN")
	cfg.MachineIdentifier = os.Getenv("MACHINE_IDENTIFIER")

	cfg.Cores = parseIntSlice(os.Getenv("CORES"))
	cfg.CoreIDs = parseIntSlice(os.Getenv("CORE_IDS"))
	cfg.CPUPolicy = os.Getenv("CPU_POLICY")
	cfg.Memory = os.Getenv("MEMORY")
	cfg.DPDKBaseMemMB = parseInt(os.Getenv("DPDK_BASE_MEMORY_MB"))

	cfg.NetworkDevice = os.Getenv("NETWORK_DEVICE")
	cfg.Subnets = parseStringSlice(os.Getenv("SUBNETS"))
	cfg.NetworkSelectors = parseStringSlice(os.Getenv("NETWORK_SELECTORS"))
	cfg.ManagementIPSelectors = parseStringSlice(os.Getenv("MANAGEMENT_IPS_SELECTORS"))
	cfg.Port = parseInt(os.Getenv("PORT"))
	cfg.AgentPort = parseInt(os.Getenv("AGENT_PORT"))
	cfg.BasePort = parseInt(os.Getenv("BASE_PORT"))
	cfg.PortRange = parseInt(os.Getenv("PORT_RANGE"))
	cfg.JoinIPs = parseStringSlice(os.Getenv("JOIN_IPS"))
	cfg.IsIPv6 = parseBool(os.Getenv("IS_IPV6"))
	cfg.UDPMode = parseBool(os.Getenv("UDP_MODE"))
	cfg.BindManagementAll = parseBool(os.Getenv("BIND_MANAGEMENT_ALL"))
	cfg.ManagementIP = os.Getenv("MANAGEMENT_IP")
	cfg.NetGateway = os.Getenv("NET_GATEWAY")
	cfg.NvidiaVFSingleIP = parseBool(os.Getenv("NVIDIA_VF_SINGLE_IP"))

	cfg.DistService = os.Getenv("DIST_SERVICE")
	cfg.DriversBuildID = os.Getenv("DRIVERS_BUILD_ID")
	cfg.DumperConfigMode = os.Getenv("DUMPER_CONFIG_MODE")
	cfg.WekaContainerID = os.Getenv("WEKA_CONTAINER_ID")
	cfg.WekaPersistenceMode = os.Getenv("WEKA_PERSISTENCE_MODE")
	cfg.AutoRemoveTimeout = parseInt(os.Getenv("AUTO_REMOVE_TIMEOUT"))
	cfg.PreRunScript = os.Getenv("PRE_RUN_SCRIPT")
	cfg.ImageName = os.Getenv("IMAGE_NAME")
	cfg.TargetImageName = os.Getenv("TARGET_IMAGE_NAME")
	cfg.SyslogPackage = os.Getenv("SYSLOG_PACKAGE")

	cfg.COSAllowHugepageConfig = parseBool(os.Getenv("WEKA_COS_ALLOW_HUGEPAGE_CONFIG"))
	cfg.COSAllowDisableDriverSign = parseBool(os.Getenv("WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING"))
	cfg.COSGlobalHugepageSize = os.Getenv("WEKA_COS_GLOBAL_HUGEPAGE_SIZE")
	cfg.COSGlobalHugepageCount = parseInt(os.Getenv("WEKA_COS_GLOBAL_HUGEPAGE_COUNT"))

	cfg.OtelEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	cfg.OtelLogsEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	cfg.OtelHeaders = os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")
	cfg.OtelLogsHeaders = os.Getenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS")
	cfg.OtelServiceName = os.Getenv("OTEL_SERVICE_NAME")
	cfg.OtelServiceVersion = os.Getenv("OTEL_SERVICE_VERSION")
	cfg.OtelLogsEnabled = parseBool(os.Getenv("OTEL_LOGS_ENABLED"))
	cfg.MaxTraceCapacityGB = parseInt(os.Getenv("MAX_TRACE_CAPACITY_GB"))
	cfg.EnsureFreeSpaceGB = parseInt(os.Getenv("ENSURE_FREE_SPACE_GB"))
	cfg.DebugSleep = parseInt(os.Getenv("WEKA_OPERATOR_DEBUG_SLEEP"))

	if raw := os.Getenv("INSTRUCTIONS"); raw != "" {
		var inst v1alpha1.Instructions
		if err := json.Unmarshal([]byte(raw), &inst); err == nil {
			cfg.Instructions = &inst
		}
	}

	cfg.Features = loadFeatureFlags()

	return cfg
}

func parseInt(s string) int {
	if s == "" {
		return 0
	}
	v, _ := strconv.Atoi(s) //nolint:errcheck // invalid env var treated as zero
	return v
}

func parseBool(s string) bool {
	v, _ := strconv.ParseBool(s) //nolint:errcheck // invalid env var treated as false
	return v
}

func parseStringSlice(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func parseIntSlice(s string) []int {
	parts := parseStringSlice(s)
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		if v := parseInt(strings.TrimSpace(p)); v != 0 || strings.TrimSpace(p) == "0" {
			result = append(result, v)
		}
	}
	return result
}

// loadFeatureFlags reads the Weka release spec from /opt/weka/dist/release/ and decodes
// the base64 bitmap into a FeatureFlags struct. Mirrors Python get_feature_flags() /
// parse_feature_bitmap() at weka_runtime.py:333-398.
func loadFeatureFlags() domain.FeatureFlags {
	const releaseDir = "/opt/weka/dist/release"
	entries, err := os.ReadDir(releaseDir)
	if err != nil || len(entries) == 0 {
		return domain.FeatureFlags{}
	}
	data, err := os.ReadFile(filepath.Join(releaseDir, entries[0].Name()))
	if err != nil {
		return domain.FeatureFlags{}
	}
	var spec struct {
		FeatureFlags string `json:"feature_flags"`
	}
	if err := json.Unmarshal(data, &spec); err != nil || spec.FeatureFlags == "" {
		return domain.FeatureFlags{}
	}
	return featureFlagsFromBitmap(spec.FeatureFlags)
}

// featureFlagsFromBitmap decodes a base64 feature bitmap string into a FeatureFlags struct.
// Bit ordering matches Python's parse_feature_bitmap: byte 0 bit 0 = index 0, etc.
func featureFlagsFromBitmap(b64 string) domain.FeatureFlags {
	bitmap, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return domain.FeatureFlags{}
	}
	active := make(map[int]bool, len(bitmap)*8)
	for byteIdx, b := range bitmap {
		if b == 0 {
			continue
		}
		for bitIdx := range 8 {
			if b&(1<<bitIdx) != 0 {
				active[byteIdx*8+bitIdx] = true
			}
		}
	}
	return domain.FeatureFlags{
		TracesOverridePartialSupport:      active[0],
		TracesOverrideInSlashTraces:       active[1],
		SupportsBindingToNotAllInterfaces: active[2],
		AgentValidate60PortsPerContainer:  active[3],
		AllowPerContainerDriverInterfaces: active[4],
		WekaGetCopyLocalDriverFiles:       active[5],
		DriverSupportsAutoDrain:           active[6],
		SsdProxyIommuSupport:              active[7],
		// bit 8 unused
		SsdProxyIncludesDpdkMemory: active[9],
	}
}
