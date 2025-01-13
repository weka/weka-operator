package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// make this service globally available
var ClustersJoinIps ClustersJoinIpsService

func init() {
	ClustersJoinIps = NewClustersJoinIpsService()
}

type GetJoinIpsError struct {
	ClusterName string
	Namespace   string
	Issue       string
}

func (e *GetJoinIpsError) Error() string {
	return fmt.Sprintf("failed to get join ips for cluster %s: %s", e.ClusterName, e.Issue)
}

type JoinIpsResult struct {
	JoinIps   []string
	UpdatedAt time.Time
}

// ClustersJoinIpsService is a service that keeps cached join ips for clusters
type ClustersJoinIpsService interface {
	// GetJoinIps returns the cached join ips for the cluster
	// If the cache is expired or the cluster is not found, the GetJoinIpsError is returned
	GetJoinIps(ctx context.Context, clusterName, namespace string) ([]string, error)
	// RefreshJoinIps refreshes the join ips for the cluster and caches them
	// NOTE: refresh is recommended to be done only by WekaClusterReconciler
	RefreshJoinIps(ctx context.Context, containers []*weka.WekaContainer, cluster *weka.WekaCluster) error
}

type clustersJoinIpsService struct {
	// map of <cluster_name>-<namespace> to join ips result
	clusterJoinIps map[string]JoinIpsResult
	ipsCacheTTL    time.Duration
	lock           sync.RWMutex
}

func NewClustersJoinIpsService() ClustersJoinIpsService {
	cacheTtl := config.Consts.JoinIpsCacheTTL
	return &clustersJoinIpsService{
		ipsCacheTTL:    cacheTtl,
		clusterJoinIps: make(map[string]JoinIpsResult),
	}
}

func (s *clustersJoinIpsService) GetJoinIps(ctx context.Context, clusterName, namespace string) ([]string, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	key := s.getKey(clusterName, namespace)
	joinIpsResult, ok := s.clusterJoinIps[key]
	if !ok {
		err := &GetJoinIpsError{ClusterName: clusterName, Issue: "cluster not found", Namespace: namespace}
		return nil, err
	}

	if time.Since(joinIpsResult.UpdatedAt) > s.ipsCacheTTL {
		err := &GetJoinIpsError{ClusterName: clusterName, Issue: "cache expired", Namespace: namespace}
		return nil, err
	}

	return joinIpsResult.JoinIps, nil
}

func (s *clustersJoinIpsService) RefreshJoinIps(ctx context.Context, containers []*weka.WekaContainer, cluster *weka.WekaCluster) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "RefreshJoinIps")
	defer end()

	s.lock.Lock()
	defer s.lock.Unlock()

	joinIps, err := discovery.SelectJoinIps(containers, cluster.Spec.FailureDomainLabel)
	if err != nil {
		err = fmt.Errorf("failed to get cluster join ips: %w", err)
		return err
	}

	key := s.getKey(cluster.Name, cluster.Namespace)
	s.clusterJoinIps[key] = JoinIpsResult{
		JoinIps:   joinIps,
		UpdatedAt: time.Now(),
	}

	logger.Info("Refreshed cluster join ips", "join_ips", joinIps, "cluster", cluster.Name)
	return nil
}

func (s *clustersJoinIpsService) getKey(clusterName, namespace string) string {
	return fmt.Sprintf("%s-%s", clusterName, namespace)
}
