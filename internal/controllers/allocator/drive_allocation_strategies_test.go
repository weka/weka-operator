package allocator

import (
	"context"
	"reflect"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/pkg/domain"
)

// TestAllocationStrategyGenerator_EvenDistribution tests even distribution strategy generation
func TestAllocationStrategyGenerator_EvenDistribution(t *testing.T) {
	tests := []struct {
		name               string
		totalCapacity      int
		numCores           int
		expectedDriveSizes []int // Expected drive sizes for the first (best) strategy
	}{
		{
			name:               "Divides evenly",
			totalCapacity:      6000,
			numCores:           3,
			expectedDriveSizes: []int{2000, 2000, 2000}, // 6000 / 3 = 2000
		},
		{
			name:               "Remainder 1: first drive gets +1",
			totalCapacity:      4000,
			numCores:           3,
			expectedDriveSizes: []int{1334, 1333, 1333}, // 4000 / 3 = 1333 remainder 1
		},
		{
			name:               "Remainder 2: first two drives get +1",
			totalCapacity:      5000,
			numCores:           3,
			expectedDriveSizes: []int{1667, 1667, 1666}, // 5000 / 3 = 1666 remainder 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test drive capacities with enough space
			driveCapacities := map[string]*physicalDriveCapacity{
				"drive1": {
					drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 20000},
					totalCapacity:     20000,
					availableCapacity: 20000,
				},
				"drive2": {
					drive:             domain.SharedDriveInfo{PhysicalUUID: "drive2", Serial: "SN002", CapacityGiB: 20000},
					totalCapacity:     20000,
					availableCapacity: 20000,
				},
			}

			generator := NewAllocationStrategyGenerator(tt.totalCapacity, tt.numCores, MinChunkSizeGiB, driveCapacities, tt.numCores*8)

			done := make(chan struct{})
			defer close(done)

			// Get first strategy
			var firstStrategy AllocationStrategy
			for strategy := range generator.GenerateStrategies(done) {
				firstStrategy = strategy
				break // Get only the first one
			}

			// Verify drive sizes match expected
			if !reflect.DeepEqual(firstStrategy.DriveSizes, tt.expectedDriveSizes) {
				t.Errorf("Expected drive sizes %v, got %v", tt.expectedDriveSizes, firstStrategy.DriveSizes)
			}

			// Verify total capacity is exact
			if firstStrategy.TotalCapacity() != tt.totalCapacity {
				t.Errorf("Expected total capacity %d, got %d", tt.totalCapacity, firstStrategy.TotalCapacity())
			}

			// Verify numCores requirement
			if firstStrategy.NumDrives() < tt.numCores {
				t.Errorf("Expected at least %d drives, got %d", tt.numCores, firstStrategy.NumDrives())
			}
		})
	}
}

// TestAllocationStrategyGenerator_MinChunkConstraint tests minimum chunk size enforcement
func TestAllocationStrategyGenerator_MinChunkConstraint(t *testing.T) {
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 5000},
			totalCapacity:     5000,
			availableCapacity: 5000,
		},
	}

	// Request capacity that would create drives smaller than minChunkSize
	// minChunkSize = 384 GiB, so if we request 1000 GiB / 3 cores = 333 GiB per drive (too small)
	generator := NewAllocationStrategyGenerator(1000, 3, MinChunkSizeGiB, driveCapacities, 3*8)

	done := make(chan struct{})
	defer close(done)

	strategies := []AllocationStrategy{}
	for strategy := range generator.GenerateStrategies(done) {
		strategies = append(strategies, strategy)
	}

	// Should NOT generate any strategies because baseSize (333) < minChunkSize (384)
	if len(strategies) != 0 {
		t.Fatalf("Expected no strategies (minChunkSize constraint prevents generation), but got %d strategies with sizes: %v",
			len(strategies), strategies[0].DriveSizes)
	}
}

// TestAllocationStrategyGenerator_NumCoresConstraint tests minimum drive count constraint
func TestAllocationStrategyGenerator_NumCoresConstraint(t *testing.T) {
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 10000},
			totalCapacity:     10000,
			availableCapacity: 10000,
		},
	}

	// Request very small capacity with high numCores requirement
	// This will fail because 500 / 5 = 100 GiB per drive, which is < minChunkSize (384 GiB)
	generator := NewAllocationStrategyGenerator(500, 5, MinChunkSizeGiB, driveCapacities, 5*8)

	done := make(chan struct{})
	defer close(done)

	strategies := []AllocationStrategy{}
	for strategy := range generator.GenerateStrategies(done) {
		strategies = append(strategies, strategy)
	}

	// Should NOT generate any strategies because baseSize (100) < minChunkSize (384)
	// This tests that numCores constraint is enforced via minChunkSize
	if len(strategies) != 0 {
		t.Fatalf("Expected no strategies (numCores constraint with minChunkSize prevents generation), but got %d strategies with sizes: %v",
			len(strategies), strategies[0].DriveSizes)
	}
}

