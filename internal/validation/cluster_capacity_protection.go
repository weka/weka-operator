package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterCapacityProtection rejects clusterCapacity specs below the production 3+2+0 protection floor
// (stripeWidth/redundancyLevel/hotSpare); AllowSingleParity relaxes this to 2+1+0 for QA (see allocator.MinProtectionFloor).
type clusterCapacityProtection struct{}

func (clusterCapacityProtection) ID() string { return "cluster_capacity_protection" }

func (clusterCapacityProtection) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok || cluster.Spec.Dynamic == nil || !cluster.Spec.Dynamic.UsesClusterCapacity() {
		return nil
	}
	var errs field.ErrorList
	specSW, specRL, specHS := cluster.Spec.StripeWidth, cluster.Spec.RedundancyLevel, cluster.Spec.HotSpare
	sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(specSW, specRL, specHS)
	minSW, minRL, minHS := allocator.MinProtectionFloor()
	// Report the raw spec value as the bad value (what the API client set), but check and message the effective one.
	if sw < minSW {
		errs = append(errs, field.Invalid(field.NewPath("spec", "stripeWidth"), specSW,
			fmt.Sprintf("clusterCapacity requires stripeWidth >= %d (effective value %d; raise spec.stripeWidth to >= %d, or leave spec.stripeWidth=0 to fall back to the PROTECTION_STRIPE_WIDTH default — which must itself be >= %d)", minSW, sw, minSW, minSW)))
	}
	if rl < minRL {
		errs = append(errs, field.Invalid(field.NewPath("spec", "redundancyLevel"), specRL,
			fmt.Sprintf("clusterCapacity requires redundancyLevel >= %d (effective value %d; raise spec.redundancyLevel to >= %d, or leave spec.redundancyLevel=0 to fall back to the PROTECTION_REDUNDANCY_LEVEL default — which must itself be >= %d)", minRL, rl, minRL, minRL)))
	}
	if hs < minHS {
		errs = append(errs, field.Invalid(field.NewPath("spec", "hotSpare"), specHS,
			fmt.Sprintf("clusterCapacity requires hotSpare >= %d (effective value %d; raise spec.hotSpare to >= %d, or leave spec.hotSpare=0 to fall back to the PROTECTION_HOT_SPARE default — which must itself be >= %d)", minHS, hs, minHS, minHS)))
	}
	return errs
}
