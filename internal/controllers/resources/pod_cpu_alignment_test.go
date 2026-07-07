package resources

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// makePod returns a minimal pod with the mandatory CPU_POLICY env var that
// setResources looks up when resolving auto policy.
func makePod(cpuPolicy weka.CpuPolicy) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "weka",
					Env: []corev1.EnvVar{
						{Name: "CPU_POLICY", Value: string(cpuPolicy)},
					},
				},
			},
		},
	}
}

// makeFactory builds a PodFactory for a compute-mode container with the given NumCores
// and the provided nodeInfo.  Compute mode does add ExtraCores to totalNumCores but also
// subtracts them back inside the DedicatedHT switch case, so with ExtraCores=0 the
// formula stays totalNumCores*2+1.  Crucially, compute mode does NOT override cpuRequestStr
// after the switch block (unlike dist/discovery/ssdproxy/telemetry/adhoc-op), so the
// CPU request reflects the alignment guard result directly.
func makeFactory(numCores int, cpuPolicy weka.CpuPolicy, nodeInfo *discovery.DiscoveryNodeInfo) *PodFactory {
	container := &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-container"},
		Spec: weka.WekaContainerSpec{
			Mode:       weka.WekaContainerModeCompute,
			NumCores:   numCores,
			ExtraCores: 0, // zero so totalNumCores stays == numCores
			CpuPolicy:  cpuPolicy,
			Hugepages:  4000,
		},
	}
	return NewPodFactory(container, nodeInfo, &domain.FeatureFlags{})
}

// minimalHgDetails returns a HugePagesDetails sufficient for setResources to apply
// without panicking (resource.MustParse on empty string would panic).
func minimalHgDetails() HugePagesDetails {
	return GetHugePagesDetails(
		&weka.WekaContainer{
			Spec: weka.WekaContainerSpec{
				Mode:      weka.WekaContainerModeCompute,
				Hugepages: 4000,
			},
		},
		nil,
	)
}

// TestFullPcpusOnlyCPUAlignment verifies the HT rounding guard inside setResources.
//
// DedicatedHT path: totalCores = numCores*2 + 1 (always odd for even numCores).
// When IsHt=true and fullPcpusOnlyEffective()=true the guard bumps odd values up by 1.
func TestFullPcpusOnlyCPUAlignment(t *testing.T) {
	savedFullPcpusOnly := config.Config.FullPcpusOnly
	defer func() { config.Config.FullPcpusOnly = savedFullPcpusOnly }()

	cases := []struct {
		name              string
		numCores          int
		isHt              bool
		forceFullPcpus    bool // config.Config.FullPcpusOnly (operator-wide force)
		nodeFullPcpusOnly bool // f.nodeInfo.NodeFullPcpusOnly (auto-detected on the node)
		wantCPU           string // expected value of pod CPU request/limit
	}{
		{
			name:     "HT, neither forced nor detected → no rounding",
			numCores: 2,
			isHt:     true,
			wantCPU:  "5", // 2*2+1=5, odd but alignment inactive
		},
		{
			name:           "HT, forced operator-wide → round up 5→6",
			numCores:       2,
			isHt:           true,
			forceFullPcpus: true,
			wantCPU:        "6", // 5 is odd, +1 → 6
		},
		{
			name:              "HT, auto-detected on node → round up 5→6",
			numCores:          2,
			isHt:              true,
			nodeFullPcpusOnly: true,
			wantCPU:           "6",
		},
		{
			name:           "not HT, forced → no rounding (guard skipped)",
			numCores:       2,
			isHt:           false,
			forceFullPcpus: true,
			wantCPU:        "5", // IsHt=false → guard condition false
		},
		{
			name:           "numCores=3, HT, forced → round up 7→8",
			numCores:       3,
			isHt:           true,
			forceFullPcpus: true,
			wantCPU:        "8", // 3*2+1=7 (odd) → 8
		},
		{
			name:     "numCores=3, HT, inactive → no rounding, stays 7",
			numCores: 3,
			isHt:     true,
			wantCPU:  "7",
		},
	}

	hgDetails := minimalHgDetails()

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Set global config for this sub-test.
			config.Config.FullPcpusOnly = tc.forceFullPcpus

			nodeInfo := &discovery.DiscoveryNodeInfo{
				IsHt:              tc.isHt,
				NodeFullPcpusOnly: tc.nodeFullPcpusOnly,
			}
			factory := makeFactory(tc.numCores, weka.CpuPolicyDedicatedHT, nodeInfo)
			pod := makePod(weka.CpuPolicyDedicatedHT)

			if err := factory.setResources(context.Background(), pod, hgDetails); err != nil {
				t.Fatalf("setResources returned unexpected error: %v", err)
			}

			gotCPU := pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
			if gotCPU.String() != tc.wantCPU {
				t.Errorf("CPU request = %q, want %q", gotCPU.String(), tc.wantCPU)
			}
		})
	}
}

// TestFullPcpusOnlyEffective tests the fullPcpusOnlyEffective() method directly:
// effective = operator-wide force OR per-node auto-detected value.
func TestFullPcpusOnlyEffective(t *testing.T) {
	savedFullPcpusOnly := config.Config.FullPcpusOnly
	defer func() { config.Config.FullPcpusOnly = savedFullPcpusOnly }()

	cases := []struct {
		name              string
		forceFullPcpus    bool
		nodeFullPcpusOnly bool
		want              bool
	}{
		{"force on, node off → on", true, false, true},
		{"force on, node on → on", true, true, true},
		{"force off, node on → on (auto-detected)", false, true, true},
		{"force off, node off → off", false, false, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			config.Config.FullPcpusOnly = tc.forceFullPcpus

			factory := &PodFactory{
				container: &weka.WekaContainer{
					Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeCompute},
				},
				nodeInfo: &discovery.DiscoveryNodeInfo{
					NodeFullPcpusOnly: tc.nodeFullPcpusOnly,
				},
				featureFlags: &domain.FeatureFlags{},
			}

			got := factory.fullPcpusOnlyEffective()
			if got != tc.want {
				t.Errorf("fullPcpusOnlyEffective() = %v, want %v", got, tc.want)
			}
		})
	}
}
