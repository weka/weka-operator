package services

import (
	"context"
	"fmt"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/app/manager/factory"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/codes"
	"gopkg.in/yaml.v2"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CrdManager interface {
	GetCluster(ctx context.Context, req ctrl.Request) (WekaClusterService, error)
	EnsureWekaContainers(ctx context.Context, cluster *wekav1alpha1.WekaCluster) ([]*wekav1alpha1.WekaContainer, error)

	GetOrInitAllocMap(ctx context.Context) (*domain.Allocations, *v1.ConfigMap, error)
	UpdateAllocationsConfigmap(ctx context.Context, allocations *domain.Allocations, configMap *v1.ConfigMap) error
}

func NewCrdManager(mgr ctrl.Manager) CrdManager {
	scheme := mgr.GetScheme()
	return &crdManager{
		Manager:              mgr,
		WekaContainerFactory: factory.NewWekaContainerFactory(scheme),
	}
}

type crdManager struct {
	Manager              ctrl.Manager
	WekaContainerFactory factory.WekaContainerFactory
}

func (r *crdManager) GetCluster(ctx context.Context, req ctrl.Request) (WekaClusterService, error) {
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

	allocations, allocConfigMap, err := r.GetOrInitAllocMap(ctx)
	if err != nil {
		logger.Error(err, "could not init allocmap")
		return nil, err
	}

	foundContainers := []*wekav1alpha1.WekaContainer{}
	template, ok := domain.WekaClusterTemplates[cluster.Spec.Template]
	if !ok {
		keys := make([]string, 0, len(domain.WekaClusterTemplates))
		for k := range domain.WekaClusterTemplates {
			keys = append(keys, k)
		}
		err := fmt.Errorf("Template not found")
		logger.Error(err, "Template not found", "template", cluster.Spec.Template, "keys", keys)
		return nil, err
	}
	topology_fn, ok := domain.Topologies[cluster.Spec.Topology]
	if !ok {
		keys := make([]string, 0, len(domain.Topologies))
		for k := range domain.Topologies {
			keys = append(keys, k)
		}
		err := fmt.Errorf("Topology not found")
		logger.Error(err, "Topology not found", "topology", cluster.Spec.Topology, "keys", keys)
		return nil, err
	}
	topology, err := topology_fn(ctx, r.getClient(), cluster.Spec.NodeSelector)
	allocator := domain.NewAllocator(topology)
	if err != nil {
		logger.Error(err, "Failed to get topology", "topology", cluster.Spec.Topology)
		return nil, err
	}
	allocations, err, changed := allocator.Allocate(
		ctx,
		domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
		template,
		allocations,
		cluster.Spec.Size)
	if err != nil {
		logger.Error(err, "Failed to allocate resources")
		return nil, err
	}
	if changed {
		if err := r.UpdateAllocationsConfigmap(ctx, allocations, allocConfigMap); err != nil {
			logger.Error(err, "Failed to update alloc map")
			return nil, err
		}
	}

	size := cluster.Spec.Size
	if size == 0 {
		size = 1
	}
	logger.InfoWithStatus(codes.Unset, "Ensuring containers")

	var joinIps []string

	created := false
	ensureContainers := func(role string, containersNum int) error {
		ctx, _, end := instrumentation.GetLogSpan(ctx, "ensureContainers", "role", role, "containersNum", containersNum)
		defer end()
		for i := 0; i < containersNum; i++ {
			ctx, logger, end := instrumentation.GetLogSpan(ctx, "ensureContainer", "index", i)
			// Check if the WekaContainer object exists
			owner := domain.Owner{
				OwnerCluster: domain.OwnerCluster{ClusterName: cluster.Name, Namespace: cluster.Namespace},
				Container:    fmt.Sprintf("%s%d", role, i),
				Role:         role,
			} // apparently need helper function with a role.

			ownedResources, _ := domain.GetOwnedResources(owner, allocations)
			wekaContainer, err := r.WekaContainerFactory.NewWekaContainerForWekaCluster(cluster, ownedResources, template, topology, role, i)
			if err != nil {
				logger.Error(err, "newWekaContainerForWekaCluster")
				end()
				return err
			}
			l := logger.WithValues("container_name", wekaContainer.Name)

			found := &wekav1alpha1.WekaContainer{}
			err = r.getClient().Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: wekaContainer.Name}, found)
			if err != nil && apierrors.IsNotFound(err) {
				// if we post cluster form should join existing cluster
				if meta.IsStatusConditionTrue(cluster.Status.Conditions, condition.CondClusterCreated) {
					if joinIps == nil {
						joinIps, err = GetJoinIps(ctx, r.getClient(), cluster)
						if err != nil {
							logger.Error(err, "Failed to get join ips")
							end()
							return err
						}
					}
					wekaContainer.Spec.JoinIps = joinIps
				}
				l.Info("Creating container")
				err = r.getClient().Create(ctx, wekaContainer)
				if err != nil {
					logger.Error(err, "Failed to create WekaContainer")
					end()
					return err
				}
				created = true
				foundContainers = append(foundContainers, wekaContainer)
				l.Info("Container created")
			} else {
				foundContainers = append(foundContainers, found)
				l.Info("Container already exists")
			}
			end()
		}
		return nil
	}
	if err := ensureContainers("drive", template.DriveContainers); err != nil {
		logger.Error(err, "Failed to ensure drive containers")
		return nil, err
	}
	if err := ensureContainers("compute", template.ComputeContainers); err != nil {
		logger.Error(err, "Failed to ensure compute containers")
		return nil, err
	}

	if err := ensureContainers("s3", template.S3Containers); err != nil {
		logger.Error(err, "Failed to ensure S3 containers")
		return nil, err
	}

	if created {
		logger.InfoWithStatus(codes.Ok, "All cluster containers are created", "containers", len(foundContainers))
	} else {
		logger.SetStatus(codes.Ok, "All cluster containers already exist")
	}

	logger.InfoWithStatus(codes.Ok, "All cluster containers are created", "containers", len(foundContainers))
	return foundContainers, nil
}