// TestAllocationStrategyGenerator_StrategyTypes tests all generated strategies match expected
func TestAllocationStrategyGenerator_StrategyTypes(t *testing.T) {
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 10000},
			totalCapacity:     10000,
			availableCapacity: 10000,
		},
		"drive2": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive2", Serial: "SN002", CapacityGiB: 10000},
			totalCapacity:     10000,
			availableCapacity: 10000,
		},
	}

	// Use capacity for testing even distribution strategies
	// maxDrives=9 to test a reasonable range
	generator := NewAllocationStrategyGenerator(6000, 3, MinChunkSizeGiB, driveCapacities, 9)

	done := make(chan struct{})
	defer close(done)

	// Expected strategies in order (even distribution for numCores=3,4,5,6,7,8,9)
	// Plus fit-to-physical fallback with splitting at the end
	expected := [][]int{
		{2000, 2000, 2000},                            // 6000/3 = 2000
		{1500, 1500, 1500, 1500},                      // 6000/4 = 1500
		{1200, 1200, 1200, 1200, 1200},                // 6000/5 = 1200
		{1000, 1000, 1000, 1000, 1000, 1000},          // 6000/6 = 1000
		{858, 857, 857, 857, 857, 857, 857},           // 6000/7 = 857 remainder 1
		{750, 750, 750, 750, 750, 750, 750, 750},      // 6000/8 = 750
		{667, 667, 667, 667, 667, 667, 666, 666, 666}, // 6000/9 = 666 remainder 6
		{1500, 3000, 1500},                            // fit-to-physical: 6000 from first drive, split to meet numCores=3
	}

	generated := []AllocationStrategy{}
	for strategy := range generator.GenerateStrategies(done) {
		generated = append(generated, strategy)
	}

	// Verify count
	if len(generated) != len(expected) {
		t.Errorf("Expected %d strategies, got %d", len(expected), len(generated))
		for i, s := range generated {
			t.Logf("  Strategy %d: %v", i, s.DriveSizes)
		}
	}

	// Verify each strategy
	for i := 0; i < len(expected) && i < len(generated); i++ {
		if !reflect.DeepEqual(generated[i].DriveSizes, expected[i]) {
			t.Errorf("Strategy %d: expected drive sizes %v, got %v", i, expected[i], generated[i].DriveSizes)
		}

		if generated[i].TotalCapacity() != 6000 {
			t.Errorf("Strategy %d: expected total capacity 6000, got %d", i, generated[i].TotalCapacity())
		}
	}
}

// TestAllocationStrategyGenerator_InsufficientCapacity tests when drives can't provide enough capacity
func TestAllocationStrategyGenerator_InsufficientCapacity(t *testing.T) {
	// Very small drives, large requirement
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 500},
			totalCapacity:     500,
			availableCapacity: 500,
		},
	}

	generator := NewAllocationStrategyGenerator(10000, 5, MinChunkSizeGiB, driveCapacities, 5*8)

	done := make(chan struct{})
	defer close(done)

	strategies := []AllocationStrategy{}
	for strategy := range generator.GenerateStrategies(done) {
		strategies = append(strategies, strategy)
	}

	// Should NOT generate any strategies - insufficient capacity
	// totalAvailable (500) < totalNeeded (10000)
	if len(strategies) != 0 {
		t.Fatalf("Expected no strategies (insufficient capacity), but got %d strategies", len(strategies))
	}
}

