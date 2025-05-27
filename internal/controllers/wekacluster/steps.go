// package wekacluster contains the reconciliation logic for WekaCluster resources
package wekacluster

import (
	"github.com/weka/go-steps-engine/lifecycle"
)

// GetAllSteps combines all reconciliation steps into a single ordered list
func GetAllSteps(loop *wekaClusterReconcilerLoop) []lifecycle.Step {
	var steps []lifecycle.Step

	// Initial container state - should always be first
	steps = append(steps, &lifecycle.SingleStep{
		Run: loop.getCurrentContainers,
	})

	// Throttled metrics steps - can run in parallel and are isolated from main flow
	steps = append(steps, GetThrottledMetricsSteps(loop)...)

	// Deletion/creation paths are mutually exclusive
	steps = append(steps, &lifecycle.GroupedSteps{
		Name: "DeletionPath",
		Predicates: []lifecycle.PredicateFunc{
			loop.cluster.IsMarkedForDeletion,
		},
		Steps:           GetDeletionSteps(loop),
		FinishOnSuccess: true,
	})

	steps = append(steps, &lifecycle.GroupedSteps{
		Name:  "ClusterSetupSteps",
		Steps: GetClusterSetupSteps(loop),
	})

	steps = append(steps, &lifecycle.GroupedSteps{
		Name:  "ClusterCreationSteps",
		Steps: GetClusterCreationSteps(loop),
	})

	steps = append(steps, &lifecycle.GroupedSteps{
		Name:  "CredentialSteps",
		Steps: GetCredentialSteps(loop),
	})

	steps = append(steps, &lifecycle.GroupedSteps{
		Name: "PostClusterConfigSteps",
		Predicates: []lifecycle.PredicateFunc{
			lifecycle.IsNotFunc(loop.cluster.IsExpand),
		},
		Steps: GetPostClusterSteps(loop),
	})

	return steps
}
