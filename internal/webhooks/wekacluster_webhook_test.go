package webhooks

import (
	"strings"
	"testing"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateDriveConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		config      *wekav1alpha1.WekaConfig
		expectError bool
		errorCount  int
		errorField  string
	}{
		{
			name:        "nil config should pass",
			config:      nil,
			expectError: false,
		},
		{
			name:        "empty config should pass",
			config:      &wekav1alpha1.WekaConfig{},
			expectError: false,
		},
		{
			name: "numDrives and containerCapacity are mutually exclusive",
			config: &wekav1alpha1.WekaConfig{
				NumDrives:         10,
				ContainerCapacity: 100,
			},
			expectError: true,
			errorCount:  1,
			errorField:  "spec.dynamicTemplate.numDrives",
		},
		{
			name: "driveCapacity and driveTypesRatio are mutually exclusive",
			config: &wekav1alpha1.WekaConfig{
				DriveCapacity: 100,
				DriveTypesRatio: &wekav1alpha1.DriveTypesRatio{
					Tlc: 1,
					Qlc: 1,
				},
			},
			expectError: true,
			errorCount:  1,
			errorField:  "spec.dynamicTemplate.driveCapacity",
		},
		{
			name: "numDrives must be >= driveCores when using driveCapacity",
			config: &wekav1alpha1.WekaConfig{
				DriveCapacity: 100,
				NumDrives:     2,
				DriveCores:    4,
			},
			expectError: true,
			errorCount:  1,
			errorField:  "spec.dynamicTemplate.numDrives",
		},
		{
			name: "numDrives >= driveCores should pass",
			config: &wekav1alpha1.WekaConfig{
				DriveCapacity: 100,
				NumDrives:     4,
				DriveCores:    4,
			},
			expectError: false,
		},
		{
			name: "driveTypesRatio requires at least one non-zero value",
			config: &wekav1alpha1.WekaConfig{
				ContainerCapacity: 100,
				DriveTypesRatio: &wekav1alpha1.DriveTypesRatio{
					Tlc: 0,
					Qlc: 0,
				},
			},
			expectError: true,
			errorCount:  1,
			errorField:  "spec.dynamicTemplate.driveTypesRatio",
		},
		{
			name: "driveTypesRatio with tlc > 0 should pass",
			config: &wekav1alpha1.WekaConfig{
				ContainerCapacity: 100,
				DriveTypesRatio: &wekav1alpha1.DriveTypesRatio{
					Tlc: 1,
					Qlc: 0,
				},
			},
			expectError: false,
		},
		{
			name: "driveTypesRatio with qlc > 0 should pass",
			config: &wekav1alpha1.WekaConfig{
				ContainerCapacity: 100,
				DriveTypesRatio: &wekav1alpha1.DriveTypesRatio{
					Tlc: 0,
					Qlc: 1,
				},
			},
			expectError: false,
		},
		{
			name: "valid TLC-only mode configuration",
			config: &wekav1alpha1.WekaConfig{
				DriveCapacity: 100,
				NumDrives:     8,
				DriveCores:    4,
			},
			expectError: false,
		},
		{
			name: "valid mixed mode configuration",
			config: &wekav1alpha1.WekaConfig{
				ContainerCapacity: 500,
				DriveTypesRatio: &wekav1alpha1.DriveTypesRatio{
					Tlc: 4,
					Qlc: 1,
				},
			},
			expectError: false,
		},
		{
			name: "negative containerCapacity should fail",
			config: &wekav1alpha1.WekaConfig{
				ContainerCapacity: -100,
			},
			expectError: true,
			errorCount:  1,
			errorField:  "spec.dynamicTemplate.containerCapacity",
		},
		{
			name: "negative driveCapacity should fail",
			config: &wekav1alpha1.WekaConfig{
				DriveCapacity: -100,
			},
			expectError: true,
			errorCount:  1,
			errorField:  "spec.dynamicTemplate.driveCapacity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fldPath := field.NewPath("spec").Child("dynamicTemplate")
			errs := validateDriveConfiguration(tt.config, fldPath)

			if tt.expectError {
				if len(errs) == 0 {
					t.Errorf("expected error but got none")
				}
				if tt.errorCount > 0 && len(errs) != tt.errorCount {
					t.Errorf("expected %d error(s) but got %d: %v", tt.errorCount, len(errs), errs)
				}
				if tt.errorField != "" {
					found := false
					for _, err := range errs {
						if err.Field == tt.errorField {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error on field %s but got errors on fields: %v", tt.errorField, errs)
					}
				}
			} else {
				if len(errs) > 0 {
					t.Errorf("expected no error but got: %v", errs)
				}
			}
		})
	}
}

