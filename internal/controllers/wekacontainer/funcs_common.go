// This file contains common functions used by WekaContainer reconciler in all/several states' flows
package wekacontainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
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

func (r *containerReconcilerLoop) IsNotAlignedImage() bool {
	return r.container.Status.LastAppliedImage != r.container.Spec.Image
}

func (r *containerReconcilerLoop) refreshPod(ctx context.Context) error {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "refreshPod")
	defer end()

	pod := &v1.Pod{}
	key := client.ObjectKey{Name: r.container.Name, Namespace: r.container.Namespace}
	if err := r.Get(ctx, key, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	r.pod = pod

	return nil
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

func (r *containerReconcilerLoop) GetNodeInfo(ctx context.Context) (*discovery.DiscoveryNodeInfo, error) {
	container := r.container

	if container.IsDiscoveryContainer() {
		return nil, errors.New("cannot get node info for discovery container")
	}

	nodeAffinity := container.GetNodeAffinity()
	if container.GetNodeAffinity() == "" {
		node, err := r.pickMatchingNode(ctx)
		if err != nil {
			return nil, err
		}
		nodeAffinity = weka.NodeName(node.Name)
	}
	discoverNodeOp := operations.NewDiscoverNodeOperation(
		r.Manager,
		r.RestClient,
		nodeAffinity,
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

func (r *containerReconcilerLoop) setErrorStatus(ctx context.Context, stepName string, err error) error {
	// ignore "the object has been modified" errors
	if apierrors.IsConflict(err) {
		return nil
	}

	reason := fmt.Sprintf("%sError", stepName)
	r.RecordEventThrottled(v1.EventTypeWarning, reason, err.Error(), time.Minute)

	if !r.IsStatusOverwritableByLocal() {
		return nil
	}

	if r.container.Status.Status == weka.Error || r.container.Status.Status == weka.Unhealthy {
		return nil
	}
	r.container.Status.Status = weka.Error
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) setDrivesErrorStatus(ctx context.Context, _ string, err error) error {
	r.RecordEvent(v1.EventTypeWarning, "DrivesAddingError", err.Error())

	if r.container.Status.Status == weka.DrivesAdding {
		return nil
	}
	r.container.Status.Status = weka.DrivesAdding
	return r.Status().Update(ctx, r.container)
}

func (r *containerReconcilerLoop) RecordEvent(eventtype, reason, message string) error {
	if r.container == nil {
		return fmt.Errorf("container is not set")
	}
	if eventtype == "" {
		normal := v1.EventTypeNormal
		eventtype = normal
	}

	r.Recorder.Event(r.container, eventtype, reason, message)
	return nil
}

func (r *containerReconcilerLoop) RecordEventThrottled(eventtype, reason, message string, interval time.Duration) error {
	throttler := r.ThrottlingMap.WithPartition("container/" + r.container.Name)

	if !throttler.ShouldRun(eventtype+reason, &throttling.ThrottlingSettings{
		Interval:                    interval,
		DisableRandomPreSetInterval: true,
	}) {
		return nil
	}

	return r.RecordEvent(eventtype, reason, message)
}

func (r *containerReconcilerLoop) IsStatusOverwritableByLocal() bool {
	// we do not want to overwrite this statuses, as they proxy some higher-level state
	if slices.Contains(
		[]weka.ContainerStatus{
			weka.Completed,
			weka.Deleting,
			weka.Destroying,
		},
		r.container.Status.Status,
	) {
		return false
	}
	return true
}

func (r *containerReconcilerLoop) updateContainerStatusIfNotEquals(ctx context.Context, newStatus weka.ContainerStatus) error {
	if r.container.Status.Status != newStatus {
		r.container.Status.Status = newStatus
		err := r.Status().Update(ctx, r.container)
		if err != nil {
			err := fmt.Errorf("failed to update container status: %w", err)
			return err
		}
	}
	return nil
}

func (r *containerReconcilerLoop) updateStatusWaitForDrivers(ctx context.Context) error {
	return r.updateContainerStatusIfNotEquals(ctx, weka.WaitForDrivers)
}

func (r *containerReconcilerLoop) ensurePod(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	nodeInfo := &discovery.DiscoveryNodeInfo{}
	if !container.IsDiscoveryContainer() {
		var err error
		nodeInfo, err = r.GetNodeInfo(ctx)
		if err != nil {
			return err
		}
	}

	image := container.Spec.Image

	if r.IsNotAlignedImage() && !container.Spec.GetOverrides().UpgradeForceReplace {
		// do not create pod with spec image if we know in advance that we cannot upgrade
		canUpgrade, err := r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			logger.Info("Cannot upgrade to new image, using last applied", "image", image, "error", err)
			image = container.Status.LastAppliedImage
		}
	}

	// refresh container join ips (if there are any)
	if len(container.Spec.JoinIps) > 0 {
		ownerRef := container.GetOwnerReferences()
		if len(ownerRef) == 0 {
			return errors.New("no owner reference found")
		}
		owner := ownerRef[0]

		joinIps, _ := services.ClustersCachedInfo.GetJoinIps(ctx, string(owner.UID), owner.Name, container.Namespace)
		if len(joinIps) > 0 {
			container.Spec.JoinIps = joinIps
		}
	}

	desiredPod, err := resources.NewPodFactory(container, nodeInfo).Create(ctx, &image)
	if err != nil {
		return errors.Wrap(err, "Failed to create pod spec")
	}

	if err := ctrl.SetControllerReference(container, desiredPod, r.Scheme); err != nil {
		return errors.Wrapf(err, "Error setting controller reference")
	}

	if err := r.Create(ctx, desiredPod); err != nil {
		return errors.Wrap(err, "Failed to create pod")
	}
	r.pod = desiredPod
	err = r.refreshPod(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) reconcileWekaLocalStatus(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	container := r.container

	wekaLocalPsTimeout := 10 * time.Second
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &wekaLocalPsTimeout)

	localContainers, err := wekaService.ListLocalContainers(ctx)
	if err != nil {
		logger.Error(err, "Error getting weka local ps")
		// TODO: Validate agent-specific errors, but should not be very important

		// check if drivers should be force-reloaded
		loaded, driversErr := r.driversLoaded(ctx)
		if driversErr != nil {
			return driversErr
		}

		if !loaded {
			err = r.updateStatusWaitForDrivers(ctx)
			if err != nil {
				return err
			}

			details := r.container.ToOwnerDetails()
			if r.container.Spec.DriversLoaderImage != "" {
				details.Image = r.container.Spec.DriversLoaderImage
			}
			driversLoader := operations.NewLoadDrivers(r.Manager, r.node, *details, r.container.Spec.DriversDistService, r.container.HasFrontend(), true)
			loaderErr := operations.ExecuteOperation(ctx, driversLoader)
			if loaderErr != nil {
				err := fmt.Errorf("drivers are not loaded: %v; %v", driversErr, loaderErr)
				return lifecycle.NewWaitError(err)
			}

			err = fmt.Errorf("weka local ps failed: %v", err)
			return lifecycle.NewWaitError(err)
		}
		return lifecycle.NewWaitError(err)
	}

	err = r.checkContainerNotFound(localContainers, err)
	if err != nil {
		if err := r.updateContainerStatusIfNotEquals(ctx, weka.Unhealthy); err != nil {
			return err
		}
		return lifecycle.NewWaitErrorWithDuration(err, 15*time.Second)
	}

	var localContainer services.WekaLocalContainer
	for _, c := range localContainers {
		if c.Name == container.Spec.WekaContainerName {
			localContainer = c
			break
		}
	}
	if localContainer.Name == "" {
		localContainer = localContainers[0]
	}
	status := localContainer.RunStatus

	// check local container status and propagate failure message (if any) as event
	internalStatus := localContainer.InternalStatus.DisplayStatus
	if internalStatus != "READY" && localContainer.LastFailure != "" && !container.IsDistMode() {
		msg := fmt.Sprintf(
			"Container is not ready, status: %s, last failure: %s (%s)",
			internalStatus, localContainer.LastFailure, localContainer.LastFailureTime,
		)
		r.RecordEventThrottled(v1.EventTypeWarning, "WekaLocalStatus", msg, time.Minute)
	}

	// skip status update for DrivesAdding
	if status == string(weka.Running) && container.Status.Status == weka.DrivesAdding {
		// update internal status if it changed (this part is for backward compatibility)
		if container.Status.InternalStatus != internalStatus {
			return fmt.Errorf("internal status changed: %s -> %s", container.Status.InternalStatus, internalStatus)
		}
		return nil
	}

	containerStatus := weka.ContainerStatus(status)
	if container.Status.Status != containerStatus || container.Status.InternalStatus != internalStatus {
		logger.Debug("Updating status", "old_status", container.Status.Status, "new_status", containerStatus, "old_internal_status", container.Status.InternalStatus, "new_internal_status", internalStatus)
		r.IsStatusOverwritableByLocal()
		container.Status.Status = containerStatus
		container.Status.InternalStatus = internalStatus
		if err := r.Status().Update(ctx, container); err != nil {
			return err
		}
		logger.WithValues("status", status, "internal_status", internalStatus).Info("Status updated")
		return nil
	}

	return nil
}

func (r *containerReconcilerLoop) checkContainerNotFound(localPsResponse []services.WekaLocalContainer, psErr error) error {
	container := r.container

	if len(localPsResponse) == 0 {
		return errors.New("weka local ps response is empty")
	}

	found := false
	for _, c := range localPsResponse {
		if c.Name == container.Spec.WekaContainerName {
			found = true
			break
		} else if c.Name == "envoy" && container.IsEnvoy() {
			found = true
			break
		}
	}

	if !found {
		return errors.New("weka container not found")
	}

	return nil
}

func (r *containerReconcilerLoop) getCachedActiveMounts(ctx context.Context) (*int, error) {
	if r.activeMounts != nil {
		return r.activeMounts, nil
	}

	activeMounts, err := r.getActiveMounts(ctx)
	if err != nil && errors.Is(err, &NoWekaFsDriverFound{}) {
		// if no weka fs driver found, we can assume that there are no active mounts
		val := 0
		r.activeMounts = &val
		return r.activeMounts, nil
	}
	if err != nil {
		return nil, err
	}
	r.activeMounts = activeMounts
	return r.activeMounts, nil
}

func (r *containerReconcilerLoop) getNodeAgentPods(ctx context.Context) ([]v1.Pod, error) {
	if r.node == nil {
		return nil, errors.New("Node is not set")
	}

	if !NodeIsReady(r.node) {
		err := errors.New("Node is not ready")
		return nil, lifecycle.NewWaitErrorWithDuration(err, time.Second*15)
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return nil, err
	}
	//TODO: We can replace this call with GetWekaContainerSimple (and remove index for pods nodenames) if we move nodeAgent to be wekacontainer
	pods, err := r.KubeService.GetPodsSimple(ctx, ns, r.node.Name, map[string]string{
		"app.kubernetes.io/component": "weka-node-agent",
	})
	if err != nil {
		return nil, err
	}
	return pods, nil
}

func (r *containerReconcilerLoop) findAdjacentNodeAgent(ctx context.Context, pod *v1.Pod) (*v1.Pod, error) {
	ctx, _, end := instrumentation.GetLogSpan(ctx, "findAdjacentNodeAgent", "node", r.container.GetNodeAffinity())
	defer end()

	agentPods, err := r.getNodeAgentPods(ctx)
	var waitErr *lifecycle.WaitError
	if err != nil && errors.As(err, &waitErr) {
		return nil, err
	}
	if err != nil {
		err = fmt.Errorf("failed to get node agent pods: %w", err)
		return nil, err
	}
	if len(agentPods) == 0 {
		return nil, errors.New("There are no agent pods on node")
	}

	var targetNodeName string
	if r.container.GetNodeAffinity() != "" {
		targetNodeName = string(r.container.GetNodeAffinity())
	} else {
		if r.pod == nil {
			return nil, errors.New("Pod is nil and no affinity on container")
		}
		targetNodeName = pod.Spec.NodeName
	}

	for _, agentPod := range agentPods {
		if agentPod.Spec.NodeName == targetNodeName {
			if agentPod.Status.Phase == v1.PodRunning {
				return &agentPod, nil
			}
			return nil, &NodeAgentPodNotRunning{}
		}
	}

	return nil, errors.New("No agent pod found on the same node")
}

// a hack putting this global, but also not a harmful one
var nodeAgentToken string
var nodeAgentLastPull time.Time

func (r *containerReconcilerLoop) getNodeAgentToken(ctx context.Context) (string, error) {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "getNodeAgentToken")
	defer end()

	if nodeAgentToken != "" && time.Since(nodeAgentLastPull) < time.Minute {
		return nodeAgentToken, nil
	}

	ns, err := util.GetPodNamespace()
	if err != nil {
		return "", err
	}

	secret, err := r.KubeService.GetSecret(ctx, config.Config.Metrics.NodeAgentSecretName, ns)
	if err != nil {
		return "", err
	}
	if secret == nil {
		logger.Info("No secret found")
		return "", errors.New("No secret found")
	}

	tokenRaw := secret.Data["token"]
	token := string(tokenRaw)
	if token == "" {
		logger.Info("No token found")
		return "", errors.New("No token found")
	}
	nodeAgentToken = token
	nodeAgentLastPull = time.Now()

	return token, nil
}

func (r *containerReconcilerLoop) getActiveMounts(ctx context.Context) (*int, error) {
	pod, err := r.findAdjacentNodeAgent(ctx, r.pod)
	if err != nil {
		return nil, err
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		err = errors.Wrap(err, "error getting node agent token")
		return nil, err
	}

	url := "http://" + pod.Status.PodIP + ":8090/getActiveMounts"

	resp, err := util.SendGetRequest(ctx, url, util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		err = errors.Wrap(err, "error sending getActiveMountsget request")
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, &NoWekaFsDriverFound{}
		}

		err := errors.New("getActiveMounts request failed")
		return nil, err
	}

	var activeMountsResp struct {
		ActiveMounts int `json:"active_mounts"`
	}
	err = json.NewDecoder(resp.Body).Decode(&activeMountsResp)
	if err != nil {
		err = errors.Wrap(err, "error decoding response")
		return nil, err
	}

	return &activeMountsResp.ActiveMounts, nil
}

