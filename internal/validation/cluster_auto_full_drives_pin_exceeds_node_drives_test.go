package validation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// autoFullDrivesPinCluster builds an auto-full-drives WekaCluster (no container-count or capacity
// field set) with the given driveCores/numDrives pins, spec.nodeSelector, and optionally
// spec.roleNodeSelector.drive.
func autoFullDrivesPinCluster(driveCores, numDrives int, selector, roleDriveSelector map[string]string) *weka.WekaCluster {
	c := &weka.WekaCluster{}
	c.Spec.Dynamic = &weka.WekaClusterTemplate{DriveCores: driveCores, NumDrives: numDrives}
	c.Spec.NodeSelector = selector
	if roleDriveSelector != nil {
		c.Spec.RoleNodeSelector.Drive = &roleDriveSelector
	}
	return c
}

// drivesOfCount returns a driveCapacitiesGiB slice of the given LENGTH for driveRoleNode: only len(...)
// matters here since this validator compares drive COUNT, not capacity.
func drivesOfCount(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = 100
	}
	return out
}

// TestAutoFullDrivesPin_DriveCoresAboveDriveCount covers a driveCores pin exceeding a node's drive
// count: unsatisfiable in full-drives mode. Must reject, name the node, and report its drive count.
func TestAutoFullDrivesPin_DriveCoresAboveDriveCount(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t, driveRoleNode(t, "big-node", labels, drivesOfCount(3)))

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(5, 0, labels, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	if got, want := errs[0].Field, "spec.dynamicTemplate.driveCores"; got != want {
		t.Errorf("expected field %q, got %q", want, got)
	}
	detail := errs[0].Detail
	for _, want := range []string{"big-node", "at most 3", "AutoFullDrivesInfeasible", "drive-sharing"} {
		if !strings.Contains(detail, want) {
			t.Errorf("expected message to contain %q, got: %s", want, detail)
		}
	}
}

// TestAutoFullDrivesPin_DriveCoresBelowDriveCountIsLossless is the inverted case of the deleted
// "too low" leg: with drives decoupled from cores, a pin below the drive count keeps every drive and
// runs it on fewer cores. It must NOT be reported.
func TestAutoFullDrivesPin_DriveCoresBelowDriveCountIsLossless(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t, driveRoleNode(t, "big-node", labels, drivesOfCount(10)))

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(2, 0, labels, nil))
	if len(errs) != 0 {
		t.Errorf("a driveCores pin below the drive count is lossless and supported; got %v", errs)
	}
}

// TestAutoFullDrivesPin_NumDrivesAboveSignedCount covers the third leg: numDrives pinned above what a
// node has signed cannot be honored there.
func TestAutoFullDrivesPin_NumDrivesAboveSignedCount(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t,
		driveRoleNode(t, "n-small", labels, drivesOfCount(4)),
		driveRoleNode(t, "n-large", labels, drivesOfCount(30)),
	)

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(0, 10, labels, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	if got, want := errs[0].Field, "spec.dynamicTemplate.numDrives"; got != want {
		t.Errorf("expected field %q, got %q", want, got)
	}
	detail := errs[0].Detail
	for _, want := range []string{"n-small", "at most 4", "1 affected node", "roleNodeSelector.drive"} {
		if !strings.Contains(detail, want) {
			t.Errorf("expected message to contain %q, got: %s", want, detail)
		}
	}
	if strings.Contains(detail, "n-large") {
		t.Errorf("expected only the worst node to be named, got: %s", detail)
	}
}

// TestAutoFullDrivesPin_NumDrivesPinnedSkipsDriveCoresLeg: with numDrives pinned, CEL already enforces
// numDrives >= driveCores, so this validator must not re-report the same comparison.
func TestAutoFullDrivesPin_NumDrivesPinnedSkipsDriveCoresLeg(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	// 30 signed drives, numDrives=5 (satisfiable), driveCores=8 > the effective 5. CEL owns it.
	c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, drivesOfCount(30)))

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(8, 5, labels, nil))
	if len(errs) != 0 {
		t.Errorf("expected the driveCores leg to be skipped under a numDrives pin, got %v", errs)
	}
}

