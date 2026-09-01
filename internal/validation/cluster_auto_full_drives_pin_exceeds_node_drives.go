package validation

import (
	"context"
	"fmt"
	"sort"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
)

// clusterAutoFullDrivesPinExceedsNodeDrives rejects an auto-full-drives numDrives or driveCores pin
// above a drive-role node's signed full drive count: numDrives selects that many of the node's largest
// drives, and weka runs at most one drive core per physical drive, so neither can be honored where
// fewer are signed. The planner then reports the whole cluster infeasible and creates nothing. Both
// legs name the fewest-drives node.
//
// The driveCores leg is skipped under a numDrives pin — CEL (numDrives >= driveCores) already covers
// that comparison. A driveCores pin BELOW the drive count is deliberately not reported: drives are
// decoupled from cores, so the node keeps every drive and runs them on fewer cores.
type clusterAutoFullDrivesPinExceedsNodeDrives struct{}

func (clusterAutoFullDrivesPinExceedsNodeDrives) ID() string {
	return "cluster_auto_full_drives_pin_exceeds_node_drives"
}

func (clusterAutoFullDrivesPinExceedsNodeDrives) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	// No nil guard on Spec.Dynamic: a nil template IS auto-full-drives mode, it just carries no pins,
	// so both legs fall through on their own (DriveCores/NumDrives read as 0).
	config := cluster.Spec.Dynamic
	if !config.UsesAutoFullDrives() {
		return nil
	}
	var driveCores, numDrives int
	if config != nil {
		driveCores, numDrives = config.DriveCores, config.NumDrives
	}
	if driveCores <= 0 && numDrives <= 0 {
		return nil
	}

	fldPath := field.NewPath("spec", "dynamicTemplate")

	nodes, errs := listDriveRoleNodes(ctx, c, cluster, fldPath)
	if errs != nil {
		return errs
	}
	if len(nodes) == 0 {
		return nil
	}

	// Only the full-drives annotation carries the information needed; no annotated node means
	// sign-drives hasn't run yet — a pre-signing state clusterDrivesUnsignedAdvisory already owns.
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

	// Sort by name so the "worst node" pick is deterministic on ties.
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })

	infos, errs := driveRoleNodeInfos(nodes, fldPath)
	if errs != nil {
		return errs
	}

	// worst* track the fewest-drives offender per leg; -1 = no offender seen yet.
	coresAffected, coresWorstCount := 0, -1
	var coresWorstNode string
	drivesAffected, drivesWorstCount := 0, -1
	var drivesWorstNode string

	for _, ni := range infos {
		if _, full := ni.Node.Annotations[consts.AnnotationWekaFullDrives]; !full {
			continue
		}
		signed := len(ni.Info.AvailableDrives)
		if signed == 0 {
			continue
		}

		if numDrives > 0 && numDrives > signed {
			drivesAffected++
			if drivesWorstCount == -1 || signed < drivesWorstCount {
				drivesWorstCount = signed
				drivesWorstNode = ni.Node.Name
			}
		}

		// Effective drive count is the pin when set, else everything the node signed. Skipped
		// entirely under a numDrives pin — CEL owns the numDrives >= driveCores comparison.
		if driveCores > 0 && numDrives <= 0 && driveCores > signed {
			coresAffected++
			if coresWorstCount == -1 || signed < coresWorstCount {
				coresWorstCount = signed
				coresWorstNode = ni.Node.Name
			}
		}
	}

	var out field.ErrorList
	if drivesAffected > 0 {
		detail := fmt.Sprintf(
			"spec.dynamicTemplate.numDrives (%d) exceeds node %q's %d signed full drive(s) — the worst "+
				"(fewest-drives) of %d affected node(s). numDrives pins how many of each node's largest "+
				"drives the cluster takes, so it cannot be honored where fewer are signed: the whole plan "+
				"is reported infeasible (AutoFullDrivesInfeasible) and no container is created anywhere. "+
				"Lower numDrives to at most %d, drop the pin so each node contributes every drive it has "+
				"signed, sign more drives on that node, or remove it from the drive-role node selector "+
				"(spec.roleNodeSelector.drive, or spec.nodeSelector when that is unset).",
			numDrives, drivesWorstNode, drivesWorstCount, drivesAffected, drivesWorstCount,
		)
		out = append(out, field.Invalid(fldPath.Child("numDrives"), numDrives, detail))
	}
	if coresAffected > 0 {
		detail := fmt.Sprintf(
			"spec.dynamicTemplate.driveCores (%d) exceeds node %q's %d signed full drive(s) — the worst "+
				"(fewest-drives) of %d affected node(s). Full-drives mode runs at most one drive core per "+
				"physical drive, so the pin cannot be satisfied there: the whole plan is reported "+
				"infeasible (AutoFullDrivesInfeasible) and no container is created anywhere. Lower "+
				"driveCores to at most %d, drop the pin so cores are derived per node, or switch to "+
				"drive-sharing (containerCapacity or clusterCapacity) to run more cores than drives.",
			driveCores, coresWorstNode, coresWorstCount, coresAffected, coresWorstCount,
		)
		out = append(out, field.Invalid(fldPath.Child("driveCores"), driveCores, detail))
	}
	return out
}