// TestAllocationStrategyGenerator_CombinedConstraint tests the combined constraint scenario
// where min drives can be 1 (for TLC in mixed TLC/QLC allocation)
func TestAllocationStrategyGenerator_CombinedConstraint(t *testing.T) {
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 10000},
			totalCapacity:     10000,
			availableCapacity: 10000,
		},
	}

	tests := []struct {
		name              string
		totalCapacity     int
		minDrives         int
		maxDrives         int
		expectedNumDrives int
		expectedDriveSize int
	}{
		{
			name:              "Min=1 with small capacity produces 1 drive",
			totalCapacity:     500,
			minDrives:         1,
			maxDrives:         24,
			expectedNumDrives: 1,
			expectedDriveSize: 500,
		},
		{
			name:              "Min=1 with larger capacity still starts at 1",
			totalCapacity:     1000,
			minDrives:         1,
			maxDrives:         24,
			expectedNumDrives: 1,
			expectedDriveSize: 1000,
		},
		{
			name:              "Min=2 skips 1-drive strategies",
			totalCapacity:     1000,
			minDrives:         2,
			maxDrives:         24,
			expectedNumDrives: 2,
			expectedDriveSize: 500,
		},
		{
			name:              "Min=1 capacity too small for even 1 drive",
			totalCapacity:     300, // < 384 MinChunkSizeGiB
			minDrives:         1,
			maxDrives:         24,
			expectedNumDrives: 0, // No strategies generated
			expectedDriveSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := NewAllocationStrategyGenerator(tt.totalCapacity, tt.minDrives, MinChunkSizeGiB, driveCapacities, tt.maxDrives)

			done := make(chan struct{})
			defer close(done)

			var firstStrategy AllocationStrategy
			count := 0
			for strategy := range generator.GenerateStrategies(done) {
				if count == 0 {
					firstStrategy = strategy
				}
				count++
				if count == 1 {
					break // Only need first strategy
				}
			}

			if tt.expectedNumDrives == 0 {
				// Expect no strategies
				if count > 0 {
					t.Errorf("Expected no strategies, got at least one with sizes %v", firstStrategy.DriveSizes)
				}
				return
			}

			if count == 0 {
				t.Fatalf("Expected at least one strategy, got none")
			}

			if firstStrategy.NumDrives() != tt.expectedNumDrives {
				t.Errorf("Expected %d drives, got %d (sizes: %v)", tt.expectedNumDrives, firstStrategy.NumDrives(), firstStrategy.DriveSizes)
			}

			if tt.expectedNumDrives > 0 && firstStrategy.DriveSizes[0] != tt.expectedDriveSize {
				t.Errorf("Expected drive size %d, got %d", tt.expectedDriveSize, firstStrategy.DriveSizes[0])
			}
		})
	}
}

// TestAllocationStrategyGenerator_AsymmetricTlcQlc simulates the combined constraint scenario
// TLC=500 GiB (can fit 1 drive), QLC needs to compensate
func TestAllocationStrategyGenerator_AsymmetricTlcQlc(t *testing.T) {
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 10000},
			totalCapacity:     10000,
			availableCapacity: 10000,
		},
	}

	// Scenario: numCores=3, TLC capacity=500, QLC capacity=1000
	// With combined constraint, TLC can have 1 drive, QLC needs at least 2
	numCores := 3
	maxDrives := numCores * 8 // 24

	// TLC allocation with min=1
	tlcGenerator := NewAllocationStrategyGenerator(500, 1, MinChunkSizeGiB, driveCapacities, maxDrives)
	done := make(chan struct{})

	var tlcStrategy AllocationStrategy
	for strategy := range tlcGenerator.GenerateStrategies(done) {
		tlcStrategy = strategy
		break
	}
	close(done)

	if tlcStrategy.NumDrives() != 1 {
		t.Fatalf("TLC: Expected 1 drive, got %d", tlcStrategy.NumDrives())
	}

	// QLC allocation with min=max(1, numCores-tlcDrives) = max(1, 3-1) = 2
	tlcDrives := tlcStrategy.NumDrives()
	qlcMin := numCores - tlcDrives
	if qlcMin < 1 {
		qlcMin = 1
	}
	qlcMax := maxDrives - tlcDrives

	qlcGenerator := NewAllocationStrategyGenerator(1000, qlcMin, MinChunkSizeGiB, driveCapacities, qlcMax)
	done2 := make(chan struct{})

	var qlcStrategy AllocationStrategy
	for strategy := range qlcGenerator.GenerateStrategies(done2) {
		qlcStrategy = strategy
		break
	}
	close(done2)

	if qlcStrategy.NumDrives() < qlcMin {
		t.Fatalf("QLC: Expected at least %d drives, got %d", qlcMin, qlcStrategy.NumDrives())
	}

	// Combined total should meet numCores constraint
	totalDrives := tlcDrives + qlcStrategy.NumDrives()
	if totalDrives < numCores {
		t.Errorf("Combined: Expected at least %d drives, got %d (TLC=%d, QLC=%d)",
			numCores, totalDrives, tlcDrives, qlcStrategy.NumDrives())
	}

	t.Logf("Success: TLC=%d drives, QLC=%d drives, Total=%d (min required=%d)",
		tlcDrives, qlcStrategy.NumDrives(), totalDrives, numCores)
}

