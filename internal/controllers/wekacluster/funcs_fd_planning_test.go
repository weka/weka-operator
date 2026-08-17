package wekacluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
)

const tib = 1024 // GiB per TiB

// ownedDriveContainer builds a drive-sharing WekaContainer owned by ownerUID, pinned to node, requesting
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

// TestResolveNodeFDValue covers single Label, CompositeLabels joined with "-", and "no FD label" cases.
// Must not apply handleFailureDomainValue normalization, or values would misalign with the planner's fdTypes keys.
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
func testConstraints() *capacityplanner.CapacityConstraints {
	return &capacityplanner.CapacityConstraints{TlcCapacityPerCoreGiB: 10 * tib, QlcCapacityPerCoreGiB: 40 * tib}
}

func computeContainer(node string, cores int) *weka.WekaContainer {
	c := &weka.WekaContainer{}
	c.Spec.Mode = weka.WekaContainerModeCompute
	c.Spec.NumCores = cores
	c.Spec.NodeAffinity = weka.NodeName(node)
	return c
}

// TestSummarizeDriveContainers verifies per-pool capacity and TLC-drive-core totals are summed only over
// this cluster's healthy drive containers: ratio-split capacity, legacy DriveCapacity, and exclusion of
// non-drive and deleting containers.
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

func TestSteadyStatePlan(t *testing.T) {
	cons := testConstraints()                                                              // TLC 10 TiB/core, QLC 40 TiB/core
	s := capacityplanner.ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 1} // MinFdNum=6

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
		desired := capacityplanner.DesiredCapacity{TlcRawGiB: 60 * tib} // exact cover, no QLC
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
		desired := capacityplanner.DesiredCapacity{TlcRawGiB: 100 * tib} // need more TLC
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (TLC short)")
		}
	})

	t.Run("compute count short -> fall through", func(t *testing.T) {
		// Only 3 compute containers but auto-derive needs >=floor(6) for totalTlcDriveCores=6.
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 3, 1)}
		desired := capacityplanner.DesiredCapacity{TlcRawGiB: 60 * tib}
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (compute count below derived target)")
		}
	})

	t.Run("QLC pool short -> fall through", func(t *testing.T) {
		// TLC covered, QLC desired but none exists -> a pool needs growth.
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 6, 1)}
		desired := capacityplanner.DesiredCapacity{TlcRawGiB: 60 * tib, QlcRawGiB: 40 * tib}
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (QLC short)")
		}
	})

	t.Run("explicit computeCores raised above existing -> fall through", func(t *testing.T) {
		// Drives covered, existing compute at 1 core each, but spec now asks for 8 cores each.
		r := &wekaClusterReconcilerLoop{containers: withCompute(drives(), 6, 1)}
		desired := capacityplanner.DesiredCapacity{TlcRawGiB: 60 * tib, ComputeCores: 8}
		if _, skip := r.steadyStatePlan(t.Context(), desired, s, cons); skip {
			t.Errorf("want skip=false (compute cores must grow)")
		}
	})
}

// TestSteadyStatePlanOverProvisioned verifies that when a pool is over-provisioned (current > desired)
// the plan still skips inventory (no auto-shrink) but emits the throttled ClusterCapacityShrink event.
func TestSteadyStatePlanOverProvisioned(t *testing.T) {
	cons := testConstraints()
	s := capacityplanner.ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 1}

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
	desired := capacityplanner.DesiredCapacity{TlcRawGiB: 40 * tib} // current 60 > desired 40

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

// TestPlanClusterCapacitySkipsNodeInventory asserts the end-to-end steady-state contract: covered capacity
// returns a no-op plan without invoking buildNodeInventory; a short pool falls through and does invoke it.
func TestPlanClusterCapacitySkipsNodeInventory(t *testing.T) {
	// sw=3,rl=2,hs=1 => MinFdNum=6, raw inflation factor (sw+rl+hs)/sw = 2.
	newLoop := func(capacity string, containers []*weka.WekaContainer, inventoryFn func() ([]capacityplanner.NodeCapacity, error)) (*wekaClusterReconcilerLoop, *int) {
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
			buildNodeInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
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
		r, calls := newLoop("30Gi", covering(), func() ([]capacityplanner.NodeCapacity, error) {
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
		r, calls := newLoop("60Gi", covering(), func() ([]capacityplanner.NodeCapacity, error) {
			return nil, nil // empty inventory -> PlanCapacity is infeasible, but it was consulted
		})
		_, _ = r.planClusterCapacity(t.Context()) // infeasible WaitError is fine; we only assert the call
		if *calls != 1 {
			t.Errorf("buildNodeInventory called %d times, want 1 (fall-through)", *calls)
		}
	})
}

// newCapacityLoop builds a wekaClusterReconcilerLoop wired with a sw3/rl2/hs1 clusterCapacity cluster and a counting buildNodeInventory test seam.
func newCapacityLoop(capacity string, containers []*weka.WekaContainer, inventoryFn func() ([]capacityplanner.NodeCapacity, error)) (*wekaClusterReconcilerLoop, *int) {
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
		buildNodeInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
			calls++
			inv, err := inventoryFn()
			return map[string]string{}, inv, map[string]bool{}, err
		},
	}
	return r, &calls
}

