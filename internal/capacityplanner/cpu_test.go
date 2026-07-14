package capacityplanner

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestCPURequestCores(t *testing.T) {
	ht := NodeCPUTopology{IsHt: true}
	htFull := NodeCPUTopology{IsHt: true, FullPcpusOnly: true}
	nonHt := NodeCPUTopology{IsHt: false}

	cases := []struct {
		name string
		spec weka.WekaContainerSpec
		topo NodeCPUTopology
		want int
	}{
		// auto on HT => dedicated_ht => numCores*2+1
		{"drive auto HT 2 cores", weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 2, CpuPolicy: weka.CpuPolicyAuto}, ht, 5},
		{"compute auto HT 1 core", weka.WekaContainerSpec{Mode: weka.WekaContainerModeCompute, NumCores: 1, CpuPolicy: weka.CpuPolicyAuto}, ht, 3},
		// auto on non-HT => dedicated => numCores+1
		{"drive auto nonHT 2 cores", weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 2, CpuPolicy: weka.CpuPolicyAuto}, nonHt, 3},
		// explicit dedicated_ht / dedicated
		{"drive dedicated_ht 4 cores", weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 4, CpuPolicy: weka.CpuPolicyDedicatedHT}, ht, 9},
		{"drive dedicated 4 cores", weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 4, CpuPolicy: weka.CpuPolicyDedicated}, ht, 5},
		// ExtraCores: dedicated_ht counts it once (numCores*2 + extra + 1); dedicated counts it once (numCores+extra+1)
		{"s3 dedicated_ht 2 cores +1 extra", weka.WekaContainerSpec{Mode: weka.WekaContainerModeS3, NumCores: 2, ExtraCores: 1, CpuPolicy: weka.CpuPolicyDedicatedHT}, ht, 6},
		{"s3 dedicated 2 cores +1 extra", weka.WekaContainerSpec{Mode: weka.WekaContainerModeS3, NumCores: 2, ExtraCores: 1, CpuPolicy: weka.CpuPolicyDedicated}, ht, 4},
		// envoy special-case: request == numCores (no doubling, no +1)
		{"envoy dedicated_ht 3 cores", weka.WekaContainerSpec{Mode: weka.WekaContainerModeEnvoy, NumCores: 3, CpuPolicy: weka.CpuPolicyDedicatedHT}, ht, 3},
		// fullPcpusOnly odd rounding: dedicated 2 cores => 3 => bump to 4
		{"drive dedicated 2 cores fullpcpus odd", weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 2, CpuPolicy: weka.CpuPolicyDedicated}, htFull, 4},
		// dedicated_ht already even stays: 2 cores => 5 (odd) => 6 under fullpcpus
		{"drive dedicated_ht 2 cores fullpcpus", weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: 2, CpuPolicy: weka.CpuPolicyDedicatedHT}, htFull, 6},
		// auto ignores coreIds (mirrors pod.go): on HT resolves to dedicated_ht => numCores*2+1
		{"auto coreIds on HT => dedicated_ht", weka.WekaContainerSpec{Mode: weka.WekaContainerModeCompute, NumCores: 3, CpuPolicy: weka.CpuPolicyAuto, CoreIds: []int{0, 1, 2}}, ht, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CPURequestCores(&tc.spec, tc.topo)
			if got != tc.want {
				t.Fatalf("CPURequestCores(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestCPURequestCores_ManualExplicit(t *testing.T) {
	spec := weka.WekaContainerSpec{
		Mode:      weka.WekaContainerModeCompute,
		NumCores:  2,
		CpuPolicy: weka.CpuPolicyManual,
		Resources: &weka.PodResourcesSpec{
			Requests: weka.PodResources{Cpu: resource.MustParse("2500m")},
			Limits:   weka.PodResources{Cpu: resource.MustParse("3")},
		},
	}
	if got := CPURequestCores(&spec, NodeCPUTopology{IsHt: true}); got != 3 {
		t.Fatalf("manual explicit request 2500m => %d cores, want 3", got)
	}
}

func TestCpuModel_MatchesCPURequestCores(t *testing.T) {
	// For the auto/dedicated/dedicated_ht cases backends use, perCore*dataCores+base must equal
	// CPURequestCores (ExtraCores=0, no fullPcpus rounding).
	for _, topo := range []NodeCPUTopology{{IsHt: true}, {IsHt: false}} {
		perCore, base := cpuModel(weka.CpuPolicyAuto, topo)
		for _, dataCores := range []int{1, 2, 5, 12} {
			want := CPURequestCores(&weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive, NumCores: dataCores, CpuPolicy: weka.CpuPolicyAuto}, topo)
			got := perCore*dataCores + base
			if got != want {
				t.Fatalf("cpuModel(auto,ht=%v): %d*%d+%d=%d, CPURequestCores=%d", topo.IsHt, perCore, dataCores, base, got, want)
			}
		}
	}
}
