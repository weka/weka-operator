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

type InsufficientDrivesError struct{}

func (e *InsufficientDrivesError) Error() string {
	return "insufficient drives"
}

type Scheduling struct {
	cluster  *wekav1alpha1.Cluster
	backends []*wekav1alpha1.Backend
}

func ForCluster(cluster *wekav1alpha1.Cluster, backends []*wekav1alpha1.Backend) *Scheduling {
	return &Scheduling{
		cluster:  cluster,
		backends: backends,
	}
}

func (s *Scheduling) AssignBackends(container *v1.LocalObjectReference) error {
	containerCount, err := s.ContainerCount()
	if err != nil {
		return err
	}

	candidatePool := []*wekav1alpha1.Backend{}
	for i := range s.backends { // use index to avoid copying
		node := s.backends[i]
		if i >= containerCount {
			break
		}

		if !s.HasFreeDrives(node) {
			continue
		}

		candidatePool = append(candidatePool, node)
	}

	if len(candidatePool) < containerCount {
		return &InsufficientNodesError{}
	}

	for i := range candidatePool { // use index to avoid copying
		// TODO: add to assignments
		backend := candidatePool[i]
		s.AssignToDrive(backend, container)
		s.cluster.Status.Backends = append(s.cluster.Status.Backends, candidatePool[i].Name)
	}

	return nil
}

func (s *Scheduling) HasFreeDrives(node *wekav1alpha1.Backend) bool {
	if node.Status.DriveCount <= 0 {
		return false
	}

	for _, assignment := range node.Status.DriveAssignments {
		if assignment.Name == "" {
			return true
		}
	}

	return false
}

func (s *Scheduling) AssignToDrive(backend *wekav1alpha1.Backend, container *v1.LocalObjectReference) error {
	for drive, assignment := range backend.Status.DriveAssignments {
		if assignment.Name == "" {
			backend.Status.DriveAssignments[drive] = container
			return nil
		}
	}
	return &InsufficientDrivesError{}
}

func (s *Scheduling) Cluster() *wekav1alpha1.Cluster {
	return s.cluster
}

func (s *Scheduling) Backends() []*wekav1alpha1.Backend {
	return s.backends
}

func (s *Scheduling) ContainerCount() (int, error) {
	sizeClass, ok := SizeClasses[s.cluster.Spec.SizeClass]
	if !ok {
		return 0, &InvalildSizeClassError{}
	}
	return sizeClass.ContainerCount, nil
}
