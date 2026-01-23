// This file contains custom error types for resource allocation
package allocator

import "fmt"

// InsufficientDrivesError is returned when there are not enough drives available
type InsufficientDrivesError struct {
	Needed    int
	Available int
}

func (e *InsufficientDrivesError) Error() string {
	return fmt.Sprintf("not enough drives available: need %d, have %d", e.Needed, e.Available)
}

// InsufficientDriveCapacityError is returned when there is not enough drive capacity available
type InsufficientDriveCapacityError struct {
	NeededGiB                  int
	UsableGiB                  int   // Only drives with available >= MinChunkSizeGiB
	AvailableGiB               int   // Total available (for context)
	PhysicalDriveCapacitiesGiB []int // Available capacity on each physical drive
	MaxVirtualDrives           int   // Maximum number of virtual drives allowed
	Type                       string
}

func (e *InsufficientDriveCapacityError) Error() string {
	var s string

	if e.UsableGiB >= e.NeededGiB {
		// Enough total capacity, but fragmented across drives
		s = fmt.Sprintf("Cannot allocate %d GiB: each virtual drive must fit entirely on a single physical drive", e.NeededGiB)
	} else {
		// Truly insufficient capacity
		s = fmt.Sprintf("not enough drive capacity available: need %d GiB, have %d GiB usable (%d GiB total)", e.NeededGiB, e.UsableGiB, e.AvailableGiB)
	}

	if e.Type != "" {
		s += fmt.Sprintf(" for drive type %s", e.Type)
	}

	// Show physical drive capacities
	if len(e.PhysicalDriveCapacitiesGiB) > 0 {
		s += fmt.Sprintf(". Physical drives available: %v GiB", e.PhysicalDriveCapacitiesGiB)
	}

	// Show constraint
	if e.MaxVirtualDrives > 0 {
		s += fmt.Sprintf(". Max virtual drives allowed: %d", e.MaxVirtualDrives)
	}

	return s
}

// PortAllocationError is returned when port allocation fails
type PortAllocationError struct {
	Cause error
}

func (e *PortAllocationError) Error() string {
	return fmt.Sprintf("failed to allocate port ranges: %v", e.Cause)
}

func (e *PortAllocationError) Unwrap() error {
	return e.Cause
}

// InvalidDriveSharingConfigError is returned when drive sharing configuration is invalid
type InvalidDriveSharingConfigError struct {
	Message string
}

func (e *InvalidDriveSharingConfigError) Error() string {
	return e.Message
}
