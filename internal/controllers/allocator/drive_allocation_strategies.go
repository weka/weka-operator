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
	// Description explains the strategy (for logging/debugging)
	Description string
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
		minChunkSizeGiB:           minChunkSizeGiB,
		driveCapacities:           driveCapacities,
		sortedAvailableCapacities: availableCapacities,
	}
}

// GenerateStrategies returns a channel that yields allocation strategies lazily
// The channel is closed when all strategies have been generated OR when done is closed
// Strategies are yielded in order of preference: simpler first, more complex later
// IMPORTANT: Pass a done channel to prevent goroutine leaks if you stop reading early
func (g *AllocationStrategyGenerator) GenerateStrategies(done <-chan struct{}) <-chan AllocationStrategy {
	ch := make(chan AllocationStrategy)

	go func() {
		defer close(ch)

		// Strategy 1: Uniform size strategies (all drives same size)
		if !g.yieldUniformStrategies(ch, done) {
			return // done was closed
		}

		// Strategy 2: Non-uniform strategies (distribute as evenly as possible)
		if !g.yieldNonUniformStrategies(ch, done) {
			return // done was closed
		}
	}()

	return ch
}

// yieldUniformStrategies generates strategies where all drives have the same size
// Tries dividing totalCapacity by numCores, numCores+1, ..., numCores*3
// Only yields strategies that divide evenly and meet minChunkSize constraint
// Returns false if done was closed, true otherwise
func (g *AllocationStrategyGenerator) yieldUniformStrategies(ch chan<- AllocationStrategy, done <-chan struct{}) bool {
	// Check if we have enough physical capacity
	if !g.hasEnoughCapacity() {
		return true // Skip but don't signal done
	}

	// Try dividing capacity by numCores, numCores+1, ..., up to numCores*3
	for numDrives := g.numCores; numDrives <= g.numCores*3; numDrives++ {
		if g.totalCapacityNeeded%numDrives == 0 {
			driveSize := g.totalCapacityNeeded / numDrives

			// Check if drive size meets minimum chunk constraint
			if driveSize >= g.minChunkSizeGiB {
				// Create strategy with all drives of same size
				driveSizes := make([]int, numDrives)
				for i := range driveSizes {
					driveSizes[i] = driveSize
				}

				select {
				case ch <- AllocationStrategy{
					DriveSizes:  driveSizes,
					Description: "uniform",
				}:
					// Successfully sent
				case <-done:
					return false // Consumer stopped reading
				}
			}
		}
	}
	return true
}

// yieldNonUniformStrategies generates strategies that distribute capacity as evenly as possible
// When capacity doesn't divide evenly, some drives get slightly larger sizes
// Returns false if done was closed, true otherwise
func (g *AllocationStrategyGenerator) yieldNonUniformStrategies(ch chan<- AllocationStrategy, done <-chan struct{}) bool {
	// Check if we have enough physical capacity
	if !g.hasEnoughCapacity() {
		return true // Skip but don't signal done
	}

	// Try with numCores, numCores+1, ..., up to numCores*3
	for numDrives := g.numCores; numDrives <= g.numCores*3; numDrives++ {
		driveSizes := g.distributeEvenly(numDrives)
		if driveSizes != nil {
			select {
			case ch <- AllocationStrategy{
				DriveSizes:  driveSizes,
				Description: "non-uniform",
			}:
				// Successfully sent
			case <-done:
				return false // Consumer stopped reading
			}
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
