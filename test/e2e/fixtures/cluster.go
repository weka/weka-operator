package fixtures

import (
	"context"
	"fmt"

	"github.com/weka/jobless/pkg/jobless"
	"github.com/weka/weka-operator/test/e2e/services"
	"k8s.io/apimachinery/pkg/types"
)

type Cluster struct {
	Name              string
	WekaClusterName   string
	OperatorNamespace string
	OperatorTemplate  string
	ProvisionTemplate string

	Jobless services.Jobless

	ProvisionParams *jobless.ProvisionParams
	InstallParams   *jobless.InstallParams

	Deployment   jobless.Deployment
	Installation *jobless.Installation

	Kubernetes services.Kubernetes
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
	k8s := services.NewKubernetes(jobless, clusterName)
	k8s.Setup(ctx)

	client, err := k8s.GetClient(ctx)
	if err != nil {
		return &ClusterError{
			Message: "failed to get client",
			Err:     err,
		}
	}
	if client == nil {
		return &ClusterError{
			Message: "expected client to be set, got nil",
			Err:     nil,
		}
	}

	c.Kubernetes = k8s

	return nil
}

func (c *Cluster) TeardownK8s(ctx context.Context) {
	c.Kubernetes.TearDown(ctx)
}

func (c *Cluster) GetKubeConfig() string {
	return ""
}

func (c *Cluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Name:      c.WekaClusterName,
		Namespace: c.OperatorNamespace,
	}
}