func (r *containerReconcilerLoop) noActiveMountsRestriction(ctx context.Context) (bool, error) {
	// do not check active mounts for s3 containers
	if r.container.IsS3Container() {
		return true, nil
	}

	// if container did not join cluster, we can skip active mounts check
	// NOTE: case - pod was stuck in Pending state and wekacontainer CR was deleted afterwards
	// we'd want to allow this container to be recreated by client reconciler
	if r.container.Status.ClusterContainerID == nil {
		return true, nil
	}

	if r.container.Spec.GetOverrides().SkipActiveMountsCheck {
		return true, nil
	}

	activeMounts, err := r.getCachedActiveMounts(ctx)
	if err != nil {
		return false, err
	}

	if activeMounts != nil && *activeMounts != 0 {
		err := fmt.Errorf("%d mounts are still active", *activeMounts)
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "ActiveMounts", err.Error(), time.Minute)

		return false, err
	}

	return true, nil
}

func (r *containerReconcilerLoop) clearStatus(ctx context.Context) error {
	r.container.Status = weka.WekaContainerStatus{}
	return r.Status().Update(ctx, r.container)
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

func (r *containerReconcilerLoop) getWekaPodContainer(pod *v1.Pod) (v1.Container, error) {
	for _, podContainer := range pod.Spec.Containers {
		if podContainer.Name == "weka-container" {
			return podContainer, nil
		}
	}
	return v1.Container{}, errors.New("Weka container not found in pod")
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
		return getHash(fd)
	}
	// replace all "/" with "-"
	return strings.ReplaceAll(fd, "/", "-")
}

// Generates SHA-256 hash and takes the first 16 characters
func getHash(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])[:16]
}
