package wekacluster

import (
	"context"
	"strings"
	"testing"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/weka/weka-operator/internal/controllers/allocator"
)

const tib = 1024 // GiB per TiB

// driveContainer builds a drive-sharing WekaContainer owned by ownerUID, pinned to node, requesting
// the given total capacity split by ratio. ownerUID/labelOwner/node/ratio/cap can be tuned per case.
func ownedDriveContainer(ownerUID, node string, capGiB, tlc, qlc int) weka.WekaContainer {
	c := weka.WekaContainer{}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.ContainerCapacity = capGiB
	c.Spec.DriveTypesRatio = &weka.DriveTypesRatio{Tlc: tlc, Qlc: qlc}
	c.Spec.NodeAffinity = weka.NodeName(node)
	if ownerUID != "" {
		c.OwnerReferences = []metav1.OwnerReference{{Kind: "WekaCluster", UID: types.UID(ownerUID)}}
	}
	return c
}

// nodeWithLabels builds a corev1.Node carrying the given labels.
func nodeWithLabels(labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: labels}}
}

func strPtr(s string) *string { return &s }

// TestResolveNodeFDValue covers the explicit failure-domain label resolution used by
// buildNodeInventory(ctx, resolveFD=true): single Label, CompositeLabels joined with "-", and the
// "no FD label" cases that cause a node to be skipped. It must NOT apply the
// handleFailureDomainValue normalization so the values stay aligned with the planner's fdTypes keys.
func TestResolveNodeFDValue(t *testing.T) {
	tests := []struct {
		name   string
		fd     *weka.FailureDomain
		labels map[string]string
		want   string
	}{
		{
			name: "nil config",
			fd:   nil,
			want: "",
		},
		{
			name:   "single label present",
			fd:     &weka.FailureDomain{Label: strPtr("topology.kubernetes.io/zone")},
			labels: map[string]string{"topology.kubernetes.io/zone": "rack-a"},
			want:   "rack-a",
		},
		{
			name:   "single label missing",
			fd:     &weka.FailureDomain{Label: strPtr("topology.kubernetes.io/zone")},
			labels: map[string]string{"other": "x"},
			want:   "",
		},
		{
			name:   "composite all present",
			fd:     &weka.FailureDomain{CompositeLabels: []string{"row", "rack"}},
			labels: map[string]string{"row": "r1", "rack": "k3"},
			want:   "r1-k3",
		},
		{
			name:   "composite subset present (only matching labels joined)",
			fd:     &weka.FailureDomain{CompositeLabels: []string{"row", "rack"}},
			labels: map[string]string{"rack": "k3"},
			want:   "k3",
		},
		{
			name:   "composite none present",
			fd:     &weka.FailureDomain{CompositeLabels: []string{"row", "rack"}},
			labels: map[string]string{"other": "x"},
			want:   "",
		},
		{
			name:   "raw value not normalized (kept verbatim, slashes intact)",
			fd:     &weka.FailureDomain{Label: strPtr("zone")},
			labels: map[string]string{"zone": "region/long-value-exceeding-sixteen-chars"},
			want:   "region/long-value-exceeding-sixteen-chars",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allocator.ResolveNodeFDValue(nodeWithLabels(tt.labels), tt.fd)
			if got != tt.want {
				t.Errorf("resolveNodeFDValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

// testConstraints is a fixed set of capacity constraints for the summary helpers (TLC 10 TiB/core).
func testConstraints() *allocator.CapacityConstraints {
	return &allocator.CapacityConstraints{TlcCapacityPerCoreGiB: 10 * tib, QlcCapacityPerCoreGiB: 40 * tib}
}

func computeContainer(node string, cores int) *weka.WekaContainer {
	c := &weka.WekaContainer{}
	c.Spec.Mode = weka.WekaContainerModeCompute
	c.Spec.NumCores = cores
	c.Spec.NodeAffinity = weka.NodeName(node)
	return c
}

// TestSummarizeDriveContainers verifies per-pool capacity and TLC-drive-core totals are summed only
// over THIS cluster's healthy drive containers, matching buildExistingDriveContainers semantics:
// ratio-split capacity, legacy DriveCapacity, and the exclusion of non-drive and deleting containers.
func TestSummarizeDriveContainers(t *testing.T) {
	mixed := ownedDriveContainer("me", "n1", 100*tib, 1, 4)  // tlc=20TiB, qlc=80TiB
	tlcOnly := ownedDriveContainer("me", "n2", 50*tib, 1, 0) // tlc=50TiB

	legacy := &weka.WekaContainer{}
	legacy.Spec.Mode = weka.WekaContainerModeDrive
	legacy.Spec.DriveCapacity = 5 * tib
	legacy.Spec.NumDrives = 3 // tlc=15TiB legacy

	compute := computeContainer("n3", 8) // excluded (not a drive)

	deleting := ownedDriveContainer("me", "n4", 100*tib, 1, 0) // excluded (marked for deletion)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}

	mixedP, tlcP := &mixed, &tlcOnly
	dp := &deleting
	containers := []*weka.WekaContainer{mixedP, tlcP, legacy, compute, dp}

	cons := testConstraints()
	got := summarizeDriveContainers(t.Context(), containers, cons)

	wantTlc := 20*tib + 50*tib + 15*tib
	if got.tlcGiB != wantTlc {
		t.Errorf("tlcGiB = %d, want %d", got.tlcGiB, wantTlc)
	}
	if got.qlcGiB != 80*tib {
		t.Errorf("qlcGiB = %d, want %d", got.qlcGiB, 80*tib)
	}
	// totalTlcDriveCores: ceil(20/10) + ceil(50/10) + ceil(15/10) = 2 + 5 + 2 = 9.
	if got.totalTlcDriveCores != 9 {
		t.Errorf("totalTlcDriveCores = %d, want 9", got.totalTlcDriveCores)
	}
}

// TestSummarizeComputeContainers verifies the count and smallest per-container core size are taken only
// over healthy compute containers, excluding drives and deleting containers.
func TestSummarizeComputeContainers(t *testing.T) {
	c1 := computeContainer("n1", 14)
	c2 := computeContainer("n2", 10) // smallest
	c3 := computeContainer("n3", 16)
	drive := ownedDriveContainer("me", "n4", 50*tib, 1, 0) // excluded
	dp := &drive

	deleting := computeContainer("n5", 4) // excluded (deleting)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}

	containers := []*weka.WekaContainer{c1, c2, c3, dp, deleting}
	got := summarizeComputeContainers(t.Context(), containers)
	if got.count != 3 {
		t.Errorf("count = %d, want 3", got.count)
	}
	if got.minCores != 10 {
		t.Errorf("minCores = %d, want 10", got.minCores)
	}

	empty := summarizeComputeContainers(t.Context(), []*weka.WekaContainer{dp})
	if empty.count != 0 || empty.minCores != 0 {
		t.Errorf("empty = %+v, want {0,0}", empty)
	}
}

// TestSteadyStatePlan covers the skip decision: when this cluster's existing healthy drive containers
// cover the desired per-pool capacity AND the existing compute set already meets the derived target,
// steadyStatePlan returns skip=true with a no-op plan echoing the existing compute. A short pool or a
// compute set below the derived target falls through (skip=false) to the full inventory path.
func TestSteadyStatePlan(t *testing.T) {
	cons := testConstraints()                                                        // TLC 10 TiB/core, QLC 40 TiB/core
	s := allocator.ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 1} // MinFdNum=6

	// 6 drive containers, 10 TiB TLC each -> tlc=60TiB, totalTlcDriveCores=6. 6 compute @ 1 core each.
	drives := func() []*weka.WekaContainer {
		var cs []*weka.WekaContainer
		for range 6 {
			c := ownedDriveContainer("me", "n", 10*tib, 1, 0)
			cs = append(cs, &c)
		}
		return cs
	}
	withCompute := func(drv []*weka.WekaContainer, n, cores int) []*weka.WekaContainer {
		out := append([]*weka.WekaContainer(nil), drv...)
		for range n {
			out = append(out, computeContainer("c", cores))
		}
		return out
	}

	t.Run("covered and compute satisfied -> skip", func(t *testing.T) {
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 6, 1)}
		desired := allocator.DesiredCapacity{TlcRawGiB: 60 * tib} // exact cover, no QLC
		plan, skip := r.steadyStatePlan(t.Context(), desired, s, cons)
		if !skip {
			t.Fatalf("want skip=true (covered), got false")
		}
		if len(plan.Grow) != 0 || len(plan.Create) != 0 {
			t.Errorf("want empty grow/create, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
		}
		if plan.ComputeContainers != 6 || plan.ComputeCores != 1 {
			t.Errorf("want compute echo 6x1, got %dx%d", plan.ComputeContainers, plan.ComputeCores)
		}
		if plan.TotalTlcDriveCores != 6 {
			t.Errorf("want totalTlcDriveCores=6, got %d", plan.TotalTlcDriveCores)
		}
	})

	t.Run("pool short -> fall through", func(t *testing.T) {
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 6, 1)}
		desired := allocator.DesiredCapacity{TlcRawGiB: 100 * tib} // need more TLC
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (TLC short)")
		}
	})

	t.Run("compute count short -> fall through", func(t *testing.T) {
		// Only 3 compute containers but auto-derive needs >=floor(6) for totalTlcDriveCores=6.
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 3, 1)}
		desired := allocator.DesiredCapacity{TlcRawGiB: 60 * tib}
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (compute count below derived target)")
		}
	})

	t.Run("QLC pool short -> fall through", func(t *testing.T) {
		// TLC covered, QLC desired but none exists -> a pool needs growth.
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 6, 1)}
		desired := allocator.DesiredCapacity{TlcRawGiB: 60 * tib, QlcRawGiB: 40 * tib}
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (QLC short)")
		}
	})

	t.Run("explicit computeCores raised above existing -> fall through", func(t *testing.T) {
		// Drives covered, existing compute at 1 core each, but spec now asks for 8 cores each.
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 6, 1)}
		desired := allocator.DesiredCapacity{TlcRawGiB: 60 * tib, ComputeCores: 8}
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (compute cores must grow)")
		}
	})
}

