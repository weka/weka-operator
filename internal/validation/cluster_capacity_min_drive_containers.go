package validation

import (
	"context"
	"fmt"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

// clusterCapacityMinDriveContainers warns when clusterCapacity's structural lower bound — the protection
// scheme's failure-domain floor (StripeWidth+RedundancyLevel+HotSpare) — sits below
// FormClusterMinDriveContainers. clusterCapacity derives the drive-container count from a capacity
// target rather than a spec field, so a plan as small as that floor is legitimate; if the floor is
// below the form-cluster minimum, the planner may hand admission nothing pinned to check while weka
// itself refuses to form the cluster, and it loops on MinContainersNotReady forever with healthy but
// idle containers — the same silent hang cluster_min_containers and cluster_auto_full_drives_min_nodes
// exist to prevent, in the one mode neither covers.
type clusterCapacityMinDriveContainers struct{}

func (clusterCapacityMinDriveContainers) ID() string { return "cluster_capacity_min_drive_containers" }

func (clusterCapacityMinDriveContainers) Validate(_ context.Context, _ client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*wekav1alpha1.WekaCluster)
	if !ok || cluster.Spec.Dynamic == nil || !cluster.Spec.Dynamic.UsesClusterCapacity() {
		return nil
	}
	minDrive := globalconfig.Consts.FormClusterMinDriveContainers
	if minDrive <= 0 { // minimum disabled by configuration — nothing to enforce
		return nil
	}
	if cluster.Spec.Dynamic.DriveContainers > 0 { // pinned counts are covered (as an Error) by cluster_min_containers
		return nil
	}

	specSW, specRL, specHS := cluster.Spec.StripeWidth, cluster.Spec.RedundancyLevel, cluster.Spec.HotSpare
	sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(specSW, specRL, specHS)
	// Below the protection floor the failure-domain math is degenerate — the shipped chart leaves
	// PROTECTION_* at 0, so an unset spec resolves to 0+0+0 and would be reported as a "floor of 0 drive
	// containers" with a remedy to lower the minimum to 0. clusterCapacityProtection already rejects
	// those schemes outright; same guard as clusterCapacityChunkFeasibility.
	minSW, minRL, minHS := allocator.MinProtectionFloor()
	if sw < minSW || rl < minRL || hs < minHS {
		return nil
	}
	minFd := capacityplanner.ProtectionScheme{StripeWidth: sw, RedundancyLevel: rl, HotSpare: hs}.MinFdNum()
	if minFd >= minDrive {
		return nil
	}

	// Report the raw spec value as the bad value (what the API client set), but check and message the
	// effective one — same convention as cluster_capacity_protection.
	return field.ErrorList{field.Invalid(field.NewPath("spec", "stripeWidth"), specSW, fmt.Sprintf(
		"clusterCapacity derives the drive-container count from the capacity target, not from a spec field; "+
			"its only structural lower bound is the protection scheme's failure-domain floor. With the "+
			"effective protection stripeWidth=%d, redundancyLevel=%d, hotSpare=%d, that floor is %d drive "+
			"container(s), below FormClusterMinDriveContainers=%d — the planner may legitimately derive a plan "+
			"as small as %d drive containers, and the cluster would then wait forever on MinContainersNotReady "+
			"with its containers running but idle. Raise stripeWidth/redundancyLevel/hotSpare (or their "+
			"PROTECTION_STRIPE_WIDTH/PROTECTION_REDUNDANCY_LEVEL/PROTECTION_HOT_SPARE Helm defaults) so the "+
			"floor reaches %d, or pin spec.dynamicTemplate.driveContainers to at least %d, or lower "+
			"FORM_CLUSTER_MIN_DRIVE_CONTAINERS to at most %d.",
		sw, rl, hs, minFd, minDrive, minFd, minDrive, minDrive, minFd))}
}
