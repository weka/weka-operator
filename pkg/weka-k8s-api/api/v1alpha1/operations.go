package v1alpha1

// Canonical operation identifiers. Single source of truth for the values
// shared across WekaManualOperationAction, WekaPolicyType, and InstructionType.
const (
	opSignDrives              = "sign-drives"
	opDiscoverDrives          = "discover-drives"
	opForceResignDrives       = "force-resign-drives"
	opEnsureNICs              = "ensure-nics"
	opBlockDrives             = "block-drives"
	opUnblockDrives           = "unblock-drives"
	opRemoteTracesSession     = "remote-traces-session"
	opEnableLocalDriversDist  = "enable-local-drivers-distribution"
	opCleanStaleVirtualDrives = "clean-stale-virtual-drives"
	opRotateSsdProxy          = "rotate-ssdproxy"
)
