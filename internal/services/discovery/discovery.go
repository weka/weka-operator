package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

const (
	DiscoveryAnnotation            = "weka.io/discovery.json"
	PodDiscoverySnapshotAnnotation = "weka.io/discovery-snapshot"
	DiscoveryTargetSchema          = 3
	ocpDriverToolkitMapName        = "ocp-driver-toolkit-images"
)

// Provider represents the cloud provider
type Provider string

const (
	ProviderAWS     Provider = "aws"
	ProviderOCI     Provider = "oci"
	ProviderUnknown Provider = ""
)

// ProviderFromID returns the cloud provider based on a node's ProviderID string.
func ProviderFromID(providerID string) Provider {
	if strings.HasPrefix(providerID, "aws://") {
		return ProviderAWS
	}
	if strings.HasPrefix(providerID, "ocid1.") {
		return ProviderOCI
	}
	return ProviderUnknown
}

// IsSupportedCloudProvider returns true if the node's ProviderID indicates a supported managed Kubernetes provider.
func IsSupportedCloudProvider(providerID string) bool {
	return ProviderFromID(providerID) != ProviderUnknown
}

// InstanceIDAndRegionFromProviderID parses an AWS node ProviderID of the form
// "aws:///<az>/<instance-id>" (e.g. "aws:///eu-west-1a/i-0123456789abcdef0") into its EC2
// instance-id and region (the AZ with its trailing zone letter stripped, e.g. "eu-west-1a" ->
// "eu-west-1"). Returns ok=false for non-"aws://" or malformed ProviderIDs.
func InstanceIDAndRegionFromProviderID(providerID string) (instanceID, region string, ok bool) {
	if ProviderFromID(providerID) != ProviderAWS {
		return "", "", false
	}

	trimmed := strings.TrimPrefix(providerID, "aws://")
	trimmed = strings.Trim(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return "", "", false
	}

	instanceID = parts[len(parts)-1]
	az := parts[len(parts)-2]
	if instanceID == "" || az == "" {
		return "", "", false
	}

	// Strip the trailing availability-zone letter (e.g. "eu-west-1a" -> "eu-west-1").
	region = strings.TrimRight(az, "abcdefghijklmnopqrstuvwxyz")
	if region == "" {
		return "", "", false
	}

	return instanceID, region, true
}

// IsKarpenterManagedNode reports whether the node was provisioned by Karpenter (or EKS Auto Mode,
// which uses the same NodeClaim API), detected structurally via the Node's ownerReference to a
// karpenter.sh NodeClaim rather than via a copyable label. This is a structural ownership fact set
// by Karpenter's own controller: any instance owned by a NodeClaim was launched via
// RunInstances/CreateFleet and can never be an ASG member. Nil-safe: returns false for a nil node.
func IsKarpenterManagedNode(node *corev1.Node) bool {
	if node == nil {
		return false
	}
	for _, ref := range node.OwnerReferences {
		if ref.Kind != "NodeClaim" {
			continue
		}
		group, _, _ := strings.Cut(ref.APIVersion, "/")
		if group == "karpenter.sh" {
			return true
		}
	}
	return false
}

type DiscoveryNodeInfo struct {
	IsHt               bool     `json:"is_ht"`
	KubernetesDistro   string   `json:"kubernetes_distro,omitempty"`
	Os                 string   `json:"os,omitempty"`
	OsBuildId          string   `json:"os_build_id,omitempty"`
	BootID             string   `json:"boot_id,omitempty"`
	Schema             int      `json:"schema,omitempty"`
	InitContainerImage string   `json:"init_container_image,omitempty"`
	NumCpus            int      `json:"num_cpus,omitempty"`
	Provider           Provider `json:"provider,omitempty"`
	Arch               string   `json:"arch,omitempty"`            // k8s-normalized, e.g. "amd64", "arm64"; set by Enrich()
	NodeFullPcpusOnly  bool     `json:"full_pcpus_only,omitempty"` // kubelet cpuManagerPolicyOptions full-pcpus-only; set by Enrich
	// this field is for internal use only, is populated by DiscoverNodeOperation.Enrich
	// Node *corev1.Node `json:"-"` // this is not necessarily aligned with a node
}

