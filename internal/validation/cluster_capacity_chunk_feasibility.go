package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// clusterCapacityChunkFeasibility rejects a brand-new (greenfield) clusterCapacity WekaCluster whose
// target + driveTypesRatio cannot spread an active pool across at least numFDmin failure domains each of
// at least MinChunkSizeGiB (384 GiB) — the reconciler would otherwise emit a recurring
// ClusterCapacityInfeasible event and never form the cluster. The planner applies the floor to TLC and
// QLC independently, so both are checked. The node-independent rule per pool is
// poolRaw >= numFDmin × 384 GiB, i.e. clusterCapacity × poolPart/(tlc+qlc) >= 384 × stripeWidth.
//
// It deliberately does NOT fire once the cluster already has TLC-bearing drive containers (an in-place
// containerCapacity->clusterCapacity migration, or an established cluster): there the existing
// containers already clear the floor and the planner only grows, so blocking the edit would be wrong.
type clusterCapacityChunkFeasibility struct{}

func (clusterCapacityChunkFeasibility) ID() string { return "cluster_capacity_chunk_feasibility" }

func (clusterCapacityChunkFeasibility) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok || cluster.Spec.Dynamic == nil || !cluster.Spec.Dynamic.UsesClusterCapacity() {
		return nil
	}

	// Resolve effective protection (spec value, else Helm default) so this greenfield gate checks the
	// same scheme the webhook accepted and the planner forms.
	sw, rl, hs := globalconfig.Config.DriveSharing.EffectiveProtection(
		cluster.Spec.StripeWidth, cluster.Spec.RedundancyLevel, cluster.Spec.HotSpare,
	)
	// The protection floor (3+2+0, or single-parity 2+1+0 when AllowSingleParity is set) is reported by
	// clusterCapacityProtection; below it the chunk math is degenerate.
	minSW, minRL, minHS := allocator.MinProtectionFloor()
	if sw < minSW || rl < minRL || hs < minHS {
		return nil
	}

	capGiB, err := cluster.Spec.Dynamic.GetClusterCapacityGiB()
	if err != nil || capGiB <= 0 {
		return nil // malformed/empty capacity is reported by CEL / parse, not here
	}

	// Migration / established path: if the cluster already has a TLC-bearing drive container, the
	// planner grows from it rather than laying out a fresh greenfield set — skip the greenfield gate.
	if clusterHasTlcDriveContainer(ctx, c, cluster) {
		return nil
	}

	raw := allocator.RawCapacityGiB(capGiB, sw, rl, hs)
	tlcRaw, qlcRaw := weka.GetTlcQlcCapacity(raw, cluster.Spec.Dynamic.DriveTypesRatio)
	numFDmin := sw + rl + hs

	// The largest even per-FD chunk of a pool is poolRaw/numFDmin; if that is below MinChunkSizeGiB the
	// target is infeasible regardless of node topology. TLC and QLC are planned independently, so each
	// active pool must clear the floor. Node/label-driven infeasibility still surfaces at reconcile.
	for _, pool := range []struct {
		name string
		raw  int
	}{{"TLC", tlcRaw}, {"QLC", qlcRaw}} {
		if pool.raw > 0 && pool.raw < numFDmin*allocator.MinChunkSizeGiB {
			return field.ErrorList{field.Invalid(
				field.NewPath("spec", "dynamic", "clusterCapacity"),
				cluster.Spec.Dynamic.ClusterCapacity,
				fmt.Sprintf("clusterCapacity %s share spread across %d failure domains is below the minimum "+
					"drive chunk of %d GiB. Adjust clusterCapacity or driveTypesRatio "+
					"(rule: each active pool's clusterCapacity × part/(tlc+qlc) >= %d × stripeWidth)",
					pool.name, numFDmin, allocator.MinChunkSizeGiB, allocator.MinChunkSizeGiB),
			)}
		}
	}
	return nil
}

// clusterHasTlcDriveContainer reports whether the cluster already has a deployed drive container that
// bears TLC (a TLC or mixed container). Best-effort: a list error or no UID resolves to "none" (gate
// applies), which is correct for a greenfield create (no containers exist) and at worst defers to the
// reconciler.
func clusterHasTlcDriveContainer(ctx context.Context, c client.Client, cluster *weka.WekaCluster) bool {
	uid := string(cluster.GetUID())
	if uid == "" {
		return false
	}
	var containers weka.WekaContainerList
	if err := c.List(ctx, &containers, client.InNamespace(cluster.Namespace), client.MatchingLabels{
		domain.WekaLabelClusterId: uid,
		domain.WekaLabelMode:      weka.WekaContainerModeDrive,
	}); err != nil {
		return false
	}
	for i := range containers.Items {
		wc := &containers.Items[i]
		// TLC-bearing when the ratio has a TLC part, or no explicit ratio (defaults to all-TLC, per
		// GetTlcQlcCapacity). Pure QLC-only containers (Tlc==0) do not count.
		if wc.Spec.DriveTypesRatio == nil || wc.Spec.DriveTypesRatio.Tlc > 0 {
			return true
		}
	}
	return false
}
