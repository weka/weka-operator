package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/weka/weka-operator/internal/app/manager/controllers/allocator"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	werrors "github.com/weka/weka-operator/internal/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"

	"go.opentelemetry.io/otel/codes"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CrdManager interface {
	GetClusterService(ctx context.Context, req ctrl.Request) (WekaClusterService, error)
	EnsureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error)
	GetOrInitAllocMap(ctx context.Context) (*domain.Allocations, *v1.ConfigMap, error)
	UpdateAllocationsConfigmap(ctx context.Context, allocations *domain.Allocations, configMap *v1.ConfigMap) error
	RefreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error)
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
		return nil, err
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
		// TODO: should've be just own step function
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
	if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) || len(cluster.Spec.ExpandEndpoints) != 0 {
		// TODO: Update-By-Expansion, cluster-side join-ips until there are own containers
		joinIps, err = GetJoinIps(ctx, r.getClient(), cluster)
		allowExpansion := false
		if err != nil {
			allowExpansion = strings.Contains(err.Error(), "No join IP port pairs found") || strings.Contains(err.Error(), "No compute containers found")
		}
		if err != nil && len(cluster.Spec.ExpandEndpoints) != 0 && allowExpansion { // TO
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

func (r *crdManager) RefreshContainer(ctx context.Context, req ctrl.Request) (*wekav1alpha1.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "refreshContainer")
	defer end()

	container := &wekav1alpha1.WekaContainer{}
	if err := r.getClient().Get(ctx, req.NamespacedName, container); err != nil {
		if apierrors.IsNotFound(err) {
			err = &werrors.NotFoundError{
				WrappedError: werrors.WrappedError{Err: err},
			}
		}
		return nil, &ContainerRefreshError{
			WrappedError: werrors.WrappedError{Err: err},
			Name:         req.Name,
		}
	}
	logger.SetStatus(codes.Ok, "Container refreshed")
	return container, nil
}

func (r *crdManager) getClient() client.Client {
	return r.Manager.GetClient()
}

// Errors ----------------------------------------------------------------------
type ContainerRefreshError struct {
	werrors.WrappedError
	Name string
}

func (e *ContainerRefreshError) Error() string {
	return fmt.Sprintf("Error refreshing container: %s", e.WrappedError.Error())
}