// PodDiscoverySnapshot holds the DiscoveryNodeInfo fields that affect pod spec creation.
// Stored as a pod annotation at creation time; compared against the actual scheduled
// node's info on subsequent reconciles to detect node-info mismatch.
type PodDiscoverySnapshot struct {
	IsHt     bool     `json:"is_ht"`
	Os       string   `json:"os,omitempty"`
	Provider Provider `json:"provider,omitempty"`
	Arch     string   `json:"arch,omitempty"`
}

func (n *DiscoveryNodeInfo) ToSnapshot() *PodDiscoverySnapshot {
	return &PodDiscoverySnapshot{
		IsHt:     n.IsHt,
		Os:       n.Os,
		Provider: n.Provider,
		Arch:     n.Arch,
	}
}

func (nodeInfo *DiscoveryNodeInfo) HasSupportedCloudProvider() bool {
	return nodeInfo.Provider != ProviderUnknown
}

func (nodeInfo *DiscoveryNodeInfo) IsRhCos() bool {
	return nodeInfo.Os == weka.OsNameOpenshift
}

// NodeInfoFromAnnotation parses a node's weka.io/discovery.json annotation into a DiscoveryNodeInfo.
// ok is false (and info nil) when the annotation is absent or unparsable. Single parse helper for the
// several call sites that read node discovery info off the annotation.
func NodeInfoFromAnnotation(node *corev1.Node) (info *DiscoveryNodeInfo, ok bool) {
	annotation, present := node.Annotations[DiscoveryAnnotation]
	if !present {
		return nil, false
	}
	return ParseNodeInfo(annotation)
}

// ParseNodeInfo unmarshals a weka.io/discovery.json annotation value into a DiscoveryNodeInfo. ok is
// false (info nil) when the value is unparsable. Callers that already hold the annotation string (e.g. to
// distinguish "absent" from "present but unparsable") use this directly so the node's annotation map is
// read only once.
func ParseNodeInfo(annotation string) (info *DiscoveryNodeInfo, ok bool) {
	info = &DiscoveryNodeInfo{}
	if json.Unmarshal([]byte(annotation), info) != nil {
		return nil, false
	}
	return info, true
}

// AnyNodeHasSelinux returns true if any node in the list is discovered to be an
// RHCOS/OpenShift node (which enforces SELinux by default). Nodes with a missing
// or unparsable discovery annotation are skipped.
func AnyNodeHasSelinux(nodes []corev1.Node) bool {
	for i := range nodes {
		if info, ok := NodeInfoFromAnnotation(&nodes[i]); ok && info.IsRhCos() {
			return true
		}
	}
	return false
}

