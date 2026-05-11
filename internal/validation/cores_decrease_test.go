package validation

import (
	"context"
	"testing"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func ptr(n int) *int { return &n }

// --- cluster ---

func TestClusterCoresDecrease(t *testing.T) {
	v := &clusterCoresDecrease{}

	tests := []struct {
		name    string
		old     *wekav1alpha1.WekaClusterTemplate
		new     *wekav1alpha1.WekaClusterTemplate
		wantErr bool
	}{
		{
			name:    "no change",
			old:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			new:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			wantErr: false,
		},
		{
			name:    "increase is allowed",
			old:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 2},
			new:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			wantErr: false,
		},
		{
			name:    "decrease is denied",
			old:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			new:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 2},
			wantErr: true,
		},
		{
			name:    "set to zero (revert to operator-derived) is denied",
			old:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			new:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 0},
			wantErr: true,
		},
		{
			name:    "first explicit setting (0 -> N) is allowed",
			old:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 0},
			new:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			wantErr: false,
		},
		{
			name:    "nil old dynamic is skipped",
			old:     nil,
			new:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 2},
			wantErr: false,
		},
		{
			name:    "computeCores decrease is denied",
			old:     &wekav1alpha1.WekaClusterTemplate{ComputeCores: 4},
			new:     &wekav1alpha1.WekaClusterTemplate{ComputeCores: 2},
			wantErr: true,
		},
		{
			name:    "dataServicesFeCores decrease is denied (nullable)",
			old:     &wekav1alpha1.WekaClusterTemplate{DataServicesFeCores: ptr(3)},
			new:     &wekav1alpha1.WekaClusterTemplate{DataServicesFeCores: ptr(1)},
			wantErr: true,
		},
		{
			name:    "dataServicesFeCores: unsetting non-nil to nil is denied",
			old:     &wekav1alpha1.WekaClusterTemplate{DataServicesFeCores: ptr(3)},
			new:     &wekav1alpha1.WekaClusterTemplate{DataServicesFeCores: nil},
			wantErr: true,
		},
		{
			name:    "removing dynamic block (non-nil to nil) is denied when old had cores",
			old:     &wekav1alpha1.WekaClusterTemplate{DriveCores: 4},
			new:     nil,
			wantErr: true,
		},
		{
			name: "multiple simultaneous decreases each produce an error",
			old:  &wekav1alpha1.WekaClusterTemplate{DriveCores: 4, ComputeCores: 4},
			new:  &wekav1alpha1.WekaClusterTemplate{DriveCores: 2, ComputeCores: 2},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldCluster := &wekav1alpha1.WekaCluster{}
			newCluster := &wekav1alpha1.WekaCluster{}
			oldCluster.Spec.Dynamic = tt.old
			newCluster.Spec.Dynamic = tt.new

			errs := v.ValidateUpdate(context.Background(), nil, oldCluster, newCluster)
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no error, got: %v", errs)
			}
		})
	}

	t.Run("multiple simultaneous decreases produce one error per field", func(t *testing.T) {
		oldCluster := &wekav1alpha1.WekaCluster{}
		newCluster := &wekav1alpha1.WekaCluster{}
		oldCluster.Spec.Dynamic = &wekav1alpha1.WekaClusterTemplate{DriveCores: 4, ComputeCores: 4, S3Cores: 2}
		newCluster.Spec.Dynamic = &wekav1alpha1.WekaClusterTemplate{DriveCores: 2, ComputeCores: 2, S3Cores: 2}

		errs := v.ValidateUpdate(context.Background(), nil, oldCluster, newCluster)
		if len(errs) != 2 {
			t.Errorf("expected 2 errors (one per decreased field), got %d: %v", len(errs), errs)
		}
	})
}

// --- container ---

