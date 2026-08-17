package allocator

import (
	"context"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

func TestComputeCapacityBasedHugepages_MaxCap(t *testing.T) {
	// containerCapacity=5000 * 6 drive containers, all TLC, computeContainers=6, computeCores=1:
	// 30000*1024/1000=30720 cluster MiB, /6=5120 + 1700 = 6820 (uncapped baseline for the table below).
	totalRawCapacityGiB := 5000 * 6 // 30000
	computeContainers := 6
	computeCores := 1

	tests := []struct {
		name     string
		maxCap   int
		expected int
	}{
		{
			name:     "no cap (0)",
			maxCap:   0,
			expected: 6820,
		},
		{
			name:     "cap above result",
			maxCap:   500000,
			expected: 6820,
		},
		{
			name:     "cap below result, even",
			maxCap:   5000,
			expected: 5000,
		},
		{
			name:     "cap exactly equals result",
			maxCap:   6820,
			expected: 6820,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origMax := globalconfig.Config.ComputeMaxHugepagesMiB
			defer func() { globalconfig.Config.ComputeMaxHugepagesMiB = origMax }()
			globalconfig.Config.ComputeMaxHugepagesMiB = tt.maxCap

			got := ComputeCapacityBasedHugepages(
				context.Background(), totalRawCapacityGiB, computeContainers, computeCores, nil,
			)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func TestComputeCapacityBasedHugepages_DividesByComputeContainers(t *testing.T) {
	// Regression guard: the capacity-share divisor must be the actual (planner-derived) compute
	// container count, not the FormClusterMinComputeContainers default (5), or hugepages are over-provisioned.
	origTlc := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	origQlc := globalconfig.Config.DriveSharing.HugepagesQlcRatio
	origMax := globalconfig.Config.ComputeMaxHugepagesMiB
	defer func() {
		globalconfig.Config.DriveSharing.HugepagesTlcRatio = origTlc
		globalconfig.Config.DriveSharing.HugepagesQlcRatio = origQlc
		globalconfig.Config.ComputeMaxHugepagesMiB = origMax
	}()
	globalconfig.Config.DriveSharing.HugepagesTlcRatio = 3500
	globalconfig.Config.ComputeMaxHugepagesMiB = 0

	// All-TLC, ~150 TiB raw. clusterMiB = 307200*1024/3500 = 89877; perCore = 1700*10 = 17000.
	totalRawCapacityGiB := 307200
	computeCores := 10

	// derived count (6): 89877/6 = 14979 + 17000 = 31979 -> rounded up to even 31980
	got6 := ComputeCapacityBasedHugepages(context.Background(), totalRawCapacityGiB, 6, computeCores, nil)
	if got6 != 31980 {
		t.Errorf("computeContainers=6: expected 31980, got %d", got6)
	}

	// buggy min-default (5): 89877/5 = 17975 + 17000 = 34975 -> rounded up to even 34976
	got5 := ComputeCapacityBasedHugepages(context.Background(), totalRawCapacityGiB, 5, computeCores, nil)
	if got5 != 34976 {
		t.Errorf("computeContainers=5: expected 34976, got %d", got5)
	}

	// Dividing by the real (larger) count must yield strictly fewer hugepages.
	if got6 >= got5 {
		t.Errorf("expected divisor=6 result (%d) < divisor=5 result (%d)", got6, got5)
	}
}

func TestCalculateDriveHugepages(t *testing.T) {
	tests := []struct {
		name       string
		numDrives  int
		driveCores int
		expected   int
	}{
		{
			name:       "traditional mode (NumDrives > 0)",
			numDrives:  4,
			driveCores: 2,
			expected:   1400*2 + 200*4,
		},
		{
			name:       "drive-sharing mode (NumDrives == 0)",
			numDrives:  0,
			driveCores: 2,
			expected:   1600 * 2,
		},
		{
			name:       "traditional, single core, single drive",
			numDrives:  1,
			driveCores: 1,
			expected:   1400 + 200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := ClusterTemplate{
				NumDrives: tt.numDrives,
				Cores:     IntPerWekaRole{Drive: tt.driveCores},
			}
			got := CalculateDriveHugepages(template)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

// Auto full drives must size compute through GetContainerHugepages -> ComputeHugepagesFromPlan, never
// falling through to the template-based path: template.Containers.Drive is the form-cluster minimum in
// this mode, not the planned container count, so a fallback would size compute wrong.
func TestCalculateDynamicComputeHugepages_AutoFullDrives_Errors(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	cluster := &weka.WekaCluster{}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{} // nothing set -> auto full drives
	if !cluster.Spec.Dynamic.UsesAutoFullDrives() {
		t.Fatal("test precondition failed: expected auto-full-drives mode")
	}

	template := GetWekaClusterTemplate(cluster.Spec.Dynamic)
	template.Cores.Compute = 4
	_, err := GetContainerHugepages(context.Background(), k8sClient, template, cluster, nil, "compute")
	if err == nil {
		t.Fatal("want an error for planner-managed compute, got none — a silent fallback would size the container wrong")
	}
	if !strings.Contains(err.Error(), "ComputeHugepagesFromPlan") {
		t.Errorf("error should point at the correct entry point, got: %v", err)
	}
}

func TestCalculateDynamicComputeHugepages_CountBased_FullDrivesUnchanged(t *testing.T) {
	origMax := globalconfig.Config.ComputeMaxHugepagesMiB
	origTlc := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	t.Cleanup(func() {
		globalconfig.Config.ComputeMaxHugepagesMiB = origMax
		globalconfig.Config.DriveSharing.HugepagesTlcRatio = origTlc
	})
	globalconfig.Config.ComputeMaxHugepagesMiB = 0
	globalconfig.Config.DriveSharing.HugepagesTlcRatio = 1000

	nodeA := makeNode("nodeA", []domain.DriveEntry{{Serial: "sn1", CapacityGiB: 3000}, {Serial: "sn2", CapacityGiB: 4000}}, nil)
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(nodeA).Build()

	cluster := &weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			// ComputeContainers/DriveContainers set (both-or-neither) so this is count-based, not auto
			// full drives — otherwise UsesAutoFullDrives() would route this through the other branch.
			Dynamic: &weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6, NumDrives: 2},
		},
	}
	driveContainer := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "drive-1"},
		Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive},
		Status: weka.WekaContainerStatus{
			NodeAffinity: "nodeA",
			Allocations:  &weka.ContainerAllocations{Drives: []string{"sn1", "sn2"}},
		},
	}
	containers := []*weka.WekaContainer{driveContainer}

	template := ClusterTemplate{
		Containers: IntPerWekaRole{Compute: 6, Drive: 6},
		Cores:      IntPerWekaRole{Compute: 1},
		NumDrives:  2,
	}

	got, err := calculateDynamicComputeHugepages(context.Background(), k8sClient, template, cluster, containers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// perContainerGiB=7000 extrapolated over Containers.Drive=6 -> 42000GiB -> 43008MiB /6=7168 +1700=8868.
	const expected = 8868
	if got != expected {
		t.Errorf("expected %d, got %d", expected, got)
	}
}

func TestCalculateDriveHugepagesOffset(t *testing.T) {
	tests := []struct {
		name       string
		numDrives  int
		driveCores int
		expected   int
	}{
		{
			name:       "traditional mode (NumDrives > 0)",
			numDrives:  4,
			driveCores: 2,
			expected:   200 * 4,
		},
		{
			name:       "drive-sharing mode (NumDrives == 0)",
			numDrives:  0,
			driveCores: 2,
			expected:   200 * 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := ClusterTemplate{
				NumDrives: tt.numDrives,
				Cores:     IntPerWekaRole{Drive: tt.driveCores},
			}
			got := CalculateDriveHugepagesOffset(template)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

// capacityplanner.ComputeContainerHugepagesMiB (the planner's node-fit gate) and ComputeCapacityBasedHugepages
// (the container controller) must agree, modulo the per-core DPDK term the planner folds in early. If they
// drift, a cluster reserves one figure and its pods request another.
func TestComputeHugepagesFromPlanMatchesTheContainerControllerFormula(t *testing.T) {
	prevMax := globalconfig.Config.ComputeMaxHugepagesMiB
	prevTlc := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	prevQlc := globalconfig.Config.DriveSharing.HugepagesQlcRatio
	t.Cleanup(func() {
		globalconfig.Config.ComputeMaxHugepagesMiB = prevMax
		globalconfig.Config.DriveSharing.HugepagesTlcRatio = prevTlc
		globalconfig.Config.DriveSharing.HugepagesQlcRatio = prevQlc
	})
	globalconfig.Config.DriveSharing.HugepagesTlcRatio = 1000
	globalconfig.Config.DriveSharing.HugepagesQlcRatio = 6000

	for _, tc := range []struct {
		name           string
		totalRawGiB    int
		count, cores   int
		maxHugepagesMi int
	}{
		{"capacity term dominates", 686736, 18, 6, 360000},
		{"per-core floor dominates", 100, 8, 4, 360000},
		{"odd rounding", 1001, 3, 1, 360000},
		{"cap binds", 686736, 2, 4, 40000},
		{"single container", 20000, 1, 12, 360000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			globalconfig.Config.ComputeMaxHugepagesMiB = tc.maxHugepagesMi

			cluster := &weka.WekaCluster{}
			cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}
			dpdkPerCore := utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeCompute)

			// TLC-only, so both sides split capacity the same way with a nil ratio. Must use
			// ConstraintsForClusterSpec, not CapacityConstraintsFromConfig, which omits the per-role DPDK term.
			cons := ConstraintsForClusterSpec(&cluster.Spec)
			planned := capacityplanner.ComputeContainerHugepagesMiB(tc.totalRawGiB, 0, tc.count, tc.cores, cons)
			fromPlan := ComputeHugepagesFromPlan(cluster, planned, tc.cores).Hugepages

			want := ComputeCapacityBasedHugepages(context.Background(), tc.totalRawGiB, tc.count, tc.cores, nil) + dpdkPerCore*tc.cores
			if fromPlan != want {
				t.Errorf("ComputeHugepagesFromPlan = %d, want %d (ComputeCapacityBasedHugepages + %d MiB/core DPDK) — "+
					"the planner's reservation and the pod's request have drifted apart",
					fromPlan, want, dpdkPerCore)
			}
		})
	}
}
