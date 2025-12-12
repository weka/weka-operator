// This file contains common "get" functions used by WekaContainer reconciler in all/several states' flows
package wekacontainer

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/kubernetes"
	"github.com/weka/weka-operator/pkg/util"
)

func NodeIsReady(node *v1.Node) bool {
	if node == nil {
		return false
	}
	// check if the node has a NodeReady condition set to True
	isNodeReady := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
			isNodeReady = true
			break
		}
	}
	return isNodeReady
}

func NodeIsUnschedulable(node *v1.Node) bool {
	if node == nil {
		return false
	}
	return node.Spec.Unschedulable
}

func CanExecInPod(pod *v1.Pod) bool {
	// Review uses/split into few functions
	// pod deletion check is too aggressive, and matters mostly for s3/nfs, while openshift that has problem with deletiontimestamp is not in scope right now
	// s3 already does not rely on this function, and seems like the only place that actually cares for locality
	return pod != nil && pod.Status.Phase == v1.PodRunning && pod.DeletionTimestamp == nil
}

func (r *containerReconcilerLoop) PodIsSet() bool {
	return r.pod != nil
}

func (r *containerReconcilerLoop) PodNotSet() bool {
	return r.pod == nil
}

func (r *containerReconcilerLoop) NodeNotSet() bool {
	return r.node == nil
}

func (r *containerReconcilerLoop) NodeIsSet() bool {
	return r.node != nil
}

func (r *containerReconcilerLoop) PodNotRunning() bool {
	return r.pod.Status.Phase != v1.PodRunning
}

func (r *containerReconcilerLoop) ShouldDeactivate() bool {
	if !r.container.IsBackend() {
		return false
	}

	if r.container.Spec.GetOverrides().SkipDeactivate {
		return false
	}

	if !r.container.IsDeletingState() {
		return false
	}

	if r.container.Status.ClusterContainerID == nil {
		return false
	}
	// should deactivate does not represent if deactivation was done or not
	// only if it needs to be deactivated
	// this ensures us that handle deletion wont be reached until deactivation is done, if it should be done
	return true

}

func (r *containerReconcilerLoop) CanSkipDeactivate() bool {
	return r.container.Spec.Overrides.SkipDeactivate
}

func (r *containerReconcilerLoop) CanSkipDrivesForceResign() bool {
	return r.container.Spec.GetOverrides().SkipDrivesForceResign
}

func (r *containerReconcilerLoop) HasStatusNodeAffinity() bool {
	return r.container.Status.NodeAffinity != ""
}

func (r *containerReconcilerLoop) HasNodeAffinity() bool {
	return r.container.GetNodeAffinity() != ""
}

func (r *containerReconcilerLoop) ResultsAreSet() bool {
	return r.container.Status.ExecutionResult != nil
}

func (r *containerReconcilerLoop) ResultsAreProcessed() bool {
	for _, c := range r.container.Status.Conditions {
		if c.Type == condition.CondResultsProcessed && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *containerReconcilerLoop) NeedWekaDrivesListUpdate() bool {
	if !r.AllExpectedDrivesAreActive() {
		return true
	}
	return r.AddedDrivesNotAligedWithAllocations()
}

func (r *containerReconcilerLoop) AllExpectedDrivesAreActive() bool {
	if r.container.Status.Stats != nil {
		activeDrives := int(r.container.Status.Stats.Drives.DriveCounters.Active)
		if activeDrives == len(r.container.Status.AddedDrives) &&
			activeDrives == r.container.Spec.NumDrives {
			// all drives are added and active
			return true
		}
	}
	return false
}

func (r *containerReconcilerLoop) AddedDrivesNotAligedWithAllocations() bool {
	if r.container.Status.Allocations == nil {
		return false
	}

	if r.container.Spec.UseDriveSharing {
		addedDrivesVirualUuids := r.container.Status.GetAddedDrivesUuids()
		if len(addedDrivesVirualUuids) == 0 {
			return false
		}

		allocatedVirtualUuids := r.container.Status.Allocations.GetVirtualDrivesUuids()
		slices.Sort(addedDrivesVirualUuids)
		slices.Sort(allocatedVirtualUuids)

		return !slices.Equal(addedDrivesVirualUuids, allocatedVirtualUuids)
	} else {
		// non-proxy mode
		addedDrivesSerials := r.container.Status.GetAddedDrivesSerials()
		if len(addedDrivesSerials) == 0 {
			return false
		}
		slices.Sort(addedDrivesSerials)
		slices.Sort(r.container.Status.Allocations.Drives)

		return !slices.Equal(addedDrivesSerials, r.container.Status.Allocations.Drives)
	}
}

func (r *containerReconcilerLoop) HasDrivesToAdd() bool {
	return len(r.container.Status.AddedDrives) < r.container.Spec.NumDrives
}

func (r *containerReconcilerLoop) NeedsDrivesToAllocate() bool {
	if r.container.Status.Allocations == nil {
		// if no allocations at all, covered by cluster's AllocateResources
		return false
	}

	if r.container.Spec.UseDriveSharing {
		return len(r.container.Status.Allocations.VirtualDrives) < r.container.Spec.NumDrives
	}

	return len(r.container.Status.Allocations.Drives) < r.container.Spec.NumDrives
}

func (r *containerReconcilerLoop) IsNotAlignedImage() bool {
	return r.container.Status.LastAppliedImage != r.container.Spec.Image
}

func (r *containerReconcilerLoop) GetNode(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		return nil
	}

	node, err := r.KubeService.GetNode(ctx, k8sTypes.NodeName(nodeName))
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Node not found", "node", nodeName)
			return nil
		}
		err = errors.Wrap(err, "failed to get node")
		return err
	}
	r.node = node
	return nil
}

