package wekacluster

import (
	"slices"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// operationalContainer builds a Drive-mode container that passes discovery.IsContainerOperational
// and matches the cluster's base port: non-nil ClusterContainerID, InternalStatus "READY", a
// management IP, and Allocations.WekaPort equal to basePort. Status defaults to Running; override
// via the returned pointer to exercise the non-Running-but-operational path.
func operationalContainer(name string, basePort int) *weka.WekaContainer {
	id := 1
	c := &weka.WekaContainer{}
	c.Name = name
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Status.Status = weka.Running
	c.Status.InternalStatus = "READY"
	c.Status.ClusterContainerID = &id
	c.Status.ManagementIPs = []string{"10.0.0.1"}
	c.Status.Allocations = &weka.ContainerAllocations{WekaPort: basePort}
	return c
}

// names returns the ordered container names, for asserting both set membership and order in one shot.
func names(containers []*weka.WekaContainer) []string {
	out := make([]string, len(containers))
	for i, c := range containers {
		out[i] = c.Name
	}
	return out
}

// TestSelectActiveContainersForManagement_OrderStableAcrossStatusFlip pins the invariant behind the
// final slices.SortFunc in selectActiveContainersForManagement: the two-pass Running/operational
// selection is a priority mechanism for WHICH containers make the cap, and must not leak into the
// returned ORDER. A container leaving Running while remaining operational (e.g. weka.Error, with
// InternalStatus still READY) must keep its alphabetical slot, not jump to the tail — that ordering
// is rendered into eds.yaml, so a pure permutation would rewrite the ConfigMap and make Envoy reload
// endpoints it already has.
func TestSelectActiveContainersForManagement_OrderStableAcrossStatusFlip(t *testing.T) {
	const basePort = 14000

	// 6 eligible containers (<= MaxManagementServiceEndpoints), alphabetically named so the fixed
	// sort order is easy to state.
	containerNames := []string{"c1-alpha", "c2-bravo", "c3-charlie", "c4-delta", "c5-echo", "c6-foxtrot"}
	wantOrder := append([]string(nil), containerNames...) // already ascending

	buildLoop := func(flipIdx int, flipStatus weka.ContainerStatus) *wekaClusterReconcilerLoop {
		var containers []*weka.WekaContainer
		for i, n := range containerNames {
			c := operationalContainer(n, basePort)
			if i == flipIdx {
				c.Status.Status = flipStatus
			}
			containers = append(containers, c)
		}
		cluster := &weka.WekaCluster{}
		cluster.Status.Ports.BasePort = basePort
		return &wekaClusterReconcilerLoop{containers: containers, cluster: cluster}
	}

	baseline := buildLoop(-1, "") // no flip: all Running
	baselineResult := baseline.selectActiveContainersForManagement()
	if got := names(baselineResult); !slices.Equal(got, wantOrder) {
		t.Fatalf("baseline order = %v, want %v", got, wantOrder)
	}

	t.Run("flip alphabetically-first container to Error", func(t *testing.T) {
		// The alphabetically FIRST container is the one whose old (pre-fix) tail-append would permute
		// the whole list -- the strongest case for catching a regression of the final sort.
		loop := buildLoop(0, weka.Error)
		got := names(loop.selectActiveContainersForManagement())
		if !slices.Equal(got, wantOrder) {
			t.Errorf("flipping c1-alpha to Error changed order: got %v, want %v (unchanged)", got, wantOrder)
		}
	})

	t.Run("flip a middle container to Error", func(t *testing.T) {
		loop := buildLoop(3, weka.Error) // c4-delta
		got := names(loop.selectActiveContainersForManagement())
		if !slices.Equal(got, wantOrder) {
			t.Errorf("flipping c4-delta to Error changed order: got %v, want %v (unchanged)", got, wantOrder)
		}
	})

	t.Run("genuinely non-operational status removes the container", func(t *testing.T) {
		// weka.Stopped is on IsContainerOperational's reject list: unlike the Error case above, this
		// must actually shrink the returned set, not just reorder it.
		loop := buildLoop(0, weka.Stopped)
		got := names(loop.selectActiveContainersForManagement())
		wantWithoutFirst := wantOrder[1:]
		if !slices.Equal(got, wantWithoutFirst) {
			t.Errorf("stopping c1-alpha: got %v, want %v (removed, not reordered)", got, wantWithoutFirst)
		}
	})

	t.Run("non-READY internal status removes the container", func(t *testing.T) {
		loop := buildLoop(-1, "")
		loop.containers[2].Status.InternalStatus = "NOT_READY" // c3-charlie
		got := names(loop.selectActiveContainersForManagement())
		want := []string{"c1-alpha", "c2-bravo", "c4-delta", "c5-echo", "c6-foxtrot"}
		if !slices.Equal(got, want) {
			t.Errorf("non-READY c3-charlie: got %v, want %v (removed, not reordered)", got, want)
		}
	})
}
