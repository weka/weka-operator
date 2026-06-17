package v1alpha1

type InstructionType string

const (
	// Operation instructions — dispatched to an adhoc-op WekaContainer.
	// These MUST equal the corresponding WekaManualOperationAction values.
	InstructionTypeSignDrives        InstructionType = opSignDrives
	InstructionTypeDiscoverDrives    InstructionType = opDiscoverDrives
	InstructionTypeForceResignDrives InstructionType = opForceResignDrives
	InstructionTypeEnsureNICs        InstructionType = opEnsureNICs

	// Internal instructions — lifecycle plumbing, not exposed via any CRD.
	InstructionTypeLoadDrivers             InstructionType = "load-drivers"
	InstructionCopyWekaFilesToDriverLoader InstructionType = "copy-weka-files-to-driver-loader"
	InstructionTypeFeatureFlagsUpdate      InstructionType = "feature-flags-update"
	InstructionTypeUmount                  InstructionType = "umount"
)
