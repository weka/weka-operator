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

func (r *containerReconcilerLoop) deletePodIfHtCpuMismatch(ctx context.Context) error {
	_, logger, end := instrumentation.GetLogSpan(ctx, "")
	defer end()

	// Prefer the container's explicit node affinity; fall back to the actual scheduled node
	// so the check works even when nodeAffinity wasn't set on the spec at creation time.
	nodeName := r.container.GetNodeAffinity()
	if nodeName == "" {
		nodeName = weka.NodeName(r.pod.Spec.NodeName)
	}
	if nodeName == "" {
		return nil
	}

	// GetNodeInfo is only reached when predicates confirmed pod is unhealthy.
	// Errors are intentionally ignored: discovery containers may fail here since
	// GetNodeInfo triggers a discovery run which they themselves serve.
	nodeInfo, err := r.GetNodeInfo(ctx, nodeName)
	if err != nil || nodeInfo == nil || !nodeInfo.IsHt {
		return nil
	}

	// HT: each main core needs 2 CPUs (sibling pair); ExtraCores count once; +1 for management.
	// ExtraCores is only set on modes that use find_full_cores (Compute/Drive/S3/NFS/DataServices),
	// which are also the only modes that can crash with "cannot find N full cores".
	expectedCPU := int64(r.container.Spec.NumCores*2 + r.container.Spec.ExtraCores + 1)

	actualCPU := r.pod.Spec.Containers[0].Resources.Requests.Cpu().Value()
	if actualCPU < expectedCPU {
		logger.Info("Pod CPU count below HT requirement, deleting for recreation",
			"actual", actualCPU, "expected", expectedCPU,
			"numCores", r.container.Spec.NumCores, "isHt", nodeInfo.IsHt)
		if err := r.deletePod(ctx, r.pod); err != nil {
			return err
		}
		return errors.New("pod deleted due to HT CPU mismatch, waiting for recreation")
	}
	return nil
}
