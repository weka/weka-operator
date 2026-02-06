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
	"github.com/weka/weka-operator/pkg/util"
)

func TestBuildDynamicTemplate_ComputeHugepages(t *testing.T) {
	tests := []struct {
		name              string
		containerCapacity int
		numDrives         int
		driveCapacity     int
		driveContainers   int
		computeContainers int
		computeCores      int
		presetHugepages   int
		expectedHugepages int
	}{
		{
			name:              "drive sharing large containerCapacity",
			containerCapacity: 5000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			expectedHugepages: 6700, // 5000*6/6 + 1700*1 = 5000 + 1700
		},
		{
			name:              "drive sharing small containerCapacity, clamped to minimum",
			containerCapacity: 500,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			expectedHugepages: 3000, // 500*6/6 + 1700*1 = 2200, min = 3000
		},
		{
			name:              "drive sharing (numDrives + driveCapacity)",
			numDrives:         4,
			driveCapacity:     2000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			expectedHugepages: 9700, // 6*4*2000/6 + 1700*1 = 8000 + 1700
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
			expectedHugepages: 13400, // 10000*6/6 + 1700*2 = 10000 + 3400
		},
		{
			name:              "explicit override preserved",
			computeCores:      1,
			presetHugepages:   5000,
			expectedHugepages: 5000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &weka.WekaConfig{
				ContainerCapacity: tt.containerCapacity,
				NumDrives:         tt.numDrives,
				DriveCapacity:     tt.driveCapacity,
				ComputeCores:      tt.computeCores,
				ComputeHugepages:  tt.presetHugepages,
			}
			if tt.driveContainers > 0 {
				config.DriveContainers = util.IntRef(tt.driveContainers)
			}
			if tt.computeContainers > 0 {
				config.ComputeContainers = util.IntRef(tt.computeContainers)
			}

			tmpl := BuildDynamicTemplate(config)

			if tmpl.ComputeHugepages != tt.expectedHugepages {
				t.Errorf("expected ComputeHugepages=%d, got %d", tt.expectedHugepages, tmpl.ComputeHugepages)
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

func TestGetEnrichedTemplate_EnrichesFromNodeDrives(t *testing.T) {
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
			Template:     "dynamic",
			NodeSelector: labels,
			Dynamic: &weka.WekaConfig{
				ComputeCores: 1,
				NumDrives:    2, // takes top 2 drives per node → 3000+4000 = 7000 per drive container
				// No ContainerCapacity/DriveCapacity → traditional mode
			},
		},
	}

	tmpl, err := GetEnrichedTemplate(context.Background(), k8sClient, "dynamic", cluster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl == nil {
		t.Fatal("expected template to be found")
	}

	// totalRawCapacity = driveContainers(6) * maxNodeCap(7000) = 42000
	// hugepages = max(42000/6 + 1700*1, 3000*1) = max(7000+1700, 3000) = 8700
	if tmpl.ComputeHugepages != 8700 {
		t.Errorf("expected enriched ComputeHugepages=8700, got %d", tmpl.ComputeHugepages)
	}
}

func TestGetEnrichedTemplate_SkipsEnrichmentWhenContainerCapacitySet(t *testing.T) {
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
			Template:     "dynamic",
			NodeSelector: labels,
			Dynamic: &weka.WekaConfig{
				ComputeCores:      1,
				ContainerCapacity: 2000, // capacity set → no enrichment
			},
		},
	}

	tmpl, err := GetEnrichedTemplate(context.Background(), k8sClient, "dynamic", cluster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl == nil {
		t.Fatal("expected template to be found")
	}

	// With ContainerCapacity=2000, driveContainers=6, computeContainers=6:
	// totalRawCapacity = 6*2000 = 12000
	// hugepages = max(12000/6+1700, 3000) = max(2000+1700, 3000) = 3700
	if tmpl.ComputeHugepages != 3700 {
		t.Errorf("expected ComputeHugepages=3700 (from spec capacity), got %d", tmpl.ComputeHugepages)
	}
}

func TestGetEnrichedTemplate_SkipsEnrichmentWhenUserOverridesHugepages(t *testing.T) {
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
			Template:     "dynamic",
			NodeSelector: labels,
			Dynamic: &weka.WekaConfig{
				ComputeCores:     1,
				NumDrives:        1,
				ComputeHugepages: 9999, // user override
			},
		},
	}

	tmpl, err := GetEnrichedTemplate(context.Background(), k8sClient, "dynamic", cluster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl == nil {
		t.Fatal("expected template to be found")
	}

	if tmpl.ComputeHugepages != 9999 {
		t.Errorf("expected user override ComputeHugepages=9999, got %d", tmpl.ComputeHugepages)
	}
}

func TestGetEnrichedTemplate_StaticTemplateUnchanged(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

	cluster := weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			Template: "small",
		},
	}

	tmpl, err := GetEnrichedTemplate(context.Background(), k8sClient, "small", cluster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl == nil {
		t.Fatal("expected template to be found")
	}

	expected := WekaClusterTemplates["small"]
	if tmpl.ComputeHugepages != expected.ComputeHugepages {
		t.Errorf("expected ComputeHugepages=%d, got %d", expected.ComputeHugepages, tmpl.ComputeHugepages)
	}
}

func TestGetEnrichedTemplate_NoNodesGracefulFallback(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	k8sClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build() // no nodes

	cluster := weka.WekaCluster{
		Spec: weka.WekaClusterSpec{
			Template:     "dynamic",
			NodeSelector: map[string]string{"weka.io/role": "server"},
			Dynamic: &weka.WekaConfig{
				ComputeCores: 1,
				NumDrives:    1,
			},
		},
	}

	tmpl, err := GetEnrichedTemplate(context.Background(), k8sClient, "dynamic", cluster)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl == nil {
		t.Fatal("expected template to be found")
	}

	// No nodes → no enrichment → default minimum
	if tmpl.ComputeHugepages != 3000 {
		t.Errorf("expected fallback ComputeHugepages=3000, got %d", tmpl.ComputeHugepages)
	}
}
