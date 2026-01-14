package allocator

import (
	"sort"
)

// Minimum chunk size: 128GiB x 3 = 384GiB
const MinChunkSizeGiB = 128 * 3

// AllocationStrategy represents a strategy for allocating virtual drives
// Each strategy specifies how many drives to create and what size each should be
type AllocationStrategy struct {
	// DriveSizes is a list of sizes for each virtual drive to allocate
	// The length of this slice is the number of drives
	DriveSizes []int
}

// TotalCapacity returns the total capacity this strategy would allocate
func (s *AllocationStrategy) TotalCapacity() int {
	total := 0
	for _, size := range s.DriveSizes {
		total += size
	}
	return total
}

// NumDrives returns the number of drives in this strategy
func (s *AllocationStrategy) NumDrives() int {
	return len(s.DriveSizes)
}

// AllocationStrategyGenerator generates different allocation strategies to try
// using a channel-based lazy generation approach (like Python generators)
type AllocationStrategyGenerator struct {
	totalCapacityNeeded int
	numCores            int
	maxDrives           int
	minChunkSizeGiB     int
	driveCapacities     map[string]*physicalDriveCapacity

	// Cached sorted list of available capacities
	sortedAvailableCapacities []int
}

// NewAllocationStrategyGenerator creates a new strategy generator
func NewAllocationStrategyGenerator(
	totalCapacityNeeded int,
	numCores int,
	minChunkSizeGiB int,
	driveCapacities map[string]*physicalDriveCapacity,
	maxDrives int,
) *AllocationStrategyGenerator {
	// Extract and sort available capacities (descending)
	availableCapacities := make([]int, 0, len(driveCapacities))
	for _, dc := range driveCapacities {
		if dc.availableCapacity >= minChunkSizeGiB {
			availableCapacities = append(availableCapacities, dc.availableCapacity)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(availableCapacities)))

	return &AllocationStrategyGenerator{
		totalCapacityNeeded:       totalCapacityNeeded,
		numCores:                  numCores,
		maxDrives:                 maxDrives,
		minChunkSizeGiB:           minChunkSizeGiB,
		driveCapacities:           driveCapacities,
		sortedAvailableCapacities: availableCapacities,
	}
}

// GenerateStrategies returns a channel that yields allocation strategies lazily
// The channel is closed when all strategies have been generated OR when done is closed
// Strategies are yielded in order of preference: even distribution first, fit-to-physical as fallback
// IMPORTANT: Pass a done channel to prevent goroutine leaks if you stop reading early
func (g *AllocationStrategyGenerator) GenerateStrategies(done <-chan struct{}) <-chan AllocationStrategy {
	ch := make(chan AllocationStrategy)

	go func() {
		defer close(ch)

		// Strategy 1: Even distribution (primary - divides capacity as evenly as possible)
		if !g.yieldEvenDistributionStrategies(ch, done) {
			return // done was closed
		}

		// Strategy 2: Fit-to-physical (fallback for heterogeneous drive sizes)
		if !g.yieldFitToPhysicalStrategies(ch, done) {
			return // done was closed
		}
	}()

	return ch
}

// yieldEvenDistributionStrategies generates strategies that distribute capacity as evenly as possible
// Tries with numCores, numCores+1, ..., up to maxDrives drives
// Returns false if done was closed, true otherwise
func (g *AllocationStrategyGenerator) yieldEvenDistributionStrategies(ch chan<- AllocationStrategy, done <-chan struct{}) bool {
	// Check if we have enough physical capacity
	if !g.hasEnoughCapacity() {
		return true // Skip but don't signal done
	}

	// Try with numCores, numCores+1, ..., up to maxDrives
	for numDrives := g.numCores; numDrives <= g.maxDrives; numDrives++ {
		driveSizes := g.distributeEvenly(numDrives)
		if driveSizes != nil {
			select {
			case ch <- AllocationStrategy{
				DriveSizes: driveSizes,
			}:
				// Successfully sent
			case <-done:
				return false // Consumer stopped reading
			}
		} else {
			// Base size is too small, all subsequent iterations will be even smaller
			break
		}
	}
	return true
}

