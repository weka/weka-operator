package fixtures

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/test/services"
	ktypes "k8s.io/apimachinery/pkg/types"
)

type Cluster struct {
	Name              string
	WekaClusterName   string
	OperatorNamespace string

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
	clusterName := c.WekaClusterName
	k8s := services.NewKubernetes(clusterName)

	c.Kubernetes = k8s

	return nil
}

func (c *Cluster) TeardownK8s(ctx context.Context) {
}

func (c *Cluster) NamespacedName() ktypes.NamespacedName {
	return ktypes.NamespacedName{
		Name:      c.WekaClusterName,
		Namespace: c.OperatorNamespace,
	}
}
