package wekacontainer

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/drivers"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

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

func (r *containerReconcilerLoop) ensurePod(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if NodeIsUnschedulable(r.node) {
		err := errors.Errorf("node %s is unschedulable, cannot create pod", r.node.Name)
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
	}

	container := r.container

	nodeInfo := &discovery.DiscoveryNodeInfo{}
	var err error
	var nodeAffinity weka.NodeName

	if !container.IsDiscoveryContainer() {
		// nodeName can be already set in the spec
		nodeAffinity = container.GetNodeAffinity()

		if nodeAffinity == "" {
			node, err := r.pickMatchingNode(ctx)
			if err != nil {
				return err
			}
			nodeAffinity = weka.NodeName(node.Name)
		}

		nodeInfo, err = r.GetNodeInfo(ctx, nodeAffinity)
		if err != nil {
			return err
		}
	}

	image := container.Spec.Image

	// For drivers-loader with DriversLoaderImage set, use it as the pod image
	// This sets both the container image and IMAGE_NAME env var correctly
	if container.Spec.Mode == weka.WekaContainerModeDriversLoader &&
		container.Spec.DriversLoaderImage != "" {
		image = container.Spec.DriversLoaderImage
	}

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

	// For drivers-builder containers, resolve the builder image and set instructions
	// before creating the pod so setDriverDependencies handles init containers uniformly
	if container.IsDriversBuilder() {
		if override := container.Annotations[operations.ImageOverrideAnnotation]; override != "" {
			image = override
		} else {
			node := &v1.Node{}
			if err := r.Get(ctx, client.ObjectKey{Name: string(nodeAffinity)}, node); err != nil {
				return errors.Wrap(err, "failed to get target node for drivers-builder")
			}
			builderImage := drivers.GetBuilderImageForNode(node)
			image = builderImage

			payloadBytes, _ := json.Marshal(map[string]string{
				"targetImage": container.Spec.Image,
				"cliImage":    builderImage,
			})
			container.Spec.Instructions = &weka.Instructions{
				Type:    weka.InstructionCopyWekaFilesToDriverLoader,
				Payload: string(payloadBytes),
			}
		}
	}

	desiredPod, err := resources.NewPodFactory(container, nodeInfo).Create(ctx, &image)
	if err != nil {
		return errors.Wrap(err, "Failed to create pod spec")
	}

	// Annotate with discovery snapshot so we can detect node-info mismatch on reconcile.
	if !container.IsDiscoveryContainer() {
		snapshotJSON, marshalErr := json.Marshal(nodeInfo.ToSnapshot())
		if marshalErr != nil {
			logger.Error(marshalErr, "Failed to marshal discovery snapshot, skipping annotation")
		} else {
			if desiredPod.Annotations == nil {
				desiredPod.Annotations = make(map[string]string)
			}
			desiredPod.Annotations[discovery.PodDiscoverySnapshotAnnotation] = string(snapshotJSON)
		}
	}

	// Annotate with pod config hash so we can detect spec drift on reconcile.
	configHash, hashErr := resources.ComputePodConfigHash(&container.Spec)
	if hashErr != nil {
		logger.Error(hashErr, "Failed to compute pod config hash, skipping annotation")
	} else {
		if desiredPod.Annotations == nil {
			desiredPod.Annotations = make(map[string]string)
		}
		desiredPod.Annotations[resources.PodConfigHashAnnotation] = configHash
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

func (r *containerReconcilerLoop) deletePodIfNodeInfoMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	if r.container.IsDiscoveryContainer() {
		return nil
	}

	// HasStatusNodeAffinity predicate guarantees this is non-empty.
	nodeName := r.container.GetNodeAffinity()

	actualInfo, err := r.GetNodeInfo(ctx, nodeName)
	if err != nil {
		return err
	}
	if actualInfo == nil {
		return lifecycle.NewWaitError(errors.New("node info not yet available for mismatch check"))
	}

	snapshotJSON := r.pod.Annotations[discovery.PodDiscoverySnapshotAnnotation]
	var snapshot discovery.PodDiscoverySnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		// No snapshot or unparsable — skip; pod will be annotated correctly on next recreation.
		return nil
	}

	actual := actualInfo.ToSnapshot()
	if snapshot == *actual {
		return nil
	}

	logger.Info("Pod discovery snapshot does not match actual node, deleting for recreation",
		"snapshotIsHt", snapshot.IsHt, "actualIsHt", actual.IsHt,
		"snapshotOs", snapshot.Os, "actualOs", actual.Os,
		"snapshotProvider", snapshot.Provider, "actualProvider", actual.Provider,
		"snapshotArch", snapshot.Arch, "actualArch", actual.Arch,
		"node", nodeName)

	if err := r.deletePod(ctx, r.pod); err != nil {
		return err
	}

	return lifecycle.NewWaitError(errors.New("pod deleted due to node-info mismatch, waiting for recreation"))
}

func (r *containerReconcilerLoop) deletePodIfConfigHashMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "deletePodIfConfigHashMismatch")
	defer end()

	annotatedHash := r.pod.Annotations[resources.PodConfigHashAnnotation]
	if annotatedHash == "" {
		// Pod was created before this feature — backfill the annotation
		// so it gets a baseline for future comparisons. Do NOT delete.
		currentHash, err := resources.ComputePodConfigHash(&r.container.Spec)
		if err != nil {
			logger.Error(err, "Failed to compute pod config hash for backfill")
			return nil
		}
		podCopy := r.pod.DeepCopy()
		if podCopy.Annotations == nil {
			podCopy.Annotations = make(map[string]string)
		}
		podCopy.Annotations[resources.PodConfigHashAnnotation] = currentHash
		if err := r.Update(ctx, podCopy); err != nil {
			logger.Error(err, "Failed to backfill pod config hash annotation")
		}
		return nil
	}

	currentHash, err := resources.ComputePodConfigHash(&r.container.Spec)
	if err != nil {
		return errors.Wrap(err, "failed to compute current pod config hash")
	}

	if annotatedHash == currentHash {
		return nil
	}

	logger.Info("Pod config hash mismatch detected, deleting pod for recreation",
		"annotatedHash", annotatedHash,
		"currentHash", currentHash,
		"container", r.container.Name,
	)

	if err := r.deletePod(ctx, r.pod); err != nil {
		return err
	}

	return lifecycle.NewWaitError(errors.New("pod deleted due to config hash mismatch, waiting for recreation"))
}