// yieldFitToPhysicalStrategies creates virtual drives matching physical drive capacities
// Used as fallback when even distribution fails due to heterogeneous drive sizes
// Example: Physical drives [20000, 500, 500] with 21000 needed → creates [20000, 500, 500]
// If not enough drives to meet numCores, splits the largest drives
// Example: [20000, 500, 500] with 4 cores → splits to [10000, 10000, 500, 500]
// Returns false if done was closed, true otherwise
func (g *AllocationStrategyGenerator) yieldFitToPhysicalStrategies(ch chan<- AllocationStrategy, done <-chan struct{}) bool {
	if !g.hasEnoughCapacity() {
		return true
	}

	// Step 1: Create initial allocation matching physical drive capacities
	// sortedAvailableCapacities is already sorted descending
	var driveSizes []int
	capacitySoFar := 0

	for _, availCap := range g.sortedAvailableCapacities {
		if availCap < g.minChunkSizeGiB {
			continue // Skip drives below minimum
		}

		// Allocate up to what we need from this drive
		allocate := availCap
		remaining := g.totalCapacityNeeded - capacitySoFar
		if allocate > remaining {
			allocate = remaining
		}

		if allocate >= g.minChunkSizeGiB {
			driveSizes = append(driveSizes, allocate)
			capacitySoFar += allocate
		}

		if capacitySoFar >= g.totalCapacityNeeded {
			break
		}
	}

	// Not enough total capacity
	if capacitySoFar < g.totalCapacityNeeded {
		return true
	}

	// Step 2: Split largest drives until we meet numCores constraint
	for len(driveSizes) < g.numCores && len(driveSizes) < g.maxDrives {
		// Find largest drive that can be split (must be >= 2*minChunkSize)
		maxIdx := -1
		maxSize := 0
		for i, size := range driveSizes {
			if size > maxSize && size >= 2*g.minChunkSizeGiB {
				maxIdx = i
				maxSize = size
			}
		}

		if maxIdx == -1 {
			break // No drive can be split further
		}

		// Split the largest drive into two
		size1 := driveSizes[maxIdx] / 2
		size2 := driveSizes[maxIdx] - size1
		driveSizes[maxIdx] = size1
		driveSizes = append(driveSizes, size2)
	}

	// Check final constraints: enough drives and within limits
	if len(driveSizes) >= g.numCores && len(driveSizes) <= g.maxDrives {
		select {
		case ch <- AllocationStrategy{DriveSizes: driveSizes}:
		case <-done:
			return false
		}
	}

	return true
}

// hasEnoughCapacity checks if total available physical capacity meets requirement
func (g *AllocationStrategyGenerator) hasEnoughCapacity() bool {
	totalAvailable := 0
	for _, dc := range g.driveCapacities {
		totalAvailable += dc.availableCapacity
	}
	return totalAvailable >= g.totalCapacityNeeded
}

// distributeEvenly distributes totalCapacity as evenly as possible across numDrives drives
// Returns nil if constraints cannot be met
func (g *AllocationStrategyGenerator) distributeEvenly(numDrives int) []int {
	if numDrives < g.numCores {
		return nil
	}

	// Calculate base size and remainder
	baseSize := g.totalCapacityNeeded / numDrives
	remainder := g.totalCapacityNeeded % numDrives

	// Check if base size meets minimum chunk constraint
	if baseSize < g.minChunkSizeGiB {
		return nil
	}

	// Create drive sizes: remainder drives get baseSize+1, rest get baseSize
	driveSizes := make([]int, numDrives)
	for i := 0; i < remainder; i++ {
		driveSizes[i] = baseSize + 1
	}
	for i := remainder; i < numDrives; i++ {
		driveSizes[i] = baseSize
	}

	return driveSizes
}
