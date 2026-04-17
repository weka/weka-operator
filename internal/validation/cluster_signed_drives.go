package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterSignedDrives rejects WekaClusters where driveContainers × numDrives
// exceeds the count of signed, non-blocked drives across matched drive-role
// nodes. Bootstrap-skipped when no node has either weka-full-drives or
// weka-shared-drives annotation set (sign-drives hasn't run yet).
type clusterSignedDrives struct{}

func (clusterSignedDrives) ID() string {
	return "cluster_signed_drives"
}

func (clusterSignedDrives) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	driveContainers := cluster.Spec.Dynamic.DriveContainers
	numDrives := cluster.Spec.Dynamic.NumDrives
	if driveContainers <= 0 || numDrives <= 0 {
		return nil
	}

	fldPath := field.NewPath("spec", "dynamic").Child("numDrives")

	selector := cluster.GetNodeSelectorForRole("drive")
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
		return field.ErrorList{
			field.InternalError(fldPath, fmt.Errorf("listing drive-role nodes: %w", err)),
		}
	}
	if len(nodes.Items) == 0 {
		return nil
	}

	anyAnnotated := false
	for i := range nodes.Items {
		n := &nodes.Items[i]
		_, full := n.Annotations[consts.AnnotationWekaFullDrives]
		_, shared := n.Annotations[consts.AnnotationSharedDrives]
		if full || shared {
			anyAnnotated = true
			break
		}
	}
	if !anyAnnotated {
		return nil
	}

	getter := allocator.NewK8sNodeInfoGetter(c)
	var available int
	for i := range nodes.Items {
		name := nodes.Items[i].Name
		info, err := getter(ctx, weka.NodeName(name))
		if err != nil {
			return field.ErrorList{
				field.InternalError(fldPath, fmt.Errorf("reading drive info for node %q: %w", name, err)),
			}
		}
		available += len(info.AvailableDrives) + len(info.SharedDrives)
	}

	requested := driveContainers * numDrives
	if requested <= available {
		return nil
	}

	detail := fmt.Sprintf(
		"spec.dynamic.driveContainers × numDrives (%d × %d = %d) exceeds the "+
			"total signed and non-blocked drives across %d matched drive node(s) (%d). "+
			"Some drive containers will not be able to claim a drive. Reduce numDrives, "+
			"reduce driveContainers, sign more drives, or label more nodes.",
		driveContainers, numDrives, requested, len(nodes.Items), available,
	)
	return field.ErrorList{
		field.Invalid(fldPath, numDrives, detail),
	}
}
