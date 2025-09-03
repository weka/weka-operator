package v1alpha1

type InstructionType string

const (
	InstructionTypeLoadDrivers             = "load-drivers"
	InstructionCopyWekaFilesToDriverLoader = "copy-weka-files-to-driver-loader"
)
