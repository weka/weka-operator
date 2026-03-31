package wekacontainer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
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

	// Annotate with spec version for drift detection.
	if specVer := targetSpecVersion(container); specVer != "" {
		if desiredPod.Annotations == nil {
			desiredPod.Annotations = make(map[string]string)
		}
		desiredPod.Annotations[consts.PodSpecVersionAnnotation] = specVer
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

// selfCalcSpecVersion returns the spec version computed from the image, pod config version, and code constant.
func selfCalcSpecVersion(image string) string {
	raw := image + "|" + config.Config.PodConfigVersion + "|" + consts.WekaRuntimeVersion
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", hash)[:12]
}

// targetSpecVersion returns the spec version that should be applied to the pod.
// If container.Spec.SpecVersion is non-empty it is used directly (allows explicit per-container control).
// For ownerless containers with no explicit SpecVersion, the version is self-calculated from the image,
// the global helm podConfigVersion and the in-code WekaRuntimeVersion.
// For containers with owners and no explicit SpecVersion, an empty string is returned (no spec-version tracking).
func targetSpecVersion(container *weka.WekaContainer) string {
	if container.Spec.SpecVersion != "" {
		return container.Spec.SpecVersion
	}
	if len(container.GetOwnerReferences()) == 0 {
		return selfCalcSpecVersion(container.Spec.Image)
	}
	return ""
}

// handleSpecVersionMismatch combines image-upgrade handling with pod spec-version drift detection.
// It runs image upgrade operations first (when the image is mismatched), then checks whether the
// running pod's weka.io/spec-version annotation matches the expected value. If the spec version has
// drifted the pod is deleted so it will be recreated with the correct spec.
func (r *containerReconcilerLoop) handleSpecVersionMismatch(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// Run image-upgrade operations first when the image is mismatched.
	if r.container.Status.LastAppliedImage != "" && r.IsNotAlignedImage() {
		if err := r.handleImageUpdate(ctx); err != nil {
			return err
		}
	}

	target := targetSpecVersion(r.container)
	if target == "" {
		return nil
	}

	podAnnotation := r.pod.Annotations[consts.PodSpecVersionAnnotation]

	if podAnnotation == "" {
		if !config.Config.AllowRotateNonAnnotated {
			return nil
		}
		logger.Info("Pod missing spec-version annotation, rotating due to allowRotateNonAnnotated")
	} else if podAnnotation == target {
		// Spec version matches — record the applied version in status if not already set.
		if r.container.Status.LastAppliedSpec != target {
			r.container.Status.LastAppliedSpec = target
			return r.Status().Update(ctx, r.container)
		}
		return nil
	} else {
		logger.Info("Pod spec-version mismatch, deleting for recreation",
			"podSpecVersion", podAnnotation, "targetSpecVersion", target)
	}

	if err := r.deletePod(ctx, r.pod); err != nil {
		return err
	}
	return lifecycle.NewWaitError(errors.New("pod deleted due to spec-version mismatch, waiting for recreation"))
}
