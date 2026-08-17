package allocator

import (
	"context"
	"encoding/json"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// TestGetDriveCores_DerivesFromCapacity verifies the static template path sizes drive cores from
// per-container capacity (matching the per-add feasibility gate's RequiredDriveCores model) instead of
// defaulting to 1 — the fix for DriveCapacityResourceShortfall on a freshly formed drive-sharing cluster.
func TestGetDriveCores_DerivesFromCapacity(t *testing.T) {
	// Set the per-core capacity caps deterministically (LoadCapacityEnv isn't called in unit tests).
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	prevQlc := globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024  // 5120 GiB/core
	globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = 50 * 1024 // 51200 GiB/core
	t.Cleanup(func() {
		globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc
		globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = prevQlc
	})

	tests := []struct {
		name              string
		containerCapacity int
		driveCores        int
		driveTypesRatio   *weka.DriveTypesRatio
		expected          int
	}{
		{
			name:              "6000 GiB TLC needs 2 cores (was under-cored at 1)",
			containerCapacity: 6000, // ceil(6000/5120)=2
			expected:          2,
		},
		{
			name:              "5000 GiB TLC fits in 1 core",
			containerCapacity: 5000, // ceil(5000/5120)=1
			expected:          1,
		},
		{
			name:              "full-drives mode (no containerCapacity) keeps default",
			containerCapacity: 0,
			expected:          1,
		},
		{
			name:              "explicit driveCores below requirement is honored",
			containerCapacity: 6000,
			driveCores:        1,
			expected:          1,
		},
		{
			name:              "explicit driveCores above requirement is preserved",
			containerCapacity: 6000,
			driveCores:        3,
			expected:          3,
		},
		{
			name:              "QLC-only capacity uses QLC per-core cap",
			containerCapacity: 60000, // all QLC: ceil(60000/51200)=2
			driveTypesRatio:   &weka.DriveTypesRatio{Tlc: 0, Qlc: 1},
			expected:          2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &weka.WekaClusterTemplate{
				ContainerCapacity: tt.containerCapacity,
				DriveCores:        tt.driveCores,
				DriveTypesRatio:   tt.driveTypesRatio,
			}
			if got := GetWekaContainerCores(config).Drive; got != tt.expected {
				t.Errorf("drive cores = %d, want %d", got, tt.expected)
			}
		})
	}
}

// TestGetDriveCores_DerivesFromNumDrivesCapacity verifies the numDrives+driveCapacity (legacy
// TLC-only) mode also derives drive cores from capacity, capped at numDrives per the CEL rule
// numDrives >= driveCores.
func TestGetDriveCores_DerivesFromNumDrivesCapacity(t *testing.T) {
	// Set the per-core capacity caps deterministically (LoadCapacityEnv isn't called in unit tests).
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	prevQlc := globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024  // 5120 GiB/core
	globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = 50 * 1024 // 51200 GiB/core
	t.Cleanup(func() {
		globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc
		globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = prevQlc
	})

	tests := []struct {
		name              string
		containerCapacity int
		numDrives         int
		driveCapacity     int
		driveCores        int
		expected          int
	}{
		{
			name:          "4 drives * 2000 GiB needs 2 cores",
			numDrives:     4,
			driveCapacity: 2000, // 8000 GiB, ceil(8000/5120)=2
			expected:      2,
		},
		{
			name:          "1 drive * 2000 GiB fits in 1 core",
			numDrives:     1,
			driveCapacity: 2000, // 2000 GiB, ceil(2000/5120)=1
			expected:      1,
		},
		{
			name:          "derived requirement capped at numDrives",
			numDrives:     2,
			driveCapacity: 20000, // 40000 GiB, ceil(40000/5120)=8, capped at numDrives=2
			expected:      2,
		},
		{
			name:          "explicit driveCores above requirement is preserved",
			numDrives:     4,
			driveCapacity: 1000, // 4000 GiB, needs 1 core
			driveCores:    3,
			expected:      3,
		},
		{
			name:          "pure full-drives mode (no driveCapacity) keeps default",
			numDrives:     4,
			driveCapacity: 0,
			expected:      1,
		},
		{
			name:          "no numDrives with driveCapacity set keeps default",
			numDrives:     0,
			driveCapacity: 2000,
			expected:      1,
		},
		{
			name:              "containerCapacity branch still used when numDrives is 0",
			containerCapacity: 6000, // ceil(6000/5120)=2
			numDrives:         0,
			expected:          2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &weka.WekaClusterTemplate{
				ContainerCapacity: tt.containerCapacity,
				NumDrives:         tt.numDrives,
				DriveCapacity:     tt.driveCapacity,
				DriveCores:        tt.driveCores,
			}
			if got := GetWekaContainerCores(config).Drive; got != tt.expected {
				t.Errorf("drive cores = %d, want %d", got, tt.expected)
			}
		})
	}
}

