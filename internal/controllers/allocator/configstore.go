package allocator

import (
	"context"
	"sync"

	"github.com/weka/go-weka-observability/instrumentation"
	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/pkg/util"
)

const (
	configMapName    = "weka-operator-allocmap"
	configMapDataKey = "allocmap.yaml"
)

type AllocationsStore interface {
	GetAllocations(ctx context.Context) (*Allocations, error)
	UpdateAllocations(ctx context.Context, allocations *Allocations) error
	// RefreshAllocations forces a refresh from the backing store and returns fresh data
	RefreshAllocations(ctx context.Context) (*Allocations, error)
}

type ConfigMapStore struct {
	configMap   *v1.ConfigMap
	client      client.Client
	allocations *Allocations
	mu          sync.RWMutex
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

	key, err := getConfigMapKey()
	if err != nil {
		logger.Error(err, "Failed to get ConfigMap key")
		return nil, err
	}
	err = k8sClient.Get(ctx, key, allocMapConfigMap)
	if err != nil && apierrors.IsNotFound(err) {
		compressedYamlData, err := util.CompressBytes(yamlData)
		if err != nil {
			return nil, err
		}

		// Define a new ConfigMap
		allocMapConfigMap = &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: key.Namespace,
			},
			BinaryData: map[string][]byte{
				configMapDataKey: compressedYamlData,
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

		yamlData, err := util.DecompressBytes(allocMapConfigMap.BinaryData[configMapDataKey])
		if err != nil {
			return nil, err
		}

		err = yaml.Unmarshal(yamlData, allocations)
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

// getConfigMapKey returns the consistent key for the ConfigMap
func getConfigMapKey() (client.ObjectKey, error) {
	podNamespace, err := util.GetPodNamespace()
	if err != nil {
		return client.ObjectKey{}, err
	}
	return client.ObjectKey{Namespace: podNamespace, Name: configMapName}, nil
}

// refreshFromConfigMap fetches the latest data from ConfigMap
func (c *ConfigMapStore) refreshFromConfigMap(ctx context.Context) error {
	key, err := getConfigMapKey()
	if err != nil {
		return err
	}

	err = c.client.Get(ctx, key, c.configMap)
	if err != nil {
		return err
	}

	// Always refresh - decompress and unmarshal the latest data
	yamlData, err := util.DecompressBytes(c.configMap.BinaryData[configMapDataKey])
	if err != nil {
		return err
	}

	allocations := InitAllocationsMap()
	err = yaml.Unmarshal(yamlData, allocations)
	if err != nil {
		return err
	}

	// Update cached data
	c.allocations = allocations

	return nil
}

func (c *ConfigMapStore) GetAllocations(ctx context.Context) (*Allocations, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return cached data without any API calls for fast access
	return c.allocations, nil
}

func (c *ConfigMapStore) RefreshAllocations(ctx context.Context) (*Allocations, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Force refresh from ConfigMap regardless of ResourceVersion
	err := c.refreshFromConfigMap(ctx)
	if err != nil {
		return nil, err
	}

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
	c.mu.Lock()
	defer c.mu.Unlock()

	// Marshal and compress the allocations
	yamlData, err := yaml.Marshal(allocations)
	if err != nil {
		return err
	}
	// Compress and store as binary(!), we are limited by just 1MiB
	compressedYamlData, err := util.CompressBytes(yamlData)
	if err != nil {
		return err
	}

	// Update the ConfigMap data
	c.configMap.BinaryData[configMapDataKey] = compressedYamlData

	// Let Kubernetes handle optimistic concurrency control
	// If the ResourceVersion is outdated, k8s will return a conflict error
	err = c.client.Update(ctx, c.configMap)
	if err != nil {
		return err
	}

	// Update cache after successful write
	c.allocations = allocations

	return nil
}

type InMemoryConfigStore struct {
	allocations *Allocations
	mu          sync.RWMutex
}

func NewInMemoryConfigStore() *InMemoryConfigStore {
	return &InMemoryConfigStore{
		allocations: InitAllocationsMap(),
	}
}

func (i *InMemoryConfigStore) GetAllocations(ctx context.Context) (*Allocations, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.allocations == nil {
		panic("allocations not initialized")
	}
	return i.allocations, nil
}

func (i *InMemoryConfigStore) UpdateAllocations(ctx context.Context, allocations *Allocations) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.allocations = allocations
	return nil
}

func (i *InMemoryConfigStore) RefreshAllocations(ctx context.Context) (*Allocations, error) {
	// In-memory store doesn't need refresh, just return current allocations
	return i.GetAllocations(ctx)
}