func (r *containerReconcilerLoop) GetWekaClient(ctx context.Context) error {
	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return nil
	} else if len(ownerRefs) > 1 {
		return errors.New("more than one owner reference found")
	}

	ownerRef := ownerRefs[0]
	if ownerRef.Kind != "WekaClient" {
		return nil
	}

	ownerUid := ownerRef.UID

	// Get wekaClient by UID
	wekaClientList := &weka.WekaClientList{}
	err := r.List(ctx, wekaClientList)
	if err != nil {
		return errors.Wrap(err, "failed to list weka clients")
	}

	for i := range wekaClientList.Items {
		if wekaClientList.Items[i].UID == ownerUid {
			r.wekaClient = &wekaClientList.Items[i]
			return nil
		}
	}

	return errors.New("weka client not found")
}

func (r *containerReconcilerLoop) FetchTargetCluster(ctx context.Context) error {
	wekaCluster := &weka.WekaCluster{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      r.wekaClient.Spec.TargetCluster.Name,
		Namespace: r.wekaClient.Spec.TargetCluster.Namespace,
	}, wekaCluster)
	if err != nil {
		return errors.Wrap(err, "failed to get target cluster")
	}
	r.targetCluster = wekaCluster

	return nil
}

func (r *containerReconcilerLoop) getCluster(ctx context.Context) (*weka.WekaCluster, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getClusterContainers")
	defer end()

	if r.cluster != nil {
		return r.cluster, nil
	}

	ownerRefs := r.container.GetOwnerReferences()
	if len(ownerRefs) == 0 {
		return nil, errors.New("no owner references found")
	} else if len(ownerRefs) > 1 {
		return nil, errors.New("more than one owner reference found")
	}

	ownerUid := ownerRefs[0].UID
	logger.Debug("Owner UID", "uid", ownerUid)

	cluster, err := discovery.GetClusterByUID(ctx, r.Client, ownerUid)
	if err != nil {
		return nil, err
	}
	r.cluster = cluster

	return r.cluster, nil
}

func (r *containerReconcilerLoop) getClusterContainers(ctx context.Context) ([]*weka.WekaContainer, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getClusterContainers")
	defer end()

	if r.clusterContainers != nil {
		return r.clusterContainers, nil
	}

	cluster, err := r.getCluster(ctx)
	if err != nil {
		return nil, fmt.Errorf("error getting cluster: %w", err)
	}

	clusterContainers := discovery.GetClusterContainers(ctx, r.Manager.GetClient(), cluster, "")
	if len(clusterContainers) == 0 {
		err := fmt.Errorf("no containers found in cluster %s", cluster.Name)
		return nil, err
	}

	msg := fmt.Sprintf("Found %d containers in cluster", len(clusterContainers))
	logger.Debug(msg, "cluster", cluster.Name)
	r.clusterContainers = clusterContainers

	return clusterContainers, nil
}