// TestDerivedDriveCores verifies DerivedDriveCores in isolation: it must ignore any explicit
// config.DriveCores (getDriveCores handles the override short-circuit) and report ok=false when
// the template has no capacity basis to derive from.
func TestDerivedDriveCores(t *testing.T) {
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	prevQlc := globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024  // 5120 GiB/core
	globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = 50 * 1024 // 51200 GiB/core
	t.Cleanup(func() {
		globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc
		globalconfig.Config.ClusterCapacity.QlcCapacityPerCoreGiB = prevQlc
	})

	tests := []struct {
		name          string
		config        *weka.WekaClusterTemplate
		expectedOk    bool
		expectedCores int
	}{
		{
			name:       "nil config not derivable",
			config:     nil,
			expectedOk: false,
		},
		{
			name: "containerCapacity mode derives, ignoring explicit driveCores",
			config: &weka.WekaClusterTemplate{
				ContainerCapacity: 6000, // ceil(6000/5120)=2
				DriveCores:        1,    // explicit value must be ignored by this function
			},
			expectedOk:    true,
			expectedCores: 2,
		},
		{
			name: "numDrives+driveCapacity mode derives, ignoring explicit driveCores",
			config: &weka.WekaClusterTemplate{
				NumDrives:     4,
				DriveCapacity: 2000, // 8000 GiB, ceil(8000/5120)=2
				DriveCores:    1,    // explicit value must be ignored by this function
			},
			expectedOk:    true,
			expectedCores: 2,
		},
		{
			name: "pure full-drives mode (no driveCapacity) not derivable",
			config: &weka.WekaClusterTemplate{
				NumDrives: 4,
			},
			expectedOk: false,
		},
		{
			name:       "empty config not derivable",
			config:     &weka.WekaClusterTemplate{},
			expectedOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cores, ok := DerivedDriveCores(tt.config)
			if ok != tt.expectedOk {
				t.Fatalf("ok = %v, want %v", ok, tt.expectedOk)
			}
			if ok && cores != tt.expectedCores {
				t.Errorf("cores = %d, want %d", cores, tt.expectedCores)
			}
		})
	}
}

// TestRequiredDriveCoresForTemplate_UnclampedVsDerived pins the one place the two functions differ:
// numDrives+driveCapacity, where DerivedDriveCores caps at numDrives so getDriveCores only ever sees an
// assignable count, while RequiredDriveCoresForTemplate reports the true requirement so admission can
// tell a reachable capacity from an unreachable one.
func TestRequiredDriveCoresForTemplate_UnclampedVsDerived(t *testing.T) {
	prevTlc := globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB
	globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = 5 * 1024 // 5120 GiB/core
	t.Cleanup(func() { globalconfig.Config.ClusterCapacity.TlcCapacityPerCoreGiB = prevTlc })

	// 4 drives × 8000 GiB = 32000 GiB, ceil(32000/5120) = 7 cores — but only 4 drives exist to carry
	// them, so the derived (assignable) value stops at 4.
	config := &weka.WekaClusterTemplate{NumDrives: 4, DriveCapacity: 8000}

	required, ok := RequiredDriveCoresForTemplate(config)
	if !ok || required != 7 {
		t.Errorf("RequiredDriveCoresForTemplate = (%d, %v), want (7, true)", required, ok)
	}
	derived, ok := DerivedDriveCores(config)
	if !ok || derived != 4 {
		t.Errorf("DerivedDriveCores = (%d, %v), want (4, true)", derived, ok)
	}

	// containerCapacity has no drive count to cap against, so the two must agree exactly.
	shared := &weka.WekaClusterTemplate{ContainerCapacity: 30000} // ceil(30000/5120) = 6
	required, _ = RequiredDriveCoresForTemplate(shared)
	derived, _ = DerivedDriveCores(shared)
	if required != 6 || derived != 6 {
		t.Errorf("containerCapacity: required = %d, derived = %d, want 6 and 6", required, derived)
	}
}

