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

// clusterMinDrivesFeasibility rejects minNumDrives exceeding the total drive count the cluster can
// ever reach — WaitForDrivesAdd() would poll forever. Total is driveContainers×numDrives, or in
// auto-full-drives mode, the signed non-blocked full drives each drive-role-matched node contributes
// (capped per node by a numDrives pin). Unlike clusterSignedDrives, auto-full-drives has no bootstrap
// skip: zero signed drives is rejected as a real infeasibility.
type clusterMinDrivesFeasibility struct{}

func (clusterMinDrivesFeasibility) ID() string {
	return "cluster_min_drives_feasibility"
}

func (clusterMinDrivesFeasibility) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}

	minNumDrives := cluster.Spec.GetStartIoConditions().MinNumDrives
	if minNumDrives <= 0 {
		return nil
	}

	fldPath := field.NewPath("spec", "startIoConditions", "minNumDrives")

	// A nil dynamicTemplate IS auto-full-drives mode (UsesAutoFullDrives returns true on a nil receiver),
	// so it takes the per-node branch rather than falling through as "nothing configured" — which is also
	// what lets everything below dereference Dynamic unguarded.
	if cluster.Spec.Dynamic.UsesAutoFullDrives() {
		return validateMinDrivesAutoFullDrives(ctx, c, cluster, minNumDrives, fldPath)
	}

	driveContainers := cluster.Spec.Dynamic.DriveContainers
	numDrives := cluster.Spec.Dynamic.NumDrives
	if driveContainers <= 0 || numDrives <= 0 {
		return nil
	}
	total := driveContainers * numDrives
	if minNumDrives <= total {
		return nil
	}

	detail := fmt.Sprintf(
		"spec.startIoConditions.minNumDrives (%d) exceeds total drive capacity "+
			"(%d × %d = %d). The cluster will never satisfy the IO-start condition. "+
			"Reduce minNumDrives or increase driveContainers / numDrives.",
		minNumDrives, driveContainers, numDrives, total,
	)
	return field.ErrorList{
		field.Invalid(fldPath, minNumDrives, detail),
	}
}

// validateMinDrivesAutoFullDrives totals signed, non-blocked full drives (AvailableDrives only —
// full-drives mode never picks up SharedDrives, a disjoint drive-sharing population) across
// drive-role-matched nodes. A pinned numDrives caps each node's contribution: the mode takes that many
// of a node's largest drives and leaves the rest, so summing the raw counts would over-state the
// reachable total and let an unsatisfiable minNumDrives through.
func validateMinDrivesAutoFullDrives(ctx context.Context, c client.Client, cluster *weka.WekaCluster, minNumDrives int, fldPath *field.Path) field.ErrorList {
	nodes, errs := listDriveRoleNodes(ctx, c, cluster, fldPath)
	if errs != nil {
		return errs
	}
	if len(nodes) == 0 {
		return nil
	}

	infos, errs := driveRoleNodeInfos(nodes, fldPath)
	if errs != nil {
		return errs
	}
	perNodeCap := 0 // 0 = unpinned, take everything the node signed
	if cluster.Spec.Dynamic != nil {
		perNodeCap = cluster.Spec.Dynamic.NumDrives
	}
	var total int
	for _, ni := range infos {
		n := len(ni.Info.AvailableDrives)
		if perNodeCap > 0 {
			n = min(n, perNodeCap)
		}
		total += n
	}

	if minNumDrives <= total {
		return nil
	}

	// total == 0: sign-drives hasn't run yet. Still a genuine infeasibility, but name the actual
	// cause rather than the generic "exceeds N drives" wording.
	pinNote := ""
	if perNodeCap > 0 {
		pinNote = fmt.Sprintf(", each node capped at the pinned numDrives=%d", perNodeCap)
	}
	detail := fmt.Sprintf(
		"spec.startIoConditions.minNumDrives (%d) exceeds the total signed, non-blocked full drives "+
			"the cluster can claim across %d matched drive-role node(s)%s (%d). The cluster will never "+
			"satisfy the IO-start condition. Reduce minNumDrives, raise or unset numDrives, sign more "+
			"drives, or label more nodes.",
		minNumDrives, len(nodes), pinNote, total,
	)
	if total == 0 {
		detail = fmt.Sprintf(
			"spec.startIoConditions.minNumDrives (%d) cannot be satisfied: none of the %d matched "+
				"drive-role node(s) has any signed, non-blocked full drive (no %s annotation, or "+
				"every drive is blocked), so the cluster has no drives to consume and the IO-start "+
				"condition would never be met. Sign drives on the drive-role nodes before applying "+
				"the cluster, label more nodes, or unset minNumDrives.",
			minNumDrives, len(nodes), consts.AnnotationWekaFullDrives,
		)
	}
	return field.ErrorList{
		field.Invalid(fldPath, minNumDrives, detail),
	}
}
