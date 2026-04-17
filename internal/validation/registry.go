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
		&clusterNetworkEthdevice{},
		&clusterDriveComputeCoreRatio{},
	}
	WekaClient = []Validator{
		&clientTargetClusterExists{},
	}
)
