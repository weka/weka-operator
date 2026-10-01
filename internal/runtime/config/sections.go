package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

var version = "dev" // set via -ldflags at build time

// Runtime holds process-level settings common to every mode.
type Runtime struct {
	Mode              string        // MODE
	BinaryVersion     string        // -ldflags, not env
	DebugExitWait     time.Duration // WEKA_OPERATOR_DEBUG_SLEEP, default 3s
	AutoRemoveTimeout int           // AUTO_REMOVE_TIMEOUT, default 0
}

func parseRuntime(e Env) (Runtime, error) {
	sleep, err := parseIntStrict(e, "WEKA_OPERATOR_DEBUG_SLEEP", 3)
	if err != nil {
		return Runtime{}, err
	}
	autoRemove, err := parseIntStrict(e, "AUTO_REMOVE_TIMEOUT", 0)
	if err != nil {
		return Runtime{}, err
	}
	return Runtime{
		Mode:              e.Get("MODE"),
		BinaryVersion:     version,
		DebugExitWait:     time.Duration(sleep) * time.Second,
		AutoRemoveTimeout: autoRemove,
	}, nil
}

// Identity holds the container's names and requested placement inputs.
type Identity struct {
	Name              string  // NAME
	NodeName          string  // NODE_NAME
	PodName           string  // POD_NAME
	PodNamespace      string  // POD_NAMESPACE
	PodID             string  // POD_ID
	FailureDomain     *string // FAILURE_DOMAIN; nil when absent
	MachineIdentifier string  // MACHINE_IDENTIFIER
}

func parseIdentity(e Env) Identity {
	id := Identity{
		Name:              e.Get("NAME"),
		NodeName:          e.Get("NODE_NAME"),
		PodName:           e.Get("POD_NAME"),
		PodNamespace:      e.Get("POD_NAMESPACE"),
		PodID:             e.Get("POD_ID"),
		MachineIdentifier: e.Get("MACHINE_IDENTIFIER"),
	}
	if v, ok := e.Lookup("FAILURE_DOMAIN"); ok {
		id.FailureDomain = &v
	}
	return id
}

// CPU holds core counts and selection policy.
type CPU struct {
	Cores            int           // CORES — a scalar count
	CoreIDs          CoreSelection // CORE_IDS
	NonDatapathCores CoreSelection // NON_DATAPATH_CORE_IDS
	Policy           string        // CPU_POLICY, default "auto"
}

func parseCPU(e Env) (CPU, error) {
	cores, err := parseIntStrict(e, "CORES", 0)
	if err != nil {
		return CPU{}, err
	}
	coreIDs, err := parseCoreSelection(e, "CORE_IDS")
	if err != nil {
		return CPU{}, err
	}
	nonDatapath, err := parseCoreSelection(e, "NON_DATAPATH_CORE_IDS")
	if err != nil {
		return CPU{}, err
	}
	policy, ok := e.Lookup("CPU_POLICY")
	if !ok {
		policy = "auto"
	}
	return CPU{Cores: cores, CoreIDs: coreIDs, NonDatapathCores: nonDatapath, Policy: policy}, nil
}

// Memory holds the container memory request and DPDK reservation.
type Memory struct {
	Request     string // MEMORY, e.g. "512GiB"; parsed to bytes by weka.ParseSize
	DPDKBaseMiB int    // DPDK_BASE_MEMORY_MB, default 64
}

func parseMemory(e Env) (Memory, error) {
	dpdk, err := parseIntStrict(e, "DPDK_BASE_MEMORY_MB", 64)
	if err != nil {
		return Memory{}, err
	}
	return Memory{Request: e.Get("MEMORY"), DPDKBaseMiB: dpdk}, nil
}

// Network holds device selection, addressing, and join inputs.
type Network struct {
	Device                string                 // NETWORK_DEVICE
	Subnets               []string               // SUBNETS
	Selectors             []weka.NetworkSelector // NETWORK_SELECTORS
	ManagementIPSelectors []weka.NetworkSelector // MANAGEMENT_IPS_SELECTORS
	ManagementIP          string                 // MANAGEMENT_IP
	JoinIPs               []string               // JOIN_IPS, "ip:port"
	IsIPv6                bool                   // IS_IPV6
	UDPMode               bool                   // UDP_MODE
	Gateway               string                 // NET_GATEWAY
	Netmask               int                    // NET_NETMASK
	BindManagementAll     bool                   // BIND_MANAGEMENT_ALL
	NvidiaVFSingleIP      *bool                  // NVIDIA_VF_SINGLE_IP; nil means leave unset
}