// TestFirstUnscheduledDriveContainer covers the transient-churn detector: an alive drive container with no
// scheduled node is reported; a scheduled drive, a non-drive, and a deleting drive (leaving) are skipped.
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
		// Capacity via Spec.DriveCapacity×NumDrives, not Spec.ContainerCapacity, so HasContainerCapacity()
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

// TestPlanClusterCapacityDefersOnTransientChurn asserts the transient-churn guard: an alive but unscheduled
// drive container defers planning (no buildNodeInventory call), so a momentary FD-count dip can never drive
// a grow that concentrates capacity onto the survivors. A deleting container does not trip the guard.
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
		r, calls := newCapacityLoop("60Gi", drives(2, -1), func() ([]capacityplanner.NodeCapacity, error) {
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
		r, calls := newCapacityLoop("60Gi", drives(-1, 2), func() ([]capacityplanner.NodeCapacity, error) {
			return nil, nil // consulted; empty inventory -> infeasible WaitError is fine
		})
		_, _ = r.planClusterCapacity(t.Context())
		if *calls != 1 {
			t.Errorf("buildNodeInventory called %d times, want 1 (deleting must not stall planning)", *calls)
		}
	})
}

func TestFormatCapacityPlanSummary(t *testing.T) {
	scheme := capacityplanner.ProtectionScheme{StripeWidth: 8, RedundancyLevel: 2, HotSpare: 1}
	desired := capacityplanner.DesiredCapacity{TlcRawGiB: 24 * tib}

	t.Run("create across nodes and FDs with compute", func(t *testing.T) {
		plan := &capacityplanner.CapacityPlan{
			Create: []capacityplanner.NewContainer{
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
		plan := &capacityplanner.CapacityPlan{
			Create: []capacityplanner.NewContainer{
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
		plan := &capacityplanner.CapacityPlan{
			Grow: []capacityplanner.ContainerGrowth{
				{Name: "c1", NewTlcGiB: 8 * tib, NewCores: 8},
				{Name: "c2", NewTlcGiB: 8 * tib, NewCores: 8},
			},
		}
		existing := []capacityplanner.ExistingContainer{
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
		plan := &capacityplanner.CapacityPlan{
			Grow: []capacityplanner.ContainerGrowth{
				{Name: "c1", NewTlcGiB: 8 * tib, NewCores: 8},
				{Name: "phantom", NewTlcGiB: 100 * tib, NewCores: 99}, // no matching existingDrives entry
			},
		}
		existing := []capacityplanner.ExistingContainer{
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
		plan := &capacityplanner.CapacityPlan{
			Grow: []capacityplanner.ContainerGrowth{{Name: "ghost", NewTlcGiB: 8 * tib, NewCores: 8}},
		}
		got := formatCapacityPlanSummary(plan, desired, scheme, nil)
		if strings.Contains(got, "growing") {
			t.Errorf("summary %q should omit the grow leg when no entry resolves", got)
		}
	})
}

// autoFullDrivesNode builds a NodeCapacity fixture with CPU/hugepages/memory set far beyond test need, so only DriveCapacitiesGiB (and TlcGiB=sum of it) varies.
func autoFullDrivesNode(name, fd string, driveCapacitiesGiB []int) capacityplanner.NodeCapacity {
	sum := 0
	for _, d := range driveCapacitiesGiB {
		sum += d
	}
	return capacityplanner.NodeCapacity{
		NodeName:              name,
		FDValue:               fd,
		DriveCapacitiesGiB:    driveCapacitiesGiB,
		TlcGiB:                sum,
		AllocatableCPU:        64,
		AvailableHugepagesMiB: 500_000,
		AvailableMemoryMiB:    500_000,
	}
}

// newAutoFullDrivesLoop is newCapacityLoop's analogue for planAutoFullDrives, using buildFullDrivesInventoryFn instead of buildNodeInventoryFn.
func newAutoFullDrivesLoop(containers []*weka.WekaContainer, inventoryFn func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error)) (*wekaClusterReconcilerLoop, *int) {
	calls := 0
	cluster := &weka.WekaCluster{}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}
	r := &wekaClusterReconcilerLoop{
		containers: containers,
		cluster:    cluster,
		Recorder:   record.NewFakeRecorder(8),
		Throttler:  throttling.NewSyncMapThrottler(),
		buildFullDrivesInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
			calls++
			return inventoryFn()
		},
	}
	return r, &calls
}

// TestPlanAutoFullDrivesReadsFullDrivesNotSharedInventory pins that planAutoFullDrives reads the full-drives population
// (buildFullDrivesInventoryFn), never the shared TLC/QLC population planClusterCapacity reads: a node with
// no signed full drives must produce no container, while a signed node gets exactly one sized from its own.
func TestPlanAutoFullDrivesReadsFullDrivesNotSharedInventory(t *testing.T) {
	withoutFormClusterComputeFloor(t)
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 10 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	sharedOnly := autoFullDrivesNode("shared-only-node", "fd-a", nil)               // no full drives; compute-eligible
	fullDrives := autoFullDrivesNode("full-drives-node", "fd-b", []int{1024, 1024}) // 2 signed full drives, 1 TiB each

	r, _ := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{}, []capacityplanner.NodeCapacity{sharedOnly, fullDrives}, map[string]bool{"shared-only-node": true}, nil
	})

	plan, err := r.planAutoFullDrives(t.Context())
	if err != nil {
		t.Fatalf("planAutoFullDrives returned error: %v", err)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 created drive container, got %d: %+v", len(plan.Create), plan.Create)
	}
	got := plan.Create[0]
	if got.Node != "full-drives-node" {
		t.Errorf("want the full-drives node to get the container, got node %q", got.Node)
	}
	if got.NumDrives != 2 {
		t.Errorf("want NumDrives=2 (both signed full drives), got %d", got.NumDrives)
	}
}

// TestPlanAutoFullDrivesDefersWithoutSignedDrives guards the bootstrap condition: nodeInv can be non-empty (a
// compute-selector node always contributes) even with zero drive-role nodes signed, so planAutoFullDrives must scan
// DriveCapacitiesGiB rather than a bare len(nodeInv) == 0 check.
func TestPlanAutoFullDrivesDefersWithoutSignedDrives(t *testing.T) {
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 10 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	driveRoleNoDrives := autoFullDrivesNode("drive-role-node", "fd-a", nil) // drive-role selector matches, nothing signed yet
	computeOnly := autoFullDrivesNode("compute-node", "fd-b", nil)          // compute-selector node, also no drives

	rec := record.NewFakeRecorder(8)
	r, _ := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{}, []capacityplanner.NodeCapacity{driveRoleNoDrives, computeOnly}, map[string]bool{"compute-node": true}, nil
	})
	r.Recorder = rec

	plan, err := r.planAutoFullDrives(t.Context())
	if err == nil {
		t.Fatalf("want planAutoFullDrives to defer with an error when no node has signed full drives, got plan %+v", plan)
	}
	if !strings.Contains(err.Error(), "signed full drives") {
		t.Errorf("want the no-signed-drives error, got: %v", err)
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "AutoFullDrivesNoSignedDrives") {
			t.Errorf("unexpected event: %q", ev)
		}
	default:
		t.Errorf("expected an AutoFullDrivesNoSignedDrives event to be emitted")
	}
}

