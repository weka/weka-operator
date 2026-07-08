package wekacluster

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nfsWekaContainer() *weka.WekaContainer {
	c := &weka.WekaContainer{}
	c.Spec.Mode = weka.WekaContainerModeNfs
	return c
}

func TestShouldDestroyNfs(t *testing.T) {
	tests := []struct {
		name          string
		allowDestroy  bool
		nfsContainers int
		containers    []*weka.WekaContainer
		nfsConfigured bool
		want          bool
	}{
		{
			name:          "no nfs desired, none present, previously configured -> destroy",
			allowDestroy:  true,
			nfsContainers: 0,
			containers:    nil,
			nfsConfigured: true,
			want:          true,
		},
		{
			name:          "destroy not allowed by override -> keep",
			allowDestroy:  false,
			nfsContainers: 0,
			containers:    nil,
			nfsConfigured: true,
			want:          false,
		},
		{
			name:          "nfs still desired in spec -> keep",
			allowDestroy:  true,
			nfsContainers: 2,
			containers:    nil,
			nfsConfigured: true,
			want:          false,
		},
		{
			name:          "nfs container still present -> keep",
			allowDestroy:  true,
			nfsContainers: 0,
			containers:    []*weka.WekaContainer{nfsWekaContainer()},
			nfsConfigured: true,
			want:          false,
		},
		{
			name:          "nfs never configured -> nothing to destroy",
			allowDestroy:  true,
			nfsContainers: 0,
			containers:    nil,
			nfsConfigured: false,
			want:          false,
		},
		{
			name:          "other-role container present but no nfs -> destroy",
			allowDestroy:  true,
			nfsContainers: 0,
			containers: []*weka.WekaContainer{
				{Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeCompute}},
			},
			nfsConfigured: true,
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{}
			cluster.Spec.Dynamic = &weka.WekaClusterTemplate{NfsContainers: tt.nfsContainers}
			cluster.Spec.Overrides = &weka.WekaClusterSpecOverrides{
				AllowNfsInterfaceGroupDestroy: tt.allowDestroy,
			}
			if tt.nfsConfigured {
				meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
					Type:   condition.ConfNfsConfigured,
					Status: metav1.ConditionTrue,
					Reason: "Configured",
				})
			}

			r := &wekaClusterReconcilerLoop{
				cluster:    cluster,
				containers: tt.containers,
			}

			if got := r.ShouldDestroyNfs(); got != tt.want {
				t.Errorf("ShouldDestroyNfs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShouldConfigureNfsIpRangesAfterDestroy guards the re-enable path: DestroyNfs removes the
// interface group (dropping its floating IP ranges) and invalidates the NfsIpRangesConfigured
// condition. Without that invalidation, ShouldConfigureNfsIpRanges would still match the stale
// hash for the same spec IpRanges and skip reapplying them to the recreated group.
func TestShouldConfigureNfsIpRangesAfterDestroy(t *testing.T) {
	ipRanges := []string{"10.0.0.1-10.0.0.10"}
	cluster := &weka.WekaCluster{}
	cluster.Spec.NFSConfig = &weka.NfsConfig{IpRanges: ipRanges}

	r := &wekaClusterReconcilerLoop{cluster: cluster}

	// Configured with the current spec hash -> no reconfiguration needed.
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:    condition.CondNfsIpRangesConfigured,
		Status:  metav1.ConditionTrue,
		Reason:  "Configured",
		Message: calculateIpRangesHash(ipRanges),
	})
	if r.ShouldConfigureNfsIpRanges() {
		t.Fatal("ShouldConfigureNfsIpRanges() = true with matching hash, want false")
	}

	// DestroyNfs invalidates the condition -> reconfiguration must re-run even for the same spec.
	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:   condition.CondNfsIpRangesConfigured,
		Status: metav1.ConditionFalse,
		Reason: "DestroyNfs",
	})
	if !r.ShouldConfigureNfsIpRanges() {
		t.Error("ShouldConfigureNfsIpRanges() = false after DestroyNfs invalidation, want true")
	}
}
