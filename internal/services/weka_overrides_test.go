package services

import (
	"context"
	"testing"

	globalconfig "github.com/weka/weka-operator/internal/config"
	wekav1alpha1 "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

type fakeOverrideSvc struct {
	WekaService
	entries []WekaOverride
	added   []string
}

func (f *fakeOverrideSvc) ListOverridesByKey(context.Context, string) ([]WekaOverride, error) {
	return f.entries, nil
}

func (f *fakeOverrideSvc) AddOverride(_ context.Context, key, value, _ string, _ bool) error {
	f.added = append(f.added, key+"="+value)
	return nil
}

func TestCreationOverrides(t *testing.T) {
	saved := globalconfig.Config.DriveSharing
	t.Cleanup(func() { globalconfig.Config.DriveSharing = saved })
	globalconfig.Config.DriveSharing.SmallBigDiskSizesMaxProportionFactor = 100
	globalconfig.Config.DriveSharing.DefaultRedundancyLevel = 1

	sharing := &wekav1alpha1.WekaCluster{Spec: wekav1alpha1.WekaClusterSpec{
		Dynamic: &wekav1alpha1.WekaClusterTemplate{DriveCapacity: 1}, RedundancyLevel: 1}}
	plain := &wekav1alpha1.WekaCluster{Spec: wekav1alpha1.WekaClusterSpec{RedundancyLevel: 2}}
	plain1 := &wekav1alpha1.WekaCluster{Spec: wekav1alpha1.WekaClusterSpec{RedundancyLevel: 1}}
	plainDefault := &wekav1alpha1.WekaCluster{}

	for _, tc := range []struct {
		name         string
		cluster      *wekav1alpha1.WekaCluster
		allowSingleP bool
		want         []string
	}{
		{"drive sharing, single parity allowed", sharing, true, []string{"authenticate_client_join", "allow_1_parity", "small_big_disk_sizes_max_proportion_factor"}},
		{"drive sharing, single parity not allowed", sharing, false, []string{"authenticate_client_join", "small_big_disk_sizes_max_proportion_factor"}},
		{"not drive sharing, rl 1, allowed", plain1, true, []string{"authenticate_client_join", "allow_1_parity"}},
		{"not drive sharing, rl 2, allowed", plain, true, []string{"authenticate_client_join"}},
		{"not drive sharing, rl from default 1, allowed", plainDefault, true, []string{"authenticate_client_join", "allow_1_parity"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			globalconfig.Config.DriveSharing.AllowSingleParity = tc.allowSingleP
			var got []string
			for _, o := range CreationOverrides(tc.cluster) {
				got = append(got, o.Key)
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

func TestEnsureOverride(t *testing.T) {
	for _, tc := range []struct {
		name    string
		val     string
		entries []WekaOverride
		want    int
	}{
		{"missing: added", "v", nil, 1},
		{"tail same and enabled: skipped", "v", []WekaOverride{{Value: "v", Enabled: true}}, 0},
		{"tail disabled: re-added", "v", []WekaOverride{{Value: "v"}}, 1},
		{"tail differs: re-added", "v", []WekaOverride{{Value: "v", Enabled: true}, {Value: "x", Enabled: true}}, 1},
		// weka lists a value-less override (authenticate_client_join) with an empty value.
		{"value-less, present and enabled: skipped", "", []WekaOverride{{Value: "", Enabled: true}}, 0},
		{"value-less, missing: added", "", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeOverrideSvc{entries: tc.entries}
			if err := EnsureOverride(context.Background(), svc, "k", tc.val, "c"); err != nil {
				t.Fatal(err)
			}
			if len(svc.added) != tc.want {
				t.Errorf("added %v, want %d", svc.added, tc.want)
			}
		})
	}
}