// TestSteadyStatePlanOverProvisioned verifies that when a pool is over-provisioned (current > desired)
// the plan still skips inventory (no auto-shrink) but emits the throttled ClusterCapacityShrink event.
func TestSteadyStatePlanOverProvisioned(t *testing.T) {
	cons := testConstraints()
	s := allocator.ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 1}

	var containers []*weka.WekaContainer
	for range 6 {
		c := ownedDriveContainer("me", "n", 10*tib, 1, 0) // tlc=60TiB total
		containers = append(containers, &c)
	}
	for range 6 {
		containers = append(containers, computeContainer("c", 1))
	}

	rec := record.NewFakeRecorder(8)
	r := &wekaClusterReconcilerLoop{
		containers: containers,
		cluster:    &weka.WekaCluster{},
		Recorder:   rec,
		Throttler:  throttling.NewSyncMapThrottler(),
	}
	desired := allocator.DesiredCapacity{TlcRawGiB: 40 * tib} // current 60 > desired 40

	plan, skip := r.steadyStatePlan(t.Context(), desired, s, cons)
	if !skip {
		t.Fatalf("want skip=true (covered, never auto-shrinks)")
	}
	if len(plan.Grow) != 0 || len(plan.Create) != 0 {
		t.Errorf("want no grow/create on shrink, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "ClusterCapacityShrink") || !strings.Contains(ev, "over-provisioned") {
			t.Errorf("unexpected shrink event: %q", ev)
		}
	default:
		t.Errorf("expected a ClusterCapacityShrink event to be emitted")
	}
}

