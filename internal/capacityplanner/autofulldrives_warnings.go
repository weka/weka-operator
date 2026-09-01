package capacityplanner

import (
	"fmt"
	"strings"
)

// autofulldrives_warnings.go is where every auto-full-drives planner Warning is worded. Each condition
// gets exactly one Warning per planning pass, naming every affected node, because the controller throttles
// events on reason alone: a second Warning under the same reason would be silently dropped for the whole
// window rather than reported. The walk in autofulldrives.go collects nodes per condition and calls one
// formatter here after it completes.

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
// in nodes ("h1-2-a (cordoned)"), so unlike stranding and placement-deferral there is no per-cause branching
// to do here.
func formatIneligibleWarning(nodes []string, freeDrives int) Warning {
	return fleetWarning(WarningKindNodeIneligible,
		"auto full drives: %d node(s) holding %d signed free full drive(s) are ineligible for a new drive "+
			"container: %s; anything already running on them keeps running and still grows",
		len(nodes), freeDrives, listNodes(nodes))
}

// formatPlacementDeferredWarning renders the aggregated PlacementDeferred message, one warning covering both
// deferral causes (unscheduled pod, container being deleted) since both map to the single reason
// AutoFullDrivesPlacementDeferred, whose throttle key ignores the message — two warnings would let one
// silently suppress the other.
func formatPlacementDeferredWarning(deferred, deleting []string) Warning {
	// Two causes share one message, so halve the budget rather than let each spend the full cap.
	limit := autoFullDrivesMaxNamedNodes
	if len(deferred) > 0 && len(deleting) > 0 {
		limit = autoFullDrivesMaxNamedNodes / 2
	}

	var clauses []string
	if len(deferred) > 0 {
		clauses = append(clauses, fmt.Sprintf(
			"pod not scheduled yet, growth waits for the scheduler: %s", listNodesCapped(deferred, limit)))
	}
	if len(deleting) > 0 {
		clauses = append(clauses, fmt.Sprintf(
			"a this-cluster drive container is still being deleted, new placement waits for it: %s", listNodesCapped(deleting, limit)))
	}
	retry := "it retries automatically"
	if len(clauses) > 1 {
		retry = "both retry automatically"
	}
	return fleetWarning(WarningKindTransient,
		"auto full drives: placement deferred on %d node(s) this pass; %s; %s",
		len(deferred)+len(deleting), strings.Join(clauses, "; "), retry)
}
