package domain

import (
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
)

type SizeClass struct {
	ContainerCount int
	DriveCount     int
}

var SizeClasses = map[string]SizeClass{
	"dev":    {1, 1},
	"small":  {3, 3},
	"medium": {5, 5},
	"large":  {7, 7},
}

type InsufficientNodesError struct{}

func (e *InsufficientNodesError) Error() string {
	return "insufficient nodes"
}

type InvalildSizeClassError struct{}

func (e *InvalildSizeClassError) Error() string {
	return "invalid size class"
}

type Scheduling struct {
	cluster  *wekav1alpha1.Cluster
	nodePool []v1.Node
}

func ForCluster(cluster *wekav1alpha1.Cluster, nodePool []v1.Node) *Scheduling {
	return &Scheduling{
		cluster:  cluster,
		nodePool: nodePool,
	}
}

func (s *Scheduling) AssignBackends() error {
	sizeClass, ok := SizeClasses[s.cluster.Spec.SizeClass]
	if !ok {
		return &InvalildSizeClassError{}
	}

	if len(s.nodePool) < sizeClass.ContainerCount {
		return &InsufficientNodesError{}
	}

	candidatePool := []v1.Node{}
	for i, node := range s.nodePool {
		if i >= sizeClass.ContainerCount {
			break
		}

		if node.Labels["weka.io/cluster"] != "" {
			continue
		}
		if node.Labels["weka.io/role"] != "backend" {
			continue
		}

		candidatePool = append(candidatePool, node)
	}

	if len(candidatePool) < sizeClass.ContainerCount {
		return &InsufficientNodesError{}
	}

	for _, node := range candidatePool {
		s.cluster.Status.Nodes = append(s.cluster.Status.Nodes, node.Name)
	}

	return nil
}

func (s *Scheduling) Cluster() *wekav1alpha1.Cluster {
	return s.cluster
}
