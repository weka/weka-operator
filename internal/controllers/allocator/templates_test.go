package allocator

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/pkg/util"
)

func TestBuildDynamicTemplate_ComputeHugepages(t *testing.T) {
	tests := []struct {
		name              string
		containerCapacity int
		numDrives         int
		driveCapacity     int
		driveContainers   int
		computeContainers int
		computeCores      int
		presetHugepages   int
		expectedHugepages int
	}{
		{
			name:              "drive sharing large containerCapacity",
			containerCapacity: 5000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			expectedHugepages: 6700, // 5000*6/6 + 1700*1 = 5000 + 1700
		},
		{
			name:              "drive sharing small containerCapacity, clamped to minimum",
			containerCapacity: 500,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			expectedHugepages: 3000, // 500*6/6 + 1700*1 = 2200, min = 3000
		},
		{
			name:              "drive sharing (numDrives + driveCapacity)",
			numDrives:         4,
			driveCapacity:     2000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			expectedHugepages: 9700, // 6*4*2000/6 + 1700*1 = 8000 + 1700
		},
		{
			name:              "no capacity backward compatible",
			computeCores:      1,
			expectedHugepages: 3000, // no capacity → min = 3000*1
		},
		{
			name:              "multiple cores",
			containerCapacity: 10000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      2,
			expectedHugepages: 13400, // 10000*6/6 + 1700*2 = 10000 + 3400
		},
		{
			name:              "explicit override preserved",
			computeCores:      1,
			presetHugepages:   5000,
			expectedHugepages: 5000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &weka.WekaConfig{
				ContainerCapacity: tt.containerCapacity,
				NumDrives:         tt.numDrives,
				DriveCapacity:     tt.driveCapacity,
				ComputeCores:      tt.computeCores,
				ComputeHugepages:  tt.presetHugepages,
			}
			if tt.driveContainers > 0 {
				config.DriveContainers = util.IntRef(tt.driveContainers)
			}
			if tt.computeContainers > 0 {
				config.ComputeContainers = util.IntRef(tt.computeContainers)
			}

			tmpl := BuildDynamicTemplate(config)

			if tmpl.ComputeHugepages != tt.expectedHugepages {
				t.Errorf("expected ComputeHugepages=%d, got %d", tt.expectedHugepages, tmpl.ComputeHugepages)
			}
		})
	}
}
