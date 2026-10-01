package config

// CoreConfig holds the Runtime, Identity, Observability, and Results sections common to every
// workflow family.
type CoreConfig struct {
	Runtime       Runtime
	Identity      Identity
	Observability Observability
	Results       Results
}

// parseCore builds the sections shared by every family.
func parseCore(e Env) (CoreConfig, error) {
	runtime, err := parseRuntime(e)
	if err != nil {
		return CoreConfig{}, err
	}
	return CoreConfig{
		Runtime:       runtime,
		Identity:      parseIdentity(e),
		Observability: parseObservability(e),
		Results:       parseResults(e),
	}, nil
}

// ContainerConfig is the configuration shared by every workflow that owns a Weka container.
// It deliberately excludes Operation, AWS, ClientPorts, Drivers, Host.
type ContainerConfig struct {
	CoreConfig
	CPU         CPU
	Memory      Memory
	Network     Network
	Ports       Ports
	Agent       Agent
	Persistence Persistence
	Traces      Traces
}

// ParseContainer builds the shared container sections. Used directly by families whose only
// addition over ContainerConfig is nothing: envoy, telemetry (Auxiliary).
func ParseContainer(e Env) (ContainerConfig, error) {
	core, err := parseCore(e)
	if err != nil {
		return ContainerConfig{}, err
	}
	cpu, err := parseCPU(e)
	if err != nil {
		return ContainerConfig{}, err
	}
	memory, err := parseMemory(e)
	if err != nil {
		return ContainerConfig{}, err
	}
	network, err := parseNetwork(e)
	if err != nil {
		return ContainerConfig{}, err
	}
	traces, err := parseTraces(e)
	if err != nil {
		return ContainerConfig{}, err
	}
	return ContainerConfig{
		CoreConfig:  core,
		CPU:         cpu,
		Memory:      memory,
		Network:     network,
		Ports:       parsePorts(e),
		Agent:       parseAgent(e),
		Persistence: parsePersistence(e),
		Traces:      traces,
	}, nil
}

// ClientConfig configures the client workflow.
type ClientConfig struct {
	ContainerConfig
	ClientPorts ClientPorts
}

// ParseClient builds a ClientConfig.
func ParseClient(e Env) (ClientConfig, error) {
	cc, err := ParseContainer(e)
	if err != nil {
		return ClientConfig{}, err
	}
	clientPorts, err := parseClientPorts(e)
	if err != nil {
		return ClientConfig{}, err
	}
	return ClientConfig{ContainerConfig: cc, ClientPorts: clientPorts}, nil
}

// ContainerOpConfig configures adhoc-op-with-container. Operation and AWS parsing stay here
// rather than in ParseContainer: no other container family needs them.
type ContainerOpConfig struct {
	ContainerConfig
	Operation Operation
	AWS       AWS
}

// ParseContainerOp builds a ContainerOpConfig. Unlike ParseContainer, a malformed
// INSTRUCTIONS envelope is fatal here because this family dispatches on Operation.Type.
func ParseContainerOp(e Env) (ContainerOpConfig, error) {
	cc, err := ParseContainer(e)
	if err != nil {
		return ContainerOpConfig{}, err
	}
	operation, err := parseOperation(e)
	if err != nil {
		return ContainerOpConfig{}, err
	}
	return ContainerOpConfig{ContainerConfig: cc, Operation: operation, AWS: parseAWS(e)}, nil
}

// AdhocConfig configures adhoc-op: host operations with no agent and no container sections.
type AdhocConfig struct {
	CoreConfig
	Network     Network
	Persistence Persistence
	Agent       Agent
	Operation   Operation
}

// ParseAdhoc builds an AdhocConfig. A malformed INSTRUCTIONS envelope is fatal, matching
// ParseContainerOp, since adhoc-op also dispatches on Operation.Type.
func ParseAdhoc(e Env) (AdhocConfig, error) {
	core, err := parseCore(e)
	if err != nil {
		return AdhocConfig{}, err
	}
	network, err := parseNetwork(e)
	if err != nil {
		return AdhocConfig{}, err
	}
	operation, err := parseOperation(e)
	if err != nil {
		return AdhocConfig{}, err
	}
	return AdhocConfig{
		CoreConfig:  core,
		Network:     network,
		Persistence: parsePersistence(e),
		Agent:       parseAgent(e),
		Operation:   operation,
	}, nil
}

// DriverBuilderConfig configures drivers-builder: build, publish, serve.
type DriverBuilderConfig struct {
	CoreConfig
	Drivers      Drivers
	PreRunScript string // PRE_RUN_SCRIPT
	ServePort    int    // PORT; unset/empty -> 60002, malformed -> error
}

// ParseDriverBuilder builds a DriverBuilderConfig. ServePort is the one place a bad PORT is
// fatal (py:4542 `int(PORT) if PORT else 60002`).
func ParseDriverBuilder(e Env) (DriverBuilderConfig, error) {
	core, err := parseCore(e)
	if err != nil {
		return DriverBuilderConfig{}, err
	}
	servePort, err := parseIntStrict(e, "PORT", 60002)
	if err != nil {
		return DriverBuilderConfig{}, err
	}
	return DriverBuilderConfig{
		CoreConfig:   core,
		Drivers:      parseDrivers(e),
		PreRunScript: e.Get("PRE_RUN_SCRIPT"),
		ServePort:    servePort,
	}, nil
}

// DiscoveryConfig configures the discovery workflow.
type DiscoveryConfig struct {
	CoreConfig
	Host Host
}

// ParseDiscovery builds a DiscoveryConfig.
func ParseDiscovery(e Env) (DiscoveryConfig, error) {
	core, err := parseCore(e)
	if err != nil {
		return DiscoveryConfig{}, err
	}
	host, err := parseHost(e)
	if err != nil {
		return DiscoveryConfig{}, err
	}
	return DiscoveryConfig{CoreConfig: core, Host: host}, nil
}

// DriverLoaderConfig configures the drivers-loader workflow.
type DriverLoaderConfig struct {
	CoreConfig
	Drivers Drivers
	Host    Host
}

// ParseDriverLoader builds a DriverLoaderConfig.
func ParseDriverLoader(e Env) (DriverLoaderConfig, error) {
	core, err := parseCore(e)
	if err != nil {
		return DriverLoaderConfig{}, err
	}
	host, err := parseHost(e)
	if err != nil {
		return DriverLoaderConfig{}, err
	}
	return DriverLoaderConfig{CoreConfig: core, Drivers: parseDrivers(e), Host: host}, nil
}

// ParseRuntimeSection reads only the process-level settings main needs before mode dispatch.
func ParseRuntimeSection(e Env) (Runtime, error) {
	return parseRuntime(e)
}
