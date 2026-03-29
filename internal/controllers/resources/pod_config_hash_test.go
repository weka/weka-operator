package resources

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func baseSpec() *weka.WekaContainerSpec {
	return &weka.WekaContainerSpec{
		Image:     "quay.io/weka/weka-in-container:4.0.0",
		Mode:      weka.WekaContainerModeCompute,
		NumCores:  4,
		Hugepages: 1024,
		Network: weka.Network{
			UdpMode: false,
		},
	}
}

func TestComputePodConfigHash_SameSpecSameHash(t *testing.T) {
	spec := baseSpec()

	hash1, err := ComputePodConfigHash(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash2, err := ComputePodConfigHash(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("same spec produced different hashes: %s vs %s", hash1, hash2)
	}
}

func TestComputePodConfigHash_TrackedFieldChangesHash(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*weka.WekaContainerSpec)
	}{
		{
			name:   "Image change",
			mutate: func(s *weka.WekaContainerSpec) { s.Image = "quay.io/weka/weka-in-container:5.0.0" },
		},
		{
			name:   "Hugepages change",
			mutate: func(s *weka.WekaContainerSpec) { s.Hugepages = 2048 },
		},
		{
			name:   "Network.UdpMode change",
			mutate: func(s *weka.WekaContainerSpec) { s.Network.UdpMode = true },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec1 := baseSpec()
			spec2 := baseSpec()
			tt.mutate(spec2)

			hash1, err := ComputePodConfigHash(spec1)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			hash2, err := ComputePodConfigHash(spec2)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if hash1 == hash2 {
				t.Errorf("expected different hashes after mutation %q, but got the same: %s", tt.name, hash1)
			}
		})
	}
}

func TestComputePodConfigHash_NonTrackedFieldSameHash(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*weka.WekaContainerSpec)
	}{
		{
			name:   "NodeAffinity change",
			mutate: func(s *weka.WekaContainerSpec) { s.NodeAffinity = "different-node" },
		},
		{
			name:   "State change",
			mutate: func(s *weka.WekaContainerSpec) { s.State = weka.ContainerStatePaused },
		},
		{
			name:   "JoinIps change",
			mutate: func(s *weka.WekaContainerSpec) { s.JoinIps = []string{"10.0.0.1:14000"} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec1 := baseSpec()
			spec2 := baseSpec()
			tt.mutate(spec2)

			hash1, err := ComputePodConfigHash(spec1)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			hash2, err := ComputePodConfigHash(spec2)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if hash1 != hash2 {
				t.Errorf("mutation %q should not change hash, but got %s vs %s", tt.name, hash1, hash2)
			}
		})
	}
}

func TestComputePodConfigHash_NilVsEmptyAdditionalSecrets(t *testing.T) {
	spec1 := baseSpec()
	spec1.AdditionalSecrets = nil

	spec2 := baseSpec()
	spec2.AdditionalSecrets = map[string]string{}

	hash1, err := ComputePodConfigHash(spec1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash2, err := ComputePodConfigHash(spec2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("nil and empty AdditionalSecrets should produce the same hash, got %s vs %s", hash1, hash2)
	}
}

func TestComputePodConfigHash_NilVsDefaultTracesConfiguration(t *testing.T) {
	spec1 := baseSpec()
	spec1.TracesConfiguration = nil

	spec2 := baseSpec()
	spec2.TracesConfiguration = weka.GetDefaultTracesConfiguration()

	hash1, err := ComputePodConfigHash(spec1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash2, err := ComputePodConfigHash(spec2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("nil TracesConfiguration and GetDefaultTracesConfiguration() should produce the same hash, got %s vs %s", hash1, hash2)
	}
}

func TestComputePodConfigHash_AffinityAffectsHash(t *testing.T) {
	spec1 := baseSpec()

	spec2 := baseSpec()
	spec2.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/hostname",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"node1"},
							},
						},
					},
				},
			},
		},
	}

	hash1, err := ComputePodConfigHash(spec1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash2, err := ComputePodConfigHash(spec2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 == hash2 {
		t.Errorf("different Affinity should produce different hashes, but got the same: %s", hash1)
	}
}

func TestComputePodConfigHash_TopologySpreadConstraintsAffectsHash(t *testing.T) {
	spec1 := baseSpec()

	spec2 := baseSpec()
	spec2.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       "kubernetes.io/hostname",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "weka"},
			},
		},
	}

	hash1, err := ComputePodConfigHash(spec1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash2, err := ComputePodConfigHash(spec2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash1 == hash2 {
		t.Errorf("different TopologySpreadConstraints should produce different hashes, but got the same: %s", hash1)
	}
}
