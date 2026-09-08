package consts

// Event actions for wekacontainer reconciliation steps.
const (
	ActionValidateNetworkConfig  = "ValidateNetworkConfig"
	ActionAllocateResources      = "AllocateResources"
	ActionManageDrives           = "ManageDrives"
	ActionManageDrivers          = "ManageDrivers"
	ActionUpgrade                = "Upgrade"
	ActionReconcile              = "Reconcile"
	ActionManageTelemetry        = "ManageTelemetry"
	ActionScheduleContainers     = "ScheduleContainers"
	ActionMonitorContainerHealth = "MonitorContainerHealth"
	ActionDrainContainer         = "DrainContainer"
	ActionManageOneOffOperation  = "ManageOneOffOperation"
	ActionSignDrives             = "SignDrives"
)

// Event actions shared across wekacluster and wekacontainer capacity planning.
const (
	ActionApplyCapacityGrowth = "ApplyCapacityGrowth"
	ActionPlanCapacity        = "PlanCapacity"
)

// Event actions for wekacluster reconciliation steps.
const (
	ActionJoinSmbwDomain           = "JoinSmbwDomain"
	ActionFormCluster              = "FormCluster"
	ActionAllocateClusterRange     = "AllocateClusterRange"
	ActionResolveDriversDist       = "ResolveDriversDist"
	ActionBuildContainers          = "BuildContainers"
	ActionEnsureAwsTerminationHook = "EnsureAwsTerminationHook"
	ActionDeleteCluster            = "DeleteCluster"
)

// ActionValidateClientVersion is used by wekaclient reconciliation.
const ActionValidateClientVersion = "ValidateClientVersion"

// Event actions for drive-management operations (internal/controllers/operations).
const (
	ActionApplyDriveTypeOverrides = "ApplyDriveTypeOverrides"
	ActionRotateSsdProxy          = "RotateSsdProxy"
	ActionCleanStaleVirtualDrives = "CleanStaleVirtualDrives"
)