// TestAllocationStrategyGenerator_FitToPhysicalFallback tests the fit-to-physical fallback
// which is generated AFTER even distribution strategies as a last-resort option
func TestAllocationStrategyGenerator_FitToPhysicalFallback(t *testing.T) {
	t.Run("Heterogeneous drives - fit-to-physical is generated as fallback", func(t *testing.T) {
		// Physical drives: 20000, 500, 500 GB
		// Even distribution [7000, 7000, 7000] will be generated first, but would fail at allocation time
		// Fit-to-physical [20000, 500, 500] is generated as fallback
		driveCapacities := map[string]*physicalDriveCapacity{
			"drive1": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 20000},
				totalCapacity:     20000,
				availableCapacity: 20000,
			},
			"drive2": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive2", Serial: "SN002", CapacityGiB: 500},
				totalCapacity:     500,
				availableCapacity: 500,
			},
			"drive3": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive3", Serial: "SN003", CapacityGiB: 500},
				totalCapacity:     500,
				availableCapacity: 500,
			},
		}

		generator := NewAllocationStrategyGenerator(21000, 3, MinChunkSizeGiB, driveCapacities, 24)

		done := make(chan struct{})
		defer close(done)

		strategies := []AllocationStrategy{}
		for strategy := range generator.GenerateStrategies(done) {
			strategies = append(strategies, strategy)
		}

		if len(strategies) == 0 {
			t.Fatal("Expected at least one strategy, got none")
		}

		// First strategy is even distribution (would fail at allocation time with these physical drives)
		firstStrategy := strategies[0]
		if firstStrategy.DriveSizes[0] == firstStrategy.DriveSizes[1] && firstStrategy.DriveSizes[1] == firstStrategy.DriveSizes[2] {
			// Good - first strategy is even distribution
			t.Logf("First strategy is even distribution: %v", firstStrategy.DriveSizes)
		}

		// Last strategy should be fit-to-physical: [20000, 500, 500]
		lastStrategy := strategies[len(strategies)-1]
		expectedFitToPhysical := []int{20000, 500, 500}
		if !reflect.DeepEqual(lastStrategy.DriveSizes, expectedFitToPhysical) {
			t.Errorf("Last strategy (fit-to-physical) expected %v, got %v", expectedFitToPhysical, lastStrategy.DriveSizes)
		}

		// Verify total capacity
		if lastStrategy.TotalCapacity() != 21000 {
			t.Errorf("Expected total capacity 21000, got %d", lastStrategy.TotalCapacity())
		}
	})

	t.Run("Fit-to-physical not generated when numCores constraint not met", func(t *testing.T) {
		// With numCores=5, fit-to-physical would only produce 3 drives (one per physical)
		// so it won't be generated
		driveCapacities := map[string]*physicalDriveCapacity{
			"drive1": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 20000},
				totalCapacity:     20000,
				availableCapacity: 20000,
			},
			"drive2": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive2", Serial: "SN002", CapacityGiB: 500},
				totalCapacity:     500,
				availableCapacity: 500,
			},
			"drive3": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive3", Serial: "SN003", CapacityGiB: 500},
				totalCapacity:     500,
				availableCapacity: 500,
			},
		}

		generator := NewAllocationStrategyGenerator(21000, 5, MinChunkSizeGiB, driveCapacities, 24)

		done := make(chan struct{})
		defer close(done)

		strategies := []AllocationStrategy{}
		for strategy := range generator.GenerateStrategies(done) {
			strategies = append(strategies, strategy)
		}

		// Even distribution strategies should still be generated (21000/5 = 4200 >= 384 minChunkSize)
		// But fit-to-physical [20000, 500, 500] has only 3 drives, which is < numCores=5
		// So the last strategy should NOT be fit-to-physical
		if len(strategies) > 0 {
			lastStrategy := strategies[len(strategies)-1]
			fitToPhysical := []int{20000, 500, 500}
			if reflect.DeepEqual(lastStrategy.DriveSizes, fitToPhysical) {
				t.Errorf("Fit-to-physical should not be generated when numCores constraint not met, but got %v", lastStrategy.DriveSizes)
			}
		}
	})

	t.Run("Homogeneous drives - even distribution preferred", func(t *testing.T) {
		driveCapacities := map[string]*physicalDriveCapacity{
			"drive1": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 10000},
				totalCapacity:     10000,
				availableCapacity: 10000,
			},
			"drive2": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive2", Serial: "SN002", CapacityGiB: 10000},
				totalCapacity:     10000,
				availableCapacity: 10000,
			},
		}

		generator := NewAllocationStrategyGenerator(6000, 3, MinChunkSizeGiB, driveCapacities, 24)

		done := make(chan struct{})
		defer close(done)

		var firstStrategy AllocationStrategy
		for strategy := range generator.GenerateStrategies(done) {
			firstStrategy = strategy
			break
		}

		// First strategy should be even distribution [2000, 2000, 2000]
		expected := []int{2000, 2000, 2000}
		if !reflect.DeepEqual(firstStrategy.DriveSizes, expected) {
			t.Errorf("First strategy expected %v, got %v", expected, firstStrategy.DriveSizes)
		}
	})

	t.Run("Fit-to-physical splits drives to meet numCores", func(t *testing.T) {
		// Physical drives: 20000, 500, 500 GiB
		// Needed: 21000 GiB, numCores: 4
		// Initial fit-to-physical: [20000, 500, 500] = 3 drives (< numCores)
		// After splitting: [10000, 10000, 500, 500] = 4 drives
		driveCapacities := map[string]*physicalDriveCapacity{
			"drive1": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 20000},
				totalCapacity:     20000,
				availableCapacity: 20000,
			},
			"drive2": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive2", Serial: "SN002", CapacityGiB: 500},
				totalCapacity:     500,
				availableCapacity: 500,
			},
			"drive3": {
				drive:             domain.SharedDriveInfo{PhysicalUUID: "drive3", Serial: "SN003", CapacityGiB: 500},
				totalCapacity:     500,
				availableCapacity: 500,
			},
		}

		generator := NewAllocationStrategyGenerator(21000, 4, MinChunkSizeGiB, driveCapacities, 32)

		done := make(chan struct{})
		defer close(done)

		strategies := []AllocationStrategy{}
		for strategy := range generator.GenerateStrategies(done) {
			strategies = append(strategies, strategy)
		}

		if len(strategies) == 0 {
			t.Fatal("Expected at least one strategy, got none")
		}

		// Last strategy should be fit-to-physical with splitting
		lastStrategy := strategies[len(strategies)-1]

		// Should have 4 drives (split the 20000 into two 10000s)
		if lastStrategy.NumDrives() != 4 {
			t.Errorf("Expected 4 drives after splitting, got %d: %v", lastStrategy.NumDrives(), lastStrategy.DriveSizes)
		}

		// Total capacity should still be 21000
		if lastStrategy.TotalCapacity() != 21000 {
			t.Errorf("Expected total capacity 21000, got %d", lastStrategy.TotalCapacity())
		}

		// Should contain two ~10000 drives and two 500 drives
		// Due to splitting: [10000, 10000, 500, 500] or similar
		t.Logf("Fit-to-physical with splitting: %v", lastStrategy.DriveSizes)
	})
}

