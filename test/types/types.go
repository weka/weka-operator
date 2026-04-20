package types

type AwsParams struct {
	Region         string
	VpcId          string
	SubnetId       string
	SecurityGroups []string
	AmiId          string
	KeyPairName    string
}

type ProvisionParams struct {
	ClusterName string
	Template    string
	AwsParams   AwsParams
}

type InstallParams struct {
	ClusterName     string
	NoCsi           bool
	QuayUsername    string
	QuayPassword    string
	WekaImage       string
	OperatorVersion string
}
