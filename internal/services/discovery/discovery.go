package discovery

import (
	"context"
	"fmt"
	"strconv"

	util2 "github.com/weka/weka-operator/pkg/util"

	"github.com/pkg/errors"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/envvars"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DiscoveryTargetSchema = 2
const ocpDriverToolkitMapName = "ocp-driver-toolkit-images"

type DiscoveryNodeInfo struct {
	IsHt               bool   `json:"is_ht"`
	KubernetesDistro   string `json:"kubernetes_distro,omitempty"`
	Os                 string `json:"os,omitempty"`
	OsBuildId          string `json:"os_build_id,omitempty"`
	BootID             string `json:"boot_id,omitempty"`
	Schema             int    `json:"schema,omitempty"`
	InitContainerImage string `json:"init_container_image,omitempty"`
	NumCpus            int    `json:"num_cpus,omitempty"`
	NumDrives          int    `json:"num_drives,omitempty"`
}

func (nodeInfo *DiscoveryNodeInfo) GetDrivesNumber() int {
	return nodeInfo.NumDrives
}

func (nodeInfo *DiscoveryNodeInfo) IsRhCos() bool {
	return nodeInfo.Os == weka.OsNameOpenshift
}

func (nodeInfo *DiscoveryNodeInfo) IsCos() bool {
	return nodeInfo.KubernetesDistro == weka.OsNameCos
}

func (d *DiscoveryNodeInfo) GetHostsidePersistenceBaseLocation() string {
	if d.IsRhCos() {
		return weka.PersistencePathBaseRhCos
	}
	if d.IsCos() {
		return weka.PersistencePathBaseCos
	}
	return weka.PersistencePathBase
}

func (d *DiscoveryNodeInfo) GetHostsideContainerPersistence() string {
	return d.GetHostsidePersistenceBaseLocation() + "/containers"
}

func (d *DiscoveryNodeInfo) GetHostsideClusterPersistence() string {
	return d.GetHostsidePersistenceBaseLocation() + "/clusters"
}

type Discoverer interface {
	DiscoverNode(ctx context.Context, nodeName weka.NodeName) (*DiscoveryNodeInfo, error)
}

func GetCluster(ctx context.Context, c client.Client, name weka.ObjectReference) (*weka.WekaCluster, error) {
	cluster := &weka.WekaCluster{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: name.Namespace,
		Name:      name.Name,
	}, cluster)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get cluster")
	}
	return cluster, nil
}

func GetJoinIps(ctx context.Context, c client.Client, cluster *weka.WekaCluster) ([]string, error) {
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
		joinIpPortPairs = append(joinIpPortPairs, WrapIpv6Brackets(container.Status.ManagementIP)+":"+strconv.Itoa(container.GetPort()))
	}
	if len(joinIpPortPairs) == 0 {
		return nil, errors.New("No join IP port pairs found")
	}
	return joinIpPortPairs, nil
}

func WrapIpv6Brackets(ip string) string {
	if util2.IsIpv6(ip) {
		return "[" + ip + "]"
	}
	return ip
}

func GetAllContainers(ctx context.Context, c client.Client) []weka.WekaContainer {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetAllContainers")
	defer end()

	containersList := weka.WekaContainerList{}
	err := c.List(ctx, &containersList)
	if err != nil {
		logger.SetError(err, "Failed to list containers")
		return nil
	}
	return containersList.Items
}

func GetClusterContainers(ctx context.Context, c client.Client, cluster *weka.WekaCluster, mode string) []*weka.WekaContainer {
	containersList := weka.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(cluster.UID)},
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}
	err := c.List(ctx, &containersList, listOpts...)

	if err != nil {
		return nil
	}

	containers := []*weka.WekaContainer{}
	for i := range containersList.Items {
		containers = append(containers, &containersList.Items[i])
	}
	return containers
}

func enrichDiscoveryInfo(ctx context.Context, c client.Client, discoveryNodeInfo *DiscoveryNodeInfo, node *corev1.Node) (*DiscoveryNodeInfo, error) {
	// Enrich with CPU info
	discoveryNodeInfo.NumCpus = int(node.Status.Allocatable.Cpu().Value())

	// Enrich with OCP toolkit image if necessary
	if discoveryNodeInfo.IsRhCos() {
		if discoveryNodeInfo.OsBuildId == "" {
			return nil, errors.New("Failed to get OCP version from node")
		}
		image, err := GetOcpToolkitImage(ctx, c, discoveryNodeInfo.OsBuildId)
		if err != nil || image == "" {
			return nil, errors.Wrap(err, fmt.Sprintf("Failed to get OCP toolkit image for version %s", discoveryNodeInfo.OsBuildId))
		}
		discoveryNodeInfo.InitContainerImage = image
	}

	return discoveryNodeInfo, nil
}

func GetOcpToolkitImage(ctx context.Context, c client.Client, v string) (string, error) {
	toolkitMap := &corev1.ConfigMap{}
	namespace, err := util2.GetPodNamespace()
	if err != nil {
		return "", err
	}
	if err := c.Get(ctx, types.NamespacedName{Name: ocpDriverToolkitMapName, Namespace: namespace}, toolkitMap); err != nil {
		return "", err
	}
	imageTag := ""
	if toolkitMap != nil {
		if toolkitMap.Data != nil {
			if toolkitMap.Data[v] != "" {
				imageTag = toolkitMap.Data[v]
			}
		}
	}
	if imageTag == "" {
		return "", errors.New(fmt.Sprintf("Failed to fetch image tag %s from configmap %s", v, ocpDriverToolkitMapName))
	}
	imageBase := envvars.GetStringEnv(envvars.EnvOCPToolkitImageBaseUrl, "quay.io/openshift-release-dev/ocp-v4.0-art-dev")
	return fmt.Sprintf("%s@sha256:%s", imageBase, imageTag), nil
}

func GetOwnedContainers(ctx context.Context, c client.Client, owner types.UID, namespace, mode string) ([]*weka.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GetOwnedContainers", "owner", owner, "mode", mode, "namespace", namespace)
	defer end()

	containersList := weka.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(owner)},
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}

	err := c.List(ctx, &containersList, listOpts...)
	if err != nil {
		return nil, err
	}
	logger.SetStatus(codes.Ok, "List success")

	containers := []*weka.WekaContainer{}
	for i := range containersList.Items {
		containers = append(containers, &containersList.Items[i])
	}
	return containers, nil
}
