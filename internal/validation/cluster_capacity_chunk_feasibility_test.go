package validation

import (
	"context"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

func ccCluster(uid string, cap string, sw, rl, hs int, ratio *weka.DriveTypesRatio) *weka.WekaCluster {
	c := &weka.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns", UID: types.UID(uid)},
	}
	c.Spec.StripeWidth, c.Spec.RedundancyLevel, c.Spec.HotSpare = sw, rl, hs
	c.Spec.Dynamic = &weka.WekaClusterTemplate{ClusterCapacity: cap, DriveTypesRatio: ratio}
	return c
}

func fakeClientWithDriveContainer(t *testing.T, uid string, ratio *weka.DriveTypesRatio) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := weka.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme)
	if uid != "" {
		wc := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "drive-0",
				Namespace: "ns",
				Labels: map[string]string{
					domain.WekaLabelClusterId: uid,
					domain.WekaLabelMode:      weka.WekaContainerModeDrive,
				},
			},
		}
		wc.Spec.Mode = weka.WekaContainerModeDrive
		wc.Spec.DriveTypesRatio = ratio
		b = b.WithObjects(wc)
	}
	return b.Build()
}

func TestClusterCapacityChunkFeasibility(t *testing.T) {
	v := &clusterCapacityChunkFeasibility{}
	ctx := context.Background()
	r := func(tlc, qlc int) *weka.DriveTypesRatio { return &weka.DriveTypesRatio{Tlc: tlc, Qlc: qlc} }

	tests := []struct {
		name            string
		cluster         *weka.WekaCluster
		tlcContainerUID string // when set, fake client returns a TLC drive container for this UID
		wantErr         bool
	}{
		{
			// 300TiB, sw=16, 1:290 -> per-FD TLC chunk ≈ 66 GiB < 384 -> infeasible greenfield.
			name:    "infeasible TLC share rejected",
			cluster: ccCluster("u1", "300TiB", 16, 4, 1, r(1, 290)),
			wantErr: true,
		},
		{
			// Mirror image: 290:1 starves the QLC pool below the per-FD floor -> rejected too.
			name:    "infeasible QLC share rejected",
			cluster: ccCluster("u1b", "300TiB", 16, 4, 1, r(290, 1)),
			wantErr: true,
		},
		{
			name:    "feasible greenfield passes",
			cluster: ccCluster("u2", "300TiB", 16, 4, 1, r(1, 10)),
			wantErr: false,
		},
		{
			// Same infeasible numbers, but an existing TLC-bearing container -> migration/established
			// path covers it (planner grows from it) -> greenfield gate skipped.
			name:            "migration with existing container skipped",
			cluster:         ccCluster("u3", "300TiB", 16, 4, 1, r(1, 290)),
			tlcContainerUID: "u3",
			wantErr:         false,
		},
		{
			name:    "below protection floor skipped (reported elsewhere)",
			cluster: ccCluster("u5", "300TiB", 2, 1, 0, r(1, 290)),
			wantErr: false,
		},
		{
			name: "non-clusterCapacity skipped",
			cluster: func() *weka.WekaCluster {
				c := ccCluster("u6", "", 16, 4, 1, r(1, 290))
				c.Spec.Dynamic.ContainerCapacity = 8000
				return c
			}(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fakeClientWithDriveContainer(t, tt.tlcContainerUID, &weka.DriveTypesRatio{Tlc: 1, Qlc: 60})
			errs := v.Validate(ctx, c, tt.cluster)
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected an error, got none")
			}
			if !tt.wantErr && len(errs) != 0 {
				t.Errorf("expected no error, got %v", errs)
			}
		})
	}
}
