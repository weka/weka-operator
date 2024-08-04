package allocator

import (
	"context"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/pkg/util"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AllocationsStore interface {
	GetAllocations(ctx context.Context) (*Allocations, error)
	UpdateAllocations(ctx context.Context, allocations *Allocations) error
}

type ConfigMapStore struct {
	configMap   *v1.ConfigMap
	client      client.Client
	allocations *Allocations
}

func NewConfigMapStore(ctx context.Context, k8sClient client.Client) (AllocationsStore, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetOrInitAllocMap")
	defer end()
	// fetch alloc map from configmap
	allocations := InitAllocationsMap()
	allocMap := allocations.NodeMap
	yamlData, err := yaml.Marshal(&allocMap)
	if err != nil {
		logger.Error(err, "Failed to marshal alloc map")
		return nil, err
	}

	allocMapConfigMap := &v1.ConfigMap{}
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		logger.Error(err, "Failed to get pod namespace")
		return nil, err
	}
	key := client.ObjectKey{Namespace: podNamespace, Name: "weka-operator-allocmap"}
	err = k8sClient.Get(ctx, key, allocMapConfigMap)
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
		err = k8sClient.Create(ctx, allocMapConfigMap)
		if err != nil {
			return nil, err
		}
	} else {
		if err != nil {
			return nil, err
		}
		err = yaml.Unmarshal([]byte(allocMapConfigMap.Data["allocmap.yaml"]), &allocations)
		if err != nil {
			return nil, err
		}
	}

	configStore := &ConfigMapStore{
		configMap:   allocMapConfigMap,
		client:      k8sClient,
		allocations: allocations,
	}

	return configStore, nil
}

func (c *ConfigMapStore) GetAllocations(ctx context.Context) (*Allocations, error) {
	return c.allocations, nil
}

func InitAllocationsMap() *Allocations {
	return &Allocations{
		NodeMap: NodeAllocMap{},
		Global: GlobalAllocations{
			ClusterRanges:   map[OwnerCluster]Range{},
			AllocatedRanges: map[OwnerCluster]map[string]Range{},
		},
	}
}

func (c *ConfigMapStore) UpdateAllocations(ctx context.Context, allocations *Allocations) error {
	yamlData, err := yaml.Marshal(&allocations)
	if err != nil {
		return err
	}
	//TODO: Compress and store as binary(!), we are limited by just 1MiB
	c.configMap.Data["allocmap.yaml"] = string(yamlData)
	err = c.client.Update(ctx, c.configMap)
	if err != nil {
		return err
	}
	c.allocations = allocations
	return nil
}

type InMemoryConfigStore struct {
	allocations *Allocations
}

func NewInMemoryConfigStore() AllocationsStore {
	return &InMemoryConfigStore{
		allocations: InitAllocationsMap(),
	}
}

func (i InMemoryConfigStore) GetAllocations(ctx context.Context) (*Allocations, error) {
	if i.allocations == nil {
		panic("allocations not initialized")
	}
	return i.allocations, nil
}

func (i InMemoryConfigStore) UpdateAllocations(ctx context.Context, allocations *Allocations) error {
	i.allocations = allocations
	return nil
}