// fakeManagerNilClient is the minimal ctrl.Manager fake buildAutoFullDrivesDriveContainers needs: its GetClient()
// result is only dereferenced by GetContainerHugepages for role=="compute", never "drive", so nil is safe here.
type fakeManagerNilClient struct {
	manager.Manager
}

func (fakeManagerNilClient) GetClient() client.Client { return nil }

// TestBuildAutoFullDrivesDriveContainersShape pins the built container's shape: NumDrives/Node feed
// Spec.NumDrives/Spec.NodeAffinity directly, and Spec.ContainerCapacity must stay 0 (auto full drives is
// exclusive, unlike clusterCapacity's drive-sharing pools which do set it).
func TestBuildAutoFullDrivesDriveContainersShape(t *testing.T) {
	withoutFormClusterComputeFloor(t)
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 10 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	node := autoFullDrivesNode("n1", "fd-a", []int{1024, 1024, 1024}) // 3 signed full drives, 1 TiB each; also its own compute candidate

	r, _ := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{}, []capacityplanner.NodeCapacity{node}, map[string]bool{"n1": true}, nil
	})
	r.Manager = fakeManagerNilClient{}

	containers, skipped, plan, err := r.buildPlannerDriveContainers(t.Context(), sizingAutoFullDrives)
	if err != nil {
		t.Fatalf("buildAutoFullDrivesDriveContainers returned error: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("want no skipped roles, got %v", skipped)
	}
	if len(containers) != 1 || len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 built container, got %d (plan.Create=%d)", len(containers), len(plan.Create))
	}
	pc := plan.Create[0]
	c := containers[0]
	if c.Spec.NumDrives != pc.NumDrives {
		t.Errorf("Spec.NumDrives = %d, want %d (pc.NumDrives)", c.Spec.NumDrives, pc.NumDrives)
	}
	if string(c.Spec.NodeAffinity) != pc.Node {
		t.Errorf("Spec.NodeAffinity = %q, want %q (pc.Node)", c.Spec.NodeAffinity, pc.Node)
	}
	if c.Spec.ContainerCapacity != 0 {
		t.Errorf("Spec.ContainerCapacity = %d, want 0 (auto full drives is exclusive, non-sharing)", c.Spec.ContainerCapacity)
	}
}

