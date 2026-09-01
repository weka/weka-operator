package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// clusterSizingModeFlip rejects an update that changes the cluster's DERIVED sizing mode while drive
// containers already exist. The mode is implicit — it follows from which of
// computeContainers/driveContainers/clusterCapacity/containerCapacity/driveCapacity are set — so
// nothing else makes the flip loud. Left unchecked, adding driveContainers to a live auto-full-drives
// ("acts as a daemonset") cluster starts creating count-based single-drive containers alongside the
// node-sized ones already running, and the two sizing regimes fight over the same drives forever.
//
// It covers EVERY transition, not only the ones touching auto-full-drives, because nothing else does:
// the cluster_capacity_* policies are create-shaped and never see the old object, and
// cluster_capacity_chunk_feasibility deliberately disarms itself once drive containers exist. The
// capacity modes fail the same way in their own idiom — see modeFlipConsequence for each.
//
// Two switches ARE supported and are allowlisted in modeSwitchSupported.
//
// Error in BOTH modes: unlike a capacity check, there is no degraded-but-working outcome here.
// Deliberately scoped to clusters that already HAVE drive containers — before any exist the mode is
// still free to change, which is what makes fixing a mistyped spec possible.
type clusterSizingModeFlip struct{}

func (clusterSizingModeFlip) ID() string { return "cluster_sizing_mode_flip" }

func (clusterSizingModeFlip) ValidateUpdate(ctx context.Context, c client.Client, oldObj, newObj runtime.Object) field.ErrorList {
	oldCluster, ok := oldObj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	newCluster, ok := newObj.(*weka.WekaCluster)
	if !ok {
		return nil
	}

	oldMode := derivedSizingMode(oldCluster.Spec.Dynamic)
	newMode := derivedSizingMode(newCluster.Spec.Dynamic)
	if oldMode == newMode {
		return nil
	}
	if modeSwitchSupported(oldMode, newMode) {
		return nil
	}
	// Note the ordering: nothing above this point touches the API server, so an unchanged mode — the
	// overwhelmingly common edit — and a supported switch are never exposed to a List failure. Only an
	// update that genuinely flips the mode to one we cannot carry over can be blocked by one.
	hasDriveContainers, err := clusterHasDriveContainer(ctx, c, newCluster)
	if err != nil {
		// Fail CLOSED. Treating "could not list" as "no containers exist" would let an apiserver blip
		// during a kubectl edit wave through the exact change this policy blocks, and the damage —
		// count-based containers created alongside the running node-pinned ones — outlasts the blip.
		// A spurious rejection costs a retry; a spurious admission costs the cluster's topology.
		return field.ErrorList{field.InternalError(
			field.NewPath("spec", "dynamicTemplate"),
			fmt.Errorf("this update changes the cluster's derived sizing mode from %s to %s, which is "+
				"not a supported switch once drive containers exist — but listing them failed, so "+
				"the change could not be validated and is rejected rather than risked: %w. Retry the "+
				"edit; if it keeps failing, check the operator's access to WekaContainer resources in "+
				"namespace %q", oldMode, newMode, err, newCluster.Namespace),
		)}
	}
	if !hasDriveContainers {
		return nil
	}

	detail := fmt.Sprintf(
		"this update changes the cluster's derived sizing mode from %s to %s while drive containers "+
			"already exist. The mode is not a field — it follows from which sizing fields are set (%s) — "+
			"so the operator would start planning the running drive containers under different rules: "+
			"%s. Once drive containers exist the only supported switches are unsetting both "+
			"spec.dynamicTemplate.computeContainers and spec.dynamicTemplate.driveContainers, which "+
			"adopts the daemonset mode by growing the existing drive containers in place, and moving a "+
			"drive-sharing cluster to spec.dynamicTemplate.clusterCapacity.",
		oldMode, newMode, sizingModeFields, modeFlipConsequence(oldMode, newMode),
	)
	return field.ErrorList{
		field.Forbidden(field.NewPath("spec", "dynamicTemplate"), detail),
	}
}

// Derived sizing modes, in the same precedence order UsesAutoFullDrives/IsDriveSharing use.
const (
	sizingModeAutoFullDrives  = "auto-full-drives (acts as a daemonset)"
	sizingModeClusterCapacity = "clusterCapacity"
	sizingModeDriveSharing    = "drive-sharing (containerCapacity/driveCapacity)"
	sizingModeCounts          = "explicit container counts"

	sizingModeFields = "computeContainers, driveContainers, clusterCapacity, containerCapacity, driveCapacity"
)