// driveSetup describes physical drives to create for testing
type driveSetup struct {
	tlcCount    int
	tlcCapacity int // capacity per TLC drive in GiB
	qlcCount    int
	qlcCapacity int // capacity per QLC drive in GiB
}

// makeDrives creates SharedDriveInfo slice for testing
func makeDrives(setup driveSetup) []domain.SharedDriveInfo {
	drives := make([]domain.SharedDriveInfo, 0, setup.tlcCount+setup.qlcCount)
	for i := 0; i < setup.tlcCount; i++ {
		drives = append(drives, domain.SharedDriveInfo{
			PhysicalUUID: "tlc-" + string(rune('0'+i)),
			Serial:       "TLC-SN-" + string(rune('0'+i)),
			CapacityGiB:  setup.tlcCapacity,
			Type:         "TLC",
		})
	}
	for i := 0; i < setup.qlcCount; i++ {
		drives = append(drives, domain.SharedDriveInfo{
			PhysicalUUID: "qlc-" + string(rune('0'+i)),
			Serial:       "QLC-SN-" + string(rune('0'+i)),
			CapacityGiB:  setup.qlcCapacity,
			Type:         "QLC",
		})
	}
	return drives
}

// TestEnforceMinDrivesPerTypePerCore tests the constraint mode behavior
// When true (default): each drive type must have at least numCores drives
// When false: TLC + QLC combined must be >= numCores
func TestEnforceMinDrivesPerTypePerCore(t *testing.T) {
	tests := []struct {
		name                           string
		enforceMinDrivesPerTypePerCore bool
		drives                         driveSetup
		containerCapacity              int
		tlcRatio                       int
		qlcRatio                       int
		numCores                       int
		expectError                    bool
		errorContains                  string
		// Expected results when successful
		expectedTlcDrives int
		expectedQlcDrives int
	}{
		{
			name:                           "per-type: valid mixed allocation (6 TLC + 6 QLC)",
			enforceMinDrivesPerTypePerCore: true,
			drives:                         driveSetup{tlcCount: 2, tlcCapacity: 10000, qlcCount: 2, qlcCapacity: 10000},
			containerCapacity:              12000,
			tlcRatio:                       1,
			qlcRatio:                       1,
			numCores:                       6,
			expectError:                    false,
			expectedTlcDrives:              6,
			expectedQlcDrives:              6,
		},
		{
			name:                           "per-type: fails when TLC capacity insufficient (1000 GiB < 1920 GiB needed)",
			enforceMinDrivesPerTypePerCore: true,
			drives:                         driveSetup{tlcCount: 1, tlcCapacity: 5000, qlcCount: 1, qlcCapacity: 5000},
			containerCapacity:              5000,
			tlcRatio:                       1,  // TLC = 5000 * 1/5 = 1000 GiB (configured)
			qlcRatio:                       4,  // QLC = 5000 * 4/5 = 4000 GiB (configured)
			numCores:                       5,  // need 5*384=1920 GiB per type → TLC too small
			expectError:                    true,
			errorContains:                  "insufficient TLC capacity",
		},
		{
			name:                           "per-type: fails when QLC capacity insufficient (1000 GiB < 1920 GiB needed)",
			enforceMinDrivesPerTypePerCore: true,
			drives:                         driveSetup{tlcCount: 1, tlcCapacity: 5000, qlcCount: 1, qlcCapacity: 5000},
			containerCapacity:              5000,
			tlcRatio:                       4,  // TLC = 5000 * 4/5 = 4000 GiB (configured)
			qlcRatio:                       1,  // QLC = 5000 * 1/5 = 1000 GiB (configured)
			numCores:                       5,  // need 5*384=1920 GiB per type → QLC too small
			expectError:                    true,
			errorContains:                  "insufficient QLC capacity",
		},
		{
			name:                           "combined: asymmetric 1 TLC + 5 QLC succeeds",
			enforceMinDrivesPerTypePerCore: false,
			drives:                         driveSetup{tlcCount: 1, tlcCapacity: 10000, qlcCount: 2, qlcCapacity: 10000},
			containerCapacity:              3000,
			tlcRatio:                       1,  // TLC = 3000 * 1/6 = 500 GiB → 1 drive
			qlcRatio:                       5,  // QLC = 3000 * 5/6 = 2500 GiB → 5 drives
			numCores:                       6,
			expectError:                    false,
			expectedTlcDrives:              1,
			expectedQlcDrives:              5,
		},
		{
			name:                           "combined: asymmetric 4 TLC + 2 QLC succeeds",
			enforceMinDrivesPerTypePerCore: false,
			drives:                         driveSetup{tlcCount: 2, tlcCapacity: 10000, qlcCount: 1, qlcCapacity: 10000},
			containerCapacity:              3000,
			tlcRatio:                       4,  // TLC = 3000 * 4/6 = 2000 GiB → 4 drives
			qlcRatio:                       2,  // QLC = 3000 * 2/6 = 1000 GiB → 2 drives
			numCores:                       6,
			expectError:                    false,
			expectedTlcDrives:              4,
			expectedQlcDrives:              2,
		},
		{
			name:                           "combined: asymmetric 2 TLC + 1 QLC (ratio 2:1)",
			enforceMinDrivesPerTypePerCore: false,
			drives:                         driveSetup{tlcCount: 2, tlcCapacity: 10000, qlcCount: 1, qlcCapacity: 10000},
			containerCapacity:              1500,
			tlcRatio:                       2,  // TLC = 1500 * 2/3 = 1000 GiB → 2 drives
			qlcRatio:                       1,  // QLC = 1500 * 1/3 = 500 GiB → 1 drive
			numCores:                       3,
			expectError:                    false,
			expectedTlcDrives:              2,
			expectedQlcDrives:              1,
		},
		{
			name:                           "combined: fails when total capacity insufficient (1000 GiB < 1920 GiB needed)",
			enforceMinDrivesPerTypePerCore: false,
			drives:                         driveSetup{tlcCount: 1, tlcCapacity: 10000, qlcCount: 1, qlcCapacity: 10000},
			containerCapacity:              1000,
			tlcRatio:                       1,
			qlcRatio:                       1,
			numCores:                       5,  // need 5*384=1920 GiB total
			expectError:                    true,
			errorContains:                  "insufficient total capacity",
		},
		{
			name:                           "TLC-only: per-type mode",
			enforceMinDrivesPerTypePerCore: true,
			drives:                         driveSetup{tlcCount: 2, tlcCapacity: 10000, qlcCount: 0, qlcCapacity: 0},
			containerCapacity:              3000,
			tlcRatio:                       1,
			qlcRatio:                       0,
			numCores:                       3,
			expectError:                    false,
			expectedTlcDrives:              3,
			expectedQlcDrives:              0,
		},
		{
			name:                           "QLC-only: combined mode",
			enforceMinDrivesPerTypePerCore: false,
			drives:                         driveSetup{tlcCount: 0, tlcCapacity: 0, qlcCount: 2, qlcCapacity: 10000},
			containerCapacity:              3000,
			tlcRatio:                       0,
			qlcRatio:                       1,
			numCores:                       3,
			expectError:                    false,
			expectedTlcDrives:              0,
			expectedQlcDrives:              3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore global config
			origEnforce := globalconfig.Config.DriveSharing.EnforceMinDrivesPerTypePerCore
			origMaxVirtualDrives := globalconfig.Config.DriveSharing.MaxVirtualDrivesPerCore
			defer func() {
				globalconfig.Config.DriveSharing.EnforceMinDrivesPerTypePerCore = origEnforce
				globalconfig.Config.DriveSharing.MaxVirtualDrivesPerCore = origMaxVirtualDrives
			}()
			globalconfig.Config.DriveSharing.EnforceMinDrivesPerTypePerCore = tt.enforceMinDrivesPerTypePerCore
			globalconfig.Config.DriveSharing.MaxVirtualDrivesPerCore = 8 // Default value

			// Create container with spec
			container := &weka.WekaContainer{
				Spec: weka.WekaContainerSpec{
					NumCores:          tt.numCores,
					ContainerCapacity: tt.containerCapacity,
					DriveTypesRatio: &weka.DriveTypesRatio{
						Tlc: tt.tlcRatio,
						Qlc: tt.qlcRatio,
					},
				},
			}

			// Create allocator with nil client (not needed for this test path)
			allocator := NewContainerResourceAllocator(nil)

			// Build allocation request
			req := &AllocationRequest{
				Container:   container,
				CapacityGiB: tt.containerCapacity,
			}

			// Create drives and run allocation
			drives := makeDrives(tt.drives)
			virtualDrives, err := allocator.allocateSharedDrivesByCapacityWithTypes(
				context.Background(),
				req,
				[]weka.WekaContainer{}, // no existing containers
				drives,
			)

			if tt.expectError {
				if err == nil {
					t.Fatalf("Expected error containing %q, but got success", tt.errorContains)
				}
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error containing %q, got: %v", tt.errorContains, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			// Count allocated drives by type
			tlcDrives := 0
			qlcDrives := 0
			totalCapacity := 0
			for _, vd := range virtualDrives {
				totalCapacity += vd.CapacityGiB
				if vd.Type == "TLC" {
					tlcDrives++
				} else if vd.Type == "QLC" {
					qlcDrives++
				}
			}

			// Validate drive counts
			if tlcDrives != tt.expectedTlcDrives {
				t.Errorf("Expected %d TLC drives, got %d", tt.expectedTlcDrives, tlcDrives)
			}
			if qlcDrives != tt.expectedQlcDrives {
				t.Errorf("Expected %d QLC drives, got %d", tt.expectedQlcDrives, qlcDrives)
			}

			// Validate total capacity matches requested containerCapacity
			if totalCapacity != tt.containerCapacity {
				t.Errorf("Expected total capacity %d GiB, got %d GiB", tt.containerCapacity, totalCapacity)
			}

			// Validate total drives meet numCores constraint
			totalDrives := tlcDrives + qlcDrives
			if totalDrives < tt.numCores {
				t.Errorf("Total drives (%d) < numCores (%d)", totalDrives, tt.numCores)
			}

			t.Logf("Allocation succeeded: TLC=%d drives, QLC=%d drives, Total=%d drives, Capacity=%d GiB",
				tlcDrives, qlcDrives, totalDrives, totalCapacity)
		})
	}
}