// TestBuildMissingContainersModeIsolation pins the BuildMissingContainers gating: a clusterCapacity cluster
// must never consult auto full drives' full-drives inventory seam, and vice versa (the two planner branches are
// mutually exclusive, also enforced by CEL at admission). Only which seam fires matters here, not whether
// the resulting plan succeeds.
func TestBuildMissingContainersModeIsolation(t *testing.T) {
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 10 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	t.Run("clusterCapacity cluster never enters the auto-full-drives branch", func(t *testing.T) {
		cluster := &weka.WekaCluster{}
		cluster.Spec.StripeWidth = 3
		cluster.Spec.RedundancyLevel = 2
		cluster.Spec.HotSpare = 1
		cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: "1Gi"}
		r := &wekaClusterReconcilerLoop{
			cluster:   cluster,
			Recorder:  record.NewFakeRecorder(8),
			Throttler: throttling.NewSyncMapThrottler(),
			buildNodeInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
				return map[string]string{}, nil, map[string]bool{}, nil // empty inventory -> infeasible, but consulted
			},
			buildFullDrivesInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
				t.Fatalf("auto full drives' full-drives inventory must not be consulted for a clusterCapacity cluster")
				return nil, nil, nil, nil
			},
		}
		if _, err := r.BuildMissingContainers(t.Context()); err == nil {
			t.Fatalf("want an error (infeasible: empty inventory), got none")
		}
	})

	t.Run("auto-full-drives cluster never enters the clusterCapacity branch", func(t *testing.T) {
		cluster := &weka.WekaCluster{}
		cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}
		r := &wekaClusterReconcilerLoop{
			cluster:   cluster,
			Recorder:  record.NewFakeRecorder(8),
			Throttler: throttling.NewSyncMapThrottler(),
			buildNodeInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
				t.Fatalf("clusterCapacity's shared-drives inventory must not be consulted for an auto-full-drives cluster")
				return nil, nil, nil, nil
			},
			buildFullDrivesInventoryFn: func(ctx context.Context) (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
				return map[string]string{}, nil, map[string]bool{}, nil // no candidate nodes -> no signed drives -> defer
			},
		}
		if _, err := r.BuildMissingContainers(t.Context()); err == nil {
			t.Fatalf("want an error (no signed drives yet), got none")
		}
	})
}

// TestPlanAutoFullDrivesHeterogeneousNodes covers two full-drives nodes with different drive counts/sizes: each
// must get its own container sized from its own drives, not a uniform size shared across nodes.
func TestPlanAutoFullDrivesHeterogeneousNodes(t *testing.T) {
	withoutFormClusterComputeFloor(t)
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 10 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	small := autoFullDrivesNode("small-node", "fd-a", []int{1024})               // 1 drive, 1 TiB
	big := autoFullDrivesNode("big-node", "fd-b", []int{1024, 1024, 1024, 1024}) // 4 drives, 4 TiB
	computeNode := autoFullDrivesNode("compute-node", "fd-c", nil)               // dedicated compute candidate

	r, _ := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{}, []capacityplanner.NodeCapacity{small, big, computeNode}, map[string]bool{"compute-node": true}, nil
	})

	plan, err := r.planAutoFullDrives(t.Context())
	if err != nil {
		t.Fatalf("planAutoFullDrives returned error: %v", err)
	}
	if len(plan.Create) != 2 {
		t.Fatalf("want 2 created drive containers, got %d: %+v", len(plan.Create), plan.Create)
	}
	byNode := make(map[string]capacityplanner.NewContainer, 2)
	for _, c := range plan.Create {
		byNode[c.Node] = c
	}
	smallC, ok := byNode["small-node"]
	if !ok {
		t.Fatalf("small-node missing from plan.Create: %+v", plan.Create)
	}
	bigC, ok := byNode["big-node"]
	if !ok {
		t.Fatalf("big-node missing from plan.Create: %+v", plan.Create)
	}
	if smallC.NumDrives != 1 {
		t.Errorf("small-node NumDrives = %d, want 1", smallC.NumDrives)
	}
	if bigC.NumDrives != 4 {
		t.Errorf("big-node NumDrives = %d, want 4", bigC.NumDrives)
	}
	if smallC.NumDrives == bigC.NumDrives {
		t.Errorf("want different NumDrives across heterogeneous nodes, both got %d", smallC.NumDrives)
	}
}

