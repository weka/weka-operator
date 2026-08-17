package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
)

// clusterSignedDrives rejects full-drives clusters where driveContainers × numDrives exceeds signed,
// non-blocked full drives across matched drive-role nodes (bootstrap-skipped until any node carries
// weka-full-drives). Only exclusive full drives count — shared drives are carved by capacity instead.
// Out of scope by construction: auto-full-drives mode (no fixed container count — drives are claimed
// per node; clusterAutoFullDrivesPinExceedsNodeDrives owns its pins) and drive sharing (numDrives
// counts virtual drives — a category error to compare; cluster_capacity_* owns feasibility there).
type clusterSignedDrives struct{}

func (clusterSignedDrives) ID() string {
	return "cluster_signed_drives"
}

func (clusterSignedDrives) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	// A nil template is auto-full-drives mode, which the next check drops anyway; the explicit guard
	// keeps the field reads below obviously safe.
	if cluster.Spec.Dynamic == nil {
		return nil
	}
	if cluster.Spec.Dynamic.UsesAutoFullDrives() || cluster.IsDriveSharing() {
		return nil
	}

	driveContainers := cluster.Spec.Dynamic.DriveContainers
	numDrives := cluster.Spec.Dynamic.NumDrives
	if driveContainers <= 0 || numDrives <= 0 {
		return nil
	}

	fldPath := field.NewPath("spec", "dynamicTemplate").Child("numDrives")

	nodes, errs := listDriveRoleNodes(ctx, c, cluster, fldPath)
	if errs != nil {
		return errs
	}
	if len(nodes) == 0 {
		return nil
	}

	// Only the full-drives annotation gates the bootstrap skip — a drive-sharing-signed node is not
	// signed for this cluster. clusterDrivesUnsignedAdvisory covers both mismatch states.
	anyAnnotated := false
	for i := range nodes {
		if _, full := nodes[i].Annotations[consts.AnnotationWekaFullDrives]; full {
			anyAnnotated = true
			break
		}
	}
	if !anyAnnotated {
		return nil
	}

	infos, errs := driveRoleNodeInfos(nodes, fldPath)
	if errs != nil {
		return errs
	}
	var available int
	for _, ni := range infos {
		available += len(ni.Info.AvailableDrives)
	}

	requested := driveContainers * numDrives
	if requested <= available {
		return nil
	}

	detail := fmt.Sprintf(
		"spec.dynamicTemplate.driveContainers × numDrives (%d × %d = %d) exceeds the "+
			"total signed and non-blocked full drives across %d matched drive node(s) (%d). "+
			"Some drive containers will not be able to claim a drive. Reduce numDrives, "+
			"reduce driveContainers, sign more drives, or label more nodes.",
		driveContainers, numDrives, requested, len(nodes), available,
	)
	return field.ErrorList{
		field.Invalid(fldPath, numDrives, detail),
	}
}