func parseNetwork(e Env) (Network, error) {
	selectors, err := parseSelectors(e, "NETWORK_SELECTORS")
	if err != nil {
		return Network{}, err
	}
	mgmtSelectors, err := parseSelectors(e, "MANAGEMENT_IPS_SELECTORS")
	if err != nil {
		return Network{}, err
	}
	netmask, err := parseIntStrict(e, "NET_NETMASK", 0)
	if err != nil {
		return Network{}, err
	}
	return Network{
		Device:                e.Get("NETWORK_DEVICE"),
		Subnets:               parseCSV(e, "SUBNETS"),
		Selectors:             selectors,
		ManagementIPSelectors: mgmtSelectors,
		ManagementIP:          e.Get("MANAGEMENT_IP"),
		JoinIPs:               parseCSV(e, "JOIN_IPS"),
		IsIPv6:                parseExactBool(e, "IS_IPV6"),
		UDPMode:               parseExactBool(e, "UDP_MODE"),
		Gateway:               e.Get("NET_GATEWAY"),
		Netmask:               netmask,
		BindManagementAll:     parseNotFalse(e, "BIND_MANAGEMENT_ALL"),
		NvidiaVFSingleIP:      parseOptionalFoldBool(e, "NVIDIA_VF_SINGLE_IP"),
	}, nil
}

// Ports holds the requested Weka and agent ports; zero means unresolved.
type Ports struct {
	Weka  int // PORT
	Agent int // AGENT_PORT
}

func parsePorts(e Env) Ports {
	return Ports{
		Weka:  parsePortPermissive(e, "PORT"),
		Agent: parsePortPermissive(e, "AGENT_PORT"),
	}
}

// ClientPorts holds the client-mode allocation window.
type ClientPorts struct {
	Base  int // BASE_PORT
	Range int // PORT_RANGE, 0 means up to 65535
}

func parseClientPorts(e Env) (ClientPorts, error) {
	rng, err := parseIntStrict(e, "PORT_RANGE", 0)
	if err != nil {
		return ClientPorts{}, err
	}
	return ClientPorts{Base: parsePortPermissive(e, "BASE_PORT"), Range: rng}, nil
}

// Agent holds weka-agent process settings.
type Agent struct {
	ImageName       string // IMAGE_NAME
	TargetImageName string // TARGET_IMAGE_NAME
	SyslogPackage   string // SYSLOG_PACKAGE, default "auto"
	NoReserveSpace  bool   // NO_RESERVE_SPACE
	EnvoyEpoch      string // envoy_restart_epoch; empty means use the current unix time
}

func parseAgent(e Env) Agent {
	syslog, ok := e.Lookup("SYSLOG_PACKAGE")
	if !ok {
		syslog = "auto"
	}
	return Agent{
		ImageName:       e.Get("IMAGE_NAME"),
		TargetImageName: e.Get("TARGET_IMAGE_NAME"),
		SyslogPackage:   syslog,
		NoReserveSpace:  parseExactBool(e, "NO_RESERVE_SPACE"),
		EnvoyEpoch:      e.Get("envoy_restart_epoch"),
	}
}

// Persistence holds the on-disk persistence mode and container identity.
type Persistence struct {
	Mode        string // WEKA_PERSISTENCE_MODE, default "local"
	ContainerID string // WEKA_CONTAINER_ID
}

func parsePersistence(e Env) Persistence {
	mode, ok := e.Lookup("WEKA_PERSISTENCE_MODE")
	if !ok {
		mode = "local"
	}
	return Persistence{Mode: mode, ContainerID: e.Get("WEKA_CONTAINER_ID")}
}

// Traces holds dumper configuration inputs.
type Traces struct {
	DumperConfigMode  string // DUMPER_CONFIG_MODE, default "auto"
	MaxCapacityGB     int    // MAX_TRACE_CAPACITY_GB, default 10
	EnsureFreeSpaceGB int    // ENSURE_FREE_SPACE_GB, default 20
}

func parseTraces(e Env) (Traces, error) {
	mode, ok := e.Lookup("DUMPER_CONFIG_MODE")
	if !ok {
		mode = "auto"
	}
	maxCap, err := parseIntStrict(e, "MAX_TRACE_CAPACITY_GB", 10)
	if err != nil {
		return Traces{}, err
	}
	freeSpace, err := parseIntStrict(e, "ENSURE_FREE_SPACE_GB", 20)
	if err != nil {
		return Traces{}, err
	}
	return Traces{DumperConfigMode: mode, MaxCapacityGB: maxCap, EnsureFreeSpaceGB: freeSpace}, nil
}

// Host holds COS hugepage and driver-signing settings.
type Host struct {
	AllowHugepageConfig    bool   // WEKA_COS_ALLOW_HUGEPAGE_CONFIG
	AllowDisableDriverSign bool   // WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING
	GlobalHugepageSize     string // WEKA_COS_GLOBAL_HUGEPAGE_SIZE, default "2M"
	GlobalHugepageCount    int    // WEKA_COS_GLOBAL_HUGEPAGE_COUNT, default 4000
}

