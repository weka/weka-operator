package services

import (
	"context"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"

	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AllocationService interface {
	GetOrInitAllocMap(ctx context.Context) (*domain.Allocations, *v1.ConfigMap, error)
	UpdateAllocationsConfigmap(ctx context.Context, allocations *domain.Allocations, configMap *v1.ConfigMap) error
}

func NewAllocationService(client client.Client) AllocationService {
	return &allocationService{
		Client: client,
	}
}

type allocationService struct {
	Client client.Client
}

func (a *allocationService) GetOrInitAllocMap(ctx context.Context) (*domain.Allocations, *v1.ConfigMap, error) {
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
	err = a.Client.Get(ctx, key, allocMapConfigMap)
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
		err = a.Client.Create(ctx, allocMapConfigMap)
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

func (a *allocationService) UpdateAllocationsConfigmap(ctx context.Context, allocations *domain.Allocations, configMap *v1.ConfigMap) error {

	if allocations == nil {
		return errors.New("allocations is nil")
	}
	if configMap == nil {
		return errors.New("configMap is nil")
	}
	yamlData, err := yaml.Marshal(&allocations)
	if err != nil {
		return err
	}
	configMap.Data["allocmap.yaml"] = string(yamlData)
	err = a.Client.Update(ctx, configMap)
	if err != nil {
		return err
	}
	return nil
}