// TestAllocationStrategyGenerator_MaxDrivesLimit tests when maxDrives limit constrains strategy generation
// In practice, maxDrives = numCores * maxVirtualDrivesPerCore (always a multiple of numCores)
func TestAllocationStrategyGenerator_MaxDrivesLimit(t *testing.T) {
	driveCapacities := map[string]*physicalDriveCapacity{
		"drive1": {
			drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 50000},
			totalCapacity:     50000,
			availableCapacity: 50000,
		},
	}

	tests := []struct {
		name                    string
		totalCapacity           int
		numCores                int
		maxVirtualDrivesPerCore int
		expectedStrategies      [][]int // All expected even distribution strategies in order
	}{
		{
			name:                    "numCores=3, maxVirtualDrivesPerCore=2 (maxDrives=6)",
			totalCapacity:           6000,
			numCores:                3,
			maxVirtualDrivesPerCore: 2,
			expectedStrategies: [][]int{
				{2000, 2000, 2000},                   // 3 drives: 6000/3 = 2000
				{1500, 1500, 1500, 1500},             // 4 drives: 6000/4 = 1500
				{1200, 1200, 1200, 1200, 1200},       // 5 drives: 6000/5 = 1200
				{1000, 1000, 1000, 1000, 1000, 1000}, // 6 drives: 6000/6 = 1000
				// No 7+ drive strategies due to maxDrives=6
			},
		},
		{
			name:                    "numCores=3, maxVirtualDrivesPerCore=1 (maxDrives=3)",
			totalCapacity:           3000,
			numCores:                3,
			maxVirtualDrivesPerCore: 1,
			expectedStrategies: [][]int{
				{1000, 1000, 1000}, // 3 drives only
			},
		},
		{
			name:                    "numCores=4, maxVirtualDrivesPerCore=2 (maxDrives=8)",
			totalCapacity:           8000,
			numCores:                4,
			maxVirtualDrivesPerCore: 2,
			expectedStrategies: [][]int{
				{2000, 2000, 2000, 2000},                         // 4 drives: 8000/4 = 2000
				{1600, 1600, 1600, 1600, 1600},                   // 5 drives: 8000/5 = 1600
				{1334, 1334, 1333, 1333, 1333, 1333},             // 6 drives: 8000/6 = 1333 r2 (2 drives get +1)
				{1143, 1143, 1143, 1143, 1143, 1143, 1142},       // 7 drives: 8000/7 = 1142 r6 (6 drives get +1)
				{1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000}, // 8 drives: 8000/8 = 1000
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxDrives := tt.numCores * tt.maxVirtualDrivesPerCore
			generator := NewAllocationStrategyGenerator(tt.totalCapacity, tt.numCores, MinChunkSizeGiB, driveCapacities, maxDrives)

			done := make(chan struct{})
			defer close(done)

			var strategies []AllocationStrategy
			for strategy := range generator.GenerateStrategies(done) {
				strategies = append(strategies, strategy)
				// Verify no strategy exceeds maxDrives
				if strategy.NumDrives() > maxDrives {
					t.Errorf("Strategy %v has %d drives, exceeds maxDrives=%d",
						strategy.DriveSizes, strategy.NumDrives(), maxDrives)
				}
			}

			if len(strategies) == 0 {
				t.Fatal("Expected at least one strategy, got none")
			}

			// Log all generated strategies
			t.Logf("Generated %d strategies (maxDrives=%d):", len(strategies), maxDrives)
			for i, s := range strategies {
				t.Logf("  Strategy %d: %v", i, s.DriveSizes)
			}

			// Verify even distribution strategies match expected (ignoring fit-to-physical at the end)
			for i, expected := range tt.expectedStrategies {
				if i >= len(strategies) {
					t.Errorf("Missing strategy %d: expected %v", i, expected)
					continue
				}
				if !reflect.DeepEqual(strategies[i].DriveSizes, expected) {
					t.Errorf("Strategy %d: expected %v, got %v", i, expected, strategies[i].DriveSizes)
				}
			}
		})
	}
}