func (nodeInfo *DiscoveryNodeInfo) IsCos() bool {
	return nodeInfo.Os == weka.OsNameCos
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

// GetHostsideEphemeralShare returns the host-side, node-level directory under
// /run (ephemeral — cleared on reboot) that is shared across all pods and
// clusters on this node. It is the generic parent for node-scoped ephemeral
// state; not tied to persistent storage or to any single cluster.
func (d *DiscoveryNodeInfo) GetHostsideEphemeralShare() string {
	return "/run/weka/ephemeral"
}

// GetHostsideSharedNetnsPath returns the host-side netns directory under the
// node ephemeral share. Shared across all pods and clusters on this node so
// network namespaces created on the host propagate to weka containers and back.
func (d *DiscoveryNodeInfo) GetHostsideSharedNetnsPath() string {
	return d.GetHostsideEphemeralShare() + "/shared-netns"
}

func (d *DiscoveryNodeInfo) GetHostsideClusterPersistence() string {
	return d.GetHostsidePersistenceBaseLocation() + "/clusters"
}

type Discoverer interface {
	DiscoverNode(ctx context.Context, nodeName weka.NodeName) (*DiscoveryNodeInfo, error)
}

// IsContainerOperational checks if a container is operational and ready for operations
func IsContainerOperational(container *weka.WekaContainer) bool {
	// Container must have a cluster container ID assigned
	if container.Status.ClusterContainerID == nil {
		return false
	}

	// Container must be in READY internal status
	if container.Status.InternalStatus != "READY" {
		return false
	}

	// Container must have at least one management IP
	if len(container.Status.GetManagementIps()) == 0 {
		return false
	}

	// Container must have WekaPort allocated
	if container.Status.Allocations == nil || container.Status.Allocations.WekaPort == 0 {
		return false
	}

	// Container must not be in unsuitable statuses
	notSuitableStatuses := []weka.ContainerStatus{
		weka.PodNotRunning,
		weka.Stopped,
		weka.Starting,
		weka.Destroying,
		weka.Deleting,
		weka.Paused,
	}
	return !slices.Contains(notSuitableStatuses, container.Status.Status)
}

func SelectOperationalContainers(containers []*weka.WekaContainer, numContainers int, roles []string) []*weka.WekaContainer {
	firstPassSuitable := []*weka.WekaContainer{}
	selected := []*weka.WekaContainer{}
	util.Shuffle(containers)

	for _, container := range containers {
		// if roles are set - select only suitable roles
		if len(roles) == 0 {
			roles = []string{weka.WekaContainerModeDrive, weka.WekaContainerModeCompute}
		}
		if len(roles) > 0 {
			roleFound := slices.Contains(roles, container.Spec.Mode)
			if !roleFound {
				continue
			}
		}

		// Use common validation function
		if !IsContainerOperational(container) {
			continue
		}

		firstPassSuitable = append(firstPassSuitable, container)
	}

	for _, container := range firstPassSuitable {
		if container.Status.Status == weka.Running {
			//TODO: Integrate/replace with healthcheck mechanics for more elaborate healthcheck
			selected = append(selected, container)
		}
	}

	// if we selected at least one "Running" - lets go with it, if none - populate with many "not running"
	if len(selected) == 0 {
		// if we could not select target amount of  containers, we will select some random that are not running
		util.Shuffle(containers)

		notSuitableStatuses := []weka.ContainerStatus{
			weka.PodNotRunning,
			weka.Stopped,
		}
		for _, container := range containers {
			if !slices.Contains(notSuitableStatuses, container.Status.Status) {
				selected = append(selected, container)
			}
			if len(selected) >= numContainers {
				break
			}
		}
	}

	return selected
}

func SelectRunningContainersByRole(containers []*weka.WekaContainer, numContainers int, role string) []*weka.WekaContainer {
	selected := []*weka.WekaContainer{}

	for _, container := range containers {
		if container.Spec.Mode != role {
			continue
		}
		if container.Status.Status == weka.Running {
			selected = append(selected, container)
		}
		if len(selected) >= numContainers {
			break
		}
	}

	return selected
}

func SelectContainersByRole(containers []*weka.WekaContainer, role string) []*weka.WekaContainer {
	selected := []*weka.WekaContainer{}
	for _, container := range containers {
		if container.Spec.Mode == role {
			selected = append(selected, container)
		}
	}

	return selected
}

func GetClusterEndpoints(ctx context.Context, containers []*weka.WekaContainer, maxEndpoints int, csiConfig weka.CsiConfig) []string {
	var endpoints []string
	for _, container := range containers {
		if hostIps := container.GetHostIps(csiConfig.EndpointsSubnets); len(hostIps) > 0 {
			endpoints = append(endpoints, hostIps[0])
		}
		if len(endpoints) >= maxEndpoints {
			break
		}
	}
	return endpoints
}

func GetClusterNfsTargetIps(ctx context.Context, containers []*weka.WekaContainer) []string {
	_, logger := instrumentation.CreateLogSpan(ctx, "GetClusterNfsTargetIps")
	defer logger.End()

	var nfsTargetIps []string
	for _, container := range containers {
		if container.IsNfsContainer() {
			managementIps := container.Status.GetManagementIps()
			if len(managementIps) > 0 {
				nfsTargetIps = append(nfsTargetIps, managementIps[0])
			}
		}
	}
	logger.SetValues("numNfsTargets", len(nfsTargetIps), "numContainers", len(containers))
	return nfsTargetIps
}

// Returns a map of FD to join IP port pairs
// (if FD label is not provided, FD will be empty string)
func SelectJoinIps(containers []*weka.WekaContainer) (map[string][]string, error) {
	joinIpsByFD := make(map[string][]string)

	//TODO: Integrate FD-selection(best-effort) logic into selectOperational
	selected := SelectOperationalContainers(containers, 12, nil)

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
	if util.IsIpv6(ip) {
		return "[" + ip + "]"
	}
	return ip
}

func GetClusterByUID(ctx context.Context, c client.Client, uid types.UID) (*weka.WekaCluster, error) {
	clustersList := weka.WekaClusterList{}
	err := c.List(ctx, &clustersList)
	if err != nil {
		return nil, err
	}
	for i := range clustersList.Items {
		if clustersList.Items[i].UID == uid {
			return &clustersList.Items[i], nil
		}
	}
	return nil, errors.New("Cluster not found")
}

func GetClusterContainers(ctx context.Context, c client.Client, cluster *weka.WekaCluster, mode string) ([]*weka.WekaContainer, error) {
	return GetClusterContainersByClusterUID(ctx, c, string(cluster.UID), cluster.Namespace, mode)
}

// GetClusterContainersNoFieldIndex is like GetClusterContainers but does NOT use the
// metadata.ownerReferences.uid field index. It lists the namespace and filters by owner UID in
// memory, so it works with a cache-less/direct client that has no field indexer registered (e.g. the
// weka-capacity CLI, which builds a plain client.New without a cache). The controller keeps using the
// index-based GetClusterContainers; the index is registered on the manager cache in
// setupContainerIndexes (cmd/manager/main.go). Sending that field selector through a direct client
// makes the apiserver reject it ("field label not supported: metadata.ownerReferences.uid").
func GetClusterContainersNoFieldIndex(ctx context.Context, c client.Client, cluster *weka.WekaCluster, mode string) ([]*weka.WekaContainer, error) {
	return getClusterContainersByClusterUID(ctx, c, string(cluster.UID), cluster.Namespace, mode, false)
}

func GetClusterContainersByClusterUID(ctx context.Context, c client.Client, clusterUID, clusterNamespace, mode string) ([]*weka.WekaContainer, error) {
	return getClusterContainersByClusterUID(ctx, c, clusterUID, clusterNamespace, mode, true)
}

// getClusterContainersByClusterUID lists a cluster's WekaContainers. When useFieldIndex is true it
// filters via the metadata.ownerReferences.uid cache field index (fast, but requires the index to be
// registered on the client's cache). When false it lists the namespace and filters by owner UID in
// memory — for clients without that index registered.
func getClusterContainersByClusterUID(ctx context.Context, c client.Client, clusterUID, clusterNamespace, mode string, useFieldIndex bool) ([]*weka.WekaContainer, error) {
	containersList := weka.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(clusterNamespace),
	}
	if useFieldIndex {
		listOpts = append(listOpts, client.MatchingFields{"metadata.ownerReferences.uid": clusterUID})
	}
	if mode != "" {
		listOpts = append(listOpts, client.MatchingLabels{"weka.io/mode": mode})
	}
	err := c.List(ctx, &containersList, listOpts...)

	if err != nil {
		return nil, errors.Wrap(err, "Failed to list containers for cluster")
	}

	containers := []*weka.WekaContainer{}
	for i := range containersList.Items {
		if !useFieldIndex && !isOwnedByUID(&containersList.Items[i], clusterUID) {
			continue
		}
		containers = append(containers, &containersList.Items[i])
	}
	return containers, nil
}