func TestContainerCoresDecrease(t *testing.T) {
	v := &containerCoresDecrease{}

	tests := []struct {
		name      string
		oldSpec   wekav1alpha1.WekaContainerSpec
		newSpec   wekav1alpha1.WekaContainerSpec
		wantErr   bool
		wantField string
	}{
		{
			name:    "no change",
			oldSpec: wekav1alpha1.WekaContainerSpec{NumCores: 4},
			newSpec: wekav1alpha1.WekaContainerSpec{NumCores: 4},
		},
		{
			name:    "increase is allowed",
			oldSpec: wekav1alpha1.WekaContainerSpec{NumCores: 2},
			newSpec: wekav1alpha1.WekaContainerSpec{NumCores: 4},
		},
		{
			name:      "numCores decrease is denied",
			oldSpec:   wekav1alpha1.WekaContainerSpec{NumCores: 4},
			newSpec:   wekav1alpha1.WekaContainerSpec{NumCores: 2},
			wantErr:   true,
			wantField: "spec.numCores",
		},
		{
			name:      "numCores revert to zero is denied",
			oldSpec:   wekav1alpha1.WekaContainerSpec{NumCores: 4},
			newSpec:   wekav1alpha1.WekaContainerSpec{NumCores: 0},
			wantErr:   true,
			wantField: "spec.numCores",
		},
		{
			name:      "extraCores decrease is denied",
			oldSpec:   wekav1alpha1.WekaContainerSpec{ExtraCores: 2},
			newSpec:   wekav1alpha1.WekaContainerSpec{ExtraCores: 1},
			wantErr:   true,
			wantField: "spec.extraCores",
		},
		{
			name: "dataServicesFeCores decrease is denied",
			oldSpec: wekav1alpha1.WekaContainerSpec{
				DataServicesConfig: &wekav1alpha1.DataServicesConfig{DataServicesFeCores: 3},
			},
			newSpec: wekav1alpha1.WekaContainerSpec{
				DataServicesConfig: &wekav1alpha1.DataServicesConfig{DataServicesFeCores: 1},
			},
			wantErr:   true,
			wantField: "spec.dataServicesConfig.dataServicesFeCores",
		},
		{
			name: "dataServicesFeCores: old nil skips check",
			oldSpec: wekav1alpha1.WekaContainerSpec{
				DataServicesConfig: nil,
			},
			newSpec: wekav1alpha1.WekaContainerSpec{
				DataServicesConfig: &wekav1alpha1.DataServicesConfig{DataServicesFeCores: 1},
			},
		},
		{
			name: "dataServicesConfig: removing block (non-nil to nil) is denied when old had cores",
			oldSpec: wekav1alpha1.WekaContainerSpec{
				DataServicesConfig: &wekav1alpha1.DataServicesConfig{DataServicesFeCores: 3},
			},
			newSpec:   wekav1alpha1.WekaContainerSpec{DataServicesConfig: nil},
			wantErr:   true,
			wantField: "spec.dataServicesConfig.dataServicesFeCores",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldC := &wekav1alpha1.WekaContainer{}
			newC := &wekav1alpha1.WekaContainer{}
			oldC.Spec = tt.oldSpec
			newC.Spec = tt.newSpec

			errs := v.ValidateUpdate(context.Background(), nil, oldC, newC)
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no error, got: %v", errs)
			}
			if tt.wantField != "" && len(errs) > 0 {
				if got := errs[0].Field; got != tt.wantField {
					t.Errorf("field = %q, want %q", got, tt.wantField)
				}
			}
		})
	}
}

// --- client ---

func TestClientCoresDecrease(t *testing.T) {
	v := &clientCoresDecrease{}

	tests := []struct {
		name    string
		oldN    int
		newN    int
		wantErr bool
	}{
		{"no change", 4, 4, false},
		{"increase is allowed", 2, 4, false},
		{"decrease is denied", 4, 2, true},
		{"revert to zero is denied", 4, 0, true},
		{"first explicit setting is allowed", 0, 4, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldWC := &wekav1alpha1.WekaClient{}
			newWC := &wekav1alpha1.WekaClient{}
			oldWC.Spec.CoresNumber = tt.oldN
			newWC.Spec.CoresNumber = tt.newN

			errs := v.ValidateUpdate(context.Background(), nil, oldWC, newWC)
			if tt.wantErr && len(errs) == 0 {
				t.Errorf("expected error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("expected no error, got: %v", errs)
			}
		})
	}
}
