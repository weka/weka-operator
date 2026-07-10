package allocator

import (
	"testing"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// Test_CapacityConstraintsFromConfig_ZeroFractionsNotCoerced asserts the fix for the PR #2604 review:
// an explicit 0 for MinGrowthFraction / MaxOverProvisionFraction is honored, not coerced back to 0.2.
// It lives in the allocator package because CapacityConstraintsFromConfig is the operator-side
// globalconfig adapter (the pure planner moved to internal/capacityplanner).
func Test_CapacityConstraintsFromConfig_ZeroFractionsNotCoerced(t *testing.T) {
	prevMin := globalconfig.Config.DriveSharing.MinGrowthFraction
	prevMax := globalconfig.Config.DriveSharing.MaxOverProvisionFraction
	t.Cleanup(func() {
		globalconfig.Config.DriveSharing.MinGrowthFraction = prevMin
		globalconfig.Config.DriveSharing.MaxOverProvisionFraction = prevMax
	})

	globalconfig.Config.DriveSharing.MinGrowthFraction = 0
	globalconfig.Config.DriveSharing.MaxOverProvisionFraction = 0
	cons := CapacityConstraintsFromConfig()
	if cons.MinGrowthFraction != 0 {
		t.Errorf("MinGrowthFraction=0 must be honored, got %v", cons.MinGrowthFraction)
	}
	if cons.MaxOverProvisionFraction != 0 {
		t.Errorf("MaxOverProvisionFraction=0 must be honored, got %v", cons.MaxOverProvisionFraction)
	}

	globalconfig.Config.DriveSharing.MinGrowthFraction = 0.35
	globalconfig.Config.DriveSharing.MaxOverProvisionFraction = 0.1
	cons = CapacityConstraintsFromConfig()
	if cons.MinGrowthFraction != 0.35 || cons.MaxOverProvisionFraction != 0.1 {
		t.Errorf("explicit non-zero values must pass through, got min=%v max=%v", cons.MinGrowthFraction, cons.MaxOverProvisionFraction)
	}
}
