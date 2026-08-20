// Package validation holds admission-validation rules for Weka CRDs. Validators are context-free —
// the caller decides what to do with the returned violations.
//
// # Sizing modes
//
// A WekaCluster's sizing mode is derived from which spec.dynamicTemplate fields are set, not named by
// a field. The four are mutually exclusive by precedence, in the order listed here — the same order
// derivedSizingMode resolves them in (cluster_sizing_mode_flip.go). This is the validation-side split;
// the controller's plannerSizingMode (steps_planner_apply.go) is coarser and resolves in the opposite
// order — it has no drive-sharing value, folding it into sizingCountBased. Each rule is scoped to the
// modes whose numbers it can actually read:
//
//	auto-full-drives   no container count and no capacity field set (a nil template included) — one
//	                   container per eligible node, both counts 0, every signed drive claimed.
//	                   numDrives/driveCores pins are legal here and stay in this mode.
//	clusterCapacity    the planner derives counts and cores from a capacity target
//	drive-sharing      containerCapacity or driveCapacity set — numDrives counts virtual drives
//	counts             driveContainers/computeContainers pinned
//
// # Naming configuration knobs
//
// In a user-facing message, name the Helm value a knob is set through (protection.stripeWidth,
// clusterCapacity.tlcCapacityPerCoreGiB), never the env var it feeds. A knob with no Helm value —
// FORM_CLUSTER_MIN_* is templated nowhere in the chart — is not named at all: the reader cannot
// reach it through the chart. Code comments may name the env var, which is what env.go reads.
//
// # Rule ownership
//
// Rules that read the same fields would otherwise report one condition two or three times per apply.
// Each condition has a single owner, and the others return nil in its territory:
//
//   - Form-cluster container floor: cluster_min_containers (pinned counts) /
//     cluster_capacity_min_drive_containers (clusterCapacity, unpinned) /
//     cluster_auto_full_drives_min_nodes (auto-full-drives, where node count IS container count).
//   - Drive count vs signed drives: cluster_signed_drives (exclusive full-drives) /
//     cluster_auto_full_drives_pin_exceeds_node_drives (auto-full-drives pins) / the
//     cluster_capacity_* rules (drive-sharing, where numDrives counts virtual drives).
//   - Nothing signed yet: cluster_drives_unsigned_advisory. Drive rules bootstrap-skip to it, except
//     cluster_min_drives_feasibility in auto-full-drives mode, which treats zero signed drives as a
//     real infeasibility rather than a pre-signing state.
//   - Selector matches too few nodes: cluster_selected_nodes_count (against pinned counts) /
//     cluster_auto_full_drives_min_nodes (against the form-cluster floor).
//   - Compute vs drive cores: cluster_compute_drive_cores_floor owns the hard 1:1 violation;
//     cluster_drive_compute_core_ratio only advises on ratios above it.
//   - driveCores below the configured capacity: cluster_drive_cores_below_capacity (an explicit
//     driveCores that can be raised) / cluster_num_drives_below_required_cores (numDrives caps
//     driveCores, so no legal value exists).
//   - Sizing mode changes: cluster_sizing_mode_flip (update-only) owns every transition once drive
//     containers exist; the create-shaped rules never see the old object.
package validation
