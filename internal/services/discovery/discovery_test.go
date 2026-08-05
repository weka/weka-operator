package discovery

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInstanceIDAndRegionFromProviderID(t *testing.T) {
	cases := []struct {
		name           string
		providerID     string
		wantInstanceID string
		wantRegion     string
		wantOk         bool
	}{
		{
			name:           "valid aws providerID",
			providerID:     "aws:///eu-west-1a/i-0123456789abcdef0",
			wantInstanceID: "i-0123456789abcdef0",
			wantRegion:     "eu-west-1",
			wantOk:         true,
		},
		{
			name:           "valid aws providerID us region",
			providerID:     "aws:///us-east-1c/i-abc123",
			wantInstanceID: "i-abc123",
			wantRegion:     "us-east-1",
			wantOk:         true,
		},
		{
			name:       "oci providerID is not aws",
			providerID: "ocid1.instance.oc1.iad.aaaaaaaa",
			wantOk:     false,
		},
		{
			name:       "empty providerID",
			providerID: "",
			wantOk:     false,
		},
		{
			name:       "malformed aws providerID missing az segment",
			providerID: "aws:///i-0123456789abcdef0",
			wantOk:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotInstanceID, gotRegion, gotOk := InstanceIDAndRegionFromProviderID(tc.providerID)
			if gotOk != tc.wantOk {
				t.Fatalf("ok = %v, want %v", gotOk, tc.wantOk)
			}
			if !tc.wantOk {
				return
			}
			if gotInstanceID != tc.wantInstanceID {
				t.Errorf("instanceID = %q, want %q", gotInstanceID, tc.wantInstanceID)
			}
			if gotRegion != tc.wantRegion {
				t.Errorf("region = %q, want %q", gotRegion, tc.wantRegion)
			}
		})
	}
}

func TestIsKarpenterManagedNode(t *testing.T) {
	cases := []struct {
		name string
		node *corev1.Node
		want bool
	}{
		{
			name: "karpenter.sh/v1 NodeClaim owner",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "test"},
					},
				},
			},
			want: true,
		},
		{
			name: "karpenter.sh/v1beta1 NodeClaim owner",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{APIVersion: "karpenter.sh/v1beta1", Kind: "NodeClaim", Name: "test"},
					},
				},
			},
			want: true,
		},
		{
			name: "no owner references",
			node: &corev1.Node{},
			want: false,
		},
		{
			name: "other owner kind",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "test"},
					},
				},
			},
			want: false,
		},
		{
			name: "NodeClaim kind in a different group",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{APIVersion: "example.com/v1", Kind: "NodeClaim", Name: "test"},
					},
				},
			},
			want: false,
		},
		{
			name: "nil node",
			node: nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsKarpenterManagedNode(tc.node); got != tc.want {
				t.Errorf("IsKarpenterManagedNode() = %v, want %v", got, tc.want)
			}
		})
	}
}