// TestPlanClusterCapacitySkipsNodeInventory asserts the end-to-end contract of the steady-state fast
// path: when this cluster's existing healthy containers already cover the desired capacity,
// planClusterCapacity returns a no-op plan WITHOUT invoking buildNodeInventory (the expensive node
// listing). When a pool is short, it falls through and DOES invoke it.
func TestPlanClusterCapacitySkipsNodeInventory(t *testing.T) {
	// sw=3,rl=2,hs=1 => MinFdNum=6, raw inflation factor (sw+rl+hs)/sw = 2.
	newLoop := func(capacity string, containers []*weka.WekaContainer, inventoryFn func() ([]allocator.NodeCapacity, error)) (*wekaClusterReconcilerLoop, *int) {
		calls := 0
		cluster := &weka.WekaCluster{}
		cluster.Spec.StripeWidth = 3
		cluster.Spec.RedundancyLevel = 2
		cluster.Spec.HotSpare = 1
		cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: capacity}
		r := &wekaClusterReconcilerLoop{
			containers: containers,
			cluster:    cluster,
			Recorder:   record.NewFakeRecorder(8),
			Throttler:  throttling.NewSyncMapThrottler(),
			buildNodeInventoryFn: func(ctx context.Context) (map[string]string, []allocator.NodeCapacity, map[string]bool, error) {
				calls++
				inv, err := inventoryFn()
				return map[string]string{}, inv, map[string]bool{}, err
			},
		}
		return r, &calls
	}

	// 30Gi usable => raw TLC = int(30×(sw+rl+hs)/sw / 0.9) = int(60/0.9) = 66 GiB. Cover it with 6 drive
	// containers @ 11 GiB TLC each (6×11 = 66), plus the MinFdNum (6) compute containers @ 1 core so
	// compute needs no change.
	covering := func() []*weka.WekaContainer {
		var cs []*weka.WekaContainer
		for range 6 {
			c := ownedDriveContainer("me", "n", 11, 1, 0) // 11 GiB TLC
			c.Status.NodeAffinity = "n"                   // scheduled (not transiently unscheduled)
			cs = append(cs, &c)
		}
		for range 6 {
			cs = append(cs, computeContainer("c", 1))
		}
		return cs
	}

	t.Run("covered -> buildNodeInventory NOT called", func(t *testing.T) {
		r, calls := newLoop("30Gi", covering(), func() ([]allocator.NodeCapacity, error) {
			t.Fatalf("buildNodeInventory must not be called on the steady-state path")
			return nil, nil
		})
		plan, err := r.planClusterCapacity(t.Context())
		if err != nil {
			t.Fatalf("planClusterCapacity returned error: %v", err)
		}
		if *calls != 0 {
			t.Errorf("buildNodeInventory called %d times, want 0", *calls)
		}
		if len(plan.Grow) != 0 || len(plan.Create) != 0 {
			t.Errorf("want no-op plan, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
		}
	})

	t.Run("pool short -> buildNodeInventory IS called", func(t *testing.T) {
		// 60Gi usable => raw TLC = int(60×2/0.9) = 133 GiB, but existing covers only 66 -> must re-plan.
		r, calls := newLoop("60Gi", covering(), func() ([]allocator.NodeCapacity, error) {
			return nil, nil // empty inventory -> PlanCapacity is infeasible, but it was consulted
		})
		_, _ = r.planClusterCapacity(t.Context()) // infeasible WaitError is fine; we only assert the call
		if *calls != 1 {
			t.Errorf("buildNodeInventory called %d times, want 1 (fall-through)", *calls)
		}
	})
}

