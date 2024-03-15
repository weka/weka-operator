package domain

import (
	"fmt"

	"github.com/go-logr/logr"
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

type InsufficientNodesError struct {
	Wanted int
	Found  int
}

func (e *InsufficientNodesError) Error() string {
	return fmt.Sprintf("insufficient nodes, wanted %d, found %d", e.Wanted, e.Found)
}

type InvalildSizeClassError struct{}

func (e *InvalildSizeClassError) Error() string {
	return "invalid size class"
}

type InsufficientDrivesError struct{}

func (e *InsufficientDrivesError) Error() string {
	return "insufficient drives"
}

type BackendDriveAssignmentsNotReadyError struct {
	Backend *wekav1alpha1.Backend
	Reason  string
}

func (e BackendDriveAssignmentsNotReadyError) Error() string {
	return fmt.Sprintf("drive assignments not ready for backend %s, reason %s", e.Backend.Name, e.Reason)
}

type Scheduling struct {
	cluster  *wekav1alpha1.Cluster
	backends []*wekav1alpha1.Backend
	Logger   logr.Logger
}

func ForCluster(cluster *wekav1alpha1.Cluster, backends []*wekav1alpha1.Backend, logger logr.Logger) (*Scheduling, error) {
	for _, backend := range backends {
		if backend.Status.DriveAssignments == nil {
			return nil, &BackendDriveAssignmentsNotReadyError{Backend: backend, Reason: "nil"}
		}
		if len(backend.Status.DriveAssignments) == 0 {
			return nil, &BackendDriveAssignmentsNotReadyError{Backend: backend, Reason: "empty"}
		}
	}

	return &Scheduling{
		cluster:  cluster,
		backends: backends,
		Logger:   logger.WithName("scheduling"),
	}, nil
}

func (s *Scheduling) AssignBackends(container *v1.LocalObjectReference) error {
	logger := s.Logger.WithName("AssignBackends")
	logger.Info("container", "container", container.Name)
	containerCount, err := s.ContainerCount()
	if err != nil {
		return err
	}

	candidatePool := []*wekav1alpha1.Backend{}
	logger.Info("considering backends", "count", len(s.backends))
	for i := range s.backends { // use index to avoid copying
		logger.Info("backend", "name", s.backends[i].Name)
		node := s.backends[i]

		if !s.HasFreeDrives(node) {
			logger.Info("node does not have free drives", "node", node.Name)
			continue
		}

		logger.Info("added node to candidate pool", "node", node.Name)
		candidatePool = append(candidatePool, node)

		if len(candidatePool) >= containerCount {
			logger.Info("found enough nodes", "count", len(candidatePool))
			break
		}
	}

	if len(candidatePool) < containerCount {
		return &InsufficientNodesError{
			Wanted: containerCount,
			Found:  len(candidatePool),
		}
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
	logger := s.Logger.WithName("HasFreeDrives")
	logger.Info("node", "name", node.Name, "count", len(node.Status.DriveAssignments))
	for _, assignment := range node.Status.DriveAssignments {
		logger.Info("candidate assignment", "name", assignment.Name)
		if assignment.Name == "" {
			return true
		}
	}

	return false
}

func (s *Scheduling) AssignToDrive(backend *wekav1alpha1.Backend, container *v1.LocalObjectReference) error {
	for drive, assignment := range backend.Status.DriveAssignments {
		if assignment.Name == "" {
			s.Logger.Info("assigned container to drive", "container", container.Name, "drive", drive)
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
