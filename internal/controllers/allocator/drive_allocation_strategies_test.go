package allocator

import (
	"reflect"
	"testing"

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
			name:               "Divides evenly by numCores",
			totalCapacity:      6000,
			numCores:           3,
			expectedDriveSizes: []int{2000, 2000, 2000}, // 6000 / 3 = 2000
		},
		{
			name:               "Divides evenly by numCores+1",
			totalCapacity:      4000,
			numCores:           3,
			expectedDriveSizes: []int{1334, 1333, 1333}, // 4000 / 3 = 1333 remainder 1
		},
		{
			name:               "Divides evenly by numCores with larger capacity",
			totalCapacity:      12000,
			numCores:           4,
			expectedDriveSizes: []int{3000, 3000, 3000, 3000}, // 12000 / 4 = 3000
		},
		{
			name:               "Non-divisible capacity (5000/3)",
			totalCapacity:      5000,
			numCores:           3,
			expectedDriveSizes: []int{1667, 1667, 1666}, // 5000 / 3 = 1666 remainder 2
		},
		{
			name:               "Larger capacity divisible by numCores",
			totalCapacity:      15000,
			numCores:           5,
			expectedDriveSizes: []int{3000, 3000, 3000, 3000, 3000}, // 15000 / 5 = 3000
		},
		{
			name:               "Odd capacity with remainder",
			totalCapacity:      7001,
			numCores:           3,
			expectedDriveSizes: []int{2334, 2334, 2333}, // 7001 / 3 = 2333 remainder 2
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