// TestAutoFullDrivesPin_BothLegsAtOnce: a numDrives pin above one node's count while driveCores also
// exceeds the effective count. Only the numDrives leg fires (the driveCores leg is CEL's).
func TestAutoFullDrivesPin_BothLegsAtOnce(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t, driveRoleNode(t, "n-small", labels, drivesOfCount(2)))

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(6, 8, labels, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation (numDrives only), got %v", errs)
	}
	if got, want := errs[0].Field, "spec.dynamicTemplate.numDrives"; got != want {
		t.Errorf("expected field %q, got %q", want, got)
	}
}

// TestAutoFullDrivesPin_Adequate covers a driveCores pin that exactly matches every node's drive
// count: no violation.
func TestAutoFullDrivesPin_Adequate(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t,
		driveRoleNode(t, "n1", labels, drivesOfCount(4)),
		driveRoleNode(t, "n2", labels, drivesOfCount(4)),
	)

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(4, 0, labels, nil))
	if len(errs) != 0 {
		t.Errorf("expected no violation, got %v", errs)
	}
}

// TestAutoFullDrivesPin_WorstPick covers several nodes tripping the SAME leg: the fewest-drives node
// must be named and the affected count must include all of them.
func TestAutoFullDrivesPin_WorstPick(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t,
		driveRoleNode(t, "n-small", labels, drivesOfCount(2)),
		driveRoleNode(t, "n-medium", labels, drivesOfCount(4)),
		driveRoleNode(t, "n-large", labels, drivesOfCount(8)),
	)

	// driveCores=8 exceeds n-small(2) and n-medium(4), matches n-large(8) exactly.
	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(8, 0, labels, nil))
	if len(errs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", errs)
	}
	detail := errs[0].Detail
	if !strings.Contains(detail, "n-small") {
		t.Errorf("expected the worst (fewest-drives) node n-small to be named, got: %s", detail)
	}
	if strings.Contains(detail, "n-medium") || strings.Contains(detail, "n-large") {
		t.Errorf("expected only the worst node to be named, got: %s", detail)
	}
	if !strings.Contains(detail, "2 affected node") {
		t.Errorf("expected 2 affected nodes (n-small, n-medium), got: %s", detail)
	}
}

// TestAutoFullDrivesPin_NotAutoFullDrives: silent when a container count or capacity field puts the
// cluster in another sizing mode — clusterDriveCoresBelowCapacity owns those.
func TestAutoFullDrivesPin_NotAutoFullDrives(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, drivesOfCount(3)))

	for name, dyn := range map[string]*weka.WekaClusterTemplate{
		"containerCapacity": {ContainerCapacity: 6000, DriveCores: 9},
		"clusterCapacity":   {ClusterCapacity: "500TiB", DriveCores: 9},
		"counts":            {ComputeContainers: 6, DriveContainers: 6, DriveCores: 9},
	} {
		t.Run(name, func(t *testing.T) {
			cluster := &weka.WekaCluster{}
			cluster.Spec.Dynamic = dyn
			cluster.Spec.NodeSelector = labels
			if errs := v.Validate(ctx, c, cluster); len(errs) != 0 {
				t.Errorf("expected no violation outside auto-full-drives mode, got %v", errs)
			}
		})
	}
}

// TestAutoFullDrivesPin_NoPins covers the mode's default shape — a nil or empty dynamicTemplate, which
// IS auto-full-drives but carries no pins to check. Must not panic and must stay silent.
func TestAutoFullDrivesPin_NoPins(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, drivesOfCount(3)))

	t.Run("nil dynamicTemplate", func(t *testing.T) {
		cluster := &weka.WekaCluster{}
		cluster.Spec.NodeSelector = labels
		if errs := v.Validate(ctx, c, cluster); len(errs) != 0 {
			t.Errorf("expected no violation for a nil template, got %v", errs)
		}
	})
	t.Run("empty dynamicTemplate", func(t *testing.T) {
		if errs := v.Validate(ctx, c, autoFullDrivesPinCluster(0, 0, labels, nil)); len(errs) != 0 {
			t.Errorf("expected no violation for an empty template, got %v", errs)
		}
	})
}

