package wekacontainer

import (
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/resources"
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

	// For drivers-builder containers, determine the builder image based on the target node's OS
	if container.IsDriversBuilder() {
		err = r.adjustPodBeforeCreate(ctx, desiredPod, nodeAffinity)
		if err != nil {
			return err
		}
		//BuilderPodAdjustments(desiredPod, node.Status.NodeInfo.OSImage)
		//logger.Info("Determined builder image for drivers-builder", "osImage", node.Status.NodeInfo.OSImage, "builderImage", image)
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

//// DetermineBuilderImage returns the appropriate builder image based on the node's OS.
//func BuilderPodAdjustments(pod *v1.Pod, osImage string) {
//	switch {
//	case strings.Contains(osImage, "Ubuntu 24.04"):
//		pod.Spec.Containers
//		return "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"
//	case strings.Contains(osImage, "Ubuntu 22.04"):
//		return "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
//	default:
//		return "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
//	}
//}

// addInitContainer adds an init container to the given pod.
func addInitContainer(pod *v1.Pod, name, image string, command []string) {
	initContainer := v1.Container{
		Name:    name,
		Image:   image,
		Command: command,
	}
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, initContainer)
}

// adjustPodBeforeCreate modifies the pod spec before creation (e.g., image overrides, init containers).
func (r *containerReconcilerLoop) adjustPodBeforeCreate(ctx context.Context, pod *v1.Pod,
	nodeAffinity weka.NodeName) error {
	node := &v1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: string(nodeAffinity)}, node); err != nil {
		return errors.Wrap(err, "failed to get target node for drivers-builder")
	}
	osImage := node.Status.NodeInfo.OSImage

	switch {
	case strings.Contains(osImage, "Ubuntu 24.04"):
		pod.Spec.Containers[0].Image = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu24"
		CopyWekaCliToMainContainer(pod)
		//addInitContainer(pod, "copy-cli", config.Config.DefaultCliContainer, []string{})
	case strings.Contains(osImage, "Ubuntu 22.04"):
		pod.Spec.Containers[0].Image = "quay.io/weka.io/weka-drivers-build-images:builder-ubuntu22"
	default:
	}

	return nil
}

// copy weka dist files if drivers-loader image is
// different from cluster image
func CopyWekaCliToMainContainer(pod *v1.Pod) {

	sharedVolumeName := "shared-weka-cli"
	sharedVolumeMountPath := "/shared-weka-cli"

	pod.Spec.InitContainers = append(pod.Spec.InitContainers, v1.Container{
		Name:    "init-copy-cli",
		Image:   config.Config.DefaultCliContainer,
		Command: []string{"sh", "-c"},
		Args: []string{
			`
					cp /usr/bin/weka /shared-weka-cli/
					echo "Init container copy cli completed successfully"
					`,
		},
		VolumeMounts: []v1.VolumeMount{
			{
				Name:      sharedVolumeName,
				MountPath: sharedVolumeMountPath,
			},
		},
	})

	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, v1.VolumeMount{
		Name:      sharedVolumeName,
		MountPath: sharedVolumeMountPath,
	})
	pod.Spec.Volumes = append(pod.Spec.Volumes, v1.Volume{

		Name: sharedVolumeName,
		VolumeSource: v1.VolumeSource{
			EmptyDir: &v1.EmptyDirVolumeSource{},
		},
	})
}
