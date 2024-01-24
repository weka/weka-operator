package state_machine

type State interface {
	Handle()
}

type Event interface {
	NextState(currentState State) State
}

type StateMachine struct {
	CurrentState State
}

func (sm *StateMachine) SetState(state State) {
	sm.CurrentState = state
}

func (sm *StateMachine) HandleEvent(event Event) {
	sm.CurrentState = event.NextState(sm.CurrentState)
}

// Operator States
//   - Starting: Event: OperatorStarted -> Idle
//   - Idle: Event: InstallClient -> InstallingClient
//   - Installing Client: Event: ClientInstalled -> Idle
type (
	OperatorStarting         struct{}
	OperatorIdle             struct{}
	OperatorInstallingClient struct{}
)

func (s OperatorStarting) Handle()         {}
func (s OperatorIdle) Handle()             {}
func (s OperatorInstallingClient) Handle() {}

type OperatorStarted struct{}

func (e OperatorStarted) NextState(currentState State) State {
	return OperatorIdle{}
}

type InstallClient struct{}

func (e InstallClient) NextState(currentState State) State {
	return OperatorInstallingClient{}
}

// Nested inside the Installing State are states where the drivers and
// daemonset are installed
//   - Installing wekafsgw
//   - Installing wekafsio
//   - Installing Agent Daemonset
type (
	InstallingWekafsgw struct{}
	InstallingWekafsio struct{}
	InstallingAgent    struct{}
)

// Driver Installation: These are the same for both drivers
//   - Create Module CRD Instance
//   - Download Cached Image if present
//   - Build Image if not present
//   - Install Driver
//   - Driver Ready
type (
	CreateModuleCRDInstance struct{}
	DownloadCachedImage     struct{}
	BuildImage              struct{}
	InstallDriver           struct{}
	DriverReady             struct{}
)

// Agent Installation
//   - Create Daemonset
//   - Daemonset Ready
//   - Capture/Generate API Key
//   - Capture Process List
//   - Capture Container List
//   - Agent is Ready
//   - Agent is Terminating
type (
	CreateDaemonset  struct{}
	DaemonsetReady   struct{}
	CaptureAPIKey    struct{}
	CaptureProcList  struct{}
	CaptureContList  struct{}
	AgentReady       struct{}
	AgentTerminating struct{}
)
