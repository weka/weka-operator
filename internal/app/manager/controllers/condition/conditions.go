package condition

const (
	CondPodsCreated                 = "PodsCreated"
	CondClusterSecretsCreated       = "ClusterSecretsCreated"
	CondClusterSecretsApplied       = "ClusterSecretsApplied"
	CondClusterClientSecretsCreated = "ClusterClientsSecretsCreated"
	CondClusterClientSecretsApplied = "ClusterClientsSecretsApplied"
	CondClusterCSISecretsCreated    = "ClusterCSIsSecretsCreated"
	CondClusterCSISecretsApplied    = "ClusterCSIsSecretsApplied"
	CondDefaultFsCreated            = "CondDefaultFsCreated"
	CondS3ClusterCreated            = "CondS3ClusterCreated" //not pre-set on purpose, as optional
	CondPodsReady                   = "PodsReady"
	CondClusterCreated              = "ClusterCreated"
	CondDrivesAdded                 = "DrivesAdded"
	CondIoStarted                   = "IoStarted"
	CondJoinedCluster               = "JoinedCluster"
	CondJoinedS3Cluster             = "JoinedS3Cluster"
	CondEnsureDrivers               = "EnsuredDrivers"
)