// TestGetWekaContainerNumbers_FormClusterFloorDefaults pins the form-cluster floor default (5/5) that
// GetWekaContainerNumbers falls back to for an empty config. This floor must keep firing regardless of
// planner mode (see the doc comment on GetWekaContainerNumbers and IsPlannerManaged) — funcs_clusterization.go
// and funcs_upgrade.go depend on it staying non-zero.
func TestGetWekaContainerNumbers_FormClusterFloorDefaults(t *testing.T) {
	got := GetWekaContainerNumbers(&weka.WekaClusterTemplate{})
	want := IntPerWekaRole{Compute: 5, Drive: 5}
	if got.Compute != want.Compute || got.Drive != want.Drive {
		t.Errorf("GetWekaContainerNumbers(empty config) = %+v, want Compute=%d Drive=%d", got, want.Compute, want.Drive)
	}
}

// The numDrives pin must carry through the template unmodified: planner-managed containers size hugepages
// from their own per-node (cores, drives) via DriveHugepagesFromPlan, not from this cluster-wide template.
func TestGetWekaClusterTemplate_AutoFullDrives_PropagatesNumDrives(t *testing.T) {
	config := &weka.WekaClusterTemplate{
		NumDrives:  4,
		DriveCores: 2,
		// ComputeContainers/DriveContainers/ContainerCapacity/DriveCapacity all unset → auto full drives.
	}
	if !config.UsesAutoFullDrives() {
		t.Fatal("test precondition failed: config expected to be auto-full-drives mode")
	}

	template := GetWekaClusterTemplate(config)
	if template.NumDrives != 4 {
		t.Errorf("ClusterTemplate.NumDrives = %d, want 4 (the pin is carried through, not zeroed)", template.NumDrives)
	}
	if template.Cores.Drive != 2 {
		t.Errorf("ClusterTemplate.Cores.Drive = %d, want 2 (explicit driveCores must still be honored)", template.Cores.Drive)
	}

	// Both axes are charged: 1400 per core plus 200 per drive, matching what the pod requests.
	if got, want := CalculateDriveHugepages(template), 1400*2+200*4; got != want {
		t.Errorf("CalculateDriveHugepages = %d, want %d (1400/core + 200/drive)", got, want)
	}

	// DriveHugepagesFromPlan returns a complete total (weka + DPDK); CalculateDriveHugepages returns the
	// pre-DPDK figure GetContainerHugepages later adds DPDK to. Once DPDK is accounted for they must agree.
	// DPDK defaults to 64 MiB/core here (no overrides set).
	cluster := &weka.WekaCluster{Spec: weka.WekaClusterSpec{Dynamic: config}}

	// 2 cores * (1400 drive + 64 dpdk) + 4 drives * 200 = 3728.
	if got, want := DriveHugepagesFromPlan(cluster, 2, 4).Hugepages, 3728; got != want {
		t.Errorf("DriveHugepagesFromPlan(2,4).Hugepages = %d, want %d (CalculateDriveHugepages + DPDK per core)", got, want)
	}
	// 2 cores * 64 dpdk + 4 drives * 200 = 928.
	if got, want := DriveHugepagesFromPlan(cluster, 2, 4).HugepagesOffset, 928; got != want {
		t.Errorf("DriveHugepagesFromPlan(2,4).HugepagesOffset = %d, want %d", got, want)
	}
	// A per-node drive count the cluster-wide template cannot express: 9 drives on 2 cores.
	// 2 cores * (1400 drive + 64 dpdk) + 9 drives * 200 = 4728.
	if got, want := DriveHugepagesFromPlan(cluster, 2, 9).Hugepages, 4728; got != want {
		t.Errorf("DriveHugepagesFromPlan(2,9).Hugepages = %d, want %d", got, want)
	}
}