func parseHost(e Env) (Host, error) {
	size, ok := e.Lookup("WEKA_COS_GLOBAL_HUGEPAGE_SIZE")
	if !ok {
		size = "2M"
	} else {
		size = strings.ToLower(size)
	}
	count, err := parseIntStrict(e, "WEKA_COS_GLOBAL_HUGEPAGE_COUNT", 4000)
	if err != nil {
		return Host{}, err
	}
	return Host{
		AllowHugepageConfig:    parseExactBool(e, "WEKA_COS_ALLOW_HUGEPAGE_CONFIG"),
		AllowDisableDriverSign: parseExactBool(e, "WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING"),
		GlobalHugepageSize:     size,
		GlobalHugepageCount:    count,
	}, nil
}

// Drivers holds driver image and distribution settings.
type Drivers struct {
	ImageName       string // IMAGE_NAME
	TargetImageName string // TARGET_IMAGE_NAME
	DistService     string // DIST_SERVICE
	BuildID         string // DRIVERS_BUILD_ID
}

func parseDrivers(e Env) Drivers {
	return Drivers{
		ImageName:       e.Get("IMAGE_NAME"),
		TargetImageName: e.Get("TARGET_IMAGE_NAME"),
		DistService:     e.Get("DIST_SERVICE"),
		BuildID:         e.Get("DRIVERS_BUILD_ID"),
	}
}

// Operation carries the raw instruction envelope; payloads decode at the consuming operation.
type Operation struct {
	Raw  string // INSTRUCTIONS, verbatim
	Type weka.InstructionType
}

// parseOperation decodes only the envelope's type field. A malformed envelope is an error
// here; whether that error is fatal depends on the family (only Operation-carrying ones fail).
func parseOperation(e Env) (Operation, error) {
	raw := e.Get("INSTRUCTIONS")
	op := Operation{Raw: raw}
	if raw == "" {
		return op, nil
	}
	var envelope struct {
		Type weka.InstructionType `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return Operation{}, fmt.Errorf("INSTRUCTIONS: %w", err)
	}
	op.Type = envelope.Type
	return op, nil
}

// AWS holds IRSA inputs used only by the ensure-nics container operation.
type AWS struct {
	RoleARN              string // AWS_ROLE_ARN
	WebIdentityTokenFile string // AWS_WEB_IDENTITY_TOKEN_FILE
	Region               string // AWS_REGION
	DefaultRegion        string // AWS_DEFAULT_REGION
}

func parseAWS(e Env) AWS {
	return AWS{
		RoleARN:              e.Get("AWS_ROLE_ARN"),
		WebIdentityTokenFile: e.Get("AWS_WEB_IDENTITY_TOKEN_FILE"),
		Region:               e.Get("AWS_REGION"),
		DefaultRegion:        e.Get("AWS_DEFAULT_REGION"),
	}
}

// Observability holds OTEL exporter settings and pod/node attributes.
type Observability struct {
	Endpoint       string // OTEL_EXPORTER_OTLP_ENDPOINT
	LogsEndpoint   string // OTEL_EXPORTER_OTLP_LOGS_ENDPOINT
	Headers        string // OTEL_EXPORTER_OTLP_HEADERS
	LogsHeaders    string // OTEL_EXPORTER_OTLP_LOGS_HEADERS
	ServiceName    string // OTEL_SERVICE_NAME
	ServiceVersion string // OTEL_SERVICE_VERSION
	LogsEnabled    bool   // OTEL_LOGS_ENABLED, default true
}

func parseObservability(e Env) Observability {
	return Observability{
		Endpoint:       e.Get("OTEL_EXPORTER_OTLP_ENDPOINT"),
		LogsEndpoint:   e.Get("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"),
		Headers:        e.Get("OTEL_EXPORTER_OTLP_HEADERS"),
		LogsHeaders:    e.Get("OTEL_EXPORTER_OTLP_LOGS_HEADERS"),
		ServiceName:    e.Get("OTEL_SERVICE_NAME"),
		ServiceVersion: e.Get("OTEL_SERVICE_VERSION"),
		LogsEnabled:    parseFoldBool(e, "OTEL_LOGS_ENABLED", true),
	}
}

// Results holds the results destination, preserving the existing Go path override.
type Results struct {
	Path string // WEKA_RUNTIME_RESULTS_PATH, default "/weka-runtime/results.json"
}

func parseResults(e Env) Results {
	path := e.Get("WEKA_RUNTIME_RESULTS_PATH")
	if path == "" {
		path = "/weka-runtime/results.json"
	}
	return Results{Path: path}
}