func TestValidateCapacityWithDriveTypesRatio(t *testing.T) {
	tests := []struct {
		name                           string
		cfg                            *wekav1alpha1.WekaConfig
		enforceMinDrivesPerTypePerCore bool
		expectError                    bool
		errorContains                  string
		errorContains2                 string // optional second error to check (for both TLC and QLC insufficient)
	}{
		{
			name:                           "nil config should pass",
			cfg:                            nil,
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "no containerCapacity should skip validation",
			cfg: &wekav1alpha1.WekaConfig{
				DriveTypesRatio: &wekav1alpha1.DriveTypesRatio{Tlc: 4, Qlc: 1},
				DriveCores:      2,
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "no driveTypesRatio should skip validation",
			cfg: &wekav1alpha1.WekaConfig{
				ContainerCapacity: 1000,
				DriveCores:        2,
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "enforceMinDrivesPerTypePerCore=false should skip validation",
			cfg: &wekav1alpha1.WekaConfig{
				ContainerCapacity: 500, // Too small for 2 cores with 4:1 ratio
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 4, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: false,
			expectError:                    false,
		},
		{
			name: "sufficient capacity for both TLC and QLC should pass",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores, each type needs 2 * 384 = 768 GiB minimum
				// With 1:1 ratio, total needs 768 * 2 = 1536 GiB
				ContainerCapacity: 2000,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "TLC-only ratio should only check TLC capacity",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores, TLC needs 2 * 384 = 768 GiB minimum
				ContainerCapacity: 800,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 0},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "QLC-only ratio should only check QLC capacity",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores, QLC needs 2 * 384 = 768 GiB minimum
				ContainerCapacity: 800,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 0, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "insufficient QLC capacity with 4:1 ratio should fail",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores and 4:1 ratio:
				// TLC gets 1000 * 4/5 = 800 GiB (needs 768) ✓
				// QLC gets 1000 * 1/5 = 200 GiB (needs 768) ✗
				ContainerCapacity: 1000,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 4, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    true,
			errorContains:                  "insufficient QLC capacity",
		},
		{
			name: "insufficient TLC capacity with 1:4 ratio should fail",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores and 1:4 ratio:
				// TLC gets 1000 * 1/5 = 200 GiB (needs 768) ✗
				// QLC gets 1000 * 4/5 = 800 GiB (needs 768) ✓
				ContainerCapacity: 1000,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 4},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    true,
			errorContains:                  "insufficient TLC capacity",
		},
		{
			name: "both TLC and QLC insufficient should report both errors",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores and 1:1 ratio:
				// Each type gets 500 GiB (needs 768) ✗
				ContainerCapacity: 1000,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    true,
			errorContains:                  "insufficient TLC capacity",
			errorContains2:                 "insufficient QLC capacity",
		},
		{
			name: "exact minimum capacity should pass",
			cfg: &wekav1alpha1.WekaConfig{
				// With 2 cores and 1:1 ratio:
				// Each type needs 2 * 384 = 768 GiB
				// Total minimum = 768 * 2 = 1536 GiB
				ContainerCapacity: 1536,
				DriveCores:        2,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "real-world scenario: 4 cores with 4:1 TLC:QLC ratio",
			cfg: &wekav1alpha1.WekaConfig{
				// With 4 cores, each type needs 4 * 384 = 1536 GiB minimum
				// With 4:1 ratio, for QLC to get 1536 GiB:
				// containerCapacity = 1536 * 5 / 1 = 7680 GiB
				ContainerCapacity: 8000,
				DriveCores:        4,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 4, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "no driveCores defaults to 1 and validates",
			cfg: &wekav1alpha1.WekaConfig{
				// With default 1 core, each type needs 1 * 384 = 384 GiB minimum
				// With 1:1 ratio, each type gets 50 GiB (insufficient)
				ContainerCapacity: 100,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    true,
			errorContains:                  "insufficient",
		},
		{
			name: "no driveCores defaults to 1, sufficient capacity passes",
			cfg: &wekav1alpha1.WekaConfig{
				// With default 1 core, each type needs 1 * 384 = 384 GiB minimum
				// With 1:1 ratio and 800 GiB, each type gets 400 GiB (sufficient)
				ContainerCapacity: 800,
				DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 1, Qlc: 1},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global config for the test
			config.Config.DriveSharing.EnforceMinDrivesPerTypePerCore = tt.enforceMinDrivesPerTypePerCore

			fldPath := field.NewPath("spec").Child("dynamicTemplate")
			errs := validateCapacityWithDriveTypesRatio(tt.cfg, fldPath)

			if tt.expectError {
				if len(errs) == 0 {
					t.Errorf("expected error but got none")
				}
				if tt.errorContains != "" {
					found := false
					for _, err := range errs {
						if strings.Contains(err.Detail, tt.errorContains) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error containing %q but got: %v", tt.errorContains, errs)
					}
				}
				if tt.errorContains2 != "" {
					found := false
					for _, err := range errs {
						if strings.Contains(err.Detail, tt.errorContains2) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error containing %q but got: %v", tt.errorContains2, errs)
					}
				}
			} else {
				if len(errs) > 0 {
					t.Errorf("expected no error but got: %v", errs)
				}
			}
		})
	}
}

func TestValidateWekaCluster(t *testing.T) {
	tests := []struct {
		name                           string
		cluster                        *wekav1alpha1.WekaCluster
		enforceMinDrivesPerTypePerCore bool
		expectError                    bool
		errorContains                  string
	}{
		{
			name: "valid cluster with no dynamic config",
			cluster: &wekav1alpha1.WekaCluster{
				Spec: wekav1alpha1.WekaClusterSpec{
					Image: "quay.io/weka.io/weka-in-container:4.2.0",
				},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "valid cluster with TLC-only mode",
			cluster: &wekav1alpha1.WekaCluster{
				Spec: wekav1alpha1.WekaClusterSpec{
					Image: "quay.io/weka.io/weka-in-container:4.2.0",
					Dynamic: &wekav1alpha1.WekaConfig{
						DriveCapacity: 100,
						NumDrives:     8,
						DriveCores:    4,
					},
				},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
		{
			name: "invalid cluster with mutually exclusive fields",
			cluster: &wekav1alpha1.WekaCluster{
				Spec: wekav1alpha1.WekaClusterSpec{
					Image: "quay.io/weka.io/weka-in-container:4.2.0",
					Dynamic: &wekav1alpha1.WekaConfig{
						NumDrives:         10,
						ContainerCapacity: 100,
					},
				},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    true,
		},
		{
			name: "invalid cluster with insufficient QLC capacity",
			cluster: &wekav1alpha1.WekaCluster{
				Spec: wekav1alpha1.WekaClusterSpec{
					Image: "quay.io/weka.io/weka-in-container:4.2.0",
					Dynamic: &wekav1alpha1.WekaConfig{
						ContainerCapacity: 1000, // Too small with 4:1 ratio for 2 cores
						DriveCores:        2,
						DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 4, Qlc: 1},
					},
				},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    true,
			errorContains:                  "insufficient QLC capacity",
		},
		{
			name: "valid cluster with sufficient capacity for mixed mode",
			cluster: &wekav1alpha1.WekaCluster{
				Spec: wekav1alpha1.WekaClusterSpec{
					Image: "quay.io/weka.io/weka-in-container:4.2.0",
					Dynamic: &wekav1alpha1.WekaConfig{
						ContainerCapacity: 8000, // Sufficient for 4 cores with 4:1 ratio
						DriveCores:        4,
						DriveTypesRatio:   &wekav1alpha1.DriveTypesRatio{Tlc: 4, Qlc: 1},
					},
				},
			},
			enforceMinDrivesPerTypePerCore: true,
			expectError:                    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global config for the test
			config.Config.DriveSharing.EnforceMinDrivesPerTypePerCore = tt.enforceMinDrivesPerTypePerCore

			_, err := validateWekaCluster(tt.cluster)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q but got: %v", tt.errorContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("expected no error but got: %v", err)
				}
			}
		})
	}
}

func TestMinChunkSizeGiBConstant(t *testing.T) {
	// Verify we're using the same constant as the allocator
	if allocator.MinChunkSizeGiB != 384 {
		t.Errorf("expected MinChunkSizeGiB to be 384, got %d", allocator.MinChunkSizeGiB)
	}
}
