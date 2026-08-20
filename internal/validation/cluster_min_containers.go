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

// clusterMinContainers rejects a pinned driveContainers/computeContainers below the minimum weka needs
// to form a cluster at all (FormClusterMinDrive/ComputeContainers — 5 by default, 3 with
// ALLOW_SINGLE_PARITY). Below it FormCluster refuses to proceed and the cluster loops on
// MinContainersNotReady forever with its containers healthy but idle — a plan the planner happily
// accepts, e.g. clusterCapacity alongside a single pinned count of 3.
//
// Only explicit pins are checked, which is why auto-full-drives never reaches the body: both counts are
// 0 there by definition, so the "unset" skip fires first.
type clusterMinContainers struct{}

func (clusterMinContainers) ID() string {
	return "cluster_min_containers"
}

func (clusterMinContainers) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok {
		return nil
	}
	if cluster.Spec.Dynamic == nil {
		return nil
	}

	config := cluster.Spec.Dynamic

	checks := []struct {
		field string
		count int
		min   int
		// role is the wording used in the remedy, and differs from field for readability.
		role string
	}{
		{"driveContainers", config.DriveContainers, globalconfig.Consts.FormClusterMinDriveContainers, "drive"},
		{"computeContainers", config.ComputeContainers, globalconfig.Consts.FormClusterMinComputeContainers, "compute"},
	}

	var out field.ErrorList
	for _, c := range checks {
		if c.min <= 0 { // minimum disabled by configuration — nothing to enforce
			continue
		}
		if c.count <= 0 { // unset: derived elsewhere (see type doc)
			continue
		}
		if c.count >= c.min {
			continue
		}
		remedy := fmt.Sprintf("raise %s to at least %d", c.field, c.min)
		detail := fmt.Sprintf(
			"spec.dynamicTemplate.%s (%d) is below the %d %s container(s) weka needs to form a cluster — "+
				"the cluster would never be created, it would wait forever on MinContainersNotReady with its "+
				"containers running but idle. %s.",
			c.field, c.count, c.min, c.role, remedy,
		)
		out = append(out, field.Invalid(field.NewPath("spec", "dynamicTemplate", c.field), c.count, detail))
	}
	return out
}