// derivedSizingMode names the sizing regime a template selects. Nil-safe: a nil template sets none of
// the fields, so it is auto-full-drives, exactly as UsesAutoFullDrives reports.
func derivedSizingMode(d *weka.WekaClusterTemplate) string {
	switch {
	case d.UsesAutoFullDrives():
		return sizingModeAutoFullDrives
	case d.UsesClusterCapacity():
		return sizingModeClusterCapacity
	case d.ContainerCapacity > 0 || d.DriveCapacity > 0:
		return sizingModeDriveSharing
	default:
		return sizingModeCounts
	}
}

// modeSwitchSupported lists the (old -> new) pairs that are safe on a cluster that already has drive
// containers, because the new mode's planner can carry the running containers over. Every other pair
// is rejected: without adoption, and with no scale-down path anywhere in the operator, the two sizing
// regimes end up planning the same drives under different rules.
func modeSwitchSupported(oldMode, newMode string) bool {
	switch {
	case oldMode == sizingModeCounts && newMode == sizingModeAutoFullDrives:
		// Both are exclusive full-drives modes over the same physical drives. The auto-full-drives
		// planner resolves an existing container's node through GetNodeAffinity(), which falls back to
		// Status.NodeAffinity, so a scheduled count-based container — which carries no Spec.NodeAffinity
		// — is still matched to its node and grown in place to that node's full drive set, rather than
		// joined by a second population.
		return true
	case oldMode == sizingModeDriveSharing && newMode == sizingModeClusterCapacity:
		// The documented in-place migration (doc/operator/deployment/cluster-capacity.md). Both hold
		// virtual drives, and inventory.DriveContainerCapacities reads containerCapacity and
		// driveCapacity alike, so the planner grows from the running set instead of planning a fresh one.
		return true
	}
	return false
}

// modeFlipConsequence states, in the user's terms, what the operator would start doing after the flip.
// One branch per way the flip goes wrong; the supported pairs never reach here.
func modeFlipConsequence(oldMode, newMode string) string {
	switch {
	case oldMode == sizingModeAutoFullDrives:
		return "the per-node containers sized from each node's own signed drives would be joined by a " +
			"second, differently sized population planned from the new fields"
	case newMode == sizingModeAutoFullDrives:
		return "the running containers hold virtual drives that the full-drives inventory accounts for " +
			"on neither side, so they could be neither adopted nor grown, and every eligible node would " +
			"additionally get a new container sized from its own signed drives"
	case oldMode == sizingModeCounts && newMode == sizingModeClusterCapacity:
		return "the running full-drives containers report no capacity to the capacity planner, so it " +
			"would plan a fresh set covering the whole target on top of them — and would then fail to " +
			"write containerCapacity onto a container that has numDrives set, wedging reconciliation on " +
			"every pass"
	case oldMode == sizingModeClusterCapacity:
		return "the planner-placed, node-pinned containers would be abandoned — counted toward the new " +
			"container count without being resized, with unpinned count-based containers created for any " +
			"shortfall, and nothing removed if there is a surplus, since the operator never auto-shrinks"
	default:
		return "the running containers would keep the drive layout their own family gave them while the " +
			"operator sizes new ones under the other family's rules, and nothing reconciles the two"
	}
}

// clusterHasDriveContainer reports whether the cluster already has any drive container. A List failure
// is returned as an error, NOT swallowed into "none" — the caller must decide, and for a mode flip the
// safe decision is to reject.
//
// A missing UID does resolve to "none", and that is a real answer rather than a fallback: containers
// are labelled with the cluster UID, so an object that was never persisted cannot have any. On an
// UPDATE admission request the UID is always populated anyway.
func clusterHasDriveContainer(ctx context.Context, c client.Client, cluster *weka.WekaCluster) (bool, error) {
	uid := string(cluster.GetUID())
	if uid == "" {
		return false, nil
	}
	var containers weka.WekaContainerList
	if err := c.List(ctx, &containers, client.InNamespace(cluster.Namespace), client.MatchingLabels{
		domain.WekaLabelClusterId: uid,
		domain.WekaLabelMode:      weka.WekaContainerModeDrive,
	}); err != nil {
		return false, fmt.Errorf("listing the cluster's drive containers: %w", err)
	}
	return len(containers.Items) > 0, nil
}