func TestGetContainerHugepages_Compute(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	tests := []struct {
		name              string
		containerCapacity int
		numDrives         int
		driveCapacity     int
		driveContainers   int
		computeContainers int
		computeCores      int
		presetHugepages   int
		driveTypesRatio   *weka.DriveTypesRatio
		expectedHugepages int
	}{
		{
			name:              "drive sharing large containerCapacity (TLC only)",
			containerCapacity: 5000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=30000GiB, all TLC: 30000*1024/1000=30720MiB cluster, /6=5120 + 1700 + 64*1 (DPDK)
			expectedHugepages: 6884,
		},
		{
			name:              "drive sharing small containerCapacity, clamped to minimum",
			containerCapacity: 500,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=3000GiB, all TLC: 3000*1024/1000=3072MiB cluster, /6=512 + 1700=2212, min=3000 + 64*1 (DPDK)
			expectedHugepages: 3064,
		},
		{
			name:              "drive sharing (numDrives + driveCapacity)",
			numDrives:         4,
			driveCapacity:     2000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=48000GiB, all TLC: 48000*1024/1000=49152MiB cluster, /6=8192 + 1700 + 64*1 (DPDK)
			expectedHugepages: 9956,
		},
		// "no capacity backward compatible" case removed: full-drives mode now blocks until
		// a drive container has allocated drives in the weka-full-drives annotation.
		{
			name:              "multiple cores",
			containerCapacity: 10000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      2,
			// total=60000GiB, all TLC: 60000*1024/1000=61440MiB cluster, /6=10240 + 1700*2=3400 + 64*2 (DPDK)
			expectedHugepages: 13768,
		},
		{
			name:              "explicit override preserved",
			computeCores:      1,
			presetHugepages:   5000,
			expectedHugepages: 5000, // user-set: DPDK not added on top
		},
		{
			name:              "mixed TLC/QLC ratio 1:1",
			containerCapacity: 5000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			driveTypesRatio:   &weka.DriveTypesRatio{Tlc: 1, Qlc: 1},
			// total=30000GiB, tlc=15000, qlc=15000
			// tlcMiB=15000*1024/1000=15360, qlcMiB=15000*1024/6000=2560
			// cluster=17920, /6=2986 + 1700 + 64*1 (DPDK)
			expectedHugepages: 4750,
		},
		{
			name:              "QLC-heavy ratio 1:10",
			containerCapacity: 10000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			driveTypesRatio:   &weka.DriveTypesRatio{Tlc: 1, Qlc: 10},
			// total=60000GiB, tlc=60000/11=5454, qlc=54546
			// tlcMiB=5454*1024/1000=5584, qlcMiB=54546*1024/6000=9309
			// cluster=14893, /6=2482 + 1700 + 64*1 (DPDK)
			expectedHugepages: 4246,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &weka.WekaClusterTemplate{
				ContainerCapacity: tt.containerCapacity,
				NumDrives:         tt.numDrives,
				DriveCapacity:     tt.driveCapacity,
				ComputeCores:      tt.computeCores,
				ComputeHugepages:  tt.presetHugepages,
				DriveTypesRatio:   tt.driveTypesRatio,
			}
			if tt.driveContainers > 0 {
				config.DriveContainers = tt.driveContainers
			}
			if tt.computeContainers > 0 {
				config.ComputeContainers = tt.computeContainers
			}

			cluster := weka.WekaCluster{
				Spec: weka.WekaClusterSpec{
					Dynamic: config,
				},
			}

			template := GetWekaClusterTemplate(config)
			hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, nil, "compute")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if hp.Hugepages != tt.expectedHugepages {
				t.Errorf("expected ComputeHugepages=%d, got %d", tt.expectedHugepages, hp.Hugepages)
			}
		})
	}
}