// isOwnedByUID reports whether obj carries an ownerReference with the given UID.
func isOwnedByUID(obj *weka.WekaContainer, uid string) bool {
	for _, ref := range obj.OwnerReferences {
		if string(ref.UID) == uid {
			return true
		}
	}
	return false
}

func GetClientContainers(ctx context.Context, c client.Client, wekaClient *weka.WekaClient) ([]*weka.WekaContainer, error) {
	containersList := weka.WekaContainerList{}
	listOpts := []client.ListOption{
		client.InNamespace(wekaClient.Namespace),
		client.MatchingFields{"metadata.ownerReferences.uid": string(wekaClient.UID)},
		client.MatchingLabels{"weka.io/mode": weka.WekaContainerModeClient},
	}

	err := c.List(ctx, &containersList, listOpts...)
	if err != nil {
		return nil, err
	}

	containers := make([]*weka.WekaContainer, len(containersList.Items))
	for i := range containersList.Items {
		containers[i] = &containersList.Items[i]
	}
	return containers, nil
}

func SelectActiveContainer(containers []*weka.WekaContainer) *weka.WekaContainer {
	operational := SelectOperationalContainers(containers, 1, nil)
	if len(operational) == 0 {
		// return any random container if no operational found
		util.Shuffle(containers)
		if len(containers) == 0 {
			return nil
		}
		// if we have no operational containers, we will return the first one
		return containers[0]
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
	namespace, err := util.GetPodNamespace()
	if err != nil {
		return "", err
	}
	if err := c.Get(ctx, types.NamespacedName{Name: ocpDriverToolkitMapName, Namespace: namespace}, toolkitMap); err != nil {
		return "", err
	}
	imageTag := ""
	if toolkitMap.Data != nil {
		if toolkitMap.Data[v] != "" {
			imageTag = toolkitMap.Data[v]
		}
	}
	if imageTag == "" {
		return "", errors.New(fmt.Sprintf("Failed to fetch image tag %s from configmap %s", v, ocpDriverToolkitMapName))
	}
	imageBase := config.Config.OcpCompatibility.DriverToolkitImageBaseUrl
	return fmt.Sprintf("%s@sha256:%s", imageBase, imageTag), nil
}

func GetOwnedContainers(ctx context.Context, c client.Client, owner types.UID, namespace, mode string) ([]*weka.WekaContainer, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetOwnedContainers", "owner", owner, "mode", mode, "namespace", namespace)
	defer logger.End()

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

func SelectNonDeletedWekaContainers(containers []*weka.WekaContainer) []*weka.WekaContainer {
	nonDeleted := make([]*weka.WekaContainer, 0, len(containers))
	for _, container := range containers {
		if container.DeletionTimestamp != nil {
			continue // skip deleted containers
		}
		if slices.Contains([]weka.ContainerState{weka.ContainerStateDeleting, weka.ContainerStateDestroying}, container.Spec.State) {
			continue // skip containers that are in deleting or destroying state
		}
		nonDeleted = append(nonDeleted, container)
	}
	return nonDeleted
}

func GetWekaClientsForCluster(ctx context.Context, c client.Client, cluster *weka.WekaCluster) ([]*weka.WekaClient, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetWekaClientsForCluster", "cluster", cluster.Name)
	defer logger.End()

	clientsList := weka.WekaClientList{}
	err := c.List(ctx, &clientsList)
	if err != nil {
		err = errors.Wrap(err, "Failed to list Weka clients")
		return nil, err
	}

	var wekaClients []*weka.WekaClient
	for i := range clientsList.Items {
		if clientsList.Items[i].Spec.TargetCluster.Name == cluster.Name && clientsList.Items[i].Spec.TargetCluster.Namespace == cluster.Namespace {
			wekaClients = append(wekaClients, &clientsList.Items[i])
		}
	}

	logger.SetValues("numClients", len(wekaClients), "cluster", cluster.Name)
	if len(wekaClients) == 0 {
		logger.Info("No Weka clients found for the cluster")
	} else {
		logger.Info("Found Weka clients for the cluster")
	}
	return wekaClients, nil
}

type SsdProxyNotFoundError struct {
	NodeName weka.NodeName
}

func (e *SsdProxyNotFoundError) Error() string {
	return fmt.Sprintf("No ssdproxy container found on node %s", e.NodeName)
}

func GetSsdProxyOnNode(ctx context.Context, c client.Client, nodeName weka.NodeName) (*weka.WekaContainer, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "GetSsdProxyOnNode", "nodeName", nodeName)
	defer logger.End()

	// Get the operator namespace where ssdproxy containers are deployed
	operatorNamespace, err := util.GetPodNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to get operator namespace: %w", err)
	}

	// List all ssdproxy containers in the operator namespace
	// Note: We don't filter by cluster because ssdproxy containers are shared across clusters on the same node
	kubeService := kubernetes.NewKubeService(c)
	containers, err := kubeService.GetWekaContainersSimple(ctx, operatorNamespace, string(nodeName), map[string]string{
		"weka.io/mode": weka.WekaContainerModeSSDProxy,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ssdpoxy containers on node %s: %w", nodeName, err)
	}

	if len(containers) == 0 {
		return nil, &SsdProxyNotFoundError{NodeName: nodeName}
	}

	proxy := containers[0]

	logger.Debug("Found ssdproxy container on node",
		"ssdproxy_name", proxy.Name,
		"ssdproxy_uid", proxy.UID,
		"node", nodeName,
	)

	return &proxy, nil
}
