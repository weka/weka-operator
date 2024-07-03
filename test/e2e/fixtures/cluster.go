package fixtures

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/test/e2e/services"
	"github.com/weka/weka-operator/test/e2e/types"
	ktypes "k8s.io/apimachinery/pkg/types"
)

type Cluster struct {
	Name              string
	WekaClusterName   string
	OperatorNamespace string
	OperatorTemplate  string
	ProvisionTemplate string

	Jobless         services.Jobless
	ProvisionParams *types.ProvisionParams
	InstallParams   *types.InstallParams

	Deployment   *services.Deployment
	Installation *services.Installation

	Kubernetes services.Kubernetes
}

func NewCluster(name, wekaClusterName, operatorNamespace, operatorTemplate, provisionTemplate string, jobless services.Jobless, provisionParams *types.ProvisionParams, installParams *types.InstallParams) *Cluster {
	return &Cluster{
		Name:              name,
		WekaClusterName:   wekaClusterName,
		OperatorNamespace: operatorNamespace,
		OperatorTemplate:  operatorTemplate,
		ProvisionTemplate: provisionTemplate,

		Jobless: jobless,

		ProvisionParams: provisionParams,

		InstallParams: installParams,
	}
}

type ClusterError struct {
	Message string
	Err     error
}

func (e *ClusterError) Error() string {
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (c *Cluster) SetupK8s(ctx context.Context) error {
	clusterName := c.Name
	jobless := c.Jobless
	kubeconfig := c.GetKubeConfig()
	k8s := services.NewKubernetes(jobless, clusterName, kubeconfig)

	c.Kubernetes = k8s

	return nil
}

func (c *Cluster) TeardownK8s(ctx context.Context) {
}

func (c *Cluster) GetKubeConfig() string {
	return c.Installation.KubeConfigPath
}

func (c *Cluster) NamespacedName() ktypes.NamespacedName {
	return ktypes.NamespacedName{
		Name:      c.WekaClusterName,
		Namespace: c.OperatorNamespace,
	}
}
