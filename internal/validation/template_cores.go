package validation

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// templateCoreSides totals the drive-side and compute-side cores a template commits to, resolving
// unset cores the way the reconciler does (GetWekaContainerCores, unset -> 1).
//
// ok is false whenever those totals would not describe the cluster that actually runs, which is the
// precondition every core-sizing rule shares:
//   - nil template — auto-full-drives mode, sized per node rather than from the template;
//   - planner-managed — under clusterCapacity computeCores/driveCores left at 0 mean "auto-derive"
//     (funcs_fd_planning.go) and compute containers are built from plan.ComputeCores/plan.ComputeLayout,
//     so GetWekaContainerCores' template defaults would compare numbers the cluster never uses;
//   - either container count unset — nothing to multiply, the count is derived elsewhere.
func templateCoreSides(config *weka.WekaClusterTemplate) (driveSide, computeSide int, ok bool) {
	if config == nil || allocator.IsPlannerManaged(config) {
		return 0, 0, false
	}
	if config.DriveContainers <= 0 || config.ComputeContainers <= 0 {
		return 0, 0, false
	}
	cores := allocator.GetWekaContainerCores(config)
	return config.DriveContainers * cores.Drive, config.ComputeContainers * cores.Compute, true
}