// TestPlanAutoFullDrivesStillDefersWithoutSignedDrivesAfterSteadyStateGate is test (e)'s end-to-end half: with no
// containers at all, the steady-state gate must fall through into planAutoFullDrives's bootstrap guard, which must
// still run buildFullDrivesInventoryFn and, finding no signed drives, emit AutoFullDrivesNoSignedDrives and return
// a WaitError. Asserts the seam call count too, so a swallowed gate is caught even if the error stays the same.
func TestPlanAutoFullDrivesStillDefersWithoutSignedDrivesAfterSteadyStateGate(t *testing.T) {
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 10 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	driveRoleNoDrives := autoFullDrivesNode("drive-role-node", "fd-a", nil) // drive-role selector matches, nothing signed yet
	computeOnly := autoFullDrivesNode("compute-node", "fd-b", nil)          // compute-selector node, also no drives

	rec := record.NewFakeRecorder(8)
	r, calls := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{}, []capacityplanner.NodeCapacity{driveRoleNoDrives, computeOnly}, map[string]bool{"compute-node": true}, nil
	})
	r.Recorder = rec

	plan, err := r.planAutoFullDrives(t.Context())
	if err == nil {
		t.Fatalf("want planAutoFullDrives to defer with an error when no node has signed full drives, got plan %+v", plan)
	}
	if !strings.Contains(err.Error(), "signed full drives") {
		t.Errorf("want the no-signed-drives error, got: %v", err)
	}
	if *calls != 1 {
		t.Errorf("buildFullDrivesInventory called %d times, want 1 (the steady-state gate must fall through to it, not silently skip)", *calls)
	}
	select {
	case ev := <-rec.Events:
		if !strings.Contains(ev, "AutoFullDrivesNoSignedDrives") {
			t.Errorf("unexpected event: %q", ev)
		}
	default:
		t.Errorf("expected an AutoFullDrivesNoSignedDrives event to be emitted")
	}
}

// drainEvents non-blockingly collects every event currently buffered on rec (tests need the full count, not just presence).
func drainEvents(rec *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case ev := <-rec.Events:
			events = append(events, ev)
		default:
			return events
		}
	}
}

// The event table is the documented surface of both planner modes, so it must stay complete and in step with
// the docs. Catches two mistakes a compiler cannot: a reason constant with no policy row (emitPlannerEvent
// would fall back to a generic Warning), and a row whose severity or throttle drifts from what the Events
// tables in doc/operator/deployment/ promise.
func TestPlannerEventSpecsCoverEveryReason(t *testing.T) {
	allReasons := []string{
		reasonClusterCapacityPlanned, reasonClusterCapacityInfeasible, reasonClusterCapacityDeferred,
		reasonClusterCapacityShrink, reasonClusterCapacityOverProvisioned, reasonClusterCapacityHeterogeneousGrowth,
		reasonAutoFullDrivesPlanned, reasonAutoFullDrivesInfeasible, reasonAutoFullDrivesNoSignedDrives,
		reasonAutoFullDrivesGrowthDetected, reasonAutoFullDrivesGrowthDeferred, reasonAutoFullDrivesDrivesStranded,
		reasonAutoFullDrivesPlacementDeferred, reasonAutoFullDrivesComputeLayout, reasonAutoFullDrivesWarning,
		reasonAutoFullDrivesNodeIneligible,
	}
	for _, reason := range allReasons {
		if _, ok := plannerEventSpecs[reason]; !ok {
			t.Errorf("reason %q has no plannerEventSpecs row, so it would emit as a generic Warning", reason)
		}
	}
	if len(plannerEventSpecs) != len(allReasons) {
		t.Errorf("plannerEventSpecs has %d rows for %d reasons — a row exists for a reason nothing emits, or "+
			"a new reason was not added to this test", len(plannerEventSpecs), len(allReasons))
	}

	// The rows the docs are explicit about, and where a drift would be silent in production.
	for _, tc := range []struct {
		reason      string
		wantType    string
		wantLong    bool // 15-minute converged-state window rather than the 1-minute default
		wantPerNode bool
	}{
		{reasonAutoFullDrivesInfeasible, corev1.EventTypeWarning, false, false},
		{reasonAutoFullDrivesPlanned, corev1.EventTypeNormal, false, false},
		{reasonAutoFullDrivesGrowthDetected, corev1.EventTypeNormal, false, false},
		{reasonAutoFullDrivesGrowthDeferred, corev1.EventTypeWarning, true, false},
		// Expected under an explicit numDrives pin, so Normal and rate-limited as a converged state.
		{reasonAutoFullDrivesDrivesStranded, corev1.EventTypeNormal, true, false},
		// Per node: one constrained node must not starve the others' events.
		{reasonAutoFullDrivesPlacementDeferred, corev1.EventTypeNormal, true, true},
		// An administrative state (cordon/taint/NotReady), not a planner failure: Normal, per node, and
		// rate-limited — a node left cordoned for maintenance must not post a Warning every minute.
		{reasonAutoFullDrivesNodeIneligible, corev1.EventTypeNormal, true, true},
		{reasonAutoFullDrivesComputeLayout, corev1.EventTypeWarning, true, false},
		{reasonClusterCapacityHeterogeneousGrowth, corev1.EventTypeWarning, false, false},
		{reasonClusterCapacityPlanned, corev1.EventTypeNormal, false, false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			spec := plannerEventSpecs[tc.reason]
			if spec.eventType != tc.wantType {
				t.Errorf("eventType = %q, want %q", spec.eventType, tc.wantType)
			}
			wantInterval := time.Minute
			if tc.wantLong {
				wantInterval = plannerConvergedEventInterval
			}
			if spec.interval != wantInterval {
				t.Errorf("interval = %v, want %v", spec.interval, wantInterval)
			}
			if got := spec.key == keyPerNode; got != tc.wantPerNode {
				t.Errorf("keyPerNode = %v, want %v", got, tc.wantPerNode)
			}
		})
	}
}

