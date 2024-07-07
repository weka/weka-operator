package services

import (
	"context"
	"fmt"
	"github.com/weka/weka-operator/internal/app/manager/controllers/allocator"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

type CrdManager interface {
	GetClusterService(ctx context.Context, req ctrl.Request) (WekaClusterService, error)
	EnsureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error)
}

func NewCrdManager(mgr ctrl.Manager) *crdManager {
	return &crdManager{
		Manager: mgr,
	}
}

type crdManager struct {
	Manager ctrl.Manager
}

func (r *crdManager) GetClusterService(ctx context.Context, req ctrl.Request) (WekaClusterService, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "FetchCluster")
	defer end()

	wekaCluster := &wekav1alpha1.WekaCluster{}
	err := r.getClient().Get(ctx, req.NamespacedName, wekaCluster)
	if err != nil {
		wekaCluster = nil
		if apierrors.IsNotFound(err) {
			err = nil
		}
	}

	wekaClusterService := NewWekaClusterService(r.Manager, wekaCluster)

	return wekaClusterService, err
}

func (r *crdManager) EnsureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "EnsureWekaContainers")
	defer end()

	template, ok := domain.GetTemplateByName(cluster.Spec.Template, *cluster)
	if !ok {
		keys := make([]string, 0, len(domain.WekaClusterTemplates))
		for k := range domain.WekaClusterTemplates {
			keys = append(keys, k)
		}
		err := fmt.Errorf("Template not found")
		logger.Error(err, "Template not found", "template", cluster.Spec.Template, "keys", keys)
		return nil, err
	}
	topologyFn, ok := domain.Topologies[cluster.Spec.Topology]
	if !ok {
		keys := make([]string, 0, len(domain.Topologies))
		for k := range domain.Topologies {
			keys = append(keys, k)
		}
		err := fmt.Errorf("Topology not found")
		logger.Error(err, "Topology not found", "topology", cluster.Spec.Topology, "keys", keys)
		return nil, err
	}
	topology, err := topologyFn(ctx, r.getClient(), cluster.Spec.NodeSelector)
	if err != nil {
		logger.Error(err, "Failed to get topology", "topology", cluster.Spec.Topology)
		return nil, err
	}
	topologyAllocator, err := allocator.NewTopologyAllocator(ctx, r.getClient(), topology)
	if err != nil {
		logger.Error(err, "Failed to create topology allocator")
		return nil, err
	}

	currentContainers := GetClusterContainers(ctx, r.getClient(), cluster, "")
	missingContainers, err := factory.BuildMissingContainers(cluster, template, topology, currentContainers)
	if err != nil {
		logger.Error(err, "Failed to create missing containers")
		return nil, err
	}
	for _, container := range missingContainers {
		if err := ctrl.SetControllerReference(cluster, container, r.Manager.GetScheme()); err != nil {
			return nil, err
		}
	}

	if len(missingContainers) == 0 {
		return currentContainers, nil
	}

	k8sClient := r.Manager.GetClient()
	if len(currentContainers) == 0 {
		logger.InfoWithStatus(codes.Unset, "Ensuring cluster-level allocation")
		//TODO: should've be just own step function
		err = topologyAllocator.AllocateClusterRange(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to allocate cluster range")
			return nil, err
		}
		err := k8sClient.Status().Update(ctx, cluster)
		if err != nil {
			logger.Error(err, "Failed to update cluster status")
			return nil, err
		}
		// update weka cluster status
	}

	err = topologyAllocator.AllocateContainers(ctx, *cluster, missingContainers)
	if err != nil {
		logger.Error(err, "Failed to allocate containers")
		return nil, err
	}

	logger.InfoWithStatus(codes.Unset, "Ensuring containers")

	var joinIps []string
	if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) {
		//TODO: Update-By-Expansion, cluster-side join-ips until there are own containers
		joinIps, err = GetJoinIps(ctx, r.getClient(), cluster)
		if err != nil && strings.Contains(err.Error(), "No join IP port pairs found") && len(cluster.Spec.ExpandEndpoints) != 0 { //TO
			joinIps = cluster.Spec.ExpandEndpoints
		} else {
			if err != nil {
				logger.Error(err, "Failed to get join ips")
				return nil, err
			}
		}
	}

	errs := []error{}

	allContainers := []*wekav1alpha1.WekaContainer{}

	for _, container := range missingContainers {
		if len(joinIps) != 0 {
			container.Spec.JoinIps = joinIps
		}
		err = r.getClient().Create(ctx, container)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		allContainers = append(allContainers, container)
	}
	allContainers = append(currentContainers, allContainers...)
	return allContainers, nil
}

func (r *crdManager) getClient() client.Client {
	return r.Manager.GetClient()
}
