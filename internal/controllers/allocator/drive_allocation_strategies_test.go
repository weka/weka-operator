package allocator

import (
	"reflect"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// TestAllocationStrategyGenerator_UniformStrategies tests uniform strategy generation with expected outputs
func TestAllocationStrategyGenerator_UniformStrategies(t *testing.T) {
	tests := []struct {
		name               string
		totalCapacity      int
		numCores           int
		expectedDriveSizes []int // Expected drive sizes for the first (best) uniform strategy
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
			expectedDriveSizes: []int{1000, 1000, 1000, 1000}, // 4000 / 4 = 1000
		},
		{
			name:               "Divides evenly by numCores with larger capacity",
			totalCapacity:      12000,
			numCores:           4,
			expectedDriveSizes: []int{3000, 3000, 3000, 3000}, // 12000 / 4 = 3000
		},
		{
			name:               "Divides evenly by numCores+1 (5000/4)",
			totalCapacity:      5000,
			numCores:           3,
			expectedDriveSizes: []int{1250, 1250, 1250, 1250}, // 5000 / 4 = 1250 (tries 3,4,5,... finds 4 first)
		},
		{
			name:               "Larger capacity divisible by numCores",
			totalCapacity:      15000,
			numCores:           5,
			expectedDriveSizes: []int{3000, 3000, 3000, 3000, 3000}, // 15000 / 5 = 3000
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

			generator := NewAllocationStrategyGenerator(tt.totalCapacity, tt.numCores, MinChunkSizeGiB, driveCapacities)

			done := make(chan struct{})
			defer close(done)

			// Get first strategy
			var firstStrategy AllocationStrategy
			for strategy := range generator.GenerateStrategies(done) {
				firstStrategy = strategy
				break // Get only the first one
			}

			// Verify description is uniform
			if firstStrategy.Description != "uniform" {
				t.Errorf("Expected strategy description uniform, got %s", firstStrategy.Description)
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

// TestAllocationStrategyGenerator_NonUniformStrategies tests non-uniform strategy when uniform doesn't work
func TestAllocationStrategyGenerator_NonUniformStrategies(t *testing.T) {
	tests := []struct {
		name                string
		totalCapacity       int
		numCores            int
		driveCapacities     map[string]*physicalDriveCapacity
		expectedDescription string
		expectedDriveSizes  []int
	}{
		{
			name:          "Capacity not divisible evenly - uses non-uniform",
			totalCapacity: 7001, // 7001 / 3 = 2333 remainder 2
			numCores:      3,
			driveCapacities: map[string]*physicalDriveCapacity{
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
			},
			expectedDescription: "non-uniform",
			// baseSize = 7001 / 3 = 2333, remainder = 2
			// 2 drives get 2334, 1 drive gets 2333
			expectedDriveSizes: []int{2334, 2334, 2333},
		},
		{
			name:          "Prime number capacity - non-uniform distribution",
			totalCapacity: 7919, // 7919 / 3 = 2639 remainder 2
			numCores:      3,
			driveCapacities: map[string]*physicalDriveCapacity{
				"drive1": {
					drive:             domain.SharedDriveInfo{PhysicalUUID: "drive1", Serial: "SN001", CapacityGiB: 10000},
					totalCapacity:     10000,
					availableCapacity: 10000,
				},
			},
			expectedDescription: "non-uniform",
			// baseSize = 7919 / 3 = 2639, remainder = 2
			// 2 drives get 2640, 1 drive gets 2639
			expectedDriveSizes: []int{2640, 2640, 2639},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := NewAllocationStrategyGenerator(tt.totalCapacity, tt.numCores, MinChunkSizeGiB, tt.driveCapacities)

			done := make(chan struct{})
			defer close(done)

			// Get first strategy
			var firstStrategy AllocationStrategy
			for strategy := range generator.GenerateStrategies(done) {
				firstStrategy = strategy
				break
			}

			// Verify description
			if firstStrategy.Description != tt.expectedDescription {
				t.Errorf("Expected strategy description %s, got %s", tt.expectedDescription, firstStrategy.Description)
			}

			// Verify exact drive sizes
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
	generator := NewAllocationStrategyGenerator(1000, 3, MinChunkSizeGiB, driveCapacities)

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
	generator := NewAllocationStrategyGenerator(500, 5, MinChunkSizeGiB, driveCapacities)

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

	// Use capacity that divides evenly to get uniform strategies
	generator := NewAllocationStrategyGenerator(6000, 3, MinChunkSizeGiB, driveCapacities)

	done := make(chan struct{})
	defer close(done)

	// Expected strategies in order they should be generated
	expected := []AllocationStrategy{
		// Uniform strategies (tries numCores=3, numCores+1=4, numCores+2=5, numCores+3=6)
		{Description: "uniform", DriveSizes: []int{2000, 2000, 2000}},           // 6000/3
		{Description: "uniform", DriveSizes: []int{1500, 1500, 1500, 1500}},     // 6000/4
		{Description: "uniform", DriveSizes: []int{1200, 1200, 1200, 1200, 1200}}, // 6000/5
		{Description: "uniform", DriveSizes: []int{1000, 1000, 1000, 1000, 1000, 1000}}, // 6000/6
		{Description: "uniform", DriveSizes: []int{750, 750, 750, 750, 750, 750, 750, 750}}, // 6000/8
		// Non-uniform strategies (tries numCores=3, numCores+1=4, numCores+2=5, etc.)
		{Description: "non-uniform", DriveSizes: []int{2000, 2000, 2000}},       // 6000/3 = 2000 remainder 0
		{Description: "non-uniform", DriveSizes: []int{1500, 1500, 1500, 1500}}, // 6000/4 = 1500 remainder 0
		{Description: "non-uniform", DriveSizes: []int{1200, 1200, 1200, 1200, 1200}}, // 6000/5 = 1200 remainder 0
		{Description: "non-uniform", DriveSizes: []int{1000, 1000, 1000, 1000, 1000, 1000}}, // 6000/6 = 1000 remainder 0
		{Description: "non-uniform", DriveSizes: []int{858, 857, 857, 857, 857, 857, 857}}, // 6000/7 = 857 remainder 1 (1 drive gets 858, 6 get 857)
		{Description: "non-uniform", DriveSizes: []int{750, 750, 750, 750, 750, 750, 750, 750}}, // 6000/8 = 750 remainder 0
		{Description: "non-uniform", DriveSizes: []int{667, 667, 667, 667, 667, 667, 666, 666, 666}}, // 6000/9 = 666 remainder 6 (6 drives get 667, 3 get 666)
	}

	generated := []AllocationStrategy{}
	for strategy := range generator.GenerateStrategies(done) {
		generated = append(generated, strategy)
	}

	// Verify count
	if len(generated) != len(expected) {
		t.Errorf("Expected %d strategies, got %d", len(expected), len(generated))
	}

	// Verify each strategy
	for i := 0; i < len(expected) && i < len(generated); i++ {
		exp := expected[i]
		gen := generated[i]

		if gen.Description != exp.Description {
			t.Errorf("Strategy %d: expected description %s, got %s", i, exp.Description, gen.Description)
		}

		if !reflect.DeepEqual(gen.DriveSizes, exp.DriveSizes) {
			t.Errorf("Strategy %d: expected drive sizes %v, got %v", i, exp.DriveSizes, gen.DriveSizes)
		}

		if gen.TotalCapacity() != 6000 {
			t.Errorf("Strategy %d: expected total capacity 6000, got %d", i, gen.TotalCapacity())
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

	generator := NewAllocationStrategyGenerator(10000, 5, MinChunkSizeGiB, driveCapacities)

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