// clusterCapacity keeps exactly its historical reason set: the daemonset mode's per-cause split and its
// growth announcements do not extend to it, since that would add new reasons to a shipped event surface
// and change when ClusterCapacityPlanned fires.
func TestClusterCapacityEventSurfaceUnchanged(t *testing.T) {
	for _, reason := range []string{
		reasonAutoFullDrivesGrowthDetected, reasonAutoFullDrivesGrowthDeferred,
		reasonAutoFullDrivesDrivesStranded, reasonAutoFullDrivesPlacementDeferred,
	} {
		if strings.HasPrefix(reason, "ClusterCapacity") {
			t.Errorf("%q is a clusterCapacity reason but belongs to the daemonset surface", reason)
		}
	}
	clusterCapacityReasons := 0
	for reason := range plannerEventSpecs {
		if strings.HasPrefix(reason, "ClusterCapacity") {
			clusterCapacityReasons++
		}
	}
	if clusterCapacityReasons != 6 {
		t.Errorf("clusterCapacity has %d reasons, want its historical 6 (Planned, Infeasible, Deferred, Shrink, "+
			"OverProvisioned, HeterogeneousGrowth)", clusterCapacityReasons)
	}
}

// TestPlanAutoFullDrivesNeverAnnouncesGrowthItself pins that AutoFullDrivesGrowthDetected is never emitted by
// planning alone; emission lives only in announceDriveGrowth. The fixture here must still produce growth,
// or the assertion is vacuous.
func TestPlanAutoFullDrivesNeverAnnouncesGrowthItself(t *testing.T) {
	withoutFormClusterComputeFloor(t)
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * tib
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	// Growth-only fixture. Status.NodeAffinity must be set: an ExistingContainer whose pod is unscheduled is
	// frozen by the planner (never grown), which would make plan.Grow empty and the assertion below vacuous.
	const bigFree = 1 << 28
	drive := &weka.WekaContainer{}
	drive.Name = "drive-n1"
	drive.Spec.Mode = weka.WekaContainerModeDrive
	drive.Spec.NodeAffinity = "n1"
	drive.Status.NodeAffinity = "n1"
	drive.Spec.NumDrives = 1
	drive.Spec.DriveCapacity = 1000
	drive.Spec.NumCores = 1

	n1 := capacityplanner.NodeCapacity{
		NodeName:              "n1",
		FDValue:               "fdA",
		DriveCapacitiesGiB:    []int{1000, 1000, 1000, 1000, 1000},
		TlcGiB:                5000,
		OwnDriveCapacitiesGiB: []int{1000},
		AllocatableCPU:        10,
		AvailableHugepagesMiB: bigFree,
		AvailableMemoryMiB:    bigFree,
	}

	// Diskless compute nodes: n1 alone cannot host both the grown drive container and the compute its
	// drive cores require at the 2:1 ratio, which would make the plan infeasible and the assertion below
	// vacuous. These carry the compute so the growth path is actually exercised.
	computeOnly := func(name string) capacityplanner.NodeCapacity {
		return capacityplanner.NodeCapacity{
			NodeName: name, FDValue: "fd-" + name,
			AllocatableCPU: 32, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		}
	}
	n2, n3 := computeOnly("n2"), computeOnly("n3")

	rec := record.NewFakeRecorder(16)
	r, _ := newAutoFullDrivesLoop([]*weka.WekaContainer{drive}, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{"n1": "fdA", "n2": "fd-n2", "n3": "fd-n3"},
			[]capacityplanner.NodeCapacity{n1, n2, n3},
			map[string]bool{"n1": true, "n2": true, "n3": true}, nil
	})
	r.Recorder = rec

	plan, err := r.planAutoFullDrives(t.Context())
	if err != nil {
		t.Fatalf("planAutoFullDrives() unexpected error: %v", err)
	}
	if len(plan.Grow) == 0 {
		t.Fatalf("fixture no longer produces growth (plan.Grow empty) -- the event assertion below would be "+
			"vacuous; plan: create=%d infeasible=%q", len(plan.Create), plan.Infeasible)
	}

	var growthEvents []string
	for _, ev := range drainEvents(rec) {
		if strings.Contains(ev, "AutoFullDrivesGrowthDetected") {
			growthEvents = append(growthEvents, ev)
		}
	}
	if len(growthEvents) != 0 {
		t.Fatalf("got %d AutoFullDrivesGrowthDetected event(s), want 0 -- planAutoFullDrives must not announce growth it does "+
			"not apply; announceDriveGrowth emits this reason for the entries actually written. got: %v",
			len(growthEvents), growthEvents)
	}
}

