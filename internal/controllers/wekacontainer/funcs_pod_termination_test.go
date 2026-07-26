package wekacontainer

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mkNodeProvider(providerID string) *v1.Node {
	return &v1.Node{Spec: v1.NodeSpec{ProviderID: providerID}}
}

func mkDuration(d time.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: d}
}

func TestResolveDeactivationTimeout(t *testing.T) {
	const (
		awsProvider = "aws:///eu-west-1a/i-0abc123def456"
		ociProvider = "ocid1.instance.oc1.eu-frankfurt-1.abc"
		globalNever = time.Duration(0)
	)

	tests := []struct {
		name          string
		node          *v1.Node
		override      *metav1.Duration
		globalDefault time.Duration
		want          time.Duration
	}{
		{"explicit override wins over AWS default", mkNodeProvider(awsProvider), mkDuration(5 * time.Minute), globalNever, 5 * time.Minute},
		{"explicit override 0 (never) wins even on AWS", mkNodeProvider(awsProvider), mkDuration(0), globalNever, 0},
		{"AWS node, no override -> 30m", mkNodeProvider(awsProvider), nil, globalNever, managedNodesPodTerminationTimeout},
		{"AWS default beats a non-zero global default", mkNodeProvider(awsProvider), nil, time.Hour, managedNodesPodTerminationTimeout},
		{"non-cloud node, no override -> global default (never)", mkNodeProvider(""), nil, globalNever, 0},
		{"non-cloud node uses non-zero global default", mkNodeProvider(""), nil, time.Hour, time.Hour},
		{"OCI/OKE node, no override -> 30m", mkNodeProvider(ociProvider), nil, globalNever, managedNodesPodTerminationTimeout},
		{"OCI/OKE default beats a non-zero global default", mkNodeProvider(ociProvider), nil, time.Hour, managedNodesPodTerminationTimeout},
		{"nil node, no override -> global default", nil, nil, 15 * time.Minute, 15 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDeactivationTimeout(tt.node, tt.override, tt.globalDefault)
			if got != tt.want {
				t.Fatalf("resolveDeactivationTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}
