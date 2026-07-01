package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterCapacityProtection rejects WekaCluster specs that use clusterCapacity
// with a protection scheme below the production 3+2+0 minimum (stripeWidth>=3,
// redundancyLevel>=2, hotSpare>=0 / hot spare optional). Any lower stripeWidth or
// redundancyLevel produces a degenerate or unbootable cluster. The floor is relaxed
// to single-parity 2+1+0 when the operator-level AllowSingleParity flag is set
// (QA/test only) — see allocator.MinProtectionFloor.
type clusterCapacityProtection struct{}

func (clusterCapacityProtection) ID() string { return "cluster_capacity_protection" }

func (clusterCapacityProtection) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok || cluster.Spec.Dynamic == nil || !cluster.Spec.Dynamic.UsesClusterCapacity() {
		return nil
	}
	var errs field.ErrorList
	sw, rl, hs := cluster.Spec.StripeWidth, cluster.Spec.RedundancyLevel, cluster.Spec.HotSpare
	minSW, minRL, minHS := allocator.MinProtectionFloor()
	if sw < minSW {
		errs = append(errs, field.Invalid(field.NewPath("spec", "stripeWidth"), sw,
			fmt.Sprintf("clusterCapacity requires stripeWidth >= %d", minSW)))
	}
	if rl < minRL {
		errs = append(errs, field.Invalid(field.NewPath("spec", "redundancyLevel"), rl,
			fmt.Sprintf("clusterCapacity requires redundancyLevel >= %d", minRL)))
	}
	if hs < minHS {
		errs = append(errs, field.Invalid(field.NewPath("spec", "hotSpare"), hs,
			fmt.Sprintf("clusterCapacity requires hotSpare >= %d", minHS)))
	}
	return errs
}
