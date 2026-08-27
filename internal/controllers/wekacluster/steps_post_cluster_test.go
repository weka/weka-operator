package wekacluster

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldCreateDefaultFs(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	cases := []struct {
		name string
		skip *bool
		want bool
	}{
		{name: "unset", skip: nil, want: true},
		{name: "explicit false", skip: boolPtr(false), want: true},
		{name: "explicit true", skip: boolPtr(true), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{}
			cluster.Spec.Overrides = &weka.WekaClusterSpecOverrides{SkipDefaultFilesystemCreation: tc.skip}
			loop := &wekaClusterReconcilerLoop{cluster: cluster}
			if got := loop.ShouldCreateDefaultFs(); got != tc.want {
				t.Fatalf("ShouldCreateDefaultFs() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFilesystemStepsAreWiredIntoReconcileLoop guards the OP-375 failure mode: a field
// read through GetOverrides() never panics and always compiles, so if the step its
// predicate gates is dropped from the steps slice, the override silently does nothing.
// Assert through GetAllSteps(), not by calling the handlers directly.
func TestFilesystemStepsAreWiredIntoReconcileLoop(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	cases := []struct {
		name             string
		skip             *bool
		wantDefaultFsRun bool
	}{
		{name: "override unset", skip: nil, wantDefaultFsRun: true},
		{name: "override false", skip: boolPtr(false), wantDefaultFsRun: true},
		{name: "override true", skip: boolPtr(true), wantDefaultFsRun: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &weka.WekaCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
				Spec: weka.WekaClusterSpec{
					Overrides: &weka.WekaClusterSpecOverrides{
						SkipDefaultFilesystemCreation: tc.skip,
					},
				},
			}

			// Steps capture cluster state as method values at construction time, so the
			// step list has to be rebuilt per scenario.
			steps := (&wekaClusterReconcilerLoop{cluster: cluster}).GetAllSteps()

			configFsStep := findStep(steps, "EnsureConfigFS")
			if configFsStep == nil {
				t.Fatal("EnsureConfigFS is not wired into GetAllSteps - .config_fs would never be created")
			}
			defaultFsStep := findStep(steps, "EnsureDefaultFS")
			if defaultFsStep == nil {
				t.Fatal("EnsureDefaultFS is not wired into GetAllSteps - spec.overrides.skipDefaultFilesystemCreation is a no-op (OP-375)")
			}

			// .config_fs is created regardless of the override.
			if !stepPredicatesPass(configFsStep) {
				t.Error("EnsureConfigFS predicates must pass regardless of the override")
			}
			if got := stepPredicatesPass(defaultFsStep); got != tc.wantDefaultFsRun {
				t.Errorf("EnsureDefaultFS predicates pass = %v, want %v", got, tc.wantDefaultFsRun)
			}
		})
	}
}
