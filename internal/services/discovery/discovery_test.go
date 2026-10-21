package discovery

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
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

func TestSelectRunningContainersByRole(t *testing.T) {
	mk := func(name, mode string, status weka.ContainerStatus) *weka.WekaContainer {
		c := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name}}
		c.Spec.Mode = mode
		c.Status.Status = status
		return c
	}
	s3 := weka.WekaContainerModeS3
	containers := []*weka.WekaContainer{
		mk("s3-stopped", s3, weka.Unhealthy),
		mk("s3-a", s3, weka.Running),
		mk("nfs", weka.WekaContainerModeNfs, weka.Running),
		mk("s3-b", s3, weka.Running),
	}

	cases := []struct {
		name  string
		limit int
		want  []string
	}{
		{"zero limit", 0, nil},
		{"negative limit", -1, nil},
		{"limit one", 1, []string{"s3-a"}},
		{"limit two skips non-running and other roles", 2, []string{"s3-a", "s3-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range SelectRunningContainersByRole(containers, tc.limit, s3) {
				got = append(got, c.Name)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
