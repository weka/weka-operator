package resources

import (
	"context"
	"fmt"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// TestClientExtraCores_CPUGrowsMemoryStable is the regression guard for pod.go's client
// memory decision at setResources: client mode now honors ExtraCores for CPU (cpuset
// headroom for weka-aio-*/management threads), but must NOT inflate the per-core weka
// memory reservation, because the runtime is only ever told CORES=NumCores (ExtraCores
// never reaches weka in any mode). Without keying memory on NumCores (not
// NumCores+ExtraCores) this would have silently grown the client memory request by
// perFrontendMemory (3050Mi) per extra core.
func TestClientExtraCores_CPUGrowsMemoryStable(t *testing.T) {
	const numCores = 16
	const extraCores = 4

	buildFactory := func(extra int) *PodFactory {
		container := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-client"},
			Spec: weka.WekaContainerSpec{
				Mode:       weka.WekaContainerModeClient,
				NumCores:   numCores,
				ExtraCores: extra,
				CpuPolicy:  weka.CpuPolicyDedicatedHT,
			},
		}
		nodeInfo := &discovery.DiscoveryNodeInfo{IsHt: true}
		return NewPodFactory(container, nodeInfo, &domain.FeatureFlags{})
	}

	hgDetails := GetHugePagesDetails(&weka.WekaContainer{
		Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient},
	}, nil)

	// Baseline: no ExtraCores. dedicated_ht CPU request = numCores*2+1 = 33.
	baseFactory := buildFactory(0)
	basePod := makePod(weka.CpuPolicyDedicatedHT)
	if err := baseFactory.setResources(context.Background(), basePod, hgDetails); err != nil {
		t.Fatalf("setResources (baseline) returned unexpected error: %v", err)
	}
	baseCPU := basePod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	baseMem := basePod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]

	if wantCPU := "33"; baseCPU.String() != wantCPU {
		t.Fatalf("baseline CPU request = %q, want %q", baseCPU.String(), wantCPU)
	}

	// With ExtraCores=4: CPU request grows to numCores*2+extraCores+1 = 37 (extraCores
	// counted once, not doubled), memory request must stay identical to the baseline.
	extraFactory := buildFactory(extraCores)
	extraPod := makePod(weka.CpuPolicyDedicatedHT)
	if err := extraFactory.setResources(context.Background(), extraPod, hgDetails); err != nil {
		t.Fatalf("setResources (extraCores) returned unexpected error: %v", err)
	}
	extraCPU := extraPod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	extraCPULimit := extraPod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
	extraMem := extraPod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]

	if wantCPU := "37"; extraCPU.String() != wantCPU {
		t.Errorf("CPU request with extraCores=%d = %q, want %q", extraCores, extraCPU.String(), wantCPU)
	}
	if extraCPULimit.String() != extraCPU.String() {
		t.Errorf("CPU limit (%q) should equal CPU request (%q) for dedicated_ht", extraCPULimit.String(), extraCPU.String())
	}
	if extraMem.String() != baseMem.String() {
		t.Errorf("memory request with extraCores=%d = %q, want unchanged from baseline %q",
			extraCores, extraMem.String(), baseMem.String())
	}

	wantMem := fmt.Sprintf("%dMi", 2000+1965+3050*numCores) // buffer + managementMemory + perFrontendMemory*NumCores
	if extraMem.String() != wantMem {
		t.Errorf("memory request with extraCores=%d = %q, want %q", extraCores, extraMem.String(), wantMem)
	}
}
