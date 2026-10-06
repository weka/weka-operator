package validation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/weka/weka-operator/internal/consts"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestAutoFullDrivesPins(t *testing.T) {
	v := &clusterAutoFullDrivesPins{}
	ctx := context.Background()
	labels := map[string]string{"role": "drive"}
	cluster := func(d *weka.WekaClusterTemplate) *weka.WekaCluster {
		c := minDrivesCluster(d, 0)
		c.Spec.NodeSelector = labels
		return c
	}
	fleet := fakeClientWithNodes(t,
		driveRoleNode(t, "n1", labels, []int{1000, 1000, 1000}),
		driveRoleNode(t, "n2", labels, []int{1000, 1000}),
		driveRoleNode(t, "unsigned", labels, nil),
	)

	t.Run("no pins is silent", func(t *testing.T) {
		if errs := v.Validate(ctx, fleet, cluster(&weka.WekaClusterTemplate{})); len(errs) != 0 {
			t.Errorf("expected none, got %v", errs)
		}
	})

	t.Run("not auto-full-drives is silent", func(t *testing.T) {
		d := &weka.WekaClusterTemplate{DriveContainers: 3, NumDrives: 9}
		if errs := v.Validate(ctx, fleet, cluster(d)); len(errs) != 0 {
			t.Errorf("expected none, got %v", errs)
		}
	})

	t.Run("pins within every signed node pass", func(t *testing.T) {
		d := &weka.WekaClusterTemplate{NumDrives: 2, DriveCores: 2}
		if errs := v.Validate(ctx, fleet, cluster(d)); len(errs) != 0 {
			t.Errorf("expected none, got %v", errs)
		}
	})

	t.Run("numDrives above a node's signed count flags only that node", func(t *testing.T) {
		errs := v.Validate(ctx, fleet, cluster(&weka.WekaClusterTemplate{NumDrives: 3}))
		if len(errs) != 1 || errs[0].Field != "spec.dynamicTemplate.numDrives" {
			t.Fatalf("expected one numDrives error, got %v", errs)
		}
		d := errs[0].Detail
		if !strings.Contains(d, "n2 (signed 2)") || strings.Contains(d, "n1") || strings.Contains(d, "unsigned") {
			t.Errorf("expected only n2, got %q", d)
		}
	})

	t.Run("signed node with no usable drives is skipped", func(t *testing.T) {
		blocked := driveRoleNode(t, "blocked", labels, []int{1000})
		blocked.Annotations[consts.AnnotationBlockedDrives] = `["blocked-d0"]`
		f := fakeClientWithNodes(t, driveRoleNode(t, "empty", labels, []int{}), blocked, driveRoleNode(t, "n1", labels, []int{1000, 1000}))
		if errs := v.Validate(ctx, f, cluster(&weka.WekaClusterTemplate{NumDrives: 2, DriveCores: 2})); len(errs) != 0 {
			t.Errorf("expected none, got %v", errs)
		}
	})

	t.Run("driveCores above signed drives when numDrives unset", func(t *testing.T) {
		errs := v.Validate(ctx, fleet, cluster(&weka.WekaClusterTemplate{DriveCores: 3}))
		if len(errs) != 1 || errs[0].Field != "spec.dynamicTemplate.driveCores" {
			t.Fatalf("expected one driveCores error, got %v", errs)
		}
		if !strings.Contains(errs[0].Detail, "n2 (signed 2)") {
			t.Errorf("expected n2, got %q", errs[0].Detail)
		}
	})

	t.Run("driveCores is checked against the numDrives pin", func(t *testing.T) {
		errs := v.Validate(ctx, fleet, cluster(&weka.WekaClusterTemplate{NumDrives: 1, DriveCores: 2}))
		if len(errs) != 1 || errs[0].Field != "spec.dynamicTemplate.driveCores" {
			t.Fatalf("expected one driveCores error, got %v", errs)
		}
		if !strings.Contains(errs[0].Detail, "2 drive-role node(s)") {
			t.Errorf("expected both signed nodes flagged, got %q", errs[0].Detail)
		}
	})

	t.Run("both fields reported separately", func(t *testing.T) {
		if errs := v.Validate(ctx, fleet, cluster(&weka.WekaClusterTemplate{NumDrives: 3, DriveCores: 4})); len(errs) != 2 {
			t.Errorf("expected two errors, got %v", errs)
		}
	})

	t.Run("lists at most 10 nodes", func(t *testing.T) {
		var nodes []*corev1.Node
		for i := 0; i < 12; i++ {
			nodes = append(nodes, driveRoleNode(t, fmt.Sprintf("n%02d", i), labels, []int{1000}))
		}
		errs := v.Validate(ctx, fakeClientWithNodes(t, nodes...), cluster(&weka.WekaClusterTemplate{NumDrives: 2}))
		if len(errs) != 1 || !strings.Contains(errs[0].Detail, "(+2 more)") {
			t.Errorf("expected (+2 more), got %v", errs)
		}
	})
}
