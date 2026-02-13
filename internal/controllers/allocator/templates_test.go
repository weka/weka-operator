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

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

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
			// total=30000GiB, all TLC: 30000*1024/1000=30720MiB cluster, /6=5120 + 1700
			expectedHugepages: 6820,
		},
		{
			name:              "drive sharing small containerCapacity, clamped to minimum",
			containerCapacity: 500,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=3000GiB, all TLC: 3000*1024/1000=3072MiB cluster, /6=512 + 1700=2212, min=3000
			expectedHugepages: 3000,
		},
		{
			name:              "drive sharing (numDrives + driveCapacity)",
			numDrives:         4,
			driveCapacity:     2000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=48000GiB, all TLC: 48000*1024/1000=49152MiB cluster, /6=8192 + 1700
			expectedHugepages: 9892,
		},
		{
			name:              "no capacity backward compatible",
			computeCores:      1,
			expectedHugepages: 3000, // no capacity → min = 3000*1
		},
		{
			name:              "multiple cores",
			containerCapacity: 10000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      2,
			// total=60000GiB, all TLC: 60000*1024/1000=61440MiB cluster, /6=10240 + 1700*2=3400
			expectedHugepages: 13640,
		},
		{
			name:              "explicit override preserved",
			computeCores:      1,
			presetHugepages:   5000,
			expectedHugepages: 5000,
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
			// cluster=17920, /6=2986 + 1700=4686
			expectedHugepages: 4686,
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
			// cluster=14893, /6=2482 + 1700=4182
			expectedHugepages: 4182,
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
			hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, "compute")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if hp == nil {
				t.Fatal("expected hugepages to be computed")
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
		annotations[consts.AnnotationWekaDrives] = string(b)
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
				NumDrives:    2, // takes top 2 drives per node → 3000+4000 = 7000 per drive container
				// No ContainerCapacity/DriveCapacity → traditional mode
			},
		},
	}

	template := GetWekaClusterTemplate(cluster.Spec.Dynamic)
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hp == nil {
		t.Fatal("expected hugepages to be computed")
	}

	// totalRawCapacity = driveContainers(6) * maxNodeCap(7000) = 42000GiB, all TLC
	// tlcMiB = 42000*1024/1000 = 43008, /6 = 7168 + 1700 = 8868
	if hp.Hugepages != 8868 {
		t.Errorf("expected enriched ComputeHugepages=8868, got %d", hp.Hugepages)
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
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hp == nil {
		t.Fatal("expected hugepages to be computed")
	}

	// With ContainerCapacity=2000, driveContainers=6, computeContainers=6, all TLC:
	// totalRaw=12000GiB, tlcMiB=12000*1024/1000=12288, /6=2048 + 1700 = 3748
	if hp.Hugepages != 3748 {
		t.Errorf("expected ComputeHugepages=3748 (from spec capacity), got %d", hp.Hugepages)
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
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hp == nil {
		t.Fatal("expected hugepages to be computed")
	}

	if hp.Hugepages != 9999 {
		t.Errorf("expected user override ComputeHugepages=9999, got %d", hp.Hugepages)
	}
}

func TestGetContainerHugepages_FallbackWhenNoNodes(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build() // no nodes

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
	hp, err := GetContainerHugepages(context.Background(), k8sClient, template, &cluster, "compute")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hp == nil {
		t.Fatal("expected hugepages to be computed")
	}

	// No nodes → no enrichment → default minimum
	if hp.Hugepages != 3000 {
		t.Errorf("expected fallback ComputeHugepages=3000, got %d", hp.Hugepages)
	}
}
