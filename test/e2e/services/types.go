package services

import "context"

type Jobless interface {
	EnsureDeployment(ctx context.Context, params ProvisionParams) (*Deployment, error)
	DeleteDeployment(ctx context.Context, clusterName string) error

	Install(ctx context.Context, params InstallParams) (*Installation, error)
	GetKubeConfig(clusterName string) (string, error)
}

type ProvisionParams interface {
	GetTemplate() string
	GetSubnetID() string
	GetRegion() string
	GetSecurityGroups() []string
	GetAmiID() string
	GetKeyPairName() string
	GetClusterName() string
	IsDualStack() bool
}

type InstallParams interface {
	GetClusterName() string
	GetQuayUsername() string
	GetQuayPassword() string
	HasCsi() bool
}

type Deployment struct {
	Cloud       string `json:"cloud"`
	ClusterName string `json:"cluster_name"`
	CreatedAt   string `json:"created_at"`
	UUID        string `json:"uuid"`
	Region      string `json:"region"`
	Template    string `json:"template"`
}

type Installation struct {
	KubeConfigPath string `json:"kube_config_path"`
}

func (d Deployment) GetClusterName() string {
	return d.ClusterName
}
