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

func (p ProvisionParams) GetTemplate() string         { return p.Template }
func (p ProvisionParams) GetSubnetID() string         { return p.AwsParams.SubnetId }
func (p ProvisionParams) GetRegion() string           { return p.AwsParams.Region }
func (p ProvisionParams) GetSecurityGroups() []string { return p.AwsParams.SecurityGroups }
func (p ProvisionParams) GetAmiID() string            { return p.AwsParams.AmiId }
func (p ProvisionParams) GetKeyPairName() string      { return p.AwsParams.KeyPairName }
func (p ProvisionParams) GetClusterName() string      { return p.ClusterName }
func (p ProvisionParams) IsDualStack() bool           { return false }

type InstallParams struct {
	ClusterName     string
	NoCsi           bool
	QuayUsername    string
	QuayPassword    string
	WekaImage       string
	OperatorVersion string
}

func (p InstallParams) GetClusterName() string  { return p.ClusterName }
func (p InstallParams) GetQuayUsername() string { return p.QuayUsername }
func (p InstallParams) GetQuayPassword() string { return p.QuayPassword }
func (p InstallParams) GetWekaImage() string    { return p.WekaImage }
func (p InstallParams) HasCsi() bool            { return !p.NoCsi }
