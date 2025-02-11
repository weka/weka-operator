package discovery

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	util2 "github.com/weka/weka-operator/pkg/util"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DiscoveryAnnotation     = "weka.io/discovery.json"
	DiscoveryTargetSchema   = 3
	ocpDriverToolkitMapName = "ocp-driver-toolkit-images"
)

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
	// this field is for internal use only, is populayed by DiscoverNodeOperation.Enrich
	//Node *corev1.Node `json:"-"` // this is not necesserally aligned with a node
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

func (d *DiscoveryNodeInfo) GetHostsideSharedData() string {
	return d.GetHostsidePersistenceBaseLocation() + "/shared"
}

func (d *DiscoveryNodeInfo) GetContainerPersistencePath(uid types.UID) string {
	return fmt.Sprintf("%s/%s", d.GetHostsideContainerPersistence(), uid)
}

func (d *DiscoveryNodeInfo) GetContainerSharedDataPath(uid types.UID) string {
	return fmt.Sprintf("%s/containers/%s", d.GetHostsideSharedData(), uid)
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

func SelectOperationalContainers(containers []*weka.WekaContainer, numContainers int, roles []string) []*weka.WekaContainer {
	firstPassSuitable := []*weka.WekaContainer{}
	selected := []*weka.WekaContainer{}
	util2.Shuffle(containers)

	for _, container := range containers {
		// if roles are set - select only suitable roles
		if len(roles) == 0 {
			roles = []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute}
		}
		if len(roles) > 0 {
			roleFound := false
			for _, role := range roles {
				if container.Spec.Mode == role {
					roleFound = true
					break
				}
			}
			if !roleFound {
				continue
			}
		}
		if container.Status.ClusterContainerID == nil {
			continue
		}
		if len(container.Status.GetManagementIps()) == 0 {
			continue
		}

		notSuitableStatuses := []string{"PodNotRunning", "Stopped", "Starting", "Destroying"}
		if slices.Contains(notSuitableStatuses, container.Status.Status) {
			continue
		}

		firstPassSuitable = append(firstPassSuitable, container)
	}

	for _, container := range firstPassSuitable {
		if container.Status.Status == "Running" {
			//TODO: Integrate/replace with healthcheck mechanics for more elaborate healthcheck
			selected = append(selected, container)
		}
	}

	// if we selected at least one "Running" - lets go with it, if none - populate with many "not running"
	if len(selected) == 0 {
		// if we could not select target amount of  containers, we will select somem random that are not running
		util2.Shuffle(firstPassSuitable)

		for _, container := range firstPassSuitable {
			if container.Status.Status != "Running" {
				selected = append(selected, container)
			}
			if len(selected) >= numContainers {
				break
			}
		}
	}

	return selected
}

func GetClusterEndpoints(ctx context.Context, containers []*weka.WekaContainer, maxEndpoints int) []string {
	var endpoints []string
	for _, container := range containers {
		if hostIps := container.GetHostIps(); len(hostIps) > 0 {
			endpoints = append(endpoints, hostIps[0])
		}
		if len(endpoints) >= maxEndpoints {
			break
		}
	}
	return endpoints
}

func GetClusterNfsTargetIps(ctx context.Context, containers []*weka.WekaContainer) []string {
	var nfsTargetIps []string
	for _, container := range containers {
		if container.IsNfsContainer() {
			managementIps := container.Status.GetManagementIps()
			if len(managementIps) > 0 {
				nfsTargetIps = append(nfsTargetIps, managementIps[0])
			}
		}
	}
	return nfsTargetIps
}

// Returns a map of FD to join IP port pairs
// (if FD label is not provided, FD will be empty string)
func SelectJoinIps(containers []*weka.WekaContainer) (map[string][]string, error) {
	joinIpsByFD := make(map[string][]string)

	//TODO: Integrate FD-selection(best-effort) logic into selectOperational
	selected := SelectOperationalContainers(containers, 10, nil)

	for _, container := range selected {
		containerJoinIps := make([]string, 0, len(container.Status.GetManagementIps()))
		for _, ip := range container.Status.GetManagementIps() {
			joinIp := WrapIpv6Brackets(ip) + ":" + strconv.Itoa(container.GetPort())
			containerJoinIps = append(containerJoinIps, joinIp)
		}
		fd := ""
		// get FD info if FD is set on the container
		if container.Status.Allocations != nil && container.Status.Allocations.FailureDomain != nil {
			fd = *container.Status.Allocations.FailureDomain
		}
		if _, ok := joinIpsByFD[fd]; !ok {
			joinIpsByFD[fd] = containerJoinIps
		} else {
			joinIpsByFD[fd] = append(joinIpsByFD[fd], containerJoinIps...)
		}
	}
	if len(joinIpsByFD) == 0 {
		return nil, errors.New("No join IP port pairs found")
	}
	return joinIpsByFD, nil
}

func WrapIpv6Brackets(ip string) string {
	if util2.IsIpv6(ip) {
		return "[" + ip + "]"
	}
	return ip
}

func GetAllClusters(ctx context.Context, c client.Client) ([]weka.WekaCluster, error) {
	clustersList := weka.WekaClusterList{}
	err := c.List(ctx, &clustersList)
	if err != nil {
		err = errors.Wrap(err, "Failed to list clusters")
		return nil, err
	}
	return clustersList.Items, nil
}

func GetClusterByUID(ctx context.Context, c client.Client, uid types.UID) (*weka.WekaCluster, error) {
	clustersList := weka.WekaClusterList{}
	err := c.List(ctx, &clustersList)
	if err != nil {
		return nil, err
	}
	for _, cluster := range clustersList.Items {
		if cluster.UID == uid {
			return &cluster, nil
		}
	}
	return nil, errors.New("Cluster not found")
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

func GetClientContainers(ctx context.Context, c client.Client, wekaClient *weka.WekaClient) []*weka.WekaContainer {
	containersList := weka.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(wekaClient.Namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(wekaClient.UID)},
		client.MatchingLabels{"weka.io/mode": weka.WekaContainerModeClient},
	}

	err := c.List(ctx, &containersList, listOpts...)
	if err != nil {
		return nil
	}

	containers := make([]*weka.WekaContainer, len(containersList.Items))
	for i := range containersList.Items {
		containers[i] = &containersList.Items[i]
	}
	return containers
}

func SelectActiveContainer(containers []*weka.WekaContainer) *weka.WekaContainer {
	operational := SelectOperationalContainers(containers, 1, nil)
	if len(operational) == 0 {
		return nil
	}
	return operational[0]
}

func SelectActiveContainerWithRole(ctx context.Context, containers []*weka.WekaContainer, role string) (*weka.WekaContainer, error) {
	operational := SelectOperationalContainers(containers, 1, []string{role})
	if len(operational) > 0 {
		return operational[0], nil
	}

	err := fmt.Errorf("no container with role %s found", role)
	return nil, err
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
	imageBase := config.Config.OcpCompatibility.DriverToolkitImageBaseUrl
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

func GetContainerByName(ctx context.Context, c client.Client, name weka.ObjectReference) (*weka.WekaContainer, error) {
	container := &weka.WekaContainer{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: name.Namespace,
		Name:      name.Name,
	}, container)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get weka container")
	}
	return container, nil
}
