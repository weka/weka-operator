package services

import (
	"context"
	"fmt"
	"math/rand"
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
	JoinIpsByFD map[string][]string
	UpdatedAt   time.Time
}

func (r *JoinIpsResult) GetRandomJoinIps(num int) []string {
	rndm := rand.New(rand.NewSource(time.Now().Unix()))

	// from each FD, select a random join ip
	joinIps := make([]string, 0, num)
	// number of failure domains
	fdNum := len(r.JoinIpsByFD)
	// calculate how many ips to select from each FD (round up)
	ipsPerFD := (num + fdNum - 1) / fdNum

	for _, ips := range r.JoinIpsByFD {
		used := make(map[int]struct{})
		expectedIps := ipsPerFD
		if len(ips) <= ipsPerFD {
			expectedIps = len(ips)
		}
		for i := 0; i < expectedIps && len(joinIps) < num; i++ {
			// select a random ip
			rndmIndex := rndm.Intn(len(ips))
			for _, ok := used[rndmIndex]; ok; _, ok = used[rndmIndex] {
				rndmIndex = rndm.Intn(len(ips))
			}
			used[rndmIndex] = struct{}{}
			joinIps = append(joinIps, ips[rndmIndex])
		}
	}

	return joinIps
}

// ClustersJoinIpsService is a service that keeps cached join ips for clusters
type ClustersJoinIpsService interface {
	// Check if the cached join ips exist and are valid
	JoinIpsAreValid(ctx context.Context, clusterName, namespace string) (bool, error)
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

func (s *clustersJoinIpsService) JoinIpsAreValid(ctx context.Context, clusterName, namespace string) (bool, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	key := s.getKey(clusterName, namespace)
	joinIpsResult, ok := s.clusterJoinIps[key]
	if !ok {
		err := &GetJoinIpsError{ClusterName: clusterName, Issue: "cluster not found", Namespace: namespace}
		return false, err
	}

	if time.Since(joinIpsResult.UpdatedAt) > s.ipsCacheTTL {
		err := &GetJoinIpsError{ClusterName: clusterName, Issue: "cache expired", Namespace: namespace}
		return false, err
	}
	return true, nil
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

	return joinIpsResult.GetRandomJoinIps(5), nil
}

func (s *clustersJoinIpsService) RefreshJoinIps(ctx context.Context, containers []*weka.WekaContainer, cluster *weka.WekaCluster) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "RefreshJoinIps")
	defer end()

	s.lock.Lock()
	defer s.lock.Unlock()

	joinIpsByFD, err := discovery.SelectJoinIps(containers)
	if err != nil {
		err = fmt.Errorf("failed to get cluster join ips: %w", err)
		return err
	}

	key := s.getKey(cluster.Name, cluster.Namespace)
	s.clusterJoinIps[key] = JoinIpsResult{
		JoinIpsByFD: joinIpsByFD,
		UpdatedAt:   time.Now(),
	}

	logger.Info("Refreshed cluster join ips", "cluster", cluster.Name)
	return nil
}

func (s *clustersJoinIpsService) getKey(clusterName, namespace string) string {
	return fmt.Sprintf("%s-%s", clusterName, namespace)
}
