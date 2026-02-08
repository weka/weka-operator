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
		driveTypesRatio   *weka.DriveTypesRatio
		expectedHugepages int
	}{
		{
			name:              "drive sharing large containerCapacity (TLC only)",
			containerCapacity: 5000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=30000GiB, all TLC: 30000*1024/1000=30720MiB cluster, /6=5120 + 1700
			expectedHugepages: 6820,
		},
		{
			name:              "drive sharing small containerCapacity, clamped to minimum",
			containerCapacity: 500,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=3000GiB, all TLC: 3000*1024/1000=3072MiB cluster, /6=512 + 1700=2212, min=3000
			expectedHugepages: 3000,
		},
		{
			name:              "drive sharing (numDrives + driveCapacity)",
			numDrives:         4,
			driveCapacity:     2000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			// total=48000GiB, all TLC: 48000*1024/1000=49152MiB cluster, /6=8192 + 1700
			expectedHugepages: 9892,
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
			// total=60000GiB, all TLC: 60000*1024/1000=61440MiB cluster, /6=10240 + 1700*2=3400
			expectedHugepages: 13640,
		},
		{
			name:              "explicit override preserved",
			computeCores:      1,
			presetHugepages:   5000,
			expectedHugepages: 5000,
		},
		{
			name:              "mixed TLC/QLC ratio 1:1",
			containerCapacity: 5000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			driveTypesRatio:   &weka.DriveTypesRatio{Tlc: 1, Qlc: 1},
			// total=30000GiB, tlc=15000, qlc=15000
			// tlcMiB=15000*1024/1000=15360, qlcMiB=15000*1024/6000=2560
			// cluster=17920, /6=2986 + 1700=4686
			expectedHugepages: 4686,
		},
		{
			name:              "QLC-heavy ratio 1:10",
			containerCapacity: 10000,
			driveContainers:   6,
			computeContainers: 6,
			computeCores:      1,
			driveTypesRatio:   &weka.DriveTypesRatio{Tlc: 1, Qlc: 10},
			// total=60000GiB, tlc=60000/11=5454, qlc=54546
			// tlcMiB=5454*1024/1000=5584, qlcMiB=54546*1024/6000=9309
			// cluster=14893, /6=2482 + 1700=4182
			expectedHugepages: 4182,
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
				DriveTypesRatio:   tt.driveTypesRatio,
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