func makeNode(name string, drives []domain.DriveEntry, labels map[string]string) *v1.Node {
	annotations := map[string]string{}
	if drives != nil {
		b, _ := json.Marshal(drives)
		annotations[consts.AnnotationWekaFullDrives] = string(b)
	}
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func TestGetContainerHugepages_EnrichesFromNodeDrives(t *testing.T) {
	labels := map[string]string{"weka.io/role": "server"}
	drives := []domain.DriveEntry{
		{Serial: "sn1", CapacityGiB: 3000},
		{Serial: "sn2", CapacityGiB: 4000},
	}
	node := makeNode("node1", drives, labels)

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	cluster := weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			NodeSelector: labels,
			Dynamic: &weka.WekaClusterTemplate{
				ComputeCores: 1,
				NumDrives:    2, // 2 drives per container → sn1+sn2 = 7000 GiB per container
				// No ContainerCapacity/DriveCapacity → full-drives mode. ComputeContainers/DriveContainers set
				// explicitly (both-or-neither) make this count-based full-drives, not auto full drives, so
				// this exercises the single-reference-container extrapolation path.
				ComputeContainers: 6,
				DriveContainers:   6,
			},
		},
	}

	// Mock drive container with 2 drives allocated on node1, for ComputeCapacityFromMostRecentDriveContainerAllocation to look up.
	mockDriveContainer := &weka.WekaContainer{
		Spec: weka.WekaContainerSpec{
			Mode: weka.WekaContainerModeDrive,
		},
		Status: weka.WekaContainerStatus{
			NodeAffinity: "node1",
			Allocations: &weka.ContainerAllocations{
				Drives: []string{"sn1", "sn2"},
			},
		},
	}

	template := GetWekaClusterTemplate(cluster.Spec.Dynamic)
	containers := []*weka.WekaContainer{mockDriveContainer}
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, containers, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// perContainerGiB = 3000+4000=7000, driveContainers=6 → totalRaw=42000GiB, all TLC
	// tlcMiB = 42000*1024/1000 = 43008, /6 = 7168 + 1700 + 64*1 (DPDK) = 8932
	if hp.Hugepages != 8932 {
		t.Errorf("expected enriched ComputeHugepages=8932, got %d", hp.Hugepages)
	}
}

func TestGetContainerHugepages_UsesContainerCapacity(t *testing.T) {
	labels := map[string]string{"weka.io/role": "server"}
	drives := []domain.DriveEntry{
		{Serial: "sn1", CapacityGiB: 5000},
	}
	node := makeNode("node1", drives, labels)

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	cluster := weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			NodeSelector: labels,
			Dynamic: &weka.WekaClusterTemplate{
				ComputeCores:      1,
				ContainerCapacity: 2000, // capacity set → no enrichment
			},
		},
	}

	template := GetWekaClusterTemplate(cluster.Spec.Dynamic)
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, nil, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With ContainerCapacity=2000, driveContainers=6, computeContainers=6, all TLC:
	// totalRaw=12000GiB, tlcMiB=12000*1024/1000=12288, /6=2048 + 1700 + 64*1 (DPDK) = 3812
	if hp.Hugepages != 3812 {
		t.Errorf("expected ComputeHugepages=3812 (from spec capacity), got %d", hp.Hugepages)
	}
}

func TestGetContainerHugepages_RespectsUserOverride(t *testing.T) {
	labels := map[string]string{"weka.io/role": "server"}
	drives := []domain.DriveEntry{
		{Serial: "sn1", CapacityGiB: 5000},
	}
	node := makeNode("node1", drives, labels)

	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()

	cluster := weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			NodeSelector: labels,
			Dynamic: &weka.WekaClusterTemplate{
				ComputeCores:     1,
				NumDrives:        1,
				ComputeHugepages: 9999, // user override
			},
		},
	}

	template := GetWekaClusterTemplate(cluster.Spec.Dynamic)
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, nil, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// User override 9999: DPDK not added on top of user-set values
	if hp.Hugepages != 9999 {
		t.Errorf("expected user override ComputeHugepages=9999, got %d", hp.Hugepages)
	}
}

func TestGetContainerHugepages_BlocksWhenNoDriveContainersAllocated(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	cluster := weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			NodeSelector: map[string]string{"weka.io/role": "server"},
			Dynamic: &weka.WekaClusterTemplate{
				ComputeCores: 1,
				NumDrives:    1,
			},
		},
	}

	template := GetWekaClusterTemplate(cluster.Spec.Dynamic)
	// No containers with allocated drives → should return an error (blocks compute container creation)
	_, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, nil, "compute")
	if err == nil {
		t.Fatal("expected error when no drive containers have allocated drives, got nil")
	}
}