func (r *containerReconcilerLoop) GetNodeInfo(ctx context.Context, nodeName weka.NodeName) (*discovery.DiscoveryNodeInfo, error) {
	container := r.container

	if container.IsDiscoveryContainer() {
		return nil, errors.New("cannot run node discovery on a discovery container")
	}

	discoverNodeOp := operations.NewDiscoverNodeOperation(
		r.Manager,
		r.RestClient,
		nodeName,
		container,
		container.ToOwnerDetails(),
	)
	err := operations.ExecuteOperation(ctx, discoverNodeOp)
	if err != nil {
		return nil, err
	}

	return discoverNodeOp.GetResult(), nil
}

func (r *containerReconcilerLoop) pickMatchingNode(ctx context.Context) (*v1.Node, error) {
	// HACK: Since we don't really know the node affinity, we will try to discover it by labels
	// Assuming, that supplied labels represent unique type of machines
	// this puts a requirement on user to separate machines by labels, which is common approach
	nodes, err := r.KubeService.GetNodes(ctx, r.container.Spec.NodeSelector)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("no matching nodes found")
	}

	// if container has affinity, try to find node that satisfies it
	if r.container.Spec.Affinity != nil {
		for _, node := range nodes {
			if kubernetes.NodeSatisfiesAffinity(&node, r.container.Spec.Affinity) {
				return &node, nil
			}
		}
	} else {
		// if no affinity, return first node from the list
		return &nodes[0], nil
	}

	return nil, errors.New("no matching nodes found")
}

func (r *containerReconcilerLoop) getFrontendPodsOnNode(ctx context.Context, nodeName string) ([]v1.Pod, error) {
	return r.KubeService.GetPods(ctx, kubernetes.GetPodsOptions{
		Node: nodeName,
		LabelsIn: map[string][]string{
			// NOTE: Clients will not have affinity set, it's a small gap of race of s3 schedule on top of client
			// There is also a possible gap of deploying clients on top of S3
			// But since we do want to allow multiple clients to multiple clusters it becomes much complex
			// So  for now mostly solving case of scheduling of protocol on top of clients, and protocol on top of another protocol
			domain.WekaLabelMode: domain.ContainerModesWithFrontend,
		},
	})
}

func (r *containerReconcilerLoop) getFrontendWekaContainerOnNode(ctx context.Context, nodeName string) ([]weka.WekaContainer, error) {
	return r.KubeService.GetWekaContainers(ctx, kubernetes.GetPodsOptions{
		Node: nodeName,
		LabelsIn: map[string][]string{
			// NOTE: Clients will not have affinity set, it's a small gap of race of s3 schedule on top of client
			// There is also a possible gap of deploying clients on top of S3
			// But since we do want to allow multiple clients to multiple clusters it becomes much complex
			// So  for now mostly solving case of scheduling of protocol on top of clients, and protocol on top of another protocol
			domain.WekaLabelMode: domain.ContainerModesWithFrontend,
		},
	})
}

func (r *containerReconcilerLoop) getFailureDomain(ctx context.Context) *string {
	fdConfig := r.container.Spec.FailureDomain
	if fdConfig == nil {
		return nil
	}

	if fdConfig.Label != nil {
		if fd, ok := r.node.Labels[*fdConfig.Label]; ok {
			fd = handleFailureDomainValue(fd)
			return &fd
		}
		return nil
	}
	if len(fdConfig.CompositeLabels) > 0 {
		fdDomainParts := make([]string, 0, len(fdConfig.CompositeLabels))
		for _, fdLabel := range fdConfig.CompositeLabels {
			if fd, ok := r.node.Labels[fdLabel]; ok {
				fdDomainParts = append(fdDomainParts, fd)
			}
		}

		if len(fdDomainParts) == 0 {
			return nil
		}
		// concatenate failure domain parts with "-"
		fdValue := strings.Join(fdDomainParts, "-")
		fdValue = handleFailureDomainValue(fdValue)
		return &fdValue
	}

	return nil
}

func handleFailureDomainValue(fd string) string {
	// failure domain must not exceed 16 characters, and match regular expression "^(?:[^\\/]+)$"'
	// if it exceeds 16 characters, truncate it to 16 characters
	if len(fd) > 16 {
		// get 16 characters' hash value (to guarantee uniqueness)
		return util.GetHash(fd, 16)
	}
	// replace all "/" with "-"
	return strings.ReplaceAll(fd, "/", "-")
}
