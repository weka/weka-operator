package services

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/pkg/errors"
	"github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/util"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DiscoveryTargetVersion = 1

type DiscoveryNodeInfo struct {
	IsHt             bool   `json:"is_ht"`
	KubernetesDistro string `json:"kubernetes_distro,omitempty"`
	Os               string `json:"os,omitempty"`
	OsBuildId        string `json:"os_build_id,omitempty"`
	BootID           string `json:"boot_id,omitempty"`
	Version          int
}

const discoveryAnnotation = "k8s.weka.io/discovery.json"

func (nodeInfo *DiscoveryNodeInfo) IsOpenshift() bool {
	return nodeInfo.KubernetesDistro == v1alpha1.OsNameOpenshift

}

func (nodeInfo *DiscoveryNodeInfo) IsCos() bool {
	return nodeInfo.KubernetesDistro == v1alpha1.OsNameCos
}

func (d *DiscoveryNodeInfo) GetHostsidePersistenceBaseLocation() string {
	if d.IsOpenshift() {
		return v1alpha1.PersistencePathBaseRhCos
	}
	if d.IsCos() {
		return v1alpha1.PersistencePathBaseCos
	}
	return v1alpha1.PersistencePathBase
}

func (d *DiscoveryNodeInfo) GetHostsideContainerPersistence() string {
	return d.GetHostsidePersistenceBaseLocation() + "/containers"
}

func (d *DiscoveryNodeInfo) GetHostsideClusterPersistence() string {
	return d.GetHostsidePersistenceBaseLocation() + "/clusters"
}

type Discoverer interface {
	DiscoverNode(ctx context.Context, nodeName string) (*DiscoveryNodeInfo, error)
}

type ExecDiscovery struct {
	client          client.Client
	ExecService     ExecService
	OwnerWekaObject *v1alpha1.OwnerWekaObject
}

func (e ExecDiscovery) DiscoverNode(ctx context.Context, nodeName string) (*DiscoveryNodeInfo, error) {
	return EnsureNodeDiscovered(ctx, e.client, *e.OwnerWekaObject, nodeName, e.ExecService)
}

func NewDiscoverer(c client.Client, exec ExecService, object *v1alpha1.OwnerWekaObject) Discoverer {
	return &ExecDiscovery{
		client:          c,
		ExecService:     exec,
		OwnerWekaObject: object,
	}
}

func GetCluster(ctx context.Context, c client.Client, name v1alpha1.ObjectReference) (*v1alpha1.WekaCluster, error) {
	cluster := &v1alpha1.WekaCluster{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: name.Namespace,
		Name:      name.Name,
	}, cluster)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get cluster")
	}
	return cluster, nil
}

func GetJoinIps(ctx context.Context, c client.Client, cluster *v1alpha1.WekaCluster) ([]string, error) {
	containers := GetClusterContainers(ctx, c, cluster, "compute")
	if len(containers) == 0 {
		return nil, errors.New("No compute containers found")
	}

	joinIpPortPairs := []string{}
	for _, container := range containers {
		if container.Status.ClusterContainerID == nil {
			continue
		}
		if container.Status.ManagementIP == "" {
			continue
		}
		joinIpPortPairs = append(joinIpPortPairs, WrapIpv6Brackets(container.Status.ManagementIP)+":"+strconv.Itoa(container.Spec.Port))
	}
	if len(joinIpPortPairs) == 0 {
		return nil, errors.New("No join IP port pairs found")
	}
	return joinIpPortPairs, nil
}

func WrapIpv6Brackets(ip string) string {
	if util.IsIpv6(ip) {
		return "[" + ip + "]"
	}
	return ip
}

func GetAllContainers(ctx context.Context, c client.Client) []v1alpha1.WekaContainer {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetAllContainers")
	defer end()

	containersList := v1alpha1.WekaContainerList{}
	err := c.List(ctx, &containersList)
	if err != nil {
		logger.SetError(err, "Failed to list containers")
		return nil
	}
	return containersList.Items
}

