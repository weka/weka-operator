package allocator

import (
	"context"
	"testing"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

func TestComputeCapacityBasedHugepages_MaxCap(t *testing.T) {
	// Use known scenario: containerCapacity=5000, driveContainers=6, computeContainers=6, computeCores=1
	// total=30000GiB, all TLC: 30000*1024/1000=30720 cluster MiB, /6=5120 + 1700 = 6820
	totalRawCapacityGiB := 5000 * 6 // 30000
	computeContainers := 6
	computeCores := 1

	tests := []struct {
		name     string
		maxCap   int
		expected int
	}{
		{
			name:     "no cap (0)",
			maxCap:   0,
			expected: 6820, // uncapped result
		},
		{
			name:     "cap above result",
			maxCap:   500000,
			expected: 6820, // cap not applied
		},
		{
			name:     "cap below result, even",
			maxCap:   5000,
			expected: 5000, // clamped, already even
		},
		{
			name:     "cap exactly equals result",
			maxCap:   6820,
			expected: 6820, // unchanged
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origMax := globalconfig.Config.ComputeMaxHugepagesMiB
			defer func() { globalconfig.Config.ComputeMaxHugepagesMiB = origMax }()
			globalconfig.Config.ComputeMaxHugepagesMiB = tt.maxCap

			got := ComputeCapacityBasedHugepages(
				context.Background(), totalRawCapacityGiB, computeContainers, computeCores, nil,
			)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func TestComputeCapacityBasedHugepages_DividesByComputeContainers(t *testing.T) {
	// The capacity-share part of compute hugepages must be divided by the actual compute
	// container count (planner-derived), not the min-default of 5. Regression guard for the
	// over-provisioning bug where the divisor fell back to FormClusterMinComputeContainers (=5).
	origTlc := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	origQlc := globalconfig.Config.DriveSharing.HugepagesQlcRatio
	origMax := globalconfig.Config.ComputeMaxHugepagesMiB
	defer func() {
		globalconfig.Config.DriveSharing.HugepagesTlcRatio = origTlc
		globalconfig.Config.DriveSharing.HugepagesQlcRatio = origQlc
		globalconfig.Config.ComputeMaxHugepagesMiB = origMax
	}()
	globalconfig.Config.DriveSharing.HugepagesTlcRatio = 3500
	globalconfig.Config.ComputeMaxHugepagesMiB = 0

	// All-TLC, ~150 TiB raw. clusterMiB = 307200*1024/3500 = 89877; perCore = 1700*10 = 17000.
	totalRawCapacityGiB := 307200
	computeCores := 10

	// derived count (6): 89877/6 = 14979 + 17000 = 31979 -> rounded up to even 31980
	got6 := ComputeCapacityBasedHugepages(context.Background(), totalRawCapacityGiB, 6, computeCores, nil)
	if got6 != 31980 {
		t.Errorf("computeContainers=6: expected 31980, got %d", got6)
	}

	// buggy min-default (5): 89877/5 = 17975 + 17000 = 34975 -> rounded up to even 34976
	got5 := ComputeCapacityBasedHugepages(context.Background(), totalRawCapacityGiB, 5, computeCores, nil)
	if got5 != 34976 {
		t.Errorf("computeContainers=5: expected 34976, got %d", got5)
	}

	// Dividing by the real (larger) count must yield strictly fewer hugepages.
	if got6 >= got5 {
		t.Errorf("expected divisor=6 result (%d) < divisor=5 result (%d)", got6, got5)
	}
}

func TestCalculateDriveHugepages(t *testing.T) {
	tests := []struct {
		name       string
		numDrives  int
		driveCores int
		expected   int
	}{
		{
			name:       "traditional mode (NumDrives > 0)",
			numDrives:  4,
			driveCores: 2,
			expected:   1400*2 + 200*4, // 3600
		},
		{
			name:       "drive-sharing mode (NumDrives == 0)",
			numDrives:  0,
			driveCores: 2,
			expected:   1600 * 2, // 3200
		},
		{
			name:       "traditional, single core, single drive",
			numDrives:  1,
			driveCores: 1,
			expected:   1400 + 200, // 1600
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := ClusterTemplate{
				NumDrives: tt.numDrives,
				Cores:     IntPerWekaRole{Drive: tt.driveCores},
			}
			got := CalculateDriveHugepages(template)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func TestCalculateDriveHugepagesOffset(t *testing.T) {
	tests := []struct {
		name       string
		numDrives  int
		driveCores int
		expected   int
	}{
		{
			name:       "traditional mode (NumDrives > 0)",
			numDrives:  4,
			driveCores: 2,
			expected:   200 * 4, // 800
		},
		{
			name:       "drive-sharing mode (NumDrives == 0)",
			numDrives:  0,
			driveCores: 2,
			expected:   200 * 2, // 400
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := ClusterTemplate{
				NumDrives: tt.numDrives,
				Cores:     IntPerWekaRole{Drive: tt.driveCores},
			}
			got := CalculateDriveHugepagesOffset(template)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}
