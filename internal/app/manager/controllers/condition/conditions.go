package condition

const (
	CondPodsCreated           = "PodsCreated"
	CondClusterSecretsCreated = "ClusterSecretsCreated"
	CondClusterSecretsApplied = "ClusterSecretsApplied"
	CondDefaultFsCreated      = "CondDefaultFsCreated"
	CondS3ClusterCreated      = "CondS3ClusterCreated" //not pre-set on purpose, as optional
	CondPodsReady             = "PodsReady"
	CondClusterCreated        = "ClusterCreated"
	CondDrivesAdded           = "DrivesAdded"
	CondIoStarted             = "IoStarted"
	CondJoinedCluster         = "JoinedCluster"
	CondEnsureDrivers         = "EnsuredDrivers"
)