// TestAutoFullDrivesWarningReasonAndSeverity pins the per-cause event mapping: each warning kind must map to
// its own reason, and the two non-problem causes must not be Warnings.
func TestAutoFullDrivesWarningReasonAndSeverity(t *testing.T) {
	for _, tc := range []struct {
		kind       capacityplanner.WarningKind
		wantReason string
		wantType   string
	}{
		{capacityplanner.WarningKindComputeLayout, "AutoFullDrivesComputeLayout", corev1.EventTypeWarning},
		// Not problems, so Normal: an explicit numDrives pin causes stranding by doing exactly what it says,
		// and a placement deferral clears itself; Warning would accumulate forever on a healthy cluster.
		{capacityplanner.WarningKindDrivesStranded, "AutoFullDrivesDrivesStranded", corev1.EventTypeNormal},
		{capacityplanner.WarningKindTransient, "AutoFullDrivesPlacementDeferred", corev1.EventTypeNormal},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			got := autoFullDrivesWarningReason(tc.kind)
			if got != tc.wantReason {
				t.Errorf("autoFullDrivesWarningReason(%q) = %q, want %q", tc.kind, got, tc.wantReason)
			}
			if spec := plannerEventSpecs[got]; spec.eventType != tc.wantType {
				t.Errorf("%q emits as %q, want %q", got, spec.eventType, tc.wantType)
			}
		})
	}

	// Every distinct kind must get a distinct reason, or the mapping has collapsed two causes back together.
	seen := map[string]capacityplanner.WarningKind{}
	for _, k := range []capacityplanner.WarningKind{
		capacityplanner.WarningKindDrivesStranded,
		capacityplanner.WarningKindTransient, capacityplanner.WarningKindComputeLayout,
	} {
		r := autoFullDrivesWarningReason(k)
		if prev, dup := seen[r]; dup {
			t.Errorf("kinds %q and %q both map to reason %q — distinct causes need distinct reasons", prev, k, r)
		}
		seen[r] = k
	}

	// An unclassified kind must stay visible under the catch-all rather than be dropped, so a kind added to
	// the planner without a case here still reaches the operator.
	if got := autoFullDrivesWarningReason(capacityplanner.WarningKind("SomethingNew")); got != "AutoFullDrivesWarning" {
		t.Errorf("autoFullDrivesWarningReason(unknown) = %q, want the AutoFullDrivesWarning fallback so it is never silent", got)
	}
}

// TestAutoFullDrivesWarningsThrottlePerSubject: N constrained nodes must each get their own event, and a message
// whose numbers drift between reconciles must not spawn a second event for a subject already reported (lab:
// two event objects for one condition, "held 6 node(s)" then "held 5 node(s)").
func TestAutoFullDrivesWarningsThrottlePerSubject(t *testing.T) {
	loop := newAutoFullDrivesGrowthLoop(t, nil)

	emit := func(subject, message string) {
		if err := loop.RecordEventThrottledPerSubject(corev1.EventTypeWarning, "AutoFullDrivesComputeLayout",
			subject, message, time.Minute); err != nil {
			t.Fatalf("RecordEventThrottledPerSubject: %v", err)
		}
	}
	emit("node-a", "node node-a cannot host its drives (node has 822 MiB free)")
	emit("node-b", "node node-b cannot host its drives (node has 640 MiB free)")
	// Same subject, drifting figure in the text: must be throttled, since node-a is already reported.
	emit("node-a", "node node-a cannot host its drives (node has 118 MiB free)")

	got := eventsMatching(drainLoopEvents(t, loop), "AutoFullDrivesComputeLayout")
	if len(got) != 2 {
		t.Fatalf("got %d event(s), want exactly 2 — one per subject, and the re-report of node-a with a "+
			"changed number must be throttled: %v", len(got), got)
	}
	var sawA, sawB bool
	for _, ev := range got {
		sawA = sawA || strings.Contains(ev, "node-a")
		sawB = sawB || strings.Contains(ev, "node-b")
	}
	if !sawA || !sawB {
		t.Errorf("want one event per constrained node (node-a and node-b), got: %v", got)
	}
}

// withoutFormClusterComputeFloor disables the form-cluster compute-container floor
// (CapacityConstraints.MinComputeContainers) for a test; these fixtures use small fleets that couldn't
// host it otherwise. The floor itself is covered by TestPlanAutoFullDrivesHonorsFormClusterComputeFloor.
func withoutFormClusterComputeFloor(t *testing.T) {
	t.Helper()
	prev := globalconfig.Consts.FormClusterMinComputeContainers
	globalconfig.Consts.FormClusterMinComputeContainers = 0
	t.Cleanup(func() { globalconfig.Consts.FormClusterMinComputeContainers = prev })
}

// withFormClusterComputeFloor pins the form-cluster compute floor for a test.
func withFormClusterComputeFloor(t *testing.T, n int) {
	t.Helper()
	prev := globalconfig.Consts.FormClusterMinComputeContainers
	globalconfig.Consts.FormClusterMinComputeContainers = n
	t.Cleanup(func() { globalconfig.Consts.FormClusterMinComputeContainers = prev })
}