// newCapacityLoop builds a wekaClusterReconcilerLoop wired with a sw3/rl2/hs1 clusterCapacity cluster
// and a counting buildNodeInventory test seam, for asserting whether the expensive inventory rebuild is
// reached on a given path.
func newCapacityLoop(capacity string, containers []*weka.WekaContainer, inventoryFn func() ([]allocator.NodeCapacity, error)) (*wekaClusterReconcilerLoop, *int) {
	calls := 0
	cluster := &weka.WekaCluster{}
	cluster.Spec.StripeWidth = 3
	cluster.Spec.RedundancyLevel = 2
	cluster.Spec.HotSpare = 1
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: capacity}
	r := &wekaClusterReconcilerLoop{
		containers: containers,
		cluster:    cluster,
		Recorder:   record.NewFakeRecorder(8),
		Throttler:  throttling.NewSyncMapThrottler(),
		buildNodeInventoryFn: func(ctx context.Context) (map[string]string, []allocator.NodeCapacity, map[string]bool, error) {
			calls++
			inv, err := inventoryFn()
			return map[string]string{}, inv, map[string]bool{}, err
		},
	}
	return r, &calls
}

// TestFirstUnscheduledDriveContainer covers the transient-churn detector: an alive drive container with
// no scheduled node (Status.NodeAffinity == "") is reported, while a scheduled drive, a non-drive, and a
// deleting drive are skipped — the latter is leaving and must not stall planning.
func TestFirstUnscheduledDriveContainer(t *testing.T) {
	scheduled := ownedDriveContainer("me", "n1", 10*tib, 1, 0)
	scheduled.Status.NodeAffinity = "n1"

	unsched := ownedDriveContainer("me", "n2", 10*tib, 1, 0)
	unsched.Name = "drive-unsched" // Status.NodeAffinity left "" -> alive but pod (re)scheduling

	deleting := ownedDriveContainer("me", "n3", 10*tib, 1, 0)
	deleting.Name = "drive-deleting"
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"x"}

	compute := computeContainer("n4", 4) // non-drive, ignored

	t.Run("all scheduled/leaving -> none transient", func(t *testing.T) {
		sc, dl := scheduled, deleting
		if name, ok := firstUnscheduledDriveContainer([]*weka.WekaContainer{&sc, &dl, compute}); ok {
			t.Errorf("want ok=false, got (%q,true)", name)
		}
	})

	t.Run("one alive-unscheduled -> reported", func(t *testing.T) {
		sc, us, dl := scheduled, unsched, deleting
		name, ok := firstUnscheduledDriveContainer([]*weka.WekaContainer{&sc, &dl, &us})
		if !ok || name != "drive-unsched" {
			t.Errorf("want (drive-unsched,true), got (%q,%v)", name, ok)
		}
	})

	t.Run("unscheduled drive-capacity container (no containerCapacity) -> reported", func(t *testing.T) {
		// Capacity via Spec.DriveCapacity×NumDrives, NOT Spec.ContainerCapacity, so HasContainerCapacity()
		// is false. The guard must still defer on it (it carries planner capacity).
		dc := ownedDriveContainer("me", "n5", 0, 0, 0)
		dc.Name = "drive-capacity-unsched"
		dc.Spec.ContainerCapacity = 0
		dc.Spec.DriveCapacity = 2 * tib
		dc.Spec.NumDrives = 3
		dc.Spec.DriveTypesRatio = nil // driveCapacity path is TLC-only
		name, ok := firstUnscheduledDriveContainer([]*weka.WekaContainer{&dc})
		if !ok || name != "drive-capacity-unsched" {
			t.Errorf("want (drive-capacity-unsched,true), got (%q,%v)", name, ok)
		}
	})

	t.Run("unscheduled but zero-capacity drive -> not reported", func(t *testing.T) {
		zero := ownedDriveContainer("me", "n6", 0, 0, 0)
		zero.Name = "drive-zero-cap" // no containerCapacity, no driveCapacity -> contributes nothing
		zero.Spec.DriveTypesRatio = nil
		if name, ok := firstUnscheduledDriveContainer([]*weka.WekaContainer{&zero}); ok {
			t.Errorf("want ok=false for zero-capacity drive, got (%q,true)", name)
		}
	})
}