func (r *crdManager) GetOrInitAllocMap(ctx context.Context) (*domain.Allocations, *v1.ConfigMap, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetOrInitAllocMap")
	defer end()
	// fetch alloc map from configmap
	allocations := &domain.Allocations{
		NodeMap: domain.AllocationsMap{},
	}
	allocMap := allocations.NodeMap
	yamlData, err := yaml.Marshal(&allocMap)
	if err != nil {
		logger.Error(err, "Failed to marshal alloc map")
		return nil, nil, err
	}

	allocMapConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Failed to get pod namespace")
		return nil, nil, err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: "weka-operator-allocmap"}
	err = r.getClient().Get(ctx, key, allocMapConfigMap)
	if err != nil && apierrors.IsNotFound(err) {
		// Define a new ConfigMap
		allocMapConfigMap = &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "weka-operator-allocmap",
				Namespace: podNamespace,
			},
			Data: map[string]string{
				"allocmap.yaml": string(yamlData),
			},
		}
		err = r.getClient().Create(ctx, allocMapConfigMap)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if err != nil {
			return nil, nil, err
		}
		err = yaml.Unmarshal([]byte(allocMapConfigMap.Data["allocmap.yaml"]), &allocations)
		if err != nil {
			return nil, nil, err
		}
	}
	return allocations, allocMapConfigMap, nil
}

func (r *crdManager) UpdateAllocationsConfigmap(ctx context.Context, allocations *domain.Allocations, configMap *v1.ConfigMap) error {
	yamlData, err := yaml.Marshal(&allocations)
	if err != nil {
		return err
	}
	configMap.Data["allocmap.yaml"] = string(yamlData)
	return r.getClient().Update(ctx, configMap)
}

func (r *crdManager) getClient() client.Client {
	return r.Manager.GetClient()
}
