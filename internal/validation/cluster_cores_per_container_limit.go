package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// clusterCoresPerContainerLimit rejects driveCores/computeCores above
// CapacityPlanner.MaxCoresPerContainer (default 19), the most cores a weka container may hold. This is
// an admission policy rather than a CEL/schema Maximum bound: a schema bound would block any edit to an
// already-over-limit running cluster, and can't track the Helm-configurable limit the planners use.
// MaxCoresPerContainer <= 0 disables the cap, matching planner behavior.
type clusterCoresPerContainerLimit struct{}

func (clusterCoresPerContainerLimit) ID() string {
	return "cluster_cores_per_container_limit"
}

func (clusterCoresPerContainerLimit) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	limit := globalconfig.Config.CapacityPlanner.MaxCoresPerContainer
	if limit <= 0 {
		return nil
	}

	config := cluster.Spec.Dynamic
	// Only the two planner-managed roles are checked; protocol roles are out of scope until confirmed to apply the same way.
	checks := []struct {
		field string
		cores int
	}{
		{"driveCores", config.DriveCores},
		{"computeCores", config.ComputeCores},
	}

	var out field.ErrorList
	for _, c := range checks {
		if c.cores <= limit { // also covers unset (0)
			continue
		}
		detail := fmt.Sprintf(
			"spec.dynamicTemplate.%s (%d) exceeds the per-container core limit of %d — a single weka "+
				"container cannot hold more cores than that. Lower %s to at most %d, or add containers "+
				"(raise %s) to spread the cores across more of them.",
			c.field, c.cores, limit, c.field, limit, containerCountFieldFor(c.field),
		)
		out = append(out, field.Invalid(field.NewPath("spec", "dynamicTemplate", c.field), c.cores, detail))
	}
	return out
}

func containerCountFieldFor(coresField string) string {
	if coresField == "driveCores" {
		return "driveContainers"
	}
	return "computeContainers"
}