// TestPlanAutoFullDrivesHonorsFormClusterComputeFloor is the regression test for an auto-full-drives cluster
// that planned cleanly, ran every pod, and then never formed (lab: driveCores:3 across 8 nodes needed 48
// compute cores and got 4 containers x 12, one below the 5 weka requires -- stuck forever on "expected 5,
// got 4" with healthy idle pods). The floor must be enforced where the container count is derived.
func TestPlanAutoFullDrivesHonorsFormClusterComputeFloor(t *testing.T) {
	// 8 nodes x 3 drives: 24 drive cores -> 48 required compute cores, which 4 containers can carry.
	nodes := make([]capacityplanner.NodeCapacity, 0, 8)
	computeNodes := map[string]bool{}
	for _, name := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8"} {
		nodes = append(nodes, autoFullDrivesNode(name, name, []int{1024, 1024, 1024}))
		computeNodes[name] = true
	}
	newLoop := func() *wekaClusterReconcilerLoop {
		r, _ := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
			return map[string]string{}, nodes, computeNodes, nil
		})
		return r
	}

	t.Run("floor of 5 is respected", func(t *testing.T) {
		withFormClusterComputeFloor(t, 5)
		plan, err := newLoop().planAutoFullDrives(t.Context())
		if err != nil {
			t.Fatalf("planAutoFullDrives returned error: %v", err)
		}
		if got := len(plan.ComputeLayout); got < 5 {
			t.Errorf("got %d compute container(s), want >= 5 — below the form-cluster minimum the cluster "+
				"never forms, so the planner must not size fewer: %+v", got, plan.ComputeLayout)
		}
		// Spreading over more containers must not trade cores away. Asserted against plan.RequiredComputeCores
		// rather than a literal, since the compute:drive ratio comes from config.
		total := 0
		for _, c := range plan.ComputeLayout {
			total += c.NumCores
		}
		if total < plan.RequiredComputeCores {
			t.Errorf("compute cores = %d, want >= the required %d — the floor must add containers, not trade "+
				"cores away", total, plan.RequiredComputeCores)
		}
	})

	t.Run("floor of zero leaves sizing untouched", func(t *testing.T) {
		// Proves the subtest above changed the count because of the floor, not because this fleet needed 5
		// containers anyway, and that the floor is opt-in.
		withFormClusterComputeFloor(t, 0)
		plan, err := newLoop().planAutoFullDrives(t.Context())
		if err != nil {
			t.Fatalf("planAutoFullDrives returned error: %v", err)
		}
		if got := len(plan.ComputeLayout); got >= 5 {
			t.Errorf("got %d compute container(s) with the floor disabled, want fewer than 5 — otherwise this "+
				"fleet needs 5 regardless and the floor-of-5 subtest proves nothing: %+v", got, plan.ComputeLayout)
		}
	})
}

// An infeasible plan is the sole signal: the advisories describe placement that will not happen, so emitting
// them alongside the infeasibility is noise on a plan that creates nothing. Both planner entries gate on this
// by returning before their advisory loops, so the fixture must produce both an infeasibility and a warning,
// or the assertion is vacuous.
func TestPlanAutoFullDrivesInfeasibleSuppressesAdvisories(t *testing.T) {
	withoutFormClusterComputeFloor(t)

	loop, _ := newAutoFullDrivesLoop(nil, func() (map[string]string, []capacityplanner.NodeCapacity, map[string]bool, error) {
		return map[string]string{}, []capacityplanner.NodeCapacity{
			{
				NodeName: "n1", FDValue: "n1",
				DriveCapacitiesGiB: []int{1000, 1000}, TlcGiB: 2000,
				AllocatableCPU: 0, AvailableHugepagesMiB: 0, AvailableMemoryMiB: 0,
			},
			{
				NodeName: "n2", FDValue: "n2",
				DriveCapacitiesGiB: []int{1000}, TlcGiB: 1000, HasDeletingDriveContainer: true,
				AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
			},
		}, map[string]bool{"n1": true, "n2": true}, nil
	})

	plan, err := loop.planAutoFullDrives(context.Background())
	if err == nil || plan != nil {
		t.Fatalf("want an infeasible plan to return a WaitError and no plan, got plan=%v err=%v", plan, err)
	}

	events := drainLoopEvents(t, loop)
	if got := eventsMatching(events, reasonAutoFullDrivesInfeasible); len(got) != 1 {
		t.Fatalf("want exactly 1 %s event, got %d: %v", reasonAutoFullDrivesInfeasible, len(got), events)
	}
	for _, suppressed := range []string{
		reasonAutoFullDrivesPlacementDeferred, reasonAutoFullDrivesDrivesStranded,
		reasonAutoFullDrivesComputeLayout, reasonAutoFullDrivesWarning, reasonAutoFullDrivesPlanned,
	} {
		if got := eventsMatching(events, suppressed); len(got) != 0 {
			t.Errorf("%s leaked on an infeasible plan (%v) — the advisory loops must stay below the gate",
				suppressed, got)
		}
	}
}
