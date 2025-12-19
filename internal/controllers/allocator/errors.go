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
	NeededGiB    int
	AvailableGiB int
}

func (e *InsufficientDriveCapacityError) Error() string {
	return fmt.Sprintf("not enough drive capacity available: need %d GiB, have %d GiB", e.NeededGiB, e.AvailableGiB)
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
