package validation

// Per-CRD registries. Append a validator here to enable it. The
// admission defaults table (internal/admission/defaults.go) must have a
// matching row; ValidateRegistry() enforces this at startup.
var (
	WekaCluster = []Validator{
		&clusterMinDrivesFeasibility{},
		&clusterMinDrivesLargeFloor{},
		&clusterMinDrivesSmallAdvisory{},
		&clusterSelectedNodesCount{},
		&clusterDriversDistServiceExists{},
		&clusterCoresAvailable{},
		&clusterHugepagesAvailable{},
		&clusterSignedDrives{},
		&clusterDrivesUnsignedAdvisory{},
		&clusterNetworkEthdevice{},
		&clusterDriveComputeCoreRatio{},
		&clusterComputeDriveCoresFloor{},
		&clusterDriveCoresBelowCapacity{},
		&clusterNumDrivesBelowRequiredCores{},
		&clusterAutoFullDrivesPinExceedsNodeDrives{},
		&clusterAutoFullDrivesComputeHugepages{},
		&clusterAutoFullDrivesMinNodes{},
		&clusterCoresPerContainerLimit{},
		&clusterMinContainers{},
		&clusterDataservicesFeCores{},
		&clusterCapacityProtection{},
		&clusterCapacityMinDriveContainers{},
		&clusterCapacityChunkFeasibility{},
		&clusterSkipDefaultFs{},
	}
	WekaClient = []Validator{
		&clientTargetClusterExists{},
	}

	// Update-only registries: validators that require both old and new objects.
	WekaClusterUpdate = []UpdateValidator{
		&clusterCoresDecrease{},
		&clusterSizingModeFlip{},
	}
	WekaContainerUpdate = []UpdateValidator{
		&containerCoresDecrease{},
	}
	WekaClientUpdate = []UpdateValidator{
		&clientCoresDecrease{},
	}
)
