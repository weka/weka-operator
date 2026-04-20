package fixtures

import (
	"context"

	"github.com/weka/weka-operator/test/services"
)

type Cluster struct {
	Name              string
	WekaClusterName   string
	OperatorNamespace string

	Kubernetes services.Kubernetes
}

func (c *Cluster) SetupK8s(ctx context.Context) error {
	clusterName := c.WekaClusterName
	k8s := services.NewKubernetes(clusterName)

	c.Kubernetes = k8s

	return nil
}