func GetClusterContainers(ctx context.Context, c client.Client, cluster *v1alpha1.WekaCluster, mode string) []*v1alpha1.WekaContainer {
	// partially moved to crd.go, need to move the rest
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetClusterContainers", "cluster", cluster.Name, "mode", mode, "cluster_uid", string(cluster.UID))
	defer end()

	containersList := v1alpha1.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(cluster.UID)},
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}
	err := c.List(ctx, &containersList, listOpts...)
	logger.InfoWithStatus(codes.Ok, "Listed containers", "count", len(containersList.Items))

	if err != nil {
		return nil
	}

	containers := []*v1alpha1.WekaContainer{}
	for i := range containersList.Items {
		containers = append(containers, &containersList.Items[i])
	}
	return containers
}

func EnsureNodeDiscovered(ctx context.Context, c client.Client, ownerDetails v1alpha1.OwnerWekaObject, nodeName string, exec ExecService) (*DiscoveryNodeInfo, error) {
	node := &corev1.Node{}
	err := c.Get(ctx, types.NamespacedName{Name: nodeName}, node)
	if err != nil {
		return nil, err
	}

	// Check if node already has the discovery.json annotation
	if annotation, ok := node.Annotations[discoveryAnnotation]; ok {
		// Validate the annotation is up to date
		discoveryNodeInfo := &DiscoveryNodeInfo{}
		err = json.Unmarshal([]byte(annotation), discoveryNodeInfo)
		if err == nil && discoveryNodeInfo.BootID == node.Status.NodeInfo.BootID && discoveryNodeInfo.Version >= DiscoveryTargetVersion {
			return discoveryNodeInfo, nil
		}
	}

	// FormCluster a WekaContainer with mode "discovery"
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}

	discoveryContainer := &v1alpha1.WekaContainer{
		ObjectMeta: v1.ObjectMeta{
			Name:      "weka-dsc-" + nodeName,
			Namespace: operatorNamespace,
		},
		Spec: v1alpha1.WekaContainerSpec{
			Mode:            v1alpha1.WekaContainerModeDiscovery,
			NodeAffinity:    nodeName,
			Image:           ownerDetails.Image,
			ImagePullSecret: ownerDetails.ImagePullSecret,
			Tolerations:     ownerDetails.Tolerations,
		},
	}

	err = c.Get(ctx, types.NamespacedName{Name: discoveryContainer.Name, Namespace: discoveryContainer.Namespace}, discoveryContainer)
	if err != nil {
		if !errors2.IsNotFound(err) {
			return nil, err
		} else {
			// Fill in the necessary fields here
			err = c.Create(ctx, discoveryContainer)
			if err != nil {
				// if already exits error check the status of existing container
				if !errors2.IsAlreadyExists(err) {
					return nil, err
				}
			}
		}
	}

	// Wait for the data to be available on this container
	err = c.Get(ctx, types.NamespacedName{Name: discoveryContainer.Name, Namespace: discoveryContainer.Namespace}, discoveryContainer)
	if err != nil {
		return nil, err
	}

	executor, err := exec.GetExecutor(ctx, discoveryContainer)
	if err != nil {
		return nil, err
	}
	stdout, _, err := executor.ExecNamed(ctx, "fetch_discovery", []string{"cat", "/tmp/weka-discovery.json"})
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read discovery.json")
	}

	discoveryNodeInfo := &DiscoveryNodeInfo{}
	err = json.Unmarshal(stdout.Bytes(), discoveryNodeInfo)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to unmarshal discovery.json")
	}

	discoveryNodeInfo.BootID = node.Status.NodeInfo.BootID
	discoveryString, err := json.Marshal(discoveryNodeInfo)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to marshal discovery.json")

	}

	// Update the node with data in annotation
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[discoveryAnnotation] = string(discoveryString)
	err = c.Update(ctx, node)
	if err != nil {
		return nil, err
	}

	// Delete WekaContainer
	// TODO: This needs to be done earlier, validating that node info is already correct
	// TODO: As we are going to leave garbage here
	err = c.Delete(ctx, discoveryContainer)
	if err != nil {
		return nil, err
	}

	return discoveryNodeInfo, nil
}
