package wekacontainer

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/drivers"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *containerReconcilerLoop) refreshPod(ctx context.Context) error {
	ctx, spanLogger := instrumentation.CreateLogSpan(ctx, "refreshPod")
	defer spanLogger.End()

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
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	// Creating the pod anyway for a mode that does not tolerate the cordon would leave it Pending until
	// deletePodIfUnschedulable reaps it, then recreate it — a churn loop, not a recovery.
	if NodeIsUnschedulable(r.node) && !resources.ToleratesCordon(container, r.scheduling) {
		err := errors.Errorf("node %s is unschedulable, cannot create pod", r.node.Name)
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
	}

	nodeInfo := &discovery.DiscoveryNodeInfo{}
	var err error
	var nodeAffinity weka.NodeName

	if !container.IsDiscoveryContainer() {
		// nodeName can be already set in the spec
		nodeAffinity = container.GetNodeAffinity()

		if nodeAffinity == "" {
			var node *v1.Node
			node, err = r.pickMatchingNode(ctx)
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
		var canUpgrade bool
		canUpgrade, err = r.upgradeConditionsPass(ctx)
		if err != nil || !canUpgrade {
			logger.Info("Cannot upgrade to new image, using last applied", "image", image, "error", err)
			image = container.Status.LastAppliedImage
		}
	}

	image, err = r.prepareContainerForPod(ctx, container, nodeAffinity, image)
	if err != nil {
		return err
	}

	desiredPod, err := r.buildPod(ctx, container, nodeInfo, image)
	if err != nil {
		return err
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

	if refErr := ctrl.SetControllerReference(container, desiredPod, r.Scheme); refErr != nil {
		return errors.Wrapf(refErr, "Error setting controller reference")
	}

	// Protect backend pods from being force-removed (manually or automatically) before the operator is
	// ready — e.g. a drive pod deleted mid-drain would drop its drive before the data rebuilds. The
	// finalizer keeps the pod object present so a delete only marks it Terminating; the operator strips
	// it in deletePod once it is ready to remove the pod. Backends only: aux/one-off pods are cleaned up
	// via paths that don't route through deletePod, so they must not carry it.
	if container.IsBackend() {
		controllerutil.AddFinalizer(desiredPod, consts.WekaFinalizer)
	}

	if createErr := r.Create(ctx, desiredPod); createErr != nil {
		return errors.Wrap(createErr, "Failed to create pod")
	}
	r.pod = desiredPod
	err = r.refreshPod(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (r *containerReconcilerLoop) buildPod(ctx context.Context, container *weka.WekaContainer, nodeInfo *discovery.DiscoveryNodeInfo, image string) (*v1.Pod, error) {
	var ff *domain.FeatureFlags
	if container.Spec.Mode == weka.WekaContainerModeSSDProxy {
		var err error
		if ff, err = r.GetFeatureFlags(ctx); err != nil {
			return nil, errors.Wrap(err, "failed to get feature flags")
		}
	}

	pod, err := resources.NewPodFactory(container, nodeInfo, ff, r.scheduling).Create(ctx, &image)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to create pod spec")
	}
	setPodConfigHash(pod)
	return pod, nil
}

// prepareContainerForPod mutates container in place (join IPs, drivers-builder instructions) and returns the image to build the pod with.
func (r *containerReconcilerLoop) prepareContainerForPod(ctx context.Context, container *weka.WekaContainer, nodeAffinity weka.NodeName, image string) (string, error) {
	// refresh container join ips (if there are any)
	if len(container.Spec.JoinIps) > 0 {
		ownerRef := container.GetOwnerReferences()
		if len(ownerRef) == 0 {
			return "", errors.New("no owner reference found")
		}
		owner := ownerRef[0]

		joinIps, _ := services.ClustersCachedInfo.GetJoinIps(ctx, string(owner.UID), owner.Name, container.Namespace) //nolint:errcheck // error return value intentionally not checked
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
			if getErr := r.Get(ctx, client.ObjectKey{Name: string(nodeAffinity)}, node); getErr != nil {
				return "", errors.Wrap(getErr, "failed to get target node for drivers-builder")
			}
			var err error
			image, err = drivers.GetBuilderImageForNode(node)
			if err != nil {
				return "", errors.Wrap(err, "failed to pick drivers-builder image")
			}
		}

		forceBuilderCli := services.GetSettings(ctx).Drivers.ForceBuilderCli

		builderFlags, ffErr := r.GetFeatureFlags(ctx)
		if ffErr != nil {
			return "", errors.Wrap(ffErr, "failed to get feature flags for drivers-builder")
		}

		payloadBytes, _ := json.Marshal(map[string]string{ //nolint:errcheck // error return value intentionally not checked
			"targetImage": container.Spec.Image,
			"cliImage":    drivers.GetBuilderCliImage(builderFlags, container.Spec.Image, image, forceBuilderCli),
		})
		container.Spec.Instructions = &weka.Instructions{
			Type:    weka.InstructionCopyWekaFilesToDriverLoader,
			Payload: string(payloadBytes),
		}
	}

	return image, nil
}

func (r *containerReconcilerLoop) deletePodIfNodeInfoMismatch(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

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

func (r *containerReconcilerLoop) handleImageMismatch(ctx context.Context) error {
	if r.container.Status.LastAppliedImage != "" && r.IsNotAlignedImage() {
		return r.handleImageUpdate(ctx)
	}
	return nil
}