// TestPlanClusterCapacityDefersOnTransientChurn asserts the transient-churn guard: when an owned drive
// container is alive but unscheduled (its pod is (re)scheduling), planClusterCapacity returns a no-op
// plan WITHOUT consulting buildNodeInventory — so a momentary FD-count dip can never drive a grow that
// concentrates capacity onto the survivors. A deleting container does NOT trip the guard (it is leaving),
// so a genuinely short pool still falls through to the inventory rebuild.
func TestPlanClusterCapacityDefersOnTransientChurn(t *testing.T) {
	// 60Gi => raw TLC 120 GiB; 6 drives @ 10 GiB TLC cover only 60 -> WITHOUT the guard this is a short
	// pool that reaches buildNodeInventory. The guard must short-circuit before that.
	drives := func(unscheduledIdx, deletingIdx int) []*weka.WekaContainer {
		var cs []*weka.WekaContainer
		for i := range 6 {
			c := ownedDriveContainer("me", "n", 10, 1, 0)
			c.Status.NodeAffinity = "n" // scheduled by default
			if i == unscheduledIdx {
				c.Status.NodeAffinity = "" // alive but pod (re)scheduling
			}
			if i == deletingIdx {
				now := metav1.Now()
				c.DeletionTimestamp = &now
				c.Finalizers = []string{"x"}
			}
			cs = append(cs, &c)
		}
		return cs
	}

	t.Run("alive-unscheduled drive -> deferred, inventory NOT called", func(t *testing.T) {
		r, calls := newCapacityLoop("60Gi", drives(2, -1), func() ([]allocator.NodeCapacity, error) {
			t.Fatalf("buildNodeInventory must not be called while a drive container is transiently unscheduled")
			return nil, nil
		})
		plan, err := r.planClusterCapacity(t.Context())
		if err != nil {
			t.Fatalf("planClusterCapacity returned error: %v", err)
		}
		if *calls != 0 {
			t.Errorf("buildNodeInventory called %d times, want 0 (deferred)", *calls)
		}
		if len(plan.Grow) != 0 || len(plan.Create) != 0 {
			t.Errorf("want no-op plan, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
		}
	})

	t.Run("deleting drive does not defer -> inventory IS called", func(t *testing.T) {
		r, calls := newCapacityLoop("60Gi", drives(-1, 2), func() ([]allocator.NodeCapacity, error) {
			return nil, nil // consulted; empty inventory -> infeasible WaitError is fine
		})
		_, _ = r.planClusterCapacity(t.Context())
		if *calls != 1 {
			t.Errorf("buildNodeInventory called %d times, want 1 (deleting must not stall planning)", *calls)
		}
	})
}

func TestFormatCapacityPlanSummary(t *testing.T) {
	scheme := allocator.ProtectionScheme{StripeWidth: 8, RedundancyLevel: 2, HotSpare: 1}
	desired := allocator.DesiredCapacity{TlcRawGiB: 24 * tib}

	t.Run("create across nodes and FDs with compute", func(t *testing.T) {
		plan := &allocator.CapacityPlan{
			Create: []allocator.NewContainer{
				{Node: "n1", FDValue: "fd-a", TlcGiB: 8 * tib, QlcGiB: 4 * tib}, // mixed
				{Node: "n2", FDValue: "fd-b", TlcGiB: 8 * tib, QlcGiB: 4 * tib}, // mixed
				{Node: "n3", FDValue: "fd-c", TlcGiB: 8 * tib},                  // TLC-only
			},
			ComputeContainers: 3,
			ComputeCores:      8,
			ComputeNodes:      []string{"n1", "n2", "n3"},
		}
		got := formatCapacityPlanSummary(plan, desired, scheme, nil)
		for _, want := range []string{
			"creating 3 drive container(s) [2 mixed, 1 TLC] across 3 node(s) / 3 failure domain(s)",
			"@ ~",          // per-FD chunk
			"placing T/Q ", // mixed create capacity
			"compute 3×8 cores on 3 node(s)",
			"minFdNum 11",
			"placed ",
			"protection 8+2+1",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("summary %q missing %q", got, want)
			}
		}
		if strings.Contains(got, "growing") {
			t.Errorf("summary %q should not mention growing when Grow is empty", got)
		}
	})

	t.Run("homogeneous create folds type into the noun (no redundant bracket)", func(t *testing.T) {
		plan := &allocator.CapacityPlan{
			Create: []allocator.NewContainer{
				{Node: "n1", FDValue: "fd-a", QlcGiB: 20 * tib},
				{Node: "n2", FDValue: "fd-b", QlcGiB: 20 * tib},
				{Node: "n3", FDValue: "fd-c", QlcGiB: 20 * tib},
			},
		}
		got := formatCapacityPlanSummary(plan, desired, scheme, nil)
		if !strings.Contains(got, "creating 3 QLC drive container(s) across 3 node(s) / 3 failure domain(s)") {
			t.Errorf("want folded homogeneous phrasing, got %q", got)
		}
		if strings.Contains(got, "[") {
			t.Errorf("homogeneous create must not emit a bracketed breakdown, got %q", got)
		}
	})

	t.Run("grow only", func(t *testing.T) {
		plan := &allocator.CapacityPlan{
			Grow: []allocator.ContainerGrowth{
				{Name: "c1", NewTlcGiB: 8 * tib, NewCores: 8},
				{Name: "c2", NewTlcGiB: 8 * tib, NewCores: 8},
			},
		}
		existing := []allocator.ExistingContainer{
			{Name: "c1", TlcGiB: 6 * tib, NumCores: 6},
			{Name: "c2", TlcGiB: 6 * tib, NumCores: 6},
		}
		got := formatCapacityPlanSummary(plan, desired, scheme, existing)
		for _, want := range []string{
			"growing 2 existing container(s) (+",
			"cores 6→8)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("summary %q missing %q", got, want)
			}
		}
		if strings.Contains(got, "creating") {
			t.Errorf("summary %q should not mention creating when Create is empty", got)
		}
	})

	t.Run("grow entry missing from existing -> excluded, no inflated numbers", func(t *testing.T) {
		// "phantom" is a logic error (a Grow must map to an existing container). It must be skipped,
		// not subtracted from a zero baseline (which would inflate the reported added cores/capacity).
		plan := &allocator.CapacityPlan{
			Grow: []allocator.ContainerGrowth{
				{Name: "c1", NewTlcGiB: 8 * tib, NewCores: 8},
				{Name: "phantom", NewTlcGiB: 100 * tib, NewCores: 99}, // no matching existingDrives entry
			},
		}
		existing := []allocator.ExistingContainer{
			{Name: "c1", TlcGiB: 6 * tib, NumCores: 6},
		}
		got := formatCapacityPlanSummary(plan, desired, scheme, existing)
		if !strings.Contains(got, "growing 1 existing container(s) (+") {
			t.Errorf("summary %q should count only the real grow (1), excluding the phantom", got)
		}
		if !strings.Contains(got, "cores 6→8)") {
			t.Errorf("summary %q should report c1's 6→8 cores, not the phantom's", got)
		}
		if strings.Contains(got, "99") || strings.Contains(got, "100") {
			t.Errorf("summary %q must not render the phantom's inflated numbers", got)
		}
	})

	t.Run("all grow entries missing -> no grow leg", func(t *testing.T) {
		plan := &allocator.CapacityPlan{
			Grow: []allocator.ContainerGrowth{{Name: "ghost", NewTlcGiB: 8 * tib, NewCores: 8}},
		}
		got := formatCapacityPlanSummary(plan, desired, scheme, nil)
		if strings.Contains(got, "growing") {
			t.Errorf("summary %q should omit the grow leg when no entry resolves", got)
		}
	})
}
