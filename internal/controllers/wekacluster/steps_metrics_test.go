package wekacluster

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func driveContainer(mode string, containerCapacity, driveCapacity, numDrives int, ratio *weka.DriveTypesRatio) *weka.WekaContainer {
	c := &weka.WekaContainer{}
	c.Spec.Mode = mode
	c.Spec.ContainerCapacity = containerCapacity
	c.Spec.DriveCapacity = driveCapacity
	c.Spec.NumDrives = numDrives
	c.Spec.DriveTypesRatio = ratio
	return c
}

func TestClusterDriveCapacityColumn(t *testing.T) {
	tests := []struct {
		name       string
		containers []*weka.WekaContainer
		want       string
	}{
		{
			name: "container capacity tlc/qlc aggregated",
			containers: []*weka.WekaContainer{
				driveContainer(weka.WekaContainerModeDrive, 30000, 0, 0, &weka.DriveTypesRatio{Tlc: 1, Qlc: 1}),
				driveContainer(weka.WekaContainerModeDrive, 30000, 0, 0, &weka.DriveTypesRatio{Tlc: 1, Qlc: 1}),
			},
			want: "T/Q 29.3/29.3 TiB",
		},
		{
			name: "legacy drive capacity tlc only",
			containers: []*weka.WekaContainer{
				driveContainer(weka.WekaContainerModeDrive, 0, 1024, 2, nil),
			},
			want: "T 2.0TiB",
		},
		{
			name: "ignores compute and non-drive-sharing containers",
			containers: []*weka.WekaContainer{
				driveContainer(weka.WekaContainerModeCompute, 30000, 0, 0, nil),
				driveContainer(weka.WekaContainerModeDrive, 0, 0, 0, nil), // not drive-sharing
				driveContainer(weka.WekaContainerModeDrive, 10240, 0, 0, nil),
			},
			want: "T 10.0TiB",
		},
		{
			name:       "empty for non-drive-sharing cluster",
			containers: []*weka.WekaContainer{driveContainer(weka.WekaContainerModeCompute, 0, 0, 0, nil)},
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clusterDriveCapacityColumn(tt.containers); got != tt.want {
				t.Errorf("clusterDriveCapacityColumn() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEntityStatefulNumString(t *testing.T) {
	tests := []struct {
		name    string
		counter weka.EntityStatefulNum
		want    string
	}{
		{
			name:    "known desired keeps three-part format",
			counter: weka.EntityStatefulNum{Active: 5, Created: 5, Desired: 3},
			want:    "5/5/3",
		},
		{
			name:    "zero desired omits desired segment",
			counter: weka.EntityStatefulNum{Active: 5, Created: 5, Desired: 0},
			want:    "5/5",
		},
		{
			name:    "zero desired drives column",
			counter: weka.EntityStatefulNum{Active: 30, Created: 30, Desired: 0},
			want:    "30/30",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.counter.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
