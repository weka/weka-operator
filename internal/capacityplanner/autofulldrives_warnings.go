package capacityplanner

import (
	"fmt"
	"strings"
)

// autofulldrives_warnings.go is where every auto-full-drives planner Warning is worded. Each condition gets
// exactly one Warning per planning pass, naming every affected node. Distinct conditions that share a
// WarningKind (and so the same event reason) carry distinct Cause values, since the controller throttles
// events on reason+cause: a condition with no dedicated Cause would share its throttle window with every
// other Warning of that Kind, and a second one landing inside that window would be silently dropped. The
// walk in autofulldrives.go collects nodes per condition and calls a formatter here after it completes.

func listNodes(parts []string) string { return listNodesCapped(parts, autoFullDrivesMaxNamedNodes) }

// listNodesCapped joins per-node parts, capping the list at limit with a "(+N more)" tail so a large fleet
// cannot turn one aggregated event into a multi-KB message.
func listNodesCapped(parts []string, limit int) string {
	if len(parts) <= limit {
		return strings.Join(parts, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(parts[:limit], ", "), len(parts)-limit)
}

// strandedNode is one node where a pinned dynamicTemplate.numDrives left signed full drives unused,
// collected during the walk so the whole fleet is reported in a single DrivesStranded warning.
type strandedNode struct {
	node   string
	signed int // drives signed on the node
	used   int // drives the container takes
}

// formatStrandedWarning renders the aggregated DrivesStranded message. The only cause is a pinned numDrives,
// an operator choice, hence Normal rather than Warning downstream. Aggregated because the cause is one
// fleet-wide setting: per-node fan-out would turn one condition into one near-identical Warning per node,
// each repeating the same ~300-character remedy.
func formatStrandedWarning(stranded []strandedNode, pin int) Warning {
	parts := make([]string, 0, len(stranded))
	unused := 0
	for _, s := range stranded {
		parts = append(parts, fmt.Sprintf("%s (%d of %d)", s.node, s.used, s.signed))
		unused += s.signed - s.used
	}
	return fleetWarning(WarningKindDrivesStranded,
		"auto full drives: dynamicTemplate.numDrives=%d is pinned, so each container takes only its node's %d "+
			"largest drive(s) — %d node(s) have more, leaving %d drive(s) unused in total; per node (used of "+
			"signed): %s; unset numDrives to claim every signed full drive on every node",
		pin, pin, len(stranded), unused, listNodes(parts))
}

// formatIneligibleWarning renders the aggregated NodeIneligible message. Each node's cause travels with it
// in nodes ("h1-2-a (cordoned)"), already resolved by resources.NodeIneligibleReason to one of exactly three
// values (cordoned, not ready, untolerated taint). reasons is the distinct subset actually present, sorted by
// the caller for a stable Cause — so a node going NotReady gets its own throttle window instead of sharing
// one with a fleet that was merely cordoned.
func formatIneligibleWarning(nodes []string, freeDrives int, reasons []string) Warning {
	return fleetWarningWithCause(WarningKindNodeIneligible, WarningCause(strings.Join(reasons, "+")),
		"auto full drives: %d node(s) holding %d signed free full drive(s) are ineligible for a new drive "+
			"container: %s; anything already running on them keeps running and still grows",
		len(nodes), freeDrives, listNodes(nodes))
}

// formatPlacementDeferredWarning renders one Warning per PlacementDeferred cause (unscheduled pod, drive
// container being deleted, compute container being deleted) instead of merging them: each gets its own
// Cause and so its own throttle window, and the full per-warning node-name cap rather than a share of it.
// computeBlockedBinding is the fit dimension every compute-blocked node was short of, or "" when they
// disagree (or it is unknown). It is only wording: the deferral itself fires on any binding, and on the
// create path as well as growth, so the clause must not promise hugepages or growth specifically.
func formatPlacementDeferredWarning(deferred, deleting, computeBlocked []string, computeBlockedBinding string) []Warning {
	held := "the resources"
	if computeBlockedBinding != "" {
		held = "the " + computeBlockedBinding
	}
	causes := []struct {
		nodes  []string
		cause  WarningCause
		clause string
	}{
		{deferred, CausePlacementUnscheduled, "pod not scheduled yet, growth waits for the scheduler"},
		{deleting, CausePlacementDriveDeleting,
			"a this-cluster drive container is still being deleted, new placement waits for it"},
		{computeBlocked, CausePlacementComputeDeleting,
			"a this-cluster compute container on the node is still being deleted and holds " + held +
				" this placement needs"},
	}
	var warnings []Warning
	for _, c := range causes {
		if len(c.nodes) > 0 {
			warnings = append(warnings, fleetWarningWithCause(WarningKindTransient, c.cause,
				"auto full drives: placement deferred on %d node(s) this pass; %s: %s; it retries automatically",
				len(c.nodes), c.clause, listNodes(c.nodes)))
		}
	}
	return warnings
}