// TestAutoFullDrivesPin_NoSignedDrives covers the pre-signing bootstrap case: no node has a populated
// full-drives annotation — silent, not a violation.
func TestAutoFullDrivesPin_NoSignedDrives(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}

	c := fakeClientWithNodes(t, driveRoleNode(t, "n1", labels, nil))

	if errs := v.Validate(ctx, c, autoFullDrivesPinCluster(9, 0, labels, nil)); len(errs) != 0 {
		t.Errorf("expected no violation pre-signing, got %v", errs)
	}
	if errs := v.Validate(ctx, c, autoFullDrivesPinCluster(0, 9, labels, nil)); len(errs) != 0 {
		t.Errorf("expected no violation pre-signing, got %v", errs)
	}
}

// TestAutoFullDrivesPin_NoMatchingNodes covers nodes existing but none matching the selector: silent.
func TestAutoFullDrivesPin_NoMatchingNodes(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()

	c := fakeClientWithNodes(t, driveRoleNode(t, "n1", map[string]string{"other": "label"}, drivesOfCount(2)))

	errs := v.Validate(ctx, c, autoFullDrivesPinCluster(9, 0, map[string]string{"role": "drive"}, nil))
	if len(errs) != 0 {
		t.Errorf("expected no violation when selector matches nothing, got %v", errs)
	}
}

// TestAutoFullDrivesPin_RoleSelectorFallback covers the role-selector fallback: unset uses
// spec.nodeSelector; set, it uses that instead and ignores the cluster-wide match.
func TestAutoFullDrivesPin_RoleSelectorFallback(t *testing.T) {
	v := &clusterAutoFullDrivesPinExceedsNodeDrives{}
	ctx := context.Background()

	clusterWideSelector := map[string]string{"pool": "general"}
	roleSelector := map[string]string{"drive-role": "yes"}

	const (
		roleNodeDrives    = 20
		clusterNodeDrives = 2
		pinnedDriveCores  = 21 // above both, so whichever node is matched trips the too-high leg
	)

	// n1 matches only the role-specific selector; n2 matches only the cluster-wide selector.
	nodeRoleOnly := driveRoleNode(t, "role-only-node", roleSelector, drivesOfCount(roleNodeDrives))
	nodeClusterOnly := driveRoleNode(t, "cluster-only-node", clusterWideSelector, drivesOfCount(clusterNodeDrives))

	t.Run("roleNodeSelector.drive unset falls back to spec.nodeSelector", func(t *testing.T) {
		c := fakeClientWithNodes(t, nodeRoleOnly, nodeClusterOnly)
		errs := v.Validate(ctx, c, autoFullDrivesPinCluster(pinnedDriveCores, 0, clusterWideSelector, nil))
		if len(errs) != 1 {
			t.Fatalf("expected exactly one violation, got %v", errs)
		}
		detail := errs[0].Detail
		if !strings.Contains(detail, "cluster-only-node") {
			t.Errorf("expected fallback to name cluster-only-node, got: %s", detail)
		}
		if strings.Contains(detail, "role-only-node") {
			t.Errorf("expected fallback to ignore role-only-node, got: %s", detail)
		}
		if !strings.Contains(detail, fmt.Sprintf("%d signed full drive", clusterNodeDrives)) {
			t.Errorf("expected drive count %d, got: %s", clusterNodeDrives, detail)
		}
	})

	t.Run("roleNodeSelector.drive set overrides spec.nodeSelector", func(t *testing.T) {
		c := fakeClientWithNodes(t, nodeRoleOnly, nodeClusterOnly)
		errs := v.Validate(ctx, c, autoFullDrivesPinCluster(pinnedDriveCores, 0, clusterWideSelector, roleSelector))
		if len(errs) != 1 {
			t.Fatalf("expected exactly one violation, got %v", errs)
		}
		detail := errs[0].Detail
		if !strings.Contains(detail, "role-only-node") {
			t.Errorf("expected role selector to name role-only-node, got: %s", detail)
		}
		if strings.Contains(detail, "cluster-only-node") {
			t.Errorf("expected role selector to ignore cluster-only-node, got: %s", detail)
		}
		if !strings.Contains(detail, fmt.Sprintf("%d signed full drive", roleNodeDrives)) {
			t.Errorf("expected drive count %d, got: %s", roleNodeDrives, detail)
		}
	})
}
